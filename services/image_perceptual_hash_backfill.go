package services

import (
	"context"
	"errors"
	"log"
	"os"
	"strconv"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"
)

const (
	imagePerceptualHashBackfillMaxFailures = 50
	imagePerceptualHashBackfillErrorLimit  = 500
	imagePerceptualHashBackfillBatchSize   = 500
)

// ErrImagePerceptualHashBackfillBusy 表示补全任务已在运行，拒绝并发启动。
var ErrImagePerceptualHashBackfillBusy = errors.New("图片指纹补全任务运行中")

type ImagePerceptualHashBackfillFailure struct {
	ImageID uint   `json:"image_id"`
	Name    string `json:"name"`
	Error   string `json:"error"`
}

// ImagePerceptualHashBackfillStatus 形态与 ImageEXIFBackfillStatus 一致。
// Total=本轮要检查的活跃图片数；Succeeded=重算出了指纹；Skipped=指纹本来就是新鲜的；
// Failed=取缩略图或读文件失败。
type ImagePerceptualHashBackfillStatus struct {
	Running        bool                                 `json:"running"`
	Cancelled      bool                                 `json:"cancelled"`
	Completed      bool                                 `json:"completed"`
	Total          int                                  `json:"total"`
	Processed      int                                  `json:"processed"`
	Succeeded      int                                  `json:"succeeded"`
	Skipped        int                                  `json:"skipped"`
	Failed         int                                  `json:"failed"`
	CurrentImageID uint                                 `json:"current_image_id"`
	StartedAt      *time.Time                           `json:"started_at,omitempty" ts_type:"string"`
	UpdatedAt      *time.Time                           `json:"updated_at,omitempty" ts_type:"string"`
	Failures       []ImagePerceptualHashBackfillFailure `json:"failures"`
	// Gate 是空闲门状态（D-032）：自动路径被挡住时这里说明原因，显式启动恒为零值。
	Gate TaskGateState `json:"gate"`
}

// imagePerceptualHashThumbnailResolver 是本服务唯一依赖的能力：取（必要时生成）缩略图。
// 取成一个接口是为了让测试不必拉起真实的解码链路。
type imagePerceptualHashThumbnailResolver interface {
	ResolveImageThumbnail(ctx context.Context, imageID uint) (*ImageMedia, error)
}

// ImagePerceptualHashBackfillService 为活跃图片批量补全感知哈希。
//
// 它自己不算哈希也不写库：指纹的计算与落库归 ImageThumbnailService 所有
// （ResolveImageThumbnail → backfillImageMetadata），本服务只负责驱动与记账。
// 这样 HEIC/RAW 的 sips 分支、缩略图缓存淘汰、EXIF 方向全都沿用既有实现，不会长出
// 第二条必然漂移的解码路径。
//
// 有这个服务之前，图片指纹只能靠用户逐张浏览顺带生成：既没有批量入口，清理面板也
// 数不出"还差多少张"，于是近似重复为空时用户无从得知原因。
type ImagePerceptualHashBackfillService struct {
	now        func() time.Time
	thumbnails imagePerceptualHashThumbnailResolver

	mu        sync.Mutex
	stopMu    sync.Mutex
	status    ImagePerceptualHashBackfillStatus
	cancel    context.CancelFunc
	worker    sync.WaitGroup
	emitter   func(ImagePerceptualHashBackfillStatus)
	stopping  bool
	registry  *BackgroundTaskRegistry
	pauseHook TaskPauseHook
}

// NewImagePerceptualHashBackfillService 创建指纹补全服务（单 worker、显式启动）。
func NewImagePerceptualHashBackfillService(thumbnails imagePerceptualHashThumbnailResolver) *ImagePerceptualHashBackfillService {
	return &ImagePerceptualHashBackfillService{
		now:        time.Now,
		thumbnails: thumbnails,
		status:     ImagePerceptualHashBackfillStatus{Failures: []ImagePerceptualHashBackfillFailure{}},
	}
}

// SetEventEmitter 注入进度事件回调（app 层接 Wails 事件 image-perceptual-hash-backfill-progress）。
func (s *ImagePerceptualHashBackfillService) SetEventEmitter(emitter func(ImagePerceptualHashBackfillStatus)) {
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
}

// SetBackgroundTaskRegistry 接入后台任务登记表（D-014）。
func (s *ImagePerceptualHashBackfillService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
}

func (s *ImagePerceptualHashBackfillService) waitForPauseHook(ctx context.Context) error {
	s.mu.Lock()
	hook := s.pauseHook
	s.mu.Unlock()
	if hook == nil {
		return nil
	}
	// 钩子在服务锁之外调用：它会阻塞很久，持锁等待会连 Status/Cancel 一起冻住。
	return hook.Wait(ctx, s.setGateState)
}

func (s *ImagePerceptualHashBackfillService) setGateState(state TaskGateState) {
	s.update(func(status *ImagePerceptualHashBackfillStatus) { status.Gate = state })
}

// StartImagePerceptualHashBackfill 是显式启动路径：同一把锁下摘掉当前这一轮的项间检查点（D-030）。
func (s *ImagePerceptualHashBackfillService) StartImagePerceptualHashBackfill(parent context.Context) (ImagePerceptualHashBackfillStatus, error) {
	return s.start(parent, nil)
}

// StartImagePerceptualHashBackfillWithPauseHook 是自动路径专用：装钩子与翻 Running 在同一把
// 锁里完成；已经在跑的那一轮不补装钩子（它可能是用户显式启动的）。
func (s *ImagePerceptualHashBackfillService) StartImagePerceptualHashBackfillWithPauseHook(parent context.Context, hook TaskPauseHook) (ImagePerceptualHashBackfillStatus, error) {
	return s.start(parent, hook)
}

func (s *ImagePerceptualHashBackfillService) start(parent context.Context, hook TaskPauseHook) (ImagePerceptualHashBackfillStatus, error) {
	if parent == nil {
		parent = context.Background()
	}
	if database.DB == nil {
		return ImagePerceptualHashBackfillStatus{}, errors.New("数据库未初始化")
	}
	if s.thumbnails == nil {
		return ImagePerceptualHashBackfillStatus{}, errors.New("缩略图服务未注入")
	}
	s.mu.Lock()
	releasing := clearReplacedPauseHook(&s.pauseHook, hook)
	if s.stopping {
		s.mu.Unlock()
		releaseTaskPauseHook(releasing)
		return ImagePerceptualHashBackfillStatus{}, errors.New("图片指纹补全任务正在停止")
	}
	if s.status.Running {
		status := cloneImagePerceptualHashBackfillStatus(s.status)
		s.mu.Unlock()
		releaseTaskPauseHook(releasing)
		return status, ErrImagePerceptualHashBackfillBusy
	}
	s.pauseHook = hook
	now := s.now()
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.status = ImagePerceptualHashBackfillStatus{
		Running: true, StartedAt: &now, UpdatedAt: &now,
		Failures: []ImagePerceptualHashBackfillFailure{},
	}
	status, emitter := cloneImagePerceptualHashBackfillStatus(s.status), s.emitter
	registry := s.registry
	s.worker.Add(1)
	s.mu.Unlock()
	releaseTaskPauseHook(releasing)
	registry.Begin(BackgroundTaskImagePerceptualHash)
	emitImagePerceptualHashBackfillStatus(emitter, status)
	go s.run(ctx, registry)
	return status, nil
}

// GetImagePerceptualHashBackfillStatus 返回当前任务状态快照。
func (s *ImagePerceptualHashBackfillService) GetImagePerceptualHashBackfillStatus() ImagePerceptualHashBackfillStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneImagePerceptualHashBackfillStatus(s.status)
}

// CancelImagePerceptualHashBackfill 取消运行中的补全任务。
func (s *ImagePerceptualHashBackfillService) CancelImagePerceptualHashBackfill() error {
	s.mu.Lock()
	cancel, running := s.cancel, s.status.Running
	s.mu.Unlock()
	if !running || cancel == nil {
		return errors.New("图片指纹补全任务未运行")
	}
	cancel()
	return nil
}

// StopAndWait 供 shutdown 与数据库恢复：取消在途任务并等待 worker 退出。
func (s *ImagePerceptualHashBackfillService) StopAndWait() {
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
	s.mu.Lock()
	s.stopping = false
	s.mu.Unlock()
}

// run 是单 worker 主循环：按 id 递增分批扫目标集图片，逐张判断指纹是否还新鲜。
//
// 目标集 = 活跃（is_stale=false）且不在黑名单目录下，与清理审阅同一套口径。
// 判据里另有两项（源文件大小、mtime）要 os.Stat 才知道，写不进 SQL，因此这里扫的是
// 整个目标集而不是一个预筛出的"待补全"集合。Total 取目标集总数，进度条因此是
// "检查到第几张"而不是"补了第几张"——已经新鲜的那些记 Skipped，不碰磁盘解码。
func (s *ImagePerceptualHashBackfillService) run(ctx context.Context, registry *BackgroundTaskRegistry) {
	defer s.worker.Done()
	defer registry.End(BackgroundTaskImagePerceptualHash)
	// 黑名单整轮只读一次：它是"哪些图片算数"的口径，中途变了也应当下一轮再生效，
	// 免得同一轮里前半段和后半段按两套集合跑。
	excluded, err := imageScanExcludedPaths(database.DB)
	if err != nil {
		if ctx.Err() == nil {
			s.update(func(status *ImagePerceptualHashBackfillStatus) {
				status.Failed++
				appendImagePerceptualHashBackfillFailure(status, ImagePerceptualHashBackfillFailure{
					Error: boundedError(err, imagePerceptualHashBackfillErrorLimit),
				})
			})
		}
		s.finish(ctx.Err() != nil, false)
		return
	}
	total, err := s.countActive(ctx, excluded)
	if err != nil {
		if ctx.Err() == nil {
			s.update(func(status *ImagePerceptualHashBackfillStatus) {
				status.Failed++
				appendImagePerceptualHashBackfillFailure(status, ImagePerceptualHashBackfillFailure{
					Error: boundedError(err, imagePerceptualHashBackfillErrorLimit),
				})
			})
		}
		s.finish(ctx.Err() != nil, false)
		return
	}
	s.update(func(status *ImagePerceptualHashBackfillStatus) { status.Total = total })

	// aborted 记录"因查询失败提前退出"，与正常跑完区分开：中断的任务不能报"已完成"。
	aborted := false
	var afterID uint
	for ctx.Err() == nil {
		batch, err := s.loadBatch(ctx, afterID, excluded)
		if err != nil {
			if ctx.Err() == nil {
				aborted = true
				s.update(func(status *ImagePerceptualHashBackfillStatus) {
					status.Failed++
					appendImagePerceptualHashBackfillFailure(status, ImagePerceptualHashBackfillFailure{
						Error: boundedError(err, imagePerceptualHashBackfillErrorLimit),
					})
				})
			}
			break
		}
		if len(batch) == 0 {
			break
		}
		for _, image := range batch {
			if ctx.Err() != nil {
				break
			}
			if err := s.waitForPauseHook(ctx); err != nil {
				break
			}
			// 游标先于处理前进：失败的那张下一批不会再来，不会卡死在同一张上。
			afterID = image.ID
			// SQL 那道黑名单是 LIKE 前缀匹配，路径写法有出入时会漏；这里按路径再
			// 兜一道，与清理审阅同一套判断。记 Skipped 而不是直接跳过，Processed
			// 才追得平 Total。
			if isScanPathExcluded(image.Path, excluded) {
				s.update(func(status *ImagePerceptualHashBackfillStatus) { status.Processed++; status.Skipped++ })
				continue
			}
			s.update(func(status *ImagePerceptualHashBackfillStatus) { status.CurrentImageID = image.ID })
			s.processOne(ctx, image)
		}
	}
	s.finish(ctx.Err() != nil, !aborted)
}

// processOne 处理一张图片并记账。指纹是否新鲜由 imagePerceptualHashIsFresh 判定，
// 与 ImageCleanupService 判"有没有可用指纹"的口径必须一致。
func (s *ImagePerceptualHashBackfillService) processOne(ctx context.Context, image models.Image) {
	info, err := os.Stat(image.Path)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.recordFailure(image, err)
		return
	}
	if imagePerceptualHashIsFresh(image, info.Size(), info.ModTime().UnixNano()) {
		s.update(func(status *ImagePerceptualHashBackfillStatus) { status.Processed++; status.Skipped++ })
		return
	}

	// ResolveImageThumbnail 内部会生成缩略图并顺带回填 width/height 与 dHash。
	if _, err := s.thumbnails.ResolveImageThumbnail(ctx, image.ID); err != nil {
		if ctx.Err() != nil {
			return
		}
		s.recordFailure(image, err)
		return
	}

	// 回读确认指纹真的落库了：ResolveImageThumbnail 返回成功只代表缩略图可用，
	// 回填本身是机会式的（失败只记日志），不回读就会把没补上的算成功。
	var refreshed models.Image
	if err := database.DB.WithContext(ctx).Select("id", "perceptual_hash", "hash_source_size", "hash_source_mod_time_ns").
		First(&refreshed, image.ID).Error; err != nil {
		if ctx.Err() != nil {
			return
		}
		s.recordFailure(image, err)
		return
	}
	if !imagePerceptualHashIsFresh(refreshed, info.Size(), info.ModTime().UnixNano()) {
		s.recordFailure(image, errors.New("缩略图已生成但指纹未回填"))
		return
	}
	s.update(func(status *ImagePerceptualHashBackfillStatus) { status.Processed++; status.Succeeded++ })
}

func (s *ImagePerceptualHashBackfillService) recordFailure(image models.Image, err error) {
	s.update(func(status *ImagePerceptualHashBackfillStatus) {
		status.Processed++
		status.Failed++
		appendImagePerceptualHashBackfillFailure(status, ImagePerceptualHashBackfillFailure{
			ImageID: image.ID, Name: image.Name,
			Error: boundedError(err, imagePerceptualHashBackfillErrorLimit),
		})
	})
}

// imagePerceptualHashIsFresh 与 ImageCleanupService 判"有没有可用指纹"同一套判据：
// 非空、长度合法、能当十六进制解析、且源文件指纹与磁盘一致。两处口径分叉会让补全
// 报完成而清理仍说待补全——少了这里的 ParseUint 就正好差一种：长度对但不是十六进制
// 的值（%016x 写不出来，手改库或将来换写入方能写出来）会被补全记成 Skipped，清理那边
// 却按 stale 计数，补全跑多少遍提示都不消失。
func imagePerceptualHashIsFresh(image models.Image, size int64, modTimeNS int64) bool {
	if len(image.PerceptualHash) != imageCleanupBandCount*2 {
		return false
	}
	if _, err := strconv.ParseUint(image.PerceptualHash, 16, 64); err != nil {
		return false
	}
	return image.HashSourceSize == size && image.HashSourceModTimeNS == modTimeNS
}

// countActive 数目标集大小：活跃、且不在黑名单目录下的图片。
//
// 黑名单这一项是用户 2026-09-20 裁决加上的：目标集本来只看 is_stale，而唯一的
// 消费方（清理审阅）是排黑名单的，于是面板报的 Total 比真正要干的活大一截，任务
// 还会去解码用户明确排除掉的目录。
func (s *ImagePerceptualHashBackfillService) countActive(ctx context.Context, excluded []string) (int, error) {
	var count int64
	query := applyImageExclusions(
		database.DB.WithContext(ctx).Model(&models.Image{}).Where("images.is_stale = ?", false),
		excluded,
	)
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

// loadBatch 取 id > afterID 的目标集图片。指纹是否新鲜无法在 SQL 里判（要 os.Stat），
// 因此不在这里过滤，交给 processOne 逐张判断。
func (s *ImagePerceptualHashBackfillService) loadBatch(ctx context.Context, afterID uint, excluded []string) ([]models.Image, error) {
	var batch []models.Image
	query := applyImageExclusions(
		database.DB.WithContext(ctx).Model(&models.Image{}).
			Select("id", "name", "path", "perceptual_hash", "hash_source_size", "hash_source_mod_time_ns").
			Where("images.is_stale = ? AND images.id > ?", false, afterID),
		excluded,
	)
	err := query.Order("images.id ASC").Limit(imagePerceptualHashBackfillBatchSize).Find(&batch).Error
	return batch, err
}

func (s *ImagePerceptualHashBackfillService) update(update func(*ImagePerceptualHashBackfillStatus)) {
	// 时钟在锁外取值：注入的 now 不应在持锁期间执行任意代码。
	now := s.now()
	s.mu.Lock()
	update(&s.status)
	s.status.UpdatedAt = &now
	status, emitter := cloneImagePerceptualHashBackfillStatus(s.status), s.emitter
	s.mu.Unlock()
	emitImagePerceptualHashBackfillStatus(emitter, status)
}

// finish 结束任务：cancelled 表示被 context 取消，completed 表示整轮确实跑完了。
// 因查询失败提前退出时两者皆为 false——既没取消也没跑完，面板不该显示"已完成"。
func (s *ImagePerceptualHashBackfillService) finish(cancelled, completed bool) {
	now := s.now()
	s.mu.Lock()
	s.status.Running = false
	s.status.Cancelled = cancelled
	s.status.Completed = !cancelled && completed
	s.status.CurrentImageID = 0
	s.status.Gate = TaskGateState{}
	s.status.UpdatedAt = &now
	s.cancel = nil
	status, emitter := cloneImagePerceptualHashBackfillStatus(s.status), s.emitter
	s.mu.Unlock()
	emitImagePerceptualHashBackfillStatus(emitter, status)
	log.Printf("[ImagePerceptualHash] 补全任务结束 cancelled=%v processed=%d succeeded=%d skipped=%d failed=%d",
		cancelled, status.Processed, status.Succeeded, status.Skipped, status.Failed)
}

func appendImagePerceptualHashBackfillFailure(status *ImagePerceptualHashBackfillStatus, failure ImagePerceptualHashBackfillFailure) {
	if len(status.Failures) < imagePerceptualHashBackfillMaxFailures {
		status.Failures = append(status.Failures, failure)
	}
}

func cloneImagePerceptualHashBackfillStatus(status ImagePerceptualHashBackfillStatus) ImagePerceptualHashBackfillStatus {
	status.Failures = append([]ImagePerceptualHashBackfillFailure(nil), status.Failures...)
	return status
}

func emitImagePerceptualHashBackfillStatus(emitter func(ImagePerceptualHashBackfillStatus), status ImagePerceptualHashBackfillStatus) {
	if emitter != nil {
		emitter(status)
	}
}
