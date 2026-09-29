package services

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// P-025：超分每批复检扣除已写分段、空间不足与取消保留检查点并续跑、运行时未就绪状态、
// 产物继承原片信息。

// enhancementSegmentBytesUnder 统计 dir 下所有超分工作目录里已写出的分段字节，
// 用来模拟「卷上的可用空间随本任务写出分段而减少」。
func enhancementSegmentBytesUnder(t *testing.T, dir string) int64 {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".cineinsight-enhance-*", "seg-*.cispart"))
	if err != nil {
		t.Fatalf("统计分段失败: %v", err)
	}
	var total int64
	for _, match := range matches {
		if info, err := os.Stat(match); err == nil {
			total += info.Size()
		}
	}
	return total
}

func enhancementSegmentsExist(dir string) bool {
	matches, _ := filepath.Glob(filepath.Join(dir, ".cineinsight-enhance-*", "seg-*.cispart"))
	return len(matches) > 0
}

// countingSidecar 包一层命令替身，数一数推理（sidecar）被调了几次——每批一次。
func countingSidecar(base enhancementCommandRunner, calls *atomic.Int32) enhancementCommandRunner {
	return func(ctx context.Context, name string, args []string) (string, error) {
		if strings.Contains(name, "sidecar") {
			calls.Add(1)
		}
		return base(ctx, name, args)
	}
}

// MEDIA-03：卷上刚好只有「下限」那么多空间，每写出一段就少一段。按固定下限复检会在第二批误报
// disk_insufficient；扣除本任务已写分段之后应当一路跑完。
func TestEnhancementDiskRecheckSubtractsWrittenSegmentsMEDIA03(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createEnhancementSourceVideo(t, "movie.mp4")
	service := newEnhancementTestService(t, fakeEnhancementCommands(t, video.Path, 250, 24))
	floor := EnhancementRequiredDiskBytes(video.Size, 320, 240)
	service.diskFree = func(path string) (uint64, error) {
		return uint64(floor - enhancementSegmentBytesUnder(t, path)), nil
	}

	view, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	task := waitEnhancementTask(t, view.ID, models.EnhancementStatusCompleted)
	service.StopAndWait()
	if task.CommittedFrames != 250 || task.OutputVideoID == nil {
		t.Fatalf("扣除已写分段后应当跑完: %+v", task)
	}
}

// MEDIA-03：中途空间不足保留工作目录与检查点；腾出空间后重试从断点继续，已完成的批不重做。
func TestEnhancementDiskInsufficientKeepsCheckpointAndRetryResumesMEDIA03(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createEnhancementSourceVideo(t, "movie.mp4")
	var sidecarCalls atomic.Int32
	service := newEnhancementTestService(t, countingSidecar(fakeEnhancementCommands(t, video.Path, 250, 24), &sidecarCalls))
	var starve atomic.Bool
	starve.Store(true)
	service.diskFree = func(path string) (uint64, error) {
		// 第一段写出之后别的程序把卷占满了。
		if starve.Load() && enhancementSegmentsExist(path) {
			return 0, nil
		}
		return 1 << 62, nil
	}

	view, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	failed := waitEnhancementTask(t, view.ID, models.EnhancementStatusFailed)
	service.StopAndWait()
	if failed.ErrorCode != enhancementCodeDiskInsufficient || failed.CommittedFrames != 120 {
		t.Fatalf("应当在第二批报空间不足、已提交 120 帧: %+v", failed)
	}
	workdir := enhancementWorkdir(models.VideoEnhancementTask{ID: failed.ID, Video: video})
	for _, name := range []string{"segments.json", "seg-00000.cispart"} {
		if _, err := os.Stat(filepath.Join(workdir, name)); err != nil {
			t.Fatalf("空间不足应保留检查点 %s: %v", name, err)
		}
	}
	if failed.SourceSHA256 == "" {
		t.Fatalf("源哈希应已固化")
	}

	starve.Store(false)
	retried, err := service.RetryTask(failed.ID)
	if err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	var queued models.VideoEnhancementTask
	if err := database.DB.First(&queued, retried.ID).Error; err != nil {
		t.Fatal(err)
	}
	if queued.SourceSHA256 != failed.SourceSHA256 {
		t.Fatalf("从检查点续跑应保留固化的源哈希")
	}
	done := waitEnhancementTask(t, failed.ID, models.EnhancementStatusCompleted)
	service.StopAndWait()
	if done.CommittedFrames != 250 || done.OutputVideoID == nil {
		t.Fatalf("续跑应当完成: %+v", done)
	}
	// 第一次跑了 1 批，续跑只补剩下的 2 批；从头重跑会是 1 + 3。
	if got := sidecarCalls.Load(); got != 3 {
		t.Fatalf("续跑不该重做已提交的批次: sidecar 调用 %d 次", got)
	}
	if _, err := os.Stat(workdir); !os.IsNotExist(err) {
		t.Fatalf("完成后应清理工作目录: %v", err)
	}
}

// MEDIA-03：第二批推理时用户取消，检查点保留；重试只补剩下的批次。
func TestEnhancementCancelKeepsCheckpointAndRetryResumesMEDIA03(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createEnhancementSourceVideo(t, "movie.mp4")
	base := fakeEnhancementCommands(t, video.Path, 250, 24)
	var sidecarCalls atomic.Int32
	var blockSecond atomic.Bool
	blockSecond.Store(true)
	secondStarted := make(chan struct{}, 1)
	runner := func(ctx context.Context, name string, args []string) (string, error) {
		if strings.Contains(name, "sidecar") {
			if sidecarCalls.Add(1) == 2 && blockSecond.Load() {
				secondStarted <- struct{}{}
				<-ctx.Done()
				return "", ctx.Err()
			}
		}
		return base(ctx, name, args)
	}
	service := newEnhancementTestService(t, runner)
	view, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"})
	if err != nil {
		t.Fatal(err)
	}
	<-secondStarted
	if err := service.CancelTask(view.ID); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	cancelled := waitEnhancementTask(t, view.ID, models.EnhancementStatusCancelled)
	service.StopAndWait()
	if cancelled.ErrorCode != enhancementCodeCancelled {
		t.Fatalf("error_code=%q", cancelled.ErrorCode)
	}
	workdir := enhancementWorkdir(models.VideoEnhancementTask{ID: cancelled.ID, Video: video})
	if _, err := os.Stat(filepath.Join(workdir, "seg-00000.cispart")); err != nil {
		t.Fatalf("取消应保留已提交的分段: %v", err)
	}

	blockSecond.Store(false)
	if _, err := service.RetryTask(cancelled.ID); err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	done := waitEnhancementTask(t, cancelled.ID, models.EnhancementStatusCompleted)
	service.StopAndWait()
	if done.CommittedFrames != 250 {
		t.Fatalf("续跑应当完成: %+v", done)
	}
	// 1（第一批）+ 1（被取消的第二批）+ 2（续跑补第二、三批）；从头重跑会是 5。
	if got := sidecarCalls.Load(); got != 4 {
		t.Fatalf("续跑不该重做已提交的批次: sidecar 调用 %d 次", got)
	}
}

// MEDIA-03：运行时换了版本时检查点里的分段出自旧模型，不能拼接：重试丢弃检查点、从头来。
func TestEnhancementRetryDiscardsCheckpointWhenRuntimeChangedMEDIA03(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createEnhancementSourceVideo(t, "movie.mp4")
	service := newEnhancementTestService(t, fakeEnhancementCommands(t, video.Path, 250, 24))
	service.stopping = true
	task := models.VideoEnhancementTask{
		VideoID: video.ID, Profile: "general", Scale: 2,
		Status: models.EnhancementStatusFailed, Phase: models.EnhancementPhaseEnhance,
		SourceSize: video.Size, OutputBasename: EnhancementOutputBasename(video.Path, "general"),
		RuntimeVersion: "old-runtime", ModelVersion: EnhancementProfiles["general"].ModelName,
		ErrorCode: enhancementCodeDiskInsufficient, TotalFrames: 250, CommittedFrames: 120, SourceSHA256: "abc",
	}
	if err := database.DB.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	workdir := enhancementWorkdir(models.VideoEnhancementTask{ID: task.ID, Video: video})
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "seg-00000.cispart"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RetryTask(task.ID); err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	if _, err := os.Stat(workdir); !os.IsNotExist(err) {
		t.Fatalf("运行时变了应丢弃检查点: %v", err)
	}
	var reloaded models.VideoEnhancementTask
	if err := database.DB.First(&reloaded, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.CommittedFrames != 0 || reloaded.SourceSHA256 != "" || reloaded.RuntimeVersion != EnhancementRuntimeIdentity {
		t.Fatalf("丢弃检查点时应清零进度: %+v", reloaded)
	}
}

// MEDIA-11：运行时不可用时给出 not_ready 与一句面向用户的说明（不带路径），可用时为 available。
func TestEnhancementRuntimeCapabilityReportsReadinessMEDIA11(t *testing.T) {
	missing := ProbeEnhancementRuntime(t.TempDir(), "")
	if missing.Available || missing.State != EnhancementRuntimeStateNotReady || missing.Hint == "" {
		t.Fatalf("缺运行时应为 not_ready 并带说明: %+v", missing)
	}
	if strings.Contains(missing.Hint, "/") || strings.Contains(missing.Hint, "enhance-runtime") {
		t.Fatalf("说明里不该带路径或技术细节: %q", missing.Hint)
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		if missing.ReasonCode != "platform_unsupported" {
			t.Fatalf("非 Apple Silicon 应报 platform_unsupported: %+v", missing)
		}
	}
	for _, reason := range []string{"models_missing", "models_corrupt", "platform_unsupported", "runtime_unavailable"} {
		capability := EnhancementRuntimeCapability{ReasonCode: reason}.withReadiness()
		if capability.State != EnhancementRuntimeStateNotReady || capability.Hint == "" {
			t.Fatalf("%s 应为 not_ready 并带说明: %+v", reason, capability)
		}
	}
	if ready := (EnhancementRuntimeCapability{Available: true}).withReadiness(); ready.State != EnhancementRuntimeStateAvailable || ready.Hint != "" {
		t.Fatalf("可用时应为 available: %+v", ready)
	}
}

func createMetadataCopyVideo(t *testing.T, name string) models.Video {
	t.Helper()
	root := t.TempDir()
	video := models.Video{Name: name, Path: filepath.Join(root, name), Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	return video
}

// MEDIA-11：产物继承原片的非自动标签、人物与作品集（追加到末尾）；自动标签、已删除的标签与
// 作品集不复制；重复执行不产生重复关系。
func TestEnhancementCopiesSourceMetadataToOutputMEDIA11(t *testing.T) {
	setupVideoServiceTestDB(t)
	source := createMetadataCopyVideo(t, "source.mp4")
	output := createMetadataCopyVideo(t, "source.enhanced-general-2x.mkv")
	other := createMetadataCopyVideo(t, "other.mp4")

	manual := models.Tag{Name: "手动", IsActive: true}
	automatic := models.Tag{Name: "短视频", AutomaticKind: "short_video", IsActive: true}
	removed := models.Tag{Name: "已删", IsActive: true}
	for _, tag := range []*models.Tag{&manual, &automatic, &removed} {
		if err := database.DB.Create(tag).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Model(&source).Association("Tags").Append(&manual, &automatic, &removed); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&removed).Error; err != nil {
		t.Fatal(err)
	}

	alice := models.Person{DisplayName: "Alice"}
	bob := models.Person{DisplayName: "Bob"}
	for _, person := range []*models.Person{&alice, &bob} {
		if err := database.DB.Create(person).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.DB.Create(&models.VideoPerson{VideoID: source.ID, PersonID: person.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}

	series := models.MediaCollection{Name: "系列", NormalizedName: "系列"}
	solo := models.MediaCollection{Name: "单独", NormalizedName: "单独"}
	gone := models.MediaCollection{Name: "已删作品集", NormalizedName: "已删作品集"}
	for _, collection := range []*models.MediaCollection{&series, &solo, &gone} {
		if err := database.DB.Create(collection).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, member := range []models.CollectionVideo{
		{CollectionID: series.ID, VideoID: other.ID, Position: 1},
		{CollectionID: series.ID, VideoID: source.ID, Position: 2},
		{CollectionID: solo.ID, VideoID: source.ID, Position: 1},
		{CollectionID: gone.ID, VideoID: source.ID, Position: 1},
	} {
		if err := database.DB.Create(&member).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Delete(&gone).Error; err != nil {
		t.Fatal(err)
	}

	for round := 0; round < 2; round++ {
		if err := database.Transaction(func(tx *gorm.DB) error {
			return copyEnhancementSourceMetadataTx(tx, source.ID, output.ID)
		}); err != nil {
			t.Fatalf("第 %d 次复制失败: %v", round+1, err)
		}
	}

	var tagIDs []uint
	if err := database.DB.Table("video_tags").Where("video_id = ?", output.ID).Order("tag_id").Pluck("tag_id", &tagIDs).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tagIDs, []uint{manual.ID}) {
		t.Fatalf("只应复制非自动、未删除的标签: %v", tagIDs)
	}
	var personIDs []uint
	if err := database.DB.Model(&models.VideoPerson{}).Where("video_id = ?", output.ID).Order("person_id").Pluck("person_id", &personIDs).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(personIDs, []uint{alice.ID, bob.ID}) {
		t.Fatalf("人物应当全部复制且不重复: %v", personIDs)
	}
	var members []models.CollectionVideo
	if err := database.DB.Where("video_id = ?", output.ID).Order("collection_id").Find(&members).Error; err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[0].CollectionID != series.ID || members[0].Position != 3 || members[1].CollectionID != solo.ID || members[1].Position != 2 {
		t.Fatalf("产物应追加到原片所在作品集的末尾、跳过已删作品集: %+v", members)
	}
}

// MEDIA-11：原片的同名外挂字幕经字幕写入器复制为产物的同名 .srt；没有外挂字幕时什么都不做；
// 写入失败只给一句警告（不含路径），不影响产物。
func TestEnhancementCopiesSidecarSubtitleToOutputMEDIA11(t *testing.T) {
	root := t.TempDir()
	source := models.Video{ID: 1, Path: filepath.Join(root, "movie.mp4")}
	output := models.Video{ID: 2, Path: filepath.Join(root, "movie.enhanced-general-2x.mkv")}
	writer := NewSubtitleFileWriter(t.TempDir())
	target := filepath.Join(root, "movie.enhanced-general-2x.srt")

	if warning := copyEnhancementSidecarSubtitle(context.Background(), writer, source, output); warning != "" {
		t.Fatalf("没有外挂字幕时不该有警告: %q", warning)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("没有外挂字幕时不该写产物字幕: %v", err)
	}

	content := "1\n00:00:01,000 --> 00:00:02,000\n你好\n\n"
	if err := os.WriteFile(filepath.Join(root, "movie.srt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if warning := copyEnhancementSidecarSubtitle(context.Background(), writer, source, output); warning != "" {
		t.Fatalf("复制字幕不该有警告: %q", warning)
	}
	copied, err := os.ReadFile(target)
	if err != nil || string(copied) != content {
		t.Fatalf("产物字幕内容应与原片一致: %q err=%v", copied, err)
	}

	// 目标被一个目录占着：写入器拒绝覆盖，只报警告。
	blockedOutput := models.Video{ID: 3, Path: filepath.Join(root, "blocked.mkv")}
	blockedSource := models.Video{ID: 4, Path: filepath.Join(root, "blocked-src.mp4")}
	if err := os.WriteFile(filepath.Join(root, "blocked-src.srt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "blocked.srt"), 0o755); err != nil {
		t.Fatal(err)
	}
	warning := copyEnhancementSidecarSubtitle(context.Background(), writer, blockedSource, blockedOutput)
	if warning == "" || strings.Contains(warning, root) {
		t.Fatalf("写入失败应只给一句不含路径的警告: %q", warning)
	}
}
