package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 想看片单在线补全的执行主体：状态机、认领并发、失败分类、中断恢复与进度事件。
//
// 依赖方向与 watchlist_metadata_source.go 声明的一致：本文件调适配器，适配器不
// 回头调本文件。落库、状态机、海报生命周期全部归这里，适配器只负责问外部要数据。
//
// 并发协调只用**条件更新加认领标识**，不用 SELECT ... FOR UPDATE（D-WM05）。
// 仓库别处（services/person_service.go、services/ai_tagging_service.go、
// services/face_review_service.go）用数据库锁，本设计刻意不沿用，也不改写它们
// ——那是另一次决定。不用锁的直接收益是：补全跑多久，片单的增删改查都不被挡住。

// watchlistEnrichRequestTimeout 是单次资料查询的总超时。
//
// 沿用 aiTaggingRequestTimeout 的写法但短得多（那边是 5 分钟）：资料查询回来的
// 是一小段 JSON，不是长推理。取长了只会把一次源侧故障拖成几分钟的 running，
// 用户看着转圈，而进程重启前谁也收不回那条认领。
const watchlistEnrichRequestTimeout = 20 * time.Second

// watchlistEnrichWorkerInterval 是巡检周期。
//
// 常规触发是用户动作（添加条目、手动重试）直接 TriggerEnrichment，这个 ticker
// 只兜两种情况：触发时 worker 恰好在忙一轮，以及上一轮因出网配置坏掉而整批失败。
const watchlistEnrichWorkerInterval = 5 * time.Minute

// ErrWatchlistEnrichmentNotRetryable 表示条目当前的状态不在「可重试」的三个里。
//
// §3.1 的转换表只允许 succeeded / failed / manual → pending：正在补全（running）
// 或已经排着队（pending）的条目再点一次重试，什么都不该发生。静默成功会让用户
// 以为重试生效了，所以如实说出来。
var ErrWatchlistEnrichmentNotRetryable = errors.New("该条目正在补全或已在补全队列中，无需重试")

// WatchlistEnrichProgress 是 watchlist-enrich-progress 事件的载荷（D-WM13）。
//
// 每次条目的补全状态**落定**发一次：认领进 running、写回 succeeded / failed、
// 启动时把残留 running 刷回 pending、用户编辑转 manual、用户手动重试回 pending。
//
// 认领守卫不成立的那条路径**不发**：它按定义没有改变任何状态（§3.1 —— 丢弃结果，
// 不改条目状态，不返回错误），发一条"进度"只会让界面显示一次并不存在的变化。
type WatchlistEnrichProgress struct {
	EntryID uint   `json:"entry_id"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	// Failure 是 §五 六个分类码之一，只在 Status 为 failed 时非空。
	Failure string `json:"failure"`
	// SourceName 是结果来自哪个源，只在 Status 为 succeeded 时非空。
	SourceName string `json:"source_name"`
}

// WatchlistCredits 是 models.WatchlistEntry.Credits 列的存储形状。
//
// 存 JSON 而不是逗号拼接：人名里带逗号（"Smith, Jr."）在源侧是常见形态，拼接后
// 就再也拆不回来了，而 JSON 自己带转义。Genres 列同理，存的是 JSON 字符串数组。
type WatchlistCredits struct {
	Directors []string `json:"directors"`
	Cast      []string `json:"cast"`
}

// watchlistEnrichmentSources 是一轮补全需要的全部出网件。
//
// 两个都**建一次反复用**：NewWatchlistMetadataHTTPClient 返回的客户端自带连接池，
// 每个请求建一个会把池子废掉，也会让代理配置散成好几份。
type watchlistEnrichmentSources struct {
	registry *WatchlistMetadataRegistry
	poster   *http.Client
}

// watchlistEnrichment 是补全 worker 的全部可变状态，挂在 WatchlistService 上。
type watchlistEnrichment struct {
	mu      sync.Mutex
	tasks   *BackgroundTaskRegistry
	emit    func(WatchlistEnrichProgress)
	sources func() (watchlistEnrichmentSources, error)

	workerMu     sync.Mutex
	workerCancel context.CancelFunc
	workerWake   chan struct{}
	runMu        sync.Mutex
}

// loadWatchlistEnrichmentSources 按当前配置装配路由表与海报下载客户端。
//
// 代理地址填错时直接失败，不退回直连——用户配了代理却让请求从本机裸奔出去，是
// 最不该悄悄发生的事。这条错误由 watchlistEnrichmentFailureFor 归成
// proxy_unreachable，认领到的条目照常落 failed 并带上分类码，而不是无声地卡在
// pending 让用户完全看不出哪里不对。
func loadWatchlistEnrichmentSources() (watchlistEnrichmentSources, error) {
	config := LoadWatchlistMetadataConfig()
	registry, err := NewWatchlistMetadataRegistry(config, watchlistEnrichRequestTimeout)
	if err != nil {
		return watchlistEnrichmentSources{}, err
	}
	// 海报下载与资料查询共用同一份代理配置，只是超时更长（图片比 JSON 大得多）。
	poster, err := NewWatchlistPosterHTTPClient(config)
	if err != nil {
		return watchlistEnrichmentSources{}, err
	}
	return watchlistEnrichmentSources{registry: registry, poster: poster}, nil
}

// SetBackgroundTaskRegistry 把补全接进后台任务登记表（D-014），key 为
// watchlist_enrich。
//
// 它**不过空闲门**：补全是用户添加条目或点重试触发的，属用户显式动作。这与
// BackgroundTaskBrowserDownload 同口径——那个 key 的注释已经写明，那道门只挡
// 自动触发的任务。
func (s *WatchlistService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	if s == nil {
		return
	}
	s.enrich.mu.Lock()
	defer s.enrich.mu.Unlock()
	s.enrich.tasks = registry
}

// SetEnrichmentEventEmitter 注入进度事件的投递口。调用方负责短路应用退出后的投递
// ——后台 worker 在应用退出时仍会发事件，那时前端已经没了。
func (s *WatchlistService) SetEnrichmentEventEmitter(emit func(WatchlistEnrichProgress)) {
	if s == nil {
		return
	}
	s.enrich.mu.Lock()
	defer s.enrich.mu.Unlock()
	s.enrich.emit = emit
}

// emitEnrichProgress 按条目当前（调用方已经改好的）字段发一次进度。
func (s *WatchlistService) emitEnrichProgress(entry models.WatchlistEntry) {
	if s == nil {
		return
	}
	s.enrich.mu.Lock()
	emit := s.enrich.emit
	s.enrich.mu.Unlock()
	if emit == nil {
		return
	}
	emit(WatchlistEnrichProgress{
		EntryID:    entry.ID,
		Title:      entry.Title,
		Kind:       entry.Kind,
		Status:     entry.EnrichmentStatus,
		Failure:    entry.EnrichmentError,
		SourceName: entry.SourceName,
	})
}

func (s *WatchlistService) enrichmentSources() (watchlistEnrichmentSources, error) {
	s.enrich.mu.Lock()
	provider := s.enrich.sources
	s.enrich.mu.Unlock()
	if provider == nil {
		provider = loadWatchlistEnrichmentSources
	}
	return provider()
}

func (s *WatchlistService) backgroundTasks() *BackgroundTaskRegistry {
	s.enrich.mu.Lock()
	defer s.enrich.mu.Unlock()
	return s.enrich.tasks
}

// StartEnrichment 启动补全 worker，启动前先做一次中断恢复。
//
// 形态照 AITaggingService.Start：重复调用是空操作，ctx 取消即退出。
func (s *WatchlistService) StartEnrichment(ctx context.Context) {
	if s == nil {
		return
	}
	s.enrich.workerMu.Lock()
	defer s.enrich.workerMu.Unlock()
	if s.enrich.workerCancel != nil {
		return
	}
	if err := s.recoverInterruptedEnrichment(); err != nil {
		log.Printf("[WatchlistEnrich] recover interrupted entries failed err=%v", err)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	s.enrich.workerCancel = cancel
	s.enrich.workerWake = make(chan struct{}, 1)
	go s.enrichmentWorkerLoop(workerCtx, s.enrich.workerWake)
}

// StopEnrichment 让 worker 在当前这一条处理完之后退出。
func (s *WatchlistService) StopEnrichment() {
	if s == nil {
		return
	}
	s.enrich.workerMu.Lock()
	defer s.enrich.workerMu.Unlock()
	if s.enrich.workerCancel != nil {
		s.enrich.workerCancel()
		s.enrich.workerCancel = nil
	}
}

// StopEnrichmentAndWait 停止并等这一轮跑完，供应用退出时调用。
func (s *WatchlistService) StopEnrichmentAndWait() {
	if s == nil {
		return
	}
	s.StopEnrichment()
	s.enrich.runMu.Lock()
	s.enrich.runMu.Unlock() //nolint:staticcheck // 借 runMu 等当前这一轮结束，照 AITaggingService.StopAndWait
}

// TriggerEnrichment 唤醒 worker 立刻跑一轮，worker 没起来时返回 false。
//
// 用带缓冲的信号而不是直接开 goroutine：用户连着添加十条，只需要 worker 醒一次，
// 它一轮就会把所有 pending 都取走。
func (s *WatchlistService) TriggerEnrichment() bool {
	if s == nil {
		return false
	}
	s.enrich.workerMu.Lock()
	wake := s.enrich.workerWake
	running := s.enrich.workerCancel != nil
	s.enrich.workerMu.Unlock()
	if !running || wake == nil {
		return false
	}
	select {
	case wake <- struct{}{}:
	default:
	}
	return true
}

func (s *WatchlistService) enrichmentWorkerLoop(ctx context.Context, wake <-chan struct{}) {
	s.runEnrichmentOnce(ctx)
	ticker := time.NewTicker(watchlistEnrichWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			s.runEnrichmentOnce(ctx)
		case <-ticker.C:
			s.runEnrichmentOnce(ctx)
		}
	}
}

// recoverInterruptedEnrichment 把进程重启后残留的 running 刷回 pending（§3.1），
// 思路照 AITaggingService.recoverInterruptedRuns。
//
// 认领标识一并作废：进程都换了，那次认领不可能再写回，留着只会在下一轮的 ABA
// 比对里多一个没有主人的旧值。
func (s *WatchlistService) recoverInterruptedEnrichment() error {
	var entries []models.WatchlistEntry
	if err := database.DB.Where("enrichment_status = ?", models.WatchlistEnrichmentRunning).
		Order("id ASC").Find(&entries).Error; err != nil {
		return fmt.Errorf("读取残留的补全条目失败: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}
	if err := database.DB.Model(&models.WatchlistEntry{}).
		Where("enrichment_status = ?", models.WatchlistEnrichmentRunning).
		Updates(map[string]any{
			"enrichment_status": models.WatchlistEnrichmentPending,
			"enrichment_claim":  "",
		}).Error; err != nil {
		return fmt.Errorf("恢复残留的补全条目失败: %w", err)
	}
	log.Printf("[WatchlistEnrich] recovered interrupted entries count=%d", len(entries))
	for _, entry := range entries {
		entry.EnrichmentStatus = models.WatchlistEnrichmentPending
		entry.EnrichmentClaim = ""
		s.emitEnrichProgress(entry)
	}
	return nil
}

// runEnrichmentOnce 跑一轮：取全部 pending，逐条认领并处理。
//
// 不分批、不设上限：用户已裁决片单尚无存量条目，这里不为假想的积压加界限。
// 顺序按 id 升序，正好走 idx_watchlist_enrichment(enrichment_status, id)。
func (s *WatchlistService) runEnrichmentOnce(ctx context.Context) {
	s.enrich.runMu.Lock()
	defer s.enrich.runMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	var ids []uint
	if err := database.DB.Model(&models.WatchlistEntry{}).
		Where("enrichment_status = ?", models.WatchlistEnrichmentPending).
		Order("id ASC").Pluck("id", &ids).Error; err != nil {
		log.Printf("[WatchlistEnrich] list pending entries failed err=%v", err)
		return
	}
	if len(ids) == 0 {
		return
	}

	tasks := s.backgroundTasks()
	tasks.Begin(BackgroundTaskWatchlistEnrich)
	defer tasks.End(BackgroundTaskWatchlistEnrich)

	// 出网件装配失败（多半是代理地址填错）不让这一轮悄悄什么都不做：照常认领，
	// 每条都落 failed 加分类码，用户才看得见「去看代理配置」这件待办。
	sources, sourcesErr := s.enrichmentSources()
	if sourcesErr != nil {
		log.Printf("[WatchlistEnrich] metadata sources unavailable err=%v", sourcesErr)
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		entry, claimed, err := s.claimEnrichment(id)
		if err != nil {
			log.Printf("[WatchlistEnrich] claim entry id=%d failed err=%v", id, err)
			continue
		}
		if !claimed {
			// 影响 0 行：另一个 worker 已经持有，或用户刚编辑/删除过。跳过该条，
			// 不报错——这是转换表里的正常分支（§3.1 第二行）。
			continue
		}
		s.emitEnrichProgress(entry)
		if sourcesErr != nil {
			s.settleEnrichmentFailure(entry, watchlistEnrichmentFailureFor(sourcesErr), sourcesErr)
			continue
		}
		s.enrichClaimedEntry(ctx, entry, sources)
	}
}

// newWatchlistEnrichmentClaim 生成认领标识：16 字节随机数的十六进制，正好 32 个
// 字符，与 EnrichmentClaim 的 size:32 对齐。
//
// 不要换成 uuid.NewString()：那是 36 个字符，SQLite 不校验长度会默默截断，
// Postgres 直接报 value too long——同一处改动在两个后端上一个静默错、一个直接崩。
func newWatchlistEnrichmentClaim() (string, error) {
	var buffer [16]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "", fmt.Errorf("生成补全认领标识失败: %w", err)
	}
	return hex.EncodeToString(buffer[:]), nil
}

// claimEnrichment 用条件更新认领一条 pending，返回认领成功后的条目。
//
// 认领的判据只有「影响了几行」：1 行就是抢到了，0 行就是没抢到。没抢到不是错误
// ——另一个 worker 已经持有，或用户在这中间编辑/删除了条目。
func (s *WatchlistService) claimEnrichment(id uint) (models.WatchlistEntry, bool, error) {
	claim, err := newWatchlistEnrichmentClaim()
	if err != nil {
		return models.WatchlistEntry{}, false, err
	}
	result := database.DB.Model(&models.WatchlistEntry{}).
		Where("id = ? AND enrichment_status = ?", id, models.WatchlistEnrichmentPending).
		Updates(map[string]any{
			"enrichment_status": models.WatchlistEnrichmentRunning,
			"enrichment_claim":  claim,
		})
	if result.Error != nil {
		return models.WatchlistEntry{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return models.WatchlistEntry{}, false, nil
	}
	var entry models.WatchlistEntry
	if err := database.DB.First(&entry, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 认领成功到读回来之间被删掉了。当没抢到处理，没有任何后续要做。
			return models.WatchlistEntry{}, false, nil
		}
		return models.WatchlistEntry{}, false, err
	}
	// 读回来的行必须仍然是本次认领：认领与读之间用户可能已经编辑过（→ manual）。
	// 不是的话就当没抢到——继续做下去，写回也必然被守卫丢弃，白跑一趟出网。
	if entry.EnrichmentClaim != claim || entry.EnrichmentStatus != models.WatchlistEnrichmentRunning {
		return models.WatchlistEntry{}, false, nil
	}
	return entry, true, nil
}

// writeBackEnrichment 带认领守卫写回，返回是否真的写进去了。
//
// 守卫是**状态加认领标识**两项，缺一不可。状态会回到 pending（用户重试），只比对
// 状态值会让上一轮的慢响应落到新一轮头上，把用户刚重试的结果覆盖成旧的——这就是
// §3.1 讨论的 ABA。
//
// 影响 0 行不是错误：它意味着用户在补全期间编辑或删除了条目，用户输入永远优先
// （D-WM06）。调用方据此丢弃结果并记日志，不改条目状态，不往上抛错。
func (s *WatchlistService) writeBackEnrichment(id uint, claim string, fields map[string]any) (bool, error) {
	result := database.DB.Model(&models.WatchlistEntry{}).
		Where("id = ? AND enrichment_status = ? AND enrichment_claim = ?",
			id, models.WatchlistEnrichmentRunning, claim).
		Updates(fields)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// enrichClaimedEntry 处理一条已经认领到的条目：问源、下海报、写回。
func (s *WatchlistService) enrichClaimedEntry(ctx context.Context, entry models.WatchlistEntry, sources watchlistEnrichmentSources) {
	detail, err := lookupWatchlistDetail(ctx, sources.registry, WatchlistMetadataKind(entry.Kind), entry.Title)
	if err != nil {
		s.settleEnrichmentFailure(entry, watchlistEnrichmentFailureFor(err), err)
		return
	}
	// 源侧 ID 是承重字段（重查详情与去重都靠它），装不下就只能算源返回了不可用的
	// 数据。截断存进去更糟：那条 ID 再也回不到源上，而库里看着像是成功了。
	if len(detail.SourceItemID) > watchlistSourceItemIDLimit {
		cause := fmt.Errorf("源条目 ID 超过 %d 字节", watchlistSourceItemIDLimit)
		s.settleEnrichmentFailure(entry, WatchlistMetadataFailureSourceError, cause)
		return
	}

	// §3.2 的回滚边界：写回是单条 UPDATE，与海报落盘不在同一原子单元，所以**先
	// 落盘图片、再写回数据库**。反过来会留下指向不存在文件的路径。
	poster := managedImageImport{}
	if strings.TrimSpace(detail.PosterURL) != "" {
		imported, posterErr := s.DownloadPoster(ctx, sources.poster, entry.ID, detail.PosterURL)
		if posterErr != nil {
			// 海报失败只影响图：已经拿到的文字字段照常写回，条目只是没有图。
			log.Printf("[WatchlistEnrich] poster download failed id=%d source=%s err=%v", entry.ID, detail.SourceName, posterErr)
		} else {
			poster = imported
		}
	}
	s.settleEnrichmentSuccess(entry, detail, poster)
}

// watchlistSourceItemIDLimit 是 SourceItemID 列的 size:64。
const watchlistSourceItemIDLimit = 64

// settleEnrichmentSuccess 写回补全结果。守卫不成立时丢弃并清理刚落盘的图片。
func (s *WatchlistService) settleEnrichmentSuccess(entry models.WatchlistEntry, detail *WatchlistMetadataDetail, poster managedImageImport) {
	enrichedAt := time.Now()
	posterPath := entry.PosterPath
	if poster.RelativePath != "" {
		posterPath = poster.RelativePath
	}
	written, err := s.writeBackEnrichment(entry.ID, entry.EnrichmentClaim, map[string]any{
		"enrichment_status": models.WatchlistEnrichmentSucceeded,
		"enrichment_error":  "",
		"enrichment_claim":  "",
		"source_name":       detail.SourceName,
		"source_item_id":    detail.SourceItemID,
		"enriched_at":       &enrichedAt,
		"year":              detail.Year,
		"overview":          detail.Overview,
		"genres":            encodeWatchlistGenres(detail.Genres),
		"credits":           encodeWatchlistCredits(detail),
		"rating":            detail.Rating,
		"poster_path":       posterPath,
	})
	if err != nil {
		// UPDATE 本身报错，写没写进去无从判断。刚落盘的图**不删**：万一那一行其实
		// 更新成功了，删掉就留下一条指向不存在文件的记录——多一个孤儿文件远比
		// 一条断掉的路径轻。残留的 running 由下次启动的中断恢复收走。
		log.Printf("[WatchlistEnrich] write back failed id=%d err=%v", entry.ID, err)
		return
	}
	if !written {
		s.discardEnrichmentResult(entry, poster, "写回成功结果时认领守卫不成立")
		return
	}
	// 换了图才清旧图。内容寻址下同一张图的相对路径一致，那时旧路径就是新路径，
	// 删掉等于把条目正在用的图删了。
	if entry.PosterPath != "" && entry.PosterPath != posterPath {
		if err := s.RemovePoster(entry.PosterPath); err != nil {
			log.Printf("[WatchlistEnrich] stale poster cleanup failed id=%d err=%v", entry.ID, err)
		}
	}
	entry.EnrichmentStatus = models.WatchlistEnrichmentSucceeded
	entry.EnrichmentError = ""
	entry.SourceName = detail.SourceName
	s.emitEnrichProgress(entry)
	log.Printf("[WatchlistEnrich] settled id=%d status=succeeded source=%s item=%s poster=%t",
		entry.ID, detail.SourceName, detail.SourceItemID, posterPath != "")
}

// settleEnrichmentFailure 写回失败分类码（§五 的六类之一）。
//
// 只落 failed 加分类码，**不自动重试**：设计里没有自动重试，等用户手动点。
func (s *WatchlistService) settleEnrichmentFailure(entry models.WatchlistEntry, failure WatchlistMetadataFailure, cause error) {
	log.Printf("[WatchlistEnrich] entry id=%d kind=%s failure=%s err=%v", entry.ID, entry.Kind, failure, cause)
	written, err := s.writeBackEnrichment(entry.ID, entry.EnrichmentClaim, map[string]any{
		"enrichment_status": models.WatchlistEnrichmentFailed,
		"enrichment_error":  string(failure),
		"enrichment_claim":  "",
	})
	if err != nil {
		log.Printf("[WatchlistEnrich] write back failure failed id=%d err=%v", entry.ID, err)
		return
	}
	if !written {
		// 失败路径没有落盘过图片，所以这里只记日志。
		s.discardEnrichmentResult(entry, managedImageImport{}, "写回失败分类码时认领守卫不成立")
		return
	}
	entry.EnrichmentStatus = models.WatchlistEnrichmentFailed
	entry.EnrichmentError = string(failure)
	s.emitEnrichProgress(entry)
}

// discardEnrichmentResult 执行「守卫不成立」这条分支：丢弃结果并记日志，不改条目
// 状态，不返回错误，不发进度事件。这是设计意图（用户输入优先），不是错误路径。
//
// 刚落盘的图要删掉，否则托管目录里会留一个没人引用的文件（TC-04）。只有本次新建
// 的文件才删：Created 为 false 说明内容寻址命中了已有文件，那就是条目当前在用的
// 那一张。
func (s *WatchlistService) discardEnrichmentResult(entry models.WatchlistEntry, poster managedImageImport, reason string) {
	log.Printf("[WatchlistEnrich] discard result id=%d reason=%s", entry.ID, reason)
	if !poster.Created || poster.RelativePath == "" {
		return
	}
	if err := s.RemovePoster(poster.RelativePath); err != nil {
		log.Printf("[WatchlistEnrich] discard poster cleanup failed id=%d path=%s err=%v", entry.ID, poster.RelativePath, err)
	}
}

// lookupWatchlistDetail 按类型走适配器链，返回排在最前那个候选的详情。
//
// 链的走法就是 D-WM07，规则只有 walkWatchlistMetadataChain 一份：只有前一个源
// **明确说没有收录**（not_found）才问下一个；凭证错误、网络错误、超时都直接
// 返回——那会把一次配置问题伪装成「查无此片」，还在每次失败时多打一次抓取请求。
//
// 这里的一跳是 watchlistSourceDetail（**同一个源**上搜索 + 取详情），不是
// SearchWatchlistMetadataChain / DetailWatchlistMetadataChain 那种搜索与详情各走
// 一遍链：自动补全要把 SourceName 与 SourceItemID 一起写回条目，两者必须出自
// 同一个源，之后才能拿这个 ID 回到那个源重查。
func lookupWatchlistDetail(ctx context.Context, registry *WatchlistMetadataRegistry, kind WatchlistMetadataKind, title string) (*WatchlistMetadataDetail, error) {
	chain, err := registry.Chain(kind)
	if err != nil {
		return nil, err
	}
	return walkWatchlistMetadataChain(chain, func(source WatchlistMetadataSource) (*WatchlistMetadataDetail, error) {
		detail, err := watchlistSourceDetail(ctx, source, kind, title)
		if err != nil && WatchlistMetadataFailureOf(err) == WatchlistMetadataFailureNotFound {
			log.Printf("[WatchlistEnrich] source=%s reported not_found kind=%s, trying next in chain", source.Name(), kind)
		}
		return detail, err
	})
}

// watchlistSourceDetail 在一个源上完成「搜索 → 取排第一的候选 → 取详情」。
func watchlistSourceDetail(ctx context.Context, source WatchlistMetadataSource, kind WatchlistMetadataKind, title string) (*WatchlistMetadataDetail, error) {
	candidates, err := source.Search(ctx, kind, title)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		// 适配器的合同是「没有收录就回 not_found 错误」，空切片加 nil 不该出现。
		// 真出现了也按 not_found 处理，别让上层拿到一个没有候选的"成功"。
		return nil, newWatchlistMetadataSourceError(source.Name(), WatchlistMetadataFailureNotFound, 0, "源返回了空候选列表", nil)
	}
	// 取排在最前的候选：源给的就是匹配度顺序，适配器不重排。用户想换别的由重选
	// 候选的入口处理，自动补全里不猜。
	detail, err := source.Detail(ctx, kind, candidates[0].SourceItemID)
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, newWatchlistMetadataSourceError(source.Name(), WatchlistMetadataFailureSourceError, 0, "详情为空", nil)
	}
	return detail, nil
}

// watchlistEnrichmentFailureFor 把任意错误映射成 §五 的六个分类码之一。
//
// 适配器给的错误自带分类，直接取。另外两种来源：
//
//   - 代理地址填错（ErrWatchlistMetadataProxyInvalid）→ proxy_unreachable。填错与
//     连不上对用户是同一件待办：去看代理配置。
//   - 其余（目前只有 ErrWatchlistMetadataKindUnsupported，即该类型尚无适配器）
//     → source_error。**不为它新开第七个码**：§五 的六类是合同，互不合并也不扩张。
func watchlistEnrichmentFailureFor(err error) WatchlistMetadataFailure {
	if failure := WatchlistMetadataFailureOf(err); failure != "" {
		return failure
	}
	if errors.Is(err, ErrWatchlistMetadataProxyInvalid) {
		return WatchlistMetadataFailureProxyUnreachable
	}
	return WatchlistMetadataFailureSourceError
}

// encodeWatchlistGenres 把类型标签编成 JSON 数组。空列表存空串而不是 "[]"——列的
// 默认值就是空串，两种"没有"不该在库里长成两个样子。
func encodeWatchlistGenres(genres []string) string {
	cleaned := trimWatchlistNames(genres)
	if len(cleaned) == 0 {
		return ""
	}
	encoded, err := json.Marshal(cleaned)
	if err != nil {
		log.Printf("[WatchlistEnrich] encode genres failed err=%v", err)
		return ""
	}
	return string(encoded)
}

// encodeWatchlistCredits 把主创编成 WatchlistCredits 的 JSON。两边都空时存空串。
func encodeWatchlistCredits(detail *WatchlistMetadataDetail) string {
	credits := WatchlistCredits{
		Directors: trimWatchlistNames(detail.Directors),
		Cast:      trimWatchlistNames(detail.Cast),
	}
	if len(credits.Directors) == 0 && len(credits.Cast) == 0 {
		return ""
	}
	encoded, err := json.Marshal(credits)
	if err != nil {
		log.Printf("[WatchlistEnrich] encode credits failed err=%v", err)
		return ""
	}
	return string(encoded)
}

func trimWatchlistNames(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return cleaned
}

// RetryEnrichment 是用户手动重试（§3.1）：succeeded / failed / manual → pending，
// 并清空失败分类码。
//
// running 与 pending 不在转换表里，对它们重试什么都不发生，如实回
// ErrWatchlistEnrichmentNotRetryable——静默成功会让用户以为重试生效了。
func (s *WatchlistService) RetryEnrichment(id uint) error {
	result := database.DB.Model(&models.WatchlistEntry{}).
		Where("id = ? AND enrichment_status IN ?", id, []string{
			models.WatchlistEnrichmentSucceeded,
			models.WatchlistEnrichmentFailed,
			models.WatchlistEnrichmentManual,
		}).
		Updates(map[string]any{
			"enrichment_status": models.WatchlistEnrichmentPending,
			"enrichment_error":  "",
		})
	if result.Error != nil {
		return fmt.Errorf("重试补全失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		// 先做条件更新再判存在：happy path 只一条语句，两条语句只出现在这条
		// 少见的分支上。行还在就说明状态不可重试，行没了就是条目已被删除。
		var entry models.WatchlistEntry
		if err := database.DB.Select("id").First(&entry, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWatchlistEntryNotFound
			}
			return fmt.Errorf("重试补全失败: %w", err)
		}
		return ErrWatchlistEnrichmentNotRetryable
	}
	var entry models.WatchlistEntry
	if err := database.DB.First(&entry, id).Error; err == nil {
		s.emitEnrichProgress(entry)
	}
	s.TriggerEnrichment()
	return nil
}
