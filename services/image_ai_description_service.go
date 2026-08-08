package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 图片 AI 描述状态机取值（设计 4.6.2：单表 image_ai_descriptions 承载结果与状态）。
const (
	imageAIDescriptionStatusPending    = "pending"
	imageAIDescriptionStatusProcessing = "processing"
	imageAIDescriptionStatusCompleted  = "completed"
	imageAIDescriptionStatusFailed     = "failed"
)

// 图片 AI 描述失败留痕 error_code（设计 4.6.4 边界条件表）。
const (
	imageAIDescriptionErrorEmptyResponse     = "empty_response"
	imageAIDescriptionErrorRequestFailed     = "request_failed"
	imageAIDescriptionErrorDecodeUnsupported = "decode_unsupported"
	imageAIDescriptionErrorInterrupted       = "interrupted"
	imageAIDescriptionErrorPersistFailed     = "persist_failed"
	imageAIDescriptionErrorMetadataStrip     = "metadata_strip_failed"
)

const (
	imageAIDescriptionMaxRunes       = 4000
	imageAIDescriptionErrorRuneLimit = 1000
	imageAIDescriptionMaxFailures    = 50
	imageAIDescriptionRequestTimeout = 5 * time.Minute

	// imageAIDescriptionCodeCancelled 是 executeOne 的内部结果码，不落库：
	// 取消时当前图片已回退 pending。
	imageAIDescriptionCodeCancelled = "cancelled"
)

// ErrImageAIDescriptionConfigUnavailable 表示 AI 配置不可用（BaseURL/Model 为空），
// Start/Regenerate 直接拒绝（设计 4.6.4：设置引导提示）。
var ErrImageAIDescriptionConfigUnavailable = errors.New("AI 配置不可用")

// ErrImageAIDescriptionBusy 表示批量任务或单张重跑正在执行，拒绝并发启动。
var ErrImageAIDescriptionBusy = errors.New("图片 AI 描述任务运行中")

// 提示词契约（设计 4.6.2）：一段 80–200 字中文自然语言描述（画面内容、场景、风格），
// 禁止标签列表/JSON/Markdown/前后缀说明。
const imageAIDescriptionSystemPrompt = "你是本地图片库的看图描述助手。你只输出一段中文自然语言描述正文，" +
	"禁止输出标签列表、JSON、Markdown，禁止任何前缀或后缀说明。"

const imageAIDescriptionUserPrompt = "请用一段 80 到 200 字的中文自然语言描述这张图片，依次覆盖画面内容、所处场景与整体风格。" +
	"只输出这一段描述正文本身：不要标签列表，不要 JSON，不要 Markdown，不要“以下是描述”之类的前后缀说明。"

// ImageDescriptionClient 把单图描述请求与传输细节解耦，便于测试注入。
type ImageDescriptionClient interface {
	Describe(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error)
}

// OpenAICompatibleImageDescriptionClient 默认实现：chat 多模态单图请求
// （temperature 0.1、5 分钟超时、无客户端重试，重试语义由 attempt_count 承担）。
type OpenAICompatibleImageDescriptionClient struct {
	config AITaggingConfig
	client *http.Client
}

// NewOpenAICompatibleImageDescriptionClient 创建 OpenAI 兼容 chat completions 客户端。
func NewOpenAICompatibleImageDescriptionClient(config AITaggingConfig) ImageDescriptionClient {
	return &OpenAICompatibleImageDescriptionClient{
		config: config,
		client: &http.Client{Timeout: imageAIDescriptionRequestTimeout},
	}
}

func (c *OpenAICompatibleImageDescriptionClient) Describe(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
	body := map[string]interface{}{
		"model": c.config.Model,
		"messages": []map[string]interface{}{
			{"role": "system", "content": imageAIDescriptionSystemPrompt},
			{"role": "user", "content": []map[string]interface{}{
				{"type": "text", "text": prompt},
				{"type": "image_url", "image_url": map[string]string{
					"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegData),
				}},
			}},
		},
		"temperature": 0.1,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	log.Printf("[ImageAIDescription] request image_id=%d model=%q payload_bytes=%d", imageID, c.config.Model, len(payload))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, imageAIDescriptionChatCompletionsURL(c.config.BaseURL), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(c.config.APIKey); key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		log.Printf("[ImageAIDescription] request failed image_id=%d", imageID)
		return "", err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return "", readErr
	}
	log.Printf("[ImageAIDescription] response image_id=%d status=%d bytes=%d", imageID, resp.StatusCode, len(respBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("图片描述 API 返回 %d", resp.StatusCode)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("解析图片描述 API 响应失败: %w", err)
	}
	// 空响应不是传输错误：交由服务层记 empty_response。
	if len(parsed.Choices) == 0 {
		return "", nil
	}
	return parsed.Choices[0].Message.Content, nil
}

// imageAIDescriptionChatCompletionsURL 镜像 openAIChatCompletionsURL 的拼接逻辑
// （本文件内实现，不改 ai_tagging_client.go）。
func imageAIDescriptionChatCompletionsURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

// ImageAIDescriptionFailure 是状态面板可见的单图失败留痕（有界列表）。
type ImageAIDescriptionFailure struct {
	ImageID uint   `json:"image_id"`
	Name    string `json:"name"`
	Code    string `json:"code"`
	Error   string `json:"error"`
}

// ImageAIDescriptionStatus 形态镜像 SemanticIndexStatus，供进度事件与状态查询共用。
type ImageAIDescriptionStatus struct {
	Running        bool                        `json:"running"`
	Cancelled      bool                        `json:"cancelled"`
	Completed      bool                        `json:"completed"`
	Total          int                         `json:"total"`
	Processed      int                         `json:"processed"`
	Succeeded      int                         `json:"succeeded"`
	Failed         int                         `json:"failed"`
	Skipped        int                         `json:"skipped"`
	CurrentImageID uint                        `json:"current_image_id"`
	StartedAt      *time.Time                  `json:"started_at,omitempty" ts_type:"string"`
	UpdatedAt      *time.Time                  `json:"updated_at,omitempty" ts_type:"string"`
	Failures       []ImageAIDescriptionFailure `json:"failures"`
}

// ImageAIDescriptionService 管理图片 AI 描述的批量三件套与单张重跑（设计 4.6，D-009）。
type ImageAIDescriptionService struct {
	db             *gorm.DB
	thumbnails     *ImageThumbnailService
	configProvider AITaggingConfigProvider
	clientFactory  func(AITaggingConfig) ImageDescriptionClient
	now            func() time.Time

	mu       sync.Mutex
	stopMu   sync.Mutex
	status   ImageAIDescriptionStatus
	cancel   context.CancelFunc
	worker   sync.WaitGroup
	emitter  func(ImageAIDescriptionStatus)
	stopping bool
	// busy 同时覆盖批量运行与同步单张重跑：两者互斥，避免同一行双写。
	busy bool
}

// NewImageAIDescriptionService 创建图片 AI 描述服务（单 worker、显式启动）。
func NewImageAIDescriptionService(db *gorm.DB, thumbnails *ImageThumbnailService, provider AITaggingConfigProvider) *ImageAIDescriptionService {
	return &ImageAIDescriptionService{
		db:             db,
		thumbnails:     thumbnails,
		configProvider: provider,
		clientFactory:  NewOpenAICompatibleImageDescriptionClient,
		now:            time.Now,
		status:         ImageAIDescriptionStatus{Failures: []ImageAIDescriptionFailure{}},
	}
}

// SetEventEmitter 注入进度事件回调（app 层接 Wails 事件 image-ai-description-progress）。
func (s *ImageAIDescriptionService) SetEventEmitter(emitter func(ImageAIDescriptionStatus)) {
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
}

// prepareClient 校验 AI 配置可用并构造客户端；BaseURL/Model 为空一律拒绝。
func (s *ImageAIDescriptionService) prepareClient() (AITaggingConfig, ImageDescriptionClient, error) {
	if s.configProvider == nil {
		return AITaggingConfig{}, nil, fmt.Errorf("%w: 配置提供者缺失", ErrImageAIDescriptionConfigUnavailable)
	}
	config, err := s.configProvider.Load()
	if err != nil {
		return config, nil, fmt.Errorf("%w: %v", ErrImageAIDescriptionConfigUnavailable, err)
	}
	if strings.TrimSpace(config.BaseURL) == "" || strings.TrimSpace(config.Model) == "" {
		return config, nil, fmt.Errorf("%w: BaseURL 或 Model 为空", ErrImageAIDescriptionConfigUnavailable)
	}
	return config, s.clientFactory(config), nil
}

// StartImageAIDescription 启动批量描述：目标集 = 活跃图片中无 completed 且非
// processing 描述的图片（failed 可重试）；运行中拒绝二次启动。
func (s *ImageAIDescriptionService) StartImageAIDescription(parent context.Context) (ImageAIDescriptionStatus, error) {
	if parent == nil {
		parent = context.Background()
	}
	config, client, err := s.prepareClient()
	if err != nil {
		return ImageAIDescriptionStatus{}, err
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return ImageAIDescriptionStatus{}, errors.New("图片 AI 描述任务正在停止")
	}
	if s.busy || s.status.Running {
		status := cloneImageAIDescriptionStatus(s.status)
		s.mu.Unlock()
		return status, ErrImageAIDescriptionBusy
	}
	now := s.now()
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.busy = true
	s.status = ImageAIDescriptionStatus{
		Running: true, StartedAt: &now, UpdatedAt: &now,
		Failures: []ImageAIDescriptionFailure{},
	}
	status, emitter := cloneImageAIDescriptionStatus(s.status), s.emitter
	s.worker.Add(1)
	s.mu.Unlock()
	emitImageAIDescriptionStatus(emitter, status)
	go s.run(ctx, config, client)
	return status, nil
}

// GetImageAIDescriptionStatus 返回当前任务状态快照。
func (s *ImageAIDescriptionService) GetImageAIDescriptionStatus() ImageAIDescriptionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneImageAIDescriptionStatus(s.status)
}

// CancelImageAIDescription 取消运行中的批量任务；当前图片回退 pending 后停止。
func (s *ImageAIDescriptionService) CancelImageAIDescription() error {
	s.mu.Lock()
	cancel, running := s.cancel, s.status.Running
	s.mu.Unlock()
	if !running || cancel == nil {
		return errors.New("图片 AI 描述任务未运行")
	}
	cancel()
	return nil
}

// StopAndWait 供 shutdown：取消在途任务（含单张重跑）并等待 worker 退出。
func (s *ImageAIDescriptionService) StopAndWait() {
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

// RegenerateImageAIDescription 同步单张重跑并覆盖旧描述。与批量任务互斥：
// 批量运行中拒绝（跟随语义索引"同一时刻只有一个写任务"的并发惯例）。
func (s *ImageAIDescriptionService) RegenerateImageAIDescription(imageID uint) (*models.ImageAIDescription, error) {
	config, client, err := s.prepareClient()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return nil, errors.New("图片 AI 描述任务正在停止")
	}
	if s.busy || s.status.Running {
		s.mu.Unlock()
		return nil, ErrImageAIDescriptionBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.busy = true
	s.cancel = cancel
	s.worker.Add(1)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy = false
		s.cancel = nil
		s.mu.Unlock()
		cancel()
		s.worker.Done()
	}()

	var img models.Image
	if err := s.db.First(&img, imageID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("图片 %d 不存在或已删除", imageID)
		}
		return nil, err
	}
	code, execErr := s.executeOne(ctx, config, client, img)
	if code == imageAIDescriptionCodeCancelled {
		return nil, errors.New("图片描述重跑已取消")
	}
	if code != "" {
		return nil, fmt.Errorf("图片描述生成失败（%s）: %v", code, execErr)
	}
	var row models.ImageAIDescription
	if err := s.db.First(&row, "image_id = ?", imageID).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// RecoverInterruptedImageDescriptions 启动恢复：processing → failed/interrupted
// （镜像 recoverInterruptedRuns 的启动复位模式）。
func (s *ImageAIDescriptionService) RecoverInterruptedImageDescriptions() error {
	return s.db.Model(&models.ImageAIDescription{}).
		Where("status = ?", imageAIDescriptionStatusProcessing).
		Updates(map[string]any{
			"status":     imageAIDescriptionStatusFailed,
			"error_code": imageAIDescriptionErrorInterrupted,
			"last_error": "图片描述任务被中断",
		}).Error
}

// run 是单 worker 主循环：目标集快照在 worker 内加载，避免持锁做 O(库) 读取。
func (s *ImageAIDescriptionService) run(ctx context.Context, config AITaggingConfig, client ImageDescriptionClient) {
	defer s.worker.Done()
	described := s.db.Model(&models.ImageAIDescription{}).Select("image_id").
		Where("status IN ?", []string{imageAIDescriptionStatusCompleted, imageAIDescriptionStatusProcessing})
	var targets []models.Image
	if err := s.db.WithContext(ctx).Model(&models.Image{}).Select("id", "name").
		Where("id NOT IN (?)", described).Order("id ASC").Find(&targets).Error; err != nil {
		if ctx.Err() == nil {
			s.updateStatus(func(status *ImageAIDescriptionStatus) {
				status.Failed++
				appendImageAIDescriptionFailure(status, ImageAIDescriptionFailure{Code: "image_list_failed", Error: boundedError(err, imageAIDescriptionErrorRuneLimit)})
			})
		}
		s.finish(ctx.Err() != nil)
		return
	}
	s.updateStatus(func(status *ImageAIDescriptionStatus) { status.Total = len(targets) })
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		s.updateStatus(func(status *ImageAIDescriptionStatus) { status.CurrentImageID = target.ID })
		// 任务中被软删/硬删的图片跳过（设计 4.6.4；硬删残留行由 CASCADE 清理）。
		var img models.Image
		if err := s.db.WithContext(ctx).First(&img, target.ID).Error; err != nil {
			if ctx.Err() != nil {
				break
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				s.updateStatus(func(status *ImageAIDescriptionStatus) { status.Processed++; status.Skipped++ })
				continue
			}
			s.recordImageFailure(target.ID, target.Name, "image_load_failed", err)
			continue
		}
		code, execErr := s.executeOne(ctx, config, client, img)
		switch code {
		case "":
			s.updateStatus(func(status *ImageAIDescriptionStatus) { status.Processed++; status.Succeeded++ })
		case imageAIDescriptionCodeCancelled:
			// 当前图片已回退 pending，停止批量。
		case imageAIDescriptionErrorDecodeUnsupported:
			// 缩略图不可得：留痕 failed/decode_unsupported 后按"跳过"计数，批量继续。
			message := boundedError(execErr, imageAIDescriptionErrorRuneLimit)
			s.updateStatus(func(status *ImageAIDescriptionStatus) {
				status.Processed++
				status.Skipped++
				appendImageAIDescriptionFailure(status, ImageAIDescriptionFailure{ImageID: img.ID, Name: img.Name, Code: code, Error: message})
			})
		default:
			s.updateStatus(func(status *ImageAIDescriptionStatus) {
				status.Processed++
				status.Failed++
				appendImageAIDescriptionFailure(status, ImageAIDescriptionFailure{ImageID: img.ID, Name: img.Name, Code: code, Error: boundedError(execErr, imageAIDescriptionErrorRuneLimit)})
			})
		}
		if code == imageAIDescriptionCodeCancelled {
			break
		}
	}
	s.finish(ctx.Err() != nil)
}

// executeOne 处理单张图片：置 processing（attempt_count++）→ 取缩略图 JPEG →
// 调 client → 脱敏截断落库。返回空串表示成功；imageAIDescriptionCodeCancelled
// 表示已取消且当前行已回退 pending；其他返回值为已落库的 error_code。
func (s *ImageAIDescriptionService) executeOne(ctx context.Context, config AITaggingConfig, client ImageDescriptionClient, img models.Image) (string, error) {
	if err := s.markProcessing(ctx, img.ID); err != nil {
		if ctx.Err() != nil {
			return imageAIDescriptionCodeCancelled, ctx.Err()
		}
		s.markFailed(img.ID, imageAIDescriptionErrorPersistFailed, err)
		return imageAIDescriptionErrorPersistFailed, err
	}
	media, err := s.thumbnails.ResolveImageThumbnail(ctx, img.ID)
	if err != nil {
		if ctx.Err() != nil {
			s.rollbackPending(img.ID)
			return imageAIDescriptionCodeCancelled, ctx.Err()
		}
		s.markFailed(img.ID, imageAIDescriptionErrorDecodeUnsupported, err)
		return imageAIDescriptionErrorDecodeUnsupported, err
	}
	jpegData, err := os.ReadFile(media.Path)
	if err != nil {
		if ctx.Err() != nil {
			s.rollbackPending(img.ID)
			return imageAIDescriptionCodeCancelled, ctx.Err()
		}
		s.markFailed(img.ID, imageAIDescriptionErrorDecodeUnsupported, err)
		return imageAIDescriptionErrorDecodeUnsupported, err
	}
	// AC-14：外发前必须剥除元数据。sips 生成的 HEIC/RAW 缩略图会原样保留源图 EXIF
	// （含 GPS），剥不掉就不外发。
	jpegData, err = StripJPEGMetadataForUpload(jpegData)
	if err != nil {
		s.markFailed(img.ID, imageAIDescriptionErrorMetadataStrip, err)
		return imageAIDescriptionErrorMetadataStrip, err
	}
	content, err := client.Describe(ctx, img.ID, imageAIDescriptionUserPrompt, jpegData)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			s.rollbackPending(img.ID)
			return imageAIDescriptionCodeCancelled, err
		}
		log.Printf("[ImageAIDescription] describe failed image_id=%d err=%v", img.ID, err)
		s.markFailed(img.ID, imageAIDescriptionErrorRequestFailed, err)
		return imageAIDescriptionErrorRequestFailed, err
	}
	// 落库前脱敏（API key、绝对路径，复用语义索引脱敏），再做 4000 runes 截断。
	description := truncateRunes(sanitizeSemanticIndexText(content, config.APIKey), imageAIDescriptionMaxRunes)
	if description == "" {
		err := errors.New("图片描述响应为空")
		s.markFailed(img.ID, imageAIDescriptionErrorEmptyResponse, err)
		return imageAIDescriptionErrorEmptyResponse, err
	}
	modelIdentifier := strings.TrimSpace(config.Model)
	if key := strings.TrimSpace(config.APIKey); key != "" {
		modelIdentifier = strings.ReplaceAll(modelIdentifier, key, "[redacted-secret]")
	}
	if err := s.markCompleted(img.ID, description, modelIdentifier); err != nil {
		s.markFailed(img.ID, imageAIDescriptionErrorPersistFailed, err)
		return imageAIDescriptionErrorPersistFailed, err
	}
	return "", nil
}

// markProcessing upsert 单图状态行：status=processing 且 attempt_count 自增。
func (s *ImageAIDescriptionService) markProcessing(ctx context.Context, imageID uint) error {
	row := models.ImageAIDescription{
		ImageID: imageID, Status: imageAIDescriptionStatusProcessing, AttemptCount: 1,
	}
	return s.db.WithContext(ctx).Omit(clause.Associations).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "image_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"status": imageAIDescriptionStatusProcessing,
			// Postgres 的 ON CONFLICT DO UPDATE 中，SET 右侧不带限定的列名是歧义的
			// （既可能指目标表列、也可能指 excluded 行），会直接报 42702 导致整批失败。
			// 用 excluded.attempt_count 显式取"本次 INSERT 试图写入的值"做自增，
			// 与表名无关，SQLite/Postgres 均成立。
			"attempt_count": gorm.Expr("excluded.attempt_count + 1"),
			"updated_at":    s.now(),
		}),
	}).Create(&row).Error
}

// markCompleted 覆盖式写入成功结果；旧描述与失败留痕一并清理。
func (s *ImageAIDescriptionService) markCompleted(imageID uint, description, modelIdentifier string) error {
	now := s.now()
	return s.db.Model(&models.ImageAIDescription{}).Where("image_id = ?", imageID).
		Updates(map[string]any{
			"status":           imageAIDescriptionStatusCompleted,
			"description":      description,
			"model_identifier": modelIdentifier,
			"generated_at":     &now,
			"error_code":       "",
			"last_error":       "",
		}).Error
}

// markFailed 写失败留痕（error_code + 有界 last_error）；保留旧描述便于回看。
func (s *ImageAIDescriptionService) markFailed(imageID uint, code string, cause error) {
	if err := s.db.Model(&models.ImageAIDescription{}).Where("image_id = ?", imageID).
		Updates(map[string]any{
			"status":     imageAIDescriptionStatusFailed,
			"error_code": code,
			"last_error": boundedError(cause, imageAIDescriptionErrorRuneLimit),
		}).Error; err != nil {
		log.Printf("[ImageAIDescription] 写失败留痕失败 image_id=%d code=%s err=%v", imageID, code, err)
	}
}

// rollbackPending 取消时把当前图片从 processing 回退为 pending（设计 4.6.4）。
func (s *ImageAIDescriptionService) rollbackPending(imageID uint) {
	if err := s.db.Model(&models.ImageAIDescription{}).
		Where("image_id = ? AND status = ?", imageID, imageAIDescriptionStatusProcessing).
		Update("status", imageAIDescriptionStatusPending).Error; err != nil {
		log.Printf("[ImageAIDescription] 取消回退失败 image_id=%d err=%v", imageID, err)
	}
}

func (s *ImageAIDescriptionService) recordImageFailure(imageID uint, name, code string, cause error) {
	s.markFailed(imageID, code, cause)
	message := boundedError(cause, imageAIDescriptionErrorRuneLimit)
	s.updateStatus(func(status *ImageAIDescriptionStatus) {
		status.Processed++
		status.Failed++
		appendImageAIDescriptionFailure(status, ImageAIDescriptionFailure{ImageID: imageID, Name: name, Code: code, Error: message})
	})
}

func (s *ImageAIDescriptionService) updateStatus(update func(*ImageAIDescriptionStatus)) {
	s.mu.Lock()
	update(&s.status)
	now := s.now()
	s.status.UpdatedAt = &now
	status, emitter := cloneImageAIDescriptionStatus(s.status), s.emitter
	s.mu.Unlock()
	emitImageAIDescriptionStatus(emitter, status)
}

func (s *ImageAIDescriptionService) finish(cancelled bool) {
	s.mu.Lock()
	s.status.Running = false
	s.status.Cancelled = cancelled
	s.status.Completed = !cancelled
	s.status.CurrentImageID = 0
	now := s.now()
	s.status.UpdatedAt = &now
	s.cancel = nil
	s.busy = false
	status, emitter := cloneImageAIDescriptionStatus(s.status), s.emitter
	s.mu.Unlock()
	emitImageAIDescriptionStatus(emitter, status)
}

func appendImageAIDescriptionFailure(status *ImageAIDescriptionStatus, failure ImageAIDescriptionFailure) {
	if len(status.Failures) < imageAIDescriptionMaxFailures {
		status.Failures = append(status.Failures, failure)
	}
}

func cloneImageAIDescriptionStatus(status ImageAIDescriptionStatus) ImageAIDescriptionStatus {
	status.Failures = append([]ImageAIDescriptionFailure(nil), status.Failures...)
	return status
}

func emitImageAIDescriptionStatus(emitter func(ImageAIDescriptionStatus), status ImageAIDescriptionStatus) {
	if emitter != nil {
		emitter(status)
	}
}
