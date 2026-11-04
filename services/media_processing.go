package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const processingMaxBytes int64 = 128 << 20
const processingMaxPixels int64 = 50_000_000
const processingMaxEditedPixels int64 = 70_000_000

// MediaProcessingRequest 只引用库内媒体或本服务的来源快照，不接受源文件路径。
type MediaProcessingRequest struct {
	Kind            string  `json:"kind"`
	MediaID         uint    `json:"media_id"`
	Operation       string  `json:"operation"`
	ParentID        string  `json:"parent_id"`
	PNGBase64       string  `json:"png_base64"`
	Profile         string  `json:"profile"`
	X               int     `json:"x"`
	Y               int     `json:"y"`
	Width           int     `json:"width"`
	Height          int     `json:"height"`
	StartSeconds    float64 `json:"start_seconds"`
	DurationSeconds float64 `json:"duration_seconds"`
}

// MediaProcessingView 不暴露磁盘路径；来源与结果均用不可猜测的会话资源读取。
type MediaProcessingView struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	Error         string `json:"error"`
	Name          string `json:"name"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	SourceBytes   int64  `json:"source_bytes"`
	OutputBytes   int64  `json:"output_bytes"`
	SourceURL     string `json:"source_url"`
	ResultURL     string `json:"result_url"`
	ComparisonURL string `json:"comparison_url"`
	Format        string `json:"format"`
}

type processingSource struct {
	kind               string
	id                 uint
	path, name, format string
	width, height      int
	info               os.FileInfo
}
type processingEntry struct {
	view                    MediaProcessingView
	source                  processingSource
	output, comparison, dir string
	ctx                     context.Context
	cancel                  context.CancelFunc
	released                bool
	wg                      sync.WaitGroup
	dirty                   bool
	dirtyRevision           uint64
	retainedBytes           int64
}

// MediaProcessingService 唯一持有临时处理、取消与资源租约；不写片库或持久超分任务。
type MediaProcessingService struct {
	mu          sync.Mutex
	entries     map[string]*processingEntry
	leases      map[string]func()
	closed      bool
	wg          sync.WaitGroup
	stopDone    chan struct{}
	db          *gorm.DB
	dir         string
	slot        *MediaWorkSlot
	enhancement *EnhancementService
	registry    *BackgroundTaskRegistry
	parent      context.Context
	resolve     func(context.Context, string, uint) (processingSource, error)
	recovered   bool // 只由持有slot的处理worker读写。
}

// NewMediaProcessingService 的parent是应用生命周期，slot沿用全应用重媒体槽。
func NewMediaProcessingService(parent context.Context, dataDir string, slot *MediaWorkSlot, enhancement *EnhancementService) *MediaProcessingService {
	if parent == nil {
		parent = context.Background()
	}
	if slot == nil {
		slot = NewMediaWorkSlot()
	}
	s := &MediaProcessingService{entries: map[string]*processingEntry{}, leases: map[string]func(){}, db: database.DB, dir: filepath.Join(dataDir, "processing-previews"), slot: slot, enhancement: enhancement, parent: parent}
	s.resolve = loadProcessingSource
	return s
}

func loadProcessingSource(ctx context.Context, kind string, id uint) (processingSource, error) {
	if id == 0 {
		return processingSource{}, errors.New("processing_invalid: 请选择媒体")
	}
	var source processingSource
	if kind == "image" {
		var img models.Image
		db := database.DB.WithContext(ctx)
		if err := applyImageVisibility(db.Model(&models.Image{}), db).First(&img, id).Error; err != nil {
			return source, err
		}
		source = processingSource{kind: kind, id: id, path: img.Path, name: img.Name, format: img.Format, width: img.Width, height: img.Height}
	} else if kind == "video" {
		return loadProcessingVideo(ctx, id)
	} else {
		return source, errors.New("processing_invalid: 媒体类型无效")
	}
	info, err := os.Stat(source.path)
	if err != nil {
		return source, errors.New("processing_source_unavailable: 源文件不可用")
	}
	if !info.Mode().IsRegular() {
		return source, errors.New("processing_source_unavailable: 来源不是普通文件")
	}
	if source.kind == "image" {
		if info.Size() > processingMaxBytes {
			return source, errors.New("processing_too_large: 原图超过128 MiB处理上限")
		}
		file, openErr := os.Open(source.path)
		if openErr != nil {
			return source, openErr
		}
		config, _, configErr := image.DecodeConfig(io.LimitReader(file, processingMaxBytes))
		file.Close()
		if configErr == nil {
			source.width, source.height = config.Width, config.Height
		} else {
			if imageDecoderForFormatAndPath(source.format, source.path) == imageDecoderSips {
				source.width, source.height, err = sipsProbeDimensions(ctx, source.path)
			} else {
				source.width, source.height, err = probeImageDimensionsFFprobe(ctx, source.path)
			}
			if err != nil {
				return source, errors.New("processing_source_unavailable: 无法核对原图尺寸")
			}
		}
	}
	source.info = info
	return source, nil
}

func loadProcessingVideo(ctx context.Context, id uint) (processingSource, error) {
	query, err := visibleVideoQuery(database.DB.WithContext(ctx).Model(&models.Video{}))
	if err != nil {
		return processingSource{}, err
	}
	var video models.Video
	if err = query.Where("videos.id = ?", id).First(&video).Error; err != nil {
		return processingSource{}, err
	}
	info, err := os.Stat(video.Path)
	if err != nil || !info.Mode().IsRegular() {
		return processingSource{}, errors.New("processing_source_unavailable: 原片不可用")
	}
	return processingSource{kind: "video", id: id, path: video.Path, name: video.Name, format: "mp4", width: video.Width, height: video.Height, info: info}, nil
}

// Begin 冻结来源身份；inspect只建立只读来源，其他操作经有界槽异步执行。
func (s *MediaProcessingService) Begin(ctx context.Context, request MediaProcessingRequest) (*MediaProcessingView, error) {
	if request.Operation != "inspect" && request.Operation != "edit" && request.Operation != "compress" && request.Operation != "enhance" && request.Operation != "sample" {
		return nil, errors.New("processing_invalid: 操作无效")
	}
	if request.Operation != "inspect" && request.ParentID == "" {
		return nil, errors.New("processing_invalid: 请先核对来源")
	}
	if request.Operation == "sample" && request.Kind != "video" || (request.Operation == "edit" || request.Operation == "compress" || request.Operation == "enhance") && request.Kind != "image" {
		return nil, errors.New("processing_invalid: 操作与媒体类型不符")
	}
	if int64(len(request.PNGBase64)) > (processingMaxBytes*4/3)+8 {
		return nil, errors.New("processing_too_large: 导出图片超过大小上限")
	}
	release, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	source, err := s.resolve(ctx, request.Kind, request.MediaID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.db != database.DB {
		return nil, errors.New("processing_stopped: 处理已停止或片库已切换")
	}
	if len(s.entries) >= 8 {
		return nil, errors.New("processing_limit: 请先关闭其他处理窗口")
	}
	if request.ParentID != "" {
		parent := s.entries[request.ParentID]
		if parent == nil || parent.released || !sameProcessingSource(source, parent.source) {
			return nil, errors.New("processing_source_changed: 来源已变化，请重新打开图片")
		}
	}
	if source.kind == "image" && (source.info.Size() > processingMaxBytes || int64(source.width)*int64(source.height) > processingMaxPixels) {
		return nil, errors.New("processing_too_large: 图片超出本次处理上限（128 MiB、5000万像素）")
	}
	id := uuid.NewString()
	workCtx, cancel := context.WithCancel(s.parent)
	entry := &processingEntry{source: source, ctx: workCtx, cancel: cancel, view: MediaProcessingView{ID: id, State: "ready", Name: source.name, Width: source.width, Height: source.height, SourceBytes: source.info.Size(), SourceURL: "/preview/processing/" + id + "/source", Format: source.format}}
	s.entries[id] = entry
	if request.Operation != "inspect" {
		entry.view.State = "queued"
		entry.wg.Add(1)
		s.wg.Add(1)
		go s.run(entry, request)
	}
	view := entry.view
	return &view, nil
}

func sameProcessingSource(a, b processingSource) bool {
	return a.kind == b.kind && a.id == b.id && a.path == b.path && os.SameFile(a.info, b.info) && a.info.Size() == b.info.Size() && a.info.ModTime().Equal(b.info.ModTime())
}
func (s *MediaProcessingService) checkSource(ctx context.Context, entry *processingEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.db != database.DB {
		return errors.New("processing_source_changed: 片库已切换")
	}
	current, err := s.resolve(ctx, entry.source.kind, entry.source.id)
	if err != nil {
		return err
	}
	if !sameProcessingSource(current, entry.source) {
		return errors.New("processing_source_changed: 源文件已变化，请重新打开")
	}
	return nil
}

func (s *MediaProcessingService) run(entry *processingEntry, request MediaProcessingRequest) {
	defer s.wg.Done()
	defer entry.wg.Done()
	s.mu.Lock()
	registry := s.registry
	s.mu.Unlock()
	if registry != nil {
		registry.Begin(BackgroundTaskMediaProcessing)
		defer registry.End(BackgroundTaskMediaProcessing)
	}
	ctx, cancel := context.WithTimeout(entry.ctx, 10*time.Minute)
	defer cancel()
	err := s.slot.Acquire(ctx)
	if err == nil {
		defer s.slot.Release()
		s.mu.Lock()
		entry.view.State = "running"
		s.mu.Unlock()
		err = s.checkSource(ctx, entry)
		if err == nil && !s.recovered {
			err = recoverProcessingPreviews(ctx, s.dir)
			if err == nil {
				s.recovered = true
			}
		}
		if err == nil {
			err = os.MkdirAll(s.dir, 0700)
		}
		if err == nil {
			entry.dir, err = os.MkdirTemp(s.dir, "preview-*")
		}
		if err == nil {
			owner, _ := json.Marshal(struct {
				Version int `json:"version"`
				PID     int `json:"pid"`
			}{1, os.Getpid()})
			err = os.WriteFile(filepath.Join(entry.dir, ".owner.json"), owner, 0600)
		}
		if err == nil {
			switch request.Operation {
			case "edit":
				err = s.acceptEditedPNG(entry, request.PNGBase64)
			case "compress":
				err = s.compressImage(ctx, entry)
			case "enhance":
				err = s.enhanceImage(ctx, entry, request)
			case "sample":
				err = s.enhanceSample(ctx, entry, request)
			}
		}
		if err == nil {
			err = s.checkSource(ctx, entry)
		}
	}
	var outputBytes, retainedBytes int64
	if err == nil && entry.output != "" {
		for _, path := range []string{entry.output, entry.comparison} {
			if path == "" {
				continue
			}
			info, statErr := os.Stat(path)
			if statErr != nil || info.Size() <= 0 || info.Size() > processingMaxBytes*2 {
				err = errors.New("processing_output_invalid: 输出无效或超出大小上限")
				break
			}
			retainedBytes += info.Size()
			if path == entry.output {
				outputBytes = info.Size()
			}
		}
	}
	s.mu.Lock()
	if entry.ctx.Err() != nil {
		entry.view.State = "cancelled"
		entry.view.Error = "处理已取消"
	} else if err != nil {
		entry.view.State = "failed"
		entry.view.Error = WithoutAbsolutePaths(err).Error()
	} else if entry.output == "" {
		entry.view.State = "not_smaller"
		entry.view.Error = "没有进一步无损压缩空间，原文件保持不变"
	} else {
		total := retainedBytes
		for _, other := range s.entries {
			if other != entry {
				total += other.retainedBytes
			}
		}
		if total > 1<<30 {
			entry.view.State = "failed"
			entry.view.Error = "processing_limit: 临时结果已达到1 GiB，请先关闭其他处理窗口"
		} else {
			entry.retainedBytes = retainedBytes
			entry.view.State = "ready"
			entry.view.OutputBytes = outputBytes
			entry.view.ResultURL = "/preview/processing/" + entry.view.ID + "/result"
			if entry.comparison != "" {
				entry.view.ComparisonURL = "/preview/processing/" + entry.view.ID + "/comparison"
			}
		}
	}
	keep := entry.view.State == "ready"
	s.mu.Unlock()
	if !keep && entry.dir != "" {
		_ = os.RemoveAll(entry.dir)
	}
}

// recoverProcessingPreviews 只回收带本模块标记且已证明原进程退出的目录。
func recoverProcessingPreviews(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "preview-") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, readErr := os.ReadFile(filepath.Join(path, ".owner.json"))
		if readErr != nil {
			continue
		}
		var owner struct {
			Version int `json:"version"`
			PID     int `json:"pid"`
		}
		if json.Unmarshal(raw, &owner) != nil || owner.Version != 1 || owner.PID < 1 || !processingOwnerGone(owner.PID) {
			continue
		}
		if err = os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

// SetBackgroundTaskRegistry 复用原任务登记表，不建立第二套运行事实。
func (s *MediaProcessingService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
}

// ActiveViews 提供任务/退出守卫所需的内存状态，dirty修订不依赖数据库。
func (s *MediaProcessingService) ActiveViews(includeDirty bool) []MediaProcessingView {
	s.mu.Lock()
	defer s.mu.Unlock()
	views := []MediaProcessingView{}
	for _, entry := range s.entries {
		if entry.view.State == "queued" || entry.view.State == "running" || !entry.released && includeDirty && entry.dirty {
			views = append(views, entry.view)
		}
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	return views
}

// SetDirty 拒绝迟到修订，避免导出完成回包清掉后续编辑的退出提示。
func (s *MediaProcessingService) SetDirty(id string, revision uint64, dirty bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[id]
	if entry == nil || entry.released || s.closed {
		return errors.New("processing_expired")
	}
	if revision > entry.dirtyRevision {
		entry.dirtyRevision = revision
		entry.dirty = dirty
	}
	return nil
}

// ExportDirectory 仅供原生保存框建议与原图同目录，不改变扫描目录配置。
func (s *MediaProcessingService) ExportDirectory(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[id]
	if entry == nil || entry.released || s.closed {
		return "", errors.New("processing_expired")
	}
	return filepath.Dir(entry.source.path), nil
}

func (s *MediaProcessingService) acceptEditedPNG(entry *processingEntry, encoded string) error {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 || int64(len(raw)) > processingMaxBytes {
		return errors.New("processing_invalid: PNG数据无效或过大")
	}
	config, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || int64(config.Width)*int64(config.Height) > processingMaxEditedPixels {
		return errors.New("processing_invalid: PNG尺寸无效或过大")
	}
	if _, err = png.Decode(bytes.NewReader(raw)); err != nil {
		return errors.New("processing_invalid: PNG数据不完整")
	}
	entry.output = filepath.Join(entry.dir, "edited.png")
	if err = os.WriteFile(entry.output, raw, 0600); err != nil {
		return err
	}
	s.mu.Lock()
	entry.view.Width = config.Width
	entry.view.Height = config.Height
	entry.view.Format = "png"
	s.mu.Unlock()
	return nil
}

// Get 读取会话快照，未知或已释放请求不能访问新会话。
func (s *MediaProcessingService) Get(id string) (*MediaProcessingView, error) {
	s.mu.Lock()
	e := s.entries[id]
	if e == nil || e.released || s.closed {
		s.mu.Unlock()
		return nil, errors.New("processing_expired: 处理会话已关闭")
	}
	view := e.view
	s.mu.Unlock()
	if view.State == "ready" {
		if err := s.checkSource(e.ctx, e); err != nil {
			return nil, err
		}
	}
	return &view, nil
}

// Release 先取消并等待真实停止，再回收文件；等待失败保留对象供重试。
func (s *MediaProcessingService) Release(ctx context.Context, id string) error {
	s.mu.Lock()
	e := s.entries[id]
	if e == nil {
		s.mu.Unlock()
		return nil
	}
	e.released = true
	e.cancel()
	closers := s.entryClosersLocked(id)
	s.mu.Unlock()
	for _, closeLease := range closers {
		closeLease()
	}
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	s.mu.Lock()
	delete(s.entries, id)
	s.mu.Unlock()
	if e.dir != "" {
		return os.RemoveAll(e.dir)
	}
	return nil
}

func (s *MediaProcessingService) entryClosersLocked(id string) []func() {
	var result []func()
	for key, closer := range s.leases {
		if len(key) > len(id) && key[:len(id)] == id {
			result = append(result, closer)
		}
	}
	return result
}

// StopAndWait 在拿全局维护锁之前调用，自身关门后不再增加WaitGroup。
func (s *MediaProcessingService) StopAndWait(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	for _, e := range s.entries {
		e.released = true
		e.cancel()
	}
	var closers []func()
	for _, closer := range s.leases {
		closers = append(closers, closer)
	}
	if s.stopDone == nil {
		s.stopDone = make(chan struct{})
		go func(done chan struct{}) { s.wg.Wait(); close(done) }(s.stopDone)
	}
	done := s.stopDone
	s.mu.Unlock()
	for _, closer := range closers {
		closer()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	s.mu.Lock()
	entries := make(map[string]*processingEntry, len(s.entries))
	for id, e := range s.entries {
		entries[id] = e
	}
	s.mu.Unlock()
	for id, e := range entries {
		if e.dir != "" {
			if err := os.RemoveAll(e.dir); err != nil {
				return err
			}
		}
		s.mu.Lock()
		delete(s.entries, id)
		s.mu.Unlock()
	}
	return nil
}

// Resume 仅在已排空且仍使用原库时恢复请求接收。
func (s *MediaProcessingService) Resume() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != database.DB {
		return errors.New("processing_source_changed: 片库已切换")
	}
	if s.stopDone != nil {
		select {
		case <-s.stopDone:
		default:
			return errors.New("processing_stopping: 处理尚未停止")
		}
	}
	s.closed = false
	s.stopDone = nil
	return nil
}

// OpenAsset 返回受控文件租约；释放/维护能主动关闭它，解除全局路径读锁。
func (s *MediaProcessingService) OpenAsset(ctx context.Context, id, part string) (*os.File, func(), error) {
	release, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	e := s.entries[id]
	if e == nil || e.released || s.closed {
		s.mu.Unlock()
		release()
		return nil, nil, errors.New("processing_expired")
	}
	path := e.source.path
	if part == "result" && e.view.State == "ready" && e.output != "" {
		path = e.output
	} else if part == "comparison" && e.view.State == "ready" && e.comparison != "" {
		path = e.comparison
	} else if part != "source" {
		s.mu.Unlock()
		release()
		return nil, nil, errors.New("processing_expired")
	}
	e.wg.Add(1)
	s.wg.Add(1)
	s.mu.Unlock()
	failed := func() { e.wg.Done(); s.wg.Done(); release() }
	if err = s.checkSource(ctx, e); err != nil {
		failed()
		return nil, nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		failed()
		return nil, nil, err
	}
	if part == "source" {
		info, statErr := file.Stat()
		if statErr != nil || !os.SameFile(info, e.source.info) || info.Size() != e.source.info.Size() || !info.ModTime().Equal(e.source.info.ModTime()) {
			file.Close()
			failed()
			return nil, nil, errors.New("processing_source_changed")
		}
	}
	key := id + ":" + uuid.NewString()
	var once sync.Once
	closeLease := func() {
		once.Do(func() {
			_ = file.Close()
			release()
			s.mu.Lock()
			delete(s.leases, key)
			s.mu.Unlock()
			e.wg.Done()
			s.wg.Done()
		})
	}
	s.mu.Lock()
	if e.released || s.closed {
		s.mu.Unlock()
		closeLease()
		return nil, nil, errors.New("processing_expired")
	}
	s.leases[key] = closeLease
	s.mu.Unlock()
	return file, closeLease, nil
}

// Save 以不覆盖方式发布副本，取消和源变化时不发布文件。
func (s *MediaProcessingService) Save(ctx context.Context, id, target string) error {
	s.mu.Lock()
	e := s.entries[id]
	if e == nil || e.released || s.closed || e.view.State != "ready" || e.output == "" {
		s.mu.Unlock()
		return errors.New("processing_expired: 没有可保存结果")
	}
	e.wg.Add(1)
	s.wg.Add(1)
	sourcePath := e.output
	s.mu.Unlock()
	defer e.wg.Done()
	defer s.wg.Done()
	operationCtx, operationCancel := context.WithCancel(e.ctx)
	defer operationCancel()
	stopCaller := context.AfterFunc(ctx, operationCancel)
	defer stopCaller()
	release, err := rLockLibraryPathsContext(operationCtx)
	if err != nil {
		return err
	}
	defer release()
	if err = s.checkSource(e.ctx, e); err != nil {
		return err
	}
	if _, err = os.Lstat(target); !os.IsNotExist(err) {
		return errors.New("processing_target_exists: 目标已存在，请另选文件名；不会覆盖原图或其他文件")
	}
	if filepath.Ext(target) != "."+e.view.Format {
		return fmt.Errorf("processing_invalid: 请使用.%s扩展名", e.view.Format)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return err
	}
	root, rootErr := filepath.EvalSymlinks(s.dir)
	if rootErr == nil {
		relative, relErr := filepath.Rel(root, dir)
		if relErr == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return errors.New("processing_invalid: 请将副本保存到临时处理目录之外")
		}
	}
	destination := filepath.Join(dir, filepath.Base(target))
	input, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer input.Close()
	temp, err := os.CreateTemp(dir, ".cineinsight-image-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	buffer := make([]byte, 64<<10)
	for {
		if err = e.ctx.Err(); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		n, readErr := input.Read(buffer)
		if n > 0 {
			if _, err = temp.Write(buffer[:n]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = s.checkSource(e.ctx, e); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.released || s.closed || e.ctx.Err() != nil {
		return errors.New("processing_cancelled: 保存已取消")
	}
	if err = editRenameNoReplace(temp.Name(), destination); err != nil {
		return errors.New("processing_publish_failed: 无法保存副本，请检查文件名、空间和目录权限")
	}
	return nil
}
