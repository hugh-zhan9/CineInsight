package services

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// 人脸分析（D-017、D-018、D-019、D-020）。
//
// sidecar 一律用假 worker：真 sidecar 要 190 MB 权重与一个 Python 环境，
// 而这里要钉的是"给定向量之后聚类、指纹、追加标记、清除各自怎么走"。

// ===== 夹具 =====

type fakeFaceWorker struct {
	mu        sync.Mutex
	plan      map[string][]DetectedFace
	calls     map[string]int
	total     int
	goneAfter int
	onDetect  func(requestID string)
	closed    bool
}

func newFakeFaceWorker() *fakeFaceWorker {
	return &fakeFaceWorker{plan: map[string][]DetectedFace{}, calls: map[string]int{}}
}

func (w *fakeFaceWorker) set(kind string, id uint, faces ...DetectedFace) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.plan[faceWorkerPlanKey(kind, id)] = faces
}

func (w *fakeFaceWorker) callCount(kind string, id uint) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls[faceWorkerPlanKey(kind, id)]
}

func faceWorkerPlanKey(kind string, id uint) string {
	return kind + "-" + itoa(id)
}

func itoa(value uint) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func (w *fakeFaceWorker) Detect(ctx context.Context, requestID, imagePath string) ([]DetectedFace, error) {
	if _, err := os.Stat(imagePath); err != nil {
		return nil, err
	}
	key := requestID
	if index := strings.LastIndex(requestID, "-"); index > 0 {
		key = requestID[:index]
	}
	w.mu.Lock()
	w.total++
	total := w.total
	w.calls[key]++
	hook, plan, goneAfter := w.onDetect, w.plan[key], w.goneAfter
	w.mu.Unlock()
	if hook != nil {
		hook(requestID)
	}
	if goneAfter > 0 && total > goneAfter {
		return nil, errFaceWorkerGone
	}
	return plan, nil
}

func (w *fakeFaceWorker) Close() error {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	return nil
}

// faceUnitVector 是坐标轴方向的单位向量：不同轴之间点积为 0（远低于 0.55），
// 同轴为 1（必然归并）。用它就能把阈值两侧的行为都钉住。
func faceUnitVector(axis int) []float32 {
	vector := make([]float32, faceEmbeddingDims)
	vector[axis%faceEmbeddingDims] = 1
	return vector
}

// faceMixedVector 造一条与 e_a 的相似度恰好是 weightA 的向量（两个权重需满足
// weightA² + weightB² = 1）。
func faceMixedVector(axisA, axisB int, weightA, weightB float32) []float32 {
	vector := make([]float32, faceEmbeddingDims)
	vector[axisA%faceEmbeddingDims] = weightA
	vector[axisB%faceEmbeddingDims] = weightB
	normalized, ok := normalizeFaceEmbedding(vector)
	if !ok {
		return vector
	}
	return normalized
}

func faceAt(x, y float64, quality float64, vector []float32) DetectedFace {
	return DetectedFace{BBox: faceBBox{X: x, Y: y, W: 0.2, H: 0.2}, Quality: quality, Embedding: vector}
}

func setupFaceTestDB(t *testing.T) {
	t.Helper()
	database.DB = dbtest.Open(t)
}

// writeFaceTestImage 写一张真 JPEG：裁剪图那一步会真去解码它。
func writeFaceTestImage(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, 120, 90))
	for y := 0; y < 90; y++ {
		for x := 0; x < 120; x++ {
			canvas.Set(x, y, color.RGBA{R: uint8(x * 2), G: uint8(y * 2), B: 120, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("建图片失败: %v", err)
	}
	defer file.Close()
	if err := jpeg.Encode(file, canvas, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("编码图片失败: %v", err)
	}
}

func newFaceAnalysisTestService(t *testing.T, worker FaceWorkerSession) *FaceAnalysisService {
	t.Helper()
	service := NewFaceAnalysisService(t.TempDir(), nil, nil)
	service.openSession = func(ctx context.Context) (FaceWorkerSession, error) { return worker, nil }
	// 视频抽帧换成"在临时目录里写一张真 JPEG"：真抽帧要 ffmpeg 与一个真视频文件。
	service.extractFrames = func(ctx context.Context, candidate faceMediaCandidate, dir string) ([]faceFrame, []string) {
		framePath := filepath.Join(dir, "frame-0.jpg")
		writeFaceTestImage(t, framePath)
		frameMS := int64(1000)
		return []faceFrame{{Path: framePath, FrameMS: &frameMS}}, nil
	}
	service.resolveImage = func(ctx context.Context, imageID uint) (string, error) {
		var image models.Image
		if err := database.DB.Select("id", "path").First(&image, imageID).Error; err != nil {
			return "", err
		}
		return image.Path, nil
	}
	return service
}

func seedFaceTestVideo(t *testing.T, name string) models.Video {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("video-bytes"), 0o644); err != nil {
		t.Fatalf("建视频文件失败: %v", err)
	}
	video := models.Video{Name: name, Path: path, Directory: filepath.Dir(path), Size: 11, Duration: 120}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("建视频记录失败: %v", err)
	}
	return video
}

func seedFaceTestImage(t *testing.T, name string) models.Image {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	writeFaceTestImage(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读图片失败: %v", err)
	}
	image := models.Image{Name: name, Path: path, Directory: filepath.Dir(path), Size: info.Size(), Format: "jpg"}
	if err := database.DB.Create(&image).Error; err != nil {
		t.Fatalf("建图片记录失败: %v", err)
	}
	return image
}

func seedFaceTestPerson(t *testing.T, name string, withAvatar bool) models.Person {
	t.Helper()
	person := models.Person{DisplayName: name}
	if withAvatar {
		path := filepath.Join(t.TempDir(), "avatar.jpg")
		writeFaceTestImage(t, path)
		person.AvatarPath = path
	}
	if err := database.DB.Create(&person).Error; err != nil {
		t.Fatalf("建人物失败: %v", err)
	}
	return person
}

func runFaceAnalysis(t *testing.T, service *FaceAnalysisService, scope string) FaceAnalysisStatus {
	t.Helper()
	done := make(chan FaceAnalysisStatus, 16)
	service.SetEventEmitter(func(status FaceAnalysisStatus) {
		if !status.Running {
			select {
			case done <- status:
			default:
			}
		}
	})
	if _, err := service.Start(context.Background(), scope); err != nil {
		t.Fatalf("启动人脸分析失败: %v", err)
	}
	select {
	case status := <-done:
		service.StopAndWait()
		return status
	case <-time.After(60 * time.Second):
		t.Fatal("人脸分析没有在 60 秒内结束")
	}
	return FaceAnalysisStatus{}
}

func countFaceRows(t *testing.T, model interface{}, query string, args ...interface{}) int64 {
	t.Helper()
	var count int64
	statement := database.DB.Model(model)
	if query != "" {
		statement = statement.Where(query, args...)
	}
	if err := statement.Count(&count).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	return count
}

// ===== 聚类 =====

// 同一张脸归到同一个簇，另一张脸自己开一个簇（D-017）。
func TestFaceAnalysisMergesSameFaceAndSplitsDifferentFace(t *testing.T) {
	setupFaceTestDB(t)
	first := seedFaceTestImage(t, "a.jpg")
	second := seedFaceTestImage(t, "b.jpg")
	third := seedFaceTestImage(t, "c.jpg")

	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, first.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	// 与 e0 的相似度 0.8 ≥ 0.55：同一个人的另一个角度。
	worker.set(models.FaceMediaKindImage, second.ID, faceAt(0.2, 0.2, 0.7, faceMixedVector(0, 1, 0.8, 0.6)))
	// 与 e0 正交：另一个人。
	worker.set(models.FaceMediaKindImage, third.ID, faceAt(0.3, 0.3, 0.6, faceUnitVector(2)))

	service := newFaceAnalysisTestService(t, worker)
	status := runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if !status.Completed || status.Failed != 0 {
		t.Fatalf("三张图都该分析成功: %+v", status)
	}
	if status.FacesDetected != 3 {
		t.Fatalf("应检出 3 张脸，实际 %d", status.FacesDetected)
	}
	if count := countFaceRows(t, &models.FaceCluster{}, ""); count != 2 {
		t.Fatalf("相似的两张脸应合成一个簇、另一张单独一个簇，实际簇数 %d", count)
	}

	var merged models.FaceCluster
	if err := database.DB.Where("observation_count = ?", 2).First(&merged).Error; err != nil {
		t.Fatalf("应有一个包含两条观测的簇: %v", err)
	}
	if merged.Status != models.FaceClusterStatusUnnamed {
		t.Fatalf("新簇应是未命名: %q", merged.Status)
	}
	// 簇代表取质量最高的那条观测。
	var representative models.FaceObservation
	if err := database.DB.First(&representative, *merged.RepresentativeObservationID).Error; err != nil {
		t.Fatalf("簇代表观测应存在: %v", err)
	}
	if representative.MediaID != first.ID {
		t.Fatalf("簇代表应是质量最高的观测（图 %d），实际 %d", first.ID, representative.MediaID)
	}
	// 裁剪图落在 faces 目录里，文件名就是观测 id。
	if representative.CropPath == "" {
		t.Fatal("观测应写出裁剪图")
	}
	if _, err := os.Stat(filepath.Join(service.FacesDir(), representative.CropPath)); err != nil {
		t.Fatalf("裁剪图应真的在 faces 目录里: %v", err)
	}
}

// 阈值下方的相似度不归并：0.5 < 0.55 就是两个人（阈值由 fixture 钉住，D-017）。
func TestFaceAnalysisDoesNotMergeBelowThreshold(t *testing.T) {
	setupFaceTestDB(t)
	first := seedFaceTestImage(t, "a.jpg")
	second := seedFaceTestImage(t, "b.jpg")

	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, first.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	worker.set(models.FaceMediaKindImage, second.ID, faceAt(0.2, 0.2, 0.8, faceMixedVector(0, 1, 0.5, 0.8660254)))

	service := newFaceAnalysisTestService(t, worker)
	runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if count := countFaceRows(t, &models.FaceCluster{}, ""); count != 2 {
		t.Fatalf("相似度 0.5 低于阈值 0.55，应是两个簇，实际 %d", count)
	}
}

// ===== no_face 与指纹 =====

// 没脸的媒体记一条标记观测，重跑不再解码（4.4.4）。
func TestFaceAnalysisWritesNoFaceMarkerAndSkipsUnchangedSource(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "empty.jpg")

	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, image.ID)

	service := newFaceAnalysisTestService(t, worker)
	status := runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if status.FacesDetected != 0 {
		t.Fatalf("没检出脸时不该计入人脸数: %+v", status)
	}
	var observations []models.FaceObservation
	if err := database.DB.Find(&observations).Error; err != nil {
		t.Fatalf("读观测失败: %v", err)
	}
	if len(observations) != 1 || observations[0].BBoxHash != models.FaceNoFaceBBoxHash {
		t.Fatalf("应留一条 no_face 标记观测: %+v", observations)
	}
	if observations[0].Embedding != nil {
		t.Fatal("no_face 标记观测不该带向量")
	}
	if count := countFaceRows(t, &models.FaceCluster{}, ""); count != 0 {
		t.Fatalf("no_face 不该建簇，实际 %d", count)
	}

	// 第二轮：指纹没变，连 worker 都不该被叫醒。
	second := runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if second.Total != 0 {
		t.Fatalf("指纹未变时候选应为空，实际 total=%d", second.Total)
	}
	if calls := worker.callCount(models.FaceMediaKindImage, image.ID); calls != 1 {
		t.Fatalf("指纹未变不该重跑检测，实际调用 %d 次", calls)
	}
}

// 源文件变了就重算：旧观测与旧裁剪图一起清掉，不留半新半旧（D-018）。
func TestFaceAnalysisReanalyzesWhenFingerprintChanges(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")

	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, image.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	service := newFaceAnalysisTestService(t, worker)
	runFaceAnalysis(t, service, FaceAnalysisScopeImages)

	var before models.FaceObservation
	if err := database.DB.First(&before).Error; err != nil {
		t.Fatalf("读首轮观测失败: %v", err)
	}
	oldCrop := filepath.Join(service.FacesDir(), before.CropPath)
	if _, err := os.Stat(oldCrop); err != nil {
		t.Fatalf("首轮裁剪图应存在: %v", err)
	}

	// 改 mtime = 换了一份源。
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(image.Path, future, future); err != nil {
		t.Fatalf("改文件时间失败: %v", err)
	}
	second := runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if second.Total != 1 {
		t.Fatalf("指纹变化的图应重新入队，实际 total=%d", second.Total)
	}
	var observations []models.FaceObservation
	if err := database.DB.Find(&observations).Error; err != nil {
		t.Fatalf("读观测失败: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("重算后同一张图只应留一条观测，实际 %d", len(observations))
	}
	if observations[0].ID == before.ID {
		t.Fatal("旧观测应被删除后重建")
	}
	if _, err := os.Stat(oldCrop); !os.IsNotExist(err) {
		t.Fatalf("旧裁剪图应随旧观测一起删除: %v", err)
	}
}

// ===== 追加候选 =====

// 已命名的簇吸收到新观测时只标 pending，绝不自动写 video_people / image_people（D-019）。
func TestFaceAnalysisMarksAppendPendingOnlyForNamedClusters(t *testing.T) {
	setupFaceTestDB(t)
	first := seedFaceTestImage(t, "a.jpg")
	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, first.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	service := newFaceAnalysisTestService(t, worker)
	runFaceAnalysis(t, service, FaceAnalysisScopeImages)

	// 模拟用户已经给这个簇命过名（P-013 的动作，这里直接改库）。
	person := seedFaceTestPerson(t, "周迅", false)
	var cluster models.FaceCluster
	if err := database.DB.First(&cluster).Error; err != nil {
		t.Fatalf("读簇失败: %v", err)
	}
	if err := database.DB.Model(&models.FaceCluster{}).Where("id = ?", cluster.ID).
		Updates(map[string]interface{}{"status": models.FaceClusterStatusNamed, "person_id": person.ID}).Error; err != nil {
		t.Fatalf("命名簇失败: %v", err)
	}

	// 同一张脸出现在新的两张图上：一张归入已命名簇、一张是新面孔。
	second := seedFaceTestImage(t, "b.jpg")
	third := seedFaceTestImage(t, "c.jpg")
	worker.set(models.FaceMediaKindImage, second.ID, faceAt(0.2, 0.2, 0.8, faceUnitVector(0)))
	worker.set(models.FaceMediaKindImage, third.ID, faceAt(0.3, 0.3, 0.7, faceUnitVector(5)))
	runFaceAnalysis(t, service, FaceAnalysisScopeImages)

	var absorbed models.FaceObservation
	if err := database.DB.Where("media_kind = ? AND media_id = ?", models.FaceMediaKindImage, second.ID).
		First(&absorbed).Error; err != nil {
		t.Fatalf("读新观测失败: %v", err)
	}
	if absorbed.AppendStatus != models.FaceAppendStatusPending {
		t.Fatalf("归入已命名簇的新观测应标 pending，实际 %q", absorbed.AppendStatus)
	}
	if absorbed.ClusterID == nil || *absorbed.ClusterID != cluster.ID {
		t.Fatalf("新观测应归入已命名的那个簇: %+v", absorbed.ClusterID)
	}

	var fresh models.FaceObservation
	if err := database.DB.Where("media_kind = ? AND media_id = ?", models.FaceMediaKindImage, third.ID).
		First(&fresh).Error; err != nil {
		t.Fatalf("读新面孔观测失败: %v", err)
	}
	if fresh.AppendStatus != models.FaceAppendStatusNone {
		t.Fatalf("未命名簇里的观测不该标 pending，实际 %q", fresh.AppendStatus)
	}

	// 关键约束：分析这条路径一行关系都不写。
	if count := countFaceRows(t, &models.ImagePerson{}, ""); count != 0 {
		t.Fatalf("分析绝不自动写 image_people，实际 %d 行", count)
	}
	if count := countFaceRows(t, &models.VideoPerson{}, ""); count != 0 {
		t.Fatalf("分析绝不自动写 video_people，实际 %d 行", count)
	}
}

// ===== 人物种子候选 =====

// 有头像的人物给出种子向量，观测数够多的未命名簇据此生成候选（D-017）。
func TestFaceAnalysisGeneratesPersonCandidatesFromAvatarSeed(t *testing.T) {
	setupFaceTestDB(t)
	person := seedFaceTestPerson(t, "周迅", true)
	images := []models.Image{
		seedFaceTestImage(t, "a.jpg"),
		seedFaceTestImage(t, "b.jpg"),
		seedFaceTestImage(t, "c.jpg"),
	}
	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindPersonAvatar, person.ID, faceAt(0.2, 0.2, 0.95, faceUnitVector(0)))
	for index, image := range images {
		worker.set(models.FaceMediaKindImage, image.ID, faceAt(0.1, 0.1, 0.9-float64(index)*0.1, faceUnitVector(0)))
	}

	service := newFaceAnalysisTestService(t, worker)
	status := runFaceAnalysis(t, service, FaceAnalysisScopeAll)
	if status.Failed != 0 {
		t.Fatalf("不该有失败项: %+v", status)
	}
	// 头像观测不参与聚类：三张图一个簇，头像自己不建簇。
	if count := countFaceRows(t, &models.FaceCluster{}, ""); count != 1 {
		t.Fatalf("应只有一个簇（头像不参与聚类），实际 %d", count)
	}
	var avatarObservation models.FaceObservation
	if err := database.DB.Where("media_kind = ?", models.FaceMediaKindPersonAvatar).First(&avatarObservation).Error; err != nil {
		t.Fatalf("头像应留一条种子观测: %v", err)
	}
	if avatarObservation.ClusterID != nil {
		t.Fatalf("头像观测不该被归入任何簇: %+v", avatarObservation.ClusterID)
	}

	var candidates []models.FacePersonCandidate
	if err := database.DB.Find(&candidates).Error; err != nil {
		t.Fatalf("读候选失败: %v", err)
	}
	if len(candidates) != 1 || candidates[0].PersonID != person.ID {
		t.Fatalf("应给出一条人物候选: %+v", candidates)
	}
	if candidates[0].Similarity < facePersonSeedThreshold {
		t.Fatalf("候选相似度应达到阈值: %v", candidates[0].Similarity)
	}
	// 候选只是建议：一行关系都没写。
	if count := countFaceRows(t, &models.ImagePerson{}, ""); count != 0 {
		t.Fatalf("生成候选不该写 image_people，实际 %d 行", count)
	}
}

// 观测数不足 3 的簇不进候选：一两张脸的簇噪声太大（D-017）。
func TestFaceAnalysisSkipsPersonCandidatesForSmallClusters(t *testing.T) {
	setupFaceTestDB(t)
	person := seedFaceTestPerson(t, "周迅", true)
	first := seedFaceTestImage(t, "a.jpg")
	second := seedFaceTestImage(t, "b.jpg")

	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindPersonAvatar, person.ID, faceAt(0.2, 0.2, 0.95, faceUnitVector(0)))
	worker.set(models.FaceMediaKindImage, first.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	worker.set(models.FaceMediaKindImage, second.ID, faceAt(0.1, 0.1, 0.8, faceUnitVector(0)))

	service := newFaceAnalysisTestService(t, worker)
	runFaceAnalysis(t, service, FaceAnalysisScopeAll)
	if count := countFaceRows(t, &models.FacePersonCandidate{}, ""); count != 0 {
		t.Fatalf("两条观测的簇不该产生候选，实际 %d", count)
	}
}

// ===== 中断与取消 =====

// sidecar 中途没了：本轮标 interrupted，已处理的留下，重启后接着跑（4.4.2）。
func TestFaceAnalysisInterruptedRunResumesOnRestart(t *testing.T) {
	setupFaceTestDB(t)
	first := seedFaceTestImage(t, "a.jpg")
	second := seedFaceTestImage(t, "b.jpg")

	worker := newFakeFaceWorker()
	worker.goneAfter = 1
	worker.set(models.FaceMediaKindImage, first.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	worker.set(models.FaceMediaKindImage, second.ID, faceAt(0.2, 0.2, 0.8, faceUnitVector(1)))

	service := newFaceAnalysisTestService(t, worker)
	status := runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if !status.Interrupted || status.Completed {
		t.Fatalf("sidecar 退出应标 interrupted: %+v", status)
	}
	if count := countFaceRows(t, &models.FaceObservation{}, "media_id = ?", first.ID); count != 1 {
		t.Fatalf("中断前处理完的那张图应留下观测，实际 %d", count)
	}
	if count := countFaceRows(t, &models.FaceObservation{}, "media_id = ?", second.ID); count != 0 {
		t.Fatalf("中断的那张图不该留下观测，实际 %d", count)
	}

	// 换一个健康的 worker 重启：只补没做完的那一张。
	healthy := newFakeFaceWorker()
	healthy.set(models.FaceMediaKindImage, second.ID, faceAt(0.2, 0.2, 0.8, faceUnitVector(1)))
	service.openSession = func(ctx context.Context) (FaceWorkerSession, error) { return healthy, nil }
	resumed := runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if !resumed.Completed {
		t.Fatalf("续跑应正常收尾: %+v", resumed)
	}
	if resumed.Total != 1 {
		t.Fatalf("续跑只该处理剩下的一张，实际 total=%d", resumed.Total)
	}
	if count := countFaceRows(t, &models.FaceObservation{}, "media_id = ?", second.ID); count != 1 {
		t.Fatalf("续跑后第二张图应有观测，实际 %d", count)
	}
}

// 取消立刻收尾：已处理的留下，没轮到的不动。
func TestFaceAnalysisCancelStopsRun(t *testing.T) {
	setupFaceTestDB(t)
	first := seedFaceTestImage(t, "a.jpg")
	second := seedFaceTestImage(t, "b.jpg")

	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, first.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	worker.set(models.FaceMediaKindImage, second.ID, faceAt(0.2, 0.2, 0.8, faceUnitVector(1)))

	service := newFaceAnalysisTestService(t, worker)
	worker.onDetect = func(requestID string) {
		if strings.HasPrefix(requestID, faceWorkerPlanKey(models.FaceMediaKindImage, first.ID)) {
			_ = service.Cancel()
		}
	}

	status := runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if !status.Cancelled || status.Completed {
		t.Fatalf("取消后应标 cancelled: %+v", status)
	}
	if calls := worker.callCount(models.FaceMediaKindImage, second.ID); calls != 0 {
		t.Fatalf("取消之后不该继续处理后面的项，实际调用 %d 次", calls)
	}
}

// ===== 登记表与项间检查点 =====

// 任务在登记表里配对进出（D-014）：跑的时候看得到 face，结束后清空。
func TestFaceAnalysisRegistersBackgroundTask(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, image.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))

	registry := NewBackgroundTaskRegistry()
	var seen [][]string
	var seenMu sync.Mutex
	registry.SetOnChange(func(running []string) {
		seenMu.Lock()
		seen = append(seen, append([]string(nil), running...))
		seenMu.Unlock()
	})
	service := newFaceAnalysisTestService(t, worker)
	service.SetBackgroundTaskRegistry(registry)

	runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if tasks := registry.Snapshot(); len(tasks) != 0 {
		t.Fatalf("结束后登记表应清空: %v", tasks)
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	sawFace := false
	for _, snapshot := range seen {
		if len(snapshot) == 1 && snapshot[0] == string(BackgroundTaskFace) {
			sawFace = true
		}
	}
	if !sawFace {
		t.Fatalf("运行期间登记表里应出现 face: %v", seen)
	}
}

// 项间检查点（D-032）：自动路径在用户活跃时停在下一项之前，
// 「忽略空闲立即运行」之后一路跑完；显式启动不装钩子。
func TestFaceAnalysisPauseHookWaitsThenRunsAfterBypass(t *testing.T) {
	setupFaceTestDB(t)
	first := seedFaceTestImage(t, "a.jpg")
	second := seedFaceTestImage(t, "b.jpg")
	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, first.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	worker.set(models.FaceMediaKindImage, second.ID, faceAt(0.2, 0.2, 0.8, faceUnitVector(1)))

	gate := busyGate()
	registry := NewBackgroundTaskRegistry()
	registry.SetOnChange(gate.SyncRunningTasks)
	service := newFaceAnalysisTestService(t, worker)
	service.SetBackgroundTaskRegistry(registry)

	done := make(chan FaceAnalysisStatus, 8)
	service.SetEventEmitter(func(status FaceAnalysisStatus) {
		if !status.Running {
			select {
			case done <- status:
			default:
			}
		}
	})
	if _, err := service.StartWithPauseHook(context.Background(), FaceAnalysisScopeImages, gate.PauseHook(string(BackgroundTaskFace))); err != nil {
		t.Fatalf("启动人脸分析失败: %v", err)
	}
	defer service.StopAndWait()

	if tasks := registry.Snapshot(); len(tasks) != 1 || tasks[0] != string(BackgroundTaskFace) {
		t.Fatalf("运行中的任务应登记为 face: %v", tasks)
	}
	waitForFaceAnalysisStatus(t, service, func(status FaceAnalysisStatus) bool { return status.Gate.WaitingIdle })
	if status := service.Status(); status.Processed != 0 {
		t.Fatalf("等待空闲期间不该处理任何一项: %+v", status)
	}

	if err := gate.RunGatedTaskNow(string(BackgroundTaskFace)); err != nil {
		t.Fatalf("立即运行失败: %v", err)
	}
	select {
	case status := <-done:
		if status.Succeeded != 2 {
			t.Fatalf("bypass 豁免的是这一轮而不是一项，两张图都该处理完: %+v", status)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("放行后没有在 60 秒内跑完")
	}
}

func waitForFaceAnalysisStatus(t *testing.T, service *FaceAnalysisService, predicate func(FaceAnalysisStatus) bool) FaceAnalysisStatus {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status := service.Status()
		if predicate(status) {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("状态未在 30 秒内满足条件: %+v", service.Status())
	return FaceAnalysisStatus{}
}

// ===== 清除与用量 =====

// ClearFaceData 只删三张人脸表与裁剪目录，人物与两张关系表一行不动（D-020）。
func TestClearFaceDataKeepsPeopleAndRelations(t *testing.T) {
	setupFaceTestDB(t)
	person := seedFaceTestPerson(t, "周迅", false)
	video := seedFaceTestVideo(t, "movie.mp4")
	image := seedFaceTestImage(t, "a.jpg")
	if err := database.DB.Create(&models.VideoPerson{VideoID: video.ID, PersonID: person.ID}).Error; err != nil {
		t.Fatalf("建视频人物关系失败: %v", err)
	}
	if err := database.DB.Create(&models.ImagePerson{ImageID: image.ID, PersonID: person.ID}).Error; err != nil {
		t.Fatalf("建图片人物关系失败: %v", err)
	}

	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, image.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	worker.set(models.FaceMediaKindVideo, video.ID, faceAt(0.2, 0.2, 0.8, faceUnitVector(0)))
	service := newFaceAnalysisTestService(t, worker)
	runFaceAnalysis(t, service, FaceAnalysisScopeAll)

	usage, err := service.DataUsage()
	if err != nil {
		t.Fatalf("读用量失败: %v", err)
	}
	if usage.ObservationCount != 2 || usage.ClusterCount != 1 || usage.CropFileCount != 2 || usage.CropBytes <= 0 {
		t.Fatalf("用量统计不对: %+v", usage)
	}

	cleared, err := service.ClearFaceData()
	if err != nil {
		t.Fatalf("清除人脸数据失败: %v", err)
	}
	if cleared.ObservationCount != 0 || cleared.ClusterCount != 0 || cleared.CandidateCount != 0 || cleared.CropFileCount != 0 {
		t.Fatalf("清除后用量应归零: %+v", cleared)
	}
	if _, err := os.Stat(service.FacesDir()); !os.IsNotExist(err) {
		t.Fatalf("裁剪目录应被删掉: %v", err)
	}
	if count := countFaceRows(t, &models.Person{}, ""); count != 1 {
		t.Fatalf("人物不该被删，实际 %d", count)
	}
	if count := countFaceRows(t, &models.VideoPerson{}, ""); count != 1 {
		t.Fatalf("video_people 不该被删，实际 %d", count)
	}
	if count := countFaceRows(t, &models.ImagePerson{}, ""); count != 1 {
		t.Fatalf("image_people 不该被删，实际 %d", count)
	}
}

// 媒体永久删除后观测跟着走（4.4.4）：多态引用做不了数据库级联，
// 由分析开跑前的对账清掉，空掉的簇一并删除。
func TestFaceAnalysisPrunesObservationsOfDeletedMedia(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, image.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	service := newFaceAnalysisTestService(t, worker)
	runFaceAnalysis(t, service, FaceAnalysisScopeImages)

	var observation models.FaceObservation
	if err := database.DB.First(&observation).Error; err != nil {
		t.Fatalf("读观测失败: %v", err)
	}
	cropPath := filepath.Join(service.FacesDir(), observation.CropPath)

	// 软删除不算删除：图片还能从回收站恢复，观测留着。
	if err := database.DB.Delete(&models.Image{}, image.ID).Error; err != nil {
		t.Fatalf("软删除图片失败: %v", err)
	}
	if _, err := service.pruneOrphanFaceData(context.Background()); err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	if count := countFaceRows(t, &models.FaceObservation{}, ""); count != 1 {
		t.Fatalf("软删除不该清掉观测，实际 %d", count)
	}

	// 永久删除之后观测与裁剪图都要消失，空簇一并删掉。
	if err := database.DB.Unscoped().Delete(&models.Image{}, image.ID).Error; err != nil {
		t.Fatalf("永久删除图片失败: %v", err)
	}
	removed, err := service.pruneOrphanFaceData(context.Background())
	if err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	if removed != 1 {
		t.Fatalf("应清掉 1 条孤儿观测，实际 %d", removed)
	}
	if count := countFaceRows(t, &models.FaceObservation{}, ""); count != 0 {
		t.Fatalf("孤儿观测应被清掉，实际 %d", count)
	}
	if count := countFaceRows(t, &models.FaceCluster{}, ""); count != 0 {
		t.Fatalf("空掉的簇应被删除，实际 %d", count)
	}
	if _, err := os.Stat(cropPath); !os.IsNotExist(err) {
		t.Fatalf("孤儿观测的裁剪图应被删除: %v", err)
	}
}

// ===== 入口约束 =====

func TestFaceAnalysisRejectsUnknownScope(t *testing.T) {
	setupFaceTestDB(t)
	service := newFaceAnalysisTestService(t, newFakeFaceWorker())
	if _, err := service.Start(context.Background(), "everything"); !errors.Is(err, ErrFaceAnalysisScopeInvalid) {
		t.Fatalf("未知 scope 应被拒绝: %v", err)
	}
}

func TestFaceAnalysisRefusesToStartWhenRuntimeUnavailable(t *testing.T) {
	setupFaceTestDB(t)
	runtime := NewFaceRuntime(t.TempDir(), func() string { return "" })
	runtime.supported = func() bool { return true }
	service := NewFaceAnalysisService(t.TempDir(), runtime, nil)
	if _, err := service.Start(context.Background(), FaceAnalysisScopeAll); !errors.Is(err, ErrFaceRuntimeUnavailable) {
		t.Fatalf("运行时未就绪时不该启动分析: %v", err)
	}
}

// 裁剪图路由只认 faces 目录内的文件：库里被写进 `../` 也读不出去（D-020）。
func TestResolveFaceCropRejectsPathOutsideFacesDir(t *testing.T) {
	setupFaceTestDB(t)
	service := newFaceAnalysisTestService(t, newFakeFaceWorker())
	outside := filepath.Join(filepath.Dir(service.FacesDir()), "secret.jpg")
	writeFaceTestImage(t, outside)

	observation := models.FaceObservation{
		MediaKind: models.FaceMediaKindImage, MediaID: 1, SourceFingerprint: "1-1",
		BBox: "0,0,1,1", BBoxHash: "000000000000", CropPath: "../secret.jpg",
		AppendStatus: models.FaceAppendStatusNone,
	}
	if err := database.DB.Create(&observation).Error; err != nil {
		t.Fatalf("建观测失败: %v", err)
	}
	if _, err := service.ResolveFaceCrop(observation.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("越界的 crop_path 应当读不到: %v", err)
	}

	if _, err := service.ResolveFaceCrop(observation.ID + 999); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("不存在的观测应返回 not exist: %v", err)
	}
}

// 全部图像都检测失败时按失败记账，不能写 no_face 标记：
// 写了就等于把这件媒体永久排除在候选之外，源没变就再也不会重试。
func TestFaceAnalysisFailsMediaWhenEveryFrameFails(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	worker := &failingFaceWorker{err: errors.New("无法解码图像")}

	service := newFaceAnalysisTestService(t, worker)
	status := runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if status.Failed != 1 || status.Succeeded != 0 {
		t.Fatalf("应记一项失败: %+v", status)
	}
	if len(status.Failures) != 1 || !strings.Contains(status.Failures[0].Error, "无法解码图像") {
		t.Fatalf("失败原因应保留底层错误: %+v", status.Failures)
	}
	if count := countFaceRows(t, &models.FaceObservation{}, ""); count != 0 {
		t.Fatalf("检测失败不该留下任何观测（尤其不能是 no_face 标记），实际 %d", count)
	}

	// 下一轮仍然把它当候选：指纹没变，但也没有观测。
	healthy := newFakeFaceWorker()
	healthy.set(models.FaceMediaKindImage, image.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	service.openSession = func(ctx context.Context) (FaceWorkerSession, error) { return healthy, nil }
	second := runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if second.Total != 1 || second.Succeeded != 1 {
		t.Fatalf("失败过的媒体应在下一轮重试: %+v", second)
	}
}

type failingFaceWorker struct {
	err error
}

func (w *failingFaceWorker) Detect(ctx context.Context, requestID, imagePath string) ([]DetectedFace, error) {
	return nil, w.err
}

func (w *failingFaceWorker) Close() error { return nil }

// 被忽略的簇继续吸收同一张脸：否则用户忽略过的人会以新的未命名簇反复冒出来
// （requirements ID-02）。
func TestFaceAnalysisAbsorbsIntoIgnoredCluster(t *testing.T) {
	setupFaceTestDB(t)
	first := seedFaceTestImage(t, "a.jpg")
	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, first.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	service := newFaceAnalysisTestService(t, worker)
	runFaceAnalysis(t, service, FaceAnalysisScopeImages)

	var cluster models.FaceCluster
	if err := database.DB.First(&cluster).Error; err != nil {
		t.Fatalf("读簇失败: %v", err)
	}
	if err := database.DB.Model(&models.FaceCluster{}).Where("id = ?", cluster.ID).
		Update("status", models.FaceClusterStatusIgnored).Error; err != nil {
		t.Fatalf("忽略簇失败: %v", err)
	}

	second := seedFaceTestImage(t, "b.jpg")
	worker.set(models.FaceMediaKindImage, second.ID, faceAt(0.2, 0.2, 0.8, faceUnitVector(0)))
	runFaceAnalysis(t, service, FaceAnalysisScopeImages)

	if count := countFaceRows(t, &models.FaceCluster{}, ""); count != 1 {
		t.Fatalf("不该为已忽略的那张脸另起一个簇，实际簇数 %d", count)
	}
	var absorbed models.FaceObservation
	if err := database.DB.Where("media_id = ?", second.ID).First(&absorbed).Error; err != nil {
		t.Fatalf("读新观测失败: %v", err)
	}
	if absorbed.ClusterID == nil || *absorbed.ClusterID != cluster.ID {
		t.Fatalf("新观测应落进那个被忽略的簇: %+v", absorbed.ClusterID)
	}
	// 已忽略不是已命名：不该标追加候选。
	if absorbed.AppendStatus != models.FaceAppendStatusNone {
		t.Fatalf("被忽略的簇不该产生追加候选，实际 %q", absorbed.AppendStatus)
	}
}

// sidecar 起不来时整轮算失败：状态里带原因、不标 completed，
// 通知说的也是失败而不是"已分析 0 项"。
func TestFaceAnalysisSessionStartFailureIsNotCompleted(t *testing.T) {
	setupFaceTestDB(t)
	seedFaceTestImage(t, "a.jpg")
	service := newFaceAnalysisTestService(t, newFakeFaceWorker())
	service.openSession = func(ctx context.Context) (FaceWorkerSession, error) {
		return nil, errors.New("人脸 sidecar 启动超时")
	}
	notifier := &recordingFaceNotifier{}
	service.SetDesktopNotifier(notifier)

	status := runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if status.Completed {
		t.Fatalf("整轮没跑起来不该标 completed: %+v", status)
	}
	if !strings.Contains(status.LastError, "启动超时") {
		t.Fatalf("状态里应带失败原因: %q", status.LastError)
	}
	if len(notifier.titles) != 1 || !strings.Contains(notifier.titles[0], "失败") {
		t.Fatalf("通知应说失败: %+v", notifier.titles)
	}
	// 通知正文不带路径（D-020）。
	for _, body := range notifier.bodies {
		if strings.Contains(body, "/") {
			t.Fatalf("通知正文不该出现路径: %q", body)
		}
	}
}

// 正常跑完发一条完成通知，正文只有计数。
func TestFaceAnalysisNotifiesOnCompletion(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, image.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	service := newFaceAnalysisTestService(t, worker)
	notifier := &recordingFaceNotifier{}
	service.SetDesktopNotifier(notifier)

	runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if len(notifier.titles) != 1 || notifier.titles[0] != "人脸分析完成" {
		t.Fatalf("应发一条完成通知: %+v", notifier.titles)
	}
	if !strings.Contains(notifier.bodies[0], "检出 1 张人脸") {
		t.Fatalf("通知正文应带计数: %q", notifier.bodies[0])
	}
	for _, body := range notifier.bodies {
		if strings.Contains(body, "/") {
			t.Fatalf("通知正文不该出现路径: %q", body)
		}
	}
}

// 取消不发通知：那是用户自己按的。
func TestFaceAnalysisDoesNotNotifyOnCancel(t *testing.T) {
	setupFaceTestDB(t)
	first := seedFaceTestImage(t, "a.jpg")
	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, first.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	service := newFaceAnalysisTestService(t, worker)
	notifier := &recordingFaceNotifier{}
	service.SetDesktopNotifier(notifier)
	worker.onDetect = func(string) { _ = service.Cancel() }

	runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if len(notifier.titles) != 0 {
		t.Fatalf("取消不该发通知: %+v", notifier.titles)
	}
}

type recordingFaceNotifier struct {
	mu     sync.Mutex
	titles []string
	bodies []string
}

func (n *recordingFaceNotifier) Notify(title, body string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.titles = append(n.titles, title)
	n.bodies = append(n.bodies, body)
}

func (n *recordingFaceNotifier) SetBadge(string) {}

// 逐项失败的文案里不能留媒体路径（D-020）：种类与 id 已经够定位。
func TestFaceAnalysisFailureMessageScrubsMediaPath(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	worker := &failingFaceWorker{err: errors.New("无法解码图像: " + image.Path)}
	service := newFaceAnalysisTestService(t, worker)

	status := runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if len(status.Failures) != 1 {
		t.Fatalf("应有一条失败记录: %+v", status.Failures)
	}
	if strings.Contains(status.Failures[0].Error, image.Path) {
		t.Fatalf("失败文案不该包含媒体路径: %q", status.Failures[0].Error)
	}
	if !strings.Contains(status.Failures[0].Error, "无法解码图像") {
		t.Fatalf("失败文案应保留原因: %q", status.Failures[0].Error)
	}
	if status.Failures[0].MediaID != image.ID || status.Failures[0].MediaKind != models.FaceMediaKindImage {
		t.Fatalf("失败记录应带种类与 id: %+v", status.Failures[0])
	}
}

// ===== 评审回归：簇计数与幽灵簇（Important 1）=====

// 重新分析后簇的 observation_count 必须等于实际观测数：
// 只加不减会让"≥3 观测"的候选门放行只剩 1 条观测的簇，质心加权也跟着失真。
func TestFaceAnalysisRecomputesClusterCountAfterReanalysis(t *testing.T) {
	setupFaceTestDB(t)
	first := seedFaceTestImage(t, "a.jpg")
	second := seedFaceTestImage(t, "b.jpg")
	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, first.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	worker.set(models.FaceMediaKindImage, second.ID, faceAt(0.2, 0.2, 0.8, faceUnitVector(0)))

	service := newFaceAnalysisTestService(t, worker)
	runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	assertFaceClusterCountsMatchObservations(t)

	var cluster models.FaceCluster
	if err := database.DB.First(&cluster).Error; err != nil {
		t.Fatalf("读簇失败: %v", err)
	}
	if cluster.ObservationCount != 2 {
		t.Fatalf("首轮两条观测应记 2，实际 %d", cluster.ObservationCount)
	}

	// 第二张图换了一份源（指纹变化）：旧观测删除、新观测重新入簇，
	// 计数必须还是 2 而不是 3。
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(second.Path, future, future); err != nil {
		t.Fatalf("改文件时间失败: %v", err)
	}
	runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	assertFaceClusterCountsMatchObservations(t)
	if err := database.DB.First(&cluster, cluster.ID).Error; err != nil {
		t.Fatalf("读簇失败: %v", err)
	}
	if cluster.ObservationCount != 2 {
		t.Fatalf("重算后簇计数应仍为 2，实际 %d（只加不减？）", cluster.ObservationCount)
	}
}

// 一件媒体重新分析后换了簇：旧簇一条观测都不剩就必须消失，不能作为幽灵簇
// 常驻审阅面板，代表观测也要跟着重算。
func TestFaceAnalysisDropsGhostClusterWhenMediaChangesFace(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, image.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))

	service := newFaceAnalysisTestService(t, worker)
	runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	var before models.FaceCluster
	if err := database.DB.First(&before).Error; err != nil {
		t.Fatalf("读簇失败: %v", err)
	}
	// 给它挂一条人物候选，验证空簇被删时候选一起走。
	person := seedFaceTestPerson(t, "周迅", false)
	if err := database.DB.Create(&models.FacePersonCandidate{ClusterID: before.ID, PersonID: person.ID, Similarity: 0.8}).Error; err != nil {
		t.Fatalf("建候选失败: %v", err)
	}

	// 换源之后是另一张脸（正交向量）：旧簇失去唯一成员。
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(image.Path, future, future); err != nil {
		t.Fatalf("改文件时间失败: %v", err)
	}
	worker.set(models.FaceMediaKindImage, image.ID, faceAt(0.3, 0.3, 0.7, faceUnitVector(7)))
	status := runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if status.Failed != 0 {
		t.Fatalf("不该有失败项（旧簇被删后内存快照要跟着收窄）: %+v", status.Failures)
	}

	if count := countFaceRows(t, &models.FaceCluster{}, "id = ?", before.ID); count != 0 {
		t.Fatalf("失去全部观测的旧簇应被删除，实际还在")
	}
	if count := countFaceRows(t, &models.FacePersonCandidate{}, ""); count != 0 {
		t.Fatalf("空簇的人物候选应一起清掉，实际 %d", count)
	}
	if count := countFaceRows(t, &models.FaceCluster{}, ""); count != 1 {
		t.Fatalf("应只剩新脸那一个簇，实际 %d", count)
	}
	assertFaceClusterCountsMatchObservations(t)
}

// assertFaceClusterCountsMatchObservations 钉住"计数列 = 实际行数"且代表观测属于本簇。
func assertFaceClusterCountsMatchObservations(t *testing.T) {
	t.Helper()
	var clusters []models.FaceCluster
	if err := database.DB.Find(&clusters).Error; err != nil {
		t.Fatalf("读簇失败: %v", err)
	}
	for _, cluster := range clusters {
		var actual int64
		if err := database.DB.Model(&models.FaceObservation{}).Where("cluster_id = ?", cluster.ID).Count(&actual).Error; err != nil {
			t.Fatalf("统计观测失败: %v", err)
		}
		if int64(cluster.ObservationCount) != actual {
			t.Fatalf("簇 %d 的计数 %d 与实际观测数 %d 不一致", cluster.ID, cluster.ObservationCount, actual)
		}
		if actual == 0 {
			t.Fatalf("簇 %d 一条观测都没有，应当已被删除", cluster.ID)
		}
		if cluster.RepresentativeObservationID == nil {
			t.Fatalf("簇 %d 缺代表观测", cluster.ID)
		}
		var representative models.FaceObservation
		if err := database.DB.First(&representative, *cluster.RepresentativeObservationID).Error; err != nil {
			t.Fatalf("簇 %d 的代表观测读不到: %v", cluster.ID, err)
		}
		if representative.ClusterID == nil || *representative.ClusterID != cluster.ID {
			t.Fatalf("簇 %d 的代表观测不属于本簇", cluster.ID)
		}
	}
}

// ===== 评审回归：路径擦除覆盖临时帧与解码缓存（Minor 3）=====

// 失败文案里不能出现临时抽帧目录下的帧路径，也不能出现 RAW/HEIC 的解码缓存路径。
func TestFaceAnalysisFailureMessageScrubsFrameAndCachePaths(t *testing.T) {
	setupFaceTestDB(t)
	video := seedFaceTestVideo(t, "movie.mp4")
	image := seedFaceTestImage(t, "raw.dng")
	// 模拟 ResolveImageView 对 RAW/HEIC 的产物：另一个目录下的 .view.jpg 缓存。
	cacheDir := t.TempDir()
	cachePath := filepath.Join(cacheDir, "42.view.jpg")
	writeFaceTestImage(t, cachePath)

	var framePath string
	worker := &pathEchoFaceWorker{}
	service := newFaceAnalysisTestService(t, worker)
	service.extractFrames = func(ctx context.Context, candidate faceMediaCandidate, dir string) ([]faceFrame, []string) {
		framePath = filepath.Join(dir, "frame-0.jpg")
		writeFaceTestImage(t, framePath)
		frameMS := int64(1000)
		return []faceFrame{{Path: framePath, FrameMS: &frameMS}}, nil
	}
	service.resolveImage = func(ctx context.Context, imageID uint) (string, error) { return cachePath, nil }

	status := runFaceAnalysis(t, service, FaceAnalysisScopeAll)
	if status.Failed != 2 {
		t.Fatalf("视频与图片都该失败一次: %+v", status)
	}
	for _, failure := range status.Failures {
		if strings.Contains(failure.Error, "/") {
			t.Fatalf("失败文案里不该出现任何路径: %q", failure.Error)
		}
	}
	if framePath == "" {
		t.Fatal("测试夹具没有产出帧路径")
	}
	for _, failure := range status.Failures {
		if strings.Contains(failure.Error, framePath) || strings.Contains(failure.Error, cachePath) || strings.Contains(failure.Error, video.Path) || strings.Contains(failure.Error, image.Path) {
			t.Fatalf("失败文案泄漏了路径: %q", failure.Error)
		}
	}
}

// pathEchoFaceWorker 模仿 sidecar 把输入路径原样写进错误的习惯。
type pathEchoFaceWorker struct{}

func (w *pathEchoFaceWorker) Detect(ctx context.Context, requestID, imagePath string) ([]DetectedFace, error) {
	return nil, fmt.Errorf("无法解码图像: %s", imagePath)
}

func (w *pathEchoFaceWorker) Close() error { return nil }

// ===== 评审回归：裁剪图路由与临时残件（Minor 6、7）=====

// 软链接不跟随：faces 目录里出现软链时按不存在处理，读不到目录外的东西。
func TestResolveFaceCropRejectsSymlink(t *testing.T) {
	setupFaceTestDB(t)
	service := newFaceAnalysisTestService(t, newFakeFaceWorker())
	if err := os.MkdirAll(service.FacesDir(), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	secret := filepath.Join(filepath.Dir(service.FacesDir()), "secret.jpg")
	writeFaceTestImage(t, secret)
	link := filepath.Join(service.FacesDir(), "9.jpg")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatalf("建软链失败: %v", err)
	}

	observation := models.FaceObservation{
		MediaKind: models.FaceMediaKindImage, MediaID: 1, SourceFingerprint: "1-1",
		FrameMS: faceFrameSentinel(), BBox: "0,0,1,1", BBoxHash: "000000999999",
		CropPath: "9.jpg", AppendStatus: models.FaceAppendStatusNone,
	}
	if err := database.DB.Create(&observation).Error; err != nil {
		t.Fatalf("建观测失败: %v", err)
	}
	if _, err := service.ResolveFaceCrop(observation.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("软链接应被当作不存在: %v", err)
	}
}

// 裁剪图的临时残件不算用量，且下一轮分析开始时被清扫。
func TestFaceAnalysisSweepsCropTempLeftovers(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, image.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	service := newFaceAnalysisTestService(t, worker)
	if err := os.MkdirAll(service.FacesDir(), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	leftover := filepath.Join(service.FacesDir(), ".face-1234.jpg")
	if err := os.WriteFile(leftover, []byte("half-written"), 0o600); err != nil {
		t.Fatalf("造残件失败: %v", err)
	}

	usage, err := service.DataUsage()
	if err != nil {
		t.Fatalf("读用量失败: %v", err)
	}
	if usage.CropFileCount != 0 || usage.CropBytes != 0 {
		t.Fatalf("临时残件不该计入用量: %+v", usage)
	}

	runFaceAnalysis(t, service, FaceAnalysisScopeImages)
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("分析开始时应清掉临时残件: %v", err)
	}
	after, err := service.DataUsage()
	if err != nil {
		t.Fatalf("读用量失败: %v", err)
	}
	if after.CropFileCount != 1 {
		t.Fatalf("清扫后应只剩这一轮写出的裁剪图，实际 %d", after.CropFileCount)
	}
}

// ===== 评审回归：帧位置哨兵值（Minor 5）=====

// 图片、头像与 no_face 标记都不写 NULL：唯一键里有 NULL 就等于没有约束。
func TestFaceObservationsUseFrameSentinelInsteadOfNull(t *testing.T) {
	setupFaceTestDB(t)
	person := seedFaceTestPerson(t, "周迅", true)
	withFace := seedFaceTestImage(t, "a.jpg")
	empty := seedFaceTestImage(t, "b.jpg")
	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindPersonAvatar, person.ID, faceAt(0.2, 0.2, 0.9, faceUnitVector(0)))
	worker.set(models.FaceMediaKindImage, withFace.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	worker.set(models.FaceMediaKindImage, empty.ID)

	service := newFaceAnalysisTestService(t, worker)
	runFaceAnalysis(t, service, FaceAnalysisScopeAll)

	var nullRows int64
	if err := database.DB.Model(&models.FaceObservation{}).Where("frame_ms IS NULL").Count(&nullRows).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if nullRows != 0 {
		t.Fatalf("不该有 frame_ms 为 NULL 的观测，实际 %d 行", nullRows)
	}
	var observations []models.FaceObservation
	if err := database.DB.Where("media_kind <> ?", models.FaceMediaKindVideo).Find(&observations).Error; err != nil {
		t.Fatalf("读观测失败: %v", err)
	}
	if len(observations) != 3 {
		t.Fatalf("应有头像、有脸的图、无脸的图三条观测，实际 %d", len(observations))
	}
	for _, observation := range observations {
		if observation.FrameMS == nil || *observation.FrameMS != models.FaceFrameMSNone {
			t.Fatalf("非视频观测的帧位置应是哨兵值: %+v", observation.FrameMS)
		}
	}

	// 唯一键这时才真正生效：同一张图的同一个位置插不进第二条。
	duplicate := observations[0]
	duplicate.ID = 0
	if err := database.DB.Create(&duplicate).Error; err == nil {
		t.Fatalf("图片观测的重复行应被唯一键拒绝(%s)", dbtest.Backend())
	}
}

// ===== 评审回归：数据目录未解析时不在相对路径上动手（#4 PARTIAL）=====

// dataDir 为空（NewApp 的 dataDirErr 路径）时，facesDir 会被拼成相对路径 "faces"，
// RemoveAll/MkdirAll 于是落在进程 cwd 上。所有会删会写的入口必须明确报错，
// 并且 cwd 下不许冒出 faces/ 或 face-runtime/。
func TestFaceServicesRefuseToTouchRelativePathsWhenDataDirMissing(t *testing.T) {
	setupFaceTestDB(t)
	cwd := t.TempDir()
	restore := chdirForFaceTest(t, cwd)
	defer restore()

	runtime := NewFaceRuntime("", func() string { return "" })
	runtime.supported = func() bool { return true }
	service := NewFaceAnalysisService("", runtime, nil)

	if service.FacesDir() != "" {
		t.Fatalf("数据目录为空时不该拼出裁剪图目录: %q", service.FacesDir())
	}
	if runtime.RuntimeDir() != "" {
		t.Fatalf("数据目录为空时不该拼出运行时目录: %q", runtime.RuntimeDir())
	}

	// 造一个同名目录放在 cwd 下：真出问题的话它会被删掉/被写进去。
	bait := filepath.Join(cwd, facesDirName)
	if err := os.MkdirAll(bait, 0o755); err != nil {
		t.Fatalf("造诱饵目录失败: %v", err)
	}
	baitFile := filepath.Join(bait, "keep-me.jpg")
	if err := os.WriteFile(baitFile, []byte("keep"), 0o600); err != nil {
		t.Fatalf("造诱饵文件失败: %v", err)
	}

	if _, err := service.ClearFaceData(); !errors.Is(err, ErrFaceDataDirUnavailable) {
		t.Fatalf("ClearFaceData 应明确拒绝: %v", err)
	}
	if _, err := service.DataUsage(); !errors.Is(err, ErrFaceDataDirUnavailable) {
		t.Fatalf("DataUsage 应明确拒绝: %v", err)
	}
	if _, err := service.Start(context.Background(), FaceAnalysisScopeAll); !errors.Is(err, ErrFaceDataDirUnavailable) {
		t.Fatalf("Start 应明确拒绝: %v", err)
	}
	if _, err := runtime.Prepare(context.Background()); !errors.Is(err, ErrFaceDataDirUnavailable) {
		t.Fatalf("PrepareFaceRuntime 应明确拒绝: %v", err)
	}
	if err := runtime.ensureVenv(context.Background()); !errors.Is(err, ErrFaceDataDirUnavailable) {
		t.Fatalf("ensureVenv 应明确拒绝: %v", err)
	}
	if err := runtime.installModels(context.Background()); !errors.Is(err, ErrFaceDataDirUnavailable) {
		t.Fatalf("installModels 应明确拒绝: %v", err)
	}
	if err := runtime.writeWorkerScript(); !errors.Is(err, ErrFaceDataDirUnavailable) {
		t.Fatalf("writeWorkerScript 应明确拒绝: %v", err)
	}
	if _, _, _, err := runtime.WorkerEnvironment(); err == nil {
		t.Fatal("WorkerEnvironment 不该在目录不可用时给出环境")
	}
	// 运行时状态要说清原因，而不是假装缺 Python。
	status := runtime.Status()
	if status.State != FaceRuntimeStateIncompatible || !strings.Contains(status.Reason, "应用数据目录未解析") {
		t.Fatalf("状态应说明数据目录未解析: %+v", status)
	}

	// 诱饵一个字节都不许动，cwd 下也不许新建运行时目录。
	if content, err := os.ReadFile(baitFile); err != nil || string(content) != "keep" {
		t.Fatalf("cwd 下的同名目录被动过了: content=%q err=%v", string(content), err)
	}
	if _, err := os.Stat(filepath.Join(cwd, faceRuntimeDirName)); !os.IsNotExist(err) {
		t.Fatalf("cwd 下不该出现 %s/: %v", faceRuntimeDirName, err)
	}
	entries, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatalf("读 cwd 失败: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != facesDirName {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("cwd 下多出了东西: %v", names)
	}
}

// chdirForFaceTest 切到指定目录并返回恢复函数。
// 不用 t.Chdir：go.mod 的 go 指令是 1.23，那是 1.24 才有的 API。
func chdirForFaceTest(t *testing.T, dir string) func() {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("取当前目录失败: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("切换目录失败: %v", err)
	}
	return func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("恢复目录失败: %v", err)
		}
	}
}
