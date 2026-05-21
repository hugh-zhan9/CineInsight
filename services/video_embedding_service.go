package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"video-master/database"
	"video-master/models"

	"gorm.io/gorm/clause"
)

const (
	videoEmbeddingKindSemantic       = "semantic"
	defaultLocalMLEmbeddingBatchSize = 25
	maxLocalMLEmbeddingBatchSize     = 200
	localMLSemanticSearchMinScore    = 0.18
)

type LocalMLEmbeddingIndexError struct {
	VideoID uint   `json:"video_id"`
	Path    string `json:"path,omitempty"`
	Error   string `json:"error"`
}

type LocalMLEmbeddingIndexResult struct {
	Requested int                          `json:"requested"`
	Indexed   int                          `json:"indexed"`
	Skipped   int                          `json:"skipped"`
	Failed    int                          `json:"failed"`
	Errors    []LocalMLEmbeddingIndexError `json:"errors"`
}

type VideoEmbeddingService struct {
	configProvider     AITaggingConfigProvider
	localMLRuntime     LocalMLRuntime
	apiEmbeddingClient func(AITaggingConfig) TextEmbeddingClient
	extractor          *AITaggingExtractor
}

func NewVideoEmbeddingService(runtime LocalMLRuntime) *VideoEmbeddingService {
	if runtime == nil {
		runtime = NewInProcessLocalMLRuntime()
	}
	return &VideoEmbeddingService{
		configProvider:     SettingsAITaggingConfigProvider{},
		localMLRuntime:     runtime,
		apiEmbeddingClient: NewOpenAICompatibleEmbeddingClient,
		extractor:          NewAITaggingExtractor(),
	}
}

func (s *VideoEmbeddingService) IndexPending(ctx context.Context, limit int) (*LocalMLEmbeddingIndexResult, error) {
	result := &LocalMLEmbeddingIndexResult{}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	config, err := s.loadLocalConfig()
	if err != nil {
		return result, err
	}
	mode := normalizeAIBackendMode(string(config.Mode))
	if mode == AIBackendModeOff {
		return result, fmt.Errorf("AI 后端已关闭")
	}
	model, err := semanticEmbeddingModelForConfig(config)
	if err != nil {
		return result, err
	}
	limit = normalizeLocalMLEmbeddingLimit(limit)
	if mode == AIBackendModeLocal {
		runtime := s.runtime()
		if err := runtime.EnsureStarted(ctx, LocalMLRuntimeConfig{
			Model:  model,
			Device: normalizeLocalMLDevice(config.LocalMLDevice),
		}); err != nil {
			return result, err
		}
	}

	var videos []models.Video
	err = database.DB.
		Where("NOT EXISTS (SELECT 1 FROM video_embeddings WHERE video_embeddings.video_id = videos.id AND video_embeddings.model = ? AND video_embeddings.kind = ? AND video_embeddings.deleted_at IS NULL)", model, videoEmbeddingKindSemantic).
		Order("videos.id asc").
		Limit(limit).
		Find(&videos).Error
	if err != nil {
		return result, err
	}
	result.Requested = len(videos)
	for _, video := range videos {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if err := s.indexVideoWithConfig(ctx, video, config); err != nil {
			result.Failed++
			result.Errors = append(result.Errors, LocalMLEmbeddingIndexError{
				VideoID: video.ID,
				Path:    video.Path,
				Error:   err.Error(),
			})
			continue
		}
		result.Indexed++
	}
	return result, nil
}

func (s *VideoEmbeddingService) Search(ctx context.Context, rawQuery string, intent videoSearchIntent, tagIDs []uint, cursorScore float64, cursorID uint, limit int) ([]models.Video, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	rawQuery = strings.TrimSpace(rawQuery)
	if rawQuery == "" {
		return nil, false, nil
	}
	config, err := s.loadLocalConfig()
	if err != nil {
		return nil, false, nil
	}
	mode := normalizeAIBackendMode(string(config.Mode))
	if mode == AIBackendModeOff {
		return nil, false, nil
	}
	model, err := semanticEmbeddingModelForConfig(config)
	if err != nil {
		return nil, false, nil
	}
	records, err := loadVideoEmbeddingsForModel(model)
	if err != nil {
		return nil, false, err
	}
	if len(records) == 0 {
		return nil, false, nil
	}
	embedder, err := s.textEmbedder(ctx, config)
	if err != nil {
		return nil, false, err
	}
	queryEmbedding, err := embedder.EmbedText(ctx, rawQuery)
	if err != nil {
		return nil, false, err
	}
	if queryEmbedding == nil || len(queryEmbedding.Embedding) == 0 {
		return nil, false, fmt.Errorf("本地 ML 文本 embedding 为空")
	}

	resolvedTagIDs, err := tagIDsForIntent(tagIDs, intent)
	if err != nil {
		return nil, false, err
	}
	type scoredVideo struct {
		video models.Video
		score float64
	}
	scored := make([]scoredVideo, 0, len(records))
	for _, record := range records {
		video := record.Video
		if video.ID == 0 {
			continue
		}
		if !videoPassesSemanticFilters(video, intent, resolvedTagIDs) {
			continue
		}
		vector, err := decodeFloat32Vector(record.VectorJSON)
		if err != nil || len(vector) == 0 {
			continue
		}
		score := cosineSimilarity(queryEmbedding.Embedding, vector)
		if score < localMLSemanticSearchMinScore {
			continue
		}
		if cursorID > 0 && !semanticResultAfterCursor(score, video.ID, cursorScore, cursorID) {
			continue
		}
		video.SearchScore = score
		scored = append(scored, scoredVideo{video: video, score: score})
	}
	if len(scored) == 0 {
		return nil, false, nil
	}
	sort.Slice(scored, func(i, j int) bool {
		if math.Abs(scored[i].score-scored[j].score) < 0.000001 {
			return scored[i].video.ID > scored[j].video.ID
		}
		return scored[i].score > scored[j].score
	})
	limit = normalizeSearchLimit(limit)
	if len(scored) > limit {
		scored = scored[:limit]
	}
	videos := make([]models.Video, 0, len(scored))
	for _, item := range scored {
		videos = append(videos, item.video)
	}
	return videos, true, nil
}

func (s *VideoEmbeddingService) indexVideoWithConfig(ctx context.Context, video models.Video, config AITaggingConfig) error {
	extractor := s.extractor
	if extractor == nil {
		extractor = NewAITaggingExtractor()
	}
	model, err := semanticEmbeddingModelForConfig(config)
	if err != nil {
		return err
	}
	mode := normalizeAIBackendMode(string(config.Mode))
	if mode == AIBackendModeAPI {
		config.FrameCount = 0
	}
	evidence := extractor.Collect(ctx, video, config)
	embedding, err := s.videoEmbedding(ctx, config, video, evidence)
	if err != nil {
		return err
	}
	if embedding == nil || len(embedding.Embedding) == 0 {
		return fmt.Errorf("AI 视频 embedding 为空")
	}
	if embedding.Model == "" {
		embedding.Model = model
	}
	if embedding.Dimension <= 0 {
		embedding.Dimension = len(embedding.Embedding)
	}
	return upsertVideoEmbedding(video.ID, embedding, videoEmbeddingKindSemantic)
}

func (s *VideoEmbeddingService) loadLocalConfig() (AITaggingConfig, error) {
	provider := s.configProvider
	if provider == nil {
		provider = SettingsAITaggingConfigProvider{}
	}
	config, err := provider.Load()
	if err != nil {
		return config, err
	}
	config.Mode = normalizeAIBackendMode(string(config.Mode))
	config.LocalMLModel = localMLModelOrDefault(config.LocalMLModel)
	config.LocalMLDevice = normalizeLocalMLDevice(config.LocalMLDevice)
	return config, nil
}

func (s *VideoEmbeddingService) runtime() LocalMLRuntime {
	if s.localMLRuntime == nil {
		s.localMLRuntime = NewInProcessLocalMLRuntime()
	}
	return s.localMLRuntime
}

func (s *VideoEmbeddingService) textEmbedder(ctx context.Context, config AITaggingConfig) (TextEmbeddingClient, error) {
	switch normalizeAIBackendMode(string(config.Mode)) {
	case AIBackendModeLocal:
		model := localMLModelOrDefault(config.LocalMLModel)
		runtime := s.runtime()
		if err := runtime.EnsureStarted(ctx, LocalMLRuntimeConfig{
			Model:  model,
			Device: normalizeLocalMLDevice(config.LocalMLDevice),
		}); err != nil {
			return nil, err
		}
		return runtime, nil
	case AIBackendModeAPI:
		if strings.TrimSpace(config.EmbeddingModel) == "" {
			return nil, fmt.Errorf("AI embedding model unavailable")
		}
		factory := s.apiEmbeddingClient
		if factory == nil {
			factory = NewOpenAICompatibleEmbeddingClient
		}
		return factory(config), nil
	default:
		return nil, fmt.Errorf("AI 后端已关闭")
	}
}

func (s *VideoEmbeddingService) videoEmbedding(ctx context.Context, config AITaggingConfig, video models.Video, evidence AITaggingEvidence) (*LocalMLEmbeddingResult, error) {
	switch normalizeAIBackendMode(string(config.Mode)) {
	case AIBackendModeLocal:
		runtime := s.runtime()
		return runtime.EmbedVideo(ctx, LocalMLVideoEmbeddingRequest{
			VideoID:      video.ID,
			VideoName:    video.Name,
			VideoPath:    video.Path,
			SubtitleText: evidence.SubtitleText,
			Frames:       evidence.Frames,
		})
	case AIBackendModeAPI:
		embedder, err := s.textEmbedder(ctx, config)
		if err != nil {
			return nil, err
		}
		result, err := embedder.EmbedText(ctx, semanticVideoText(video, evidence))
		if err != nil {
			return nil, err
		}
		result.Model = strings.TrimSpace(config.EmbeddingModel)
		return result, nil
	default:
		return nil, fmt.Errorf("AI 后端已关闭")
	}
}

func semanticEmbeddingModelForConfig(config AITaggingConfig) (string, error) {
	switch normalizeAIBackendMode(string(config.Mode)) {
	case AIBackendModeLocal:
		return localMLModelOrDefault(config.LocalMLModel), nil
	case AIBackendModeAPI:
		model := strings.TrimSpace(config.EmbeddingModel)
		if model == "" {
			return "", fmt.Errorf("AI embedding model unavailable")
		}
		return model, nil
	default:
		return "", fmt.Errorf("AI 后端已关闭")
	}
}

func semanticVideoText(video models.Video, evidence AITaggingEvidence) string {
	parts := []string{
		video.Name,
		video.Path,
		video.Directory,
		evidence.SubtitleText,
	}
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			filtered = append(filtered, strings.TrimSpace(part))
		}
	}
	return strings.Join(filtered, "\n")
}

func upsertVideoEmbedding(videoID uint, embedding *LocalMLEmbeddingResult, kind string) error {
	model := localMLModelOrDefault(embedding.Model)
	vectorJSON, err := json.Marshal(embedding.Embedding)
	if err != nil {
		return err
	}
	record := models.VideoEmbedding{
		VideoID:    videoID,
		Model:      model,
		Kind:       kind,
		VectorJSON: string(vectorJSON),
		Dimension:  embedding.Dimension,
		Source:     embedding.Source,
	}
	if record.Dimension <= 0 {
		record.Dimension = len(embedding.Embedding)
	}
	return database.DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "video_id"},
			{Name: "model"},
			{Name: "kind"},
		},
		DoUpdates: clause.AssignmentColumns([]string{"vector_json", "dimension", "source", "updated_at"}),
	}).Create(&record).Error
}

func loadVideoEmbeddingsForModel(model string) ([]models.VideoEmbedding, error) {
	var records []models.VideoEmbedding
	err := database.DB.
		Preload("Video.Tags").
		Where("model = ? AND kind = ?", localMLModelOrDefault(model), videoEmbeddingKindSemantic).
		Find(&records).Error
	return records, err
}

func decodeFloat32Vector(raw string) ([]float32, error) {
	var vector []float32
	if err := json.Unmarshal([]byte(raw), &vector); err != nil {
		return nil, err
	}
	return vector, nil
}

func normalizeLocalMLEmbeddingLimit(limit int) int {
	if limit <= 0 {
		return defaultLocalMLEmbeddingBatchSize
	}
	if limit > maxLocalMLEmbeddingBatchSize {
		return maxLocalMLEmbeddingBatchSize
	}
	return limit
}

func normalizeSearchLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func semanticResultAfterCursor(score float64, id uint, cursorScore float64, cursorID uint) bool {
	if cursorID == 0 {
		return true
	}
	if cursorScore <= 0 {
		return id < cursorID
	}
	if score < cursorScore-0.000001 {
		return true
	}
	return math.Abs(score-cursorScore) < 0.000001 && id < cursorID
}

func tagIDsForIntent(tagIDs []uint, intent videoSearchIntent) ([]uint, error) {
	allTagIDs := append([]uint(nil), tagIDs...)
	if len(intent.TagNames) > 0 {
		resolvedTagIDs, err := resolveVideoSearchTagIDs(intent.TagNames)
		if err != nil {
			return nil, err
		}
		allTagIDs = append(allTagIDs, resolvedTagIDs...)
	}
	return uniqueUintIDs(allTagIDs), nil
}

func videoPassesSemanticFilters(video models.Video, intent videoSearchIntent, tagIDs []uint) bool {
	if intent.MinSize > 0 && video.Size < intent.MinSize {
		return false
	}
	if intent.MaxSize > 0 && video.Size >= intent.MaxSize {
		return false
	}
	if intent.MinHeight > 0 && video.Height < intent.MinHeight {
		return false
	}
	if intent.MaxHeight > 0 && video.Height > intent.MaxHeight {
		return false
	}
	if intent.PortraitOnly && video.Height <= video.Width {
		return false
	}
	if intent.LandscapeOnly && video.Width < video.Height {
		return false
	}
	if len(tagIDs) > 0 && !videoHasAllTags(video, tagIDs) {
		return false
	}
	if intent.RequireFaces && !videoHasDetectedFace(video.ID) {
		return false
	}
	if strings.TrimSpace(intent.SubtitleKeyword) != "" && !videoSubtitleContains(video.ID, intent.SubtitleKeyword) {
		return false
	}
	return true
}

func videoHasAllTags(video models.Video, tagIDs []uint) bool {
	if len(tagIDs) == 0 {
		return true
	}
	seen := make(map[uint]struct{}, len(video.Tags))
	for _, tag := range video.Tags {
		seen[tag.ID] = struct{}{}
	}
	for _, id := range tagIDs {
		if _, ok := seen[id]; !ok {
			return false
		}
	}
	return true
}

func videoHasDetectedFace(videoID uint) bool {
	var count int64
	if err := database.DB.Model(&models.VideoFace{}).
		Where("video_id = ? AND status = ?", videoID, models.VideoFaceStatusDetected).
		Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

func videoSubtitleContains(videoID uint, keyword string) bool {
	pattern := "%" + strings.ToLower(escapeSQLLike(strings.TrimSpace(keyword))) + "%"
	var count int64
	if err := database.DB.Model(&models.SubtitleSegment{}).
		Where("video_id = ? AND LOWER(text) LIKE ? ESCAPE '\\'", videoID, pattern).
		Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}
