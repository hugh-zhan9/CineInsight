package services

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const partialHashChunkSize = 64 * 1024

type CleanupCriteria struct {
	MinDuration time.Duration `json:"min_duration"`
	MinWidth    int           `json:"min_width"`
	MinHeight   int           `json:"min_height"`
}

type CleanupDuplicateGroup struct {
	Original   models.Video   `json:"original"`
	Candidates []models.Video `json:"candidates"`
	Reason     string         `json:"reason"`
}

type CleanupSameSourceGroup struct {
	RelationID       uint         `json:"relation_id"`
	Preferred        models.Video `json:"preferred"`
	Alternative      models.Video `json:"alternative"`
	Confidence       string       `json:"confidence"`
	Reason           string       `json:"reason"`
	EstimatedSavings int64        `json:"estimated_savings"`
}

type CleanupAnalysis struct {
	DuplicateGroups     []CleanupDuplicateGroup  `json:"duplicate_groups"`
	NearDuplicateGroups []CleanupDuplicateGroup  `json:"near_duplicate_groups"`
	SameSourceGroups    []CleanupSameSourceGroup `json:"same_source_groups"`
	LowDuration         []models.Video           `json:"low_duration"`
	LowResolution       []models.Video           `json:"low_resolution"`
	// StaleHashCount 是源文件已变更、感知哈希失效待重算的视频数；这些视频
	// 暂不参与近似重复检测，可通过"补全感知哈希"一键重算。
	StaleHashCount int64 `json:"stale_hash_count"`
}

type CleanupProgress struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Path    string `json:"path"`
}

type CleanupStatus struct {
	Running   bool            `json:"running"`
	Completed bool            `json:"completed"`
	Error     string          `json:"error"`
	Progress  CleanupProgress `json:"progress"`
	// Stale 表示缓存结果算出后库又发生了变化（删除、扫描、感知哈希补全等）。
	// 结果仍然保留供用户继续审阅，只是提示可能过期，由用户决定何时重新分析。
	Stale     bool             `json:"stale"`
	Analysis  *CleanupAnalysis `json:"analysis,omitempty"`
	StartedAt *time.Time       `json:"started_at,omitempty" ts_type:"string"`
	UpdatedAt *time.Time       `json:"updated_at,omitempty" ts_type:"string"`
}

type CleanupService struct {
	ctx                  context.Context
	mu                   sync.Mutex
	status               CleanupStatus
	invalidatedDuringRun bool
	// runID 每次启动分析自增。后台 goroutine 写完状态到发出 done 事件之间没有持锁，
	// 期间用户可能已经启动了新一轮；靠它判断自己是否仍是当前这轮，避免旧的收尾事件
	// 把 done 阶段盖到新一轮的状态上。
	runID uint64
}

func (s *CleanupService) SetContext(ctx context.Context) {
	s.ctx = ctx
}

func (s *CleanupService) StartAnalysis(criteria CleanupCriteria) (*CleanupStatus, error) {
	s.mu.Lock()
	if s.status.Running {
		status := s.statusSnapshotLocked()
		s.mu.Unlock()
		return &status, nil
	}
	now := time.Now()
	s.status = CleanupStatus{
		Running:   true,
		Completed: false,
		StartedAt: &now,
		UpdatedAt: &now,
		Progress: CleanupProgress{
			Stage:   "load",
			Message: "正在准备清理候选分析…",
			Current: 0,
			Total:   0,
		},
	}
	s.invalidatedDuringRun = false
	s.runID++
	runID := s.runID
	status := s.statusSnapshotLocked()
	s.mu.Unlock()

	go func() {
		analysis, _, err := s.analyzeCleanupCandidates(criteria)

		s.mu.Lock()
		now := time.Now()
		s.status.Running = false
		s.status.UpdatedAt = &now
		// 运行期间库发生了变化：结果仍然保留供审阅，只标记为可能过期。
		staleDuringRun := s.invalidatedDuringRun
		s.invalidatedDuringRun = false
		if err != nil {
			s.status.Completed = false
			s.status.Error = err.Error()
			s.status.Analysis = nil
			s.status.Stale = false
		} else {
			s.status.Completed = true
			s.status.Error = ""
			s.status.Analysis = analysis
			s.status.Stale = staleDuringRun
		}
		total := s.status.Progress.Total
		s.mu.Unlock()

		// done 事件必须在状态写入之后再发：前端收到 done 会立刻回读 Status()，
		// 先发事件会读到 running=true / analysis=nil，界面就永远停在"分析中"。
		// 失败同样要发终止事件，否则纯事件驱动的界面收不到任何结束信号。
		if err != nil {
			s.emitDoneForRun(runID, total, fmt.Sprintf("分析失败：%v", err))
			return
		}
		s.emitDoneForRun(runID, total, cleanupDoneMessage(analysis))
	}()

	return &status, nil
}

func (s *CleanupService) Status() *CleanupStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.statusSnapshotLocked()
	return &status
}

// InvalidateAnalysis 标记缓存结果可能已过期；结果本身保留，用户重开审阅界面
// 仍能看到并继续处理，由用户自己决定何时重新分析（不再静默丢弃并自动重跑）。
func (s *CleanupService) InvalidateAnalysis() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Running {
		s.invalidatedDuringRun = true
		return
	}
	if s.status.Analysis == nil {
		return
	}
	s.status.Stale = true
	now := time.Now()
	s.status.UpdatedAt = &now
}

func (s *CleanupService) statusSnapshotLocked() CleanupStatus {
	status := s.status
	if status.Analysis != nil {
		analysisCopy := *status.Analysis
		status.Analysis = &analysisCopy
	}
	return status
}

// AnalyzeCleanupCandidates 同步执行一次完整分析并发出终止事件，供 GetCleanupCandidates
// 这类同步调用方使用；异步任务走 analyzeCleanupCandidates，由 StartAnalysis 在写完状态后补发 done。
func (s *CleanupService) AnalyzeCleanupCandidates(criteria CleanupCriteria) (*CleanupAnalysis, error) {
	result, hashCandidates, err := s.analyzeCleanupCandidates(criteria)
	if err != nil {
		return nil, err
	}
	s.emitProgress("done", hashCandidates, hashCandidates, "", cleanupDoneMessage(result))
	return result, nil
}

func cleanupDoneMessage(result *CleanupAnalysis) string {
	return fmt.Sprintf(
		"分析完成：重复组 %d，近似重复 %d，同源候选 %d，短视频 %d，低清视频 %d。",
		len(result.DuplicateGroups), len(result.NearDuplicateGroups), len(result.SameSourceGroups), len(result.LowDuration), len(result.LowResolution),
	)
}

// analyzeCleanupCandidates 返回分析结果和参与哈希比对的候选数；不发终止事件。
func (s *CleanupService) analyzeCleanupCandidates(criteria CleanupCriteria) (*CleanupAnalysis, int, error) {
	startedAt := time.Now()
	var videos []models.Video
	if err := database.DB.Order("id asc").Find(&videos).Error; err != nil {
		return nil, 0, err
	}
	videoService := &VideoService{}

	log.Printf("[Cleanup] analysis started total_videos=%d criteria={min_duration=%s min_width=%d min_height=%d}",
		len(videos), criteria.MinDuration, criteria.MinWidth, criteria.MinHeight,
	)
	s.emitProgress("load", 0, len(videos), "", fmt.Sprintf("已读取 %d 条视频记录，正在整理候选…", len(videos)))

	result := &CleanupAnalysis{}
	sizeBuckets := make(map[int64][]models.Video)

	for idx, video := range videos {
		info, err := os.Stat(video.Path)
		if err != nil {
			if os.IsNotExist(err) {
				log.Printf("[Cleanup] skip missing video id=%d path=%s", video.ID, video.Path)
			} else {
				log.Printf("[Cleanup] skip unreadable video id=%d path=%s err=%v", video.ID, video.Path, err)
			}
			continue
		}
		if info.IsDir() {
			log.Printf("[Cleanup] skip directory video id=%d path=%s", video.ID, video.Path)
			continue
		}

		workingVideo := video
		freshDuration, freshResolution, freshWidth, freshHeight := videoService.getVideoMetadata(video.Path)
		hasFreshMetadata := freshDuration > 0 && freshResolution != "" && freshWidth > 0 && freshHeight > 0
		if hasFreshMetadata {
			workingVideo.Duration = freshDuration
			workingVideo.Resolution = freshResolution
			workingVideo.Width = freshWidth
			workingVideo.Height = freshHeight
		} else {
			log.Printf("[Cleanup] metadata unavailable for candidate id=%d path=%s", video.ID, video.Path)
			continue
		}

		if hasFreshMetadata && criteria.MinDuration > 0 && time.Duration(workingVideo.Duration*float64(time.Second)) < criteria.MinDuration {
			result.LowDuration = append(result.LowDuration, workingVideo)
		}
		if hasFreshMetadata && criteria.MinWidth > 0 && criteria.MinHeight > 0 && (workingVideo.Width < criteria.MinWidth || workingVideo.Height < criteria.MinHeight) {
			result.LowResolution = append(result.LowResolution, workingVideo)
		}
		sizeBuckets[workingVideo.Size] = append(sizeBuckets[workingVideo.Size], workingVideo)

		if shouldEmitCleanupProgress(idx+1, len(videos), 400) {
			s.emitProgress("group", idx+1, len(videos), video.Path, "正在按文件大小聚合候选…")
		}
	}

	hashCandidates := make([]models.Video, 0)
	for _, bucket := range sizeBuckets {
		if len(bucket) < 2 {
			continue
		}
		hashCandidates = append(hashCandidates, bucket...)
	}

	s.emitProgress("hash", 0, len(hashCandidates), "", fmt.Sprintf("发现 %d 个疑似重复文件，正在读取采样哈希…", len(hashCandidates)))

	duplicateBuckets := make(map[string][]models.Video)
	for idx, video := range hashCandidates {
		hash, err := getPartialHash(video.Path)
		if err != nil || hash == "" {
			if shouldEmitCleanupProgress(idx+1, len(hashCandidates), 50) {
				s.emitProgress("hash", idx+1, len(hashCandidates), video.Path, "正在读取疑似重复文件的采样哈希…")
			}
			continue
		}
		bucketKey := buildDuplicateBucketKey(video.Size, hash)
		duplicateBuckets[bucketKey] = append(duplicateBuckets[bucketKey], video)

		if shouldEmitCleanupProgress(idx+1, len(hashCandidates), 50) {
			s.emitProgress("hash", idx+1, len(hashCandidates), video.Path, "正在读取疑似重复文件的采样哈希…")
		}
	}

	for _, bucket := range duplicateBuckets {
		if len(bucket) < 2 {
			continue
		}
		sort.Slice(bucket, func(i, j int) bool {
			return isPreferredOriginal(bucket[i], bucket[j])
		})
		result.DuplicateGroups = append(result.DuplicateGroups, CleanupDuplicateGroup{
			Original:   bucket[0],
			Candidates: append([]models.Video(nil), bucket[1:]...),
			Reason:     "文件大小和采样哈希一致",
		})
	}

	exactPairs := make(map[[2]uint]struct{})
	for _, group := range result.DuplicateGroups {
		members := append([]models.Video{group.Original}, group.Candidates...)
		for i := 0; i < len(members); i++ {
			for j := i + 1; j < len(members); j++ {
				exactPairs[cleanupVideoPairKey(members[i].ID, members[j].ID)] = struct{}{}
			}
		}
	}
	dismissed, err := loadNearDuplicateDismissals()
	if err != nil {
		return nil, 0, err
	}
	excludedPairs := make(map[[2]uint]struct{}, len(exactPairs)+len(dismissed))
	for pair := range exactPairs {
		excludedPairs[pair] = struct{}{}
	}
	for pair := range dismissed {
		excludedPairs[pair] = struct{}{}
	}
	nearDuplicateGroups, nearPairs, staleHashCount, err := loadCleanupNearDuplicateGroups(excludedPairs)
	if err != nil {
		return nil, 0, err
	}
	result.NearDuplicateGroups = nearDuplicateGroups
	result.StaleHashCount = staleHashCount
	for pair := range nearPairs {
		exactPairs[pair] = struct{}{}
	}
	sameSourceGroups, err := loadCleanupSameSourceGroups(exactPairs)
	if err != nil {
		return nil, 0, err
	}
	result.SameSourceGroups = sameSourceGroups

	sort.Slice(result.DuplicateGroups, func(i, j int) bool {
		return result.DuplicateGroups[i].Original.ID < result.DuplicateGroups[j].Original.ID
	})

	log.Printf("[Cleanup] analysis completed elapsed=%s duplicate_groups=%d near_duplicate_groups=%d same_source_groups=%d low_duration=%d low_resolution=%d hash_candidates=%d",
		time.Since(startedAt).Round(time.Millisecond),
		len(result.DuplicateGroups), len(result.NearDuplicateGroups), len(result.SameSourceGroups), len(result.LowDuration), len(result.LowResolution), len(hashCandidates),
	)
	// done 事件由调用方在写完状态后发出，这里不发，避免前端收到 done 时回读到尚未写入结果的状态。
	return result, len(hashCandidates), nil
}

// emitDoneForRun 只在自己仍是当前这轮分析时写入 done 进度并发事件。
func (s *CleanupService) emitDoneForRun(runID uint64, total int, message string) {
	progress := CleanupProgress{Stage: "done", Message: message, Current: total, Total: total}
	s.mu.Lock()
	if s.runID != runID {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	s.status.Progress = progress
	s.status.UpdatedAt = &now
	s.mu.Unlock()

	if s.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(s.ctx, "cleanup-progress", progress)
}

func loadCleanupSameSourceGroups(exactPairs map[[2]uint]struct{}) ([]CleanupSameSourceGroup, error) {
	var relations []models.VideoSameSourceRelation
	err := database.DB.Model(&models.VideoSameSourceRelation{}).
		Joins("INNER JOIN videos AS same_source_video_a ON same_source_video_a.id = video_same_source_relations.video_a_id AND same_source_video_a.deleted_at IS NULL").
		Joins("INNER JOIN videos AS same_source_video_b ON same_source_video_b.id = video_same_source_relations.video_b_id AND same_source_video_b.deleted_at IS NULL").
		Preload("VideoA.Tags").
		Preload("VideoB.Tags").
		Where("video_same_source_relations.status = ?", models.VideoSameSourceStatusDetected).
		Order("video_same_source_relations.id ASC").
		Find(&relations).Error
	if err != nil {
		return nil, err
	}

	groups := make([]CleanupSameSourceGroup, 0, len(relations))
	for _, relation := range relations {
		if _, duplicate := exactPairs[cleanupVideoPairKey(relation.VideoAID, relation.VideoBID)]; duplicate {
			continue
		}
		preferred, alternative := relation.VideoA, relation.VideoB
		if isPreferredCleanupVideo(alternative, preferred) {
			preferred, alternative = alternative, preferred
		}
		reason := relation.Reasoning
		if reason == "" {
			reason = "画面指纹与 AI 复核判断为同源视频"
		}
		groups = append(groups, CleanupSameSourceGroup{
			RelationID: relation.ID, Preferred: preferred, Alternative: alternative,
			Confidence: relation.Confidence, Reason: reason, EstimatedSavings: alternative.Size,
		})
	}
	return groups, nil
}

func cleanupVideoPairKey(a, b uint) [2]uint {
	if a > b {
		a, b = b, a
	}
	return [2]uint{a, b}
}

func shouldEmitCleanupProgress(current int, total int, every int) bool {
	if total <= 0 {
		return false
	}
	if current <= 1 || current >= total {
		return true
	}
	return every > 0 && current%every == 0
}

func (s *CleanupService) emitProgress(stage string, current int, total int, currentPath string, message string) {
	progress := CleanupProgress{
		Stage:   stage,
		Message: message,
		Current: current,
		Total:   total,
		Path:    currentPath,
	}
	s.mu.Lock()
	now := time.Now()
	s.status.Progress = progress
	s.status.UpdatedAt = &now
	s.mu.Unlock()

	if s.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(s.ctx, "cleanup-progress", progress)
}

func buildDuplicateBucketKey(size int64, hash string) string {
	return fmt.Sprintf("%d:%s", size, hash)
}

func isPreferredOriginal(a, b models.Video) bool {
	return isPreferredCleanupVideo(a, b)
}

func isPreferredCleanupVideo(a, b models.Video) bool {
	aPixels := a.Width * a.Height
	bPixels := b.Width * b.Height
	if aPixels != bPixels {
		return aPixels > bPixels
	}
	if a.Size != b.Size {
		return a.Size > b.Size
	}
	if len(a.Tags) != len(b.Tags) {
		return len(a.Tags) > len(b.Tags)
	}
	return a.ID < b.ID
}

func getPartialHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}

	size := info.Size()
	hash := md5.New()

	if _, err := io.CopyN(hash, f, partialHashChunkSize); err != nil && err != io.EOF {
		return "", err
	}

	if size > partialHashChunkSize*3 {
		if _, err := f.Seek(size/2, io.SeekStart); err == nil {
			_, _ = io.CopyN(hash, f, partialHashChunkSize)
		}
	}

	if size > partialHashChunkSize {
		if _, err := f.Seek(size-partialHashChunkSize, io.SeekStart); err == nil {
			_, _ = io.Copy(hash, f)
		}
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}
