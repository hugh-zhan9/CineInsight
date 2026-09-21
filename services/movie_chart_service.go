package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"video-master/models"

	"gorm.io/gorm"
)

// MovieChartService 是 movie_chart_entries / movie_chart_marks /
// movie_chart_year_states 三张表的**唯一写入者**（概要设计 §3）。
//
// 依赖方向单向：本服务调 MovieChartSource，适配器不碰数据库、不反向依赖本服务。
// 本文件只放骨架——构造、依赖注入、后台任务的启停与刷新时机（D-MC12）；
// 抓取与补全的写路径在 movie_chart_refresh.go，标记在 movie_chart_marks.go（P-004），
// 读路径在 movie_chart_query.go（P-005）。
//
// 为什么不扩 WatchlistService：那边的状态机、唯一键 (title, kind) 与「用户输入
// 永远优先」都是围绕手输条目建立的，榜单条目由外部列表批量产生、以豆瓣 ID 为身份，
// 塞进同一个服务会让两套生命周期共用一张表和一个 worker（概要设计 §3）。
// **复用的是更下层的东西**：出网客户端、六类失败分类、认领与写回的条件更新模式。
type MovieChartService struct {
	// db 在构造时注入。照 NewImageAITaggingService 的形态而不是直接摸 database.DB：
	// 双后端测试要在每个用例里换一个独立库，摸全局会让用例互相看见对方的数据。
	db *gorm.DB

	// watchlist 是「想看」标记要联动的想看片单服务（D-MC13），同样构造时注入。
	//
	// 依赖方向是单向的 MovieChartService → WatchlistService，片单一侧完全不感知榜单。
	// 它从 MarkEntry / ClearMark 的**入参**收成构造字段（P-005），理由是这条依赖对
	// 本服务是永久的、不是某一次调用的参数：入参形态下每个调用点都得自己记得传，
	// 漏传（传 nil）编译期没有任何拦截，要等运行时点了「想看」才报「想看片单服务
	// 不可用」。收成字段之后，忘记注入在构造点就只有一处可看。
	watchlist *WatchlistService

	// newSource 每轮刷新调**一次**，装配这一轮用的适配器。
	//
	// 不在构造时装配好：出网代理是用户随时可改的设置，装配一次存起来会让改完设置
	// 之后的刷新仍然走旧代理。装配失败（多半是代理地址填错）按六类分类写进年状态，
	// 不静默什么都不做——见 refreshYear。
	newSource func() (MovieChartSource, error)

	// now 是落库时间戳的时钟，测试注入固定时刻来验 30 天判定与 last_refreshed_at。
	now func() time.Time

	// detailInterval 是详情阶段两次**请求**之间的间隔（D-MC06 的 1 次/秒）。
	// 小于等于 0 表示不限速。
	detailInterval time.Duration
	// sleep 是限速用的等待，可被 ctx 打断。单独留一个口子是因为一年约 1500 条详情，
	// 真睡要 25 分钟——测试注入一个只记账不睡的实现，既不拖慢套件，也仍然能断言
	// 「每两次请求之间确实等过 detailInterval」。
	sleep func(ctx context.Context, d time.Duration) error

	// mu 只保护下面这组刷新状态，不保护任何数据库写入——库里的并发不变量靠条件
	// 更新（需求设计文档 §5），不靠进程内的锁。
	mu             sync.Mutex
	tasks          *BackgroundTaskRegistry
	refreshCancel  context.CancelFunc
	refreshDone    chan struct{}
	refreshingYear int

	// markMu 把标记的「读当前状态 → 动片单 → 落标记行」整段串起来，让本服务对
	// movie_chart_marks 成为**事实上的单写者**。它与 mu 各管各的，不嵌套。
	//
	// 为什么需要它：MarkEntry 与 ClearMark 都是先读当前标记、再据此决定要不要建或
	// 删片单条目、最后才落标记行。Wails 给每个绑定调用各起一个 goroutine，用户双击
	// 就能让两次调用交错：A 读到「无标记」→ B 也读到「无标记」→ A 建片单条目 N 并
	// 写 want → B 写 skip 覆盖掉它。最终库里是 skip、watchlist_entry_id=0，而片单
	// 条目 N 再也没有任何标记认领——界面上多出一条追溯不到来源、也撤不掉的「想看」。
	//
	// 为什么是进程内的锁而不是再加一层谓词：需求设计文档 §5 禁的是**数据库**锁
	// （SELECT ... FOR UPDATE、LOCK TABLES、咨询锁），并把「确立单写者」列为优先的
	// 不变量形态。这三张表本来就只由本服务写，这把锁只是把「单写者」从约定变成事实。
	//
	// 锁序：markMu → WatchlistService 内部的锁（Create 唤醒补全 worker 时的 workerMu、
	// Delete 清理海报时的托管图片服务）。片单一侧完全不感知榜单，没有反向获取，
	// 因此不存在环；TriggerEnrichment 往唤醒通道发送时带 default 分支，不会在持锁
	// 期间阻塞。持 markMu 期间也不取 mu：标记路径不碰刷新状态。
	//
	// P-004 把它写成包级变量，只是因为字段得声明在本文件而那个切片不该改这里；
	// 应用只构造一个 MovieChartService，两种写法语义等价，P-005 原样搬成了字段。
	markMu sync.Mutex
}

const (
	// movieChartRequestTimeout 是单次豆瓣请求的总超时，与想看片单补全同值：
	// 回来的是一小段 JSON，取长了只会把一次源侧故障拖成几分钟的无响应。
	movieChartRequestTimeout = 20 * time.Second

	// movieChartDetailInterval 是详情阶段的限速（D-MC06：1 次/秒）。
	movieChartDetailInterval = time.Second

	// movieChartRefreshInterval 是当年榜单的自动重抓间隔（D-MC12：30 天）。
	// 计时基准是**上次成功刷新**，不是自然月，也不是上次尝试。
	movieChartRefreshInterval = 30 * 24 * time.Hour
)

// ErrMovieChartRefreshRunning 表示已经有一轮刷新在跑。
//
// 第二次请求**报错而不是排队**（需求设计文档 §7 末行）：排队会让用户连点几下
// 之后排出一串重复的整年抓取，每一轮都是约 100 次列表请求。
var ErrMovieChartRefreshRunning = errors.New("年度榜单正在刷新，请稍候")

// NewMovieChartService 构造服务。db 为三张新表所在的连接，watchlist 是「想看」
// 标记要联动的想看片单服务（D-MC13），两者都是永久依赖。
func NewMovieChartService(db *gorm.DB, watchlist *WatchlistService) *MovieChartService {
	return &MovieChartService{
		db:             db,
		watchlist:      watchlist,
		newSource:      loadMovieChartSource,
		now:            time.Now,
		detailInterval: movieChartDetailInterval,
		sleep:          movieChartSleep,
	}
}

// loadMovieChartSource 按当前设置装配豆瓣适配器。
//
// 出网一律经 NewWatchlistMetadataHTTPClient（D-MC01）：用户配的资料源代理必须对
// 这条链路同样生效，自建 http.Client 会让代理只覆盖一部分请求；代理地址填错时
// 它返回 ErrWatchlistMetadataProxyInvalid 而**不退回直连**，这条语义本切片不改。
func loadMovieChartSource() (MovieChartSource, error) {
	client, err := NewWatchlistMetadataHTTPClient(LoadWatchlistMetadataConfig(), movieChartRequestTimeout)
	if err != nil {
		return nil, err
	}
	return NewDoubanMovieChartSource(client), nil
}

// movieChartSleep 是可被取消打断的等待。
func movieChartSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// SetBackgroundTaskRegistry 把榜单抓取接进后台任务登记表（D-014），key 为
// movie_chart。
//
// 它**不进空闲门**：刷新由用户打开榜单页或点刷新按钮触发，属用户显式动作。
// 与 BackgroundTaskBrowserDownload、BackgroundTaskWatchlistEnrich 同口径——
// 那道门只挡自动触发的任务（background_task_registry.go:30-34）。
func (s *MovieChartService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks = registry
}

func (s *MovieChartService) backgroundTasks() *BackgroundTaskRegistry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tasks
}

// StartRefresh 起一轮后台刷新，已经在跑时返回 ErrMovieChartRefreshRunning。
//
// 年份的合法区间由绑定层校验（需求设计文档 §6.1），这里不复制一份边界——
// 两处各有一套上下界，早晚会对不上。
func (s *MovieChartService) StartRefresh(year int) error {
	if s == nil {
		return errors.New("年度榜单服务不可用")
	}
	s.mu.Lock()
	if s.refreshCancel != nil {
		busy := s.refreshingYear
		s.mu.Unlock()
		return fmt.Errorf("%w（正在刷新 %d 年）", ErrMovieChartRefreshRunning, busy)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.refreshCancel, s.refreshDone, s.refreshingYear = cancel, done, year
	s.mu.Unlock()

	go func() {
		defer close(done)
		defer cancel()
		defer func() {
			s.mu.Lock()
			s.refreshCancel, s.refreshDone, s.refreshingYear = nil, nil, 0
			s.mu.Unlock()
		}()
		if err := s.refreshYear(ctx, year); err != nil {
			log.Printf("[MovieChart] refresh year=%d ended err=%v", year, err)
		}
	}()
	return nil
}

// CancelRefresh 请求中止当前这一轮。没有在跑时是空操作。
//
// 取消**不是失败**：已 upsert 的条目全部保留，last_refreshed_at 不更新，
// 也不写失败码（需求设计文档 §7）。落实在 refreshYear。
func (s *MovieChartService) CancelRefresh() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.refreshCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// StopRefreshAndWait 取消并等这一轮真的结束，供应用退出时调用。
//
// 取消与「取出要等的那个通道」必须在**同一次持锁**里完成，中间不能放锁（P-005 修）：
// 分成 CancelRefresh 与 waitRefreshDone 两次持锁时，两次之间恰好起来的那一轮会被
// 等待却没有被取消——它要把整年跑完才会关掉 refreshDone，退出因此卡在那里，最坏
// 是详情阶段的约 1500 次限速请求（25 分钟）。反过来，先等再取消同样不行。
//
// 等待放在锁外：refresh goroutine 收尾时要取 s.mu 清空刷新状态，持锁等它关通道
// 就是自锁。cancel() 本身只关一个 context 的通道，不取 s.mu，放在锁内是安全的。
func (s *MovieChartService) StopRefreshAndWait() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.refreshCancel != nil {
		s.refreshCancel()
	}
	done := s.refreshDone
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}

// RefreshStatus 返回当前是否有刷新在跑、跑的是哪一年，供读接口填
// MovieChartCacheState.Refreshing（P-005）。
func (s *MovieChartService) RefreshStatus() (year int, running bool) {
	if s == nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshingYear, s.refreshCancel != nil
}

// EnsureYearRefreshed 是 D-MC12 的刷新时机判定：打开榜单页时调一次，返回是否
// 真的起了一轮。
//
// 两条规则不一样，因为两种年份的过期含义不同：
//
//   - **当年**：距上次**成功**刷新 ≥30 天就重抓，从未成功过（含首次打开）也算到期。
//     触发点是用户打开页面这个动作，不是常驻定时器。但**上一轮失败过就不再自动
//     出网**：源持续不可用时，「从未成功」会让每一次打开页面都变成约 100 次列表
//     请求，正是这种打法会把客户端打进豆瓣的黑名单；§7 不让往年这么干的理由
//     （避免每次打开页面都重试）对当年同样成立。用户什么都不会看不见——概要设计
//     §4.2 的第二形态本来就在页面顶部显示缓存内容、缓存时间、失败原因和刷新按钮，
//     手动刷新任何时候都能用。
//   - **其余年份**（往年与未来年）：只在「从未抓过且本地没有该年缓存」时抓一次。
//     判据里的「从未抓过」看 last_attempt_at 而不是 last_refreshed_at——需求设计
//     文档 §7 第一行明说往年「一条数据都没有且从未成功抓取」时**不**自动出网，
//     只看有没有缓存会让一个抓不成功的往年在每次打开页面时都重抓一遍，那是一条
//     谁也没要求的隐式重试。
//
// 已经有一轮在跑时返回 (false, nil) 而不是错误：这是页面打开的自动路径，不是
// 用户点的刷新按钮，把「已经在刷了」弹成错误没有意义。手动刷新走 StartRefresh，
// 那里照常返回 ErrMovieChartRefreshRunning。
func (s *MovieChartService) EnsureYearRefreshed(year int) (bool, error) {
	if s == nil {
		return false, errors.New("年度榜单服务不可用")
	}
	due, err := s.refreshDue(year)
	if err != nil || !due {
		return false, err
	}
	if err := s.StartRefresh(year); err != nil {
		if errors.Is(err, ErrMovieChartRefreshRunning) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *MovieChartService) refreshDue(year int) (bool, error) {
	state, err := s.loadYearState(year)
	if err != nil {
		return false, err
	}
	if year == s.now().Year() {
		if state != nil && state.LastFailure != "" {
			return false, nil
		}
		if state == nil || state.LastRefreshedAt == nil {
			return true, nil
		}
		return s.now().Sub(*state.LastRefreshedAt) >= movieChartRefreshInterval, nil
	}
	if state != nil && state.LastAttemptAt != nil {
		return false, nil
	}
	var cached int64
	if err := s.db.Model(&models.MovieChartEntry{}).Where("year = ?", year).Count(&cached).Error; err != nil {
		return false, fmt.Errorf("统计 %d 年榜单缓存失败: %w", year, err)
	}
	return cached == 0, nil
}

// loadYearState 读某一年的抓取状态，没有该年的行时返回 (nil, nil)。
func (s *MovieChartService) loadYearState(year int) (*models.MovieChartYearState, error) {
	var state models.MovieChartYearState
	if err := s.db.Where("year = ?", year).First(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 %d 年榜单状态失败: %w", year, err)
	}
	return &state, nil
}
