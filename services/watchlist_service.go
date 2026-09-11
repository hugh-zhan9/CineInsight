package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// WatchlistService 只管理片名备忘，不参与扫描、下载或媒体关系维护。
//
// 它另外承担想看条目海报的生命周期（D-WM09）：落盘、取图与删除条目时的清理都
// 走同一个 ManagedImageService，形态与 PersonService / CollectionService 一致。
// 下载与落盘的入口在 watchlist_artwork.go。
type WatchlistService struct {
	images *ManagedImageService
	// enrich 是在线补全 worker 的全部可变状态，实现在 watchlist_enrichment.go。
	// 零值可用：没接登记表、没接事件口、没启动 worker 的服务（单测夹具、
	// 只做增删改查的路径）照常工作。
	enrich watchlistEnrichment
}

// NewWatchlistService 照 NewPersonService / NewCollectionService 的形态构造：
// dataDir 只用来定位托管图片根目录，片单数据仍走 database.DB。
func NewWatchlistService(dataDir string) *WatchlistService {
	return &WatchlistService{images: NewManagedImageService(dataDir)}
}

// WatchlistPage 按添加顺序倒序返回；NextID 为零时没有下一页。
type WatchlistPage struct {
	Entries []models.WatchlistEntry `json:"entries"`
	NextID  uint                    `json:"next_id"`
}

var ErrWatchlistTitleExists = errors.New("该片名已在想看片单中")

var ErrWatchlistEntryNotFound = errors.New("这条想看记录已不存在，请刷新列表")

// ErrWatchlistKindUnsupported 表示传进来的类型不在 D-WM02 的五个里。
var ErrWatchlistKindUnsupported = errors.New("不支持的想看类型")

// normalizeWatchlistKind 校验类型入参（D-WM13）：空值按 movie，非法值**拒绝**。
//
// 非法值不静默回退成 movie：一次前端缺陷会因此变成一整批存错类型的条目，而用户
// 看不到任何迹象，之后每条都补全到错的源上。空值是另一回事——旧调用方压根没有
// 类型这个概念，按默认值处理是它们的既有语义。
func normalizeWatchlistKind(kind string) (string, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return models.WatchlistKindMovie, nil
	}
	switch kind {
	case models.WatchlistKindMovie, models.WatchlistKindTV, models.WatchlistKindAnime,
		models.WatchlistKindShow, models.WatchlistKindAV:
		return kind, nil
	}
	return "", fmt.Errorf("%w：%q", ErrWatchlistKindUnsupported, kind)
}

func validateWatchlistText(text string, required bool) (string, error) {
	text = strings.TrimSpace(text)
	if required && text == "" {
		return "", errors.New("请输入片名")
	}
	if !utf8.ValidString(text) || strings.ContainsFunc(text, unicode.IsControl) {
		return "", errors.New("片名不能包含控制字符或无效文本")
	}
	if utf8.RuneCountInString(text) > 200 {
		return "", errors.New("片名或搜索词不能超过 200 个字符")
	}
	return text, nil
}

// List 查询片名；cursorID 为零取首页，limit 沿用实体列表的有界页大小。
func (s *WatchlistService) List(keyword string, cursorID uint, limit int) (*WatchlistPage, error) {
	keyword, err := validateWatchlistText(keyword, false)
	if err != nil {
		return nil, err
	}
	limit = normalizeEntityPageLimit(limit)
	query := database.DB.Model(&models.WatchlistEntry{})
	if keyword != "" {
		query = query.Where("LOWER(title) LIKE ? ESCAPE '\\'", "%"+escapeSQLLike(strings.ToLower(keyword))+"%")
	}
	if cursorID != 0 {
		query = query.Where("id < ?", cursorID)
	}
	page := &WatchlistPage{Entries: make([]models.WatchlistEntry, 0)}
	if err := query.Order("id DESC").Limit(limit + 1).Find(&page.Entries).Error; err != nil {
		return nil, fmt.Errorf("读取想看片单失败: %w", err)
	}
	if len(page.Entries) > limit {
		page.Entries = page.Entries[:limit]
		page.NextID = page.Entries[limit-1].ID
	}
	return page, nil
}

// Create 保存唯一片名并指定类型（D-WM13），首尾空白由校验统一移除。
//
// 类型是有路由后果的一级维度（movie 走 TMDB、anime 走 Bangumi、av 走 FANZA+JavBus），
// 所以只留这一个入口、不提供「不说类型」的重载：kind 为空按 movie，非法值拒绝，
// **不静默回退**。
func (s *WatchlistService) Create(title, kind string) (*models.WatchlistEntry, error) {
	title, err := validateWatchlistText(title, true)
	if err != nil {
		return nil, err
	}
	kind, err = normalizeWatchlistKind(kind)
	if err != nil {
		return nil, err
	}
	// 补全状态一律从 pending 起步——凭证是否配置由后台 worker 判定，不在创建时短路。
	entry := &models.WatchlistEntry{
		Title:            title,
		Kind:             kind,
		EnrichmentStatus: models.WatchlistEnrichmentPending,
	}
	if err := database.DB.Create(entry).Error; err != nil {
		if watchlistTitleConflict(err) {
			return nil, ErrWatchlistTitleExists
		}
		return nil, fmt.Errorf("添加想看记录失败: %w", err)
	}
	// 新条目就是补全 worker 的待办，立刻唤醒它，不用等下一次巡检。worker 没起来
	// 时这是空操作。条目本身随返回值给到调用方，所以这里不额外发进度事件——
	// pending 是初始状态，不是 §3.1 转换表里的落点。
	s.TriggerEnrichment()
	return entry, nil
}

// Update 只改指定记录的片名，不会重新创建已移除的记录。
//
// 用户编辑即接管这条记录（§3.1 / D-WM06）：状态一律转 manual，在补全期间编辑
// 会让那一轮的写回撞上认领守卫而被丢弃。失败分类码按转换表**不动**——那一行的
// 效果列是空的，只有手动重试才清它。
func (s *WatchlistService) Update(id uint, title string) error {
	title, err := validateWatchlistText(title, true)
	if err != nil {
		return err
	}
	result := database.DB.Model(&models.WatchlistEntry{}).Where("id = ?", id).Updates(map[string]any{
		"title":             title,
		"enrichment_status": models.WatchlistEnrichmentManual,
	})
	if result.Error != nil {
		if watchlistTitleConflict(result.Error) {
			return ErrWatchlistTitleExists
		}
		return fmt.Errorf("修改想看记录失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrWatchlistEntryNotFound
	}
	// 状态落定就发一次进度。读不回来（这一瞬又被删了）就不发，编辑本身已经成功。
	var entry models.WatchlistEntry
	if err := database.DB.First(&entry, id).Error; err == nil {
		s.emitEnrichProgress(entry)
	}
	return nil
}

// Delete 移除指定片名备忘，不操作媒体记录或磁盘文件；补全下来的海报随条目一起
// 清掉，否则 media-details/watchlist/<id>/ 下会留孤儿文件（TC-04）。
//
// 顺序是先读路径、再删行、最后删图。反过来（先删图再删行）会在删行失败时留下一条
// 指向不存在文件的记录，正是 §3.2 回滚边界要避免的那种状态。删图失败时行已经没了，
// 只能把「条目已删除但图片没清掉」如实报出来，照 PersonService 的既有做法。
func (s *WatchlistService) Delete(id uint) error {
	var entry models.WatchlistEntry
	if err := database.DB.Select("id", "poster_path").First(&entry, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrWatchlistEntryNotFound
		}
		return fmt.Errorf("移除想看记录失败: %w", err)
	}
	result := database.DB.Where("id = ?", id).Delete(&models.WatchlistEntry{})
	if result.Error != nil {
		return fmt.Errorf("移除想看记录失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrWatchlistEntryNotFound
	}
	if entry.PosterPath == "" {
		return nil
	}
	if err := s.images.Remove(entry.PosterPath); err != nil {
		return fmt.Errorf("想看记录已移除但海报清理失败: %w", err)
	}
	return nil
}

// ResolveWatchlistPoster 把条目 id 解析成可直接回给 HTTP 的海报文件，形态照
// PersonService.ResolvePersonAvatar：条目不存在、没有海报、文件已丢都统一回
// os.ErrNotExist，由路由翻译成 404。
func (s *WatchlistService) ResolveWatchlistPoster(entryID uint) (ManagedImageAsset, error) {
	var entry models.WatchlistEntry
	if err := database.DB.Select("id", "poster_path").First(&entry, entryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ManagedImageAsset{}, os.ErrNotExist
		}
		return ManagedImageAsset{}, err
	}
	if entry.PosterPath == "" {
		return ManagedImageAsset{}, os.ErrNotExist
	}
	return s.images.Resolve(entry.PosterPath)
}

// WatchlistCandidateView 是回给前端的一条候选（D-WM13 的重选入口）。
//
// 只给挑选时真要看的字段：SourceItemID 是 ApplyCandidate 的入参，其余供用户辨认
// 是不是同一部片。**不透传源站的海报外链**——那是一条绕过用户所配资料源代理的
// 出网请求，从界面上发出去既可能加载不出来，也违背「所有外部资料源请求经代理
// 客户端发出」（AC-06）。
type WatchlistCandidateView struct {
	SourceName    string  `json:"source_name"`
	SourceItemID  string  `json:"source_item_id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title"`
	Year          int     `json:"year"`
	Overview      string  `json:"overview"`
	Rating        float64 `json:"rating"`
}

// loadWatchlistEntry 读一条完整记录，不存在时统一回 ErrWatchlistEntryNotFound。
func (s *WatchlistService) loadWatchlistEntry(id uint) (models.WatchlistEntry, error) {
	var entry models.WatchlistEntry
	if err := database.DB.First(&entry, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.WatchlistEntry{}, ErrWatchlistEntryNotFound
		}
		return models.WatchlistEntry{}, fmt.Errorf("读取想看记录失败: %w", err)
	}
	return entry, nil
}

// watchlistMetadataChainFor 装配出网件并取该类型的适配器链。
func (s *WatchlistService) watchlistMetadataChainFor(kind string) (watchlistEnrichmentSources, []WatchlistMetadataSource, error) {
	sources, err := s.enrichmentSources()
	if err != nil {
		return watchlistEnrichmentSources{}, nil, err
	}
	chain, err := sources.registry.Chain(WatchlistMetadataKind(kind))
	if err != nil {
		return watchlistEnrichmentSources{}, nil, err
	}
	return sources, chain, nil
}

// ListCandidates 按条目当前的片名与类型走链搜索，把源给出的候选原样交给用户挑
// （D-WM13）。顺序就是源给的匹配度顺序，这里不重排，也不改条目的任何字段。
//
// 走的是 SearchWatchlistMetadataChain 而不是自动补全那条「同一个源上搜索加取详情」
// 的路径：列举只要候选，没必要为每条候选都打一次详情请求。
func (s *WatchlistService) ListCandidates(id uint) ([]WatchlistCandidateView, error) {
	entry, err := s.loadWatchlistEntry(id)
	if err != nil {
		return nil, err
	}
	// 列举只问搜索，用不到海报下载客户端。
	_, chain, err := s.watchlistMetadataChainFor(entry.Kind)
	if err != nil {
		return nil, fmt.Errorf("读取候选失败: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), watchlistEnrichRequestTimeout)
	defer cancel()
	candidates, err := SearchWatchlistMetadataChain(ctx, chain, WatchlistMetadataKind(entry.Kind), entry.Title)
	if err != nil {
		return nil, fmt.Errorf("读取候选失败: %w", err)
	}
	views := make([]WatchlistCandidateView, 0, len(candidates))
	for _, candidate := range candidates {
		views = append(views, WatchlistCandidateView{
			SourceName:    candidate.SourceName,
			SourceItemID:  candidate.SourceItemID,
			Title:         candidate.Title,
			OriginalTitle: candidate.OriginalTitle,
			Year:          candidate.Year,
			Overview:      candidate.Overview,
			Rating:        candidate.Rating,
		})
	}
	return views, nil
}

// ApplyCandidate 把用户选中的候选取详情后写成该条目的补全结果（D-WM13）。
//
// 状态落 manual 而不是 succeeded：这是用户亲手挑的结果，按 §3.1 的「用户编辑 →
// manual」处理，后台补全不再覆盖它。认领标识一并清空——补全正跑着的那一轮写回
// 会因此撞上守卫被丢弃，用户输入永远优先（D-WM06）。
//
// 顺序照 §3.2 的回滚边界：先落盘海报、再写库。海报失败只影响图，文字字段照常写。
func (s *WatchlistService) ApplyCandidate(id uint, sourceItemID string) error {
	sourceItemID = strings.TrimSpace(sourceItemID)
	if sourceItemID == "" {
		return errors.New("请选择要应用的候选")
	}
	if len(sourceItemID) > watchlistSourceItemIDLimit {
		return fmt.Errorf("源条目 ID 超过 %d 字节", watchlistSourceItemIDLimit)
	}
	entry, err := s.loadWatchlistEntry(id)
	if err != nil {
		return err
	}
	sources, chain, err := s.watchlistMetadataChainFor(entry.Kind)
	if err != nil {
		return fmt.Errorf("应用候选失败: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), watchlistEnrichRequestTimeout)
	defer cancel()
	detail, err := DetailWatchlistMetadataChain(ctx, chain, WatchlistMetadataKind(entry.Kind), sourceItemID)
	if err != nil {
		return fmt.Errorf("应用候选失败: %w", err)
	}
	// 源侧 ID 是承重字段（重查详情靠它），装不下就只能算源返回了不可用的数据。
	// 截断存进去更糟：那条 ID 再也回不到源上，而库里看着像是成功了。
	if len(detail.SourceItemID) > watchlistSourceItemIDLimit {
		return fmt.Errorf("应用候选失败: 源条目 ID 超过 %d 字节", watchlistSourceItemIDLimit)
	}

	poster := managedImageImport{}
	if strings.TrimSpace(detail.PosterURL) != "" {
		imported, posterErr := s.DownloadPoster(ctx, sources.poster, entry.ID, detail.PosterURL)
		if posterErr != nil {
			log.Printf("[WatchlistEnrich] apply candidate poster download failed id=%d source=%s err=%v", entry.ID, detail.SourceName, posterErr)
		} else {
			poster = imported
		}
	}
	posterPath := entry.PosterPath
	if poster.RelativePath != "" {
		posterPath = poster.RelativePath
	}
	enrichedAt := time.Now()
	result := database.DB.Model(&models.WatchlistEntry{}).Where("id = ?", id).Updates(map[string]any{
		"enrichment_status": models.WatchlistEnrichmentManual,
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
	if result.Error != nil {
		return fmt.Errorf("应用候选失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		// 行在取详情这段时间里被删掉了。刚落盘的图没人引用，删掉，别在托管目录
		// 里留孤儿（TC-04）。只删本次新建的：Created 为 false 说明内容寻址命中了
		// 已有文件，那可能正是别的条目在用的那张。
		if poster.Created && poster.RelativePath != "" {
			if err := s.RemovePoster(poster.RelativePath); err != nil {
				log.Printf("[WatchlistEnrich] apply candidate poster cleanup failed id=%d path=%s err=%v", id, poster.RelativePath, err)
			}
		}
		return ErrWatchlistEntryNotFound
	}
	// 换了图才清旧图。内容寻址下同一张图的相对路径一致，那时旧路径就是新路径，
	// 删掉等于把条目正在用的图删了。
	if entry.PosterPath != "" && entry.PosterPath != posterPath {
		if err := s.RemovePoster(entry.PosterPath); err != nil {
			log.Printf("[WatchlistEnrich] apply candidate stale poster cleanup failed id=%d err=%v", id, err)
		}
	}
	entry.EnrichmentStatus = models.WatchlistEnrichmentManual
	entry.EnrichmentError = ""
	entry.SourceName = detail.SourceName
	s.emitEnrichProgress(entry)
	log.Printf("[WatchlistEnrich] applied candidate id=%d source=%s item=%s", entry.ID, detail.SourceName, detail.SourceItemID)
	return nil
}

// watchlistTitleConflict 把撞名翻译成 ErrWatchlistTitleExists。两个后端报错的形状
// 不同：Postgres 给出索引名 idx_watchlist_title_kind，SQLite 给出列清单
// "watchlist_entries.title, watchlist_entries.kind"。唯一键换成 (title, kind) 之后
// 这两条判据都按新形状显式匹配——判据一旦失配，用户看到的就不再是「该片名已在想看
// 片单中」而是一条通用数据库错误，所以 TestWatchlistDuplicateTitleMessage 钉住文案。
//
// 还有一处隐式耦合：两个后端都用 &gorm.Config{} 打开（database/database.go），
// TranslateError 没开。一旦有人全局打开它，Postgres 驱动会把 23505 收敛成
// gorm.ErrDuplicatedKey，索引名从报错里消失，判据 A 随即失配；SQLite 上判据 B
// 仍然命中，所以只跑 SQLite 的测试也不会红。改那个开关时必须回来看这里。
func watchlistTitleConflict(err error) bool {
	message := err.Error()
	return strings.Contains(message, "idx_watchlist_title_kind") ||
		strings.Contains(message, "UNIQUE constraint failed: watchlist_entries.title, watchlist_entries.kind")
}
