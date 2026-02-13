package services

import (
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
	return s.SearchVideosWithFilters(keyword, nil, cursorScore, cursorSize, cursorID, limit)
}

// SearchVideosByTags 按标签搜索（多选 AND）- 支持分页（按概率优先排序）
func (s *VideoService) SearchVideosByTags(tagIDs []uint, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
	return s.SearchVideosWithFilters("", tagIDs, cursorScore, cursorSize, cursorID, limit)
}

// SearchVideosWithFilters 组合搜索（关键词 + 标签 AND）- 支持分页（按概率优先排序）
func (s *VideoService) SearchVideosWithFilters(keyword string, tagIDs []uint, cursorScore float64, cursorSize int64, cursorID uint, limit int) ([]models.Video, error) {
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
		query = query.Where("videos.name LIKE ?", "%"+strings.TrimSpace(keyword)+"%")
	}
	if len(tagIDs) > 0 {
		query = query.Joins("JOIN video_tags ON video_tags.video_id = videos.id").
			Where("video_tags.tag_id IN ?", tagIDs).
			Group("videos.id").
			Having("COUNT(DISTINCT video_tags.tag_id) = ?", len(tagIDs))
	}

	query = applyCursorCondition(query, scoreSql, cursorScore, cursorSize, cursorID, "videos.")

	err = query.Limit(limit).Find(&videos).Error
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
		return &existingVideo, ErrVideoExists
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
				return &existingVideo, ErrVideoExists
			}
		}
		return nil, err
	}
	log.Printf("新增视频 path=%s", path)
	return video, nil
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

	return openPath(video.Directory, true)
}

// PlayVideo 使用系统默认播放器播放视频
func (s *VideoService) PlayVideo(videoID uint) error {
	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		return err
	}

	// 使用数据库原子操作更新播放次数和最后播放时间
	now := time.Now()
	database.DB.Model(&video).Updates(map[string]interface{}{
		"play_count":     gorm.Expr("play_count + 1"),
		"last_played_at": now,
	})

	return openWithDefaultFn(video.Path, false)
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
		return nil, ErrNoVideos
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
	now := time.Now()
	database.DB.Model(&selectedVideo).Updates(map[string]interface{}{
		"random_play_count": gorm.Expr("random_play_count + 1"),
		"last_played_at":    now,
	})
	selectedVideo.RandomPlayCount++ // 同步内存中的值

	// 播放视频
	if err := openWithDefaultFn(selectedVideo.Path, false); err != nil {
		return &selectedVideo, fmt.Errorf("播放失败: %s (%s): %w", selectedVideo.Name, selectedVideo.Path, err)
	}

	return &selectedVideo, nil
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
