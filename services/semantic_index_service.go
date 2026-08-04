package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultSemanticIndexTextRunes = 8192
	maxSemanticIndexTextRunes     = 16384
	semanticSubtitleRunes         = 4000
	semanticAISummaryRunes        = 2000
	semanticEmbeddingResponseMax  = 8 << 20
	semanticStateWriteTimeout     = 2 * time.Second
)

var (
	ErrSemanticIndexUnavailable       = errors.New("semantic_index_unavailable")
	ErrSemanticIndexRebuildRequired   = errors.New("semantic_index_rebuild_required")
	ErrSemanticIndexModelMismatch     = errors.New("semantic_index_model_mismatch")
	ErrSemanticIndexDimensionMismatch = errors.New("semantic_index_dimension_mismatch")
	semanticAbsolutePathPattern       = regexp.MustCompile(`(?i)(?:[a-z]:[\\/][^\s,;"']+|\\\\[^\\/\s]+(?:\\[^\\/\s,;"']+)+|/(?:[^/\s]+/)+[^\s,;"']+)`)
)

// SemanticIndexConfig reuses the configured OpenAI-compatible endpoint without persisting its secret.
type SemanticIndexConfig struct {
	BaseURL      string
	APIKey       string
	Model        string
	MaxTextRunes int
}

// SemanticIndexConfigProvider loads the endpoint configuration for one explicit run.
type SemanticIndexConfigProvider interface {
	Load() (SemanticIndexConfig, error)
}

// SemanticIndexConfigProviderFunc adapts a function into a config provider.
type SemanticIndexConfigProviderFunc func() (SemanticIndexConfig, error)

func (f SemanticIndexConfigProviderFunc) Load() (SemanticIndexConfig, error) { return f() }

// SemanticIndexConfigFromAITagging reuses the existing AI endpoint, key and model.
func SemanticIndexConfigFromAITagging(config AITaggingConfig) SemanticIndexConfig {
	return SemanticIndexConfig{BaseURL: config.BaseURL, APIKey: config.APIKey, Model: config.Model, MaxTextRunes: defaultSemanticIndexTextRunes}
}

// SemanticEmbeddingClient produces one embedding without exposing transport details to the worker.
type SemanticEmbeddingClient interface {
	Embed(context.Context, string) ([]float64, error)
}

type OpenAICompatibleSemanticEmbeddingClient struct {
	config SemanticIndexConfig
	client *http.Client
}

// NewOpenAICompatibleSemanticEmbeddingClient creates an /embeddings client.
func NewOpenAICompatibleSemanticEmbeddingClient(config SemanticIndexConfig) SemanticEmbeddingClient {
	return &OpenAICompatibleSemanticEmbeddingClient{config: config, client: &http.Client{Timeout: 2 * time.Minute}}
}

func (c *OpenAICompatibleSemanticEmbeddingClient) Embed(ctx context.Context, text string) ([]float64, error) {
	payload, err := json.Marshal(map[string]any{"model": c.config.Model, "input": text})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIEmbeddingsURL(c.config.BaseURL), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(c.config.APIKey); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, semanticEmbeddingResponseMax+1))
	if err != nil {
		return nil, err
	}
	if len(body) > semanticEmbeddingResponseMax {
		return nil, fmt.Errorf("embedding response exceeds %d bytes", semanticEmbeddingResponseMax)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding API returned %d", response.StatusCode)
	}
	var decoded struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(decoded.Data) != 1 || len(decoded.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embedding API returned no vector")
	}
	if err := validateSemanticEmbedding(decoded.Data[0].Embedding); err != nil {
		return nil, err
	}
	return decoded.Data[0].Embedding, nil
}

func openAIEmbeddingsURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(base, "/embeddings") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/embeddings"
	}
	return base + "/v1/embeddings"
}

type SemanticIndexBuildRequest struct {
	Rebuild bool `json:"rebuild"`
}

type SemanticIndexFailure struct {
	VideoID uint   `json:"video_id"`
	Name    string `json:"name"`
	Code    string `json:"code"`
	Error   string `json:"error"`
}

type SemanticIndexStatus struct {
	Available      bool                   `json:"available"`
	Running        bool                   `json:"running"`
	Cancelled      bool                   `json:"cancelled"`
	Completed      bool                   `json:"completed"`
	NeedsRebuild   bool                   `json:"needs_rebuild"`
	Model          string                 `json:"model"`
	Dimension      int                    `json:"dimension"`
	Generation     int                    `json:"generation"`
	Total          int                    `json:"total"`
	Processed      int                    `json:"processed"`
	Succeeded      int                    `json:"succeeded"`
	Skipped        int                    `json:"skipped"`
	Failed         int                    `json:"failed"`
	CurrentVideoID uint                   `json:"current_video_id"`
	StartedAt      *time.Time             `json:"started_at,omitempty" ts_type:"string"`
	UpdatedAt      *time.Time             `json:"updated_at,omitempty" ts_type:"string"`
	Failures       []SemanticIndexFailure `json:"failures"`
	Unavailable    string                 `json:"unavailable"`
}

type SemanticIndexService struct {
	db              *gorm.DB
	capability      database.SemanticVectorCapability
	configProvider  SemanticIndexConfigProvider
	embedderFactory func(SemanticIndexConfig) SemanticEmbeddingClient
	now             func() time.Time
	mu              sync.Mutex
	stopMu          sync.Mutex
	status          SemanticIndexStatus
	cancel          context.CancelFunc
	worker          sync.WaitGroup
	emitter         func(SemanticIndexStatus)
	stopping        bool
	queryMu         sync.Mutex
	queryCache      map[string]semanticQueryCacheEntry
}

// NewSemanticIndexService creates an explicitly started, single-worker semantic indexer.
func NewSemanticIndexService(db *gorm.DB, capability database.SemanticVectorCapability, provider SemanticIndexConfigProvider) *SemanticIndexService {
	unavailable := strings.TrimSpace(capability.Message)
	if !capability.Available && unavailable == "" {
		unavailable = "pgvector 不可用"
	}
	service := &SemanticIndexService{
		db: db, capability: capability, configProvider: provider,
		embedderFactory: NewOpenAICompatibleSemanticEmbeddingClient,
		now:             time.Now,
		status:          SemanticIndexStatus{Available: capability.Available, Unavailable: unavailable},
	}
	service.refreshStoredStatus()
	return service
}

func (s *SemanticIndexService) SetEventEmitter(emitter func(SemanticIndexStatus)) {
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
}

func (s *SemanticIndexService) Start(parent context.Context, request SemanticIndexBuildRequest) (SemanticIndexStatus, error) {
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	status, emitter, err := s.startLocked(parent, request)
	s.mu.Unlock()
	if emitter != nil {
		emitSemanticIndexStatus(emitter, status)
	}
	return status, err
}

func (s *SemanticIndexService) startLocked(parent context.Context, request SemanticIndexBuildRequest) (SemanticIndexStatus, func(SemanticIndexStatus), error) {
	if s.stopping {
		return SemanticIndexStatus{}, nil, errors.New("语义索引任务正在停止")
	}
	if s.status.Running {
		if request.Rebuild {
			return SemanticIndexStatus{}, nil, errors.New("语义索引任务运行中，请先取消当前任务再重建")
		}
		return cloneSemanticIndexStatus(s.status), nil, nil
	}
	if s.db == nil || !s.capability.Available {
		reason := strings.TrimSpace(s.capability.Message)
		if reason == "" {
			reason = "pgvector 不可用"
		}
		s.status = SemanticIndexStatus{Available: false, Unavailable: reason}
		return cloneSemanticIndexStatus(s.status), nil, fmt.Errorf("%w: %s", ErrSemanticIndexUnavailable, reason)
	}
	if s.configProvider == nil {
		return SemanticIndexStatus{}, nil, fmt.Errorf("%w: embedding config provider is missing", ErrSemanticIndexUnavailable)
	}
	config, err := s.configProvider.Load()
	if err != nil {
		return SemanticIndexStatus{}, nil, fmt.Errorf("%w: %v", ErrSemanticIndexUnavailable, err)
	}
	config, err = normalizeSemanticIndexConfig(config)
	if err != nil {
		return SemanticIndexStatus{}, nil, err
	}
	profile, err := s.prepareProfile(config.Model, request.Rebuild)
	if err != nil {
		return SemanticIndexStatus{}, nil, err
	}
	if s.capability.Backend == "postgres" && profile.Dimension > 0 {
		if err := database.EnsureSemanticVectorANNIndex(s.db, s.capability, profile.Dimension); err != nil {
			return SemanticIndexStatus{}, nil, fmt.Errorf("语义向量 ANN 索引创建失败（pgvector 需要 0.5+ 支持 HNSW）: %w", err)
		}
	}
	if request.Rebuild {
		s.clearSemanticQueryCache()
	}
	now := s.now()
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.status = SemanticIndexStatus{
		Available: true, Running: true, Model: profile.ActiveModel, Dimension: profile.Dimension,
		Generation: profile.Generation, NeedsRebuild: profile.NeedsRebuild,
		StartedAt: &now, UpdatedAt: &now, Failures: []SemanticIndexFailure{},
	}
	status, emitter := cloneSemanticIndexStatus(s.status), s.emitter
	embedder := s.embedderFactory(config)
	s.worker.Add(1)
	go s.run(ctx, profile, config, embedder)
	return status, emitter, nil
}

func (s *SemanticIndexService) Status() SemanticIndexStatus {
	s.mu.Lock()
	status := cloneSemanticIndexStatus(s.status)
	s.mu.Unlock()
	if status.Running || !status.Available || s.db == nil {
		return status
	}
	var profile models.SemanticIndexProfile
	if err := s.db.First(&profile, "id = ?", 1).Error; err == nil {
		status.Model = profile.ActiveModel
		status.Dimension = profile.Dimension
		status.Generation = profile.Generation
		status.NeedsRebuild = profile.NeedsRebuild
		if s.configProvider != nil {
			if config, configErr := s.configProvider.Load(); configErr == nil && strings.TrimSpace(config.Model) != "" && strings.TrimSpace(config.Model) != profile.ActiveModel {
				status.NeedsRebuild = true
			}
		}
	}
	return status
}

func (s *SemanticIndexService) refreshStoredStatus() {
	if s.db == nil || !s.capability.Available {
		return
	}
	var profile models.SemanticIndexProfile
	if err := s.db.First(&profile, "id = ?", 1).Error; err != nil {
		return
	}
	var total, indexed int64
	_ = s.db.Model(&models.Video{}).Count(&total).Error
	if profile.Dimension > 0 {
		_ = s.db.Model(&models.VideoSemanticIndex{}).
			Where("model_identifier = ? AND dimension = ? AND generation = ?", profile.ActiveModel, profile.Dimension, profile.Generation).
			Where("video_id IN (?)", s.db.Model(&models.Video{}).Select("id")).
			Distinct("video_id").Count(&indexed).Error
	}
	s.status.Model = profile.ActiveModel
	s.status.Dimension = profile.Dimension
	s.status.Generation = profile.Generation
	s.status.NeedsRebuild = profile.NeedsRebuild
	s.status.Total = int(total)
	s.status.Processed = int(indexed)
	s.status.Succeeded = int(indexed)
	s.status.Completed = profile.Dimension > 0 && indexed == total && !profile.NeedsRebuild
}

func (s *SemanticIndexService) Cancel() error {
	s.mu.Lock()
	cancel, running := s.cancel, s.status.Running
	s.mu.Unlock()
	if !running || cancel == nil {
		return errors.New("语义索引任务未运行")
	}
	cancel()
	return nil
}

func (s *SemanticIndexService) StopAndWait() {
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

func (s *SemanticIndexService) prepareProfile(model string, rebuild bool) (models.SemanticIndexProfile, error) {
	var profile models.SemanticIndexProfile
	err := s.db.First(&profile, "id = ?", 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		profile = models.SemanticIndexProfile{ID: 1, ActiveModel: model, Generation: 1}
		if err := s.db.Create(&profile).Error; err != nil {
			return profile, err
		}
		return profile, nil
	}
	if err != nil {
		return profile, err
	}
	if !rebuild && (profile.ActiveModel != model || profile.NeedsRebuild) {
		return profile, fmt.Errorf("%w: active model=%q requested=%q", ErrSemanticIndexRebuildRequired, profile.ActiveModel, model)
	}
	if rebuild {
		err := s.db.Transaction(func(tx *gorm.DB) error {
			profile.ActiveModel = model
			profile.Dimension = 0
			profile.Generation++
			profile.NeedsRebuild = false
			profile.LastError = ""
			profile.DimensionSetAt = nil
			return tx.Save(&profile).Error
		})
		if err != nil {
			return profile, err
		}
	}
	return profile, nil
}

func (s *SemanticIndexService) run(ctx context.Context, profile models.SemanticIndexProfile, config SemanticIndexConfig, embedder SemanticEmbeddingClient) {
	defer s.worker.Done()
	// 全量视频加载在 worker 内完成，避免在服务互斥锁内做 O(库) 的读取，
	// 阻塞 Status/Cancel 调用；快照语义：启动后新增的视频等下次运行。
	var videos []models.Video
	if err := s.db.WithContext(ctx).Preload("Tags").Order("id ASC").Find(&videos).Error; err != nil {
		if ctx.Err() == nil {
			s.updateStatus(func(status *SemanticIndexStatus) {
				status.Failed++
				status.Failures = append(status.Failures, SemanticIndexFailure{Code: "video_list_failed", Error: boundedError(err, 1000)})
			})
		}
		s.finish(ctx.Err() != nil)
		return
	}
	s.updateStatus(func(status *SemanticIndexStatus) { status.Total = len(videos) })
	for _, video := range videos {
		if ctx.Err() != nil {
			break
		}
		s.updateStatus(func(status *SemanticIndexStatus) { status.CurrentVideoID = video.ID })
		text, fingerprint, err := s.buildIndexText(ctx, video, config)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			s.recordVideoFailure(video, profile, fingerprint, "index_text_failed", err)
			continue
		}
		if profile.Dimension > 0 {
			var count int64
			err := s.db.WithContext(ctx).Model(&models.VideoSemanticIndex{}).
				Where("video_id = ? AND model_identifier = ? AND dimension = ? AND generation = ? AND content_fingerprint = ?", video.ID, profile.ActiveModel, profile.Dimension, profile.Generation, fingerprint).
				Count(&count).Error
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				s.recordVideoFailure(video, profile, fingerprint, "resume_check_failed", err)
				continue
			}
			if count > 0 {
				s.updateStatus(func(status *SemanticIndexStatus) { status.Processed++; status.Skipped++ })
				continue
			}
		}
		s.markAttempt(ctx, video.ID, profile, fingerprint, models.SemanticIndexAttemptProcessing, 0, "", "")
		embedding, err := embedder.Embed(ctx, text)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				s.markAttempt(context.Background(), video.ID, profile, fingerprint, models.SemanticIndexAttemptCancelled, 0, "cancelled", "")
				break
			}
			s.recordVideoFailure(video, profile, fingerprint, "embedding_failed", err)
			continue
		}
		if err := validateSemanticEmbedding(embedding); err != nil {
			s.recordVideoFailure(video, profile, fingerprint, "embedding_invalid", err)
			continue
		}
		hadDimension := profile.Dimension > 0
		dimension, err := s.fixDimension(profile, len(embedding))
		if err != nil {
			s.recordVideoFailure(video, profile, fingerprint, "dimension_mismatch", err)
			s.updateStatus(func(status *SemanticIndexStatus) { status.NeedsRebuild = true })
			break
		}
		profile.Dimension = dimension
		s.updateStatus(func(status *SemanticIndexStatus) { status.Dimension = dimension })
		if !hadDimension && s.capability.Backend == "postgres" {
			if err := database.EnsureSemanticVectorANNIndex(s.db, s.capability, dimension); err != nil {
				s.recordVideoFailure(video, profile, fingerprint, "ann_index_failed", err)
				break
			}
		}
		if err := s.storeEmbedding(ctx, video.ID, profile, fingerprint, embedding); err != nil {
			if ctx.Err() != nil {
				break
			}
			s.recordVideoFailure(video, profile, fingerprint, "persist_failed", err)
			continue
		}
		s.markAttempt(ctx, video.ID, profile, fingerprint, models.SemanticIndexAttemptCompleted, dimension, "", "")
		s.updateStatus(func(status *SemanticIndexStatus) { status.Processed++; status.Succeeded++ })
	}
	s.finish(ctx.Err() != nil)
}

func (s *SemanticIndexService) fixDimension(profile models.SemanticIndexProfile, dimension int) (int, error) {
	if dimension <= 0 {
		return 0, fmt.Errorf("embedding dimension is empty")
	}
	var current models.SemanticIndexProfile
	if err := s.db.First(&current, "id = ?", profile.ID).Error; err != nil {
		return 0, err
	}
	if current.ActiveModel != profile.ActiveModel || current.Generation != profile.Generation {
		return 0, ErrSemanticIndexRebuildRequired
	}
	if current.Dimension == 0 {
		now := s.now()
		result := s.db.Model(&models.SemanticIndexProfile{}).
			Where("id = ? AND dimension = 0", current.ID).
			Updates(map[string]any{"dimension": dimension, "dimension_set_at": now, "needs_rebuild": false, "last_error": ""})
		if result.Error != nil {
			return 0, result.Error
		}
		if result.RowsAffected == 1 {
			return dimension, nil
		}
		if err := s.db.First(&current, "id = ?", profile.ID).Error; err != nil {
			return 0, err
		}
	}
	if current.Dimension != dimension {
		message := fmt.Sprintf("embedding dimension changed from %d to %d", current.Dimension, dimension)
		_ = s.db.Model(&current).Updates(map[string]any{"needs_rebuild": true, "last_error": message}).Error
		return 0, fmt.Errorf("%w: %s", ErrSemanticIndexDimensionMismatch, message)
	}
	return current.Dimension, nil
}

func (s *SemanticIndexService) storeEmbedding(ctx context.Context, videoID uint, profile models.SemanticIndexProfile, fingerprint string, embedding []float64) error {
	now := s.now()
	row := models.VideoSemanticIndex{
		VideoID: videoID, ModelIdentifier: profile.ActiveModel, Dimension: len(embedding),
		Generation: profile.Generation, ContentFingerprint: fingerprint, IndexedAt: now,
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "video_id"}, {Name: "model_identifier"}, {Name: "dimension"}},
			DoUpdates: clause.AssignmentColumns([]string{"generation", "content_fingerprint", "indexed_at", "updated_at"}),
		}).Create(&row).Error; err != nil {
			return err
		}
		if s.capability.Backend == "postgres" {
			return database.UpsertSemanticVector(tx, s.capability, videoID, profile.ActiveModel, len(embedding), profile.Generation, embedding, now)
		}
		return nil
	})
}

func (s *SemanticIndexService) buildIndexText(ctx context.Context, video models.Video, config SemanticIndexConfig) (string, string, error) {
	sections := make([]string, 0, 7)
	title := strings.TrimSpace(video.DisplayTitle)
	if title == "" {
		title = strings.TrimSpace(video.Name)
	}
	appendSemanticSection(&sections, "标题", title)
	appendSemanticSection(&sections, "原始标题", video.OriginalTitle)
	appendSemanticSection(&sections, "简介", video.Description)
	tags := make([]string, 0, len(video.Tags))
	for _, tag := range video.Tags {
		if name := strings.TrimSpace(tag.Name); name != "" {
			tags = append(tags, name)
		}
	}
	sort.Slice(tags, func(i, j int) bool { return normalizeLocalMetadataName(tags[i]) < normalizeLocalMetadataName(tags[j]) })
	if len(tags) > 0 {
		appendSemanticSection(&sections, "标签", strings.Join(tags, " / "))
	}
	var candidates []models.AITagCandidate
	if err := s.db.WithContext(ctx).Select("reasoning", "source_summary").
		Where("video_id = ? AND status IN ?", video.ID, []string{models.AITagCandidateStatusPending, models.AITagCandidateStatusApproved}).
		Order("updated_at DESC, id DESC").Limit(5).Find(&candidates).Error; err != nil {
		return "", "", err
	}
	aiParts := make([]string, 0, len(candidates)*2)
	for _, candidate := range candidates {
		if value := strings.TrimSpace(candidate.Reasoning); value != "" {
			aiParts = append(aiParts, value)
		}
		if value := sanitizeSemanticAISourceSummary(candidate.SourceSummary); value != "" {
			aiParts = append(aiParts, value)
		}
	}
	appendSemanticSection(&sections, "AI 摘要", truncateSemanticRunes(strings.Join(aiParts, "\n"), semanticAISummaryRunes))
	var subtitleTexts []string
	if err := s.db.WithContext(ctx).Model(&models.SubtitleSegment{}).Where("video_id = ?", video.ID).
		Order("segment_index ASC").Pluck("text", &subtitleTexts).Error; err != nil {
		return "", "", err
	}
	appendSemanticSection(&sections, "字幕摘要", truncateSemanticRunes(strings.Join(subtitleTexts, "\n"), semanticSubtitleRunes))
	text := sanitizeSemanticIndexText(strings.Join(sections, "\n"), config.APIKey)
	text = truncateSemanticRunes(text, config.MaxTextRunes)
	digest := sha256.Sum256([]byte(text))
	return text, hex.EncodeToString(digest[:]), nil
}

func normalizeSemanticIndexConfig(config SemanticIndexConfig) (SemanticIndexConfig, error) {
	config.BaseURL = strings.TrimSpace(config.BaseURL)
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.Model = strings.TrimSpace(config.Model)
	if config.BaseURL == "" || config.Model == "" {
		return config, fmt.Errorf("%w: embedding endpoint and model are required", ErrSemanticIndexUnavailable)
	}
	if config.MaxTextRunes <= 0 {
		config.MaxTextRunes = defaultSemanticIndexTextRunes
	}
	if config.MaxTextRunes > maxSemanticIndexTextRunes {
		config.MaxTextRunes = maxSemanticIndexTextRunes
	}
	return config, nil
}

func validateSemanticEmbedding(embedding []float64) error {
	if len(embedding) == 0 || len(embedding) > database.MaxSemanticVectorDimensions {
		return fmt.Errorf("embedding dimension %d is unsupported", len(embedding))
	}
	norm := 0.0
	for _, value := range embedding {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("embedding contains a non-finite value")
		}
		norm += value * value
	}
	if norm == 0 {
		return fmt.Errorf("embedding cannot be all zero")
	}
	return nil
}

func appendSemanticSection(sections *[]string, label, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		*sections = append(*sections, label+": "+value)
	}
}

func sanitizeSemanticAISourceSummary(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return ""
	}
	value = pruneSemanticSummaryValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return truncateSemanticRunes(string(encoded), semanticAISummaryRunes)
}

func pruneSemanticSummaryValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cleaned := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized := strings.ToLower(key)
			if strings.Contains(normalized, "path") || strings.Contains(normalized, "directory") || strings.Contains(normalized, "api_key") || strings.Contains(normalized, "token") || strings.Contains(normalized, "data_url") || normalized == "frames" || normalized == "subtitle_text" {
				continue
			}
			cleaned[key] = pruneSemanticSummaryValue(item)
		}
		return cleaned
	case []any:
		cleaned := make([]any, len(typed))
		for index, item := range typed {
			cleaned[index] = pruneSemanticSummaryValue(item)
		}
		return cleaned
	default:
		return value
	}
}

func sanitizeSemanticIndexText(value, apiKey string) string {
	if apiKey != "" {
		value = strings.ReplaceAll(value, apiKey, "[redacted-secret]")
	}
	// 通用正则以空白截断，含空格或单段的绝对路径盖不全；先按已配置的
	// 扫描根做精确前缀脱敏（含空格路径也能整段移除），再跑通用正则兜底。
	for _, root := range knownLibraryRootPaths() {
		if root == "" {
			continue
		}
		value = strings.ReplaceAll(value, root, "[redacted-path]")
	}
	value = semanticAbsolutePathPattern.ReplaceAllString(value, "[redacted-path]")
	return strings.TrimSpace(value)
}

// knownLibraryRootPaths 返回已配置扫描目录的绝对路径，用于精确脱敏。
func knownLibraryRootPaths() []string {
	if database.DB == nil {
		return nil
	}
	var roots []string
	_ = database.DB.Model(&models.ScanDirectory{}).Order("length(path) DESC").Pluck("path", &roots).Error
	return roots
}

func truncateSemanticRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func (s *SemanticIndexService) markAttempt(parent context.Context, videoID uint, profile models.SemanticIndexProfile, fingerprint, status string, dimension int, code, message string) {
	ctx, cancel := context.WithTimeout(parent, semanticStateWriteTimeout)
	defer cancel()
	now := s.now()
	attempt := models.SemanticIndexAttempt{
		VideoID: videoID, ModelIdentifier: profile.ActiveModel, Generation: profile.Generation,
		Status: status, AttemptCount: 1, ContentFingerprint: fingerprint, Dimension: dimension,
		ErrorCode: code, LastError: boundedError(errors.New(message), 1000), LastAttemptedAt: &now,
	}
	if status == models.SemanticIndexAttemptCompleted {
		attempt.CompletedAt = &now
	}
	updates := map[string]any{
		"status": status, "content_fingerprint": fingerprint, "dimension": dimension,
		"error_code": code, "last_error": attempt.LastError, "last_attempted_at": now,
		"completed_at": nil,
	}
	if status == models.SemanticIndexAttemptProcessing {
		updates["attempt_count"] = gorm.Expr("semantic_index_attempts.attempt_count + 1")
	}
	if attempt.CompletedAt != nil {
		updates["completed_at"] = now
	}
	_ = s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "video_id"}, {Name: "model_identifier"}, {Name: "generation"}},
		DoUpdates: clause.Assignments(updates),
	}).Create(&attempt).Error
}

func (s *SemanticIndexService) recordVideoFailure(video models.Video, profile models.SemanticIndexProfile, fingerprint, code string, operationErr error) {
	message := boundedError(operationErr, 1000)
	s.markAttempt(context.Background(), video.ID, profile, fingerprint, models.SemanticIndexAttemptFailed, 0, code, message)
	s.updateStatus(func(status *SemanticIndexStatus) {
		status.Processed++
		status.Failed++
		if len(status.Failures) < 50 {
			status.Failures = append(status.Failures, SemanticIndexFailure{VideoID: video.ID, Name: video.Name, Code: code, Error: message})
		}
	})
}

func (s *SemanticIndexService) updateStatus(update func(*SemanticIndexStatus)) {
	s.mu.Lock()
	update(&s.status)
	now := s.now()
	s.status.UpdatedAt = &now
	status, emitter := cloneSemanticIndexStatus(s.status), s.emitter
	s.mu.Unlock()
	emitSemanticIndexStatus(emitter, status)
}

func (s *SemanticIndexService) finish(cancelled bool) {
	s.mu.Lock()
	s.status.Running = false
	s.status.Cancelled = cancelled
	s.status.Completed = !cancelled && !s.status.NeedsRebuild
	s.status.CurrentVideoID = 0
	now := s.now()
	s.status.UpdatedAt = &now
	s.cancel = nil
	status, emitter := cloneSemanticIndexStatus(s.status), s.emitter
	s.mu.Unlock()
	emitSemanticIndexStatus(emitter, status)
}

func cloneSemanticIndexStatus(status SemanticIndexStatus) SemanticIndexStatus {
	status.Failures = append([]SemanticIndexFailure(nil), status.Failures...)
	return status
}

func emitSemanticIndexStatus(emitter func(SemanticIndexStatus), status SemanticIndexStatus) {
	if emitter != nil {
		emitter(status)
	}
}

// ValidateSearchEmbedding prevents callers from mixing a model or dimension with the active profile.
func (s *SemanticIndexService) ValidateSearchEmbedding(ctx context.Context, model string, embedding []float64) error {
	var profile models.SemanticIndexProfile
	if err := s.db.WithContext(ctx).First(&profile, "id = ?", 1).Error; err != nil {
		return err
	}
	if profile.NeedsRebuild || profile.Dimension <= 0 {
		return ErrSemanticIndexRebuildRequired
	}
	if strings.TrimSpace(model) != profile.ActiveModel {
		return fmt.Errorf("%w: active=%q requested=%q", ErrSemanticIndexModelMismatch, profile.ActiveModel, model)
	}
	if len(embedding) != profile.Dimension {
		return fmt.Errorf("%w: active=%d requested=%d", ErrSemanticIndexDimensionMismatch, profile.Dimension, len(embedding))
	}
	return nil
}
