package services

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 片单与年度榜单的联动（D-PC52）里属于片单一侧的部分：撞名判定、榜单建条目时的
// 复用、删除时撤销榜单 want、补全写回豆瓣 ID 时通知榜单。
//
// 依赖方向不变：片单一侧不 import 榜单服务。它只
//   - 在删除的事务里**条件更新** movie_chart_marks（同事务是设计要求，见
//     revokeChartWantForEntry 的注释），
//   - 通过 watchlistDoubanBindObserver 接口把「写回了豆瓣 ID」告诉外面，实现由
//     MovieChartService 在构造时挂上。

// 片单条目的来源标注（WatchlistPage.Origins 的取值）。
const (
	WatchlistOriginManual = "manual"
	WatchlistOriginChart  = "chart"
)

// watchlistDoubanBindObserver 在条目被补全写回豆瓣 ID 之后回调（补全 worker 与
// ApplyCandidate 两条路径）。回调发生在写库成功之后、且不持有片单一侧的任何锁。
type watchlistDoubanBindObserver interface {
	OnWatchlistDoubanBound(entryID uint, doubanID, title string, year int)
	// OnWatchlistSourceChanged 在条目的来源 ID 发生任何变化（含改选到非豆瓣来源，此时
	// newDoubanID 为空）之后、OnWatchlistDoubanBound 之前回调：榜单一侧据此释放仍认领着
	// 该条目、但豆瓣 ID 与新来源不一致的 want 标记（APP-07 I-1）。
	OnWatchlistSourceChanged(entryID uint, newDoubanID string)
}

// watchlistDoubanBind 是观察者槽位，零值可用。
type watchlistDoubanBind struct {
	mu       sync.RWMutex
	observer watchlistDoubanBindObserver
}

func (b *watchlistDoubanBind) set(observer watchlistDoubanBindObserver) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.observer = observer
}

func (b *watchlistDoubanBind) get() watchlistDoubanBindObserver {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.observer
}

// SetDoubanBindObserver 挂上（或用 nil 摘掉）豆瓣 ID 写回观察者。
func (s *WatchlistService) SetDoubanBindObserver(observer watchlistDoubanBindObserver) {
	if s == nil {
		return
	}
	s.bind.set(observer)
}

// watchlistDoubanIDOf 取来源里的豆瓣 ID；来源不是豆瓣时为空。
func watchlistDoubanIDOf(sourceName, sourceItemID string) string {
	if sourceName != WatchlistMetadataSourceDouban {
		return ""
	}
	return sourceItemID
}

// notifySourceChanged 在写回让条目的来源 ID 变化后通知观察者。previous 是写回**前**的
// 条目。豆瓣 ID 前后一致（含都不是豆瓣）时不通知：没有标记会因此失去依据。
func (s *WatchlistService) notifySourceChanged(previous models.WatchlistEntry, detail *WatchlistMetadataDetail) {
	if detail == nil {
		return
	}
	observer := s.bind.get()
	if observer == nil {
		return
	}
	oldID := watchlistDoubanIDOf(previous.SourceName, previous.SourceItemID)
	newID := watchlistDoubanIDOf(detail.SourceName, detail.SourceItemID)
	if oldID == newID {
		return
	}
	observer.OnWatchlistSourceChanged(previous.ID, newID)
}

// notifyDoubanBound 在补全把条目写成 douban 来源之后通知观察者。只有电影会带豆瓣来源。
func (s *WatchlistService) notifyDoubanBound(entry models.WatchlistEntry, detail *WatchlistMetadataDetail) {
	if detail == nil || detail.SourceName != WatchlistMetadataSourceDouban || detail.SourceItemID == "" {
		return
	}
	observer := s.bind.get()
	if observer == nil {
		return
	}
	observer.OnWatchlistDoubanBound(entry.ID, detail.SourceItemID, entry.Title, detail.Year)
}

// watchlistTitleKindTaken 在**所有**条目里查 (title, kind) 是否已被占用，exceptID
// 排除自身（改名时保留原名不算撞）。
func watchlistTitleKindTaken(title, kind string, exceptID uint) (bool, error) {
	query := database.DB.Model(&models.WatchlistEntry{}).Where("title = ? AND kind = ?", title, kind)
	if exceptID != 0 {
		query = query.Where("id <> ?", exceptID)
	}
	var count int64
	if err := query.Limit(1).Count(&count).Error; err != nil {
		return false, fmt.Errorf("检查片名是否重复失败: %w", err)
	}
	return count > 0, nil
}

// EnsureChartEntry 为榜单「想看」找到或新建片单条目，返回 (条目 ID, 是否本次新建)。
//
// 顺序（D-PC52）：
//  1. 按 (source_name='douban', source_item_id=<id>) 找，命中即复用；
//  2. 再按 (title, kind='movie') 找**没有豆瓣来源**的条目复用。已被 TMDB 补全过的
//     手动条目 source_item_id 非空，同样是复用候选——数据库索引不再挡手动同名，
//     若不复用就会再建出一条重复条目。source_item_id 为空的才补写豆瓣来源；已有别家
//     来源的保持原样，改写它会让那个 ID 再也回不到原来的源上；
//  3. 都没有才新建，写 source_name / source_item_id，补全状态 pending。
//
// 同名但豆瓣 ID 不同的两部电影因此可以共存：第 2 步只看没有豆瓣来源的条目。
// 复用（第 1、2 步）返回 created=false，榜单一侧据此以 reuse 来源认领该条目：删除条目时
// 能按 ID 撤销那个 want，但榜单一侧的撤销永远不删它（APP-07）。
func (s *WatchlistService) EnsureChartEntry(title, doubanID string) (uint, bool, error) {
	title, err := validateWatchlistText(title, true)
	if err != nil {
		return 0, false, err
	}
	if !doubanSubjectID.MatchString(doubanID) {
		return 0, false, fmt.Errorf("豆瓣 ID 无效: %q", doubanID)
	}
	db := database.DB

	var byID models.WatchlistEntry
	found, err := firstWatchlistEntry(db.Select("id").
		Where("source_name = ? AND source_item_id = ? AND kind = ?",
			WatchlistMetadataSourceDouban, doubanID, models.WatchlistKindMovie), &byID)
	if err != nil {
		return 0, false, err
	}
	if found {
		return byID.ID, false, nil
	}

	var byTitle models.WatchlistEntry
	found, err = firstWatchlistEntry(db.Select("id", "source_item_id").
		Where("title = ? AND kind = ? AND source_name <> ?",
			title, models.WatchlistKindMovie, WatchlistMetadataSourceDouban), &byTitle)
	if err != nil {
		return 0, false, err
	}
	if found {
		if byTitle.SourceItemID == "" {
			// 条件更新：读到之后被别人补全过就不覆盖，仍然复用这一条。补全正在跑
			// （claim 非空 / running）的条目同样只复用、不回填：回填会与那一轮写回竞争来源列。
			err := db.Model(&models.WatchlistEntry{}).
				Where("id = ? AND source_item_id = ? AND enrichment_claim = ? AND enrichment_status <> ?",
					byTitle.ID, "", "", models.WatchlistEnrichmentRunning).
				Updates(map[string]any{
					"source_name":    WatchlistMetadataSourceDouban,
					"source_item_id": doubanID,
				}).Error
			if err != nil {
				if watchlistTitleConflict(err) {
					return 0, false, ErrWatchlistTitleExists
				}
				return 0, false, fmt.Errorf("关联想看记录失败: %w", err)
			}
		}
		return byTitle.ID, false, nil
	}

	entry := &models.WatchlistEntry{
		Title:            title,
		Kind:             models.WatchlistKindMovie,
		EnrichmentStatus: models.WatchlistEnrichmentPending,
		SourceName:       WatchlistMetadataSourceDouban,
		SourceItemID:     doubanID,
	}
	if err := db.Create(entry).Error; err != nil {
		if watchlistTitleConflict(err) {
			return 0, false, ErrWatchlistTitleExists
		}
		return 0, false, fmt.Errorf("添加想看记录失败: %w", err)
	}
	s.TriggerEnrichment()
	return entry.ID, true, nil
}

// firstWatchlistEntry 取第一条匹配（按 id 升序），没有时返回 found=false 而不是错误。
func firstWatchlistEntry(query *gorm.DB, dest *models.WatchlistEntry) (bool, error) {
	var rows []models.WatchlistEntry
	if err := query.Order("id ASC").Limit(1).Find(&rows).Error; err != nil {
		return false, fmt.Errorf("读取想看片单失败: %w", err)
	}
	if len(rows) == 0 {
		return false, nil
	}
	*dest = rows[0]
	return true, nil
}

// revokeChartWantForEntry 在删除条目的事务里撤销对应的榜单 want：mark 置空、
// watchlist_entry_id 置 0，行保留（快照三列不动）。
//
// 主判据是精确的认领关系：watchlist_entry_id = 条目 ID，覆盖榜单新建（chart）、补全绑定
// （enrichment）与复用（reuse，APP-07）三种来源，与片名、来源 ID 此后怎么变都无关（复用后
// 改名再删除仍能撤销）。
//
// 只有历史行（本批次之前复用时不记认领：watchlist_entry_id = 0 且来源为空）才退回猜测：
// 条目带豆瓣 ID 时按 douban_id 对；条目是电影时按片名对，快照片名与条目片名两边都去掉
// 首尾空白再比（APP-07 I-2：复用的是被 TMDB 补全过的同名条目，没有豆瓣 ID 可对）。
// 已认领别的条目的 want 一律不动。
//
// 条件更新，不加锁：只碰 mark = 'want' 的行。
func revokeChartWantForEntry(tx *gorm.DB, entry models.WatchlistEntry) error {
	var legacy []string
	var legacyArgs []any
	if entry.SourceName == WatchlistMetadataSourceDouban && entry.SourceItemID != "" {
		legacy = append(legacy, "douban_id = ?")
		legacyArgs = append(legacyArgs, entry.SourceItemID)
	}
	if title := strings.TrimSpace(movieChartTruncateTitle(entry.Title)); entry.Kind == models.WatchlistKindMovie && title != "" {
		legacy = append(legacy, "TRIM(title) = ?")
		legacyArgs = append(legacyArgs, title)
	}
	condition := "watchlist_entry_id = ?"
	args := []any{entry.ID}
	if len(legacy) > 0 {
		condition += " OR (watchlist_entry_id = 0 AND watchlist_entry_origin = '' AND (" + strings.Join(legacy, " OR ") + "))"
		args = append(args, legacyArgs...)
	}
	query := tx.Model(&models.MovieChartMark{}).
		Where("mark = ?", models.MovieChartMarkWant).
		Where("("+condition+")", args...)
	err := query.Updates(map[string]any{
		"mark":                   "",
		"watchlist_entry_id":     0,
		"watchlist_entry_origin": "",
		"updated_at":             time.Now(),
	}).Error
	if err != nil {
		return fmt.Errorf("撤销榜单想看标记失败: %w", err)
	}
	return nil
}

// releaseReuseChartWantsForEntry 释放以 reuse 认领该条目的 want 标记（APP-07 M-2）：认领归零、
// 来源改为 unclaimed，mark 保留 want——那是用户在榜单上的明确表态，片单一侧的编辑不替他撤销。
// 条件更新，不加锁：只碰 mark = 'want' 且确实以 reuse 认领着这一条的行。
func releaseReuseChartWantsForEntry(tx *gorm.DB, entryID uint) error {
	err := tx.Model(&models.MovieChartMark{}).
		Where("mark = ? AND watchlist_entry_id = ? AND watchlist_entry_origin = ?",
			models.MovieChartMarkWant, entryID, movieChartOriginReuse).
		Updates(map[string]any{
			"watchlist_entry_id":     0,
			"watchlist_entry_origin": movieChartOriginUnclaimed,
			"updated_at":             time.Now(),
		}).Error
	if err != nil {
		return fmt.Errorf("释放榜单想看的复用认领失败: %w", err)
	}
	return nil
}

// watchlistOrigins 给一页条目标来源：被某个 want 标记认领的是「榜单」，其余是「手动」。
// 复用（reuse）认领的是用户自己先建的条目，仍标「手动」。
func watchlistOrigins(entries []models.WatchlistEntry) (map[uint]string, error) {
	origins := make(map[uint]string, len(entries))
	if len(entries) == 0 {
		return origins, nil
	}
	ids := make([]uint, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
		origins[entry.ID] = WatchlistOriginManual
	}
	var owned []uint
	err := database.DB.Model(&models.MovieChartMark{}).
		Where("mark = ? AND watchlist_entry_id IN ? AND watchlist_entry_origin <> ?", models.MovieChartMarkWant, ids, movieChartOriginReuse).
		Pluck("watchlist_entry_id", &owned).Error
	if err != nil {
		return nil, fmt.Errorf("读取想看来源失败: %w", err)
	}
	for _, id := range owned {
		origins[id] = WatchlistOriginChart
	}
	return origins, nil
}
