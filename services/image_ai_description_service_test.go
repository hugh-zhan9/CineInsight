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
	"gorm.io/gorm/clause"
)

type imageDescriptionClientFunc func(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error)

func (f imageDescriptionClientFunc) Describe(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
	return f(ctx, imageID, prompt, jpegData)
}

type imageAIDescriptionTestProvider struct {
	config AITaggingConfig
	err    error
}

func (p imageAIDescriptionTestProvider) Load() (AITaggingConfig, error) {
	return p.config, p.err
}

const (
	imageAIDescriptionTestAPIKey = "secret-key-123"
	imageAIDescriptionTestModel  = "vl-model"
)

func setupImageAIDescriptionTestDB(t *testing.T) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "image_ai_description_test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	database.DB = db
}

// imageAIDescriptionTestJPEG 生成一张真实可解码的小 JPEG，充当缩略图产物。
func imageAIDescriptionTestJPEG(t *testing.T) []byte {
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

// newImageAIDescriptionTestThumbnails 返回不依赖 ffmpeg/sips 的缩略图服务：
// heic 走注入的 convert runner，直接写出真实 JPEG。
func newImageAIDescriptionTestThumbnails(t *testing.T) *ImageThumbnailService {
	t.Helper()
	svc := NewImageThumbnailService(t.TempDir())
	jpegData := imageAIDescriptionTestJPEG(t)
	svc.SetDecodeRunnersForTest(
		func(ctx context.Context, sourcePath, destinationPath string, maxEdge int) error {
			return os.WriteFile(destinationPath, jpegData, 0644)
		},
		func(ctx context.Context, sourcePath string) (int, int, error) { return 8, 6, nil },
	)
	return svc
}

func newImageAIDescriptionTestService(t *testing.T, client ImageDescriptionClient) *ImageAIDescriptionService {
	t.Helper()
	setupImageAIDescriptionTestDB(t)
	provider := imageAIDescriptionTestProvider{config: AITaggingConfig{
		BaseURL: "http://ai.test", APIKey: imageAIDescriptionTestAPIKey, Model: imageAIDescriptionTestModel,
	}}
	svc := NewImageAIDescriptionService(database.DB, newImageAIDescriptionTestThumbnails(t), provider)
	svc.clientFactory = func(AITaggingConfig) ImageDescriptionClient { return client }
	return svc
}

func imageAIDescriptionTestImage(t *testing.T, format string) *models.Image {
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

func waitImageAIDescription(t *testing.T, svc *ImageAIDescriptionService) ImageAIDescriptionStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := svc.GetImageAIDescriptionStatus()
		if !status.Running {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("图片描述 worker 未停止: %+v", svc.GetImageAIDescriptionStatus())
	return ImageAIDescriptionStatus{}
}

func imageAIDescriptionRow(t *testing.T, imageID uint) models.ImageAIDescription {
	t.Helper()
	var row models.ImageAIDescription
	if err := database.DB.First(&row, "image_id = ?", imageID).Error; err != nil {
		t.Fatalf("读取描述行失败 image_id=%d: %v", imageID, err)
	}
	return row
}

func TestImageAIDescriptionBatchSuccessWritesCompletedRow(t *testing.T) {
	var mu sync.Mutex
	var gotPrompt string
	var gotJPEGBytes int
	client := imageDescriptionClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
		mu.Lock()
		gotPrompt, gotJPEGBytes = prompt, len(jpegData)
		mu.Unlock()
		return "画面中是一只橘猫趴在窗台上晒太阳，背景是安静的居民楼，整体色调温暖，风格贴近日常生活抓拍。", nil
	})
	svc := newImageAIDescriptionTestService(t, client)
	img := imageAIDescriptionTestImage(t, "heic")

	var emitMu sync.Mutex
	var emitted []ImageAIDescriptionStatus
	svc.SetEventEmitter(func(status ImageAIDescriptionStatus) {
		emitMu.Lock()
		emitted = append(emitted, status)
		emitMu.Unlock()
	})

	if _, err := svc.StartImageAIDescription(context.Background()); err != nil {
		t.Fatalf("启动批量描述失败: %v", err)
	}
	status := waitImageAIDescription(t, svc)
	if !status.Completed || status.Total != 1 || status.Succeeded != 1 || status.Failed != 0 || status.Processed != 1 {
		t.Fatalf("批量状态异常: %+v", status)
	}

	row := imageAIDescriptionRow(t, img.ID)
	if row.Status != "completed" || row.ErrorCode != "" || row.LastError != "" {
		t.Fatalf("描述行状态异常: %+v", row)
	}
	if !strings.Contains(row.Description, "橘猫") {
		t.Fatalf("描述内容异常: %q", row.Description)
	}
	if row.ModelIdentifier != imageAIDescriptionTestModel {
		t.Fatalf("model_identifier = %q", row.ModelIdentifier)
	}
	if row.GeneratedAt == nil || row.AttemptCount != 1 {
		t.Fatalf("generated_at/attempt_count 异常: %+v", row)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotJPEGBytes == 0 {
		t.Fatalf("客户端未收到缩略图 JPEG 字节")
	}
	for _, expected := range []string{"80", "200", "中文", "画面内容", "场景", "风格", "标签", "JSON", "Markdown"} {
		if !strings.Contains(gotPrompt, expected) {
			t.Errorf("提示词缺少 %q: %s", expected, gotPrompt)
		}
	}

	emitMu.Lock()
	defer emitMu.Unlock()
	if len(emitted) == 0 || !emitted[len(emitted)-1].Completed {
		t.Fatalf("进度事件缺失或末态异常: %d", len(emitted))
	}
}

func TestImageAIDescriptionEmptyResponseAndOverlongTruncation(t *testing.T) {
	var emptyImageID uint
	client := imageDescriptionClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
		if imageID == emptyImageID {
			return "   ", nil
		}
		return strings.Repeat("图", 4500), nil
	})
	svc := newImageAIDescriptionTestService(t, client)
	imgEmpty := imageAIDescriptionTestImage(t, "heic")
	imgLong := imageAIDescriptionTestImage(t, "heic")
	emptyImageID = imgEmpty.ID

	if _, err := svc.StartImageAIDescription(context.Background()); err != nil {
		t.Fatalf("启动批量描述失败: %v", err)
	}
	status := waitImageAIDescription(t, svc)
	if status.Succeeded != 1 || status.Failed != 1 {
		t.Fatalf("批量状态异常: %+v", status)
	}

	rowEmpty := imageAIDescriptionRow(t, imgEmpty.ID)
	if rowEmpty.Status != "failed" || rowEmpty.ErrorCode != "empty_response" {
		t.Fatalf("空响应留痕异常: %+v", rowEmpty)
	}
	rowLong := imageAIDescriptionRow(t, imgLong.ID)
	if rowLong.Status != "completed" {
		t.Fatalf("超长响应应截断后完成: %+v", rowLong)
	}
	if runeCount := len([]rune(rowLong.Description)); runeCount != 4000 {
		t.Fatalf("截断后 rune 数 = %d", runeCount)
	}
}

func TestImageAIDescriptionRequestFailureLeavesRetryableTrace(t *testing.T) {
	failing := imageDescriptionClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
		return "", errors.New(strings.Repeat("x", 6000))
	})
	svc := newImageAIDescriptionTestService(t, failing)
	img := imageAIDescriptionTestImage(t, "heic")

	if _, err := svc.StartImageAIDescription(context.Background()); err != nil {
		t.Fatalf("启动批量描述失败: %v", err)
	}
	status := waitImageAIDescription(t, svc)
	if status.Failed != 1 || len(status.Failures) != 1 || status.Failures[0].Code != "request_failed" {
		t.Fatalf("失败状态异常: %+v", status)
	}
	row := imageAIDescriptionRow(t, img.ID)
	if row.Status != "failed" || row.ErrorCode != "request_failed" || row.AttemptCount != 1 {
		t.Fatalf("失败留痕异常: %+v", row)
	}
	if len(row.LastError) != 1000 {
		t.Fatalf("last_error 未做有界截断: len=%d", len(row.LastError))
	}

	// failed 行属于可重试目标集：第二次 Start 用成功客户端覆盖为 completed。
	svc.clientFactory = func(AITaggingConfig) ImageDescriptionClient {
		return imageDescriptionClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
			return "夜色下的城市街道，霓虹灯映在湿润的路面上，行人稀少，整体风格偏冷色调的纪实摄影。", nil
		})
	}
	if _, err := svc.StartImageAIDescription(context.Background()); err != nil {
		t.Fatalf("重试启动失败: %v", err)
	}
	retry := waitImageAIDescription(t, svc)
	if retry.Succeeded != 1 || retry.Total != 1 {
		t.Fatalf("重试状态异常: %+v", retry)
	}
	row = imageAIDescriptionRow(t, img.ID)
	if row.Status != "completed" || row.AttemptCount != 2 || row.ErrorCode != "" || row.LastError != "" {
		t.Fatalf("重试后行异常: %+v", row)
	}
}

func TestImageAIDescriptionDecodeUnsupportedSkipsAndBatchContinues(t *testing.T) {
	client := imageDescriptionClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
		return "山间清晨的薄雾笼罩着松林，光线柔和，画面风格宁静自然。", nil
	})
	svc := newImageAIDescriptionTestService(t, client)
	imgUnsupported := imageAIDescriptionTestImage(t, "bmp") // 解码矩阵未收录 → ErrImageDecodeUnsupported
	imgOK := imageAIDescriptionTestImage(t, "heic")

	if _, err := svc.StartImageAIDescription(context.Background()); err != nil {
		t.Fatalf("启动批量描述失败: %v", err)
	}
	status := waitImageAIDescription(t, svc)
	if status.Total != 2 || status.Processed != 2 || status.Skipped != 1 || status.Succeeded != 1 || status.Failed != 0 {
		t.Fatalf("批量状态异常: %+v", status)
	}
	if len(status.Failures) != 1 || status.Failures[0].Code != "decode_unsupported" || status.Failures[0].ImageID != imgUnsupported.ID {
		t.Fatalf("decode_unsupported 留痕异常: %+v", status.Failures)
	}

	rowUnsupported := imageAIDescriptionRow(t, imgUnsupported.ID)
	if rowUnsupported.Status != "failed" || rowUnsupported.ErrorCode != "decode_unsupported" {
		t.Fatalf("decode_unsupported 行异常: %+v", rowUnsupported)
	}
	rowOK := imageAIDescriptionRow(t, imgOK.ID)
	if rowOK.Status != "completed" {
		t.Fatalf("批量未继续处理后续图片: %+v", rowOK)
	}
}

func TestImageAIDescriptionCancelRollsBackCurrentToPending(t *testing.T) {
	entered := make(chan struct{}, 1)
	client := imageDescriptionClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return "", ctx.Err()
	})
	svc := newImageAIDescriptionTestService(t, client)
	img := imageAIDescriptionTestImage(t, "heic")

	if _, err := svc.StartImageAIDescription(context.Background()); err != nil {
		t.Fatalf("启动批量描述失败: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("客户端未被调用")
	}
	if err := svc.CancelImageAIDescription(); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	status := waitImageAIDescription(t, svc)
	if !status.Cancelled || status.Completed {
		t.Fatalf("取消状态异常: %+v", status)
	}
	row := imageAIDescriptionRow(t, img.ID)
	if row.Status != "pending" {
		t.Fatalf("取消后当前图片未回退 pending: %+v", row)
	}
}

// 后台批量会自动开跑且可能跑很久；用户点某一张的"重新生成"要能立刻插进去，
// 而不是被批量顶回来。批量的目标集是"没有描述的图"，重跑针对"已有描述的图"，
// 两者本来就不重叠，只需要行级认领防住罕见的重叠。
func TestImageAIDescriptionRegenerateRunsAlongsideRunningBatch(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	var calls int32
	client := imageDescriptionClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// 第一次调用属于批量：卡住它，制造"批量正在跑"的窗口。
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
		}
		return "湖面平静如镜，远处雪山与晚霞相接，画面开阔，风格偏风光摄影。", nil
	})
	svc := newImageAIDescriptionTestService(t, client)
	batchTarget := imageAIDescriptionTestImage(t, "heic")
	// 另建一张已有描述的图：它不在批量目标集里，正是"重新生成"的典型对象。
	regenTarget := imageAIDescriptionTestImage(t, "heic")
	if err := database.DB.Create(&models.ImageAIDescription{
		ImageID: regenTarget.ID, Status: "completed", Description: "旧描述",
	}).Error; err != nil {
		t.Fatalf("创建既有描述失败: %v", err)
	}

	if _, err := svc.StartImageAIDescription(context.Background()); err != nil {
		t.Fatalf("启动批量描述失败: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("客户端未被调用")
	}
	// 批量之间仍然互斥：重复 Start 必须被拒绝。
	if _, err := svc.StartImageAIDescription(context.Background()); !errors.Is(err, ErrImageAIDescriptionBusy) {
		t.Fatalf("重复 Start 未被拒绝: %v", err)
	}

	// 批量还卡在第一张上，单张重跑应当立刻完成。
	done := make(chan error, 1)
	go func() {
		_, err := svc.RegenerateImageAIDescription(regenTarget.ID)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("批量运行中单张重跑应当成功: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("批量运行中单张重跑被阻塞了")
	}
	if row := imageAIDescriptionRow(t, regenTarget.ID); row.Description == "旧描述" {
		t.Fatalf("重跑应覆盖旧描述，实际 %+v", row)
	}

	close(release)
	status := waitImageAIDescription(t, svc)
	if status.Succeeded != 1 {
		t.Fatalf("批量结束状态异常: %+v", status)
	}
	if batchTarget.ID == 0 {
		t.Fatal("批量目标图片未创建")
	}
}

// 同一张图不允许并发重跑两次，避免同一行双写。
func TestImageAIDescriptionRejectsConcurrentRegenerateOfSameImage(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	client := imageDescriptionClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return "湖面平静如镜，远处雪山与晚霞相接，画面开阔，风格偏风光摄影。", nil
	})
	svc := newImageAIDescriptionTestService(t, client)
	img := imageAIDescriptionTestImage(t, "heic")

	done := make(chan error, 1)
	go func() {
		_, err := svc.RegenerateImageAIDescription(img.ID)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("客户端未被调用")
	}
	if _, err := svc.RegenerateImageAIDescription(img.ID); !errors.Is(err, ErrImageAIDescriptionBusy) {
		t.Fatalf("同一张图的并发重跑应被拒绝: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("首个重跑失败: %v", err)
	}
}

func TestImageAIDescriptionRejectsWhenConfigUnavailable(t *testing.T) {
	setupImageAIDescriptionTestDB(t)
	thumbnails := newImageAIDescriptionTestThumbnails(t)

	loadErr := imageAIDescriptionTestProvider{err: fmt.Errorf("AI tagging config unavailable")}
	svc := NewImageAIDescriptionService(database.DB, thumbnails, loadErr)
	if _, err := svc.StartImageAIDescription(context.Background()); !errors.Is(err, ErrImageAIDescriptionConfigUnavailable) {
		t.Fatalf("未配置时 Start 未被拒绝: %v", err)
	}
	if _, err := svc.RegenerateImageAIDescription(1); !errors.Is(err, ErrImageAIDescriptionConfigUnavailable) {
		t.Fatalf("未配置时 Regenerate 未被拒绝: %v", err)
	}

	emptyModel := imageAIDescriptionTestProvider{config: AITaggingConfig{BaseURL: "http://ai.test"}}
	svc = NewImageAIDescriptionService(database.DB, thumbnails, emptyModel)
	if _, err := svc.StartImageAIDescription(context.Background()); !errors.Is(err, ErrImageAIDescriptionConfigUnavailable) {
		t.Fatalf("Model 为空时 Start 未被拒绝: %v", err)
	}
}

func TestImageAIDescriptionRecoverInterruptedResetsProcessing(t *testing.T) {
	client := imageDescriptionClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
		return "", errors.New("unused")
	})
	svc := newImageAIDescriptionTestService(t, client)
	img := imageAIDescriptionTestImage(t, "heic")
	seed := models.ImageAIDescription{ImageID: img.ID, Status: "processing", AttemptCount: 2}
	if err := database.DB.Omit(clause.Associations).Create(&seed).Error; err != nil {
		t.Fatalf("预置 processing 行失败: %v", err)
	}

	if err := svc.RecoverInterruptedImageDescriptions(); err != nil {
		t.Fatalf("启动恢复失败: %v", err)
	}
	row := imageAIDescriptionRow(t, img.ID)
	if row.Status != "failed" || row.ErrorCode != "interrupted" || row.AttemptCount != 2 {
		t.Fatalf("恢复复位异常: %+v", row)
	}
}

func TestImageAIDescriptionRegenerateOverwritesExistingDescription(t *testing.T) {
	client := imageDescriptionClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
		return "新的描述：黄昏的海滩上有人在放风筝，天空呈橙紫渐变，画面轻松惬意。", nil
	})
	svc := newImageAIDescriptionTestService(t, client)
	img := imageAIDescriptionTestImage(t, "heic")
	seed := models.ImageAIDescription{
		ImageID: img.ID, Status: "completed", Description: "旧描述",
		ModelIdentifier: "old-model", AttemptCount: 1,
	}
	if err := database.DB.Omit(clause.Associations).Create(&seed).Error; err != nil {
		t.Fatalf("预置旧描述失败: %v", err)
	}

	row, err := svc.RegenerateImageAIDescription(img.ID)
	if err != nil {
		t.Fatalf("单张重跑失败: %v", err)
	}
	if row == nil || !strings.Contains(row.Description, "新的描述") || strings.Contains(row.Description, "旧描述") {
		t.Fatalf("重跑未覆盖旧描述: %+v", row)
	}
	if row.Status != "completed" || row.ModelIdentifier != imageAIDescriptionTestModel || row.AttemptCount != 2 || row.GeneratedAt == nil {
		t.Fatalf("重跑结果字段异常: %+v", row)
	}
}

func TestImageAIDescriptionSanitizesSecretsAndPathsBeforePersist(t *testing.T) {
	client := imageDescriptionClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
		return "夜晚街景，来源 " + imageAIDescriptionTestAPIKey + " 存放于 /Users/private/photos/img.jpg 的原图。", nil
	})
	svc := newImageAIDescriptionTestService(t, client)
	img := imageAIDescriptionTestImage(t, "heic")

	if _, err := svc.StartImageAIDescription(context.Background()); err != nil {
		t.Fatalf("启动批量描述失败: %v", err)
	}
	status := waitImageAIDescription(t, svc)
	if status.Succeeded != 1 {
		t.Fatalf("批量状态异常: %+v", status)
	}
	row := imageAIDescriptionRow(t, img.ID)
	for _, forbidden := range []string{imageAIDescriptionTestAPIKey, "/Users/private"} {
		if strings.Contains(row.Description, forbidden) {
			t.Errorf("落库描述包含敏感内容 %q: %s", forbidden, row.Description)
		}
	}
	for _, expected := range []string{"[redacted-secret]", "[redacted-path]", "夜晚街景"} {
		if !strings.Contains(row.Description, expected) {
			t.Errorf("落库描述缺少 %q: %s", expected, row.Description)
		}
	}
}

func TestImageAIDescriptionSoftDeletedImageExcludedFromTargets(t *testing.T) {
	client := imageDescriptionClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
		return "室内书桌一角，暖黄台灯下摊开的笔记本与钢笔，风格安静温馨。", nil
	})
	svc := newImageAIDescriptionTestService(t, client)
	active := imageAIDescriptionTestImage(t, "heic")
	deleted := imageAIDescriptionTestImage(t, "heic")
	if err := database.DB.Delete(&models.Image{}, deleted.ID).Error; err != nil {
		t.Fatalf("软删图片失败: %v", err)
	}

	if _, err := svc.StartImageAIDescription(context.Background()); err != nil {
		t.Fatalf("启动批量描述失败: %v", err)
	}
	status := waitImageAIDescription(t, svc)
	if status.Total != 1 || status.Succeeded != 1 {
		t.Fatalf("软删图片进入了目标集: %+v", status)
	}
	if row := imageAIDescriptionRow(t, active.ID); row.Status != "completed" {
		t.Fatalf("活跃图片未完成: %+v", row)
	}
	var count int64
	if err := database.DB.Model(&models.ImageAIDescription{}).Where("image_id = ?", deleted.ID).Count(&count).Error; err != nil {
		t.Fatalf("统计软删图片描述行失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("软删图片被写入描述行: %d", count)
	}
}

func TestImageAIDescriptionStartWithNoTargetsCompletes(t *testing.T) {
	client := imageDescriptionClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
		t.Errorf("空目标集不应调用客户端")
		return "", nil
	})
	svc := newImageAIDescriptionTestService(t, client)
	if _, err := svc.StartImageAIDescription(context.Background()); err != nil {
		t.Fatalf("空目标集启动失败: %v", err)
	}
	status := waitImageAIDescription(t, svc)
	if !status.Completed || status.Total != 0 || status.Processed != 0 {
		t.Fatalf("空目标集状态异常: %+v", status)
	}
}

func TestImageAIDescriptionClientBuildsOpenAICompatibleRequest(t *testing.T) {
	var emptyChoices bool
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
		if len(payload.Messages) != 2 || payload.Messages[0].Role != "system" || payload.Messages[1].Role != "user" {
			t.Fatalf("messages 结构异常: %+v", payload.Messages)
		}
		var systemPrompt string
		if err := json.Unmarshal(payload.Messages[0].Content, &systemPrompt); err != nil || !strings.Contains(systemPrompt, "禁止") {
			t.Errorf("system 提示异常: %q err=%v", systemPrompt, err)
		}
		var parts []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if err := json.Unmarshal(payload.Messages[1].Content, &parts); err != nil || len(parts) != 2 {
			t.Fatalf("user content 结构异常: %v %d", err, len(parts))
		}
		if parts[0].Type != "text" || parts[0].Text != "描述提示" {
			t.Errorf("文本部分异常: %+v", parts[0])
		}
		if parts[1].Type != "image_url" || !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/jpeg;base64,") {
			t.Errorf("图片部分异常: %+v", parts[1])
		}
		writer.Header().Set("Content-Type", "application/json")
		if emptyChoices {
			_, _ = writer.Write([]byte(`{"choices":[]}`))
			return
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"一段中文描述"}}]}`))
	}))
	defer server.Close()

	client := NewOpenAICompatibleImageDescriptionClient(AITaggingConfig{
		BaseURL: server.URL, APIKey: "test-key", Model: "vl-model",
	})
	content, err := client.Describe(context.Background(), 7, "描述提示", []byte{1, 2, 3})
	if err != nil || content != "一段中文描述" {
		t.Fatalf("Describe 返回异常: %q %v", content, err)
	}

	emptyChoices = true
	content, err = client.Describe(context.Background(), 7, "描述提示", []byte{1, 2, 3})
	if err != nil || content != "" {
		t.Fatalf("空 choices 应返回空内容: %q %v", content, err)
	}
}

func TestImageAIDescriptionChatCompletionsURLJoins(t *testing.T) {
	cases := map[string]string{
		"http://host":                         "http://host/v1/chat/completions",
		"http://host/":                        "http://host/v1/chat/completions",
		"http://host/v1":                      "http://host/v1/chat/completions",
		"http://host/v1/chat/completions":     "http://host/v1/chat/completions",
		"  http://host/v1/chat/completions/ ": "http://host/v1/chat/completions",
	}
	for input, expected := range cases {
		if got := imageAIDescriptionChatCompletionsURL(input); got != expected {
			t.Errorf("URL 拼接 %q = %q, 期望 %q", input, got, expected)
		}
	}
}
