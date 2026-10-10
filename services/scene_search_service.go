package services

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"

	"gorm.io/gorm"
)

// 场景检索查询（场景检索合同「查询」）。对白复用 subtitle_segments 的字面子串匹配；
// 画面（本地）用 worker 把查询转成向量，按段 ID 分批流式读取范围内当前模型的段，
// Go 侧 int8 点积维护 top-limit；画面（外部描述）对 caption 做字面匹配。
// all 模式两路各取 limit 条，按倒数排名融合（k=60）排序，每条保留来源。
// 查询词、字幕文本与描述一律不写日志。
const (
	SceneModeAll      = "all"
	SceneModeDialogue = "dialogue"
	SceneModeVisual   = "visual"

	SceneSourceDialogue = "dialogue"
	SceneSourceVisual   = "visual"

	// 查询结果附带的提示：说明哪一部分没执行或没有数据，不把未索引说成没有命中。
	SceneNoticeRuntimeUnavailable    = "scene_runtime_unavailable"
	SceneNoticeVisualIndexEmpty      = "visual_index_empty"
	SceneNoticeVisualFailed          = "scene_visual_failed"
	SceneNoticeExternalNotConfigured = "scene_external_not_configured"
	SceneNoticeSubtitleIndexEmpty    = "subtitle_index_empty"

	sceneQueryMaxRunes   = 200
	sceneDefaultLimit    = 50
	sceneMaxLimit        = 200
	sceneScanBatchSize   = 2000
	sceneRRFConstant     = 60
	sceneDialogueContext = 1
)

var (
	// ErrSceneQueryInvalid：查询词为空或超过 200 字符。
	ErrSceneQueryInvalid = errors.New("scene_query_invalid")
	// ErrSceneModeInvalid：mode 不是 all / dialogue / visual。
	ErrSceneModeInvalid = errors.New("scene_mode_invalid")
)

// SceneSearchRequest 是 SearchScenes 的参数。filter 为空表示全库（复用片库筛选的视频范围，
// 忽略排序与折叠版本）。
type SceneSearchRequest struct {
	Query  string         `json:"query"`
	Mode   string         `json:"mode"`
	Filter *LibraryFilter `json:"filter"`
	Limit  int            `json:"limit"`
}

// SceneHit 是一条命中：时间区间 [start_ms, end_ms)，来源 dialogue 或 visual。
type SceneHit struct {
	VideoID       uint    `json:"video_id"`
	Title         string  `json:"title"`
	StartMS       int64   `json:"start_ms"`
	EndMS         int64   `json:"end_ms"`
	Source        string  `json:"source"`
	Score         float64 `json:"score"`
	Text          string  `json:"text"`
	ContextBefore string  `json:"context_before"`
	ContextAfter  string  `json:"context_after"`
}

// SceneCoverage 描述检索范围内的索引覆盖。
type SceneCoverage struct {
	TotalVideos int64 `json:"total_videos"`
	// SubtitleIndexed：有 .srt 逐条索引的视频数。
	SubtitleIndexed int64 `json:"subtitle_indexed"`
	// SubtitleUnindexed：只有其他格式旁挂字幕或内嵌字幕、没有逐条索引的视频数。
	SubtitleUnindexed int64 `json:"subtitle_unindexed"`
	// VisualIndexed：当前模型画面已索引且源未变化的视频数。
	VisualIndexed  int64  `json:"visual_indexed"`
	VisualModelID  string `json:"visual_model_id"`
	VisualProvider string `json:"visual_provider"`
}

// SceneSearchResult 是 SearchScenes 的返回。
type SceneSearchResult struct {
	Hits     []SceneHit    `json:"hits"`
	Coverage SceneCoverage `json:"coverage"`
	Notices  []string      `json:"notices"`
}

// SceneSearchService 执行场景检索；与建索引共用运行时可用性判定与常驻 worker。
// 它不持有外部描述客户端：检索只对已存的描述做字面匹配，从不调用外部接口。
type SceneSearchService struct {
	vectors          sceneVectorSource
	runtimeAvailable func() bool
	syncSubtitles    func()
}

// NewSceneSearchService 与建索引服务共用运行时、worker 与外部配置读取。
func NewSceneSearchService(index *SceneIndexService) *SceneSearchService {
	return &SceneSearchService{
		vectors:          index.vectors,
		runtimeAvailable: index.runtimeAvailable,
		syncSubtitles:    func() { _ = syncSubtitleIndexesFromFilesystem() },
	}
}

func normalizeSceneSearchRequest(request SceneSearchRequest) (SceneSearchRequest, error) {
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" || utf8.RuneCountInString(request.Query) > sceneQueryMaxRunes {
		return request, ErrSceneQueryInvalid
	}
	request.Mode = strings.TrimSpace(request.Mode)
	switch request.Mode {
	case "":
		request.Mode = SceneModeAll
	case SceneModeAll, SceneModeDialogue, SceneModeVisual:
	default:
		return request, ErrSceneModeInvalid
	}
	switch {
	case request.Limit <= 0:
		request.Limit = sceneDefaultLimit
	case request.Limit > sceneMaxLimit:
		request.Limit = sceneMaxLimit
	}
	return request, nil
}

// visualModel 返回当前提供方、模型标识与采样间隔；外部未配置时 modelID 为空。
// 间隔也是有效性的一部分：改了间隔之后旧段不再参与检索与覆盖率（M-2）。
func (s *SceneSearchService) visualModel(ctx context.Context) (provider, modelID string, intervalMS int64, err error) {
	settings, err := loadSceneSettings(ctx)
	if err != nil {
		return "", "", 0, err
	}
	return settings.Provider, settings.currentModelID(), settings.IntervalMS, nil
}

// Search 执行一次场景检索。
func (s *SceneSearchService) Search(ctx context.Context, request SceneSearchRequest) (SceneSearchResult, error) {
	request, err := normalizeSceneSearchRequest(request)
	if err != nil {
		return SceneSearchResult{}, err
	}
	provider, modelID, intervalMS, err := s.visualModel(ctx)
	if err != nil {
		return SceneSearchResult{}, err
	}
	coverage, err := sceneCoverage(ctx, request.Filter, provider, modelID, intervalMS)
	if err != nil {
		return SceneSearchResult{}, err
	}
	result := SceneSearchResult{Hits: []SceneHit{}, Coverage: coverage, Notices: []string{}}
	var dialogue, visual []SceneHit
	if request.Mode != SceneModeVisual {
		if s.syncSubtitles != nil {
			s.syncSubtitles()
		}
		if dialogue, err = searchSceneDialogue(ctx, request.Query, request.Filter, request.Limit); err != nil {
			return SceneSearchResult{}, err
		}
		if coverage.SubtitleIndexed == 0 {
			result.Notices = append(result.Notices, SceneNoticeSubtitleIndexEmpty)
		}
	}
	if request.Mode != SceneModeDialogue {
		var notice string
		visual, notice, err = s.searchVisual(ctx, request, provider, modelID, intervalMS, coverage)
		if err != nil {
			return SceneSearchResult{}, err
		}
		if notice != "" {
			result.Notices = append(result.Notices, notice)
		}
	}
	switch request.Mode {
	case SceneModeDialogue:
		result.Hits = dialogue
	case SceneModeVisual:
		result.Hits = visual
	default:
		result.Hits = fuseSceneHits(dialogue, visual, request.Limit)
	}
	if err := fillSceneHitTitles(ctx, result.Hits); err != nil {
		return SceneSearchResult{}, err
	}
	return result, nil
}

// sceneVisualValidSQL 是"一个视频的画面索引此刻有效"的唯一判定式：状态为当前模型、
// 已索引、源 size 与 videos.size 一致（视频活跃非失效由外层范围保证）。
const sceneVisualValidSQL = `EXISTS (SELECT 1 FROM scene_index_states st WHERE st.video_id = videos.id
	AND st.model_id = ? AND st.status = ? AND st.source_size = videos.size AND st.interval_ms = ?)`

const sceneSubtitleIndexedSQL = `EXISTS (SELECT 1 FROM subtitle_index_states sis WHERE sis.video_id = videos.id AND sis.segment_count > 0)`

// sceneCoverage 一次查出范围内视频总数、有字幕逐条索引数、只有其他格式或内嵌字幕而未索引数、
// 当前模型画面已索引数。
func sceneCoverage(ctx context.Context, filter *LibraryFilter, provider, modelID string, intervalMS int64) (SceneCoverage, error) {
	scope, err := sceneScopeQuery(ctx, filter)
	if err != nil {
		return SceneCoverage{}, err
	}
	var row struct {
		Total             int64
		SubtitleIndexed   int64
		SubtitleUnindexed int64
		VisualIndexed     int64
	}
	selectSQL := `COUNT(*) AS total,
		COALESCE(SUM(CASE WHEN ` + sceneSubtitleIndexedSQL + ` THEN 1 ELSE 0 END), 0) AS subtitle_indexed,
		COALESCE(SUM(CASE WHEN NOT ` + sceneSubtitleIndexedSQL + ` AND (EXISTS (SELECT 1 FROM subtitle_index_states sis2
			WHERE sis2.video_id = videos.id AND sis2.has_sidecar) OR ` + hasEmbeddedSubtitleSQL + `) THEN 1 ELSE 0 END), 0) AS subtitle_unindexed,
		COALESCE(SUM(CASE WHEN ` + sceneVisualValidSQL + ` THEN 1 ELSE 0 END), 0) AS visual_indexed`
	if err := scope.Select(selectSQL, modelID, models.SceneIndexStatusIndexed, intervalMS).Scan(&row).Error; err != nil {
		return SceneCoverage{}, err
	}
	return SceneCoverage{
		TotalVideos: row.Total, SubtitleIndexed: row.SubtitleIndexed, SubtitleUnindexed: row.SubtitleUnindexed,
		VisualIndexed: row.VisualIndexed, VisualModelID: modelID, VisualProvider: provider,
	}, nil
}

// GetCoverage 只读覆盖率（场景检索页打开时与筛选变化时用）。
func (s *SceneSearchService) GetCoverage(ctx context.Context, filter *LibraryFilter) (SceneCoverage, error) {
	provider, modelID, intervalMS, err := s.visualModel(ctx)
	if err != nil {
		return SceneCoverage{}, err
	}
	return sceneCoverage(ctx, filter, provider, modelID, intervalMS)
}

type sceneDialogueRow struct {
	VideoID      uint
	SegmentIndex int
	StartTimeMs  int64
	EndTimeMs    int64
	Text         string
}

func querySceneDialogue(ctx context.Context, pattern string, filter *LibraryFilter, limit int) ([]sceneDialogueRow, error) {
	scope, err := sceneScopeQuery(ctx, filter)
	if err != nil {
		return nil, err
	}
	var rows []sceneDialogueRow
	err = database.DB.WithContext(ctx).Table("subtitle_segments").
		Select("subtitle_segments.video_id, subtitle_segments.segment_index, subtitle_segments.start_time_ms, subtitle_segments.end_time_ms, subtitle_segments.text").
		Joins("JOIN videos ON videos.id = subtitle_segments.video_id AND videos.deleted_at IS NULL").
		Where(`LOWER(subtitle_segments.text) LIKE ? ESCAPE '\'`, pattern).
		Where("subtitle_segments.video_id IN (?)", scope.Select("videos.id")).
		Order("subtitle_segments.video_id DESC, subtitle_segments.start_time_ms ASC, subtitle_segments.segment_index ASC").
		Limit(limit).Scan(&rows).Error
	return rows, err
}

// searchSceneDialogue 返回全片范围内的对白命中（每条命中一条字幕的起止时间与前后各一条上下文），
// 按视频再按时间排序。命中视频的 .srt 改过时就地重建这几个视频的索引再查一次（同字幕搜索）。
func searchSceneDialogue(ctx context.Context, query string, filter *LibraryFilter, limit int) ([]SceneHit, error) {
	pattern := "%" + strings.ToLower(escapeSQLLike(query)) + "%"
	rows, err := querySceneDialogue(ctx, pattern, filter, limit)
	if err != nil {
		return nil, err
	}
	if refreshed, err := refreshStaleSubtitleIndexes(ctx, rows); err != nil {
		return nil, err
	} else if refreshed {
		if rows, err = querySceneDialogue(ctx, pattern, filter, limit); err != nil {
			return nil, err
		}
	}
	contexts, err := loadSceneDialogueContext(ctx, rows)
	if err != nil {
		return nil, err
	}
	hits := make([]SceneHit, 0, len(rows))
	for _, row := range rows {
		around := contexts[row.VideoID]
		hits = append(hits, SceneHit{
			VideoID: row.VideoID, StartMS: row.StartTimeMs, EndMS: row.EndTimeMs, Source: SceneSourceDialogue,
			Score: 1, Text: row.Text,
			ContextBefore: around[row.SegmentIndex-sceneDialogueContext],
			ContextAfter:  around[row.SegmentIndex+sceneDialogueContext],
		})
	}
	return hits, nil
}

func refreshStaleSubtitleIndexes(ctx context.Context, rows []sceneDialogueRow) (bool, error) {
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.VideoID)
	}
	ids = uniqueUintIDs(ids)
	if len(ids) == 0 {
		return false, nil
	}
	var videos []models.Video
	if err := database.DB.WithContext(ctx).Select("id", "path").Where("id IN ?", ids).Find(&videos).Error; err != nil {
		return false, err
	}
	refreshed := false
	for _, video := range videos {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		current, err := isSubtitleIndexCurrent(video, subtitleparser.SRTPathForVideo(video.Path))
		if err == nil && current {
			continue
		}
		refreshed = true
		_ = ensureSubtitleIndexForVideo(video)
	}
	return refreshed, nil
}

// loadSceneDialogueContext 按视频一次取出命中条目前后各一条的文本：video_id → segment_index → text。
func loadSceneDialogueContext(ctx context.Context, rows []sceneDialogueRow) (map[uint]map[int]string, error) {
	wanted := map[uint][]int{}
	for _, row := range rows {
		wanted[row.VideoID] = append(wanted[row.VideoID], row.SegmentIndex-sceneDialogueContext, row.SegmentIndex+sceneDialogueContext)
	}
	out := make(map[uint]map[int]string, len(wanted))
	for videoID, indexes := range wanted {
		var segments []models.SubtitleSegment
		if err := database.DB.WithContext(ctx).Select("segment_index", "text").
			Where("video_id = ? AND segment_index IN ?", videoID, indexes).Find(&segments).Error; err != nil {
			return nil, err
		}
		texts := make(map[int]string, len(segments))
		for _, segment := range segments {
			texts[segment.SegmentIndex] = segment.Text
		}
		out[videoID] = texts
	}
	return out, nil
}

// searchVisual 执行画面部分。本地运行时不可用或推理出错时只给提示、不改用任何其他提供方（TC-18）。
func (s *SceneSearchService) searchVisual(ctx context.Context, request SceneSearchRequest, provider, modelID string, intervalMS int64, coverage SceneCoverage) ([]SceneHit, string, error) {
	if provider == SceneProviderExternal {
		if modelID == "" {
			return []SceneHit{}, SceneNoticeExternalNotConfigured, nil
		}
		if coverage.VisualIndexed == 0 {
			return []SceneHit{}, SceneNoticeVisualIndexEmpty, nil
		}
		hits, err := searchSceneCaptions(ctx, request.Query, request.Filter, modelID, intervalMS, request.Limit)
		return hits, "", err
	}
	if s.vectors == nil || s.runtimeAvailable == nil || !s.runtimeAvailable() {
		return []SceneHit{}, SceneNoticeRuntimeUnavailable, nil
	}
	if coverage.VisualIndexed == 0 {
		return []SceneHit{}, SceneNoticeVisualIndexEmpty, nil
	}
	vectors, err := s.vectors.EmbedTexts(ctx, []string{request.Query})
	if err != nil || len(vectors) != 1 {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return []SceneHit{}, SceneNoticeVisualFailed, nil
	}
	candidates, err := scanSceneVisualSegments(ctx, request.Filter, modelID, intervalMS, vectors[0], request.Limit)
	if err != nil {
		return nil, "", err
	}
	merged := mergeAdjacentSceneCandidates(candidates)
	hits := make([]SceneHit, 0, len(merged))
	for _, candidate := range merged {
		hits = append(hits, SceneHit{
			VideoID: candidate.VideoID, StartMS: candidate.StartMS, EndMS: candidate.EndMS,
			Source: SceneSourceVisual, Score: float64(candidate.Score),
		})
	}
	return hits, "", nil
}

type sceneSegmentScanRow struct {
	ID         uint   `gorm:"column:id"`
	VideoID    uint   `gorm:"column:video_id"`
	StartMS    int64  `gorm:"column:start_ms"`
	EndMS      int64  `gorm:"column:end_ms"`
	Embedding  []byte `gorm:"column:embedding"`
	Caption    string `gorm:"column:caption"`
	IntervalMS int64  `gorm:"column:interval_ms"`
}

// validSceneSegments 是"当前模型、已索引、源未变化、视频活跃非失效"的段查询。
func validSceneSegments(ctx context.Context, modelID string, intervalMS int64, columns string) *gorm.DB {
	return database.DB.WithContext(ctx).Table("scene_visual_segments AS seg").
		Select(columns).
		Joins("JOIN scene_index_states st ON st.video_id = seg.video_id AND st.model_id = seg.model_id").
		Joins("JOIN videos ON videos.id = seg.video_id AND videos.deleted_at IS NULL").
		Where("seg.model_id = ? AND st.status = ? AND st.source_size = videos.size AND st.interval_ms = ? AND videos.is_stale = ?",
			modelID, models.SceneIndexStatusIndexed, intervalMS, false)
}

// scanSceneVisualSegments 按段 ID 分批（每批 2,000 行）流式读取并打分，只保留 top-limit。
func scanSceneVisualSegments(ctx context.Context, filter *LibraryFilter, modelID string, intervalMS int64, query []float32, limit int) ([]sceneVisualCandidate, error) {
	scope, err := sceneScopeQuery(ctx, filter)
	if err != nil {
		return nil, err
	}
	var scopeIDs []uint
	if err := scope.Pluck("videos.id", &scopeIDs).Error; err != nil {
		return nil, err
	}
	inScope := make(map[uint]struct{}, len(scopeIDs))
	for _, id := range scopeIDs {
		inScope[id] = struct{}{}
	}
	top := newSceneTopK(limit)
	var lastID uint
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var batch []sceneSegmentScanRow
		if err := validSceneSegments(ctx, modelID, intervalMS, "seg.id, seg.video_id, seg.start_ms, seg.end_ms, seg.embedding, st.interval_ms").
			Where("seg.id > ?", lastID).Order("seg.id ASC").Limit(sceneScanBatchSize).Scan(&batch).Error; err != nil {
			return nil, err
		}
		for _, row := range batch {
			lastID = row.ID
			if _, ok := inScope[row.VideoID]; !ok {
				continue
			}
			score, ok := sceneQuantizedDot(query, row.Embedding)
			if !ok {
				continue
			}
			top.offer(sceneVisualCandidate{SegmentID: row.ID, VideoID: row.VideoID, StartMS: row.StartMS, EndMS: row.EndMS, IntervalMS: row.IntervalMS, Score: score})
		}
		if len(batch) < sceneScanBatchSize {
			break
		}
	}
	return top.sorted(), nil
}

// searchSceneCaptions 对外部描述做与对白相同的字面子串匹配，按视频再按时间排序。
func searchSceneCaptions(ctx context.Context, query string, filter *LibraryFilter, modelID string, intervalMS int64, limit int) ([]SceneHit, error) {
	scope, err := sceneScopeQuery(ctx, filter)
	if err != nil {
		return nil, err
	}
	pattern := "%" + strings.ToLower(escapeSQLLike(query)) + "%"
	var rows []sceneSegmentScanRow
	if err := validSceneSegments(ctx, modelID, intervalMS, "seg.id, seg.video_id, seg.start_ms, seg.end_ms, seg.caption, st.interval_ms").
		Where(`LOWER(seg.caption) LIKE ? ESCAPE '\'`, pattern).
		Where("seg.video_id IN (?)", scope.Select("videos.id")).
		Order("seg.video_id DESC, seg.start_ms ASC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	hits := make([]SceneHit, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, SceneHit{VideoID: row.VideoID, StartMS: row.StartMS, EndMS: row.EndMS, Source: SceneSourceVisual, Score: 1, Text: row.Caption})
	}
	return hits, nil
}

// fuseSceneHits 按倒数排名融合（k=60）合并两路结果：每条只按它在自己那一路里的名次得分，
// 不把两种分数直接相加；同分时名次靠前者在前，再同则对白在前。结果截到 limit。
func fuseSceneHits(dialogue, visual []SceneHit, limit int) []SceneHit {
	type ranked struct {
		hit    SceneHit
		rank   int
		source int
	}
	all := make([]ranked, 0, len(dialogue)+len(visual))
	for index, hit := range dialogue {
		hit.Score = 1 / float64(sceneRRFConstant+index+1)
		all = append(all, ranked{hit: hit, rank: index, source: 0})
	}
	for index, hit := range visual {
		hit.Score = 1 / float64(sceneRRFConstant+index+1)
		all = append(all, ranked{hit: hit, rank: index, source: 1})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].hit.Score != all[j].hit.Score {
			return all[i].hit.Score > all[j].hit.Score
		}
		if all[i].rank != all[j].rank {
			return all[i].rank < all[j].rank
		}
		return all[i].source < all[j].source
	})
	if len(all) > limit {
		all = all[:limit]
	}
	hits := make([]SceneHit, 0, len(all))
	for _, item := range all {
		hits = append(hits, item.hit)
	}
	return hits
}

// fillSceneHitTitles 给命中补上视频标题（显示标题优先，没有时用文件名；不返回路径）。
func fillSceneHitTitles(ctx context.Context, hits []SceneHit) error {
	ids := make([]uint, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.VideoID)
	}
	ids = uniqueUintIDs(ids)
	if len(ids) == 0 {
		return nil
	}
	var videos []models.Video
	if err := database.DB.WithContext(ctx).Select("id", "name", "display_title").Where("id IN ?", ids).Find(&videos).Error; err != nil {
		return err
	}
	titles := make(map[uint]string, len(videos))
	for _, video := range videos {
		title := strings.TrimSpace(video.DisplayTitle)
		if title == "" {
			title = video.Name
		}
		titles[video.ID] = title
	}
	for index := range hits {
		hits[index].Title = titles[hits[index].VideoID]
	}
	return nil
}
