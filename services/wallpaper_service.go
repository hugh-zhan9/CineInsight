package services

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"video-master/database"
	"video-master/models"
)

const (
	wallpaperMinLongEdge  = 1920
	wallpaperMinShortEdge = 720
	wallpaperMaxImageSize = 64 << 20
)

// WallpaperPreflight 是无副作用的壁纸资格检查，不把分辨率当作画面锐度评分。
type WallpaperPreflight struct {
	Eligible   bool   `json:"eligible"`
	ReasonCode string `json:"reason_code"`
	Message    string `json:"message"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
}

// WallpaperStatus 只报告本应用拥有的动态壁纸，不尝试追踪用户在系统设置里的更改。
type WallpaperStatus struct {
	Supported   bool   `json:"supported"`
	State       string `json:"state"`
	VideoID     uint   `json:"video_id"`
	DisplayName string `json:"display_name"`
	Message     string `json:"message"`
}

type wallpaperMedia struct {
	path, name    string
	width, height int
	stale         bool
}

type wallpaperNativeStatus struct{ state, message string }

// wallpaperPlatform 隔离 AppKit 的主线程对象；测试使用模拟器，不修改真实桌面。
type wallpaperPlatform interface {
	Available() bool
	Inspect(path, kind string) (int, int, error)
	SetImage(path string) error // 成功后在同一个主线程操作内停止动态播放。
	StartVideo(path string) error
	Status() wallpaperNativeStatus
	Stop() error
	Close()
}

// WallpaperService 拥有唯一的壁纸播放会话。设置、停止和退出由同一互斥锁串行化。
// 不经过 VideoService 播放入口，因此不会写播放次数、已看或断点。
type WallpaperService struct {
	mu      sync.Mutex
	native  wallpaperPlatform
	dataDir string
	load    func(string, uint) (wallpaperMedia, error)
	videoID uint
	name    string
	closed  bool
}

// NewWallpaperService 构造壁纸服务；只有用户设置壁纸时才创建原生窗口。
func NewWallpaperService(dataDir string) *WallpaperService {
	return &WallpaperService{native: newWallpaperPlatform(), dataDir: dataDir, load: loadWallpaperMedia}
}

func loadWallpaperMedia(kind string, id uint) (wallpaperMedia, error) {
	if database.DB == nil {
		return wallpaperMedia{}, fmt.Errorf("片库尚未就绪")
	}
	if kind == "video" {
		var video models.Video
		err := database.DB.Select("id", "name", "display_title", "path", "width", "height", "is_stale").First(&video, id).Error
		name := video.DisplayTitle
		if name == "" {
			name = video.Name
		}
		return wallpaperMedia{video.Path, name, video.Width, video.Height, video.IsStale}, err
	}
	var img models.Image
	err := database.DB.Select("id", "name", "path", "width", "height", "is_stale").First(&img, id).Error
	return wallpaperMedia{img.Path, img.Name, img.Width, img.Height, img.IsStale}, err
}

func wallpaperResolutionOK(width, height int) bool {
	return min(width, height) >= wallpaperMinShortEdge && max(width, height) >= wallpaperMinLongEdge
}

func wallpaperResolutionMessage(width, height int) string {
	return fmt.Sprintf("素材分辨率 %d×%d 不足，壁纸要求长边至少 %d、短边至少 %d 像素。", width, height, wallpaperMinLongEdge, wallpaperMinShortEdge)
}

func (s *WallpaperService) preflightLocked(kind string, id uint) (WallpaperPreflight, wallpaperMedia, os.FileInfo) {
	result := WallpaperPreflight{}
	reject := func(code, message string) (WallpaperPreflight, wallpaperMedia, os.FileInfo) {
		result.ReasonCode, result.Message = code, message
		return result, wallpaperMedia{}, nil
	}
	if s.closed || !s.native.Available() {
		return reject("unsupported_platform", "桌面壁纸需要 macOS 14 或更新版本，并使用包含原生支持的应用。")
	}
	if id == 0 || (kind != "image" && kind != "video") {
		return reject("invalid_media", "请选择图片或视频。")
	}
	media, err := s.load(kind, id)
	if err != nil || media.stale {
		return reject("media_unavailable", "素材已删除、失效或暂时无法读取。")
	}
	result.Width, result.Height = media.width, media.height
	info, err := os.Stat(media.path)
	if os.IsNotExist(err) {
		return reject("file_missing", "原文件不存在，请连接所在磁盘或重新定位文件。")
	}
	if err != nil || !info.Mode().IsRegular() {
		return reject("file_unreadable", "无法读取原文件，请检查磁盘和文件权限。")
	}
	if media.width <= 0 || media.height <= 0 {
		return reject("dimensions_unknown", "素材尺寸尚未探测，请先重新读取技术信息或加载图片预览。")
	}
	if !wallpaperResolutionOK(media.width, media.height) {
		return reject("resolution_too_low", wallpaperResolutionMessage(media.width, media.height))
	}
	if kind == "image" && info.Size() > wallpaperMaxImageSize {
		return reject("file_unreadable", "壁纸图片不能超过 64 MiB。")
	}
	result.Eligible = true
	return result, media, info
}

// Preflight 检查数据库尺寸和原文件可用性，不启动播放器或改变桌面。
func (s *WallpaperService) Preflight(kind string, id uint) WallpaperPreflight {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, _, _ := s.preflightLocked(kind, id)
	return result
}

// Set 重新检查真实素材后设置壁纸；检查失败保留正在使用的壁纸。
func (s *WallpaperService) Set(kind string, id uint) (WallpaperStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	check, media, before := s.preflightLocked(kind, id)
	if !check.Eligible {
		return s.statusLocked(), fmt.Errorf("%s", check.Message)
	}
	width, height, err := s.native.Inspect(media.path, kind)
	if err != nil {
		return s.statusLocked(), err
	}
	if !wallpaperResolutionOK(width, height) {
		return s.statusLocked(), fmt.Errorf("%s", wallpaperResolutionMessage(width, height))
	}
	if !wallpaperSourceUnchanged(media.path, before) {
		return s.statusLocked(), fmt.Errorf("原文件在检查期间发生变化，请重新读取素材信息后再试。")
	}
	if kind == "image" {
		path, err := s.copyImage(media.path, before)
		if err != nil {
			return s.statusLocked(), err
		}
		if err := s.native.SetImage(path); err != nil {
			return s.statusLocked(), err
		}
		s.videoID, s.name = 0, ""
	} else {
		if err := s.native.StartVideo(media.path); err != nil {
			return s.statusLocked(), err
		}
		s.videoID, s.name = id, media.name
	}
	return s.statusLocked(), nil
}

func wallpaperSourceUnchanged(path string, before os.FileInfo) bool {
	after, err := os.Stat(path)
	return err == nil && os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

func (s *WallpaperService) copyImage(path string, before os.FileInfo) (string, error) {
	if !filepath.IsAbs(s.dataDir) {
		return "", fmt.Errorf("应用数据目录不可用，无法保存壁纸副本。")
	}
	dir := filepath.Join(s.dataDir, "wallpapers")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("无法创建壁纸副本目录，请检查磁盘空间和权限。")
	}
	source, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("无法读取壁纸原图。")
	}
	defer source.Close()
	tmp, err := os.CreateTemp(dir, ".wallpaper-*")
	if err != nil {
		return "", fmt.Errorf("无法保存壁纸副本，请检查磁盘空间和权限。")
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(source, wallpaperMaxImageSize+1))
	if err != nil || written != before.Size() || written > wallpaperMaxImageSize || !wallpaperSourceUnchanged(path, before) {
		return "", fmt.Errorf("壁纸副本未保存：原文件发生变化或磁盘写入失败。")
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("壁纸副本写入失败，请检查磁盘空间。")
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("壁纸副本写入失败。")
	}
	target := filepath.Join(dir, fmt.Sprintf("%x%s", hash.Sum(nil), strings.ToLower(filepath.Ext(path))))
	if err := os.Rename(tmp.Name(), target); err != nil {
		return "", fmt.Errorf("无法发布壁纸副本。")
	}
	return target, nil
}

func (s *WallpaperService) statusLocked() WallpaperStatus {
	if s.closed {
		return WallpaperStatus{State: "idle"}
	}
	native := s.native.Status()
	return WallpaperStatus{s.native.Available(), native.state, s.videoID, s.name, native.message}
}

// Status 返回原生播放器的实际状态，包括异步解码失败。
func (s *WallpaperService) Status() WallpaperStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

// Stop 幂等停止动态视频，不改系统静态壁纸。
func (s *WallpaperService) Stop() (WallpaperStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		if err := s.native.Stop(); err != nil {
			return s.statusLocked(), err
		}
		s.videoID, s.name = 0, ""
	}
	return s.statusLocked(), nil
}

// Close 释放原生对象，阻止退出期间又开始播放。
func (s *WallpaperService) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.native.Close()
		s.closed = true
		s.videoID, s.name = 0, ""
	}
}
