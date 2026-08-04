package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"
)

type LocalMetadataExportRequest struct {
	Filter LibraryFilter `json:"filter"`
}

type LocalMetadataExportStatus struct {
	Running        bool                   `json:"running"`
	Cancelled      bool                   `json:"cancelled"`
	Completed      bool                   `json:"completed"`
	Total          int                    `json:"total"`
	Processed      int                    `json:"processed"`
	Succeeded      int                    `json:"succeeded"`
	Failed         int                    `json:"failed"`
	CurrentVideoID uint                   `json:"current_video_id"`
	StartedAt      *time.Time             `json:"started_at" ts_type:"string"`
	UpdatedAt      *time.Time             `json:"updated_at" ts_type:"string"`
	Failures       []LocalMetadataFailure `json:"failures"`
	// FailuresTruncated 表示超过 localMetadataFailureLimit、未逐条列入
	// Failures 的失败数量；Failed 仍是全部失败的总数。
	FailuresTruncated int `json:"failures_truncated"`
}

type localMetadataExportTask struct {
	mu     sync.Mutex
	status LocalMetadataExportStatus
	cancel context.CancelFunc
	worker sync.WaitGroup
	// stopping 在 StopExport 等待 worker 退出期间置位；此时拒绝新的
	// StartExport，保证 worker.Add 不会与 worker.Wait 并发（WaitGroup 复用约束）。
	stopping bool
	emitter  func(LocalMetadataExportStatus)
}

func (s *LocalMetadataService) exportState() *localMetadataExportTask {
	s.exportInit.Do(func() {
		if s.exportTask == nil {
			s.exportTask = &localMetadataExportTask{}
		}
	})
	return s.exportTask
}

func (s *LocalMetadataService) SetExportEventEmitter(emitter func(LocalMetadataExportStatus)) {
	state := s.exportState()
	state.mu.Lock()
	state.emitter = emitter
	state.mu.Unlock()
}

func (s *LocalMetadataService) ExportVideoNFO(ctx context.Context, videoID uint) (*LocalMetadataNFOExportResult, error) {
	if videoID == 0 {
		return nil, errors.New("视频 ID 不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var video models.Video
	if err := database.DB.WithContext(ctx).Preload("Tags").First(&video, videoID).Error; err != nil {
		return nil, err
	}
	people, err := loadCurrentLocalMetadataPeople(videoID)
	if err != nil {
		return nil, err
	}
	collections, err := loadExportCollections(videoID)
	if err != nil {
		return nil, err
	}
	tags := make([]string, 0, len(video.Tags))
	for _, tag := range video.Tags {
		tags = append(tags, tag.Name)
	}
	sort.Strings(tags)
	personNames := make([]string, 0, len(people))
	for _, person := range people {
		personNames = append(personNames, person.Name)
	}
	collection := ""
	warnings := []string{}
	if len(collections) > 0 {
		collection = collections[0]
	}
	if len(collections) > 1 {
		warnings = append(warnings, "Kodi movie NFO 仅支持一个作品集，已按片库顺序写出第一个作品集")
	}

	libraryPathMutationMu.RLock()
	defer libraryPathMutationMu.RUnlock()
	result, err := ExportLocalMetadataNFO(ctx, video.Path, LocalMetadataNFOExportInput{
		DisplayTitle: video.DisplayTitle, PersonalRating: video.PersonalRating, Tags: tags,
		People: personNames, Collection: collection,
	})
	if result != nil {
		result.Warnings = warnings
	}
	return result, err
}

func loadExportCollections(videoID uint) ([]string, error) {
	var rows []struct{ Name string }
	err := database.DB.Table("media_collections").Select("media_collections.name").
		Joins("JOIN collection_videos ON collection_videos.collection_id = media_collections.id").
		Where("collection_videos.video_id = ? AND media_collections.deleted_at IS NULL", videoID).
		Order("collection_videos.position ASC, media_collections.id ASC").Scan(&rows).Error
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.Name)
	}
	return result, err
}

func (s *LocalMetadataService) StartExport(parent context.Context, request LocalMetadataExportRequest) (LocalMetadataExportStatus, error) {
	if s.exportProcess == nil {
		s.exportProcess = s.ExportVideoNFO
	}
	state := s.exportState()
	if parent == nil {
		parent = context.Background()
	}
	filter, err := normalizeLibraryFilter(request.Filter)
	if err != nil {
		return LocalMetadataExportStatus{}, err
	}
	state.mu.Lock()
	if state.stopping {
		state.mu.Unlock()
		return LocalMetadataExportStatus{}, errors.New("本地资料写出任务正在停止")
	}
	if state.status.Running {
		status := cloneLocalMetadataExportStatus(state.status)
		state.mu.Unlock()
		return status, nil
	}
	previous := state.status
	now := time.Now()
	ctx, cancel := context.WithCancel(parent)
	state.cancel = cancel
	state.status = LocalMetadataExportStatus{Running: true, StartedAt: &now, UpdatedAt: &now, Failures: []LocalMetadataFailure{}}
	state.mu.Unlock()

	// 慢操作（字幕索引同步、筛选查询）在锁外执行，避免大片库时长时间阻塞
	// ExportStatus/CancelExport；期间 Running 已置位，重复 StartExport 直接返回当前状态。
	videoIDs, err := s.collectExportVideoIDs(ctx, filter)
	if err != nil {
		cancel()
		state.mu.Lock()
		state.status = previous
		state.cancel = nil
		state.mu.Unlock()
		return LocalMetadataExportStatus{}, err
	}

	state.mu.Lock()
	if ctx.Err() != nil {
		// 准备期间被 CancelExport/StopExport 取消：按已取消收尾，不再启动 worker。
		state.status.Running = false
		state.status.Cancelled = true
		state.status.Completed = true
		finishedAt := time.Now()
		state.status.UpdatedAt = &finishedAt
		state.cancel = nil
		status, emitter := cloneLocalMetadataExportStatus(state.status), state.emitter
		state.mu.Unlock()
		cancel()
		emitLocalMetadataExport(emitter, status)
		return status, nil
	}
	state.status.Total = len(videoIDs)
	updatedAt := time.Now()
	state.status.UpdatedAt = &updatedAt
	status, emitter := cloneLocalMetadataExportStatus(state.status), state.emitter
	state.worker.Add(1)
	state.mu.Unlock()
	emitLocalMetadataExport(emitter, status)
	go s.runExport(ctx, videoIDs)
	return status, nil
}

func (s *LocalMetadataService) collectExportVideoIDs(ctx context.Context, filter LibraryFilter) ([]uint, error) {
	if libraryFilterNeedsSubtitleSync(filter) {
		if err := syncSubtitleIndexesFromFilesystem(); err != nil {
			return nil, err
		}
	}
	query, err := applyLibraryFilter(database.DB.WithContext(ctx).Model(&models.Video{}).Select("videos.id"), filter, time.Now())
	if err != nil {
		return nil, err
	}
	var videoIDs []uint
	if err := query.Order("videos.id ASC").Pluck("videos.id", &videoIDs).Error; err != nil {
		return nil, fmt.Errorf("读取当前筛选结果失败: %w", err)
	}
	return videoIDs, nil
}

func (s *LocalMetadataService) ExportStatus() LocalMetadataExportStatus {
	state := s.exportState()
	state.mu.Lock()
	defer state.mu.Unlock()
	return cloneLocalMetadataExportStatus(state.status)
}

func (s *LocalMetadataService) CancelExport() error {
	state := s.exportState()
	state.mu.Lock()
	cancel, running := state.cancel, state.status.Running
	state.mu.Unlock()
	if !running || cancel == nil {
		return errors.New("本地资料写出任务未运行")
	}
	cancel()
	return nil
}

func (s *LocalMetadataService) StopExport() {
	state := s.exportState()
	state.mu.Lock()
	// stopping 置位后新的 StartExport 会被拒绝，保证下方 Wait 不会与
	// worker.Add 并发；Wait 返回后再清除。
	state.stopping = true
	cancel := state.cancel
	state.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	state.worker.Wait()
	state.mu.Lock()
	state.stopping = false
	state.mu.Unlock()
}

// runExport 逐个写出 NFO。已知的有界交互：批量写出会改动媒体目录内的 NFO
// 文件，使库监视器的快照变脏并触发一轮 reconcile 扫描；监视器对已存在视频
// 只做观察、不回写数据库，因此不会形成数据回环。
func (s *LocalMetadataService) runExport(ctx context.Context, videoIDs []uint) {
	defer s.exportState().worker.Done()
	for _, videoID := range videoIDs {
		if ctx.Err() != nil {
			break
		}
		s.updateExport(func(status *LocalMetadataExportStatus) { status.CurrentVideoID = videoID })
		_, err := s.exportProcess(ctx, videoID)
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				break
			}
			s.updateExport(func(status *LocalMetadataExportStatus) {
				status.Processed++
				status.Failed++
				if len(status.Failures) < localMetadataFailureLimit {
					status.Failures = append(status.Failures, localMetadataFailure(videoID, err))
				} else {
					status.FailuresTruncated++
				}
			})
			continue
		}
		s.updateExport(func(status *LocalMetadataExportStatus) { status.Processed++; status.Succeeded++ })
	}
	s.finishExport(ctx.Err() != nil)
}

func (s *LocalMetadataService) updateExport(update func(*LocalMetadataExportStatus)) {
	state := s.exportState()
	state.mu.Lock()
	update(&state.status)
	now := time.Now()
	state.status.UpdatedAt = &now
	status, emitter := cloneLocalMetadataExportStatus(state.status), state.emitter
	state.mu.Unlock()
	emitLocalMetadataExport(emitter, status)
}

func (s *LocalMetadataService) finishExport(cancelled bool) {
	state := s.exportState()
	state.mu.Lock()
	state.status.Running = false
	state.status.Cancelled = cancelled
	state.status.Completed = true
	state.status.CurrentVideoID = 0
	now := time.Now()
	state.status.UpdatedAt = &now
	state.cancel = nil
	status, emitter := cloneLocalMetadataExportStatus(state.status), state.emitter
	state.mu.Unlock()
	emitLocalMetadataExport(emitter, status)
}

func cloneLocalMetadataExportStatus(status LocalMetadataExportStatus) LocalMetadataExportStatus {
	status.Failures = append([]LocalMetadataFailure(nil), status.Failures...)
	return status
}

func emitLocalMetadataExport(emitter func(LocalMetadataExportStatus), status LocalMetadataExportStatus) {
	if emitter != nil {
		emitter(status)
	}
}
