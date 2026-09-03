package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 建议作品集（D-023、D-024、D-025）。
//
// 只产出候选：分析永远不建作品集、不改标题、不改文件名。用户在面板里确认，
// 才复用 CollectionService 真正写关系。
//
// 轻任务：只读库 + 内存里解析文件名，不碰 ffmpeg，因此不经 MediaWorkSlot；
// 自动路径仍要经 IdleGate（D-030），入口在 app 层。

var (
	// ErrCollectionSuggestionNotPending 对应设计 7.3.2 的 suggestion_not_pending。
	ErrCollectionSuggestionNotPending = errors.New("suggestion_not_pending")
	// ErrCollectionSuggestionMemberMismatch 对应 member_mismatch：确认时传来的
	// 视频不在候选成员里。
	ErrCollectionSuggestionMemberMismatch = errors.New("member_mismatch")
	// ErrCollectionSuggestionNameInvalid 对应 collection_name_invalid。
	ErrCollectionSuggestionNameInvalid = errors.New("collection_name_invalid")
)

// collectionSuggestionPageSize 是分析时取视频的分页大小。
const collectionSuggestionPageSize = 500

// CollectionSuggestionStatus 是分析任务的状态快照。
type CollectionSuggestionStatus struct {
	Running   bool `json:"running"`
	Cancelled bool `json:"cancelled"`
	Completed bool `json:"completed"`
	// Total 是本轮要扫的活跃视频数，Scanned 是已解析的条数。
	Total   int `json:"total"`
	Scanned int `json:"scanned"`
	// Matched 是解析出剧集模式的视频数；Pending 是本轮结束后待审阅的候选数。
	Matched   int        `json:"matched"`
	Pending   int        `json:"pending"`
	LastError string     `json:"last_error"`
	StartedAt *time.Time `json:"started_at" ts_type:"string"`
	UpdatedAt *time.Time `json:"updated_at" ts_type:"string"`
}

// CollectionSuggestionMemberView 是候选成员在面板上的呈现形态。
type CollectionSuggestionMemberView struct {
	VideoID  uint   `json:"video_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Season   *int   `json:"season"`
	Episode  *int   `json:"episode"`
	Position int    `json:"position"`
	// ThumbnailURL 走既有缩略图路由，与片库同一套。
	ThumbnailURL string `json:"thumbnail_url"`
	// MultipleVersions 表示同一集还有别的文件（03 与 03v2），面板据此提示。
	MultipleVersions bool `json:"multiple_versions"`
}

// CollectionSuggestionView 是一条待审阅候选。
type CollectionSuggestionView struct {
	ID          uint                             `json:"id"`
	ScanRoot    string                           `json:"scan_root"`
	SeriesName  string                           `json:"series_name"`
	Status      string                           `json:"status"`
	MemberCount int                              `json:"member_count"`
	Members     []CollectionSuggestionMemberView `json:"members"`
	CreatedAt   time.Time                        `json:"created_at" ts_type:"string"`
}

// CollectionSuggestionService 分析剧集候选并把确认动作转成作品集写入。
type CollectionSuggestionService struct {
	collections *CollectionService
	now         func() time.Time

	mu       sync.Mutex
	status   CollectionSuggestionStatus
	cancel   context.CancelFunc
	worker   sync.WaitGroup
	stopping bool
	emitter  func(CollectionSuggestionStatus)
	registry *BackgroundTaskRegistry

	stopMu sync.Mutex
	// confirmMu 串行化确认与忽略：两个都会翻候选状态并写作品集关系，
	// 并发进来会让"同名活跃作品集存在则加入"这一步互相看不见对方。
	confirmMu sync.Mutex
}

func NewCollectionSuggestionService(collections *CollectionService) *CollectionSuggestionService {
	return &CollectionSuggestionService{collections: collections, now: time.Now}
}

func (s *CollectionSuggestionService) SetEventEmitter(emitter func(CollectionSuggestionStatus)) {
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
}

// SetBackgroundTaskRegistry 接入后台任务登记表（D-014）。
func (s *CollectionSuggestionService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
}

// Analyze 启动一轮分析并立刻返回状态。已经在跑时返回当前状态、不重复启动。
func (s *CollectionSuggestionService) Analyze(parent context.Context) (CollectionSuggestionStatus, error) {
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return CollectionSuggestionStatus{}, errors.New("剧集分析正在停止")
	}
	if s.status.Running {
		status := s.status
		s.mu.Unlock()
		return status, nil
	}
	total, err := countActiveVideosForSuggestions(parent)
	if err != nil {
		s.mu.Unlock()
		return CollectionSuggestionStatus{}, err
	}
	now := s.now()
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.status = CollectionSuggestionStatus{Running: true, Total: total, StartedAt: &now, UpdatedAt: &now}
	status, emitter, registry := s.status, s.emitter, s.registry
	s.worker.Add(1)
	s.mu.Unlock()

	registry.Begin(BackgroundTaskCollectionSuggest)
	if emitter != nil {
		emitter(status)
	}
	go s.run(ctx, registry)
	return status, nil
}

func (s *CollectionSuggestionService) Status() CollectionSuggestionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *CollectionSuggestionService) Cancel() error {
	s.mu.Lock()
	cancel, running := s.cancel, s.status.Running
	s.mu.Unlock()
	if !running || cancel == nil {
		return errors.New("剧集分析未运行")
	}
	cancel()
	return nil
}

// StopAndWait 在应用退出时用：取消本轮并等 worker 收尾，别让它在库关掉之后还在写。
func (s *CollectionSuggestionService) StopAndWait() {
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

func (s *CollectionSuggestionService) run(ctx context.Context, registry *BackgroundTaskRegistry) {
	defer s.worker.Done()
	defer registry.End(BackgroundTaskCollectionSuggest)
	err := s.analyzeOnce(ctx)
	cancelled := ctx.Err() != nil
	s.update(func(status *CollectionSuggestionStatus) {
		status.Running = false
		status.Cancelled = cancelled
		status.Completed = !cancelled && err == nil
		if err != nil && !cancelled {
			status.LastError = boundedError(err, 500)
		}
	})
	if err != nil && !cancelled {
		log.Printf("剧集候选分析失败 err=%v", err)
	}
}

func (s *CollectionSuggestionService) update(mutate func(*CollectionSuggestionStatus)) {
	s.mu.Lock()
	mutate(&s.status)
	now := s.now()
	s.status.UpdatedAt = &now
	status, emitter := s.status, s.emitter
	s.mu.Unlock()
	if emitter != nil {
		emitter(status)
	}
}

// suggestionCandidate 是一组待写库的候选。
type suggestionCandidate struct {
	scanRoot         string
	seriesName       string
	normalizedSeries string
	fingerprint      string
	members          []models.CollectionSuggestionMember
}

// analyzeOnce 跑完一轮分析：解析 → 分组 → 排除 → 与记忆比对 → upsert pending。
func (s *CollectionSuggestionService) analyzeOnce(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}
	roots, err := activeScanRoots(ctx)
	if err != nil {
		return err
	}
	groups := make(map[string]*suggestionCandidate)
	var cursor uint
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		var videos []models.Video
		if err := database.DB.WithContext(ctx).
			Select("id", "name", "path").
			Where("id > ?", cursor).
			Order("id ASC").
			Limit(collectionSuggestionPageSize).
			Find(&videos).Error; err != nil {
			return fmt.Errorf("读取视频用于剧集分析: %w", err)
		}
		if len(videos) == 0 {
			break
		}
		matched := 0
		for _, video := range videos {
			cursor = video.ID
			root, found := matchScanRoot(video.Path, roots)
			if !found {
				continue
			}
			parsed := ParseEpisodeFromFileName(fileNameStem(video.Name))
			if !parsed.OK {
				continue
			}
			matched++
			key := root + "\x00" + parsed.NormalizedSeries
			group, exists := groups[key]
			if !exists {
				group = &suggestionCandidate{scanRoot: root, seriesName: parsed.Series, normalizedSeries: parsed.NormalizedSeries}
				groups[key] = group
			}
			group.members = append(group.members, models.CollectionSuggestionMember{
				VideoID: video.ID, Season: parsed.Season, Episode: parsed.Episode,
			})
		}
		scanned := len(videos)
		s.update(func(status *CollectionSuggestionStatus) {
			status.Scanned += scanned
			status.Matched += matched
		})
		if len(videos) < collectionSuggestionPageSize {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return nil
	}

	collected, err := videoIDsInAnyCollection(ctx)
	if err != nil {
		return err
	}
	candidates := make([]*suggestionCandidate, 0, len(groups))
	for _, group := range groups {
		group.members = dropCollectedMembers(group.members, collected)
		if len(group.members) < 2 {
			continue
		}
		sortSuggestionMembers(group.members)
		assignSuggestionPositions(group.members)
		group.fingerprint = suggestionFingerprint(group.members)
		candidates = append(candidates, group)
	}
	// 写入顺序稳定：同一库跑两遍产生的 ID 顺序一致，测试与界面都不会漂。
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].scanRoot != candidates[j].scanRoot {
			return candidates[i].scanRoot < candidates[j].scanRoot
		}
		return candidates[i].normalizedSeries < candidates[j].normalizedSeries
	})
	if err := ctx.Err(); err != nil {
		return nil
	}
	pending, err := s.persistCandidates(ctx, candidates)
	if err != nil {
		return err
	}
	s.update(func(status *CollectionSuggestionStatus) { status.Pending = pending })
	return nil
}

// persistCandidates 把本轮候选落库，返回待审阅候选数。
//
// 记忆规则（D-025）：fingerprint 已经是 confirmed 或 dismissed 的，跳过不再提；
// 成员集合变了就是新指纹，于是重新出现。上一轮留下的 pending 只要不在本轮结果里
// 就作废——它的成员集合已经变了，留着只会让用户确认一个过期的组。
func (s *CollectionSuggestionService) persistCandidates(ctx context.Context, candidates []*suggestionCandidate) (int, error) {
	fresh := make(map[string]*suggestionCandidate, len(candidates))
	for _, candidate := range candidates {
		fresh[candidate.fingerprint] = candidate
	}
	pending := 0
	err := database.Transaction(func(tx *gorm.DB) error {
		tx = tx.WithContext(ctx)
		var existing []models.CollectionSuggestion
		if err := tx.Find(&existing).Error; err != nil {
			return fmt.Errorf("读取既有剧集候选: %w", err)
		}
		known := make(map[string]models.CollectionSuggestion, len(existing))
		staleIDs := make([]uint, 0)
		for _, row := range existing {
			known[row.Fingerprint] = row
			if row.Status != models.CollectionSuggestionStatusPending {
				continue
			}
			if _, still := fresh[row.Fingerprint]; !still {
				staleIDs = append(staleIDs, row.ID)
			}
		}
		if len(staleIDs) > 0 {
			// 成员行靠外键级联删；SQLite 与 Postgres 都开着约束，这里只删父行。
			if err := tx.Where("id IN ?", staleIDs).Delete(&models.CollectionSuggestion{}).Error; err != nil {
				return fmt.Errorf("清理过期剧集候选: %w", err)
			}
		}
		for _, candidate := range candidates {
			row, exists := known[candidate.fingerprint]
			if exists {
				if row.Status != models.CollectionSuggestionStatusPending {
					continue
				}
				pending++
				if row.ScanRoot == candidate.scanRoot && row.SeriesName == candidate.seriesName {
					continue
				}
				// 目录改名或文件改名后系列名会变，成员没变就还是同一条候选。
				if err := tx.Model(&models.CollectionSuggestion{}).Where("id = ?", row.ID).Updates(map[string]any{
					"scan_root":         candidate.scanRoot,
					"series_name":       candidate.seriesName,
					"normalized_series": candidate.normalizedSeries,
				}).Error; err != nil {
					return fmt.Errorf("更新剧集候选: %w", err)
				}
				continue
			}
			suggestion := models.CollectionSuggestion{
				ScanRoot:         candidate.scanRoot,
				SeriesName:       candidate.seriesName,
				NormalizedSeries: candidate.normalizedSeries,
				Status:           models.CollectionSuggestionStatusPending,
				Fingerprint:      candidate.fingerprint,
			}
			if err := tx.Create(&suggestion).Error; err != nil {
				return fmt.Errorf("写入剧集候选: %w", err)
			}
			members := make([]models.CollectionSuggestionMember, 0, len(candidate.members))
			for _, member := range candidate.members {
				member.SuggestionID = suggestion.ID
				members = append(members, member)
			}
			if err := tx.Create(&members).Error; err != nil {
				return fmt.Errorf("写入剧集候选成员: %w", err)
			}
			pending++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return pending, nil
}

// List 返回待审阅候选，成员按 position 排序并附上活跃视频的文件名与缩略图。
// 成员视频已被删除（软删除或彻底删除）的候选不再展示：它的成员集合已经变了。
func (s *CollectionSuggestionService) List() ([]CollectionSuggestionView, error) {
	var suggestions []models.CollectionSuggestion
	if err := database.DB.Where("status = ?", models.CollectionSuggestionStatusPending).
		Order("scan_root ASC").Order("normalized_series ASC").Order("id ASC").
		Find(&suggestions).Error; err != nil {
		return nil, fmt.Errorf("读取剧集候选: %w", err)
	}
	views := make([]CollectionSuggestionView, 0, len(suggestions))
	for _, suggestion := range suggestions {
		members, err := suggestionMemberViews(suggestion.ID)
		if err != nil {
			return nil, err
		}
		if len(members) < 2 {
			continue
		}
		views = append(views, CollectionSuggestionView{
			ID:          suggestion.ID,
			ScanRoot:    suggestion.ScanRoot,
			SeriesName:  suggestion.SeriesName,
			Status:      suggestion.Status,
			MemberCount: len(members),
			Members:     members,
			CreatedAt:   suggestion.CreatedAt,
		})
	}
	return views, nil
}

// Confirm 把候选变成真的作品集（D-024、7.3.2）。
//
// 复用 CollectionService 的三个入口：CreateCollection（同名活跃作品集存在时改为
// 加入该集）、AddCollectionVideos、ReorderCollectionVideos。绝不改任何视频的
// display_title / name / path。
//
// 这三个方法各自开自己的事务（SQLite 上还是 BEGIN IMMEDIATE），套一层外层事务
// 会在同一进程里自锁，所以这里按"每步自身原子 + 整体可重入"来做：任何一步失败
// 候选都留在 pending，用户重试时同名作品集已经存在，于是走"加入"分支，
// AddCollectionVideos 跳过已存在的成员、Reorder 重排一次，结果与一次成功等价。
func (s *CollectionSuggestionService) Confirm(suggestionID uint, name string, orderedVideoIDs []uint) (*CollectionDetail, error) {
	s.confirmMu.Lock()
	defer s.confirmMu.Unlock()

	suggestion, members, err := s.loadPendingSuggestion(suggestionID)
	if err != nil {
		return nil, err
	}
	ordered, err := validateSuggestionSelection(members, orderedVideoIDs)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		trimmed = suggestion.SeriesName
	}
	if strings.TrimSpace(trimmed) == "" {
		return nil, ErrCollectionSuggestionNameInvalid
	}

	collectionID, err := s.resolveConfirmTargetCollection(trimmed)
	if err != nil {
		return nil, err
	}
	if err := s.collections.AddCollectionVideos(collectionID, ordered); err != nil {
		return nil, err
	}
	if err := s.reorderConfirmedCollection(collectionID, ordered); err != nil {
		return nil, err
	}
	if err := database.DB.Model(&models.CollectionSuggestion{}).Where("id = ?", suggestion.ID).
		Update("status", models.CollectionSuggestionStatusConfirmed).Error; err != nil {
		return nil, fmt.Errorf("标记剧集候选已确认: %w", err)
	}
	return s.collections.GetCollectionDetail(collectionID)
}

// resolveConfirmTargetCollection 同名活跃作品集存在就用它，否则新建。
func (s *CollectionSuggestionService) resolveConfirmTargetCollection(name string) (uint, error) {
	// 用 Find 而不是 First：找不到是正常分支，不该在日志里留一条 record not found。
	var existing []models.MediaCollection
	if err := database.DB.Where("normalized_name = ?", strings.ToLower(name)).Limit(1).Find(&existing).Error; err != nil {
		return 0, fmt.Errorf("查找同名作品集: %w", err)
	}
	if len(existing) > 0 {
		return existing[0].ID, nil
	}
	created, err := s.collections.CreateCollection(name, "")
	if err != nil {
		if strings.Contains(err.Error(), "collection name") {
			return 0, fmt.Errorf("%w: %v", ErrCollectionSuggestionNameInvalid, err)
		}
		return 0, err
	}
	return created.ID, nil
}

// reorderConfirmedCollection 把确认的成员按集号排到作品集末尾。
// ReorderCollectionVideos 要求传入作品集里全部活跃视频，所以先读当前顺序，
// 再把本次确认的成员搬到尾部——加入已有作品集时不打乱人家原有的次序。
func (s *CollectionSuggestionService) reorderConfirmedCollection(collectionID uint, ordered []uint) error {
	detail, err := s.collections.GetCollectionDetail(collectionID)
	if err != nil {
		return err
	}
	confirmed := idSet(ordered)
	desired := make([]uint, 0, len(detail.Videos))
	for _, item := range detail.Videos {
		if _, isConfirmed := confirmed[item.Video.ID]; isConfirmed {
			continue
		}
		desired = append(desired, item.Video.ID)
	}
	desired = append(desired, ordered...)
	if len(desired) != len(detail.Videos) {
		// 确认的成员里有视频没能进作品集（例如刚被删除），顺序无从谈起。
		return ErrCollectionSuggestionMemberMismatch
	}
	return s.collections.ReorderCollectionVideos(collectionID, desired)
}

// Dismiss 忽略候选：成员一个不动，靠 fingerprint 记住"这一组不要再提"（D-025）。
func (s *CollectionSuggestionService) Dismiss(suggestionID uint) error {
	s.confirmMu.Lock()
	defer s.confirmMu.Unlock()
	if _, _, err := s.loadPendingSuggestion(suggestionID); err != nil {
		return err
	}
	if err := database.DB.Model(&models.CollectionSuggestion{}).Where("id = ?", suggestionID).
		Update("status", models.CollectionSuggestionStatusDismissed).Error; err != nil {
		return fmt.Errorf("忽略剧集候选: %w", err)
	}
	return nil
}

func (s *CollectionSuggestionService) loadPendingSuggestion(suggestionID uint) (models.CollectionSuggestion, []models.CollectionSuggestionMember, error) {
	var suggestion models.CollectionSuggestion
	if err := database.DB.First(&suggestion, suggestionID).Error; err != nil {
		return models.CollectionSuggestion{}, nil, err
	}
	if suggestion.Status != models.CollectionSuggestionStatusPending {
		return models.CollectionSuggestion{}, nil, ErrCollectionSuggestionNotPending
	}
	var members []models.CollectionSuggestionMember
	if err := database.DB.Where("suggestion_id = ?", suggestionID).
		Order("position ASC").Order("video_id ASC").Find(&members).Error; err != nil {
		return models.CollectionSuggestion{}, nil, fmt.Errorf("读取剧集候选成员: %w", err)
	}
	return suggestion, members, nil
}

// validateSuggestionSelection 校验 orderedVideoIDs ⊆ 成员，并保留调用方顺序。
// 传空视为"全选"，顺序取成员的集号顺序。
func validateSuggestionSelection(members []models.CollectionSuggestionMember, orderedVideoIDs []uint) ([]uint, error) {
	memberSet := make(map[uint]struct{}, len(members))
	for _, member := range members {
		memberSet[member.VideoID] = struct{}{}
	}
	if len(orderedVideoIDs) == 0 {
		ordered := make([]uint, 0, len(members))
		for _, member := range members {
			ordered = append(ordered, member.VideoID)
		}
		if len(ordered) < 2 {
			return nil, ErrCollectionSuggestionMemberMismatch
		}
		return ordered, nil
	}
	ordered := make([]uint, 0, len(orderedVideoIDs))
	seen := make(map[uint]struct{}, len(orderedVideoIDs))
	for _, videoID := range orderedVideoIDs {
		if _, isMember := memberSet[videoID]; !isMember {
			return nil, ErrCollectionSuggestionMemberMismatch
		}
		if _, duplicate := seen[videoID]; duplicate {
			return nil, ErrCollectionSuggestionMemberMismatch
		}
		seen[videoID] = struct{}{}
		ordered = append(ordered, videoID)
	}
	if len(ordered) < 2 {
		return nil, ErrCollectionSuggestionMemberMismatch
	}
	return ordered, nil
}

func suggestionMemberViews(suggestionID uint) ([]CollectionSuggestionMemberView, error) {
	var members []models.CollectionSuggestionMember
	if err := database.DB.Where("suggestion_id = ?", suggestionID).
		Order("position ASC").Order("video_id ASC").Find(&members).Error; err != nil {
		return nil, fmt.Errorf("读取剧集候选成员: %w", err)
	}
	videoIDs := make([]uint, 0, len(members))
	for _, member := range members {
		videoIDs = append(videoIDs, member.VideoID)
	}
	videoByID := make(map[uint]models.Video, len(videoIDs))
	if len(videoIDs) > 0 {
		var videos []models.Video
		if err := database.DB.Select("id", "name", "path").Where("id IN ?", videoIDs).Find(&videos).Error; err != nil {
			return nil, fmt.Errorf("读取剧集候选成员视频: %w", err)
		}
		for _, video := range videos {
			videoByID[video.ID] = video
		}
	}
	positionCounts := make(map[int]int, len(members))
	for _, member := range members {
		if _, active := videoByID[member.VideoID]; !active {
			continue
		}
		positionCounts[member.Position]++
	}
	views := make([]CollectionSuggestionMemberView, 0, len(members))
	for _, member := range members {
		video, active := videoByID[member.VideoID]
		if !active {
			continue
		}
		views = append(views, CollectionSuggestionMemberView{
			VideoID:          member.VideoID,
			Name:             video.Name,
			Path:             video.Path,
			Season:           member.Season,
			Episode:          member.Episode,
			Position:         member.Position,
			ThumbnailURL:     fmt.Sprintf("/preview/thumbnail/%d", member.VideoID),
			MultipleVersions: positionCounts[member.Position] > 1,
		})
	}
	return views, nil
}

func countActiveVideosForSuggestions(ctx context.Context) (int, error) {
	var total int64
	if err := database.DB.WithContext(ctx).Model(&models.Video{}).Count(&total).Error; err != nil {
		return 0, fmt.Errorf("统计视频用于剧集分析: %w", err)
	}
	return int(total), nil
}

// activeScanRoots 返回扫描根，按路径长度倒序：一个根嵌在另一个根里时取更深的那个。
func activeScanRoots(ctx context.Context) ([]string, error) {
	var directories []models.ScanDirectory
	if err := database.DB.WithContext(ctx).Select("id", "path").Find(&directories).Error; err != nil {
		return nil, fmt.Errorf("读取扫描目录: %w", err)
	}
	roots := make([]string, 0, len(directories))
	for _, directory := range directories {
		path := strings.TrimSpace(directory.Path)
		if path == "" {
			continue
		}
		roots = append(roots, filepath.Clean(path))
	}
	sort.Slice(roots, func(i, j int) bool {
		if len(roots[i]) != len(roots[j]) {
			return len(roots[i]) > len(roots[j])
		}
		return roots[i] < roots[j]
	})
	return roots, nil
}

// matchScanRoot 找视频所属的扫描根。不在任何根下面的视频不参与分组：
// 把它们凑成一个空扫描根等于跨根合并，而 D-023 明令跨根不合并。
func matchScanRoot(path string, roots []string) (string, bool) {
	if strings.TrimSpace(path) == "" {
		return "", false
	}
	for _, root := range roots {
		if pathIsEqualOrInside(path, root) {
			return root, true
		}
	}
	return "", false
}

func fileNameStem(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// videoIDsInAnyCollection 返回已经在任何作品集里的视频（D-025）。
// 作品集软删除时它的关系行已经被删掉，所以这里不用再筛作品集状态。
func videoIDsInAnyCollection(ctx context.Context) (map[uint]struct{}, error) {
	var videoIDs []uint
	if err := database.DB.WithContext(ctx).Model(&models.CollectionVideo{}).
		Distinct().Pluck("video_id", &videoIDs).Error; err != nil {
		return nil, fmt.Errorf("读取作品集成员: %w", err)
	}
	return idSet(videoIDs), nil
}

func dropCollectedMembers(members []models.CollectionSuggestionMember, collected map[uint]struct{}) []models.CollectionSuggestionMember {
	kept := make([]models.CollectionSuggestionMember, 0, len(members))
	for _, member := range members {
		if _, inCollection := collected[member.VideoID]; inCollection {
			continue
		}
		kept = append(kept, member)
	}
	return kept
}

// sortSuggestionMembers 按 (season, episode, video_id) 排序。没有季信息的按第 0 季
// 排在最前：同一系列里混着 `SxxExx` 与 `第 N 集` 两种命名时，顺序至少是稳定的。
func sortSuggestionMembers(members []models.CollectionSuggestionMember) {
	sort.Slice(members, func(i, j int) bool {
		left, right := members[i], members[j]
		leftSeason, rightSeason := intValueOrZero(left.Season), intValueOrZero(right.Season)
		if leftSeason != rightSeason {
			return leftSeason < rightSeason
		}
		leftEpisode, rightEpisode := intValueOrZero(left.Episode), intValueOrZero(right.Episode)
		if leftEpisode != rightEpisode {
			return leftEpisode < rightEpisode
		}
		return left.VideoID < right.VideoID
	})
}

// assignSuggestionPositions 给成员编号。同一 (season, episode) 共用一个 position：
// `03` 与 `03v2` 是同一集的两个版本，界面据此提示"同集多版本"（4.5.4）。
func assignSuggestionPositions(members []models.CollectionSuggestionMember) {
	position := 0
	previousSeason, previousEpisode := 0, 0
	for index := range members {
		season, episode := intValueOrZero(members[index].Season), intValueOrZero(members[index].Episode)
		if index == 0 || season != previousSeason || episode != previousEpisode {
			position++
			previousSeason, previousEpisode = season, episode
		}
		members[index].Position = position
	}
}

func intValueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

// suggestionFingerprint 是成员 ID 排序后的 sha256（D-024）。它既是唯一键，
// 也是"已确认/已忽略"的记忆键：成员集合变一个，指纹就变。
func suggestionFingerprint(members []models.CollectionSuggestionMember) string {
	ids := make([]uint, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.VideoID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatUint(uint64(id), 10))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, ",")))
	return hex.EncodeToString(sum[:])
}
