package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrImageSemanticIndexUnavailable 表示 pgvector 能力或 embedding 配置缺失，图片语义索引与检索不可用。
	ErrImageSemanticIndexUnavailable = errors.New("image_semantic_index_unavailable")
	// ErrImageSemanticIndexRebuildRequired 表示共享 profile 与请求的模型不符或已标记重建；
	// 图片任务不触发 rebuild（D-010），需先在视频侧完成语义索引重建/模型切换。
	ErrImageSemanticIndexRebuildRequired = errors.New("image_semantic_index_rebuild_required")
)

// ImageSemanticIndexFailure 记录单张图片的索引失败留痕，镜像 SemanticIndexFailure。
type ImageSemanticIndexFailure struct {
	ImageID uint   `json:"image_id"`
	Name    string `json:"name"`
	Code    string `json:"code"`
	Error   string `json:"error"`
}

// ImageSemanticIndexStatus 是图片语义索引任务的状态快照，结构镜像 SemanticIndexStatus。
type ImageSemanticIndexStatus struct {
	Available      bool                        `json:"available"`
	Running        bool                        `json:"running"`
	Cancelled      bool                        `json:"cancelled"`
	Completed      bool                        `json:"completed"`
	NeedsRebuild   bool                        `json:"needs_rebuild"`
	Model          string                      `json:"model"`
	Dimension      int                         `json:"dimension"`
	Generation     int                         `json:"generation"`
	Total          int                         `json:"total"`
	Processed      int                         `json:"processed"`
	Succeeded      int                         `json:"succeeded"`
	Skipped        int                         `json:"skipped"`
	Failed         int                         `json:"failed"`
	CurrentImageID uint                        `json:"current_image_id"`
	StartedAt      *time.Time                  `json:"started_at,omitempty" ts_type:"string"`
	UpdatedAt      *time.Time                  `json:"updated_at,omitempty" ts_type:"string"`
	Failures       []ImageSemanticIndexFailure `json:"failures"`
	Unavailable    string                      `json:"unavailable"`
}

// ImageSemanticIndexService 是图片语义索引与照片页语义检索的单 worker 服务，
// 与视频侧共享全局 SemanticIndexProfile（id=1）但不改写其 rebuild 语义；
// 视频/图片索引任务的全局互斥由 app 层负责。
type ImageSemanticIndexService struct {
	db              *gorm.DB
	capability      database.SemanticVectorCapability
	configProvider  SemanticIndexConfigProvider
	embedderFactory func(SemanticIndexConfig) SemanticEmbeddingClient
	now             func() time.Time
	mu              sync.Mutex
	stopMu          sync.Mutex
	status          ImageSemanticIndexStatus
	cancel          context.CancelFunc
	worker          sync.WaitGroup
	emitter         func(ImageSemanticIndexStatus)
	stopping        bool
	queryMu         sync.Mutex
	queryCache      map[string]semanticQueryCacheEntry
}

// NewImageSemanticIndexService 创建显式启动的图片语义索引服务，签名镜像视频侧。
func NewImageSemanticIndexService(db *gorm.DB, capability database.SemanticVectorCapability, provider SemanticIndexConfigProvider) *ImageSemanticIndexService {
	unavailable := strings.TrimSpace(capability.Message)
	if !capability.Available && unavailable == "" {
		unavailable = "pgvector 不可用"
	}
	service := &ImageSemanticIndexService{
		db: db, capability: capability, configProvider: provider,
		embedderFactory: NewOpenAICompatibleSemanticEmbeddingClient,
		now:             time.Now,
		status:          ImageSemanticIndexStatus{Available: capability.Available, Unavailable: unavailable},
	}
	service.refreshStoredStatus()
	return service
}

// SetEventEmitter 注入状态事件回调，由 app 层接线（建议事件名 image-semantic-index-state）。
func (s *ImageSemanticIndexService) SetEventEmitter(emitter func(ImageSemanticIndexStatus)) {
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
}

// Start 启动一次图片语义索引任务；无 rebuild 参数（D-010：图片任务不触发 rebuild），
// 本服务运行中时拒绝重复 Start。
func (s *ImageSemanticIndexService) Start(parent context.Context) (ImageSemanticIndexStatus, error) {
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	status, emitter, err := s.startLocked(parent)
	s.mu.Unlock()
	if emitter != nil {
		emitImageSemanticIndexStatus(emitter, status)
	}
	return status, err
}

func (s *ImageSemanticIndexService) startLocked(parent context.Context) (ImageSemanticIndexStatus, func(ImageSemanticIndexStatus), error) {
	if s.stopping {
		return ImageSemanticIndexStatus{}, nil, errors.New("图片语义索引任务正在停止")
	}
	if s.status.Running {
		return cloneImageSemanticIndexStatus(s.status), nil, errors.New("图片语义索引任务运行中")
	}
	if s.db == nil || !s.capability.Available {
		reason := strings.TrimSpace(s.capability.Message)
		if reason == "" {
			reason = "pgvector 不可用"
		}
		s.status = ImageSemanticIndexStatus{Available: false, Unavailable: reason}
		return cloneImageSemanticIndexStatus(s.status), nil, fmt.Errorf("%w: %s", ErrImageSemanticIndexUnavailable, reason)
	}
	if s.configProvider == nil {
		return ImageSemanticIndexStatus{}, nil, fmt.Errorf("%w: embedding config provider is missing", ErrImageSemanticIndexUnavailable)
	}
	config, err := s.configProvider.Load()
	if err != nil {
		return ImageSemanticIndexStatus{}, nil, fmt.Errorf("%w: %v", ErrImageSemanticIndexUnavailable, err)
	}
	config, err = normalizeImageSemanticIndexConfig(config)
	if err != nil {
		return ImageSemanticIndexStatus{}, nil, err
	}
	profile, err := s.prepareProfile(config.Model)
	if err != nil {
		return ImageSemanticIndexStatus{}, nil, err
	}
	if s.capability.Backend == "postgres" && profile.Dimension > 0 {
		if err := database.EnsureImageSemanticVectorANNIndex(s.db, s.capability, profile.Dimension); err != nil {
			return ImageSemanticIndexStatus{}, nil, fmt.Errorf("图片语义向量 ANN 索引创建失败（pgvector 需要 0.5+ 支持 HNSW）: %w", err)
		}
	}
	now := s.now()
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.status = ImageSemanticIndexStatus{
		Available: true, Running: true, Model: profile.ActiveModel, Dimension: profile.Dimension,
		Generation: profile.Generation, NeedsRebuild: profile.NeedsRebuild,
		StartedAt: &now, UpdatedAt: &now, Failures: []ImageSemanticIndexFailure{},
	}
	status, emitter := cloneImageSemanticIndexStatus(s.status), s.emitter
	embedder := s.embedderFactory(config)
	s.worker.Add(1)
	go s.run(ctx, profile, config, embedder)
	return status, emitter, nil
}

// Status 返回当前状态快照；空闲时刷新共享 profile 的最新模型/维度/重建标记。
func (s *ImageSemanticIndexService) Status() ImageSemanticIndexStatus {
	s.mu.Lock()
	status := cloneImageSemanticIndexStatus(s.status)
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

func (s *ImageSemanticIndexService) refreshStoredStatus() {
	if s.db == nil || !s.capability.Available {
		return
	}
	var profile models.SemanticIndexProfile
	if err := s.db.First(&profile, "id = ?", 1).Error; err != nil {
		return
	}
	var total, indexed int64
	_ = s.db.Model(&models.Image{}).Count(&total).Error
	if profile.Dimension > 0 {
		_ = s.db.Model(&models.ImageSemanticIndex{}).
			Where("model_identifier = ? AND dimension = ? AND generation = ?", profile.ActiveModel, profile.Dimension, profile.Generation).
			Where("image_id IN (?)", s.db.Model(&models.Image{}).Select("id")).
			Distinct("image_id").Count(&indexed).Error
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

// Cancel 请求取消运行中的任务。
func (s *ImageSemanticIndexService) Cancel() error {
	s.mu.Lock()
	cancel, running := s.cancel, s.status.Running
	s.mu.Unlock()
	if !running || cancel == nil {
		return errors.New("图片语义索引任务未运行")
	}
	cancel()
	return nil
}

// StopAndWait 取消并等待 worker 退出，用于应用关停。
func (s *ImageSemanticIndexService) StopAndWait() {
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

// prepareProfile 镜像视频侧 CAS：profile 缺失时创建；存在但模型不符或已标记
// 重建时拒绝（图片任务不触发 rebuild，需先在视频侧完成重建/模型切换）。
func (s *ImageSemanticIndexService) prepareProfile(model string) (models.SemanticIndexProfile, error) {
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
	if profile.ActiveModel != model || profile.NeedsRebuild {
		return profile, fmt.Errorf("%w: active model=%q requested=%q needs_rebuild=%t，请先在视频侧完成语义索引重建/模型切换", ErrImageSemanticIndexRebuildRequired, profile.ActiveModel, model, profile.NeedsRebuild)
	}
	return profile, nil
}

func (s *ImageSemanticIndexService) run(ctx context.Context, profile models.SemanticIndexProfile, config SemanticIndexConfig, embedder SemanticEmbeddingClient) {
	defer s.worker.Done()
	// 快照加载全部活跃图片（软删除由默认作用域排除）；启动后新增的图片等下次运行。
	var images []models.Image
	if err := s.db.WithContext(ctx).Preload("Tags").Order("id ASC").Find(&images).Error; err != nil {
		if ctx.Err() == nil {
			s.updateStatus(func(status *ImageSemanticIndexStatus) {
				status.Failed++
				status.Failures = append(status.Failures, ImageSemanticIndexFailure{Code: "image_list_failed", Error: boundedError(err, 1000)})
			})
		}
		s.finish(ctx.Err() != nil)
		return
	}
	s.updateStatus(func(status *ImageSemanticIndexStatus) { status.Total = len(images) })
	for _, image := range images {
		if ctx.Err() != nil {
			break
		}
		s.updateStatus(func(status *ImageSemanticIndexStatus) { status.CurrentImageID = image.ID })
		description, hasDescription, err := s.loadCompletedDescription(ctx, image.ID)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			s.recordImageFailure(image, profile, "", "index_text_failed", err)
			continue
		}
		if !hasDescription {
			// D-010：无 completed AI 描述的图片不索引，Skipped 计数。
			s.updateStatus(func(status *ImageSemanticIndexStatus) { status.Processed++; status.Skipped++ })
			continue
		}
		text, fingerprint := s.buildIndexText(image, description, config)
		if profile.Dimension > 0 {
			var count int64
			err := s.db.WithContext(ctx).Model(&models.ImageSemanticIndex{}).
				Where("image_id = ? AND model_identifier = ? AND dimension = ? AND generation = ? AND content_fingerprint = ?", image.ID, profile.ActiveModel, profile.Dimension, profile.Generation, fingerprint).
				Count(&count).Error
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				s.recordImageFailure(image, profile, fingerprint, "resume_check_failed", err)
				continue
			}
			if count > 0 {
				s.updateStatus(func(status *ImageSemanticIndexStatus) { status.Processed++; status.Skipped++ })
				continue
			}
		}
		s.markAttempt(ctx, image.ID, profile, fingerprint, models.SemanticIndexAttemptProcessing, 0, "", "")
		embedding, err := embedder.Embed(ctx, text)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				s.markAttempt(context.Background(), image.ID, profile, fingerprint, models.SemanticIndexAttemptCancelled, 0, "cancelled", "")
				break
			}
			s.recordImageFailure(image, profile, fingerprint, "embedding_failed", err)
			continue
		}
		if err := validateSemanticEmbedding(embedding); err != nil {
			s.recordImageFailure(image, profile, fingerprint, "embedding_invalid", err)
			continue
		}
		hadDimension := profile.Dimension > 0
		dimension, err := s.fixDimension(profile, len(embedding))
		if err != nil {
			s.recordImageFailure(image, profile, fingerprint, "dimension_mismatch", err)
			s.updateStatus(func(status *ImageSemanticIndexStatus) { status.NeedsRebuild = true })
			break
		}
		profile.Dimension = dimension
		s.updateStatus(func(status *ImageSemanticIndexStatus) { status.Dimension = dimension })
		if !hadDimension && s.capability.Backend == "postgres" {
			if err := database.EnsureImageSemanticVectorANNIndex(s.db, s.capability, dimension); err != nil {
				s.recordImageFailure(image, profile, fingerprint, "ann_index_failed", err)
				break
			}
		}
		if err := s.storeEmbedding(ctx, image.ID, profile, fingerprint, embedding); err != nil {
			if ctx.Err() != nil {
				break
			}
			s.recordImageFailure(image, profile, fingerprint, "persist_failed", err)
			continue
		}
		s.markAttempt(ctx, image.ID, profile, fingerprint, models.SemanticIndexAttemptCompleted, dimension, "", "")
		s.updateStatus(func(status *ImageSemanticIndexStatus) { status.Processed++; status.Succeeded++ })
	}
	s.finish(ctx.Err() != nil)
}

// loadCompletedDescription 返回图片的 completed AI 描述；无行或描述为空视为无描述。
func (s *ImageSemanticIndexService) loadCompletedDescription(ctx context.Context, imageID uint) (string, bool, error) {
	var row models.ImageAIDescription
	err := s.db.WithContext(ctx).
		Where("image_id = ? AND status = ?", imageID, imageAIDescriptionStatusCompleted).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(row.Description) == "" {
		return "", false, nil
	}
	return row.Description, true, nil
}

// buildIndexText 构建三段式索引文本（标题=文件名、标签、AI 描述），复用视频侧的
// 分段结构、脱敏与截断，并返回 sha256 内容指纹。
func (s *ImageSemanticIndexService) buildIndexText(image models.Image, description string, config SemanticIndexConfig) (string, string) {
	sections := make([]string, 0, 3)
	appendSemanticSection(&sections, "标题", image.Name)
	tags := make([]string, 0, len(image.Tags))
	for _, tag := range image.Tags {
		if name := strings.TrimSpace(tag.Name); name != "" {
			tags = append(tags, name)
		}
	}
	sort.Slice(tags, func(i, j int) bool { return normalizeLocalMetadataName(tags[i]) < normalizeLocalMetadataName(tags[j]) })
	if len(tags) > 0 {
		appendSemanticSection(&sections, "标签", strings.Join(tags, " / "))
	}
	appendSemanticSection(&sections, "AI 描述", description)
	text := sanitizeSemanticIndexText(strings.Join(sections, "\n"), config.APIKey)
	text = truncateSemanticRunes(text, config.MaxTextRunes)
	digest := sha256.Sum256([]byte(text))
	return text, hex.EncodeToString(digest[:])
}

// fixDimension 镜像视频侧：CAS 首次定维；模型/代际被并发改写时要求重建；
// 维度失配时置 needs_rebuild 并报 dimension_mismatch。
func (s *ImageSemanticIndexService) fixDimension(profile models.SemanticIndexProfile, dimension int) (int, error) {
	if dimension <= 0 {
		return 0, fmt.Errorf("embedding dimension is empty")
	}
	var current models.SemanticIndexProfile
	if err := s.db.First(&current, "id = ?", profile.ID).Error; err != nil {
		return 0, err
	}
	if current.ActiveModel != profile.ActiveModel || current.Generation != profile.Generation {
		return 0, ErrImageSemanticIndexRebuildRequired
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

// storeEmbedding 事务双写：ImageSemanticIndex 元数据 upsert + pgvector 图片向量表。
func (s *ImageSemanticIndexService) storeEmbedding(ctx context.Context, imageID uint, profile models.SemanticIndexProfile, fingerprint string, embedding []float64) error {
	now := s.now()
	row := models.ImageSemanticIndex{
		ImageID: imageID, ModelIdentifier: profile.ActiveModel, Dimension: len(embedding),
		Generation: profile.Generation, ContentFingerprint: fingerprint, IndexedAt: now,
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "image_id"}, {Name: "model_identifier"}, {Name: "dimension"}},
			DoUpdates: clause.AssignmentColumns([]string{"generation", "content_fingerprint", "indexed_at", "updated_at"}),
		}).Create(&row).Error; err != nil {
			return err
		}
		if s.capability.Backend == "postgres" {
			return database.UpsertImageSemanticVector(tx, s.capability, imageID, profile.ActiveModel, len(embedding), profile.Generation, embedding, now)
		}
		return nil
	})
}

func (s *ImageSemanticIndexService) markAttempt(parent context.Context, imageID uint, profile models.SemanticIndexProfile, fingerprint, status string, dimension int, code, message string) {
	ctx, cancel := context.WithTimeout(parent, semanticStateWriteTimeout)
	defer cancel()
	now := s.now()
	attempt := models.ImageSemanticIndexAttempt{
		ImageID: imageID, ModelIdentifier: profile.ActiveModel, Generation: profile.Generation,
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
		updates["attempt_count"] = gorm.Expr("image_semantic_index_attempts.attempt_count + 1")
	}
	if attempt.CompletedAt != nil {
		updates["completed_at"] = now
	}
	_ = s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "image_id"}, {Name: "model_identifier"}, {Name: "generation"}},
		DoUpdates: clause.Assignments(updates),
	}).Create(&attempt).Error
}

func (s *ImageSemanticIndexService) recordImageFailure(image models.Image, profile models.SemanticIndexProfile, fingerprint, code string, operationErr error) {
	message := boundedError(operationErr, 1000)
	s.markAttempt(context.Background(), image.ID, profile, fingerprint, models.SemanticIndexAttemptFailed, 0, code, message)
	s.updateStatus(func(status *ImageSemanticIndexStatus) {
		status.Processed++
		status.Failed++
		if len(status.Failures) < 50 {
			status.Failures = append(status.Failures, ImageSemanticIndexFailure{ImageID: image.ID, Name: image.Name, Code: code, Error: message})
		}
	})
}

func (s *ImageSemanticIndexService) updateStatus(update func(*ImageSemanticIndexStatus)) {
	s.mu.Lock()
	update(&s.status)
	now := s.now()
	s.status.UpdatedAt = &now
	status, emitter := cloneImageSemanticIndexStatus(s.status), s.emitter
	s.mu.Unlock()
	emitImageSemanticIndexStatus(emitter, status)
}

func (s *ImageSemanticIndexService) finish(cancelled bool) {
	s.mu.Lock()
	s.status.Running = false
	s.status.Cancelled = cancelled
	s.status.Completed = !cancelled && !s.status.NeedsRebuild
	s.status.CurrentImageID = 0
	now := s.now()
	s.status.UpdatedAt = &now
	s.cancel = nil
	status, emitter := cloneImageSemanticIndexStatus(s.status), s.emitter
	s.mu.Unlock()
	emitImageSemanticIndexStatus(emitter, status)
}

func cloneImageSemanticIndexStatus(status ImageSemanticIndexStatus) ImageSemanticIndexStatus {
	status.Failures = append([]ImageSemanticIndexFailure(nil), status.Failures...)
	return status
}

func emitImageSemanticIndexStatus(emitter func(ImageSemanticIndexStatus), status ImageSemanticIndexStatus) {
	if emitter != nil {
		emitter(status)
	}
}

// normalizeImageSemanticIndexConfig 复用视频侧的归一化，仅把失败映射为图片版哨兵。
func normalizeImageSemanticIndexConfig(config SemanticIndexConfig) (SemanticIndexConfig, error) {
	config, err := normalizeSemanticIndexConfig(config)
	if err != nil {
		return config, fmt.Errorf("%w: embedding endpoint and model are required", ErrImageSemanticIndexUnavailable)
	}
	return config, nil
}
