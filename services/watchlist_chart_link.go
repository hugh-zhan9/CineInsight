package services

import (
	"fmt"
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
// 复用（第 1、2 步）返回 created=false，榜单一侧据此不认领该条目。
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
			// 条件更新：读到之后被别人补全过就不覆盖，仍然复用这一条。
			err := db.Model(&models.WatchlistEntry{}).
				Where("id = ? AND source_item_id = ?", byTitle.ID, "").
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
// 匹配两种归属：标记认领了这条条目（watchlist_entry_id = 条目 ID，含 P-019 之前
// 建的、没有豆瓣来源的存量条目），或条目带豆瓣 ID 且该 want 没有归属任何别的条目
// （watchlist_entry_id = 0，撞名复用的情形）。指向别的条目的 want 不动。
//
// 条件更新，不加锁：只碰 mark = 'want' 的行。
func revokeChartWantForEntry(tx *gorm.DB, entry models.WatchlistEntry) error {
	query := tx.Model(&models.MovieChartMark{}).Where("mark = ?", models.MovieChartMarkWant)
	if entry.SourceName == WatchlistMetadataSourceDouban && entry.SourceItemID != "" {
		query = query.Where("watchlist_entry_id = ? OR (douban_id = ? AND watchlist_entry_id = 0)",
			entry.ID, entry.SourceItemID)
	} else {
		query = query.Where("watchlist_entry_id = ?", entry.ID)
	}
	err := query.Updates(map[string]any{
		"mark":               "",
		"watchlist_entry_id": 0,
		"updated_at":         time.Now(),
	}).Error
	if err != nil {
		return fmt.Errorf("撤销榜单想看标记失败: %w", err)
	}
	return nil
}

// watchlistOrigins 给一页条目标来源：被某个 want 标记认领的是「榜单」，其余是「手动」。
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
		Where("mark = ? AND watchlist_entry_id IN ?", models.MovieChartMarkWant, ids).
		Pluck("watchlist_entry_id", &owned).Error
	if err != nil {
		return nil, fmt.Errorf("读取想看来源失败: %w", err)
	}
	for _, id := range owned {
		origins[id] = WatchlistOriginChart
	}
	return origins, nil
}
