package services

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type VideoService struct{}

const recentActiveFileThreshold = 5 * time.Minute

var tempVideoStemSuffixes = []string{
	".temp", "_temp", "-temp",
	".tmp", "_tmp", "-tmp",
}

// GetAllVideos 获取所有视频（已废弃，使用分页方式）
func (s *VideoService) GetAllVideos() ([]models.Video, error) {
	var videos []models.Video
	err := database.DB.Preload("Tags").Order("created_at desc").Limit(50).Find(&videos).Error
	return videos, err
}

// getPlayWeight 获取播放权重配置
func (s *VideoService) getPlayWeight() (float64, error) {
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		return 0, fmt.Errorf("获取设置失败: %w", err)
	}
	w := settings.PlayWeight
	if w < 0.1 {
		w = 0.1
	}
	return w, nil
}

// scoreExpr 返回播放分数的 SQL 表达式片段，使用 fmt.Sprintf 将权重直接嵌入 SQL，
// 避免在复合 WHERE 条件中反复传递 ? 占位符导致参数计数出错。
func scoreExpr(playWeight float64) string {
	return fmt.Sprintf("(play_count * %g + random_play_count)", playWeight)
}

// applyCursorCondition 为查询添加游标分页的 WHERE 条件
// 排序规则：score ASC, size DESC, id DESC
func applyCursorCondition(query *gorm.DB, scoreSql string, cursorScore float64, cursorSize int64, cursorID uint, tablePrefix string) *gorm.DB {
	if cursorID == 0 {
		return query
	}
	sizeCol := tablePrefix + "size"
	idCol := tablePrefix + "id"
	// 三元组游标条件：(score > ?) OR (score = ? AND size < ?) OR (score = ? AND size = ? AND id < ?)
	cond := fmt.Sprintf(
		"(%s > ?) OR (%s = ? AND %s < ?) OR (%s = ? AND %s = ? AND %s < ?)",
		scoreSql, scoreSql, sizeCol, scoreSql, sizeCol, idCol,
	)
	return query.Where(cond, cursorScore, cursorScore, cursorSize, cursorScore, cursorSize, cursorID)
}

// GetVideosPaginated 游标分页获取视频（按概率优先排序）
func (s *VideoService) GetVideosPaginated(cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	playWeight, err := s.getPlayWeight()
	if err != nil {
		return nil, err
	}

	var videos []models.Video
	scoreSql := scoreExpr(playWeight)
	query := database.DB.Preload("Tags").
		Order(clause.Expr{SQL: scoreSql + " ASC"}).
		Order("size desc").
		Order("id desc")

	query = applyCursorCondition(query, scoreSql, cursorScore, cursorSize, cursorID, "")

	err = query.Limit(limit).Find(&videos).Error
	return videos, err
}

// SearchVideos 搜索视频（按名称）- 支持分页（按概率优先排序）
func (s *VideoService) SearchVideos(keyword string, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	return s.SearchVideosWithFilters(keyword, nil, 0, 0, 0, 0, cursorScore, cursorSize, cursorID, limit)
}

// SearchVideosByTags 按标签搜索（多选 AND）- 支持分页（按概率优先排序）
func (s *VideoService) SearchVideosByTags(tagIDs []uint, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	return s.SearchVideosWithFilters("", tagIDs, 0, 0, 0, 0, cursorScore, cursorSize, cursorID, limit)
}

// SearchVideosWithFilters 组合搜索（关键词 + 标签 + 体积 + 分辨率 AND）- 支持分页（按概率优先排序）
func (s *VideoService) SearchVideosWithFilters(keyword string, tagIDs []uint, minSize, maxSize int64, minHeight, maxHeight int, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	var videos []models.Video
	playWeight, err := s.getPlayWeight()
	if err != nil {
		return nil, err
	}

	scoreSql := scoreExpr(playWeight)
	query := database.DB.Model(&models.Video{}).Preload("Tags").
		Order(clause.Expr{SQL: scoreSql + " ASC"}).
		Order("videos.size desc").
		Order("videos.id desc")

	if strings.TrimSpace(keyword) != "" {
		kw := "%" + strings.TrimSpace(keyword) + "%"
		query = query.Where("(videos.name LIKE ? OR videos.path LIKE ?)", kw, kw)
	}

	// 体积过滤 (左闭右开 [min, max) )
	if minSize > 0 {
		query = query.Where("videos.size >= ?", minSize)
	}
	if maxSize > 0 {
		query = query.Where("videos.size < ?", maxSize)
	}

	// 分辨率过滤 (按高度判断)
	if minHeight > 0 {
		query = query.Where("videos.height >= ?", minHeight)
	}
	if maxHeight > 0 {
		query = query.Where("videos.height <= ?", maxHeight)
	}

	if len(tagIDs) > 0 {
		query = query.Joins("JOIN video_tags ON video_tags.video_id = videos.id").
			Where("video_tags.tag_id IN ?", tagIDs)
		query = query.Group("videos.id").
			Having("COUNT(DISTINCT video_tags.tag_id) = ?", len(tagIDs))
	}

	query = applyCursorCondition(query, scoreSql, cursorScore, cursorSize, cursorID, "videos.")

	err = query.Limit(limit).Find(&videos).Error
	return videos, err
}

// getVideoMetadata 使用 ffprobe 获取视频时长、分辨率、宽、高
func (s *VideoService) getVideoMetadata(path string) (duration float64, resolution string, width, height int) {
	ffprobeBin, err := exec.LookPath("ffprobe")
	if err != nil {
		// 尝试常见安装路径 (Homebrew)
		if runtime.GOOS == "darwin" {
			paths := []string{"/opt/homebrew/bin/ffprobe", "/usr/local/bin/ffprobe"}
			for _, p := range paths {
				if _, err := os.Stat(p); err == nil {
					ffprobeBin = p
					break
				}
			}
		}
	}

	if ffprobeBin == "" {
		log.Printf("[VideoService] ffprobe not found, skipping metadata extraction")
		return 0, "", 0, 0
	}

	// 获取时长和分辨率 (JSON 格式)
	cmd := exec.Command(ffprobeBin, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,duration", "-of", "json", path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[VideoService] ffprobe failed for %s: %v", path, err)
		return 0, "", 0, 0
	}

	var data struct {
		Streams []struct {
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			Duration string `json:"duration"`
		} `json:"streams"`
	}

	if err := json.Unmarshal(output, &data); err != nil {
		log.Printf("[VideoService] failed to parse ffprobe output: %v", err)
		return 0, "", 0, 0
	}

	if len(data.Streams) > 0 {
		stream := data.Streams[0]
		width = stream.Width
		height = stream.Height
		if stream.Width > 0 && stream.Height > 0 {
			resolution = fmt.Sprintf("%dx%d", stream.Width, stream.Height)
		}
		if stream.Duration != "" {
			fmt.Sscanf(stream.Duration, "%f", &duration)
		}
	}

	return duration, resolution, width, height
}

// AddVideo 添加视频
func (s *VideoService) AddVideo(path string) (*models.Video, error) {
	path = filepath.Clean(strings.TrimSpace(path))

	// 检查文件是否存在
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("文件不存在: %w", err)
	}

	// 检查是否已存在
	var existingVideo models.Video
	if err := database.DB.Where("path = ?", path).First(&existingVideo).Error; err == nil {
		log.Printf("跳过已存在视频 path=%s", path)
		return &existingVideo, ErrVideoExists
	}

	duration, resolution, width, height := s.getVideoMetadata(path)

	video := &models.Video{
		Name:       filepath.Base(path),
		Path:       path,
		Directory:  filepath.Dir(path),
		Size:       info.Size(),
		Duration:   duration,
		Resolution: resolution,
		Width:      width,
		Height:     height,
	}

	err = database.DB.Create(video).Error
	if err != nil {
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "unique") || strings.Contains(errMsg, "constraint") {
			if findErr := database.DB.Where("path = ?", path).First(&existingVideo).Error; findErr == nil {
				return &existingVideo, ErrVideoExists
			}
		}
		return nil, err
	}
	log.Printf("新增视频 path=%s", path)
	return video, nil
}

// GetVideo 获取单个视频详情
func (s *VideoService) GetVideo(id uint) (*models.Video, error) {
	var video models.Video
	if err := database.DB.First(&video, id).Error; err != nil {
		return nil, err
	}
	return &video, nil
}

// DeleteVideo 删除视频
func (s *VideoService) DeleteVideo(id uint, deleteFile bool) error {
	var video models.Video
	if err := database.DB.First(&video, id).Error; err != nil {
		return err
	}

	// 如果需要删除原始文件，则先移动到回收站，为后续恢复能力留出基础。
	if deleteFile {
		trashPath, err := NewTrashService().MoveToTrash(video.Path)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("移动文件到回收站失败: %w", err)
		}
		if err == nil {
			log.Printf("视频已移入回收站 src=%s dst=%s", video.Path, trashPath)
		}
	}

	// 从数据库删除
	return database.DB.Delete(&video).Error
}

// AddTagToVideo 为视频添加标签
func (s *VideoService) AddTagToVideo(videoID uint, tagID uint) error {
	var video models.Video
	var tag models.Tag

	if err := database.DB.First(&video, videoID).Error; err != nil {
		return err
	}
	if err := database.DB.First(&tag, tagID).Error; err != nil {
		return err
	}

	return database.DB.Model(&video).Association("Tags").Append(&tag)
}

// RemoveTagFromVideo 移除视频的标签
func (s *VideoService) RemoveTagFromVideo(videoID uint, tagID uint) error {
	var video models.Video
	var tag models.Tag

	if err := database.DB.First(&video, videoID).Error; err != nil {
		return err
	}
	if err := database.DB.First(&tag, tagID).Error; err != nil {
		return err
	}

	return database.DB.Model(&video).Association("Tags").Delete(&tag)
}

// ScanDirectory 扫描目录获取视频文件
func (s *VideoService) ScanDirectory(dir string) ([]string, error) {
	files, err := s.ScanDirectoryWithInfo(dir)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	return paths, nil
}

// ScannedFile 扫描结果（附带文件大小，用于迁移检测）
type ScannedFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// ScanDirectoryWithInfo 扫描目录获取视频文件（附带文件大小）
func (s *VideoService) ScanDirectoryWithInfo(dir string) ([]ScannedFile, error) {
	var videoFiles []ScannedFile

	// 从设置中获取支持的视频格式
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		return nil, fmt.Errorf("获取设置失败: %w", err)
	}

	// 解析视频格式
	videoExts := strings.Split(settings.VideoExtensions, ",")
	if len(videoExts) == 1 && strings.TrimSpace(videoExts[0]) == "" {
		videoExts = strings.Split(".mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt", ",")
	}
	for i := range videoExts {
		videoExts[i] = strings.TrimSpace(videoExts[i])
		if videoExts[i] == "" {
			continue
		}
		if !strings.HasPrefix(videoExts[i], ".") {
			videoExts[i] = "." + videoExts[i]
		}
	}

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 跳过错误的文件
		}

		if shouldSkipHiddenPath(info) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			if isTrashDirName(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		if isTrashPath(path) || hasTempVideoSuffix(path) || isRecentlyActiveFile(info) {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		for _, videoExt := range videoExts {
			if ext == strings.ToLower(videoExt) {
				videoFiles = append(videoFiles, ScannedFile{Path: path, Size: info.Size()})
				break
			}
		}

		return nil
	})
	log.Printf("扫描目录完成 dir=%s files=%d", dir, len(videoFiles))

	return videoFiles, err
}

func shouldSkipHiddenPath(info os.FileInfo) bool {
	return info.Name() != "." && strings.HasPrefix(info.Name(), ".")
}

func isTrashDirName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), DefaultTrashDirName)
}

func isTrashPath(path string) bool {
	cleanPath := filepath.Clean(path)
	volume := filepath.VolumeName(cleanPath)
	trimmed := strings.TrimPrefix(cleanPath, volume)
	for _, part := range strings.Split(trimmed, string(os.PathSeparator)) {
		if isTrashDirName(part) {
			return true
		}
	}
	return false
}

func hasTempVideoSuffix(path string) bool {
	baseName := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(baseName))
	stem := strings.TrimSuffix(baseName, ext)
	for _, suffix := range tempVideoStemSuffixes {
		if stem == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(stem, suffix) {
			return true
		}
	}
	return false
}

func isRecentlyActiveFile(info os.FileInfo) bool {
	return time.Since(info.ModTime()) < recentActiveFileThreshold
}

// RelocateVideo 更新视频路径（文件迁移场景，保留标签等元数据）
func (s *VideoService) RelocateVideo(id uint, newPath string) error {
	newPath = filepath.Clean(strings.TrimSpace(newPath))

	// 验证新路径文件存在
	if _, err := os.Stat(newPath); err != nil {
		return fmt.Errorf("目标文件不存在: %w", err)
	}

	// 检查新路径是否已被其他记录占用
	var existing models.Video
	if err := database.DB.Where("path = ? AND id != ?", newPath, id).First(&existing).Error; err == nil {
		return fmt.Errorf("目标路径已被其他记录占用: %s", newPath)
	}

	// 迁移时也尝试重新提取元数据（可能之前的元数据是空的）
	duration, resolution, width, height := s.getVideoMetadata(newPath)

	result := database.DB.Model(&models.Video{}).Where("id = ?", id).Updates(map[string]interface{}{
		"path":       newPath,
		"directory":  filepath.Dir(newPath),
		"name":       filepath.Base(newPath),
		"duration":   duration,
		"resolution": resolution,
		"width":      width,
		"height":     height,
	})
	if result.Error != nil {
		return result.Error
	}
	log.Printf("视频迁移并更新元数据 id=%d newPath=%s duration=%.1f res=%s", id, newPath, duration, resolution)
	return nil
}

// RefreshVideoMetadata 刷新并修复视频的元数据
func (s *VideoService) RefreshVideoMetadata(id uint) error {
	var video models.Video
	if err := database.DB.First(&video, id).Error; err != nil {
		return err
	}

	duration, resolution, width, height := s.getVideoMetadata(video.Path)
	if duration == 0 && resolution == "" {
		return fmt.Errorf("未能从文件中提取有效元数据: %s", video.Path)
	}

	return database.DB.Model(&video).Updates(map[string]interface{}{
		"duration":   duration,
		"resolution": resolution,
		"width":      width,
		"height":     height,
	}).Error
}

// RenameVideo 重命名视频文件及数据库记录
func (s *VideoService) RenameVideo(id uint, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("文件名不能为空")
	}
	// 禁止路径分隔符
	if strings.ContainsAny(newName, "/\\") {
		return fmt.Errorf("文件名不能包含路径分隔符")
	}

	var video models.Video
	if err := database.DB.First(&video, id).Error; err != nil {
		return fmt.Errorf("视频不存在: %w", err)
	}

	// 保留原始扩展名（如果新名称没带扩展名）
	oldExt := filepath.Ext(video.Name)
	if filepath.Ext(newName) == "" {
		newName = newName + oldExt
	}

	oldPath := video.Path
	newPath := filepath.Join(video.Directory, newName)

	// 新旧路径相同则跳过
	if oldPath == newPath {
		return nil
	}

	// 检查目标路径是否已存在
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("目标文件已存在: %s", newName)
	}

	// 重命名磁盘文件
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("重命名文件失败: %w", err)
	}

	// 更新数据库记录
	if err := database.DB.Model(&video).Updates(map[string]interface{}{
		"name": newName,
		"path": newPath,
	}).Error; err != nil {
		// 回滚：将文件名改回去
		_ = os.Rename(newPath, oldPath)
		return fmt.Errorf("更新数据库失败: %w", err)
	}

	log.Printf("视频重命名 id=%d oldName=%s newName=%s", id, video.Name, newName)
	return nil
}

// GetVideosByDirectory 按目录获取视频记录
func (s *VideoService) GetVideosByDirectory(dir string) ([]models.Video, error) {
	var videos []models.Video
	cleanDir := filepath.Clean(strings.TrimSpace(dir))
	childPrefix := escapeSQLLike(cleanDir+string(os.PathSeparator)) + "%"
	err := database.DB.Preload("Tags").
		Where("directory = ? OR directory LIKE ? ESCAPE '\\'", cleanDir, childPrefix).
		Order("id desc").
		Find(&videos).Error
	return videos, err
}

func escapeSQLLike(input string) string {
	replacer := strings.NewReplacer(
		`\\`, `\\\\`,
		`%`, `\\%`,
		`_`, `\\_`,
	)
	return replacer.Replace(input)
}

// OpenDirectory 打开文件所在目录
func (s *VideoService) OpenDirectory(videoID uint) error {
	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		return err
	}

	return openPath(video.Directory, true)
}

// PlayVideo 使用系统默认播放器发起正式播放
func (s *VideoService) PlayVideo(videoID uint) (*PlaybackAttemptResult, error) {
	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		return nil, err
	}

	return s.dispatchFormalPlayback(&video, false)
}

// PlayRandomVideo 智能加权随机发起播放
func (s *VideoService) PlayRandomVideo() (*PlaybackAttemptResult, error) {
	// 获取播放权重配置
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		return nil, fmt.Errorf("获取设置失败: %w", err)
	}
	playWeight := settings.PlayWeight
	if playWeight < 0.1 {
		playWeight = 0.1
	}

	// 仅查询计算权重所需的最少字段，避免全量加载
	type videoScoreRow struct {
		ID              uint
		PlayCount       int
		RandomPlayCount int
	}
	var rows []videoScoreRow
	if err := database.DB.Model(&models.Video{}).
		Select("id, play_count, random_play_count").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	if len(rows) == 0 {
		return &PlaybackAttemptResult{
			DispatchSucceeded: false,
			ReasonCode:        "no_videos",
			UserMessage:       "随机播放失败：当前没有可播放的视频记录。",
		}, nil
	}

	// 计算每个视频的播放分数和最大分数
	scores := make([]float64, len(rows))
	maxScore := 0.0
	for i, r := range rows {
		scores[i] = float64(r.PlayCount)*playWeight + float64(r.RandomPlayCount)
		if scores[i] > maxScore {
			maxScore = scores[i]
		}
	}

	// 计算选择权重并做加权随机选择
	totalWeight := 0.0
	weights := make([]float64, len(rows))
	for i, score := range scores {
		weights[i] = maxScore - score + 1.0
		totalWeight += weights[i]
	}

	// 使用加权随机选择（Go 1.20+ 全局 rand 已自动 seed，无需手动调用）
	randomValue := rand.Float64() * totalWeight
	selectedIdx := len(rows) - 1 // 默认最后一个（防御浮点精度）
	cumulative := 0.0
	for i, w := range weights {
		cumulative += w
		if randomValue <= cumulative {
			selectedIdx = i
			break
		}
	}

	// 仅对选中的视频查询完整记录（含 Tags）
	var selectedVideo models.Video
	if err := database.DB.Preload("Tags").First(&selectedVideo, rows[selectedIdx].ID).Error; err != nil {
		return nil, fmt.Errorf("查询选中视频失败: %w", err)
	}

	// 使用数据库原子操作更新随机播放次数和最后播放时间
	return s.dispatchFormalPlayback(&selectedVideo, true)
}

var openWithDefaultFn = openPath

// openPath 使用系统默认方式打开路径（文件或目录）
// Windows 下目录用 explorer，文件用 cmd /c start；其他平台统一用 open/xdg-open
func openPath(path string, isDir bool) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		if isDir {
			cmd = exec.Command("explorer", path)
		} else {
			cmd = exec.Command("cmd", "/c", "start", "", path)
		}
	case "linux":
		cmd = exec.Command("xdg-open", path)
	default:
		return ErrUnsupportedOS
	}

	return cmd.Start()
}

func (s *VideoService) dispatchFormalPlayback(video *models.Video, random bool) (*PlaybackAttemptResult, error) {
	info, err := os.Stat(video.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return s.buildPlaybackFailureResult(video, "file_missing", "源文件不存在或已被移动。", true), nil
		}
		return s.buildPlaybackFailureResult(video, "path_unreadable", err.Error(), false), nil
	}
	if info.IsDir() {
		return s.buildPlaybackFailureResult(video, "path_is_directory", "当前路径不是可播放文件。", true), nil
	}

	if err := openWithDefaultFn(video.Path, false); err != nil {
		return s.buildPlaybackFailureResult(video, "dispatch_failed", err.Error(), false), nil
	}

	now := time.Now()
	updates := map[string]interface{}{
		"last_played_at": now,
		"is_stale":       false,
	}
	if random {
		updates["random_play_count"] = gorm.Expr("random_play_count + 1")
		video.RandomPlayCount++
	} else {
		updates["play_count"] = gorm.Expr("play_count + 1")
		video.PlayCount++
	}
	if err := database.DB.Model(video).Updates(updates).Error; err != nil {
		log.Printf("更新播放统计失败 id=%d err=%v", video.ID, err)
	}
	video.LastPlayedAt = &now
	video.IsStale = false

	return &PlaybackAttemptResult{
		Video:             video,
		DispatchSucceeded: true,
	}, nil
}

func (s *VideoService) buildPlaybackFailureResult(video *models.Video, reasonCode string, detail string, shouldReconcile bool) *PlaybackAttemptResult {
	result := &PlaybackAttemptResult{
		Video:             video,
		DispatchSucceeded: false,
		ReasonCode:        reasonCode,
		UserMessage:       fmt.Sprintf("播放失败: %s (%s)\n原因: %s", video.Name, video.Path, detail),
	}
	if shouldReconcile {
		result.ReconcileResult = s.reconcileAfterPlaybackFailure(video, reasonCode)
	}
	return result
}

func (s *VideoService) reconcileAfterPlaybackFailure(video *models.Video, reasonCode string) *PlaybackReconcileResult {
	result := &PlaybackReconcileResult{
		VideoID:    video.ID,
		ReasonCode: reasonCode,
	}

	if err := database.DB.Model(video).Update("is_stale", true).Error; err == nil {
		video.IsStale = true
		result.DidMarkStale = true
	}

	matchedPath, ambiguous, err := s.findRelocatedVideoCandidate(video)
	if err != nil {
		log.Printf("自动纠偏扫描失败 id=%d err=%v", video.ID, err)
		result.NeedsReload = true
		if updatedVideo, loadErr := s.GetVideo(video.ID); loadErr == nil {
			updatedVideo.IsStale = true
			result.UpdatedVideo = updatedVideo
		}
		return result
	}

	if ambiguous {
		result.NeedsReload = true
		if updatedVideo, loadErr := s.GetVideo(video.ID); loadErr == nil {
			result.UpdatedVideo = updatedVideo
		}
		return result
	}

	if matchedPath != "" && matchedPath != video.Path {
		if err := s.RelocateVideo(video.ID, matchedPath); err == nil {
			_ = database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Update("is_stale", false).Error
			if updatedVideo, loadErr := s.GetVideo(video.ID); loadErr == nil {
				updatedVideo.IsStale = false
				result.DidRelocate = true
				result.UpdatedVideo = updatedVideo
				return result
			}
		}
		result.NeedsReload = true
		return result
	}

	result.NeedsReload = true
	if updatedVideo, loadErr := s.GetVideo(video.ID); loadErr == nil {
		result.UpdatedVideo = updatedVideo
	}
	return result
}

func (s *VideoService) findRelocatedVideoCandidate(video *models.Video) (string, bool, error) {
	var directories []models.ScanDirectory
	if err := database.DB.Order("path asc").Find(&directories).Error; err != nil {
		return "", false, err
	}

	if len(directories) == 0 {
		return "", false, nil
	}

	primary := make([]string, 0, len(directories))
	secondary := make([]string, 0, len(directories))
	for _, dir := range directories {
		cleanPath := filepath.Clean(dir.Path)
		if cleanPath == "" {
			continue
		}
		prefix := cleanPath + string(os.PathSeparator)
		if video.Directory == cleanPath || strings.HasPrefix(video.Directory, prefix) {
			primary = append(primary, cleanPath)
		} else {
			secondary = append(secondary, cleanPath)
		}
	}

	roots := append(primary, secondary...)
	seenCandidates := map[string]struct{}{}
	for _, root := range roots {
		scannedFiles, err := s.ScanDirectoryWithInfo(root)
		if err != nil {
			return "", false, err
		}
		for _, candidate := range scannedFiles {
			if filepath.Base(candidate.Path) != video.Name {
				continue
			}
			if candidate.Size != video.Size {
				continue
			}
			seenCandidates[candidate.Path] = struct{}{}
			if len(seenCandidates) > 1 {
				return "", true, nil
			}
		}
	}

	for candidatePath := range seenCandidates {
		return candidatePath, false, nil
	}

	return "", false, nil
}
