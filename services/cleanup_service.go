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

type CleanupAnalysis struct {
	DuplicateGroups []CleanupDuplicateGroup `json:"duplicate_groups"`
	LowDuration     []models.Video          `json:"low_duration"`
	LowResolution   []models.Video          `json:"low_resolution"`
}

type CleanupProgress struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Path    string `json:"path"`
}

type CleanupStatus struct {
	Running   bool             `json:"running"`
	Completed bool             `json:"completed"`
	Error     string           `json:"error"`
	Progress  CleanupProgress  `json:"progress"`
	Analysis  *CleanupAnalysis `json:"analysis,omitempty"`
	StartedAt *time.Time       `json:"started_at,omitempty" ts_type:"string"`
	UpdatedAt *time.Time       `json:"updated_at,omitempty" ts_type:"string"`
}

type CleanupService struct {
	ctx    context.Context
	mu     sync.Mutex
	status CleanupStatus
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
	status := s.statusSnapshotLocked()
	s.mu.Unlock()

	go func() {
		analysis, err := s.AnalyzeCleanupCandidates(criteria)
		s.mu.Lock()
		defer s.mu.Unlock()
		now := time.Now()
		s.status.Running = false
		s.status.Completed = err == nil
		s.status.UpdatedAt = &now
		if err != nil {
			s.status.Error = err.Error()
			s.status.Analysis = nil
			return
		}
		s.status.Error = ""
		s.status.Analysis = analysis
	}()

	return &status, nil
}

func (s *CleanupService) Status() *CleanupStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.statusSnapshotLocked()
	return &status
}

func (s *CleanupService) statusSnapshotLocked() CleanupStatus {
	status := s.status
	if status.Analysis != nil {
		analysisCopy := *status.Analysis
		status.Analysis = &analysisCopy
	}
	return status
}

func (s *CleanupService) AnalyzeCleanupCandidates(criteria CleanupCriteria) (*CleanupAnalysis, error) {
	startedAt := time.Now()
	var videos []models.Video
	if err := database.DB.Order("id asc").Find(&videos).Error; err != nil {
		return nil, err
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

	sort.Slice(result.DuplicateGroups, func(i, j int) bool {
		return result.DuplicateGroups[i].Original.ID < result.DuplicateGroups[j].Original.ID
	})

	log.Printf("[Cleanup] analysis completed elapsed=%s duplicate_groups=%d low_duration=%d low_resolution=%d hash_candidates=%d",
		time.Since(startedAt).Round(time.Millisecond),
		len(result.DuplicateGroups), len(result.LowDuration), len(result.LowResolution), len(hashCandidates),
	)
	s.emitProgress("done", len(hashCandidates), len(hashCandidates), "", fmt.Sprintf(
		"分析完成：重复组 %d，短视频 %d，低清视频 %d。",
		len(result.DuplicateGroups), len(result.LowDuration), len(result.LowResolution),
	))

	return result, nil
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
	aPixels := a.Width * a.Height
	bPixels := b.Width * b.Height
	if aPixels != bPixels {
		return aPixels > bPixels
	}
	if a.Size != b.Size {
		return a.Size > b.Size
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
