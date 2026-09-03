package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 图片打标失败留痕 error_code（镜像图片描述链路的边界条件表）。
const (
	imageAITaggingErrorRequestFailed     = "request_failed"
	imageAITaggingErrorDecodeUnsupported = "decode_unsupported"
	imageAITaggingErrorInterrupted       = "interrupted"
	imageAITaggingErrorPersistFailed     = "persist_failed"
	imageAITaggingErrorMetadataStrip     = "metadata_strip_failed"
)

// 跳过原因，落在 image_ai_tagging_states.skip_reason 上，与视频侧取值一致。
const (
	imageAITaggingSkipAlreadyTagged   = "already_tagged"
	imageAITaggingSkipEmptyTagLibrary = "empty_tag_library"
)

const (
	imageAITaggingErrorRuneLimit = 1000
	imageAITaggingMaxFailures    = 50

	// executeOne 的内部结果码，不落库。
	imageAITaggingCodeCancelled = "cancelled"
	imageAITaggingCodeSkipped   = "skipped"
)

// ErrImageAITaggingConfigUnavailable 表示 AI 配置不可用（BaseURL/Model 为空），
// Start/Retag 直接拒绝。
var ErrImageAITaggingConfigUnavailable = errors.New("AI 配置不可用")

// ErrImageAITaggingBusy 表示批量任务或同一张图的重跑正在执行，拒绝并发启动。
var ErrImageAITaggingBusy = errors.New("图片 AI 打标任务运行中")

// ImageAITaggingFailure 是状态面板可见的单图失败留痕（有界列表）。
type ImageAITaggingFailure struct {
	ImageID uint   `json:"image_id"`
	Name    string `json:"name"`
	Code    string `json:"code"`
	Error   string `json:"error"`
}

// ImageAITaggingStatus 形态与图片 EXIF 补全等长任务的状态一致，供进度事件与状态查询共用。
type ImageAITaggingStatus struct {
	Running        bool                    `json:"running"`
	Cancelled      bool                    `json:"cancelled"`
	Completed      bool                    `json:"completed"`
	Total          int                     `json:"total"`
	Processed      int                     `json:"processed"`
	Succeeded      int                     `json:"succeeded"`
	Failed         int                     `json:"failed"`
	Skipped        int                     `json:"skipped"`
	Candidates     int                     `json:"candidates"`
	CurrentImageID uint                    `json:"current_image_id"`
	StartedAt      *time.Time              `json:"started_at,omitempty" ts_type:"string"`
	UpdatedAt      *time.Time              `json:"updated_at,omitempty" ts_type:"string"`
	Failures       []ImageAITaggingFailure `json:"failures"`
	// Gate 是空闲门状态（D-032）：自动路径被挡住时这里说明原因，显式启动恒为零值。
	Gate TaskGateState `json:"gate"`
}

// ImageAITaggingService 管理图片 AI 打标的批量三件套与单张重跑（设计 4.6.6）。
// 与视频侧共享 tags 表与闭合标签词表，但请求是单轮的：静图没有抽帧/字幕/同源可查，
// 视频那套多轮 agent 在这里没有意义。
type ImageAITaggingService struct {
	db             *gorm.DB
	thumbnails     *ImageThumbnailService
	configProvider AITaggingConfigProvider
	clientFactory  func(AITaggingConfig) ImageTaggingClient
	now            func() time.Time

	mu       sync.Mutex
	stopMu   sync.Mutex
	status   ImageAITaggingStatus
	cancel   context.CancelFunc
	worker   sync.WaitGroup
	emitter  func(ImageAITaggingStatus)
	stopping bool
	busy     bool
	// inFlight 是"这张图正在被处理"的认领表，批量与单张重跑共用一张。
	// 只让重跑登记是不够的：批量先 check 再执行，重跑可以在这个窗口里插进来，
	// 于是同一张图被发两次 AI、状态行互相覆盖。两条路径都登记，冲突才真的被挡住。
	// 值是各自的取消函数，供 shutdown 统一取消。
	inFlight  map[uint]context.CancelFunc
	registry  *BackgroundTaskRegistry
	pauseHook TaskPauseHook
}

// NewImageAITaggingService 创建图片 AI 打标服务（单 worker、显式启动）。
func NewImageAITaggingService(db *gorm.DB, thumbnails *ImageThumbnailService, provider AITaggingConfigProvider) *ImageAITaggingService {
	return &ImageAITaggingService{
		db:             db,
		thumbnails:     thumbnails,
		configProvider: provider,
		clientFactory:  NewOpenAICompatibleImageTaggingClient,
		now:            time.Now,
		status:         ImageAITaggingStatus{Failures: []ImageAITaggingFailure{}},
	}
}

// SetEventEmitter 注入进度事件回调（app 层接 Wails 事件 image-ai-tagging-progress）。
func (s *ImageAITaggingService) SetEventEmitter(emitter func(ImageAITaggingStatus)) {
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
}

// prepareClient 校验 AI 配置可用并构造客户端；BaseURL/Model 为空一律拒绝。
// SetBackgroundTaskRegistry 接入后台任务登记表（D-014）。
func (s *ImageAITaggingService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
}

func (s *ImageAITaggingService) waitForPauseHook(ctx context.Context) error {
	s.mu.Lock()
	hook := s.pauseHook
	s.mu.Unlock()
	if hook == nil {
		return nil
	}
	// 钩子在服务锁之外调用：它会阻塞很久，持锁等待会连 Status/Cancel 一起冻住。
	return hook.Wait(ctx, s.setGateState)
}

func (s *ImageAITaggingService) setGateState(state TaskGateState) {
	s.updateStatus(func(status *ImageAITaggingStatus) { status.Gate = state })
}

func (s *ImageAITaggingService) prepareClient() (AITaggingConfig, ImageTaggingClient, error) {
	if s.configProvider == nil {
		return AITaggingConfig{}, nil, fmt.Errorf("%w: 配置提供者缺失", ErrImageAITaggingConfigUnavailable)
	}
	config, err := s.configProvider.Load()
	if err != nil {
		return config, nil, fmt.Errorf("%w: %v", ErrImageAITaggingConfigUnavailable, err)
	}
	if strings.TrimSpace(config.BaseURL) == "" || strings.TrimSpace(config.Model) == "" {
		return config, nil, fmt.Errorf("%w: BaseURL 或 Model 为空", ErrImageAITaggingConfigUnavailable)
	}
	return config, s.clientFactory(config), nil
}

// StartImageAITagging 启动批量打标：目标集 = 活跃图片中打标状态非 processing 的图片；
// 运行中拒绝二次启动。
//
// 目标集只排除 processing，"这张图要不要再发一次 AI"由证据指纹判定，不由状态过滤决定。
// 视频侧的 findUntaggedVideos 把 completed 与 skipped 一起排除在外，代价是：标签库扩充后，
// 已经打过标的媒体永远不会按新词表重新评估，"标签库为空时跑过一次"的媒体更是永久停在 skipped。
// 图片侧不复制这个行为——不发请求的跳过路径代价只有一次状态查询，
// 而标签库变化本来就在证据指纹里，指纹变了就该重打。
// StartImageAITagging 是显式启动路径：同一把锁下摘掉当前这一轮的项间检查点（D-030）。
func (s *ImageAITaggingService) StartImageAITagging(parent context.Context) (ImageAITaggingStatus, error) {
	return s.startImageAITagging(parent, nil)
}

// StartImageAITaggingWithPauseHook 是自动路径专用：装钩子与翻 Running 在同一把锁里完成；
// 已经在跑的那一轮不补装钩子（它可能是用户显式启动的）。
func (s *ImageAITaggingService) StartImageAITaggingWithPauseHook(parent context.Context, hook TaskPauseHook) (ImageAITaggingStatus, error) {
	return s.startImageAITagging(parent, hook)
}

func (s *ImageAITaggingService) startImageAITagging(parent context.Context, hook TaskPauseHook) (ImageAITaggingStatus, error) {
	if parent == nil {
		parent = context.Background()
	}
	config, client, err := s.prepareClient()
	if err != nil {
		s.mu.Lock()
		releasing := clearReplacedPauseHook(&s.pauseHook, hook)
		s.mu.Unlock()
		releaseTaskPauseHook(releasing)
		return ImageAITaggingStatus{}, err
	}
	s.mu.Lock()
	releasing := clearReplacedPauseHook(&s.pauseHook, hook)
	if s.stopping {
		s.mu.Unlock()
		releaseTaskPauseHook(releasing)
		return ImageAITaggingStatus{}, errors.New("图片 AI 打标任务正在停止")
	}
	if s.busy || s.status.Running {
		status := cloneImageAITaggingStatus(s.status)
		s.mu.Unlock()
		releaseTaskPauseHook(releasing)
		return status, ErrImageAITaggingBusy
	}
	s.pauseHook = hook
	now := s.now()
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.busy = true
	s.status = ImageAITaggingStatus{
		Running: true, StartedAt: &now, UpdatedAt: &now,
		Failures: []ImageAITaggingFailure{},
	}
	status, emitter := cloneImageAITaggingStatus(s.status), s.emitter
	registry := s.registry
	s.worker.Add(1)
	s.mu.Unlock()
	releaseTaskPauseHook(releasing)
	registry.Begin(BackgroundTaskImageAITagging)
	emitImageAITaggingStatus(emitter, status)
	go s.run(ctx, config, client, registry)
	return status, nil
}

// GetImageAITaggingStatus 返回当前任务状态快照。
func (s *ImageAITaggingService) GetImageAITaggingStatus() ImageAITaggingStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneImageAITaggingStatus(s.status)
}

// CancelImageAITagging 取消运行中的批量任务；当前图片回退未打标状态后停止。
func (s *ImageAITaggingService) CancelImageAITagging() error {
	s.mu.Lock()
	cancel, running := s.cancel, s.status.Running
	s.mu.Unlock()
	if !running || cancel == nil {
		return errors.New("图片 AI 打标任务未运行")
	}
	cancel()
	return nil
}

// StopAndWait 供 shutdown：取消在途任务（含单张重跑）并等待 worker 退出。
func (s *ImageAITaggingService) StopAndWait() {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	s.mu.Lock()
	s.stopping = true
	cancel := s.cancel
	inFlight := make([]context.CancelFunc, 0, len(s.inFlight))
	for _, itemCancel := range s.inFlight {
		inFlight = append(inFlight, itemCancel)
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, itemCancel := range inFlight {
		itemCancel()
	}
	s.worker.Wait()
	s.mu.Lock()
	s.stopping = false
	s.mu.Unlock()
}

// RetagImage 同步单张重跑。可以与后台批量任务并发：用户点了这一张就应该立刻拿到结果，
// 而不是等一个可能要跑几小时的批量任务结束。冲突只用行级认领来防：
// 同一张图不允许并发重跑两次，批量走到正在重跑的图会跳过。
// 重跑绕过证据指纹判定——用户显式要求重来，就该真的重来。
func (s *ImageAITaggingService) RetagImage(imageID uint) ([]models.ImageAITagCandidate, error) {
	config, client, err := s.prepareClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		cancel()
		return nil, errors.New("图片 AI 打标任务正在停止")
	}
	if _, running := s.inFlight[imageID]; running {
		s.mu.Unlock()
		cancel()
		return nil, ErrImageAITaggingBusy
	}
	if s.inFlight == nil {
		s.inFlight = make(map[uint]context.CancelFunc)
	}
	s.inFlight[imageID] = cancel
	s.worker.Add(1)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inFlight, imageID)
		s.mu.Unlock()
		cancel()
		s.worker.Done()
	}()

	var img models.Image
	if err := s.db.Preload("Tags").First(&img, imageID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("图片 %d 不存在或已删除", imageID)
		}
		return nil, err
	}
	code, _, execErr := s.executeOne(ctx, config, client, img, true)
	switch code {
	case imageAITaggingCodeCancelled:
		return nil, errors.New("图片打标重跑已取消")
	case imageAITaggingCodeSkipped:
		return nil, fmt.Errorf("图片打标已跳过: %v", execErr)
	case "":
	default:
		return nil, fmt.Errorf("图片打标失败（%s）: %v", code, execErr)
	}
	var candidates []models.ImageAITagCandidate
	if err := s.db.Preload("MatchedTag").
		Where("image_id = ? AND status = ?", imageID, models.AITagCandidateStatusPending).
		Order("id ASC").Find(&candidates).Error; err != nil {
		return nil, err
	}
	return candidates, nil
}

// claimImage 尝试认领一张图。认领成功返回 true，调用方必须配对调用 releaseImage。
// 已被另一条路径认领时返回 false。
func (s *ImageAITaggingService) claimImage(imageID uint, cancel context.CancelFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, running := s.inFlight[imageID]; running {
		return false
	}
	if s.inFlight == nil {
		s.inFlight = make(map[uint]context.CancelFunc)
	}
	s.inFlight[imageID] = cancel
	return true
}

func (s *ImageAITaggingService) releaseImage(imageID uint) {
	s.mu.Lock()
	delete(s.inFlight, imageID)
	s.mu.Unlock()
}

// RecoverInterruptedImageTagging 启动恢复：processing → failed/interrupted。
func (s *ImageAITaggingService) RecoverInterruptedImageTagging() error {
	return s.db.Model(&models.ImageAITaggingState{}).
		Where("status = ?", models.AITaggingStateStatusProcessing).
		Updates(map[string]any{
			"status":     models.AITaggingStateStatusFailed,
			"last_error": imageAITaggingErrorInterrupted + ": 图片打标任务被中断",
		}).Error
}

// run 是单 worker 主循环：目标集快照在 worker 内加载，避免持锁做 O(库) 读取。
func (s *ImageAITaggingService) run(ctx context.Context, config AITaggingConfig, client ImageTaggingClient, registry *BackgroundTaskRegistry) {
	defer s.worker.Done()
	defer registry.End(BackgroundTaskImageAITagging)
	inFlight := s.db.Model(&models.ImageAITaggingState{}).Select("image_id").
		Where("status = ?", models.AITaggingStateStatusProcessing)
	var targets []models.Image
	// is_stale 的图片路径已经失效，取缩略图必然失败；放进来只会每轮刷一条
	// decode_unsupported 失败留痕，纯噪音。
	if err := s.db.WithContext(ctx).Model(&models.Image{}).Select("id", "name").
		Where("is_stale = ?", false).
		Where("id NOT IN (?)", inFlight).Order("id ASC").Find(&targets).Error; err != nil {
		if ctx.Err() == nil {
			s.updateStatus(func(status *ImageAITaggingStatus) {
				status.Failed++
				appendImageAITaggingFailure(status, ImageAITaggingFailure{Code: "image_list_failed", Error: boundedError(err, imageAITaggingErrorRuneLimit)})
			})
		}
		s.finish(ctx.Err() != nil)
		return
	}
	s.updateStatus(func(status *ImageAITaggingStatus) { status.Total = len(targets) })
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		if err := s.waitForPauseHook(ctx); err != nil {
			break
		}
		s.updateStatus(func(status *ImageAITaggingStatus) { status.CurrentImageID = target.ID })
		// 任务中被软删/硬删的图片跳过；硬删残留行由 CASCADE 清理。
		var img models.Image
		if err := s.db.WithContext(ctx).Preload("Tags").First(&img, target.ID).Error; err != nil {
			if ctx.Err() != nil {
				break
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				s.updateStatus(func(status *ImageAITaggingStatus) { status.Processed++; status.Skipped++ })
				continue
			}
			s.recordImageTaggingFailure(target.ID, target.Name, "image_load_failed", err)
			continue
		}
		imageCtx, imageCancel := context.WithCancel(ctx)
		if !s.claimImage(img.ID, imageCancel) {
			// 用户已经手动重跑这一张了，批量不再重复调用一次 AI。
			imageCancel()
			s.updateStatus(func(status *ImageAITaggingStatus) { status.Processed++; status.Skipped++ })
			continue
		}
		code, created, execErr := s.executeOne(imageCtx, config, client, img, false)
		s.releaseImage(img.ID)
		imageCancel()
		switch code {
		case "":
			s.updateStatus(func(status *ImageAITaggingStatus) {
				status.Processed++
				status.Succeeded++
				status.Candidates += created
			})
		case imageAITaggingCodeCancelled:
			// 当前图片已回退未打标状态，停止批量。
		case imageAITaggingCodeSkipped:
			s.updateStatus(func(status *ImageAITaggingStatus) { status.Processed++; status.Skipped++ })
		case imageAITaggingErrorDecodeUnsupported:
			// 缩略图不可得：留痕 failed/decode_unsupported 后按"跳过"计数，批量继续。
			message := boundedError(execErr, imageAITaggingErrorRuneLimit)
			s.updateStatus(func(status *ImageAITaggingStatus) {
				status.Processed++
				status.Skipped++
				appendImageAITaggingFailure(status, ImageAITaggingFailure{ImageID: img.ID, Name: img.Name, Code: code, Error: message})
			})
		default:
			s.updateStatus(func(status *ImageAITaggingStatus) {
				status.Processed++
				status.Failed++
				appendImageAITaggingFailure(status, ImageAITaggingFailure{ImageID: img.ID, Name: img.Name, Code: code, Error: boundedError(execErr, imageAITaggingErrorRuneLimit)})
			})
		}
		if code == imageAITaggingCodeCancelled {
			break
		}
	}
	s.finish(ctx.Err() != nil)
}

// executeOne 处理单张图片：闭合词表与已有标签检查 → 证据指纹幂等判定 → 置 processing →
// 取缩略图 JPEG → 剥元数据 → 调 client → 落候选。返回值 (code, created, err)：
// code 为空表示成功，imageAITaggingCodeCancelled 表示已取消，
// imageAITaggingCodeSkipped 表示按 skip_reason 跳过，其余为已落库的 error_code。
// force 为真时绕过证据指纹判定（单张重跑）。
func (s *ImageAITaggingService) executeOne(ctx context.Context, config AITaggingConfig, client ImageTaggingClient, img models.Image, force bool) (string, int, error) {
	// 已有手工标签的图片不打标：这类候选在审阅时本来就会被整体置 superseded，
	// 发出去只是白花一次 AI 调用。
	//
	// 判定必须减去审批记录，不能像视频侧那样只看 hasNonAutomaticTags：AI 标签库里的标签
	// AutomaticKind 全是空串，用户接受过一个候选之后这张图就会被误判成"手工打标"，
	// 从此永久跳过——那正好废掉了「标签库变了就重新评估」这条改进。
	manual, err := s.hasManualOfficialImageTags(ctx, img.ID)
	if err != nil {
		s.markTaggingFailed(img.ID, imageAITaggingErrorPersistFailed, err)
		return imageAITaggingErrorPersistFailed, 0, err
	}
	if manual {
		if err := s.markState(ctx, img.ID, models.AITaggingStateStatusSkipped, imageAITaggingSkipAlreadyTagged, "", ""); err != nil {
			s.markTaggingFailed(img.ID, imageAITaggingErrorPersistFailed, err)
			return imageAITaggingErrorPersistFailed, 0, err
		}
		return imageAITaggingCodeSkipped, 0, errors.New(imageAITaggingSkipAlreadyTagged)
	}
	tags, err := s.loadActiveLibraryTags(ctx)
	if err != nil {
		s.markTaggingFailed(img.ID, imageAITaggingErrorPersistFailed, err)
		return imageAITaggingErrorPersistFailed, 0, err
	}
	if len(tags) == 0 {
		if err := s.markState(ctx, img.ID, models.AITaggingStateStatusSkipped, imageAITaggingSkipEmptyTagLibrary, "", ""); err != nil {
			s.markTaggingFailed(img.ID, imageAITaggingErrorPersistFailed, err)
			return imageAITaggingErrorPersistFailed, 0, err
		}
		return imageAITaggingCodeSkipped, 0, errors.New(imageAITaggingSkipEmptyTagLibrary)
	}

	fingerprint := buildImageEvidenceFingerprint(img, tags)
	if !force {
		skip, err := s.shouldSkipForFingerprint(ctx, img.ID, fingerprint)
		if err != nil {
			s.markTaggingFailed(img.ID, imageAITaggingErrorPersistFailed, err)
			return imageAITaggingErrorPersistFailed, 0, err
		}
		if skip {
			return imageAITaggingCodeSkipped, 0, errors.New("evidence_unchanged")
		}
	}

	if err := s.markProcessing(ctx, img.ID, fingerprint); err != nil {
		if ctx.Err() != nil {
			return imageAITaggingCodeCancelled, 0, ctx.Err()
		}
		s.markTaggingFailed(img.ID, imageAITaggingErrorPersistFailed, err)
		return imageAITaggingErrorPersistFailed, 0, err
	}
	media, err := s.thumbnails.ResolveImageThumbnail(ctx, img.ID)
	if err != nil {
		if ctx.Err() != nil {
			s.rollbackTaggingPending(img.ID)
			return imageAITaggingCodeCancelled, 0, ctx.Err()
		}
		s.markTaggingFailed(img.ID, imageAITaggingErrorDecodeUnsupported, err)
		return imageAITaggingErrorDecodeUnsupported, 0, err
	}
	jpegData, err := os.ReadFile(media.Path)
	if err != nil {
		if ctx.Err() != nil {
			s.rollbackTaggingPending(img.ID)
			return imageAITaggingCodeCancelled, 0, ctx.Err()
		}
		s.markTaggingFailed(img.ID, imageAITaggingErrorDecodeUnsupported, err)
		return imageAITaggingErrorDecodeUnsupported, 0, err
	}
	// 外发前必须剥除元数据。sips 生成的 HEIC/RAW 缩略图会原样保留源图 EXIF（含 GPS），
	// 剥不掉就不外发。
	jpegData, err = StripJPEGMetadataForUpload(jpegData)
	if err != nil {
		s.markTaggingFailed(img.ID, imageAITaggingErrorMetadataStrip, err)
		return imageAITaggingErrorMetadataStrip, 0, err
	}
	// 宽高是解析缩略图时才回填的，这里重读一次，否则首轮打标的 source_summary 记的是 0x0
	// ——审阅者最需要"AI 看到的是多大一张图"这个上下文的时候它恰好是空的。
	var refreshed models.Image
	if err := s.db.WithContext(ctx).Select("id", "width", "height").First(&refreshed, img.ID).Error; err == nil {
		img.Width, img.Height = refreshed.Width, refreshed.Height
	}
	suggestions, err := client.AnalyzeImageTags(ctx, img.ID, buildImageAITaggingPrompt(tags), jpegData)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			s.rollbackTaggingPending(img.ID)
			return imageAITaggingCodeCancelled, 0, err
		}
		log.Printf("[ImageAITagging] analyze failed image_id=%d err=%v", img.ID, err)
		s.markTaggingFailed(img.ID, imageAITaggingErrorRequestFailed, err)
		return imageAITaggingErrorRequestFailed, 0, err
	}
	created, err := s.persistImageSuggestions(img, tags, suggestions, config)
	if err != nil {
		s.markTaggingFailed(img.ID, imageAITaggingErrorPersistFailed, err)
		return imageAITaggingErrorPersistFailed, 0, err
	}
	if err := s.markState(ctx, img.ID, models.AITaggingStateStatusCompleted, "", fingerprint, ""); err != nil {
		s.markTaggingFailed(img.ID, imageAITaggingErrorPersistFailed, err)
		return imageAITaggingErrorPersistFailed, 0, err
	}
	return "", created, nil
}

// hasManualOfficialImageTags 是 hasManualOfficialImageTagsInTx 的非事务版本，
// 两条路径必须用同一套口径，否则同一张图会在打标路径上"有手工标签"、
// 在审阅路径上"没有手工标签"。
func (s *ImageAITaggingService) hasManualOfficialImageTags(ctx context.Context, imageID uint) (bool, error) {
	return s.hasManualOfficialImageTagsInTx(s.db.WithContext(ctx), imageID)
}

// loadActiveLibraryTags 与视频侧 loadActiveTags 同口径：只取 is_system AND is_active。
// 注意不要换成 TagService.GetAITagLibrary——那个只过滤 is_system，会把已停用的标签
// 一起喂给模型。
func (s *ImageAITaggingService) loadActiveLibraryTags(ctx context.Context) ([]models.Tag, error) {
	var tags []models.Tag
	if err := s.db.WithContext(ctx).
		Where("is_system = ? AND is_active = ?", true, true).
		Order("namespace asc, sort_order asc, id asc").
		Find(&tags).Error; err != nil {
		return nil, err
	}
	return tags, nil
}

// buildImageEvidenceFingerprint 是图片侧的证据指纹：图片本体身份 + 提示词里实际出现的标签词表。
// 图片没有字幕/抽帧这些可变证据，本体只要没换过文件、词表没动过，
// 再跑一次 AI 拿到的就是同一份输入。
//
// 刻意不含 PerceptualHash 与 HashSourceModTimeNS：解析缩略图会顺带回填这两列，
// 把它们算进指纹等于把打标自己的副作用算了进去，第一次跑完指纹必然失配，
// 幂等判定会永远失效。
//
// 词表部分刻意不用视频侧的 tagLibraryHash：那个哈希的是 id + name + updated_at，
// 而提示词里只出现 namespace 与 name。改个标签颜色、切一下 review_required、
// 或在某个 namespace 中间插入一个标签（会重排后续标签的 sort_order）都会改写 updated_at，
// 于是整库图片的指纹一起失配 —— 启动时的自动触发会把整个图库无人值守地重新发一遍。
func buildImageEvidenceFingerprint(image models.Image, tags []models.Tag) string {
	payload := map[string]interface{}{
		"image_id":     image.ID,
		"path":         image.Path,
		"name":         image.Name,
		"size":         image.Size,
		"prompt_terms": imagePromptTagTerms(tags),
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// imagePromptTagTerms 返回提示词里真正会出现的词表条目，顺序与提示词一致。
func imagePromptTagTerms(tags []models.Tag) []string {
	terms := make([]string, 0, len(tags))
	for _, tag := range tags {
		if !tag.IsSystem || !tag.IsActive {
			continue
		}
		namespace := strings.TrimSpace(tag.Namespace)
		if namespace == "" {
			namespace = "other"
		}
		terms = append(terms, namespace+":"+tag.Name)
	}
	return terms
}

// shouldSkipForFingerprint 在上次已 completed 且证据指纹未变时跳过重复调用。
// 只认 completed：failed 必须能重跑，否则一次上游抖动会把这张图永久钉死；
// skipped 的两种原因（已手工打标、词表为空）在本函数之前就已重新判定过。
func (s *ImageAITaggingService) shouldSkipForFingerprint(ctx context.Context, imageID uint, fingerprint string) (bool, error) {
	var state models.ImageAITaggingState
	err := s.db.WithContext(ctx).Where("image_id = ?", imageID).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if state.Status != models.AITaggingStateStatusCompleted {
		return false, nil
	}
	return state.EvidenceFingerprint == fingerprint, nil
}

// persistImageSuggestions 把模型建议落成待审候选，语义逐条镜像视频侧 persistSuggestions：
// 丢弃 low 置信度；闭合词表下丢弃库外建议；按 (image_id, normalized_name, pending) 幂等 upsert。
func (s *ImageAITaggingService) persistImageSuggestions(img models.Image, tags []models.Tag, suggestions []AITagSuggestion, config AITaggingConfig) (int, error) {
	tagsByName := make(map[string]models.Tag, len(tags))
	for _, tag := range tags {
		tagsByName[normalizeAITagName(tag.Name)] = tag
	}

	// 重打之前先把这张图旧的待审候选整体置 superseded：模型这轮不再建议的标签、
	// 或者已经被停用/移出词表的标签，不该继续挂在待审列表里等人处理。
	// 本轮仍然建议的标签会在下面被重新写成 pending。
	if err := s.db.Model(&models.ImageAITagCandidate{}).
		Where("image_id = ? AND status = ?", img.ID, models.AITagCandidateStatusPending).
		Update("status", models.AITagCandidateStatusSuperseded).Error; err != nil {
		return 0, err
	}

	// 用户拒绝过的标签不再重复推送：拒绝就是"这张图不要这个标签"，
	// 词表一变就把它重新塞回待审列表，等于让用户反复拒同一个东西。
	rejected, err := s.rejectedCandidateNames(img.ID)
	if err != nil {
		return 0, err
	}

	sourceSummary := buildImageTaggingSourceSummary(img, config)
	emitted := make(map[string]struct{}, len(suggestions))
	created := 0
	for _, suggestion := range suggestions {
		confidence := normalizeAIConfidence(suggestion.Confidence)
		if confidence == "" || confidence == models.AITagConfidenceLow {
			continue
		}
		label := strings.TrimSpace(suggestion.Label)
		normalized := normalizeAITagName(label)
		if normalized == "" {
			continue
		}
		var matchedTagID *uint
		if matched, ok := tagsByName[normalizeAITagName(suggestion.MatchedExistingName)]; ok {
			id := matched.ID
			matchedTagID = &id
			label = matched.Name
			normalized = normalizeAITagName(label)
		} else if matched, ok := tagsByName[normalized]; ok {
			id := matched.ID
			matchedTagID = &id
			label = matched.Name
			normalized = normalizeAITagName(label)
		}
		// 闭合词表：库外标签一律丢弃，绝不新建 tag。
		if matchedTagID == nil {
			log.Printf("[ImageAITagging] drop out-of-library suggestion image_id=%d", img.ID)
			continue
		}
		if _, denied := rejected[normalized]; denied {
			continue
		}
		candidate := models.ImageAITagCandidate{
			ImageID:        img.ID,
			SuggestedName:  label,
			NormalizedName: normalized,
			MatchedTagID:   matchedTagID,
			Confidence:     confidence,
			Reasoning:      truncateRunes(sanitizeSemanticIndexText(strings.TrimSpace(suggestion.Reasoning), config.APIKey), imageAITaggingErrorRuneLimit),
			SourceSummary:  sourceSummary,
			Status:         models.AITagCandidateStatusPending,
		}
		// 同一轮里模型重复给出同一个标签只落一行。去重放在内存里而不是靠数据库的
		// 部分唯一索引：那个索引是并发兜底，不该是正确性的前提——依赖它会让任何
		// 没建索引的库（比如只跑 AutoMigrate 的测试库）行为不同。
		if _, seen := emitted[normalized]; seen {
			continue
		}
		emitted[normalized] = struct{}{}
		if err := s.db.Omit(clause.Associations).Create(&candidate).Error; err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

// rejectedCandidateNames 返回这张图被用户明确拒绝过的标签归一化名。
func (s *ImageAITaggingService) rejectedCandidateNames(imageID uint) (map[string]struct{}, error) {
	var names []string
	if err := s.db.Model(&models.ImageAITagCandidate{}).
		Where("image_id = ? AND status = ?", imageID, models.AITagCandidateStatusRejected).
		Distinct().Pluck("normalized_name", &names).Error; err != nil {
		return nil, err
	}
	denied := make(map[string]struct{}, len(names))
	for _, name := range names {
		denied[name] = struct{}{}
	}
	return denied, nil
}

// buildImageTaggingSourceSummary 记录审阅者判断候选可信度所需的最小上下文：
// AI 实际看到的是缩略图而不是原图。不写入路径，避免绝对路径进库。
func buildImageTaggingSourceSummary(img models.Image, config AITaggingConfig) string {
	model := strings.TrimSpace(config.Model)
	if key := strings.TrimSpace(config.APIKey); key != "" {
		model = strings.ReplaceAll(model, key, "[redacted-secret]")
	}
	payload := map[string]interface{}{
		"source": "thumbnail",
		"width":  img.Width,
		"height": img.Height,
		"model":  model,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(data)
}

// markProcessing upsert 单图打标状态：status=processing 且 attempt_count 自增。
func (s *ImageAITaggingService) markProcessing(ctx context.Context, imageID uint, fingerprint string) error {
	row := models.ImageAITaggingState{
		ImageID: imageID, Status: models.AITaggingStateStatusProcessing,
		EvidenceFingerprint: fingerprint, AttemptCount: 1,
	}
	return s.db.WithContext(ctx).Omit(clause.Associations).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "image_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"status":               models.AITaggingStateStatusProcessing,
			"skip_reason":          "",
			"evidence_fingerprint": fingerprint,
			// Postgres 的 ON CONFLICT DO UPDATE 中，SET 右侧不带限定的列名是歧义的
			// （既可能指目标表列、也可能指 excluded 行），会直接报 42702 导致整批失败，
			// 所以必须用表名限定。
			//
			// 注意不能写成 excluded.attempt_count + 1：excluded 是"本次 INSERT 试图写入的行"，
			// 而那一行的 attempt_count 是下面硬编码的 1，结果会恒等于 2 而不是自增。
			"attempt_count": gorm.Expr("image_ai_tagging_states.attempt_count + 1"),
			"updated_at":    s.now(),
		}),
	}).Create(&row).Error
}

// markState upsert 终态（completed/skipped/failed）。
func (s *ImageAITaggingService) markState(ctx context.Context, imageID uint, status, skipReason, fingerprint, lastError string) error {
	now := s.now()
	row := models.ImageAITaggingState{
		ImageID: imageID, Status: status, SkipReason: skipReason,
		EvidenceFingerprint: fingerprint, LastError: lastError, LastProcessedAt: &now,
	}
	return s.db.WithContext(ctx).Omit(clause.Associations).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "image_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"status":               status,
			"skip_reason":          skipReason,
			"evidence_fingerprint": fingerprint,
			"last_error":           lastError,
			"last_processed_at":    &now,
			"updated_at":           now,
		}),
	}).Create(&row).Error
}

// markTaggingFailed 写失败留痕（error_code 前缀 + 有界 last_error）。
func (s *ImageAITaggingService) markTaggingFailed(imageID uint, code string, cause error) {
	message := code
	if detail := boundedError(cause, imageAITaggingErrorRuneLimit); detail != "" {
		message = code + ": " + detail
	}
	if err := s.markState(context.Background(), imageID, models.AITaggingStateStatusFailed, "", "", message); err != nil {
		log.Printf("[ImageAITagging] 写失败留痕失败 image_id=%d code=%s err=%v", imageID, code, err)
	}
}

// rollbackTaggingPending 取消时把当前图片回退到 pending，使它下次仍在目标集内。
// 只回退仍停在 processing 的行：没有这个守卫，一次取消可能把另一条路径刚写好的
// completed 踩回 pending，而 evidence_fingerprint 还留着，下轮明明证据没变却会重发一次。
func (s *ImageAITaggingService) rollbackTaggingPending(imageID uint) {
	if err := s.db.Model(&models.ImageAITaggingState{}).
		Where("image_id = ? AND status = ?", imageID, models.AITaggingStateStatusProcessing).
		Updates(map[string]any{
			"status":     models.AITaggingStateStatusPending,
			"last_error": "",
		}).Error; err != nil {
		log.Printf("[ImageAITagging] 回退 pending 失败 image_id=%d err=%v", imageID, err)
	}
}

func (s *ImageAITaggingService) recordImageTaggingFailure(imageID uint, name, code string, cause error) {
	s.markTaggingFailed(imageID, code, cause)
	message := boundedError(cause, imageAITaggingErrorRuneLimit)
	s.updateStatus(func(status *ImageAITaggingStatus) {
		status.Processed++
		status.Failed++
		appendImageAITaggingFailure(status, ImageAITaggingFailure{ImageID: imageID, Name: name, Code: code, Error: message})
	})
}

func (s *ImageAITaggingService) updateStatus(mutate func(*ImageAITaggingStatus)) {
	s.mu.Lock()
	mutate(&s.status)
	now := s.now()
	s.status.UpdatedAt = &now
	snapshot, emitter := cloneImageAITaggingStatus(s.status), s.emitter
	s.mu.Unlock()
	emitImageAITaggingStatus(emitter, snapshot)
}

func (s *ImageAITaggingService) finish(cancelled bool) {
	s.mu.Lock()
	s.status.Running = false
	s.status.Cancelled = cancelled
	s.status.Completed = !cancelled
	s.status.CurrentImageID = 0
	s.status.Gate = TaskGateState{}
	now := s.now()
	s.status.UpdatedAt = &now
	s.busy = false
	s.cancel = nil
	snapshot, emitter := cloneImageAITaggingStatus(s.status), s.emitter
	s.mu.Unlock()
	emitImageAITaggingStatus(emitter, snapshot)
}

func appendImageAITaggingFailure(status *ImageAITaggingStatus, failure ImageAITaggingFailure) {
	if len(status.Failures) >= imageAITaggingMaxFailures {
		return
	}
	status.Failures = append(status.Failures, failure)
}

func cloneImageAITaggingStatus(status ImageAITaggingStatus) ImageAITaggingStatus {
	clone := status
	clone.Failures = append([]ImageAITaggingFailure{}, status.Failures...)
	return clone
}

func emitImageAITaggingStatus(emitter func(ImageAITaggingStatus), status ImageAITaggingStatus) {
	if emitter != nil {
		emitter(status)
	}
}
