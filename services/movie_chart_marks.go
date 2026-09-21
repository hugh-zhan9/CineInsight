package services

import (
	"errors"
	"fmt"
	"log"
	"strconv"

	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 榜单标记的状态机：想看 / 不想看 / 已看的写入、改标记与撤销，以及「想看」与
// 想看片单的联动。状态图见概要设计 §4.3，归属边界见 D-MC13，跨边界写入顺序见
// 需求设计文档 §5。
//
// 依赖方向是单向的 MovieChartService → WatchlistService：片单一侧完全不感知榜单。
// 片单服务是**构造时注入的字段** s.watchlist（P-005 从方法入参收上去的，理由记在
// MovieChartService 的字段注释里）。
//
// 标记路径的串行化用的是 s.markMu（同上，字段与完整理由都在 movie_chart_service.go）：
// 它让本服务对 movie_chart_marks 成为事实上的单写者，挡住两次绑定调用交错留下的
// 无主片单条目。ClearMark 的删除守卫挡的是同一种伤害，两道都留着。
//
// 全程不用事务包住「片单写入 + 标记写入」：两者是两个服务、两张表、两套唯一键，
// 靠固定顺序把崩溃后果收敛（见 markEntryWatchlistLink 的注释），不靠一个跨服务的
// 大事务。

// MovieChartMarkResult 是一次标记动作的结果，回给绑定层（需求设计文档 §6.1）。
//
// WatchlistCreated 与 WatchlistConflict 互斥，且只有 want 这一个标记会置位：
// 前者表示片单里新添了一条由榜单创建、撤销时会一并删掉的记录，后者表示撞上了
// 用户已有的同名同类型条目——界面据此显示既有的「该片名已在想看片单中」。
type MovieChartMarkResult struct {
	Mark              string `json:"mark"`
	WatchlistCreated  bool   `json:"watchlist_created"`
	WatchlistConflict bool   `json:"watchlist_conflict"`
}

// ErrMovieChartEntryNotFound 表示要标记的豆瓣 ID 不在本地榜单缓存里。
//
// 标记的 title / release_year / poster_url 三列是**标记时快照**，快照只能从缓存行
// 取：没有缓存行就没有可信的片名与年份，硬记一条只有豆瓣 ID 的标记，已看页上就是
// 一张无名无年份的卡片（需求设计文档 §7）。
var ErrMovieChartEntryNotFound = errors.New("该影片不在本地榜单缓存中，请先刷新该年榜单")

// ErrMovieChartMarkUnsupported 表示标记值不在 want / skip / watched 三个里。
var ErrMovieChartMarkUnsupported = errors.New("不支持的榜单标记")

// movieChartMarkSupported 校验标记枚举。
//
// 这与 StartRefresh 把年份区间留给绑定层不是一回事：年份上下界是两处各写一套早晚
// 会对不上的边界，而这三个取值由 models 唯一拥有，这里只是照着那一份判断。放过一个
// 未知值的后果也不同——它会**落库**成一个谁都不认识的标记，榜单既不显示也撤不掉。
func movieChartMarkSupported(mark string) bool {
	switch mark {
	case models.MovieChartMarkWant, models.MovieChartMarkSkip, models.MovieChartMarkWatched:
		return true
	}
	return false
}

// MarkEntry 写入或改写一个条目的标记，返回这次动作对想看片单做了什么。
//
// 三条分支：
//   - 重复点同一个标记：幂等，只刷新 marked_at，**不再碰片单**。再调一次
//     WatchlistService.Create 必然撞上自己刚建的那条，撞名分支会把
//     watchlist_entry_id 抹成 0，撤销时就再也删不掉那条由榜单创建的记录了。
//   - 从 want 改成别的：先跑 want 的撤销副作用（删掉本次由榜单创建的片单条目），
//     再落新标记（概要设计 §4.3）。
//   - 落到 want：先建片单条目、再写标记，见 markEntryWatchlistLink。
func (s *MovieChartService) MarkEntry(doubanID, mark string) (*MovieChartMarkResult, error) {
	if s == nil {
		return nil, errors.New("年度榜单服务不可用")
	}
	s.markMu.Lock()
	defer s.markMu.Unlock()
	return s.markEntry(doubanID, mark)
}

// markEntry 是 MarkEntry 去掉串行化之后的内核。调用方必须持有 s.markMu。
//
// 拆出来只为一件事：测试要模拟一个**不受这把锁约束**的写入者，从库层的角度验证
// 撤销守卫。同一个 goroutine 再进一次 MarkEntry 会撞上不可重入的互斥量。
func (s *MovieChartService) markEntry(doubanID, mark string) (*MovieChartMarkResult, error) {
	if s.watchlist == nil {
		return nil, errors.New("想看片单服务不可用")
	}
	if !movieChartMarkSupported(mark) {
		return nil, fmt.Errorf("%w：%q", ErrMovieChartMarkUnsupported, mark)
	}
	entry, err := s.loadChartEntry(doubanID)
	if err != nil {
		return nil, err
	}
	existing, err := s.loadChartMark(doubanID)
	if err != nil {
		return nil, err
	}

	if existing != nil && existing.Mark == mark {
		// 幂等分支只动 marked_at：快照三列保持第一次标记时的值（需求设计文档 §7
		// 「对同一条目重复点同一个标记 → 幂等，只刷新 marked_at」）。
		if err := s.touchChartMark(existing.ID); err != nil {
			return nil, err
		}
		return &MovieChartMarkResult{Mark: mark}, nil
	}

	// 改标记时**先**跑上一个标记的撤销副作用。失败就整个动作放弃：此刻标记还是
	// 原样、片单条目还挂在它名下，用户重试一次即可。反过来（先落新标记再删片单）
	// 一旦删失败，那条片单记录就没有任何标记再指向它，谁也清不掉了。
	if err := s.undoWantWatchlistEntry(existing); err != nil {
		return nil, err
	}

	result := &MovieChartMarkResult{Mark: mark}
	watchlistEntryID := uint(0)
	// 片名先按 size:200 截断，片单与标记两边用同一个值：片名来自豆瓣，长度不受
	// 任何人约束，而 watchlist_entries.title 与 movie_chart_marks.title 都是
	// varchar(200)。Postgres 上超长直接报 22001，SQLite 不校验会默默存下——只跑
	// 默认后端完全看不见（P-003 在 upsertListPage 上踩过同一个坑）。
	//
	// 还有一处**跨服务的隐式耦合**：movieChartTitleLimit 恰好等于
	// validateWatchlistText 对用户输入的 200 字符上限。截断到 200 之后
	// WatchlistService.Create 才收得下这个片名；那边的上限一旦降到 200 以下，
	// 超长片名会变成「标记记上了、片单没建成」——MarkEntry 直接返回校验错误。
	title := movieChartTruncateTitle(entry.Title)
	if mark == models.MovieChartMarkWant {
		watchlistEntryID, result.WatchlistConflict, err = s.markEntryWatchlistLink(title)
		if err != nil {
			return nil, err
		}
		result.WatchlistCreated = watchlistEntryID != 0
	}
	if err := s.upsertChartMark(entry, title, mark, watchlistEntryID); err != nil {
		return nil, err
	}
	return result, nil
}

// ClearMark 撤销标记。没有标记时是幂等的空操作，不报错。
//
// **不要求缓存里还有这个条目**：已看记录是用户产生的事实，缓存被清空或条目从豆瓣
// 下架之后（标记表靠快照三列自立门户，D-MC05）照样要能撤销。
//
// 删除本身带守卫，只删读到的那个标记；读完之后标记被改写过就什么都不做，见下。
func (s *MovieChartService) ClearMark(doubanID string) error {
	if s == nil {
		return errors.New("年度榜单服务不可用")
	}
	s.markMu.Lock()
	defer s.markMu.Unlock()
	return s.clearMark(doubanID)
}

// clearMark 是 ClearMark 去掉串行化之后的内核。调用方必须持有 s.markMu，
// 拆分的理由同 markEntry。
func (s *MovieChartService) clearMark(doubanID string) error {
	if s.watchlist == nil {
		return errors.New("想看片单服务不可用")
	}
	existing, err := s.loadChartMark(doubanID)
	if err != nil {
		return err
	}
	if existing == nil {
		return nil
	}
	// 顺序同改标记：先删片单条目，再删标记行。删片单失败时标记原样留着，重试一次
	// 就能接着往下走；倒过来则会留下一条再也没人认领的片单记录。
	if err := s.undoWantWatchlistEntry(existing); err != nil {
		return err
	}
	// 删除带**条件更新守卫**：只删「我刚才读到的那个标记」。需求设计文档 §5 通篇
	// 用条件更新而不是锁，这一句是同一个模式用在最后一处写入上。
	//
	// 守卫比的是标记值与片单归属，**不能只比 id**：改标记走的是按 douban_id 的
	// upsert，冲突时更新的就是这一行，主键根本不变。只比 id 的话，读到之后被改成
	// want 的那一行照样会被删掉，而它刚建的片单记录就此无人认领——界面上多出一条
	// 追溯不到来源的「想看」，之后没有任何标记能再把它清掉。这两列一变，就说明
	// 这已经不是我打算撤销的那个标记了。
	//
	// 代价是这条守卫不拦「同一个标记被重复点了一下」（只有 marked_at 变）：那种
	// 情况下照常删除。要拦的是**归属发生变化**，不是所有并发写。
	//
	// s.markMu 已经把进程内的交错排除掉了，所以这条守卫如今是第二道防线：
	// 锁保证「读到的就是最新的」，守卫保证「即便不是，也不会误删」。两道都留着，
	// 将来谁把锁挪走或绕开入口，库里仍然不会出现无主的片单记录。
	result := s.db.Where("id = ? AND mark = ? AND watchlist_entry_id = ?",
		existing.ID, existing.Mark, existing.WatchlistEntryID).
		Delete(&models.MovieChartMark{})
	if result.Error != nil {
		return fmt.Errorf("撤销 %s 的标记失败: %w", doubanID, result.Error)
	}
	if result.RowsAffected == 0 {
		// 影响 0 行＝读到之后有人改过它。丢弃并记日志、**不报错**，与详情写回撞上
		// 认领守卫时的处置一致（movie_chart_refresh.go 的 writeBackDetail）。
		log.Printf("[MovieChart] discard clear mark douban=%s reason=撤销守卫不成立（标记已被改写）", doubanID)
	}
	return nil
}

// markEntryWatchlistLink 建「想看」对应的片单条目，返回 (片单条目 ID, 是否撞名)。
//
// **顺序是承重的：先建片单条目，再写标记行**（需求设计文档 §5）。这两次写入不是
// 一个原子单元——片单是另一个服务、另一张表、另一套唯一键，没法用一个事务罩住。
// 固定成这个顺序是为了限定崩溃的后果：中间挂掉只会多出一条用户看得见、也删得掉的
// 片单记录；反过来则会留下一个指向不存在片单 ID 的标记，而撤销会照着那个 ID 去删，
// 删掉的可能是别人的行。
//
// 撞名（用户自己早先手输过同名同类型的条目）时**标记照记、但不记录归属**：返回
// 的 ID 是 0，撤销时就不会去动那条记录。榜单没有权限删除用户手工维护的数据
// （D-MC13）。
func (s *MovieChartService) markEntryWatchlistLink(title string) (uint, bool, error) {
	created, err := s.watchlist.Create(title, models.WatchlistKindMovie)
	if err != nil {
		if errors.Is(err, ErrWatchlistTitleExists) {
			return 0, true, nil
		}
		return 0, false, fmt.Errorf("添加想看片单条目失败: %w", err)
	}
	return created.ID, false, nil
}

// undoWantWatchlistEntry 跑「想看」的撤销副作用：只删**本次由榜单创建**的那条片单
// 记录。
//
// 三种情况都直接返回、什么都不删：没有标记、标记不是 want、watchlist_entry_id 为 0
// （撞名复用了用户自己的条目）。最后一种正是 TC-10 要守的那条线。
//
// 片单条目已经不在了（用户自己先删掉的）算撤销目的已达成，不报错。
//
// 这两件事是**耦合**的，改动时要一起看：正因为下面容忍
// ErrWatchlistEntryNotFound，上面 watchlist_entry_id == 0 这个判断在行为上才是
// 冗余的——去掉它只会走到 Delete(0)，查不到行、被容忍，照样什么都不删（测试因此
// 也测不出差别）。反过来说，一旦有人把这份容忍改成报错，那个 == 0 的判断立刻重新
// 承重：撞名标记的撤销会从「什么都不做」变成「报一条查无记录的错」。
func (s *MovieChartService) undoWantWatchlistEntry(existing *models.MovieChartMark) error {
	if existing == nil || existing.Mark != models.MovieChartMarkWant || existing.WatchlistEntryID == 0 {
		return nil
	}
	// 走 WatchlistService.Delete 而不是自己拼一条 DELETE：那条路径连带清理补全下来
	// 的海报，绕过它会在 media-details/watchlist/<id>/ 下留孤儿文件。
	if err := s.watchlist.Delete(existing.WatchlistEntryID); err != nil {
		if errors.Is(err, ErrWatchlistEntryNotFound) {
			log.Printf("[MovieChart] undo want douban=%s watchlist_entry=%d 已不存在，按撤销成功处理",
				existing.DoubanID, existing.WatchlistEntryID)
			return nil
		}
		return fmt.Errorf("移除榜单创建的想看片单条目失败: %w", err)
	}
	return nil
}

// upsertChartMark 按豆瓣 ID upsert 一条标记（一个条目同时只有一个标记）。
//
// 快照三列在这里取值：title / release_year / poster_url 记的是**这一次标记时**
// 缓存行的样子，让已看页在缓存被整年重建、或条目从豆瓣下架之后仍然完整可用
// （D-MC05）。它们不是对缓存表的引用，因此后续列表刷新改了片名也不会回写这里。
func (s *MovieChartService) upsertChartMark(entry models.MovieChartEntry, title, mark string, watchlistEntryID uint) error {
	now := s.now()
	row := models.MovieChartMark{
		DoubanID:         entry.DoubanID,
		Mark:             mark,
		ReleaseYear:      movieChartReleaseYear(entry.ReleaseDate),
		Title:            title,
		PosterURL:        entry.PosterURL,
		WatchlistEntryID: watchlistEntryID,
		MarkedAt:         now,
	}
	// updated_at 必须显式列进来：GORM 在 DoUpdates 这条路径上不维护它
	// （services/image_ai_tagging_service.go:773 与 writeYearState 同此写法）。
	// SET 右侧全是字面量，没有一处引用目标表的列，因此不会踩 Postgres 的 42702。
	err := s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "douban_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"mark":               row.Mark,
			"release_year":       row.ReleaseYear,
			"title":              row.Title,
			"poster_url":         row.PosterURL,
			"watchlist_entry_id": row.WatchlistEntryID,
			"marked_at":          row.MarkedAt,
			"updated_at":         now,
		}),
	}).Create(&row).Error
	if err != nil {
		return fmt.Errorf("写入 %s 的标记失败: %w", entry.DoubanID, err)
	}
	return nil
}

// touchChartMark 只刷新 marked_at（与 updated_at），供重复点同一标记的幂等分支用。
func (s *MovieChartService) touchChartMark(id uint) error {
	now := s.now()
	err := s.db.Model(&models.MovieChartMark{}).Where("id = ?", id).
		Updates(map[string]any{"marked_at": now, "updated_at": now}).Error
	if err != nil {
		return fmt.Errorf("刷新标记时间失败: %w", err)
	}
	return nil
}

// loadChartEntry 读一条缓存条目，没有时返回 ErrMovieChartEntryNotFound。
func (s *MovieChartService) loadChartEntry(doubanID string) (models.MovieChartEntry, error) {
	var entry models.MovieChartEntry
	if err := s.db.Where("douban_id = ?", doubanID).First(&entry).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.MovieChartEntry{}, fmt.Errorf("%w（豆瓣 ID %s）", ErrMovieChartEntryNotFound, doubanID)
		}
		return models.MovieChartEntry{}, fmt.Errorf("读取榜单条目 %s 失败: %w", doubanID, err)
	}
	return entry, nil
}

// loadChartMark 读一个条目当前的标记，没有标记时返回 (nil, nil)。
func (s *MovieChartService) loadChartMark(doubanID string) (*models.MovieChartMark, error) {
	var mark models.MovieChartMark
	if err := s.db.Where("douban_id = ?", doubanID).First(&mark).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 %s 的标记失败: %w", doubanID, err)
	}
	return &mark, nil
}

// movieChartReleaseYear 从 release_date（YYYY-MM-DD 或空串）取上映年份快照。
//
// **不拿 movie_chart_entries.year 兜底**：那一列存的是抓取时用的 tags 年份，不是
// 上映年份，跨年上映与豆瓣改档时两者会不同（models/movie_chart.go 对该列的说明）。
// 取不到就是 0，已看页把 0 单列成「年份未知」一组——需求设计文档 §7 明说这里**不猜**。
func movieChartReleaseYear(releaseDate string) int {
	if len(releaseDate) < 4 {
		return 0
	}
	year, err := strconv.Atoi(releaseDate[:4])
	if err != nil || year <= 0 {
		return 0
	}
	return year
}
