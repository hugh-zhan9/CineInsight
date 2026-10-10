package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"video-master/database"
	"video-master/models"
	"video-master/services/editalign"

	"gorm.io/gorm"
)

// VideoEditService 拥有视频工作台的编辑配方、冻结计划、执行记录与工作目录（视频编辑合同）。
// 项目与导出项两张表是事实源；全应用单 worker 按项 ID 先进先出执行，worker 是项状态的单写者。
// 原片只读：服务从不删除或改写来源，成品一律另存、不覆盖并入库。
type VideoEditService struct {
	probe   *MediaProbeService
	slot    *MediaWorkSlot
	dataDir string

	// 测试接缝：外部命令、编码器探测、磁盘空间、平台、时间源与分析算法。
	runOutput     videoEditOutputRunner
	runProgress   videoEditProgressRunner
	findFFmpeg    func() (string, error)
	findFFprobe   func() (string, error)
	listEncoders  func(ctx context.Context, ffmpeg string) (map[string]bool, error)
	diskFree      func(path string) (uint64, error)
	goos          string
	now           func() time.Time
	detectIntros  func(ctx context.Context, sources []editalign.Source, opts editalign.IntroOptions) ([]editalign.IntroResult, error)
	alignSegments func(ctx context.Context, long, hd editalign.Source, opts editalign.AlignOptions, progress func(done, total int)) ([]editalign.AlignedSegment, error)
	readHashes    func(ctx context.Context, ffmpeg, path string, startMS, durationMS, stepMS int64) (editalign.Sequence, error)
	grayReader    func(ffmpeg, path string, side int) editalign.GrayFrameReader
	// publishTxHook 只在单测里设置：在入库事务内最后调用，返回错误即模拟事务失败。
	publishTxHook func(tx *gorm.DB) error
	// afterClaimHook 只在单测里设置：worker 领到一项之后、登记取消函数之前调用。
	afterClaimHook func()
	// beforePublishRename 只在单测里设置：发布时选好文件名、写入 publish_target 之后、rename 之前调用。
	beforePublishRename func(target string)

	// bootTime 是服务构造时间：启动对账只处理在它之前排队/开始分析的项目，不碰本次进程里新排的项。
	bootTime time.Time

	mu               sync.Mutex
	parentCtx        context.Context
	stopping         bool
	workerRunning    bool
	kick             uint64
	worker           sync.WaitGroup
	currentItemID    uint
	currentProject   uint
	currentCancel    context.CancelFunc
	cancelRequested  map[uint]bool
	analysisProject  uint
	analysisCancel   context.CancelFunc
	analysisDone     float64
	analysisFinished chan struct{}
	analysis         sync.WaitGroup
	encoders         map[string]bool
	emitter          func(VideoEditStateEvent)
	registry         *BackgroundTaskRegistry
}

// videoEditOutputRunner 运行一次外部命令，返回完整 stdout（有上限）与 stderr 尾部。
type videoEditOutputRunner func(ctx context.Context, name string, args []string) (stdout string, stderrTail string, err error)

// videoEditProgressRunner 运行带 -progress pipe:1 的 ffmpeg，把 out_time 毫秒回调给 onProgress。
type videoEditProgressRunner func(ctx context.Context, name string, args []string, onProgress func(outMS int64)) (stderrTail string, err error)

// VideoEditStateEvent 是 video-edit-state 事件载荷：只带 ID、状态与进度（合同「接口」）。
type VideoEditStateEvent struct {
	ProjectID  uint    `json:"project_id"`
	ItemID     uint    `json:"item_id"`
	Status     string  `json:"status"`
	ItemStatus string  `json:"item_status"`
	Phase      string  `json:"phase"`
	Progress   float64 `json:"progress"`
}

// NewVideoEditService 创建服务；slot 为共享的重媒体槽（app.mediaWorkSlot）。
func NewVideoEditService(probe *MediaProbeService, slot *MediaWorkSlot, dataDir string) *VideoEditService {
	if slot == nil {
		slot = NewMediaWorkSlot()
	}
	return &VideoEditService{
		probe:           probe,
		slot:            slot,
		dataDir:         dataDir,
		runOutput:       runVideoEditOutput,
		runProgress:     runVideoEditProgress,
		findFFmpeg:      findThumbnailFFmpeg,
		findFFprobe:     findFFProbeBinary,
		listEncoders:    nil,
		diskFree:        enhancementDiskFree,
		goos:            runtime.GOOS,
		now:             time.Now,
		detectIntros:    editalign.DetectIntros,
		alignSegments:   editalign.AlignSegments,
		readHashes:      editalign.ReadHashSequence,
		grayReader:      editalign.NewFFmpegGrayReader,
		cancelRequested: map[uint]bool{},
		// 截到微秒：PostgreSQL 的时间戳只有微秒精度，本进程里写入的 queued_at 截断后也不会早于它。
		bootTime: time.Now().Truncate(time.Microsecond),
	}
}

// SetEventEmitter 注册 video-edit-state 事件回调。
func (s *VideoEditService) SetEventEmitter(emitter func(VideoEditStateEvent)) {
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
}

// SetBackgroundTaskRegistry 接入后台任务登记表（键 video_edit）。
func (s *VideoEditService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
}

func (s *VideoEditService) emit(event VideoEditStateEvent) {
	s.mu.Lock()
	emitter := s.emitter
	s.mu.Unlock()
	if emitter != nil {
		emitter(event)
	}
}

// EditProjectCreateRequest 新建项目。hd_replace 的 video_ids 为 [长版, 高清版]。
type EditProjectCreateRequest struct {
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	VideoIDs []uint `json:"video_ids"`
}

// EditRecipeUpdateRequest 以 expected_revision 做乐观并发更新配方；mode/title 为空表示不改。
type EditRecipeUpdateRequest struct {
	ProjectID        uint       `json:"project_id"`
	ExpectedRevision uint64     `json:"expected_revision"`
	Recipe           EditRecipe `json:"recipe"`
	Mode             string     `json:"mode"`
	Title            string     `json:"title"`
}

// EditItemView 是导出项快照；路径一律不暴露。
type EditItemView struct {
	ID             uint       `json:"id"`
	Seq            int        `json:"seq"`
	Status         string     `json:"status"`
	Phase          string     `json:"phase"`
	Progress       float64    `json:"progress"`
	OutputName     string     `json:"output_name"`
	OutputVideoID  *uint      `json:"output_video_id"`
	SourceVideoIDs []uint     `json:"source_video_ids"`
	ErrorCode      string     `json:"error_code"`
	ErrorMessage   string     `json:"error_message"`
	StartedAt      *time.Time `json:"started_at" ts_type:"string"`
	FinishedAt     *time.Time `json:"finished_at" ts_type:"string"`
}

// EditProjectView 是项目详情。analysis 为最近一轮分析的结果（从未分析为 null）。
type EditProjectView struct {
	ID                   uint                `json:"id"`
	Kind                 string              `json:"kind"`
	Title                string              `json:"title"`
	Status               string              `json:"status"`
	Revision             uint64              `json:"revision"`
	Mode                 string              `json:"mode"`
	Recipe               EditRecipe          `json:"recipe"`
	Analysis             *EditAnalysisResult `json:"analysis"`
	AcknowledgedWarnings []string            `json:"acknowledged_warnings"`
	ErrorCode            string              `json:"error_code"`
	ErrorMessage         string              `json:"error_message"`
	Progress             float64             `json:"progress"`
	Items                []EditItemView      `json:"items"`
	CreatedAt            time.Time           `json:"created_at" ts_type:"string"`
	UpdatedAt            time.Time           `json:"updated_at" ts_type:"string"`
	QueuedAt             *time.Time          `json:"queued_at" ts_type:"string"`
	FinishedAt           *time.Time          `json:"finished_at" ts_type:"string"`
}

// EditProjectSummary 是项目列表的一行。
type EditProjectSummary struct {
	ID             uint       `json:"id"`
	Kind           string     `json:"kind"`
	Title          string     `json:"title"`
	Status         string     `json:"status"`
	Revision       uint64     `json:"revision"`
	Mode           string     `json:"mode"`
	ItemCount      int        `json:"item_count"`
	CompletedCount int        `json:"completed_count"`
	Progress       float64    `json:"progress"`
	ErrorCode      string     `json:"error_code"`
	ErrorMessage   string     `json:"error_message"`
	CreatedAt      time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt      time.Time  `json:"updated_at" ts_type:"string"`
	FinishedAt     *time.Time `json:"finished_at" ts_type:"string"`
}

// VideoEditStatus 是服务整体状态（GetVideoEditStatus）。
type VideoEditStatus struct {
	WorkerRunning      bool    `json:"worker_running"`
	CurrentProjectID   uint    `json:"current_project_id"`
	CurrentItemID      uint    `json:"current_item_id"`
	AnalyzingProjectID uint    `json:"analyzing_project_id"`
	AnalysisProgress   float64 `json:"analysis_progress"`
	QueuedItems        int     `json:"queued_items"`
	RunningItems       int     `json:"running_items"`
}

func editDB(ctx context.Context) (*gorm.DB, error) {
	if database.DB == nil {
		return nil, editError("database_unavailable", "数据库未初始化")
	}
	return database.DB.WithContext(ctx), nil
}

// loadEditVideos 读取活跃（未软删）视频；任一缺失返回 source_missing。
func loadEditVideos(ctx context.Context, ids []uint) (map[uint]models.Video, error) {
	db, err := editDB(ctx)
	if err != nil {
		return nil, err
	}
	var videos []models.Video
	if len(ids) > 0 {
		if err := db.Where("id IN ?", ids).Find(&videos).Error; err != nil {
			return nil, err
		}
	}
	byID := make(map[uint]models.Video, len(videos))
	for _, video := range videos {
		byID[video.ID] = video
	}
	for _, id := range ids {
		if _, ok := byID[id]; !ok {
			return nil, editError("source_missing", "视频 %d 不存在或已删除", id)
		}
	}
	return byID, nil
}

// editVideoDisplayName 取显示标题，没有时取去扩展名的文件名。
func editVideoDisplayName(video models.Video) string {
	if title := strings.TrimSpace(video.DisplayTitle); title != "" {
		return title
	}
	name := strings.TrimSpace(video.Name)
	if dot := strings.LastIndex(name, "."); dot > 0 {
		name = name[:dot]
	}
	return name
}

func clampEditTitle(title string) string {
	title = strings.TrimSpace(title)
	if utf8.RuneCountInString(title) > videoEditTitleMaxRunes {
		title = string([]rune(title)[:videoEditTitleMaxRunes])
	}
	return title
}

func defaultEditTitle(kind string, videoIDs []uint, videos map[uint]models.Video) string {
	first := ""
	if len(videoIDs) > 0 {
		first = editVideoDisplayName(videos[videoIDs[0]])
	}
	switch kind {
	case models.VideoEditKindMerge:
		return fmt.Sprintf("合并：%s 等 %d 个", first, len(videoIDs))
	case models.VideoEditKindTrimIntro:
		return fmt.Sprintf("去片头：%s 等 %d 个", first, len(videoIDs))
	default:
		return "高清替换：" + first
	}
}

// CreateProject 新建草稿项目：revision=1、mode=precise 显式写入（无 gorm default）。
func (s *VideoEditService) CreateProject(ctx context.Context, request EditProjectCreateRequest) (*EditProjectView, error) {
	recipe, err := defaultEditRecipe(request.Kind, request.VideoIDs)
	if err != nil {
		return nil, err
	}
	videos, err := loadEditVideos(ctx, request.VideoIDs)
	if err != nil {
		return nil, err
	}
	raw, err := encodeEditRecipe(recipe)
	if err != nil {
		return nil, err
	}
	title := clampEditTitle(request.Title)
	if title == "" {
		title = clampEditTitle(defaultEditTitle(request.Kind, request.VideoIDs, videos))
	}
	project := models.VideoEditProject{
		Kind: request.Kind, Title: title, Status: models.VideoEditStatusDraft, Revision: 1,
		Mode: models.VideoEditModePrecise, RecipeJSON: raw, AnalysisJSON: "", AcknowledgedJSON: "[]",
	}
	db, err := editDB(ctx)
	if err != nil {
		return nil, err
	}
	if err := db.Create(&project).Error; err != nil {
		return nil, err
	}
	s.emit(VideoEditStateEvent{ProjectID: project.ID, Status: project.Status})
	return s.GetProject(ctx, project.ID)
}

func loadEditProject(ctx context.Context, projectID uint) (models.VideoEditProject, error) {
	var project models.VideoEditProject
	db, err := editDB(ctx)
	if err != nil {
		return project, err
	}
	if err := db.First(&project, projectID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return project, editError("edit_project_not_found", "编辑项目不存在")
		}
		return project, err
	}
	return project, nil
}

func loadEditItems(ctx context.Context, projectID uint) ([]models.VideoEditItem, error) {
	db, err := editDB(ctx)
	if err != nil {
		return nil, err
	}
	var items []models.VideoEditItem
	err = db.Where("project_id = ?", projectID).Order("seq ASC").Find(&items).Error
	return items, err
}

// GetProject 返回项目详情（含导出项）。
func (s *VideoEditService) GetProject(ctx context.Context, projectID uint) (*EditProjectView, error) {
	project, err := loadEditProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	items, err := loadEditItems(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return buildEditProjectView(project, items), nil
}

func buildEditProjectView(project models.VideoEditProject, items []models.VideoEditItem) *EditProjectView {
	recipe, _ := parseEditRecipe(project.RecipeJSON)
	normalizeEditRecipe(&recipe)
	view := &EditProjectView{
		ID: project.ID, Kind: project.Kind, Title: project.Title, Status: project.Status,
		Revision: project.Revision, Mode: project.Mode, Recipe: recipe,
		Analysis: parseEditAnalysis(project.AnalysisJSON), AcknowledgedWarnings: parseEditAcknowledged(project.AcknowledgedJSON),
		ErrorCode: project.ErrorCode, ErrorMessage: project.ErrorMessage,
		CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt, QueuedAt: project.QueuedAt, FinishedAt: project.FinishedAt,
		Items: make([]EditItemView, 0, len(items)),
	}
	for _, item := range items {
		view.Items = append(view.Items, buildEditItemView(item))
	}
	view.Progress = editItemsProgress(items)
	return view
}

func buildEditItemView(item models.VideoEditItem) EditItemView {
	var plan struct {
		Sources []struct {
			VideoID uint `json:"video_id"`
		} `json:"sources"`
	}
	_ = json.Unmarshal([]byte(item.PlanJSON), &plan)
	ids := make([]uint, 0, len(plan.Sources))
	for _, source := range plan.Sources {
		ids = append(ids, source.VideoID)
	}
	return EditItemView{
		ID: item.ID, Seq: item.Seq, Status: item.Status, Phase: item.Phase, Progress: item.Progress,
		OutputName: item.OutputName, OutputVideoID: item.OutputVideoID, SourceVideoIDs: ids,
		ErrorCode: item.ErrorCode, ErrorMessage: item.ErrorMessage, StartedAt: item.StartedAt, FinishedAt: item.FinishedAt,
	}
}

func editItemsProgress(items []models.VideoEditItem) float64 {
	if len(items) == 0 {
		return 0
	}
	total := 0.0
	for _, item := range items {
		if item.Status == models.VideoEditStatusCompleted {
			total++
		} else {
			total += item.Progress
		}
	}
	return total / float64(len(items))
}

func parseEditAcknowledged(raw string) []string {
	keys := []string{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &keys)
	}
	if keys == nil {
		keys = []string{}
	}
	return keys
}

// ListProjects 按 ID 倒序列出最近的项目（limit 默认 50，上限 200）。
func (s *VideoEditService) ListProjects(ctx context.Context, limit int) ([]EditProjectSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	db, err := editDB(ctx)
	if err != nil {
		return nil, err
	}
	var projects []models.VideoEditProject
	if err := db.Order("id DESC").Limit(limit).Find(&projects).Error; err != nil {
		return nil, err
	}
	summaries := make([]EditProjectSummary, 0, len(projects))
	if len(projects) == 0 {
		return summaries, nil
	}
	ids := make([]uint, 0, len(projects))
	for _, project := range projects {
		ids = append(ids, project.ID)
	}
	var items []models.VideoEditItem
	if err := db.Select("id", "project_id", "status", "progress").Where("project_id IN ?", ids).Find(&items).Error; err != nil {
		return nil, err
	}
	byProject := map[uint][]models.VideoEditItem{}
	for _, item := range items {
		byProject[item.ProjectID] = append(byProject[item.ProjectID], item)
	}
	for _, project := range projects {
		projectItems := byProject[project.ID]
		completed := 0
		for _, item := range projectItems {
			if item.Status == models.VideoEditStatusCompleted {
				completed++
			}
		}
		summaries = append(summaries, EditProjectSummary{
			ID: project.ID, Kind: project.Kind, Title: project.Title, Status: project.Status,
			Revision: project.Revision, Mode: project.Mode, ItemCount: len(projectItems), CompletedCount: completed,
			Progress: editItemsProgress(projectItems), ErrorCode: project.ErrorCode, ErrorMessage: project.ErrorMessage,
			CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt, FinishedAt: project.FinishedAt,
		})
	}
	return summaries, nil
}

// UpdateRecipe 以 CAS 更新草稿配方：WHERE id AND revision AND status='draft'，零行时重读并报
// edit_project_conflict（合同「状态模型」）。
func (s *VideoEditService) UpdateRecipe(ctx context.Context, request EditRecipeUpdateRequest) (*EditProjectView, error) {
	project, err := loadEditProject(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	if err := validateEditRecipeStructure(project.Kind, request.Recipe); err != nil {
		return nil, err
	}
	if _, err := loadEditVideos(ctx, editRecipeVideoIDs(request.Recipe)); err != nil {
		return nil, err
	}
	mode := project.Mode
	if request.Mode != "" {
		if !oneOf(request.Mode, models.VideoEditModePrecise, models.VideoEditModeFast) {
			return nil, editError("recipe_invalid", "导出模式只能是 precise 或 fast")
		}
		mode = request.Mode
	}
	title := project.Title
	if trimmed := clampEditTitle(request.Title); trimmed != "" {
		title = trimmed
	}
	raw, err := encodeEditRecipe(request.Recipe)
	if err != nil {
		return nil, err
	}
	db, err := editDB(ctx)
	if err != nil {
		return nil, err
	}
	result := db.Model(&models.VideoEditProject{}).
		Where("id = ? AND revision = ? AND status IN ?", project.ID, request.ExpectedRevision, []string{models.VideoEditStatusDraft}).
		Updates(map[string]any{
			"recipe_json": raw, "revision": gorm.Expr("revision + 1"), "mode": mode, "title": title,
			"updated_at": s.now(),
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, s.conflictError(ctx, project.ID)
	}
	s.emit(VideoEditStateEvent{ProjectID: project.ID, Status: models.VideoEditStatusDraft})
	return s.GetProject(ctx, project.ID)
}

// conflictError 重读项目，按当前状态与 revision 写出 edit_project_conflict 的说明。
func (s *VideoEditService) conflictError(ctx context.Context, projectID uint) error {
	current, err := loadEditProject(ctx, projectID)
	if err != nil {
		return err
	}
	if current.Status != models.VideoEditStatusDraft {
		return editError("edit_project_conflict", "项目当前状态为 %s，配方不可修改（revision %d）", current.Status, current.Revision)
	}
	return editError("edit_project_conflict", "项目已被修改（当前 revision %d），请刷新后重试", current.Revision)
}

// editActiveStatuses 是不能删除、不能改配方的项目状态。
func editActiveStatuses() []string {
	return []string{models.VideoEditStatusQueued, models.VideoEditStatusRunning, models.VideoEditStatusAnalyzing}
}

// DeleteProject 删除非排队/运行/分析中的项目与其导出项记录；从不删除来源或已入库的成品。
func (s *VideoEditService) DeleteProject(ctx context.Context, projectID uint) error {
	project, err := loadEditProject(ctx, projectID)
	if err != nil {
		return err
	}
	if oneOf(project.Status, editActiveStatuses()...) {
		return editError("edit_project_conflict", "项目正在排队、运行或分析，不能删除")
	}
	items, err := loadEditItems(ctx, projectID)
	if err != nil {
		return err
	}
	deleted := false
	err = database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		result := tx.Where("id = ? AND status NOT IN ?", projectID, editActiveStatuses()).Delete(&models.VideoEditProject{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		deleted = true
		return tx.Where("project_id = ?", projectID).Delete(&models.VideoEditItem{}).Error
	})
	if err != nil {
		return err
	}
	if !deleted {
		return s.conflictError(ctx, projectID)
	}
	for _, item := range items {
		removeEditWorkdir(item.WorkDir)
	}
	s.emit(VideoEditStateEvent{ProjectID: projectID, Status: "deleted"})
	return nil
}

// MemoryStatus 只返回内存里的 worker 与分析状态（数据库不可读时用）。
func (s *VideoEditService) MemoryStatus() VideoEditStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return VideoEditStatus{
		WorkerRunning: s.workerRunning, CurrentProjectID: s.currentProject, CurrentItemID: s.currentItemID,
		AnalyzingProjectID: s.analysisProject, AnalysisProgress: s.analysisDone,
	}
}

// Status 返回服务整体状态（含排队/运行项计数；读库失败时计数为 0）。
func (s *VideoEditService) Status(ctx context.Context) VideoEditStatus {
	status := s.MemoryStatus()
	queued, running, err := s.ActiveItemCounts(ctx)
	if err == nil {
		status.QueuedItems, status.RunningItems = queued, running
	}
	return status
}

// ActiveItemCounts 统计排队与运行中的导出项（退出确认与任务中心用）。
func (s *VideoEditService) ActiveItemCounts(ctx context.Context) (queued int, running int, err error) {
	db, err := editDB(ctx)
	if err != nil {
		return 0, 0, err
	}
	var rows []struct {
		Status string
		Count  int
	}
	if err := db.Model(&models.VideoEditItem{}).Select("status, COUNT(*) AS count").
		Where("status IN ?", []string{models.VideoEditStatusQueued, models.VideoEditStatusRunning}).
		Group("status").Scan(&rows).Error; err != nil {
		return 0, 0, err
	}
	for _, row := range rows {
		if row.Status == models.VideoEditStatusQueued {
			queued = row.Count
		} else {
			running = row.Count
		}
	}
	return queued, running, nil
}

func logVideoEdit(format string, args ...any) {
	log.Printf("[VideoEdit] "+format, args...)
}

// ===== worker =====

func (s *VideoEditService) ensureWorker() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kick++
	if s.workerRunning || s.stopping {
		return
	}
	parent := s.parentCtx
	if parent == nil {
		parent = context.Background()
	}
	s.workerRunning = true
	s.worker.Add(1)
	go s.runWorker(parent)
}

// runWorker 按项 ID 先进先出取排队项。退出判定与 ensureWorker 同锁：取空之后若期间有人
// 排了新项（kick 变了）就再取一轮，不会丢唤醒。
func (s *VideoEditService) runWorker(parent context.Context) {
	defer s.worker.Done()
	for {
		s.mu.Lock()
		seen := s.kick
		if s.stopping || parent.Err() != nil {
			s.workerRunning = false
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		item, ok := s.claimNextItem(parent)
		if !ok {
			s.mu.Lock()
			if s.kick != seen && !s.stopping && parent.Err() == nil {
				s.mu.Unlock()
				continue
			}
			s.workerRunning = false
			s.mu.Unlock()
			return
		}
		if s.afterClaimHook != nil {
			s.afterClaimHook()
		}
		ctx, cancel := context.WithCancel(parent)
		s.mu.Lock()
		s.currentItemID = item.ID
		s.currentProject = item.ProjectID
		s.currentCancel = cancel
		// StopAndWait 可能落在「检查 stopping → 领取 → 登记取消函数」之间：那时它没有取消可调，
		// 这里补上，被领取的项随即以 interrupted 收尾，而不是让退出/维护等整段编码跑完。
		if s.stopping {
			cancel()
		}
		s.mu.Unlock()
		s.runItemRegistered(ctx, item)
		cancel()
		s.mu.Lock()
		s.currentItemID = 0
		s.currentProject = 0
		s.currentCancel = nil
		s.mu.Unlock()
	}
}

// claimNextItem 取最老的排队项（所属项目仍在排队或运行），并以条件更新抢占为 running。
// 只领本进程启动之后排队的项目：上一次进程留下的排队/运行项归启动对账（RecoverOnStartup，异步）处理，
// 对账完成之前用户一排队就唤醒 worker 时，不能抢先把它们跑掉。
func (s *VideoEditService) claimNextItem(ctx context.Context) (models.VideoEditItem, bool) {
	for attempt := 0; attempt < 5; attempt++ {
		db, err := editDB(ctx)
		if err != nil {
			return models.VideoEditItem{}, false
		}
		var item models.VideoEditItem
		err = db.Model(&models.VideoEditItem{}).
			Joins("JOIN video_edit_projects ON video_edit_projects.id = video_edit_items.project_id").
			Where("video_edit_items.status = ? AND video_edit_projects.status IN ? AND video_edit_projects.queued_at >= ?",
				models.VideoEditStatusQueued, []string{models.VideoEditStatusQueued, models.VideoEditStatusRunning}, s.bootTime).
			Order("video_edit_items.id ASC").Limit(1).Select("video_edit_items.*").Find(&item).Error
		if err != nil || item.ID == 0 {
			return models.VideoEditItem{}, false
		}
		now := s.now()
		result := db.Model(&models.VideoEditItem{}).
			Where("id = ? AND status = ?", item.ID, models.VideoEditStatusQueued).
			Updates(map[string]any{"status": models.VideoEditStatusRunning, "phase": models.VideoEditPhaseCheck, "started_at": &now})
		if result.Error != nil {
			return models.VideoEditItem{}, false
		}
		if result.RowsAffected == 0 {
			continue
		}
		_ = db.Model(&models.VideoEditProject{}).
			Where("id = ? AND status = ?", item.ProjectID, models.VideoEditStatusQueued).
			Updates(map[string]any{"status": models.VideoEditStatusRunning, "updated_at": now}).Error
		item.Status = models.VideoEditStatusRunning
		item.Phase = models.VideoEditPhaseCheck
		item.StartedAt = &now
		return item, true
	}
	return models.VideoEditItem{}, false
}

// runItemRegistered 跑一个导出项并保证登记表 Begin/End 成对。
func (s *VideoEditService) runItemRegistered(ctx context.Context, item models.VideoEditItem) {
	s.mu.Lock()
	registry := s.registry
	s.mu.Unlock()
	if registry != nil {
		registry.Begin(BackgroundTaskVideoEdit)
		defer registry.End(BackgroundTaskVideoEdit)
	}
	s.emit(VideoEditStateEvent{ProjectID: item.ProjectID, ItemID: item.ID, Status: models.VideoEditStatusRunning,
		ItemStatus: item.Status, Phase: item.Phase})
	var err error
	if s.projectCancelled(ctx, item.ProjectID) {
		err = context.Canceled
	} else {
		err = s.processItem(ctx, item)
	}
	s.finishItem(ctx, item, err)
	s.finalizeProject(ctx, item.ProjectID)
}

// editBackground 给取消之后的收尾写库用：脱离已取消的 ctx，但有期限。
func editBackground(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
}

func (s *VideoEditService) projectCancelled(ctx context.Context, projectID uint) bool {
	s.mu.Lock()
	requested := s.cancelRequested[projectID]
	s.mu.Unlock()
	if requested {
		return true
	}
	bg, cancel := editBackground(ctx)
	defer cancel()
	project, err := loadEditProject(bg, projectID)
	return err == nil && project.Status == models.VideoEditStatusCancelled
}

// finishItem 写导出项终态。成功时发布事务已经写好 completed；失败、取消与中断都删工作目录。
func (s *VideoEditService) finishItem(ctx context.Context, item models.VideoEditItem, runErr error) {
	bg, cancel := editBackground(ctx)
	defer cancel()
	if runErr == nil {
		s.emitItemByID(bg, item.ID)
		return
	}
	if current, err := loadEditItemByID(bg, item.ID); err == nil {
		item = current
	}
	removeEditWorkdir(item.WorkDir)
	status, code := models.VideoEditStatusFailed, VideoEditErrorCode(runErr)
	if errors.Is(runErr, context.Canceled) || ctx.Err() != nil {
		status, code = models.VideoEditStatusInterrupted, "interrupted"
		if s.projectCancelled(bg, item.ProjectID) {
			status, code = models.VideoEditStatusCancelled, "cancelled"
		}
	}
	if code == "" {
		code = "encode_failed"
	}
	var plan editPlan
	_ = json.Unmarshal([]byte(item.PlanJSON), &plan)
	message := scrubEditPaths(runErr.Error(), append(editPlanPaths(plan, item.WorkDir), editWorkdirFor(plan, item.ID))...)
	if status != models.VideoEditStatusFailed {
		message = ""
	}
	now := s.now()
	db, err := editDB(bg)
	if err != nil {
		return
	}
	if err := db.Model(&models.VideoEditItem{}).Where("id = ? AND status = ?", item.ID, models.VideoEditStatusRunning).
		Updates(map[string]any{"status": status, "error_code": code, "error_message": message, "finished_at": &now,
			"work_dir": "", "publish_target": "", "staged_size": 0}).Error; err != nil {
		logVideoEdit("item=%d finish write failed: %v", item.ID, scrubEditMessage(err.Error()))
	}
	if status == models.VideoEditStatusInterrupted {
		s.interruptProject(bg, item.ProjectID)
	}
	logVideoEdit("item=%d project=%d %s code=%s", item.ID, item.ProjectID, status, code)
	s.emitItemByID(bg, item.ID)
}

// interruptProject 把项目与其余排队项一起标 interrupted（重启或维护打断了运行项）。
func (s *VideoEditService) interruptProject(ctx context.Context, projectID uint) {
	db, err := editDB(ctx)
	if err != nil {
		return
	}
	now := s.now()
	_ = db.Model(&models.VideoEditItem{}).Where("project_id = ? AND status = ?", projectID, models.VideoEditStatusQueued).
		Updates(map[string]any{"status": models.VideoEditStatusInterrupted, "error_code": "interrupted", "finished_at": &now}).Error
	_ = db.Model(&models.VideoEditProject{}).
		Where("id = ? AND status IN ?", projectID, []string{models.VideoEditStatusQueued, models.VideoEditStatusRunning}).
		Updates(map[string]any{"status": models.VideoEditStatusInterrupted, "error_code": "interrupted",
			"error_message": "", "finished_at": &now, "updated_at": now}).Error
}

func loadEditItemByID(ctx context.Context, itemID uint) (models.VideoEditItem, error) {
	var item models.VideoEditItem
	db, err := editDB(ctx)
	if err != nil {
		return item, err
	}
	err = db.First(&item, itemID).Error
	return item, err
}

// finalizeProject 在项目没有排队/运行项之后写项目终态：全部成功 completed、部分成功 partial、
// 否则 failed（错误码取第一个失败项）。已取消或已中断的项目保持原状态。
func (s *VideoEditService) finalizeProject(ctx context.Context, projectID uint) {
	bg, cancel := editBackground(ctx)
	defer cancel()
	items, err := loadEditItems(bg, projectID)
	if err != nil {
		return
	}
	completed, firstCode, firstMessage := 0, "", ""
	for _, item := range items {
		switch item.Status {
		case models.VideoEditStatusQueued, models.VideoEditStatusRunning:
			return
		case models.VideoEditStatusCompleted:
			completed++
		default:
			if firstCode == "" {
				firstCode, firstMessage = item.ErrorCode, item.ErrorMessage
			}
		}
	}
	status := models.VideoEditStatusFailed
	switch {
	case completed == len(items):
		status, firstCode, firstMessage = models.VideoEditStatusCompleted, "", ""
	case completed > 0:
		status = models.VideoEditStatusPartial
	}
	now := s.now()
	db, err := editDB(bg)
	if err != nil {
		return
	}
	_ = db.Model(&models.VideoEditProject{}).
		Where("id = ? AND status IN ?", projectID, []string{models.VideoEditStatusQueued, models.VideoEditStatusRunning}).
		Updates(map[string]any{"status": status, "error_code": firstCode, "error_message": firstMessage,
			"finished_at": &now, "updated_at": now}).Error
	s.mu.Lock()
	delete(s.cancelRequested, projectID)
	s.mu.Unlock()
	if project, err := loadEditProject(bg, projectID); err == nil {
		s.emit(VideoEditStateEvent{ProjectID: projectID, Status: project.Status, Progress: editItemsProgress(items)})
	}
}

func (s *VideoEditService) emitItemByID(ctx context.Context, itemID uint) {
	item, err := loadEditItemByID(ctx, itemID)
	if err != nil {
		return
	}
	project, err := loadEditProject(ctx, item.ProjectID)
	if err != nil {
		return
	}
	s.emit(VideoEditStateEvent{ProjectID: item.ProjectID, ItemID: item.ID, Status: project.Status,
		ItemStatus: item.Status, Phase: item.Phase, Progress: item.Progress})
}

// CancelProject：排队项直接 cancelled；运行项取消 context，worker 等 ffmpeg 退出、清理工作目录后
// 写 cancelled。已发布的项保持 completed。取消已取消的项目是幂等的。
func (s *VideoEditService) CancelProject(ctx context.Context, projectID uint) error {
	project, err := loadEditProject(ctx, projectID)
	if err != nil {
		return err
	}
	if project.Status == models.VideoEditStatusCancelled {
		return nil
	}
	if !oneOf(project.Status, models.VideoEditStatusQueued, models.VideoEditStatusRunning) {
		return editError("edit_project_conflict", "项目当前状态为 %s，没有可取消的导出", project.Status)
	}
	db, err := editDB(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	result := db.Model(&models.VideoEditProject{}).
		Where("id = ? AND status IN ?", projectID, []string{models.VideoEditStatusQueued, models.VideoEditStatusRunning}).
		Updates(map[string]any{"status": models.VideoEditStatusCancelled, "error_code": "cancelled",
			"error_message": "", "finished_at": &now, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return s.cancelRecheck(ctx, projectID)
	}
	if err := db.Model(&models.VideoEditItem{}).
		Where("project_id = ? AND status = ?", projectID, models.VideoEditStatusQueued).
		Updates(map[string]any{"status": models.VideoEditStatusCancelled, "error_code": "cancelled", "finished_at": &now}).Error; err != nil {
		return err
	}
	// 只给正在运行的项目登记内存标记（finalizeProject 会清掉它）；只在排队的项目没有收尾这一步，
	// 登记了就永远留在表里。worker 刚领到、还没登记 currentProject 的那一瞬由项目行的 cancelled 状态兜住。
	s.mu.Lock()
	cancel := s.currentCancel
	running := s.currentProject == projectID
	if running {
		s.cancelRequested[projectID] = true
	}
	s.mu.Unlock()
	if running && cancel != nil {
		cancel()
	}
	s.emit(VideoEditStateEvent{ProjectID: projectID, Status: models.VideoEditStatusCancelled})
	return nil
}

// cancelRecheck 处理取消与 worker 收尾的竞争：项目刚好结束时取消是空操作。
func (s *VideoEditService) cancelRecheck(ctx context.Context, projectID uint) error {
	project, err := loadEditProject(ctx, projectID)
	if err != nil {
		return err
	}
	if project.Status == models.VideoEditStatusCancelled {
		return nil
	}
	return editError("edit_project_conflict", "项目已结束（%s），无法取消", project.Status)
}

// EditQueueRequest 排队（或继续未完成项）。acknowledged_warnings 是预检 warnings[].key 的集合。
type EditQueueRequest struct {
	ProjectID            uint     `json:"project_id"`
	ExpectedRevision     uint64   `json:"expected_revision"`
	AcknowledgedWarnings []string `json:"acknowledged_warnings"`
}

// EditQueueResult：queued=false 时 preflight 给出阻止导出的 errors，或 unacknowledged 列出
// 尚未确认的警告 key；queued=true 时 project 是排队后的项目。
type EditQueueResult struct {
	Queued         bool             `json:"queued"`
	Project        *EditProjectView `json:"project"`
	Preflight      *EditPreflight   `json:"preflight"`
	Unacknowledged []string         `json:"unacknowledged"`
}

// QueueProject 重新预检；无错误且全部警告已确认时冻结每个导出项的计划并排队，配方此后只读。
func (s *VideoEditService) QueueProject(ctx context.Context, request EditQueueRequest) (*EditQueueResult, error) {
	project, err := loadEditProject(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	if project.Status != models.VideoEditStatusDraft || project.Revision != request.ExpectedRevision {
		return nil, s.conflictError(ctx, project.ID)
	}
	return s.freezeAndQueue(ctx, project, nil, request.AcknowledgedWarnings)
}

// RequeueProject 对中断、失败或部分成功的项目「继续未完成项」：重新预检并冻结，已完成项不重做。
func (s *VideoEditService) RequeueProject(ctx context.Context, request EditQueueRequest) (*EditQueueResult, error) {
	project, err := loadEditProject(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	if !oneOf(project.Status, models.VideoEditStatusInterrupted, models.VideoEditStatusFailed, models.VideoEditStatusPartial) ||
		project.Revision != request.ExpectedRevision {
		return nil, editError("edit_project_conflict", "只有中断、失败或部分成功的项目可以继续（当前 %s，revision %d）", project.Status, project.Revision)
	}
	items, err := loadEditItems(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	completed := map[int]bool{}
	for _, item := range items {
		if item.Status == models.VideoEditStatusCompleted {
			completed[item.Seq] = true
		}
	}
	return s.freezeAndQueue(ctx, project, completed, request.AcknowledgedWarnings)
}

func editUnacknowledged(warnings []EditIssue, acknowledged []string) (missing []string, kept []string) {
	acked := map[string]bool{}
	for _, key := range acknowledged {
		acked[key] = true
	}
	missing, kept = []string{}, []string{}
	for _, warning := range warnings {
		if acked[warning.Key] {
			kept = append(kept, warning.Key)
		} else {
			missing = append(missing, warning.Key)
		}
	}
	return missing, kept
}

var errEditQueueConflict = errors.New("edit queue conflict")

func (s *VideoEditService) freezeAndQueue(ctx context.Context, project models.VideoEditProject, completedSeqs map[int]bool, acknowledged []string) (*EditQueueResult, error) {
	recipe, err := parseEditRecipe(project.RecipeJSON)
	if err != nil {
		return nil, err
	}
	pre, plans, err := s.preflight(ctx, project, recipe, completedSeqs)
	if err != nil {
		return nil, err
	}
	result := &EditQueueResult{Preflight: pre, Unacknowledged: []string{}}
	if len(pre.Errors) > 0 {
		return result, nil
	}
	missing, kept := editUnacknowledged(pre.Warnings, acknowledged)
	if len(missing) > 0 {
		result.Unacknowledged = missing
		return result, nil
	}
	if len(plans) == 0 {
		return nil, editError("edit_project_conflict", "没有需要导出的项")
	}
	ackRaw, _ := json.Marshal(kept)
	requeue := completedSeqs != nil
	now := s.now()
	err = database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		update := tx.Model(&models.VideoEditProject{}).
			Where("id = ? AND revision = ? AND status = ?", project.ID, project.Revision, project.Status).
			Updates(map[string]any{"status": models.VideoEditStatusQueued, "acknowledged_json": string(ackRaw),
				"queued_at": &now, "finished_at": nil, "error_code": "", "error_message": "", "updated_at": now})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected == 0 {
			return errEditQueueConflict
		}
		for _, plan := range plans {
			raw, err := json.Marshal(plan)
			if err != nil {
				return err
			}
			fields := map[string]any{
				"status": models.VideoEditStatusQueued, "phase": models.VideoEditPhasePending, "progress": 0.0,
				"plan_json": string(raw), "output_name": plan.OutputBase + plan.OutputExt, "publish_target": "",
				"staged_size": 0, "output_path": "", "work_dir": "", "error_code": "", "error_message": "",
				"started_at": nil, "finished_at": nil,
			}
			if requeue {
				res := tx.Model(&models.VideoEditItem{}).
					Where("project_id = ? AND seq = ? AND status IN ?", project.ID, plan.Seq, []string{
						models.VideoEditStatusFailed, models.VideoEditStatusCancelled, models.VideoEditStatusInterrupted}).
					Updates(fields)
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected == 0 {
					return errEditQueueConflict
				}
				continue
			}
			item := models.VideoEditItem{
				ProjectID: project.ID, Seq: plan.Seq, Status: models.VideoEditStatusQueued, Phase: models.VideoEditPhasePending,
				PlanJSON: string(raw), OutputName: plan.OutputBase + plan.OutputExt,
			}
			if err := tx.Create(&item).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errEditQueueConflict) {
		return nil, s.conflictError(ctx, project.ID)
	}
	if err != nil {
		return nil, err
	}
	s.ensureWorker()
	s.emit(VideoEditStateEvent{ProjectID: project.ID, Status: models.VideoEditStatusQueued})
	view, err := s.GetProject(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	result.Queued = true
	result.Project = view
	return result, nil
}

// ===== 生命周期：退出、维护与启动对账 =====

// StopAndWait 停止 worker 与分析并等待退出（退出应用与数据库维护前调用）：运行项由 worker 标
// interrupted、删工作目录；分析回到 draft。之后服务不再领新项，直到 Resume。
func (s *VideoEditService) StopAndWait() {
	s.mu.Lock()
	s.stopping = true
	cancel := s.currentCancel
	analysisCancel := s.analysisCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if analysisCancel != nil {
		analysisCancel()
	}
	s.worker.Wait()
	s.analysis.Wait()
}

// Resume 在维护失败、离开维护模式后恢复领取排队项。
func (s *VideoEditService) Resume() {
	s.mu.Lock()
	s.stopping = false
	s.mu.Unlock()
	s.ensureWorker()
}

// RecoverOnStartup 做启动对账（合同「执行与发布」）：分析中的项目回到 draft；running 项若已写
// publish_target 且文件大小等于 staged_size、库里没有该路径的记录，就补做入库事务，否则删该文件
// 以外的工作目录并标 interrupted；其余 queued/running 项一律 interrupted。
func (s *VideoEditService) RecoverOnStartup(parent context.Context) {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return
	}
	s.parentCtx = parent
	s.worker.Add(1)
	s.mu.Unlock()
	defer s.worker.Done()
	db, err := editDB(parent)
	if err != nil {
		return
	}
	now := s.now()
	interruptedAnalysis, _ := json.Marshal(EditAnalysisResult{Status: "interrupted", Error: "应用重启，分析已中断", FinishedAt: &now})
	_ = db.Model(&models.VideoEditProject{}).Where("status = ? AND updated_at < ?", models.VideoEditStatusAnalyzing, s.bootTime).
		Updates(map[string]any{"status": models.VideoEditStatusDraft, "analysis_json": string(interruptedAnalysis), "updated_at": now}).Error

	var items []models.VideoEditItem
	before := db.Model(&models.VideoEditProject{}).Select("id").Where("queued_at IS NULL OR queued_at < ?", s.bootTime)
	if err := db.Where("status IN ? AND project_id IN (?)", []string{models.VideoEditStatusQueued, models.VideoEditStatusRunning}, before).
		Order("id ASC").Find(&items).Error; err != nil {
		logVideoEdit("startup recover list failed: %v", scrubEditMessage(err.Error()))
		return
	}
	projects := map[uint]bool{}
	for _, item := range items {
		projects[item.ProjectID] = true
		if item.Status == models.VideoEditStatusRunning && item.PublishTarget != "" && item.OutputVideoID == nil {
			if s.reconcilePublishedItem(parent, item) {
				continue
			}
		}
		removeEditWorkdir(item.WorkDir)
		_ = db.Model(&models.VideoEditItem{}).Where("id = ? AND status IN ?", item.ID,
			[]string{models.VideoEditStatusQueued, models.VideoEditStatusRunning}).
			Updates(map[string]any{"status": models.VideoEditStatusInterrupted, "error_code": "interrupted", "error_message": "",
				"work_dir": "", "publish_target": "", "staged_size": 0, "finished_at": &now}).Error
	}
	var stale []models.VideoEditProject
	_ = db.Where("status IN ? AND (queued_at IS NULL OR queued_at < ?)", []string{models.VideoEditStatusQueued, models.VideoEditStatusRunning}, s.bootTime).
		Find(&stale).Error
	for _, project := range stale {
		projects[project.ID] = true
	}
	for projectID := range projects {
		s.recoverProjectStatus(parent, projectID)
	}
	if len(items) > 0 {
		logVideoEdit("startup recover items=%d projects=%d", len(items), len(projects))
	}
}

// recoverProjectStatus：全部项已完成的项目记 completed，否则 interrupted。
func (s *VideoEditService) recoverProjectStatus(ctx context.Context, projectID uint) {
	items, err := loadEditItems(ctx, projectID)
	if err != nil {
		return
	}
	status := models.VideoEditStatusCompleted
	for _, item := range items {
		if item.Status != models.VideoEditStatusCompleted {
			status = models.VideoEditStatusInterrupted
		}
	}
	code := ""
	if status == models.VideoEditStatusInterrupted {
		code = "interrupted"
	}
	now := s.now()
	db, err := editDB(ctx)
	if err != nil {
		return
	}
	_ = db.Model(&models.VideoEditProject{}).
		Where("id = ? AND status IN ?", projectID, []string{models.VideoEditStatusQueued, models.VideoEditStatusRunning}).
		Updates(map[string]any{"status": status, "error_code": code, "error_message": "", "finished_at": &now, "updated_at": now}).Error
}

// PreflightProject 对项目当前配方做预检。中断/失败/部分成功的项目只检查未完成的项（与「继续」一致）。
func (s *VideoEditService) PreflightProject(ctx context.Context, projectID uint) (*EditPreflight, error) {
	project, err := loadEditProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	recipe, err := parseEditRecipe(project.RecipeJSON)
	if err != nil {
		return nil, err
	}
	var completed map[int]bool
	if oneOf(project.Status, models.VideoEditStatusInterrupted, models.VideoEditStatusFailed, models.VideoEditStatusPartial) {
		items, err := loadEditItems(ctx, projectID)
		if err != nil {
			return nil, err
		}
		completed = map[int]bool{}
		for _, item := range items {
			if item.Status == models.VideoEditStatusCompleted {
				completed[item.Seq] = true
			}
		}
	}
	pre, _, err := s.preflight(ctx, project, recipe, completed)
	return pre, err
}
