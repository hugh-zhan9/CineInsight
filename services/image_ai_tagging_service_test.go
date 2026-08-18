package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type imageTaggingClientFunc func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error)

func (f imageTaggingClientFunc) AnalyzeImageTags(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
	return f(ctx, imageID, prompt, jpegData)
}

type imageAITaggingTestProvider struct {
	config AITaggingConfig
	err    error
}

func (p imageAITaggingTestProvider) Load() (AITaggingConfig, error) {
	return p.config, p.err
}

const (
	imageAITaggingTestAPIKey = "secret-key-123"
	imageAITaggingTestModel  = "vl-model"
)

func setupImageAITaggingTestDB(t *testing.T) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "image_ai_tagging_test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	// 部分唯一索引 AutoMigrate 建不出来，测试库要和生产建同一套，
	// 否则并发兜底约束在测试里根本不存在。
	database.EnsureImageAITaggingIndexes(db)
	database.DB = db
}

// imageAITaggingTestJPEG 生成一张真实可解码的小 JPEG，充当缩略图产物。
func imageAITaggingTestJPEG(t *testing.T) []byte {
	t.Helper()
	canvas := image.NewGray(image.Rect(0, 0, 8, 6))
	for i := range canvas.Pix {
		canvas.Pix[i] = uint8(i * 7)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, nil); err != nil {
		t.Fatalf("编码测试 JPEG 失败: %v", err)
	}
	return buf.Bytes()
}

// newImageAITaggingTestThumbnails 返回不依赖 ffmpeg/sips 的缩略图服务：
// 注入的 convert runner 直接写出给定的 JPEG 字节。
func newImageAITaggingTestThumbnails(t *testing.T, jpegData []byte) *ImageThumbnailService {
	t.Helper()
	svc := NewImageThumbnailService(t.TempDir())
	svc.SetDecodeRunnersForTest(
		func(ctx context.Context, sourcePath, destinationPath string, maxEdge int) error {
			return os.WriteFile(destinationPath, jpegData, 0644)
		},
		func(ctx context.Context, sourcePath string) (int, int, error) { return 8, 6, nil },
	)
	return svc
}

func newImageAITaggingTestService(t *testing.T, client ImageTaggingClient) *ImageAITaggingService {
	t.Helper()
	setupImageAITaggingTestDB(t)
	return newImageAITaggingTestServiceWithThumbnailData(t, client, imageAITaggingTestJPEG(t))
}

// newImageAITaggingTestServiceWithThumbnailData 假定调用方已经建好测试库。
func newImageAITaggingTestServiceWithThumbnailData(t *testing.T, client ImageTaggingClient, jpegData []byte) *ImageAITaggingService {
	t.Helper()
	provider := imageAITaggingTestProvider{config: AITaggingConfig{
		BaseURL: "http://ai.test", APIKey: imageAITaggingTestAPIKey, Model: imageAITaggingTestModel,
	}}
	svc := NewImageAITaggingService(database.DB, newImageAITaggingTestThumbnails(t, jpegData), provider)
	svc.clientFactory = func(AITaggingConfig) ImageTaggingClient { return client }
	return svc
}

// imageAITaggingTestLibrary 建立闭合标签词表：只有 is_system && is_active 的标签才进提示词。
func imageAITaggingTestLibrary(t *testing.T, names ...string) []models.Tag {
	t.Helper()
	tags := make([]models.Tag, 0, len(names))
	for index, name := range names {
		tag := models.Tag{Name: name, Namespace: "场景", IsSystem: true, IsActive: true, SortOrder: index}
		if err := database.DB.Create(&tag).Error; err != nil {
			t.Fatalf("创建标签失败 %s: %v", name, err)
		}
		tags = append(tags, tag)
	}
	return tags
}

func imageAITaggingTestImage(t *testing.T, format string) *models.Image {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture."+format)
	if err := os.WriteFile(path, []byte("fixture"), 0644); err != nil {
		t.Fatalf("写图片夹具失败: %v", err)
	}
	img := &models.Image{Name: filepath.Base(path), Path: path, Directory: dir, Size: 7, Format: format}
	if err := database.DB.Create(img).Error; err != nil {
		t.Fatalf("创建图片记录失败: %v", err)
	}
	return img
}

func waitImageAITagging(t *testing.T, svc *ImageAITaggingService) ImageAITaggingStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := svc.GetImageAITaggingStatus()
		if !status.Running {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("图片打标 worker 未停止: %+v", svc.GetImageAITaggingStatus())
	return ImageAITaggingStatus{}
}

func imageAITaggingState(t *testing.T, imageID uint) models.ImageAITaggingState {
	t.Helper()
	var row models.ImageAITaggingState
	if err := database.DB.First(&row, "image_id = ?", imageID).Error; err != nil {
		t.Fatalf("读取打标状态行失败 image_id=%d: %v", imageID, err)
	}
	return row
}

func imageAITaggingCandidates(t *testing.T, imageID uint) []models.ImageAITagCandidate {
	t.Helper()
	var rows []models.ImageAITagCandidate
	if err := database.DB.Where("image_id = ?", imageID).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("读取候选失败 image_id=%d: %v", imageID, err)
	}
	return rows
}

// TestImageAITaggingBatchCreatesPendingCandidates 钉住 4.6.6 的核心契约：
// AI 产出的是待审标签候选，不是描述，且候选精确对应标签库里的标签。
func TestImageAITaggingBatchCreatesPendingCandidates(t *testing.T) {
	var mu sync.Mutex
	var gotPrompt string
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		mu.Lock()
		gotPrompt = prompt
		mu.Unlock()
		return []AITagSuggestion{
			{Label: "海边", Confidence: "high", Reasoning: "画面主体是海岸线"},
			{Label: "日落", Confidence: "medium", Reasoning: "天空呈橙红色"},
		}, nil
	})
	svc := newImageAITaggingTestService(t, client)
	library := imageAITaggingTestLibrary(t, "海边", "日落", "雪山")
	img := imageAITaggingTestImage(t, "heic")

	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("启动打标失败: %v", err)
	}
	status := waitImageAITagging(t, svc)
	if status.Succeeded != 1 || status.Failed != 0 || status.Candidates != 2 {
		t.Fatalf("状态不符: %+v", status)
	}

	candidates := imageAITaggingCandidates(t, img.ID)
	if len(candidates) != 2 {
		t.Fatalf("候选数 = %d", len(candidates))
	}
	for _, candidate := range candidates {
		if candidate.Status != models.AITagCandidateStatusPending {
			t.Fatalf("候选应为 pending: %+v", candidate)
		}
		if candidate.MatchedTagID == nil {
			t.Fatalf("闭合词表下候选必须命中标签库: %+v", candidate)
		}
	}
	if candidates[0].SuggestedName != "海边" || *candidates[0].MatchedTagID != library[0].ID {
		t.Fatalf("首个候选不符: %+v", candidates[0])
	}
	if candidates[1].Confidence != models.AITagConfidenceMedium {
		t.Fatalf("置信度归一化失败: %+v", candidates[1])
	}

	state := imageAITaggingState(t, img.ID)
	if state.Status != models.AITaggingStateStatusCompleted || state.EvidenceFingerprint == "" {
		t.Fatalf("打标状态不符: %+v", state)
	}

	mu.Lock()
	prompt := gotPrompt
	mu.Unlock()
	for _, name := range []string{"海边", "日落", "雪山"} {
		if !strings.Contains(prompt, name) {
			t.Fatalf("提示词缺少标签库条目 %s: %q", name, prompt)
		}
	}
}

// TestImageAITaggingDropsOutOfLibraryAndLowConfidence 钉住闭合词表：模型自造的标签
// 一律丢弃且绝不新建 tag；low 置信度不进候选。
func TestImageAITaggingDropsOutOfLibraryAndLowConfidence(t *testing.T) {
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		return []AITagSuggestion{
			{Label: "海边", Confidence: "high"},
			{Label: "模型自造的标签", Confidence: "high"},
			{Label: "日落", Confidence: "low"},
			{Label: "   ", Confidence: "high"},
		}, nil
	})
	svc := newImageAITaggingTestService(t, client)
	imageAITaggingTestLibrary(t, "海边", "日落")
	img := imageAITaggingTestImage(t, "heic")

	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("启动打标失败: %v", err)
	}
	waitImageAITagging(t, svc)

	candidates := imageAITaggingCandidates(t, img.ID)
	if len(candidates) != 1 || candidates[0].SuggestedName != "海边" {
		t.Fatalf("只应保留库内高/中置信度候选: %+v", candidates)
	}
	var tagCount int64
	if err := database.DB.Model(&models.Tag{}).Count(&tagCount).Error; err != nil {
		t.Fatalf("统计标签失败: %v", err)
	}
	if tagCount != 2 {
		t.Fatalf("闭合词表下不得新建标签，当前标签数 = %d", tagCount)
	}
}

// TestImageAITaggingSkipsWhenEvidenceFingerprintUnchanged 钉住幂等：同一张图在
// 图片本体与标签库都没变时不重复调用 AI。
func TestImageAITaggingSkipsWhenEvidenceFingerprintUnchanged(t *testing.T) {
	var calls int32
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		atomic.AddInt32(&calls, 1)
		return []AITagSuggestion{{Label: "海边", Confidence: "high"}}, nil
	})
	svc := newImageAITaggingTestService(t, client)
	imageAITaggingTestLibrary(t, "海边")
	img := imageAITaggingTestImage(t, "heic")

	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("首次启动失败: %v", err)
	}
	waitImageAITagging(t, svc)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("首轮调用次数 = %d", got)
	}

	// 目标集只排除 processing，所以这张已 completed 的图会再次进入循环；
	// 真正拦住重复调用的是证据指纹，不是状态过滤。
	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("二次启动失败: %v", err)
	}
	status := waitImageAITagging(t, svc)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("证据未变时不应再次调用 AI，实际调用 %d 次", got)
	}
	if status.Total != 1 || status.Skipped != 1 {
		t.Fatalf("已完成的图应仍进目标集并计入跳过: %+v", status)
	}
	firstFingerprint := imageAITaggingState(t, img.ID).EvidenceFingerprint

	// 标签库变化会改变证据指纹，此时应重新调用——这正是视频侧做不到的那件事。
	imageAITaggingTestLibrary(t, "日落")
	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("三次启动失败: %v", err)
	}
	waitImageAITagging(t, svc)
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("标签库变化后应重新调用，实际调用 %d 次", got)
	}
	if after := imageAITaggingState(t, img.ID); after.EvidenceFingerprint == firstFingerprint {
		t.Fatalf("标签库变化后证据指纹应改变: %q", after.EvidenceFingerprint)
	}
}

// TestImageAITaggingSkipsAlreadyTaggedAndEmptyLibrary 覆盖两条不发请求的跳过路径。
func TestImageAITaggingSkipsAlreadyTaggedAndEmptyLibrary(t *testing.T) {
	var calls int32
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		atomic.AddInt32(&calls, 1)
		return nil, nil
	})
	svc := newImageAITaggingTestService(t, client)
	img := imageAITaggingTestImage(t, "heic")

	// 标签库为空：跳过且不发请求。
	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	waitImageAITagging(t, svc)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("空标签库不应发起请求，实际 %d 次", got)
	}
	if state := imageAITaggingState(t, img.ID); state.SkipReason != imageAITaggingSkipEmptyTagLibrary {
		t.Fatalf("跳过原因不符: %+v", state)
	}

	// 已有手工标签：即使标签库非空也跳过。
	imageAITaggingTestLibrary(t, "海边")
	manual := models.Tag{Name: "我自己打的"}
	if err := database.DB.Create(&manual).Error; err != nil {
		t.Fatalf("创建手工标签失败: %v", err)
	}
	if err := database.DB.Exec("INSERT INTO image_tags(image_id, tag_id) VALUES (?, ?)", img.ID, manual.ID).Error; err != nil {
		t.Fatalf("关联手工标签失败: %v", err)
	}
	if err := database.DB.Model(&models.ImageAITaggingState{}).
		Where("image_id = ?", img.ID).Update("status", models.AITaggingStateStatusPending).Error; err != nil {
		t.Fatalf("重置状态失败: %v", err)
	}
	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("二次启动失败: %v", err)
	}
	waitImageAITagging(t, svc)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("已手工打标不应发起请求，实际 %d 次", got)
	}
	if state := imageAITaggingState(t, img.ID); state.SkipReason != imageAITaggingSkipAlreadyTagged {
		t.Fatalf("跳过原因不符: %+v", state)
	}
}

// TestImageAITaggingCancelRollsBackCurrentToPending 钉住取消语义：当前图回到可重跑状态。
func TestImageAITaggingCancelRollsBackCurrentToPending(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return nil, ctx.Err()
	})
	svc := newImageAITaggingTestService(t, client)
	imageAITaggingTestLibrary(t, "海边")
	img := imageAITaggingTestImage(t, "heic")

	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker 未进入 AI 调用")
	}
	if err := svc.CancelImageAITagging(); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	close(release)
	status := waitImageAITagging(t, svc)
	if !status.Cancelled {
		t.Fatalf("状态应标记为已取消: %+v", status)
	}
	if state := imageAITaggingState(t, img.ID); state.Status != models.AITaggingStateStatusPending {
		t.Fatalf("取消后当前图应回退 pending: %+v", state)
	}
}

// TestImageAITaggingRejectsWhenConfigUnavailable 钉住配置缺失时的显式拒绝。
func TestImageAITaggingRejectsWhenConfigUnavailable(t *testing.T) {
	setupImageAITaggingTestDB(t)
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		t.Fatal("配置不可用时不应调用 AI")
		return nil, nil
	})
	for name, config := range map[string]AITaggingConfig{
		"缺 BaseURL": {Model: imageAITaggingTestModel},
		"缺 Model":   {BaseURL: "http://ai.test"},
	} {
		svc := NewImageAITaggingService(database.DB, newImageAITaggingTestThumbnails(t, imageAITaggingTestJPEG(t)), imageAITaggingTestProvider{config: config})
		svc.clientFactory = func(AITaggingConfig) ImageTaggingClient { return client }
		if _, err := svc.StartImageAITagging(context.Background()); !errors.Is(err, ErrImageAITaggingConfigUnavailable) {
			t.Fatalf("%s 应返回配置不可用，实际 %v", name, err)
		}
		if _, err := svc.RetagImage(1); !errors.Is(err, ErrImageAITaggingConfigUnavailable) {
			t.Fatalf("%s 单张重跑应返回配置不可用，实际 %v", name, err)
		}
	}
}

// TestImageAITaggingSendsOnlyStrippedThumbnailAndPrompt 是本切片的安全断言：
// 外发内容只有提示词与缩略图，且缩略图里的 EXIF/GPS 已被剥掉。
func TestImageAITaggingSendsOnlyStrippedThumbnailAndPrompt(t *testing.T) {
	setupImageAITaggingTestDB(t)
	withEXIF := exifFixtureJPEG(t)
	if !exifBytesPresent(withEXIF) || !gpsRationalBytesPresent(withEXIF) {
		t.Fatal("夹具本身应含 EXIF 与 GPS，否则这个测试没有意义")
	}

	var mu sync.Mutex
	var sentJPEG []byte
	var sentPrompt string
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		mu.Lock()
		sentJPEG = append([]byte{}, jpegData...)
		sentPrompt = prompt
		mu.Unlock()
		return []AITagSuggestion{{Label: "海边", Confidence: "high"}}, nil
	})
	svc := newImageAITaggingTestServiceWithThumbnailData(t, client, withEXIF)
	imageAITaggingTestLibrary(t, "海边")
	img := imageAITaggingTestImage(t, "heic")

	if _, err := svc.RetagImage(img.ID); err != nil {
		t.Fatalf("单张打标失败: %v", err)
	}

	mu.Lock()
	payload, prompt := sentJPEG, sentPrompt
	mu.Unlock()
	if len(payload) == 0 {
		t.Fatal("未捕获到外发图像")
	}
	if exifBytesPresent(payload) || gpsRationalBytesPresent(payload) {
		t.Fatal("外发图像仍含 EXIF/GPS")
	}
	if _, err := jpeg.Decode(bytes.NewReader(payload)); err != nil {
		t.Fatalf("剥除后外发图像不可解码: %v", err)
	}
	// 提示词里不得出现图片的绝对路径、目录、文件名，也不得出现 API key。
	for _, forbidden := range []string{img.Path, img.Directory, img.Name, imageAITaggingTestAPIKey} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("提示词泄漏了 %q: %q", forbidden, prompt)
		}
	}
}

// TestImageAITaggingPromptExcludesInactiveLibraryTags 钉住词表口径：
// 只有 is_system && is_active 的标签才进提示词。用 TagService.GetAITagLibrary 的口径
// （只过滤 is_system）会把停用标签一起喂给模型。
func TestImageAITaggingPromptExcludesInactiveLibraryTags(t *testing.T) {
	var mu sync.Mutex
	var gotPrompt string
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		mu.Lock()
		gotPrompt = prompt
		mu.Unlock()
		return nil, nil
	})
	svc := newImageAITaggingTestService(t, client)
	library := imageAITaggingTestLibrary(t, "海边", "已停用的标签")
	if err := database.DB.Model(&models.Tag{}).Where("id = ?", library[1].ID).
		Update("is_active", false).Error; err != nil {
		t.Fatalf("停用标签失败: %v", err)
	}
	imageAITaggingTestImage(t, "heic")

	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	waitImageAITagging(t, svc)

	mu.Lock()
	prompt := gotPrompt
	mu.Unlock()
	if !strings.Contains(prompt, "海边") {
		t.Fatalf("启用的标签应进提示词: %q", prompt)
	}
	if strings.Contains(prompt, "已停用的标签") {
		t.Fatalf("停用的标签不应进提示词: %q", prompt)
	}
}

// TestImageAITaggingFingerprintIgnoresCosmeticTagChanges 钉住 F3：
// 指纹只认提示词里真正出现的内容。改颜色不该让整库图片重新发一遍 AI。
func TestImageAITaggingFingerprintIgnoresCosmeticTagChanges(t *testing.T) {
	var calls int32
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		atomic.AddInt32(&calls, 1)
		return []AITagSuggestion{{Label: "海边", Confidence: "high"}}, nil
	})
	svc := newImageAITaggingTestService(t, client)
	library := imageAITaggingTestLibrary(t, "海边")
	imageAITaggingTestImage(t, "heic")

	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("首轮启动失败: %v", err)
	}
	waitImageAITagging(t, svc)

	// 改颜色不影响提示词内容，不应触发重打。
	if err := database.DB.Model(&models.Tag{}).Where("id = ?", library[0].ID).
		Update("color", "#ff0000").Error; err != nil {
		t.Fatalf("改标签颜色失败: %v", err)
	}
	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("二轮启动失败: %v", err)
	}
	waitImageAITagging(t, svc)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("改颜色不该触发重打，实际调用 %d 次", got)
	}

	// 改标签名会改变提示词，必须触发重打。
	if err := database.DB.Model(&models.Tag{}).Where("id = ?", library[0].ID).
		Update("name", "海滨").Error; err != nil {
		t.Fatalf("改标签名失败: %v", err)
	}
	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("三轮启动失败: %v", err)
	}
	waitImageAITagging(t, svc)
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("改标签名应触发重打，实际调用 %d 次", got)
	}
}

// TestImageAITaggingRefusesToSendWhenStripFails 钉住 fail-closed：剥不掉就一个字节都不外发。
func TestImageAITaggingRefusesToSendWhenStripFails(t *testing.T) {
	setupImageAITaggingTestDB(t)
	called := false
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		called = true
		return nil, nil
	})
	svc := newImageAITaggingTestServiceWithThumbnailData(t, client, []byte("definitely-not-a-jpeg"))
	imageAITaggingTestLibrary(t, "海边")
	img := imageAITaggingTestImage(t, "heic")

	if _, err := svc.RetagImage(img.ID); err == nil {
		t.Fatal("剥除失败时应报错")
	}
	if called {
		t.Fatal("剥除失败后仍然发起了外发请求")
	}
	state := imageAITaggingState(t, img.ID)
	if state.Status != models.AITaggingStateStatusFailed || !strings.Contains(state.LastError, imageAITaggingErrorMetadataStrip) {
		t.Fatalf("失败留痕不符: %+v", state)
	}
}

// TestImageAITaggingRetagBypassesFingerprint 钉住单张重跑的语义：用户显式要求重来就真的重来。
func TestImageAITaggingRetagBypassesFingerprint(t *testing.T) {
	var calls int32
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		atomic.AddInt32(&calls, 1)
		return []AITagSuggestion{{Label: "海边", Confidence: "high"}}, nil
	})
	svc := newImageAITaggingTestService(t, client)
	imageAITaggingTestLibrary(t, "海边")
	img := imageAITaggingTestImage(t, "heic")

	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	waitImageAITagging(t, svc)

	candidates, err := svc.RetagImage(img.ID)
	if err != nil {
		t.Fatalf("重跑失败: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("重跑应绕过指纹判定，实际调用 %d 次", got)
	}
	if len(candidates) != 1 || candidates[0].SuggestedName != "海边" {
		t.Fatalf("重跑返回的候选不符: %+v", candidates)
	}
	// 重打会把上一轮的待审候选置 superseded 再写新的：待审列表里始终只有一条"海边"，
	// 旧的那条留作审计痕迹而不是继续占着待审位。
	all := imageAITaggingCandidates(t, img.ID)
	pendingCount, supersededCount := 0, 0
	for _, item := range all {
		switch item.Status {
		case models.AITagCandidateStatusPending:
			pendingCount++
		case models.AITagCandidateStatusSuperseded:
			supersededCount++
		}
	}
	if pendingCount != 1 || supersededCount != 1 {
		t.Fatalf("重打后应是 1 条待审 + 1 条 superseded，实际 %+v", all)
	}
}

// TestImageAITaggingRecoverInterruptedResetsProcessing 钉住启动复位。
func TestImageAITaggingRecoverInterruptedResetsProcessing(t *testing.T) {
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		return nil, nil
	})
	svc := newImageAITaggingTestService(t, client)
	img := imageAITaggingTestImage(t, "heic")
	if err := database.DB.Create(&models.ImageAITaggingState{
		ImageID: img.ID, Status: models.AITaggingStateStatusProcessing,
	}).Error; err != nil {
		t.Fatalf("构造 processing 行失败: %v", err)
	}

	if err := svc.RecoverInterruptedImageTagging(); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	state := imageAITaggingState(t, img.ID)
	if state.Status != models.AITaggingStateStatusFailed || !strings.Contains(state.LastError, imageAITaggingErrorInterrupted) {
		t.Fatalf("中断复位不符: %+v", state)
	}
}

// TestImageAITaggingRequestFailureLeavesRetryableTrace 钉住失败留痕可重跑。
func TestImageAITaggingRequestFailureLeavesRetryableTrace(t *testing.T) {
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		return nil, fmt.Errorf("上游 500")
	})
	svc := newImageAITaggingTestService(t, client)
	imageAITaggingTestLibrary(t, "海边")
	img := imageAITaggingTestImage(t, "heic")

	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	status := waitImageAITagging(t, svc)
	if status.Failed != 1 || len(status.Failures) != 1 {
		t.Fatalf("状态应记录一次失败: %+v", status)
	}
	if status.Failures[0].Code != imageAITaggingErrorRequestFailed {
		t.Fatalf("失败码不符: %+v", status.Failures[0])
	}
	state := imageAITaggingState(t, img.ID)
	if state.Status != models.AITaggingStateStatusFailed {
		t.Fatalf("状态行应为 failed: %+v", state)
	}
	if state.AttemptCount < 1 {
		t.Fatalf("失败也应记一次尝试: %+v", state)
	}
}

// TestImageAITaggingRetriesAfterFailure 钉住"失败可重跑"：一次上游抖动不能把这张图钉死。
// 证据指纹判定只认 completed，所以 failed 的图下一轮仍会被重新发出去。
func TestImageAITaggingRetriesAfterFailure(t *testing.T) {
	var calls int32
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return nil, fmt.Errorf("上游 500")
		}
		return []AITagSuggestion{{Label: "海边", Confidence: "high"}}, nil
	})
	svc := newImageAITaggingTestService(t, client)
	imageAITaggingTestLibrary(t, "海边")
	img := imageAITaggingTestImage(t, "heic")

	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("首轮启动失败: %v", err)
	}
	waitImageAITagging(t, svc)
	if imageAITaggingState(t, img.ID).Status != models.AITaggingStateStatusFailed {
		t.Fatal("首轮应失败")
	}

	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("二轮启动失败: %v", err)
	}
	waitImageAITagging(t, svc)
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("失败的图下一轮应被重试，实际调用 %d 次", got)
	}
	state := imageAITaggingState(t, img.ID)
	if state.Status != models.AITaggingStateStatusCompleted {
		t.Fatalf("重试后应完成: %+v", state)
	}
	// attempt_count 必须真的自增。写成 excluded.attempt_count + 1 会恒等于 2，
	// 让"第 2 次"和"第 40 次"无法区分。
	if state.AttemptCount != 2 {
		t.Fatalf("两次尝试后 attempt_count 应为 2，实际 %d", state.AttemptCount)
	}
}

// TestImageAITaggingClientBuildsOpenAICompatibleRequest 覆盖真实客户端的请求构造与响应解析。
func TestImageAITaggingClientBuildsOpenAICompatibleRequest(t *testing.T) {
	var content string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("请求路径 = %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		var payload struct {
			Model       string  `json:"model"`
			Temperature float64 `json:"temperature"`
			Messages    []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("解析请求失败: %v", err)
		}
		if payload.Model != "vl-model" || payload.Temperature != 0.1 {
			t.Errorf("model/temperature 异常: %q %v", payload.Model, payload.Temperature)
		}
		// 这是 httptest handler 的 goroutine：t.Fatalf 会直接 Goexit 且不写响应，
		// 客户端只会看到一个莫名其妙的传输错误而不是这条信息。必须用 Errorf + return。
		if len(payload.Messages) != 2 || payload.Messages[0].Role != "system" || payload.Messages[1].Role != "user" {
			t.Errorf("messages 结构异常: %+v", payload.Messages)
			return
		}
		var parts []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if err := json.Unmarshal(payload.Messages[1].Content, &parts); err != nil || len(parts) != 2 {
			t.Errorf("user content 结构异常: %v %d", err, len(parts))
			return
		}
		if parts[0].Type != "text" || !strings.Contains(parts[0].Text, "海边") {
			t.Errorf("文本部分应包含标签库: %+v", parts[0])
		}
		if parts[1].Type != "image_url" || !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/jpeg;base64,") {
			t.Errorf("图片部分异常: %+v", parts[1])
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(content))
	}))
	defer server.Close()

	client := NewOpenAICompatibleImageTaggingClient(AITaggingConfig{
		BaseURL: server.URL, APIKey: "test-key", Model: "vl-model",
	})
	prompt := buildImageAITaggingPrompt([]models.Tag{{ID: 1, Name: "海边", Namespace: "场景", IsSystem: true, IsActive: true}})

	// 代码块围栏包裹的 JSON 也要能解析（复用视频侧的 normalizeAITaggingJSONContent）。
	content = "{\"choices\":[{\"message\":{\"content\":\"```json\\n{\\\"suggestions\\\":[{\\\"label\\\":\\\"海边\\\",\\\"confidence\\\":\\\"high\\\"}]}\\n```\"}}]}"
	suggestions, err := client.AnalyzeImageTags(context.Background(), 7, prompt, []byte{1, 2, 3})
	if err != nil || len(suggestions) != 1 || suggestions[0].Label != "海边" {
		t.Fatalf("解析建议失败: %+v %v", suggestions, err)
	}

	// 无 choices 等价于"没有建议"，不是传输错误。
	content = `{"choices":[]}`
	suggestions, err = client.AnalyzeImageTags(context.Background(), 7, prompt, []byte{1, 2, 3})
	if err != nil || len(suggestions) != 0 {
		t.Fatalf("空 choices 应返回空建议: %+v %v", suggestions, err)
	}
}

// TestImageAITaggingApprovedTagsDoNotBlockFutureRuns 钉住 F1：
// AI 接受过的标签不算"手工标签"。用 hasNonAutomaticTags 判定会让图片在接受一个候选之后
// 永久停在 skipped/already_tagged，正好废掉「标签库变了就重新评估」这条改进。
func TestImageAITaggingApprovedTagsDoNotBlockFutureRuns(t *testing.T) {
	var calls int32
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		atomic.AddInt32(&calls, 1)
		return []AITagSuggestion{{Label: "海边", Confidence: "high"}}, nil
	})
	svc := newImageAITaggingTestService(t, client)
	imageAITaggingTestLibrary(t, "海边")
	img := imageAITaggingTestImage(t, "heic")

	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("首轮启动失败: %v", err)
	}
	waitImageAITagging(t, svc)

	candidates := imageAITaggingCandidates(t, img.ID)
	if len(candidates) != 1 {
		t.Fatalf("首轮应产出 1 条候选，实际 %d", len(candidates))
	}
	if _, err := svc.ApproveImageAITagCandidate(candidates[0].ID); err != nil {
		t.Fatalf("接受候选失败: %v", err)
	}

	// 标签库扩充后，这张图必须被重新评估，而不是因为"已有标签"被跳过。
	imageAITaggingTestLibrary(t, "日落")
	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("二轮启动失败: %v", err)
	}
	waitImageAITagging(t, svc)

	state := imageAITaggingState(t, img.ID)
	if state.SkipReason == imageAITaggingSkipAlreadyTagged {
		t.Fatal("AI 接受过的标签被误判成了手工标签")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("接受候选后仍应能重新打标，实际调用 %d 次", got)
	}

	// 反过来：真正的手工标签仍然要挡住打标。
	manual := models.Tag{Name: "我自己打的"}
	if err := database.DB.Create(&manual).Error; err != nil {
		t.Fatalf("创建手工标签失败: %v", err)
	}
	if err := database.DB.Exec("INSERT INTO image_tags(image_id, tag_id) VALUES (?, ?)", img.ID, manual.ID).Error; err != nil {
		t.Fatalf("关联手工标签失败: %v", err)
	}
	imageAITaggingTestLibrary(t, "雪山")
	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("三轮启动失败: %v", err)
	}
	waitImageAITagging(t, svc)
	if imageAITaggingState(t, img.ID).SkipReason != imageAITaggingSkipAlreadyTagged {
		t.Fatal("真正的手工标签应挡住打标")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("手工打标后不该再调用 AI，实际调用 %d 次", got)
	}
}

// TestImageAITaggingDoesNotResurrectRejectedCandidates 钉住 F4a：
// 拒绝就是"这张图不要这个标签"，词表一变就把它塞回待审列表等于让用户反复拒同一个东西。
func TestImageAITaggingDoesNotResurrectRejectedCandidates(t *testing.T) {
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		return []AITagSuggestion{{Label: "海边", Confidence: "high"}}, nil
	})
	svc := newImageAITaggingTestService(t, client)
	imageAITaggingTestLibrary(t, "海边")
	img := imageAITaggingTestImage(t, "heic")

	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("首轮启动失败: %v", err)
	}
	waitImageAITagging(t, svc)

	candidates := imageAITaggingCandidates(t, img.ID)
	if len(candidates) != 1 {
		t.Fatalf("首轮应产出 1 条候选，实际 %d", len(candidates))
	}
	if err := svc.RejectImageAITagCandidate(candidates[0].ID); err != nil {
		t.Fatalf("拒绝候选失败: %v", err)
	}

	// 词表变化触发重打，模型仍然给出"海边"——但用户已经拒过了。
	imageAITaggingTestLibrary(t, "日落")
	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("二轮启动失败: %v", err)
	}
	waitImageAITagging(t, svc)

	items, err := svc.ListImageAITagCandidates(img.ID, "", "")
	if err != nil {
		t.Fatalf("列出候选失败: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("被拒绝过的标签不应重新出现在待审列表: %+v", items)
	}
}

// TestImageAITaggingSupersedesStalePendingCandidates 钉住 F4b：
// 模型这轮不再建议的标签不该继续挂在待审列表里等人处理。
func TestImageAITaggingSupersedesStalePendingCandidates(t *testing.T) {
	var round int32
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		if atomic.AddInt32(&round, 1) == 1 {
			return []AITagSuggestion{{Label: "海边", Confidence: "high"}}, nil
		}
		return []AITagSuggestion{{Label: "雪山", Confidence: "high"}}, nil
	})
	svc := newImageAITaggingTestService(t, client)
	imageAITaggingTestLibrary(t, "海边", "雪山")
	img := imageAITaggingTestImage(t, "heic")

	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("首轮启动失败: %v", err)
	}
	waitImageAITagging(t, svc)

	imageAITaggingTestLibrary(t, "日落")
	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("二轮启动失败: %v", err)
	}
	waitImageAITagging(t, svc)

	items, err := svc.ListImageAITagCandidates(img.ID, "", "")
	if err != nil {
		t.Fatalf("列出候选失败: %v", err)
	}
	if len(items) != 1 || items[0].SuggestedName != "雪山" {
		t.Fatalf("待审列表应只剩本轮建议的标签: %+v", items)
	}
	var superseded int64
	if err := database.DB.Model(&models.ImageAITagCandidate{}).
		Where("image_id = ? AND status = ? AND normalized_name = ?", img.ID, models.AITagCandidateStatusSuperseded, normalizeAITagName("海边")).
		Count(&superseded).Error; err != nil {
		t.Fatalf("统计 superseded 失败: %v", err)
	}
	if superseded != 1 {
		t.Fatalf("上一轮的候选应被置 superseded，实际 %d 条", superseded)
	}
}

// TestImageAITaggingSkipsStaleImages 钉住 F10：路径已失效的图片不进目标集，
// 否则每轮都会在取缩略图时失败一次，刷一堆纯噪音的失败留痕。
func TestImageAITaggingSkipsStaleImages(t *testing.T) {
	var calls int32
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		atomic.AddInt32(&calls, 1)
		return nil, nil
	})
	svc := newImageAITaggingTestService(t, client)
	imageAITaggingTestLibrary(t, "海边")
	stale := imageAITaggingTestImage(t, "heic")
	if err := database.DB.Model(&models.Image{}).Where("id = ?", stale.ID).
		Update("is_stale", true).Error; err != nil {
		t.Fatalf("标记失效失败: %v", err)
	}

	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	status := waitImageAITagging(t, svc)
	if status.Total != 0 {
		t.Fatalf("失效图片不该进目标集: %+v", status)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("失效图片不该触发 AI 调用，实际 %d 次", got)
	}
}
