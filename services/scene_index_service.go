package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 画面索引任务（场景检索合同「画面索引」）：显式启动、单 worker、FIFO、可取消；每项先取
// 共享 MediaWorkSlot，登记表键 scene_index，事件 scene-index-state。没有自动触发；退出时
// StopAndWait 但不阻止退出（派生数据，已完成项已落库）。失败写 failed 状态与抹掉路径的
// 原因，下一轮重试。worker 是每个视频派生行的唯一写者。
const (
	sceneIndexFailureLimit        = 50
	sceneIndexFailureWriteTimeout = 2 * time.Second
	sceneIndexInsertBatch         = 500
	// sceneErrorSourceChanged：建索引途中源文件变了，结果整批丢弃。
	sceneErrorSourceChanged = "source_changed"
	// sceneErrorExternalRevoked：外部描述已被撤销，剩余外部项跳过。
	sceneErrorExternalRevoked = "scene_external_revoked"
)

var (
	// ErrSceneIndexNotRunning：当前没有建索引任务可取消。
	ErrSceneIndexNotRunning = errors.New("scene_index_not_running")
	// ErrSceneIndexRunning：建索引进行中，清理旧索引须等它结束。
	ErrSceneIndexRunning = errors.New("scene_index_running")
	// ErrSceneIndexStopping：应用正在退出。
	ErrSceneIndexStopping = errors.New("scene_index_stopping")
	// ErrSceneIndexCancelling：上一轮已取消、尚未收尾，稍后再启动。
	ErrSceneIndexCancelling = errors.New("scene_index_cancelling")
	// errSceneExternalRevoked：外部描述在入队之后被关掉或换了接口/模型/Key，不再发请求。
	errSceneExternalRevoked = errors.New(sceneErrorExternalRevoked)
	// errSceneSourceUnreadable：源文件读不到（例如移动硬盘未接），已有索引原样保留。
	errSceneSourceUnreadable = errors.New("source_unreadable")
	errSceneSourceChanged    = errors.New(sceneErrorSourceChanged)
	errSceneNoFrames         = errors.New("scene_no_frames")
)

// SceneIndexRequest 是 StartSceneIndex 的参数：video_ids 非空时只考虑这些视频，否则按
// filter（为空即全库）解析；provider 为空时取设置里的提供方。
type SceneIndexRequest struct {
	VideoIDs []uint         `json:"video_ids"`
	Filter   *LibraryFilter `json:"filter"`
	Provider string         `json:"provider"`
}

type SceneIndexFailure struct {
	VideoID uint   `json:"video_id"`
	Name    string `json:"name"`
	Error   string `json:"error"`
}

// SceneIndexStatus 是 GetSceneIndexStatus 与 scene-index-state 事件的载荷。
type SceneIndexStatus struct {
	Running          bool                `json:"running"`
	Preparing        bool                `json:"preparing"`
	Cancelled        bool                `json:"cancelled"`
	Completed        bool                `json:"completed"`
	Provider         string              `json:"provider"`
	ModelID          string              `json:"model_id"`
	Total            int                 `json:"total"`
	Processed        int                 `json:"processed"`
	Succeeded        int                 `json:"succeeded"`
	Skipped          int                 `json:"skipped"`
	Failed           int                 `json:"failed"`
	CurrentVideoID   uint                `json:"current_video_id"`
	CurrentVideoName string              `json:"current_video_name"`
	StartedAt        *time.Time          `json:"started_at" ts_type:"string"`
	UpdatedAt        *time.Time          `json:"updated_at" ts_type:"string"`
	Failures         []SceneIndexFailure `json:"failures"`
}

// sceneIndexItem 是队列里的一项：提供方、模型与间隔在入队时定下，整项不变。
type sceneIndexItem struct {
	VideoID    uint
	Name       string
	Provider   string
	ModelID    string
	IntervalMS int64
	captioner  SceneCaptionClient
	// externalFingerprint 是入队时外部配置的摘要；外部项每次发请求前都要与当前设置比对。
	externalFingerprint string
}

// sceneFrameExtractor 把视频按间隔抽成 224×224 JPEG，返回按时间排序的帧路径。
type sceneFrameExtractor func(ctx context.Context, sourcePath, dir string, intervalMS int64) ([]string, error)

// sceneVectorSource 是本地向量的来源（真实实现是常驻 worker 宿主）。
type sceneVectorSource interface {
	EmbedImages(ctx context.Context, paths []string) ([][]float32, error)
	EmbedTexts(ctx context.Context, texts []string) ([][]float32, error)
}

// SceneIndexService 构建与维护 scene_visual_segments / scene_index_states。
type SceneIndexService struct {
	runtime          *SceneRuntime
	host             *sceneWorkerHost
	vectors          sceneVectorSource
	captions         sceneCaptionClientFactory
	extract          sceneFrameExtractor
	runtimeAvailable func() bool
	tempRoot         func() string
	slot             *MediaWorkSlot
	now              func() time.Time

	mu       sync.Mutex
	stopMu   sync.Mutex
	status   SceneIndexStatus
	pending  []sceneIndexJob
	queued   map[uint]struct{}
	cancel   context.CancelFunc
	worker   sync.WaitGroup
	emitter  func(SceneIndexStatus)
	registry *BackgroundTaskRegistry
	stopping bool
}

// NewSceneIndexService 构造建索引服务；runtime 提供可用性、worker 与临时目录。
func NewSceneIndexService(runtime *SceneRuntime, slot *MediaWorkSlot) *SceneIndexService {
	host := newSceneRuntimeWorkerHost(runtime)
	return &SceneIndexService{
		runtime:          runtime,
		host:             host,
		vectors:          host,
		captions:         defaultSceneCaptionClientFactory,
		extract:          ffmpegSceneFrames,
		runtimeAvailable: runtime.Available,
		tempRoot:         runtime.TempDir,
		slot:             slot,
		now:              time.Now,
		status:           SceneIndexStatus{Failures: []SceneIndexFailure{}},
	}
}

func (s *SceneIndexService) SetEventEmitter(emitter func(SceneIndexStatus)) {
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
}

// SetBackgroundTaskRegistry 接入后台任务登记表（键 scene_index）。
func (s *SceneIndexService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
}

func (s *SceneIndexService) Status() SceneIndexStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSceneIndexStatus(s.status)
}

// resolveProvider 决定这一轮用哪个提供方，并确认它此刻可用。外部提供方只有在设置里
// 显式选了 external 时才允许；本地不可用时只报错，绝不改用外部（TC-18）。
func (s *SceneIndexService) resolveProvider(requested string, settings sceneSettings) (string, string, SceneCaptionClient, error) {
	provider := settings.Provider
	if requested != "" {
		provider = NormalizeSceneVisualProvider(requested)
	}
	if provider == SceneProviderExternal {
		if settings.Provider != SceneProviderExternal {
			return "", "", nil, ErrSceneExternalNotEnabled
		}
		client, err := s.captions()
		if err != nil {
			return "", "", nil, ErrSceneExternalNotConfigured
		}
		return provider, sceneExternalModelID(client.ModelName()), client, nil
	}
	if s.runtimeAvailable == nil || !s.runtimeAvailable() {
		return "", "", nil, ErrSceneRuntimeUnavailable
	}
	return SceneProviderLocal, SceneLocalModelID, nil, nil
}

// sceneIndexJob 是一次 StartSceneIndex：入队时定下提供方、模型与间隔，由 worker 按 FIFO
// 逐个解析成候选视频并处理。
type sceneIndexJob struct {
	request    SceneIndexRequest
	provider   string
	modelID    string
	intervalMS int64
	captioner  SceneCaptionClient
	// externalFingerprint 同 sceneIndexItem 的同名字段，只对外部提供方有意义。
	externalFingerprint string
}

// Start 是唯一的启动入口（没有自动触发）。已有一轮在跑时把这次请求排到队尾（FIFO）。
func (s *SceneIndexService) Start(parent context.Context, request SceneIndexRequest) (SceneIndexStatus, error) {
	if s == nil {
		return SceneIndexStatus{}, ErrSceneRuntimeUnavailable
	}
	if parent == nil {
		parent = context.Background()
	}
	settings, err := loadSceneSettings(parent)
	if err != nil {
		return s.Status(), err
	}
	provider, modelID, captioner, err := s.resolveProvider(request.Provider, settings)
	if err != nil {
		return s.Status(), err
	}
	job := sceneIndexJob{request: request, provider: provider, modelID: modelID, intervalMS: settings.IntervalMS, captioner: captioner}
	if provider == SceneProviderExternal {
		job.externalFingerprint = settings.ExternalFingerprint
	}

	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return SceneIndexStatus{}, ErrSceneIndexStopping
	}
	if s.status.Running && s.status.Cancelled {
		// 上一轮已取消、正在收尾：此时排进去的请求会随那一轮一起被丢掉，明确告诉调用方（M-3）。
		s.mu.Unlock()
		return s.Status(), ErrSceneIndexCancelling
	}
	if s.status.Running {
		s.queueJobLocked(job)
		status, emitter := cloneSceneIndexStatus(s.status), s.emitter
		s.mu.Unlock()
		emitSceneIndexStatus(emitter, status)
		return status, nil
	}
	now := s.now()
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.queued = map[uint]struct{}{}
	s.status = SceneIndexStatus{
		Running: true, Preparing: true, Provider: provider, ModelID: modelID,
		StartedAt: &now, UpdatedAt: &now, Failures: []SceneIndexFailure{},
	}
	s.pending = []sceneIndexJob{job}
	status, emitter, registry := cloneSceneIndexStatus(s.status), s.emitter, s.registry
	s.worker.Add(1)
	s.mu.Unlock()

	registry.Begin(BackgroundTaskSceneIndex)
	emitSceneIndexStatus(emitter, status)
	go func() {
		defer s.worker.Done()
		defer registry.End(BackgroundTaskSceneIndex)
		s.run(ctx)
	}()
	return status, nil
}

func (s *SceneIndexService) queueJobLocked(job sceneIndexJob) {
	s.pending = append(s.pending, job)
	now := s.now()
	s.status.UpdatedAt = &now
}

// Cancel 取消当前一轮（含排队中的请求）。正在处理的那一项被丢弃，不写库。
func (s *SceneIndexService) Cancel() error {
	s.mu.Lock()
	cancel, running := s.cancel, s.status.Running
	if running && cancel != nil {
		s.status.Cancelled = true
		now := s.now()
		s.status.UpdatedAt = &now
	}
	status, emitter := cloneSceneIndexStatus(s.status), s.emitter
	s.mu.Unlock()
	if !running || cancel == nil {
		return ErrSceneIndexNotRunning
	}
	cancel()
	emitSceneIndexStatus(emitter, status)
	return nil
}

// StopAndWait 在应用退出时取消并等 worker 收尾，再关掉常驻 sidecar。
func (s *SceneIndexService) StopAndWait() {
	if s == nil {
		return
	}
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	s.mu.Lock()
	s.stopping = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.worker.Wait()
	s.host.Close()
	s.mu.Lock()
	s.stopping = false
	s.mu.Unlock()
}

// run 按 FIFO 逐个处理请求；队列空或被取消时在同一把锁里收尾，Start 不会漏掉新请求。
func (s *SceneIndexService) run(ctx context.Context) {
	for {
		job, ok := s.nextJob(ctx)
		if !ok {
			return
		}
		items, err := s.resolveItems(ctx, job)
		if err != nil {
			if ctx.Err() == nil {
				s.recordFailure(sceneIndexItem{Name: "整理候选视频"}, err)
			}
			continue
		}
		s.update(func(status *SceneIndexStatus) {
			status.Preparing = false
			status.Total += len(items)
			status.Provider, status.ModelID = job.provider, job.modelID
		})
		if s.processItems(ctx, items) {
			// 本地运行时没了：整轮停止，排队中的请求一并放弃（M-4）。
			s.mu.Lock()
			s.pending = nil
			s.mu.Unlock()
		}
	}
}

// processItems 逐项处理一个请求的候选。外部描述被撤销时这一请求的剩余项全部跳过（I-1）；
// 本地运行时不可用或 worker 起不来时返回 true，调用方据此结束整轮（M-4）。
func (s *SceneIndexService) processItems(ctx context.Context, items []sceneIndexItem) (abort bool) {
	for index, item := range items {
		if ctx.Err() != nil {
			return false
		}
		err := s.processItem(ctx, item)
		switch {
		case err == nil:
		case ctx.Err() != nil:
		case errors.Is(err, errSceneSkipped):
			s.update(func(status *SceneIndexStatus) { status.Processed++; status.Skipped++ })
		case errors.Is(err, errSceneExternalRevoked):
			s.recordStopped("外部描述", sceneErrorExternalRevoked, len(items)-index)
			return false
		case errors.Is(err, ErrSceneRuntimeUnavailable):
			s.recordFailure(item, ErrSceneRuntimeUnavailable)
			s.recordStopped("", "", len(items)-index-1)
			return true
		default:
			s.recordFailure(item, err)
		}
	}
	return false
}

// recordStopped 把 count 项记为跳过；code 非空时附一条说明原因的记录（不计入失败数）。
func (s *SceneIndexService) recordStopped(name, code string, count int) {
	s.update(func(status *SceneIndexStatus) {
		status.Processed += count
		status.Skipped += count
		if code != "" && len(status.Failures) < sceneIndexFailureLimit {
			status.Failures = append(status.Failures, SceneIndexFailure{Name: name, Error: code})
		}
	})
}

// nextJob 取下一个请求；没有请求或已取消时收尾并返回 false。
func (s *SceneIndexService) nextJob(ctx context.Context) (sceneIndexJob, bool) {
	s.mu.Lock()
	if ctx.Err() == nil && len(s.pending) > 0 {
		job := s.pending[0]
		s.pending = s.pending[1:]
		s.status.Preparing = true
		status, emitter := cloneSceneIndexStatus(s.status), s.emitter
		s.mu.Unlock()
		emitSceneIndexStatus(emitter, status)
		return job, true
	}
	cancelled := ctx.Err() != nil
	s.pending, s.queued = nil, nil
	s.status.Running, s.status.Preparing = false, false
	s.status.Cancelled, s.status.Completed = cancelled, !cancelled
	s.status.CurrentVideoID, s.status.CurrentVideoName = 0, ""
	now := s.now()
	s.status.UpdatedAt = &now
	cancel := s.cancel
	s.cancel = nil
	status, emitter := cloneSceneIndexStatus(s.status), s.emitter
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	emitSceneIndexStatus(emitter, status)
	return sceneIndexJob{}, false
}

var errSceneSkipped = errors.New("scene_index_skipped")

// sceneVideoRow 是解析范围时读出的视频字段。
type sceneVideoRow struct {
	ID       uint
	Name     string
	Path     string
	Size     int64
	Duration float64
}

// sceneScopeQuery 返回"活跃、非失效、在扫描范围内"并满足筛选条件的视频查询。
// 复用 applyLibraryFilter 的范围；排序与折叠版本不参与。失效视图也不会让失效视频进来。
func sceneScopeQuery(ctx context.Context, filter *LibraryFilter) (*gorm.DB, error) {
	if database.DB == nil {
		return nil, errSceneDatabaseUnavailable
	}
	scoped := LibraryFilter{}
	if filter != nil {
		scoped = *filter
	}
	scoped.SortMode = ""
	query, err := applyLibraryFilter(database.DB.WithContext(ctx).Model(&models.Video{}), scoped, time.Now())
	if err != nil {
		return nil, err
	}
	return query.Where("videos.is_stale = ?", false), nil
}

// resolveItems 把一次请求解析成待处理视频：当前模型没有状态、状态失败、源 size/mtime
// 或采样间隔变化的视频。本轮已排过的视频不重复计入。
func (s *SceneIndexService) resolveItems(ctx context.Context, job sceneIndexJob) ([]sceneIndexItem, error) {
	var query *gorm.DB
	if ids := uniqueUintIDs(job.request.VideoIDs); len(ids) > 0 {
		query = videoBackfillQuery(ctx).Where("videos.id IN ?", ids)
	} else {
		scoped, err := sceneScopeQuery(ctx, job.request.Filter)
		if err != nil {
			return nil, err
		}
		query = scoped
	}
	var rows []sceneVideoRow
	if err := query.Select("videos.id", "videos.name", "videos.path", "videos.size", "videos.duration").
		Order("videos.id ASC").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取视频范围失败: %w", err)
	}
	states, err := loadSceneIndexStates(ctx, rows)
	if err != nil {
		return nil, err
	}
	// 先在锁外 stat（可能很慢），再在锁内去重：Status / Cancel 不该被文件系统拖住。
	stale := make([]sceneVideoRow, 0, len(rows))
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if state, ok := states[row.ID]; ok && sceneStateIsFresh(state, job.modelID, job.intervalMS, row.Path) {
			continue
		}
		stale = append(stale, row)
	}
	items := make([]sceneIndexItem, 0, len(stale))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range stale {
		if _, seen := s.queued[row.ID]; seen {
			continue
		}
		s.queued[row.ID] = struct{}{}
		items = append(items, sceneIndexItem{
			VideoID: row.ID, Name: row.Name, Provider: job.provider, ModelID: job.modelID,
			IntervalMS: job.intervalMS, captioner: job.captioner, externalFingerprint: job.externalFingerprint,
		})
	}
	return items, nil
}

func loadSceneIndexStates(ctx context.Context, rows []sceneVideoRow) (map[uint]models.SceneIndexState, error) {
	states := make(map[uint]models.SceneIndexState, len(rows))
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	for start := 0; start < len(ids); start += 1000 {
		end := min(start+1000, len(ids))
		var batch []models.SceneIndexState
		if err := database.DB.WithContext(ctx).Where("video_id IN ?", ids[start:end]).Find(&batch).Error; err != nil {
			return nil, fmt.Errorf("读取画面索引状态失败: %w", err)
		}
		for _, state := range batch {
			states[state.VideoID] = state
		}
	}
	return states, nil
}

// sceneStateIsFresh 判断一行状态是否仍对应磁盘上的文件、当前模型与当前间隔。
func sceneStateIsFresh(state models.SceneIndexState, modelID string, intervalMS int64, path string) bool {
	if state.Status != models.SceneIndexStatusIndexed || state.ModelID != modelID || int64(state.IntervalMS) != intervalMS {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return info.Size() == state.SourceSize && info.ModTime().UnixNano() == state.SourceMtimeNS
}

// processItem 处理一个视频：取媒体槽 → 重读记录并记下源 size/mtime → 抽帧 → 取向量或描述
// → 分段 → 源未变化才在一个事务里替换该视频的段与状态。返回 errSceneSkipped 表示不必处理。
func (s *SceneIndexService) processItem(ctx context.Context, item sceneIndexItem) error {
	// 先确认提供方此刻仍可用，再去抢媒体槽与抽帧（I-1、M-4）。
	if err := s.checkProvider(ctx, item); err != nil {
		return err
	}
	if s.slot != nil {
		if err := s.slot.Acquire(ctx); err != nil {
			return err
		}
		defer s.slot.Release()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.update(func(status *SceneIndexStatus) {
		status.CurrentVideoID, status.CurrentVideoName = item.VideoID, item.Name
	})
	var video sceneVideoRow
	err := videoBackfillQuery(ctx).Select("videos.id", "videos.name", "videos.path", "videos.size", "videos.duration").
		Where("videos.id = ?", item.VideoID).Take(&video).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errSceneSkipped
	}
	if err != nil {
		return fmt.Errorf("重读视频记录失败: %w", err)
	}
	before, err := os.Stat(video.Path)
	if err != nil || !before.Mode().IsRegular() {
		// 读不到源（移动硬盘未接）不是索引失效：已有索引原样保留（I-2）。
		s.recordUnreadableSource(ctx, item)
		return errSceneSourceUnreadable
	}
	var state models.SceneIndexState
	if err := database.DB.WithContext(ctx).Where("video_id = ?", item.VideoID).Take(&state).Error; err == nil &&
		sceneStateIsFresh(state, item.ModelID, item.IntervalMS, video.Path) {
		return errSceneSkipped
	}
	drafts, err := s.buildDrafts(ctx, item, video)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, errSceneExternalRevoked):
			return err
		case errors.Is(err, ErrSceneRuntimeUnavailable) || errors.Is(err, errSceneWorkerStartup):
			// 运行时或 worker 的问题不是这个视频的错，不写失败状态（M-4）。
			return ErrSceneRuntimeUnavailable
		}
		if _, statErr := os.Stat(video.Path); statErr != nil {
			s.recordUnreadableSource(ctx, item)
			return errSceneSourceUnreadable
		}
		s.recordStateFailure(ctx, item, before.Size(), before.ModTime().UnixNano(), err)
		return err
	}
	after, err := os.Stat(video.Path)
	if err != nil {
		s.recordUnreadableSource(ctx, item)
		return errSceneSourceUnreadable
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		// 源文件在抽帧或推理期间变了：结果对应的不是现在的文件，整批丢弃。
		s.recordStateFailure(ctx, item, before.Size(), before.ModTime().UnixNano(), errSceneSourceChanged)
		return errSceneSourceChanged
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := s.replaceVideoSegments(ctx, item, before, drafts); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	s.update(func(status *SceneIndexStatus) { status.Processed++; status.Succeeded++ })
	return nil
}

// buildDrafts 抽帧并按提供方算出段。临时目录在 <runtime>/.tmp/<videoID>/，每项结束删除。
func (s *SceneIndexService) buildDrafts(ctx context.Context, item sceneIndexItem, video sceneVideoRow) ([]sceneSegmentDraft, error) {
	root := ""
	if s.tempRoot != nil {
		root = s.tempRoot()
	}
	if root == "" {
		return nil, ErrSceneDataDirUnavailable
	}
	dir := filepath.Join(root, strconv.FormatUint(uint64(video.ID), 10))
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	frames, err := s.extract(ctx, video.Path, dir, item.IntervalMS)
	if err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, errSceneNoFrames
	}
	durationMS := int64(video.Duration * 1000)
	if item.Provider == SceneProviderExternal {
		captions, err := s.captionFrames(ctx, item, frames)
		if err != nil {
			return nil, err
		}
		return buildSceneCaptionSegments(captions, item.IntervalMS, durationMS), nil
	}
	vectors, err := s.embedFrames(ctx, frames)
	if err != nil {
		return nil, err
	}
	return buildSceneSegments(vectors, item.IntervalMS, durationMS), nil
}

// embedFrames 按每批至多 32 张请求本地向量；批间检查取消。
func (s *SceneIndexService) embedFrames(ctx context.Context, frames []string) ([][]float32, error) {
	if s.vectors == nil {
		return nil, ErrSceneRuntimeUnavailable
	}
	vectors := make([][]float32, 0, len(frames))
	for start := 0; start < len(frames); start += sceneWorkerMaxImages {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		end := min(start+sceneWorkerMaxImages, len(frames))
		batch, err := s.vectors.EmbedImages(ctx, frames[start:end])
		if err != nil {
			return nil, err
		}
		if len(batch) != end-start {
			return nil, fmt.Errorf("scene_worker_error: expected %d embeddings, got %d", end-start, len(batch))
		}
		vectors = append(vectors, batch...)
	}
	return vectors, nil
}

// captionFrames 按每批 8 帧请求外部描述；只在该项的提供方为 external 时被调用。
func (s *SceneIndexService) captionFrames(ctx context.Context, item sceneIndexItem, frames []string) ([]string, error) {
	if item.captioner == nil {
		return nil, ErrSceneExternalNotConfigured
	}
	captions := make([]string, 0, len(frames))
	for start := 0; start < len(frames); start += sceneCaptionFramesPerRequest {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// 每批发出前重读设置：改回本地或换了接口/模型/Key 之后一个请求也不再发（I-1）。
		if err := checkSceneExternalAllowed(ctx, item.externalFingerprint); err != nil {
			return nil, err
		}
		end := min(start+sceneCaptionFramesPerRequest, len(frames))
		jpegs := make([][]byte, 0, end-start)
		for _, frame := range frames[start:end] {
			data, err := os.ReadFile(frame)
			if err != nil {
				return nil, errors.New("frame_unreadable")
			}
			jpegs = append(jpegs, data)
		}
		batch, err := item.captioner.CaptionFrames(ctx, jpegs)
		if err != nil {
			return nil, err
		}
		if len(batch) != len(jpegs) {
			return nil, fmt.Errorf("scene_external_caption_count: %d/%d", len(batch), len(jpegs))
		}
		captions = append(captions, batch...)
	}
	return captions, nil
}

// replaceVideoSegments 在一个事务里删掉该视频的全部旧段（任何模型），插入新段并 upsert 状态。
func (s *SceneIndexService) replaceVideoSegments(ctx context.Context, item sceneIndexItem, source os.FileInfo, drafts []sceneSegmentDraft) error {
	rows := make([]models.SceneVisualSegment, 0, len(drafts))
	for _, draft := range drafts {
		row := models.SceneVisualSegment{VideoID: item.VideoID, ModelID: item.ModelID, StartMS: draft.StartMS, EndMS: draft.EndMS, Caption: draft.Caption}
		if item.Provider != SceneProviderExternal {
			blob, err := quantizeSceneVector(draft.Vector)
			if err != nil {
				return err
			}
			row.Embedding = blob
		}
		rows = append(rows, row)
	}
	now := s.now()
	state := models.SceneIndexState{
		VideoID: item.VideoID, ModelID: item.ModelID, Provider: item.Provider,
		SourceSize: source.Size(), SourceMtimeNS: source.ModTime().UnixNano(), IntervalMS: int(item.IntervalMS),
		SegmentCount: len(rows), Status: models.SceneIndexStatusIndexed, LastError: "", IndexedAt: &now, UpdatedAt: now,
	}
	return database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		if err := tx.Where("video_id = ?", item.VideoID).Delete(&models.SceneVisualSegment{}).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(&rows, sceneIndexInsertBatch).Error; err != nil {
				return err
			}
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "video_id"}}, UpdateAll: true}).Create(&state).Error
	})
}

// recordStateFailure 写 failed 状态并删掉该视频的旧段（它们对应的已不是当前文件或模型）。
// 任务被取消时父 ctx 已失效，失败原因仍要落库，因此单独给一个短窗口。
func (s *SceneIndexService) recordStateFailure(parent context.Context, item sceneIndexItem, size, mtimeNS int64, cause error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), sceneIndexFailureWriteTimeout)
	defer cancel()
	state := models.SceneIndexState{
		VideoID: item.VideoID, ModelID: item.ModelID, Provider: item.Provider, SourceSize: size, SourceMtimeNS: mtimeNS,
		IntervalMS: int(item.IntervalMS), SegmentCount: 0, Status: models.SceneIndexStatusFailed,
		LastError: redactFrameHashErrorPaths(boundedError(cause, 500)), UpdatedAt: s.now(),
	}
	_ = database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		if err := tx.Where("video_id = ?", item.VideoID).Delete(&models.SceneVisualSegment{}).Error; err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "video_id"}}, UpdateAll: true}).Create(&state).Error
	})
}

func (s *SceneIndexService) recordFailure(item sceneIndexItem, err error) {
	message := redactFrameHashErrorPaths(boundedError(err, 300))
	s.update(func(status *SceneIndexStatus) {
		status.Processed++
		status.Failed++
		if len(status.Failures) < sceneIndexFailureLimit {
			status.Failures = append(status.Failures, SceneIndexFailure{VideoID: item.VideoID, Name: item.Name, Error: message})
		}
	})
}

func (s *SceneIndexService) update(mutate func(*SceneIndexStatus)) {
	s.mu.Lock()
	mutate(&s.status)
	now := s.now()
	s.status.UpdatedAt = &now
	status, emitter := cloneSceneIndexStatus(s.status), s.emitter
	s.mu.Unlock()
	emitSceneIndexStatus(emitter, status)
}

func cloneSceneIndexStatus(status SceneIndexStatus) SceneIndexStatus {
	status.Failures = append([]SceneIndexFailure{}, status.Failures...)
	return status
}

func emitSceneIndexStatus(emitter func(SceneIndexStatus), status SceneIndexStatus) {
	if emitter != nil {
		emitter(status)
	}
}

// SceneIndexCleanupResult 是"清理旧索引"的结果。
type SceneIndexCleanupResult struct {
	DeletedSegments int64  `json:"deleted_segments"`
	DeletedStates   int64  `json:"deleted_states"`
	ModelID         string `json:"model_id"`
}

// ClearStaleIndex 删除非当前模型的全部段与状态（当前模型由设置里的提供方决定）。
// 建索引进行中拒绝执行：worker 是派生行的唯一写者。
func (s *SceneIndexService) ClearStaleIndex(ctx context.Context) (SceneIndexCleanupResult, error) {
	s.mu.Lock()
	running := s.status.Running
	s.mu.Unlock()
	if running {
		return SceneIndexCleanupResult{}, ErrSceneIndexRunning
	}
	settings, err := loadSceneSettings(ctx)
	if err != nil {
		return SceneIndexCleanupResult{}, err
	}
	modelID := settings.currentModelID()
	if modelID == "" {
		return SceneIndexCleanupResult{}, ErrSceneExternalNotConfigured
	}
	result := SceneIndexCleanupResult{ModelID: modelID}
	err = database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		segments := tx.Where("model_id <> ?", modelID).Delete(&models.SceneVisualSegment{})
		if segments.Error != nil {
			return segments.Error
		}
		states := tx.Where("model_id <> ?", modelID).Delete(&models.SceneIndexState{})
		if states.Error != nil {
			return states.Error
		}
		result.DeletedSegments, result.DeletedStates = segments.RowsAffected, states.RowsAffected
		return nil
	})
	return result, err
}

// ffmpegSceneFrames 单趟按间隔抽 224×224 帧（场景检索合同「画面索引」的命令）；
// 第 n 帧（从 0 起）代表 n×间隔毫秒。-map 0:V:0 跳过封面这类附带图片流。
func ffmpegSceneFrames(ctx context.Context, sourcePath, dir string, intervalMS int64) ([]string, error) {
	binary, err := findThumbnailFFmpeg()
	if err != nil {
		return nil, errors.New("ffmpeg_unavailable")
	}
	seconds := max(intervalMS/1000, 1)
	command := exec.CommandContext(ctx, binary,
		"-v", "error", "-nostdin",
		"-i", sourcePath,
		"-map", "0:V:0",
		"-vf", fmt.Sprintf("fps=1/%d,scale=224:224:flags=bicubic", seconds),
		"-q:v", "3",
		filepath.Join(dir, "f_%06d.jpg"),
	)
	output, runErr := command.CombinedOutput()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if runErr != nil {
		return nil, fmt.Errorf("ffmpeg 抽帧失败: %v: %s", runErr, truncateLogSnippet(string(output), 300))
	}
	frames, err := filepath.Glob(filepath.Join(dir, "f_*.jpg"))
	if err != nil {
		return nil, err
	}
	sort.Strings(frames)
	return frames, nil
}

// checkProvider 在每项开始前确认提供方仍可用：本地看运行时是否就绪；外部重读设置，
// 只有仍是 external 且接口/模型/Key 与入队时一致才放行。读不到设置一律按撤销处理（宁可不发）。
func (s *SceneIndexService) checkProvider(ctx context.Context, item sceneIndexItem) error {
	if item.Provider == SceneProviderExternal {
		return checkSceneExternalAllowed(ctx, item.externalFingerprint)
	}
	if s.runtimeAvailable == nil || !s.runtimeAvailable() {
		return ErrSceneRuntimeUnavailable
	}
	return nil
}

func checkSceneExternalAllowed(ctx context.Context, fingerprint string) error {
	settings, err := loadSceneSettings(ctx)
	if err != nil || !settings.externalAllowed(fingerprint) {
		return errSceneExternalRevoked
	}
	return nil
}

// recordUnreadableSource 是源文件读不到时的记账：已有 indexed 状态（任何模型）原样保留，
// 段一行不动；没有有效状态时才写一条 failed / source_unreadable，同样不删任何段（I-2）。
func (s *SceneIndexService) recordUnreadableSource(parent context.Context, item sceneIndexItem) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), sceneIndexFailureWriteTimeout)
	defer cancel()
	var existing models.SceneIndexState
	err := database.DB.WithContext(ctx).Where("video_id = ?", item.VideoID).Limit(1).Find(&existing).Error
	if err != nil || (existing.VideoID != 0 && existing.Status == models.SceneIndexStatusIndexed) {
		return
	}
	state := models.SceneIndexState{
		VideoID: item.VideoID, ModelID: item.ModelID, Provider: item.Provider, IntervalMS: int(item.IntervalMS),
		Status: models.SceneIndexStatusFailed, LastError: errSceneSourceUnreadable.Error(), UpdatedAt: s.now(),
	}
	_ = database.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "video_id"}}, UpdateAll: true}).Create(&state).Error
}
