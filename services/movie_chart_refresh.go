package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 年度榜单一轮刷新的写路径：列表阶段、年状态维护、详情补全 worker。
// 流程见概要设计 §4.1，并发不变量见需求设计文档 §5。
//
// 并发协调**只用条件更新加认领标识**，全程不用 SELECT ... FOR UPDATE、LOCK TABLES
// 或数据库咨询锁。模式照 services/watchlist_enrichment.go，不另发明一套。

// movieChartListDoUpdateColumns 是**列表阶段拥有的列**，也是 upsert 冲突时唯一
// 允许被覆盖的那几列。
//
// 这份清单是承重的，别往里加东西：只要混进任何一个 detail_* 或 release_* 列，
// 每一次列表重抓都会把已经补全过的条目打回 pending，一年白跑约 1500 次详情请求
// （需求设计文档 §5）。title 由列表阶段拥有、original_title 由详情阶段拥有，
// 两者不是同一列，也不要互相覆盖。
var movieChartListDoUpdateColumns = []string{
	"title",
	"card_subtitle",
	"rating",
	"rating_count",
	"poster_url",
	"year",
	"list_fetched_at",
}

// refreshYear 跑完一轮：列表阶段 → 写年状态 → 重置待补全 → 详情阶段。
//
// 返回错误只用于日志与测试断言，界面看到的是年状态表里的三形态（概要设计 §4.2）。
func (s *MovieChartService) refreshYear(ctx context.Context, year int) error {
	tasks := s.backgroundTasks()
	tasks.Begin(BackgroundTaskMovieChart)
	defer tasks.End(BackgroundTaskMovieChart)

	source, err := s.newSource()
	if err != nil {
		// 出网件装配失败（多半是代理地址填错）不让这一轮悄悄什么都不做：照常把
		// 分类码写进年状态，用户才看得见「去看代理配置」这件待办。
		s.settleRefreshFailure(ctx, year, watchlistEnrichmentFailureFor(err), err)
		return fmt.Errorf("装配 %d 年榜单数据源失败: %w", year, err)
	}

	if listErr := s.runListPhase(ctx, year, source); listErr != nil {
		// 取消要先于失败判定：用户点的取消不是故障，写失败码会让页面显示一条
		// 根本没发生的错误，也会把「上次成功时间」旁边挂上一个假的失败原因。
		if ctx.Err() != nil {
			s.settleRefreshCanceled(ctx, year)
			return ctx.Err()
		}
		s.settleRefreshFailure(ctx, year, watchlistEnrichmentFailureFor(listErr), listErr)
		return listErr
	}
	// last_refreshed_at 只在**整轮列表阶段成功**之后才写（需求设计文档 §4.3）。
	// 详情补全跑到哪一步不影响它：详情是逐条独立的增量，未补全的条目照常显示。
	s.settleRefreshSuccess(ctx, year)

	if err := s.resetYearDetailBacklog(ctx, year); err != nil {
		log.Printf("[MovieChart] reset detail backlog year=%d failed err=%v", year, err)
	}
	s.runDetailPhase(ctx, year, source)
	return nil
}

// runListPhase 跑四种排序 × 固定 25 页，每页按豆瓣 ID upsert。
//
// **单页失败即中止整轮**，不重试、不降级（概要设计 §4.1）：需求没有点名这个场景，
// 仓库的既有约定是不加没被要求的 fallback。已经 upsert 的条目全部保留——增量更新
// 从不删除（D-MC04），下一轮从头再跑一遍，upsert 幂等，重跑没有副作用。
func (s *MovieChartService) runListPhase(ctx context.Context, year int, source MovieChartSource) error {
	var summary movieChartListSummary
	for _, sortKey := range MovieChartSorts() {
		err := source.ListYear(ctx, year, sortKey, func(page MovieChartListPage) error {
			summary.observe(page)
			return s.upsertListPage(ctx, year, page.Items)
		})
		if err != nil {
			return fmt.Errorf("抓取 %d 年榜单失败（排序 %s）: %w", year, sortKey, err)
		}
	}
	log.Printf("[MovieChart] list phase done year=%d pages=%d kept=%d skipped_non_movie=%d skipped_other_year=%d kept_without_year=%d",
		year, summary.pages, summary.kept, summary.skippedNonMovie, summary.skippedOtherYear, summary.keptWithoutYear)
	return nil
}

// movieChartListSummary 汇总一轮列表阶段的条目去向，只进日志。
//
// 三个「少了几条」的计数分开记：跨年、非电影、没给年份是三件不同的事，合成一个
// 数字之后就再也看不出这一年为什么比预期少。
type movieChartListSummary struct {
	pages            int
	kept             int
	skippedNonMovie  int
	skippedOtherYear int
	keptWithoutYear  int
}

func (m *movieChartListSummary) observe(page MovieChartListPage) {
	m.pages++
	m.kept += len(page.Items)
	m.skippedNonMovie += page.SkippedNonMovie
	m.skippedOtherYear += page.SkippedOtherYear
	m.keptWithoutYear += page.KeptWithoutYear
}

// upsertListPage 把一页条目按 douban_id 增量写入（D-MC04）。空页是实测过的瞬时
// 现象，什么都不做就继续下一页，既不终止也不记失败码（D-MC07）。
func (s *MovieChartService) upsertListPage(ctx context.Context, year int, items []MovieChartListItem) error {
	deduped := movieChartDedupeListItems(items)
	if len(deduped) == 0 {
		return nil
	}
	now := s.now()
	fetchedAt := now
	rows := make([]models.MovieChartEntry, 0, len(deduped))
	for _, item := range deduped {
		rows = append(rows, models.MovieChartEntry{
			DoubanID: item.DoubanID,
			Year:     year,
			// 片名来自外部信源、长度不受任何人约束，而 title 是 size:200。
			// Postgres 上超长直接报 22001 让整批失败，而列表阶段单页失败即中止整轮、
			// 页集合又是确定的，一条病态片名就能把这一年永久卡死；SQLite 不校验长度，
			// 只跑默认后端完全看不见。与 21000 那条是同一类陷阱，处置也一样：入库前截断。
			Title:        movieChartTruncateTitle(item.Title),
			CardSubtitle: item.CardSubtitle,
			Rating:       item.Rating,
			RatingCount:  item.RatingCount,
			PosterURL:    item.PosterURL,
			// 新条目一律从 pending 起步。显式写而不是靠列默认值：批量 INSERT 里
			// 零值字段会不会被换成标签默认值取决于 GORM 的实现细节，承重的初始
			// 状态不该压在那上面。已存在的行不受影响——detail_status 不在
			// movieChartListDoUpdateColumns 里。
			DetailStatus:  models.MovieChartDetailPending,
			ListFetchedAt: &fetchedAt,
		})
	}
	// SET 右侧一律是 "excluded"."<列>"（clause.AssignmentColumns 的形态）或字面量，
	// **没有任何一处裸引用目标表的列**。Postgres 里不带限定的列名在
	// ON CONFLICT DO UPDATE 的 SET 右侧是歧义的（既可能指目标行、也可能指 excluded），
	// 会直接报 42702 让整批失败——将来要在这里引用目标表的列，必须写成
	// movie_chart_entries.<列>（services/image_ai_tagging_service.go:767 记过这个坑）。
	//
	// updated_at 必须显式赋值：GORM 在 DoUpdates 这条路径上不维护它
	// （同文件 773 行即此写法）。
	assignments := append(clause.AssignmentColumns(movieChartListDoUpdateColumns),
		clause.Assignment{Column: clause.Column{Name: "updated_at"}, Value: now})
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "douban_id"}},
		DoUpdates: assignments,
	}).Create(&rows).Error
	if err != nil {
		return fmt.Errorf("写入 %d 年榜单条目失败: %w", year, err)
	}
	return nil
}

// movieChartDedupeListItems 按 douban_id 去重，同一批里后出现的覆盖先出现的。
//
// 必须在构造批次前做，不能指望数据库容忍：一条 INSERT 里出现两行相同的冲突键时
// Postgres 报 21000（ON CONFLICT DO UPDATE command cannot affect row a second time）
// **整批失败**，而 SQLite 不报——只跑默认后端的测试永远发现不了（需求设计文档 §5）。
func movieChartDedupeListItems(items []MovieChartListItem) []MovieChartListItem {
	index := make(map[string]int, len(items))
	deduped := make([]MovieChartListItem, 0, len(items))
	for _, item := range items {
		if at, seen := index[item.DoubanID]; seen {
			deduped[at] = item
			continue
		}
		index[item.DoubanID] = len(deduped)
		deduped = append(deduped, item)
	}
	return deduped
}

// resetYearDetailBacklog 在列表阶段成功后，把该年**待重试**的条目放回 pending。
//
// 两种状态一起收，理由不同：
//
//   - failed：这是刷新语义的一部分，不是重试机制——它由用户点刷新或月度刷新驱动，
//     不自行发生。不重置的话失败条目永远不会再被补全（需求设计文档 §5）。
//   - running：进程在补全途中被杀会留下这种行。取消路径自己会把认领退回 pending，
//     所以这里收的是崩溃残留。安全的前提是同时只有一轮刷新（StartRefresh 拒绝并发），
//     此刻该年没有任何 worker 持有认领；将来若放开并发刷新，这一句必须重新审。
//
// detail_error 一并清空：状态回到 pending 而错误码还挂着，界面会给一条正在排队的
// 条目显示失败提示。口径与 WatchlistService.RetryEnrichment 一致。
func (s *MovieChartService) resetYearDetailBacklog(ctx context.Context, year int) error {
	result := s.db.WithContext(ctx).Model(&models.MovieChartEntry{}).
		Where("year = ? AND detail_status IN ?", year, []string{
			models.MovieChartDetailFailed,
			models.MovieChartDetailRunning,
		}).
		Updates(map[string]any{
			"detail_status": models.MovieChartDetailPending,
			"detail_error":  "",
			"detail_claim":  "",
		})
	if result.Error != nil {
		return fmt.Errorf("重置 %d 年待补全条目失败: %w", year, result.Error)
	}
	if result.RowsAffected > 0 {
		log.Printf("[MovieChart] detail backlog reset year=%d rows=%d", year, result.RowsAffected)
	}
	return nil
}

// runDetailPhase 逐条补全该年的详情：认领 → 限速取详情 → 带守卫写回。
//
// 与列表阶段相反，**单条失败只影响该条**：详情天然逐条独立，失败的条目留在
// failed，榜单里标「上映信息待确认」，下次刷新由 resetYearDetailBacklog 放回队列。
func (s *MovieChartService) runDetailPhase(ctx context.Context, year int, source MovieChartSource) {
	var ids []uint
	// 按 detail_status 过滤、按 id 升序，正好走 idx_movie_chart_entry_detail，
	// 认领顺序也因此是确定的。
	if err := s.db.WithContext(ctx).Model(&models.MovieChartEntry{}).
		Where("year = ? AND detail_status = ?", year, models.MovieChartDetailPending).
		Order("id ASC").Pluck("id", &ids).Error; err != nil {
		log.Printf("[MovieChart] list pending details year=%d failed err=%v", year, err)
		return
	}
	if len(ids) == 0 {
		return
	}
	log.Printf("[MovieChart] detail phase start year=%d pending=%d", year, len(ids))

	requested := false
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		entry, claim, claimed, err := s.claimEntryDetail(ctx, id)
		if err != nil {
			log.Printf("[MovieChart] claim detail id=%d failed err=%v", id, err)
			continue
		}
		if !claimed {
			// 影响 0 行：这一条已被处理，或用户刚刷新改了状态。跳到下一条，
			// **不报错**——这是需求设计文档 §5 转换表里的正常分支。
			continue
		}
		if requested {
			if err := s.sleepBetweenDetails(ctx); err != nil {
				s.releaseEntryClaim(ctx, entry.ID, claim)
				return
			}
		}
		requested = true
		detail, detailErr := source.Detail(ctx, entry.DoubanID)
		if ctx.Err() != nil {
			// 取消不是失败：把认领退回 pending，下一轮从这一条接着跑（可断点续跑）。
			s.releaseEntryClaim(ctx, entry.ID, claim)
			return
		}
		if detailErr != nil {
			s.settleDetailFailure(ctx, entry, claim, watchlistEnrichmentFailureFor(detailErr), detailErr)
			continue
		}
		s.settleDetailSuccess(ctx, entry, claim, detail)
	}
}

// sleepBetweenDetails 是 D-MC06 的 1 次/秒限速。detailInterval 非正即不限速。
func (s *MovieChartService) sleepBetweenDetails(ctx context.Context) error {
	if s.detailInterval <= 0 {
		return ctx.Err()
	}
	return s.sleep(ctx, s.detailInterval)
}

// claimEntryDetail 用条件更新认领一条 pending，返回认领成功后的条目与本次认领标识。
//
// 判据只有「影响了几行」：1 行是抢到了，0 行是没抢到。没抢到不是错误。
func (s *MovieChartService) claimEntryDetail(ctx context.Context, id uint) (models.MovieChartEntry, string, bool, error) {
	// 认领标识直接复用 newWatchlistEnrichmentClaim：16 字节随机数的十六进制，正好
	// 32 个字符，与 detail_claim 的 size:32 对齐。**不得换成 uuid.NewString()**
	// ——那是 36 个字符，SQLite 不校验长度会默默存下，Postgres 直接报
	// value too long（SQLSTATE 22001）。同一个不变量只留一份实现。
	claim, err := newWatchlistEnrichmentClaim()
	if err != nil {
		return models.MovieChartEntry{}, "", false, err
	}
	result := s.db.WithContext(ctx).Model(&models.MovieChartEntry{}).
		Where("id = ? AND detail_status = ?", id, models.MovieChartDetailPending).
		Updates(map[string]any{
			"detail_status": models.MovieChartDetailRunning,
			"detail_claim":  claim,
		})
	if result.Error != nil {
		return models.MovieChartEntry{}, "", false, result.Error
	}
	if result.RowsAffected == 0 {
		return models.MovieChartEntry{}, "", false, nil
	}
	var entry models.MovieChartEntry
	if err := s.db.WithContext(ctx).First(&entry, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 认领成功到读回来之间这一行被删掉了，没有行可退。
			return models.MovieChartEntry{}, "", false, nil
		}
		// 读回失败（最常见的就是此刻被取消）时认领**已经写进库里了**。不退回去的话
		// 调用方只会记一条日志就跳下一条，这一行就永远停在 running：年内的详情补全
		// 进度条再也到不了 100%，而往年一旦有过 last_attempt_at 就不再自动刷新，
		// 只能等用户手动点一次。退回照旧带守卫，退的只可能是本次认领。
		s.releaseEntryClaim(ctx, id, claim)
		return models.MovieChartEntry{}, "", false, err
	}
	// 认领与读回之间用户可能又点了一次刷新。读回来的行不是本次认领就当没抢到，
	// 继续做下去也只是白跑一次出网——写回必然被守卫丢弃。
	if entry.DetailClaim != claim || entry.DetailStatus != models.MovieChartDetailRunning {
		return models.MovieChartEntry{}, "", false, nil
	}
	return entry, claim, true, nil
}

// writeBackDetail 带认领守卫写回，返回是否真的写进去了。
//
// 守卫是**状态加认领标识**两项，缺一不可。detail_status 会从 failed/succeeded 回到
// pending（用户点刷新），只比对状态值区分不出「同一次认领」与「新一轮认领」，
// 一次慢响应会落到重试后的新一轮上——这就是需求设计文档 §5 说的 ABA。
//
// 影响 0 行**不是错误**：调用方丢弃结果并记日志，不改条目状态，不往上抛错。
func (s *MovieChartService) writeBackDetail(ctx context.Context, id uint, claim string, fields map[string]any) (bool, error) {
	result := s.db.WithContext(ctx).Model(&models.MovieChartEntry{}).
		Where("id = ? AND detail_status = ? AND detail_claim = ?",
			id, models.MovieChartDetailRunning, claim).
		Updates(fields)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// settleDetailSuccess 写回一条补全结果。
//
// 不写 title：片名归列表阶段拥有，详情只写 original_title（需求设计文档 §2.2）。
// 判定为剧集的条目走的也是这条路径——适配器给回 release_scope=excluded 的正常结果，
// 这里照常落 succeeded，不删行（D-MC04），榜单查询靠 release_scope 把它挡在外面。
func (s *MovieChartService) settleDetailSuccess(ctx context.Context, entry models.MovieChartEntry, claim string, detail *MovieChartDetail) {
	fetchedAt := s.now()
	written, err := s.writeBackDetail(ctx, entry.ID, claim, map[string]any{
		"detail_status":     models.MovieChartDetailSucceeded,
		"detail_error":      "",
		"detail_claim":      "",
		"detail_fetched_at": &fetchedAt,
		// original_title 同样是 size:200 的外部字符串，理由见 upsertListPage 里的注释。
		// 这条写回是逐条独立的，超长只会卡死这一条而不是整年，但结果一样是永远补不全。
		"original_title":  movieChartTruncateTitle(detail.OriginalTitle),
		"release_scope":   detail.ReleaseScope,
		"release_date":    detail.ReleaseDate,
		"release_pubdate": detail.ReleasePubdate,
		"is_released":     detail.IsReleased,
		"rating":          detail.Rating,
		"rating_count":    detail.RatingCount,
		"countries":       movieChartJoinValues(detail.Countries),
		"genres":          movieChartJoinValues(detail.Genres),
		"directors":       movieChartJoinValues(detail.Directors),
		// cast 是 SQL 保留字。走 Updates 的 map 时列名由 GORM 加引号，两个后端都对；
		// 将来手写 SQL 碰这一列必须自己加（Postgres "cast"，SQLite `cast`）。
		"cast":     movieChartJoinValues(detail.Cast),
		"overview": detail.Overview,
	})
	if err != nil {
		log.Printf("[MovieChart] write back detail id=%d failed err=%v", entry.ID, err)
		return
	}
	if !written {
		log.Printf("[MovieChart] discard detail id=%d douban=%s reason=写回成功结果时认领守卫不成立", entry.ID, entry.DoubanID)
		return
	}
	log.Printf("[MovieChart] settled detail id=%d douban=%s scope=%s date=%q", entry.ID, entry.DoubanID, detail.ReleaseScope, detail.ReleaseDate)
}

// settleDetailFailure 写回六类分类码之一。只落 failed 不自动重试——重试由下一次
// 刷新的 resetYearDetailBacklog 驱动。
func (s *MovieChartService) settleDetailFailure(ctx context.Context, entry models.MovieChartEntry, claim string, failure WatchlistMetadataFailure, cause error) {
	log.Printf("[MovieChart] detail id=%d douban=%s failure=%s err=%v", entry.ID, entry.DoubanID, failure, cause)
	written, err := s.writeBackDetail(ctx, entry.ID, claim, map[string]any{
		"detail_status": models.MovieChartDetailFailed,
		"detail_error":  string(failure),
		"detail_claim":  "",
	})
	if err != nil {
		log.Printf("[MovieChart] write back detail failure id=%d err=%v", entry.ID, err)
		return
	}
	if !written {
		log.Printf("[MovieChart] discard detail id=%d douban=%s reason=写回失败分类码时认领守卫不成立", entry.ID, entry.DoubanID)
	}
}

// releaseEntryClaim 在取消时把认领退回 pending，让下一轮从这一条接着跑。
//
// 守卫照旧（状态 + 认领标识）：用户在取消的同时又点了刷新时，退回的必须是本次
// 认领，不能把新一轮刚认领出去的行打回 pending。
//
// 写入用 context.WithoutCancel 派生的上下文：此刻 ctx 已经被取消，直接用它这条
// UPDATE 根本发不出去，认领就永远留在 running 了。
func (s *MovieChartService) releaseEntryClaim(ctx context.Context, id uint, claim string) {
	written, err := s.writeBackDetail(context.WithoutCancel(ctx), id, claim, map[string]any{
		"detail_status": models.MovieChartDetailPending,
		"detail_claim":  "",
	})
	if err != nil {
		log.Printf("[MovieChart] release detail claim id=%d failed err=%v", id, err)
		return
	}
	if !written {
		log.Printf("[MovieChart] release detail claim id=%d skipped reason=认领守卫不成立", id)
	}
}

// movieChartJoinValues 把多值字段拼成 \n 分隔的一列，沿用仓库既有写法。
func movieChartJoinValues(values []string) string {
	return strings.Join(movieChartTrimmedValues(values, 0), "\n")
}

// movieChartTitleLimit 是 title / original_title 两列的 size:200。
//
// Postgres 的 varchar(200) 数的是**字符**不是字节，所以这个上限按 rune 算才对得上。
const movieChartTitleLimit = 200

// movieChartTruncateTitle 按 rune 截断片名，保证落得进 size:200 的列。
//
// **必须按 rune 切，不能按字节切**：中文片名一个字三个字节，从字节中间切开会在库里
// 留下半个字符（乱码），而这一列是要直接显示给用户的。
//
// 这里不校验也不拒绝，只截断：片名来自豆瓣响应，不是用户输入——仓库既有的 200 字符
// 校验（validateWatchlistText）是给用户输入用的，对外部信源套用等于让一条异常数据
// 把整年抓取判失败。截断的代价是极少数超长片名尾部丢失，比整年卡死轻得多。
func movieChartTruncateTitle(value string) string {
	runes := []rune(value)
	if len(runes) <= movieChartTitleLimit {
		return value
	}
	return string(runes[:movieChartTitleLimit])
}

// settleRefreshSuccess 整轮列表阶段成功：写 last_refreshed_at 与 last_attempt_at，
// 清空失败码。
func (s *MovieChartService) settleRefreshSuccess(ctx context.Context, year int) {
	now := s.now()
	s.writeYearState(ctx, models.MovieChartYearState{Year: year, LastRefreshedAt: &now, LastAttemptAt: &now},
		map[string]any{
			"last_refreshed_at": now,
			"last_attempt_at":   now,
			"last_failure":      "",
			"updated_at":        now,
		})
}

// settleRefreshFailure 记一轮失败：只动 last_attempt_at 与 last_failure。
//
// **last_refreshed_at 一个字都不碰**：它是「上次成功刷新」的时间，失败的一轮把它
// 推后，过期的缓存就看起来还新鲜，30 天判定与页面状态条一起失真（需求设计文档 §4.3）。
func (s *MovieChartService) settleRefreshFailure(ctx context.Context, year int, failure WatchlistMetadataFailure, cause error) {
	log.Printf("[MovieChart] refresh year=%d failure=%s err=%v", year, failure, cause)
	now := s.now()
	s.writeYearState(ctx, models.MovieChartYearState{Year: year, LastAttemptAt: &now, LastFailure: string(failure)},
		map[string]any{
			"last_attempt_at": now,
			"last_failure":    string(failure),
			"updated_at":      now,
		})
}

// settleRefreshCanceled 记一轮取消：只动 last_attempt_at。
//
// 取消不是失败（需求设计文档 §7）：不写 last_refreshed_at，也**不碰 last_failure**
// ——把它清空会抹掉上一轮真实的失败原因，写上一个码则会把用户自己的中止显示成故障。
// ctx 此刻已被取消，但写入照样发得出去：writeYearState 统一剥掉了取消。
func (s *MovieChartService) settleRefreshCanceled(ctx context.Context, year int) {
	log.Printf("[MovieChart] refresh year=%d canceled（已写入的条目保留，last_refreshed_at 不更新）", year)
	now := s.now()
	s.writeYearState(ctx, models.MovieChartYearState{Year: year, LastAttemptAt: &now},
		map[string]any{
			"last_attempt_at": now,
			"updated_at":      now,
		})
}

// writeYearState 按年 upsert 抓取状态。assignments 只列本次要改的列，没列到的
// 保持原值——三种落定各自该动哪几列，见上面三个调用点。
//
// **三条落定路径的写入一律不随本轮 ctx 取消**（P-005 评审补齐）。落定记的是
// 「这一轮已经发生过的事实」，而取消可以恰好落在事实发生之后、这条 UPDATE 发出
// 之前：那时用本轮 ctx 发出去只会拿到 context canceled，最近一次尝试的结果就此
// 丢掉，页面顶部的三形态（概要设计 §4.2）跟着说假话——用户刚看见「更新失败：
// 代理地址无效」，手一抖点了取消，那行提示就永远不会再出现，而失败是真实发生过的。
// 用户的取消是「别再抓了」，不是「把已经发生的事忘掉」。
//
// 统一包在这一层，而不是让三个调用点各自记得包：P-005 就是在 settleRefreshFailure
// 少了这一层上踩过一次——旁边的 settleRefreshCanceled 恰好包了，于是「每条路径
// 应该都包了」看起来理所当然，唯一没包的那条反而最难被发现。往这里加第四条落定
// 路径时不用再想这件事。
//
// 传进来的 ctx 仍然照传：它带着调用链上的其它值，去掉的只有「取消」这一项。
func (s *MovieChartService) writeYearState(ctx context.Context, row models.MovieChartYearState, assignments map[string]any) {
	// updated_at 由调用方显式放进 assignments：GORM 不在 DoUpdates 上维护它。
	err := s.db.WithContext(context.WithoutCancel(ctx)).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "year"}},
		DoUpdates: clause.Assignments(assignments),
	}).Create(&row).Error
	if err != nil {
		log.Printf("[MovieChart] write year state year=%d failed err=%v", row.Year, err)
	}
}
