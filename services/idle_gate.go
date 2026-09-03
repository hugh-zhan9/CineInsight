package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"video-master/database"
)

// 等待原因（D-031）。面板直接展示，不再翻译成别的措辞。
const (
	IdleWaitReasonUserActive    = "user_active"
	IdleWaitReasonOnBattery     = "on_battery"
	IdleWaitReasonOutsideWindow = "outside_window"
	IdleWaitReasonProbeFailed   = "probe_failed"
)

const (
	defaultIdleThresholdMinutes = 5
	minIdleThresholdMinutes     = 1
	maxIdleThresholdMinutes     = 120
	// 探测与设置快照的缓存时长（D-031：每 30 秒探测一次）。
	defaultIdleProbeInterval = 30 * time.Second
	// bypass 的兜底寿命：正常路径都会主动清除，这一条只防"哪条路径漏了"。
	idleBypassMaxAge = 10 * time.Minute
)

// ErrIdleGateTaskAlreadyWaiting 表示该任务已经有一个自动唤醒在门口排队，本次请求被合并。
var ErrIdleGateTaskAlreadyWaiting = errors.New("该后台任务已有一个自动唤醒在等待空闲")

// IdleGateTaskNotWaitingCode 是 ErrIdleGateTaskNotWaiting 的稳定标识。
//
// 它必须出现在错误文案最前面：前端要据此把"任务刚好已经被放行"与真正的失败区分开，
// 靠整句中文模糊匹配的话，改一个字就静默失效。
const IdleGateTaskNotWaitingCode = "idle_gate_task_not_waiting"

// ErrIdleGateTaskNotWaiting 表示该任务当前没有被空闲门挡住，"立即运行"无从谈起。
var ErrIdleGateTaskNotWaiting = errors.New(IdleGateTaskNotWaitingCode + ": 该后台任务当前没有在等待空闲")

// TaskGateState 是任务状态里的空闲门快照（D-032）。作为新增字段挂在各服务的
// Status 上，既有字段一个不动。
type TaskGateState struct {
	WaitingIdle bool   `json:"waiting_idle"`
	Reason      string `json:"reason"`
}

// TaskPauseHook 是单 worker 服务的项间检查点（D-032）。
//
// 只有自动路径会装钩子；用户显式启动的任务一律不装，且显式启动会摘掉当前这一轮
// 的钩子——摘钩子必须能把已经阻塞在里面的 worker 立刻放出来，所以这里是个接口
// 而不是裸函数。
type TaskPauseHook interface {
	// Wait 在处理下一项之前调用：不空闲时阻塞（ctx 可取消），
	// 阻塞期间通过 notify 把 waiting_idle 回报给服务自己的 Status。
	Wait(ctx context.Context, notify func(TaskGateState)) error
	// Release 放行还阻塞在这个钩子里的 worker（摘钩子时调用）。
	Release()
}

// IdleSchedulerSettings 是空闲门读到的设置快照。
type IdleSchedulerSettings struct {
	Enabled          bool   `json:"enabled"`
	ThresholdMinutes int    `json:"threshold_minutes"`
	RequireACPower   bool   `json:"require_ac_power"`
	WindowStart      string `json:"window_start"`
	WindowEnd        string `json:"window_end"`
}

// IdleWaitingTask 是一个正被空闲门挡住的任务。
type IdleWaitingTask struct {
	TaskKey string    `json:"task_key"`
	Reason  string    `json:"reason"`
	Since   time.Time `json:"since" ts_type:"string"`
}

// IdleSchedulerStatus 是设置页与任务面板读的门控总览。
type IdleSchedulerStatus struct {
	Enabled          bool              `json:"enabled"`
	ThresholdMinutes int               `json:"threshold_minutes"`
	RequireACPower   bool              `json:"require_ac_power"`
	WindowStart      string            `json:"window_start"`
	WindowEnd        string            `json:"window_end"`
	IdleSeconds      int64             `json:"idle_seconds"`
	OnACPower        bool              `json:"on_ac_power"`
	Probed           bool              `json:"probed"`
	ProbeError       string            `json:"probe_error"`
	SettingsError    string            `json:"settings_error"`
	SupportsProbe    bool              `json:"supports_probe"`
	Waiting          []IdleWaitingTask `json:"waiting"`
	BypassTasks      []string          `json:"bypass_tasks"`
}

// IdleSample 是一次系统探测的结果（空闲时长 + 是否接着电源）。
type IdleSample struct {
	Idle      time.Duration
	OnACPower bool
}

// idleWaiter 是一次具体的等待。release 由 Release 关闭，用来把摘钩子的 worker 放出去。
type idleWaiter struct {
	taskKey  string
	isRun    bool
	reason   string
	since    time.Time
	release  chan struct{}
	released bool
}

// idleBypass 是"忽略空闲立即运行"的一次性豁免。
//
// consumed：已经被某次放行用掉；entered：用掉之后任务确实进了登记表。
// 两者一起决定什么时候清除，避免标记永久留在门里（下一轮自动任务会莫名其妙直通）。
type idleBypass struct {
	grantedAt time.Time
	consumed  bool
	entered   bool
}

// IdleGate 让自动触发的后台任务等到机器空闲再跑（D-030..D-032）。
//
// 只挡自动路径：用户显式点的按钮永远直通。开关关闭时 Run 与钩子都是空操作，
// 行为与本切片之前完全一致。
type IdleGate struct {
	probe        func(context.Context) (IdleSample, error)
	loadSettings func() (IdleSchedulerSettings, error)
	now          func() time.Time
	interval     time.Duration

	probeMu sync.Mutex

	mu           sync.Mutex
	sample       IdleSample
	sampleErr    error
	sampledAt    time.Time
	settings     IdleSchedulerSettings
	settingsAt   time.Time
	settingsOK   bool
	settingsErr  string
	waiters      map[string]map[*idleWaiter]struct{}
	runWaiter    map[string]*idleWaiter
	mergedWakes  map[string]int
	bypass       map[string]*idleBypass
	lastRunning  map[string]bool
	wake         chan struct{}
	emitter      func(IdleSchedulerStatus)
	refreshing   bool
	probeSupport bool
}

// NewIdleGate 创建按真实系统探测工作的空闲门。
func NewIdleGate() *IdleGate {
	return &IdleGate{
		probe:        probeSystemIdle,
		loadSettings: loadIdleSchedulerSettings,
		now:          time.Now,
		interval:     defaultIdleProbeInterval,
		waiters:      make(map[string]map[*idleWaiter]struct{}),
		runWaiter:    make(map[string]*idleWaiter),
		mergedWakes:  make(map[string]int),
		bypass:       make(map[string]*idleBypass),
		lastRunning:  make(map[string]bool),
		wake:         make(chan struct{}),
		probeSupport: idleProbeSupported,
	}
}

// SetProbe 替换系统探测实现。生产代码用平台默认实现（darwin 读 ioreg/pmset，
// 其余平台恒为空闲）；集成测试必须能确定性地摆布"用户活跃 / 机器空闲"，
// 真实读数做不到这一点。
func (g *IdleGate) SetProbe(probe func(context.Context) (IdleSample, error)) {
	g.mu.Lock()
	g.probe = probe
	g.sampledAt = time.Time{}
	g.mu.Unlock()
}

// SetProbeInterval 调整探测与设置快照的缓存时长（默认 30 秒）。同为测试注入点。
func (g *IdleGate) SetProbeInterval(interval time.Duration) {
	g.mu.Lock()
	g.interval = interval
	g.sampledAt = time.Time{}
	g.settingsOK = false
	g.mu.Unlock()
}

// SetEventEmitter 注入 idle-scheduler-state 事件回调。
func (g *IdleGate) SetEventEmitter(emitter func(IdleSchedulerStatus)) {
	g.mu.Lock()
	g.emitter = emitter
	g.mu.Unlock()
}

// InvalidateSettings 丢掉设置快照并唤醒等待者：保存设置之后要立刻生效，
// 不能让用户对着一个"已经关了却还在等空闲"的任务干等 30 秒。
func (g *IdleGate) InvalidateSettings() {
	g.mu.Lock()
	g.settingsOK = false
	g.mu.Unlock()
	g.wakeWaiters()
	g.emitStatus()
}

// Run 是自动路径的入口：不空闲时登记等待并阻塞，空闲后执行 start。
//
// 同一个 taskKey 同时只留一个等待者：扫描与实时监听一天能触发很多次自动唤醒，
// 各起一个 goroutine 堵在门后，放行时会一起惊醒。后来者直接合并，返回
// ErrIdleGateTaskAlreadyWaiting。ctx 取消时返回 ctx.Err()，start 不会被调用。
func (g *IdleGate) Run(ctx context.Context, taskKey string, start func(context.Context) error) error {
	if start == nil {
		return errors.New("idle gate: start 不能为空")
	}
	if err := g.wait(ctx, taskKey, nil, true, nil); err != nil {
		return err
	}
	defer g.finishRun(taskKey)
	return start(ctx)
}

// PauseHook 返回给单 worker 服务用的项间检查点。每次自动启动都拿一个新实例：
// "这一轮的钩子被摘掉了"这件事记在实例上（见 idlePauseHook.detached）。
func (g *IdleGate) PauseHook(taskKey string) TaskPauseHook {
	assertBackgroundTaskKey(BackgroundTaskKey(taskKey))
	return &idlePauseHook{gate: g, taskKey: taskKey}
}

type idlePauseHook struct {
	gate    *IdleGate
	taskKey string
	// detached 是一次性的粘性标记：显式启动摘钩子时置上，此后这个实例永久放行。
	//
	// 状态必须挂在实例上而不是按 taskKey 记世代：worker 在服务锁内读出钩子指针、
	// 放掉锁，到走进 Wait 之间有若干条指令的间隙，按 key 记的快照会在这个间隙里
	// 被抬高又比平，worker 于是停在一个已经摘掉的钩子上。读指针与清指针同步在
	// 服务的同一把锁上，所以 worker 要么拿到 nil 直接返回，要么持有的正是即将被
	// 标记 detached 的这个实例——没有第三种情况。
	detached atomic.Bool
}

func (h *idlePauseHook) Wait(ctx context.Context, notify func(TaskGateState)) error {
	return h.gate.wait(ctx, h.taskKey, notify, false, h.detached.Load)
}

// Release 先把实例标成 detached，再唤醒已经登记的等待者。顺序不能颠倒：
// 反过来的话，刚被唤醒的 worker 可能在标记落地之前又转回去等下一轮。
func (h *idlePauseHook) Release() {
	h.detached.Store(true)
	h.gate.Release(h.taskKey)
}

// clearReplacedPauseHook 供各单 worker 服务在启动路径上调用（必须持有服务锁）：
// 显式启动（incoming 为 nil）摘掉当前这一轮的钩子并把它交回调用方去 Release；
// 自动启动（incoming 非 nil）不动既有钩子，由调用方决定装不装。
func clearReplacedPauseHook(current *TaskPauseHook, incoming TaskPauseHook) TaskPauseHook {
	if incoming != nil {
		return nil
	}
	replaced := *current
	*current = nil
	return replaced
}

// releaseTaskPauseHook 把已经阻塞在钩子里的 worker 放出来。必须在服务锁之外调用。
func releaseTaskPauseHook(hook TaskPauseHook) {
	if hook != nil {
		hook.Release()
	}
}

// Release 唤醒该任务停在项间检查点上的 worker（摘钩子时调用）。
//
// 只放行钩子等待者：排队等空闲的自动唤醒（isRun）不受影响——用户显式跑了一轮
// 不代表下一轮自动任务也该无视空闲门。
//
// 还没登记的 worker 由钩子实例上的 detached 标记兜住（见 idlePauseHook），
// 这里只负责把已经睡下去的叫起来。
func (g *IdleGate) Release(taskKey string) {
	g.mu.Lock()
	released := make([]*idleWaiter, 0, len(g.waiters[taskKey]))
	for waiter := range g.waiters[taskKey] {
		if waiter.released || waiter.isRun {
			continue
		}
		waiter.released = true
		released = append(released, waiter)
	}
	g.mu.Unlock()
	for _, waiter := range released {
		close(waiter.release)
	}
}

// RunGatedTaskNow 为一个正被挡住的任务设置一次性 bypass（"忽略空闲立即运行"）。
//
// 没有任何任务在等这把门时直接报错：留下一个没人消费的标记，下一轮自动任务
// 就会在用户不知情的情况下绕过空闲门。
func (g *IdleGate) RunGatedTaskNow(taskKey string) error {
	if !IsBackgroundTaskKey(taskKey) {
		return errors.New("未知的后台任务标识: " + taskKey)
	}
	g.mu.Lock()
	g.pruneBypassLocked()
	if len(g.waiters[taskKey]) == 0 {
		g.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrIdleGateTaskNotWaiting, taskKey)
	}
	g.bypass[taskKey] = &idleBypass{grantedAt: g.now()}
	g.mu.Unlock()
	g.wakeWaiters()
	g.emitStatus()
	return nil
}

// SyncRunningTasks 由登记表变化驱动：记下 bypass 对应的任务是否真的跑起来了，
// 并在它跑完（running→absent）时清掉标记。只看跃迁，不看瞬时集合，
// 免得在 Run 放行与服务 Begin 之间的空窗里误清。
func (g *IdleGate) SyncRunningTasks(running []string) {
	current := make(map[string]bool, len(running))
	for _, key := range running {
		current[key] = true
	}
	g.mu.Lock()
	for key := range current {
		if state, exists := g.bypass[key]; exists && state.consumed {
			state.entered = true
		}
	}
	for key := range g.lastRunning {
		if current[key] {
			continue
		}
		if state, exists := g.bypass[key]; exists && state.consumed {
			delete(g.bypass, key)
		}
	}
	g.lastRunning = current
	g.pruneBypassLocked()
	g.mu.Unlock()
}

// GetIdleSchedulerStatus 返回门控总览（开关、阈值、当前空闲秒数、电源、等待清单）。
// 只读缓存的探测结果：这是界面调用的路径，不能在这里同步 exec 两个命令；
// 缓存过期时在后台补一次探测，刷完照常发 idle-scheduler-state。
func (g *IdleGate) GetIdleSchedulerStatus() IdleSchedulerStatus {
	status := g.status()
	if !status.Probed || g.sampleIsStale() {
		g.refreshSampleInBackground()
	}
	return status
}

// sampleIsStale 报告缓存的探测结果是否已过期。
func (g *IdleGate) sampleIsStale() bool {
	_, _, fresh := g.cachedSample()
	return !fresh
}

// refreshSampleInBackground 后台补一次探测，同一时刻只允许一个在跑：
// 界面轮询与事件回读都会走到这里，不设闸的话会滚成一片 goroutine。
func (g *IdleGate) refreshSampleInBackground() {
	g.mu.Lock()
	if g.refreshing {
		g.mu.Unlock()
		return
	}
	g.refreshing = true
	g.mu.Unlock()
	go func() {
		defer func() {
			g.mu.Lock()
			g.refreshing = false
			g.mu.Unlock()
		}()
		if _, err := g.refreshSample(); err != nil {
			log.Printf("[IdleGate] 空闲探测失败 err=%v", err)
		}
		g.emitStatus()
	}()
}

// finishRun 在 Run 的 start 返回后收尾：任务压根没进登记表（无活可干、或
// 服务已经在跑而提前返回）时，把这次 bypass 一并清掉，不留悬挂状态。
func (g *IdleGate) finishRun(taskKey string) {
	g.mu.Lock()
	state, exists := g.bypass[taskKey]
	if exists && state.consumed && !state.entered {
		delete(g.bypass, taskKey)
	}
	g.pruneBypassLocked()
	g.mu.Unlock()
}

// wait 阻塞到放行；notify 非空时把 waiting_idle 回报给调用方的 Status。
// detached 非空时报告"这个钩子实例已被摘掉"，此时一律放行（只有钩子路径会传）。
func (g *IdleGate) wait(ctx context.Context, taskKey string, notify func(TaskGateState), isRun bool, detached func() bool) error {
	assertBackgroundTaskKey(BackgroundTaskKey(taskKey))
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// 入口先看一眼：显式启动可能在 worker 走到这里之前就把钩子摘了。
	if detached != nil && detached() {
		return nil
	}
	// 先判一次：能直接放行就不登记等待者，免得界面上闪一下"等待空闲"。
	if allowed, _ := g.evaluate(taskKey); allowed {
		return nil
	}
	waiter, err := g.registerWaiter(taskKey, isRun)
	if err != nil {
		return err
	}
	defer g.unregisterWaiter(waiter)
	// evaluate 可能读库、可能 exec 探测，摘钩子若落在那段时间里，Release 遍历不到
	// 还没登记的自己；登记之后再看一眼就补住了这个窗口。
	if detached != nil && detached() {
		return nil
	}

	notifiedReason := ""
	defer func() {
		if notifiedReason != "" && notify != nil {
			notify(TaskGateState{})
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if detached != nil && detached() {
			return nil
		}
		// 先拿唤醒通道再判定：反过来的话，判定与 select 之间发生的「立即运行」
		// 会关掉旧通道、换上新的，这里就等在一个永远不会响的通道上，白白多睡一轮。
		wake := g.wakeChannel()
		allowed, reason := g.evaluate(taskKey)
		if allowed {
			return nil
		}
		g.setWaiterReason(waiter, reason)
		if notify != nil && reason != notifiedReason {
			notify(TaskGateState{WaitingIdle: true, Reason: reason})
			notifiedReason = reason
		}
		timer := time.NewTimer(g.pollInterval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-waiter.release:
			// 钩子被摘掉（用户显式启动了这个任务）：立刻放行，不再管空闲与否。
			timer.Stop()
			return nil
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (g *IdleGate) registerWaiter(taskKey string, isRun bool) (*idleWaiter, error) {
	g.mu.Lock()
	if isRun {
		if existing := g.runWaiter[taskKey]; existing != nil {
			g.mergedWakes[taskKey]++
			merged := g.mergedWakes[taskKey]
			g.mu.Unlock()
			log.Printf("[IdleGate] %s 已有自动唤醒在等待空闲，合并本次请求 merged=%d", taskKey, merged)
			return nil, ErrIdleGateTaskAlreadyWaiting
		}
	}
	waiter := &idleWaiter{taskKey: taskKey, isRun: isRun, since: g.now(), release: make(chan struct{})}
	if g.waiters[taskKey] == nil {
		g.waiters[taskKey] = make(map[*idleWaiter]struct{})
	}
	g.waiters[taskKey][waiter] = struct{}{}
	if isRun {
		g.runWaiter[taskKey] = waiter
	}
	g.mu.Unlock()
	g.emitStatus()
	return waiter, nil
}

func (g *IdleGate) unregisterWaiter(waiter *idleWaiter) {
	g.mu.Lock()
	if group := g.waiters[waiter.taskKey]; group != nil {
		delete(group, waiter)
		if len(group) == 0 {
			delete(g.waiters, waiter.taskKey)
		}
	}
	if waiter.isRun && g.runWaiter[waiter.taskKey] == waiter {
		delete(g.runWaiter, waiter.taskKey)
		delete(g.mergedWakes, waiter.taskKey)
	}
	g.mu.Unlock()
	g.emitStatus()
}

func (g *IdleGate) setWaiterReason(waiter *idleWaiter, reason string) {
	g.mu.Lock()
	if waiter.reason == reason {
		g.mu.Unlock()
		return
	}
	waiter.reason = reason
	g.mu.Unlock()
	g.emitStatus()
}

// evaluate 判定当前是否放行；不放行时给出原因。
func (g *IdleGate) evaluate(taskKey string) (bool, string) {
	settings := g.currentSettings()
	if !settings.Enabled {
		return true, ""
	}
	if g.consumeBypass(taskKey) {
		return true, ""
	}
	sample, err := g.currentSample()
	if err != nil {
		return false, IdleWaitReasonProbeFailed
	}
	if sample.Idle < time.Duration(settings.ThresholdMinutes)*time.Minute {
		return false, IdleWaitReasonUserActive
	}
	if settings.RequireACPower && !sample.OnACPower {
		return false, IdleWaitReasonOnBattery
	}
	if !withinIdleWindow(g.now(), settings.WindowStart, settings.WindowEnd) {
		return false, IdleWaitReasonOutsideWindow
	}
	return true, ""
}

func (g *IdleGate) consumeBypass(taskKey string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneBypassLocked()
	state, exists := g.bypass[taskKey]
	if !exists {
		return false
	}
	state.consumed = true
	return true
}

// pruneBypassLocked 是兜底：正常路径都会主动清除 bypass，这里只防遗漏，
// 免得一个悬挂的标记让日后某次自动任务静默绕过空闲门。
func (g *IdleGate) pruneBypassLocked() {
	if len(g.bypass) == 0 {
		return
	}
	now := g.now()
	for key, state := range g.bypass {
		if now.Sub(state.grantedAt) > idleBypassMaxAge {
			delete(g.bypass, key)
		}
	}
}

func (g *IdleGate) currentSettings() IdleSchedulerSettings {
	now := g.now()
	g.mu.Lock()
	if g.settingsOK && now.Sub(g.settingsAt) < g.interval {
		settings := g.settings
		g.mu.Unlock()
		return settings
	}
	g.mu.Unlock()

	settings, err := g.loadSettings()
	message := ""
	if err != nil {
		// 设置读不出来（库还没就绪、或读失败）时按"门关着"处理：让后台任务
		// 卡在一个连状态都查不到的门后面，比恢复旧行为糟糕得多。如实记一条日志，
		// 并把原因带进 IdleSchedulerStatus，不静默。
		message = boundedError(err, 300)
		log.Printf("[IdleGate] 读取空闲调度设置失败，本轮按关闭处理 err=%v", err)
		settings = IdleSchedulerSettings{}
	}
	settings = normalizeIdleSchedulerSettings(settings)
	g.mu.Lock()
	g.settings = settings
	g.settingsAt = now
	g.settingsOK = true
	g.settingsErr = message
	g.mu.Unlock()
	return settings
}

// currentSample 返回缓存的探测结果，过期则重新探测。
//
// 探测一律用独立的 context.Background()：拿调用方的 ctx 去探测，任何一个任务被
// 取消都会把 context.Canceled 缓存成所有人共享的 probe_failed，整整 30 秒里
// 谁都别想过门。
func (g *IdleGate) currentSample() (IdleSample, error) {
	if sample, err, fresh := g.cachedSample(); fresh {
		return sample, err
	}
	return g.refreshSample()
}

func (g *IdleGate) cachedSample() (IdleSample, error, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sampledAt.IsZero() {
		return IdleSample{}, nil, false
	}
	return g.sample, g.sampleErr, g.now().Sub(g.sampledAt) < g.interval
}

func (g *IdleGate) refreshSample() (IdleSample, error) {
	// 探测要 exec 两个命令，绝不能在持有状态锁的时候做。
	g.probeMu.Lock()
	defer g.probeMu.Unlock()
	if sample, err, fresh := g.cachedSample(); fresh {
		return sample, err
	}
	g.mu.Lock()
	probe := g.probe
	g.mu.Unlock()
	sample, err := probe(context.Background())
	g.mu.Lock()
	g.sample = sample
	g.sampleErr = err
	g.sampledAt = g.now()
	g.mu.Unlock()
	return sample, err
}

func (g *IdleGate) wakeChannel() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.wake == nil {
		g.wake = make(chan struct{})
	}
	return g.wake
}

// wakeWaiters 关闭当前的广播通道，唤醒所有等待者后换上新的。
func (g *IdleGate) wakeWaiters() {
	g.mu.Lock()
	wake := g.wake
	g.wake = make(chan struct{})
	g.mu.Unlock()
	if wake != nil {
		close(wake)
	}
}

func (g *IdleGate) pollInterval() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.interval <= 0 {
		return defaultIdleProbeInterval
	}
	return g.interval
}

func (g *IdleGate) status() IdleSchedulerStatus {
	settings := g.currentSettings()
	sample, sampleErr, _ := g.cachedSample()
	probed := !g.sampledAtIsZero()
	status := IdleSchedulerStatus{
		Enabled:          settings.Enabled,
		ThresholdMinutes: settings.ThresholdMinutes,
		RequireACPower:   settings.RequireACPower,
		WindowStart:      settings.WindowStart,
		WindowEnd:        settings.WindowEnd,
		IdleSeconds:      int64(sample.Idle / time.Second),
		OnACPower:        sample.OnACPower,
		Probed:           probed,
		SupportsProbe:    g.probeSupport,
		Waiting:          []IdleWaitingTask{},
		BypassTasks:      []string{},
	}
	if sampleErr != nil {
		status.ProbeError = boundedError(sampleErr, 500)
		status.IdleSeconds = 0
	}
	g.mu.Lock()
	g.pruneBypassLocked()
	status.SettingsError = g.settingsErr
	for _, key := range backgroundTaskKeyOrder {
		taskKey := string(key)
		if waiter := g.earliestWaiterLocked(taskKey); waiter != nil {
			status.Waiting = append(status.Waiting, IdleWaitingTask{
				TaskKey: taskKey, Reason: waiter.reason, Since: waiter.since,
			})
		}
		if _, exists := g.bypass[taskKey]; exists {
			status.BypassTasks = append(status.BypassTasks, taskKey)
		}
	}
	g.mu.Unlock()
	return status
}

func (g *IdleGate) sampledAtIsZero() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sampledAt.IsZero()
}

func (g *IdleGate) earliestWaiterLocked(taskKey string) *idleWaiter {
	var earliest *idleWaiter
	for waiter := range g.waiters[taskKey] {
		if earliest == nil || waiter.since.Before(earliest.since) {
			earliest = waiter
		}
	}
	return earliest
}

func (g *IdleGate) emitStatus() {
	g.mu.Lock()
	emitter := g.emitter
	g.mu.Unlock()
	if emitter == nil {
		return
	}
	emitter(g.status())
}

// loadIdleSchedulerSettings 从设置表读一份门控设置快照。
func loadIdleSchedulerSettings() (IdleSchedulerSettings, error) {
	if database.DB == nil {
		return IdleSchedulerSettings{}, errors.New("数据库未初始化")
	}
	settings, err := (&SettingsService{}).GetSettings()
	if err != nil {
		return IdleSchedulerSettings{}, err
	}
	return IdleSchedulerSettings{
		Enabled:          settings.IdleSchedulingEnabled,
		ThresholdMinutes: settings.IdleThresholdMinutes,
		RequireACPower:   settings.IdleRequireACPower,
		WindowStart:      settings.IdleWindowStart,
		WindowEnd:        settings.IdleWindowEnd,
	}, nil
}

func normalizeIdleSchedulerSettings(settings IdleSchedulerSettings) IdleSchedulerSettings {
	settings.ThresholdMinutes = NormalizeIdleThresholdMinutes(settings.ThresholdMinutes)
	settings.WindowStart = NormalizeIdleWindowBound(settings.WindowStart)
	settings.WindowEnd = NormalizeIdleWindowBound(settings.WindowEnd)
	return settings
}

// NormalizeIdleThresholdMinutes 把空闲阈值收进 1–120 分钟；非正值取默认 5。
func NormalizeIdleThresholdMinutes(value int) int {
	if value <= 0 {
		return defaultIdleThresholdMinutes
	}
	if value < minIdleThresholdMinutes {
		return minIdleThresholdMinutes
	}
	if value > maxIdleThresholdMinutes {
		return maxIdleThresholdMinutes
	}
	return value
}

// NormalizeIdleWindowBound 只接受 HH:MM；其余一律归一成空（等于不设时间窗）。
func NormalizeIdleWindowBound(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	hour, minute, ok := parseIdleWindowBound(trimmed)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", hour, minute)
}

func parseIdleWindowBound(value string) (int, int, bool) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	hour, ok := parseTwoDigitNumber(parts[0])
	if !ok || hour > 23 {
		return 0, 0, false
	}
	minute, ok := parseTwoDigitNumber(parts[1])
	if !ok || minute > 59 {
		return 0, 0, false
	}
	return hour, minute, true
}

func parseTwoDigitNumber(value string) (int, bool) {
	if len(value) == 0 || len(value) > 2 {
		return 0, false
	}
	total := 0
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, false
		}
		total = total*10 + int(char-'0')
	}
	return total, true
}

// withinIdleWindow 判断当前时间是否落在时间窗内；支持 22:00–06:00 这种跨午夜写法。
// 窗口任一端为空、或两端相同时视为不限时段。
func withinIdleWindow(now time.Time, start, end string) bool {
	startHour, startMinute, startOK := parseIdleWindowBound(start)
	endHour, endMinute, endOK := parseIdleWindowBound(end)
	if !startOK || !endOK {
		return true
	}
	startMinutes := startHour*60 + startMinute
	endMinutes := endHour*60 + endMinute
	if startMinutes == endMinutes {
		return true
	}
	nowMinutes := now.Hour()*60 + now.Minute()
	if startMinutes < endMinutes {
		return nowMinutes >= startMinutes && nowMinutes < endMinutes
	}
	return nowMinutes >= startMinutes || nowMinutes < endMinutes
}
