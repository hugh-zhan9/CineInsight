package services

import (
	"errors"
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

	"gorm.io/gorm/clause"
)

type VideoService struct{}

// GetAllVideos 获取所有视频（已废弃，使用分页方式）
func (s *VideoService) GetAllVideos() ([]models.Video, error) {
	var videos []models.Video
	err := database.DB.Preload("Tags").Order("created_at desc").Limit(50).Find(&videos).Error
	return videos, err
}

// GetVideosPaginated 游标分页获取视频（按概率优先排序）
func (s *VideoService) GetVideosPaginated(cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		return nil, fmt.Errorf("获取设置失败: %w", err)
	}
	playWeight := settings.PlayWeight
	if playWeight < 0.1 {
		playWeight = 0.1
	}

	var videos []models.Video
	scoreSQL := "play_count * ? + random_play_count"
	query := database.DB.Preload("Tags").
		Order(clause.Expr{SQL: scoreSQL + " ASC", Vars: []interface{}{playWeight}}).
		Order("size desc").
		Order("id desc")

	if cursorID > 0 {
		query = query.Where("("+scoreSQL+" > ?) OR ("+scoreSQL+" = ? AND size < ?) OR ("+scoreSQL+" = ? AND size = ? AND id < ?)",
			playWeight, cursorScore,
			playWeight, cursorScore, cursorSize,
			playWeight, cursorScore, cursorSize, cursorID,
		)
	}

	err := query.Limit(limit).Find(&videos).Error
	return videos, err
}

// SearchVideos 搜索视频（按名称）- 支持分页（按概率优先排序）
func (s *VideoService) SearchVideos(keyword string, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	return s.SearchVideosWithFilters(keyword, nil, cursorScore, cursorSize, cursorID, limit)
}

// SearchVideosByTags 按标签搜索（多选 AND）- 支持分页（按概率优先排序）
func (s *VideoService) SearchVideosByTags(tagIDs []uint, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	return s.SearchVideosWithFilters("", tagIDs, cursorScore, cursorSize, cursorID, limit)
}

// SearchVideosWithFilters 组合搜索（关键词 + 标签 AND）- 支持分页（按概率优先排序）
func (s *VideoService) SearchVideosWithFilters(keyword string, tagIDs []uint, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	var videos []models.Video
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		return nil, fmt.Errorf("获取设置失败: %w", err)
	}
	playWeight := settings.PlayWeight
	if playWeight < 0.1 {
		playWeight = 0.1
	}

	scoreSQL := "play_count * ? + random_play_count"
	query := database.DB.Model(&models.Video{}).Preload("Tags").
		Order(clause.Expr{SQL: scoreSQL + " ASC", Vars: []interface{}{playWeight}}).
		Order("videos.size desc").
		Order("videos.id desc")

	if strings.TrimSpace(keyword) != "" {
		query = query.Where("videos.name LIKE ?", "%"+strings.TrimSpace(keyword)+"%")
	}
	if len(tagIDs) > 0 {
		query = query.Joins("JOIN video_tags ON video_tags.video_id = videos.id").
			Where("video_tags.tag_id IN ?", tagIDs).
			Group("videos.id").
			Having("COUNT(DISTINCT video_tags.tag_id) = ?", len(tagIDs))
	}

	if cursorID > 0 {
		query = query.Where("("+scoreSQL+" > ?) OR ("+scoreSQL+" = ? AND videos.size < ?) OR ("+scoreSQL+" = ? AND videos.size = ? AND videos.id < ?)",
			playWeight, cursorScore,
			playWeight, cursorScore, cursorSize,
			playWeight, cursorScore, cursorSize, cursorID,
		)
	}

	err := query.Limit(limit).Find(&videos).Error
	return videos, err
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
		return &existingVideo, errors.New("视频已存在")
	}

	video := &models.Video{
		Name:      filepath.Base(path),
		Path:      path,
		Directory: filepath.Dir(path),
		Size:      info.Size(),
		Duration:  0, // TODO: 使用 ffmpeg 获取时长
	}

	err = database.DB.Create(video).Error
	if err != nil {
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "unique") || strings.Contains(errMsg, "constraint") {
			if findErr := database.DB.Where("path = ?", path).First(&existingVideo).Error; findErr == nil {
				return &existingVideo, errors.New("视频已存在")
			}
		}
		return nil, err
	}
	if err == nil {
		log.Printf("新增视频 path=%s", path)
	}
	return video, err
}

// DeleteVideo 删除视频
func (s *VideoService) DeleteVideo(id uint, deleteFile bool) error {
	var video models.Video
	if err := database.DB.First(&video, id).Error; err != nil {
		return err
	}

	// 如果需要删除原始文件
	if deleteFile {
		if err := os.Remove(video.Path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除文件失败: %w", err)
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
	var videoFiles []string

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

		if info.Name() != "." && strings.HasPrefix(info.Name(), ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		for _, videoExt := range videoExts {
			if ext == strings.ToLower(videoExt) {
				videoFiles = append(videoFiles, path)
				break
			}
		}

		return nil
	})
	log.Printf("扫描目录完成 dir=%s files=%d", dir, len(videoFiles))

	return videoFiles, err
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

	return openFileManager(video.Directory)
}

// PlayVideo 使用系统默认播放器播放视频
func (s *VideoService) PlayVideo(videoID uint) error {
	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		return err
	}

	// 更新播放次数和最后播放时间
	now := time.Now()
	database.DB.Model(&video).Updates(map[string]interface{}{
		"play_count":     video.PlayCount + 1,
		"last_played_at": now,
	})

	return openWithDefaultFn(video.Path)
}

// PlayRandomVideo 智能加权随机播放视频
func (s *VideoService) PlayRandomVideo() (*models.Video, error) {
	// 获取播放权重配置
	var settings models.Settings
	if err := database.DB.First(&settings).Error; err != nil {
		return nil, fmt.Errorf("获取设置失败: %w", err)
	}
	playWeight := settings.PlayWeight
	if playWeight < 0.1 {
		playWeight = 0.1
	}

	// 获取所有视频
	var videos []models.Video
	if err := database.DB.Preload("Tags").Find(&videos).Error; err != nil {
		return nil, err
	}

	if len(videos) == 0 {
		return nil, errors.New("没有可播放的视频")
	}

	// 计算每个视频的播放分数
	type VideoScore struct {
		Video *models.Video
		Score float64
	}

	videoScores := make([]VideoScore, len(videos))
	maxScore := 0.0

	for i := range videos {
		// 播放分数 = 普通播放次数 × 权重 + 随机播放次数
		score := float64(videos[i].PlayCount)*playWeight + float64(videos[i].RandomPlayCount)
		videoScores[i] = VideoScore{
			Video: &videos[i],
			Score: score,
		}
		if score > maxScore {
			maxScore = score
		}
	}

	// 计算每个视频的选择权重（分数越低，权重越高）
	type WeightedVideo struct {
		Video  *models.Video
		Weight float64
	}

	weightedVideos := make([]WeightedVideo, len(videoScores))
	totalWeight := 0.0

	for i, vs := range videoScores {
		// 选择权重 = max_score - score + 1
		// 每个视频独立计算权重，数量会自然影响整体概率
		weight := maxScore - vs.Score + 1.0
		weightedVideos[i] = WeightedVideo{
			Video:  vs.Video,
			Weight: weight,
		}
		totalWeight += weight
	}

	// 使用加权随机选择
	rand.Seed(time.Now().UnixNano())
	randomValue := rand.Float64() * totalWeight

	cumulative := 0.0
	var selectedVideo *models.Video

	for _, wv := range weightedVideos {
		cumulative += wv.Weight
		if randomValue <= cumulative {
			selectedVideo = wv.Video
			break
		}
	}

	// 防御性编程：如果没选中（浮点数精度问题），选最后一个
	if selectedVideo == nil {
		selectedVideo = weightedVideos[len(weightedVideos)-1].Video
	}

	// 更新随机播放次数和最后播放时间
	now := time.Now()
	database.DB.Model(selectedVideo).Updates(map[string]interface{}{
		"random_play_count": selectedVideo.RandomPlayCount + 1,
		"last_played_at":    now,
	})

	// 播放视频
	if err := openWithDefaultFn(selectedVideo.Path); err != nil {
		return selectedVideo, fmt.Errorf("播放失败: %s (%s): %w", selectedVideo.Name, selectedVideo.Path, err)
	}

	return selectedVideo, nil
}

var openWithDefaultFn = openWithDefault

// 打开文件管理器
func openFileManager(path string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin": // macOS
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("explorer", path)
	case "linux":
		cmd = exec.Command("xdg-open", path)
	default:
		return errors.New("不支持的操作系统")
	}

	return cmd.Start()
}

// 使用默认程序打开文件
func openWithDefault(path string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin": // macOS
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	case "linux":
		cmd = exec.Command("xdg-open", path)
	default:
		return errors.New("不支持的操作系统")
	}

	return cmd.Start()
}
