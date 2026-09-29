package services

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"video-master/database"
	"video-master/models"
)

// 超分跟进修复：copy_metadata 落库与发布接线（MEDIA-11）、新建任务清理保留的检查点（MEDIA-03）。

const enhancementFollowupSubtitle = "1\n00:00:01,000 --> 00:00:02,000\n你好\n\n"

// enhancementMetadataFixture 是原片身上可被产物继承的信息：一条手动标签、一个人物、一个作品集
// （原片排在第 1 位）与同名外挂字幕。
type enhancementMetadataFixture struct {
	tag        models.Tag
	person     models.Person
	collection models.MediaCollection
}

func seedEnhancementSourceMetadata(t *testing.T, source models.Video) enhancementMetadataFixture {
	t.Helper()
	fixture := enhancementMetadataFixture{
		tag:        models.Tag{Name: "手动", IsActive: true},
		person:     models.Person{DisplayName: "Alice"},
		collection: models.MediaCollection{Name: "系列", NormalizedName: "系列"},
	}
	if err := database.DB.Create(&fixture.tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&source).Association("Tags").Append(&fixture.tag); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&fixture.person).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.VideoPerson{VideoID: source.ID, PersonID: fixture.person.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&fixture.collection).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.CollectionVideo{CollectionID: fixture.collection.ID, VideoID: source.ID, Position: 1}).Error; err != nil {
		t.Fatal(err)
	}
	sourceSRT := strings.TrimSuffix(source.Path, filepath.Ext(source.Path)) + ".srt"
	if err := os.WriteFile(sourceSRT, []byte(enhancementFollowupSubtitle), 0o644); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// enhancementOutputInheritance 读出产物身上的标签、人物、作品集成员与同名字幕。
type enhancementOutputInheritance struct {
	tagIDs      []uint
	personIDs   []uint
	members     []models.CollectionVideo
	subtitle    string
	hasSubtitle bool
}

func loadEnhancementOutputInheritance(t *testing.T, output models.Video) enhancementOutputInheritance {
	t.Helper()
	var got enhancementOutputInheritance
	// 只看非自动标签：短视频之类的自动标签由产物自己的规则判定，不是「继承」。
	if err := database.DB.Table("video_tags").
		Joins("JOIN tags ON tags.id = video_tags.tag_id").
		Where("video_tags.video_id = ? AND COALESCE(tags.automatic_kind, '') = ''", output.ID).
		Order("video_tags.tag_id").Pluck("video_tags.tag_id", &got.tagIDs).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.VideoPerson{}).Where("video_id = ?", output.ID).Order("person_id").Pluck("person_id", &got.personIDs).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Where("video_id = ?", output.ID).Order("collection_id").Find(&got.members).Error; err != nil {
		t.Fatal(err)
	}
	outputSRT := strings.TrimSuffix(output.Path, filepath.Ext(output.Path)) + ".srt"
	if content, err := os.ReadFile(outputSRT); err == nil {
		got.subtitle, got.hasSubtitle = string(content), true
	}
	return got
}

func loadEnhancementOutputVideo(t *testing.T, task models.VideoEnhancementTask) models.Video {
	t.Helper()
	if task.OutputVideoID == nil {
		t.Fatalf("完成的任务应带产物 ID: %+v", task)
	}
	var output models.Video
	if err := database.DB.First(&output, *task.OutputVideoID).Error; err != nil {
		t.Fatal(err)
	}
	return output
}

func assertEnhancementOutputInheritsNothing(t *testing.T, output models.Video) {
	t.Helper()
	got := loadEnhancementOutputInheritance(t, output)
	if len(got.tagIDs) != 0 || len(got.personIDs) != 0 || len(got.members) != 0 || got.hasSubtitle {
		t.Fatalf("copy_metadata=false 时产物不该继承任何原片信息: %+v", got)
	}
}

// MEDIA-11：开启 copy_metadata，产物继承原片的标签、人物、作品集（追加到末尾）与同名外挂字幕；
// 选择落在任务行上，发布时读的是任务行。
func TestEnhancementCopyMetadataOnOutputInheritsSourceMEDIA11(t *testing.T) {
	setupVideoServiceTestDB(t)
	source := createEnhancementSourceVideo(t, "movie.mp4")
	fixture := seedEnhancementSourceMetadata(t, source)
	service := newEnhancementTestService(t, fakeEnhancementCommands(t, source.Path, 250, 24))

	view, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: source.ID, Profile: "general", CopyMetadata: true})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if !view.CopyMetadata {
		t.Fatalf("任务视图应带上 copy_metadata: %+v", view.VideoEnhancementTask)
	}
	waitEnhancementTask(t, view.ID, models.EnhancementStatusCompleted)
	service.StopAndWait()
	// 状态在发布事务里就变成 completed，外挂字幕与警告在事务之后：等 worker 退出再读任务行。
	task := reloadEnhancementTask(t, view.ID)
	if !task.CopyMetadata || task.ErrorCode != "" || task.ErrorSummary != "" {
		t.Fatalf("任务行应记住 copy_metadata 且无警告: %+v", task)
	}

	got := loadEnhancementOutputInheritance(t, loadEnhancementOutputVideo(t, task))
	if !reflect.DeepEqual(got.tagIDs, []uint{fixture.tag.ID}) {
		t.Fatalf("产物应继承手动标签: %v", got.tagIDs)
	}
	if !reflect.DeepEqual(got.personIDs, []uint{fixture.person.ID}) {
		t.Fatalf("产物应继承人物: %v", got.personIDs)
	}
	if len(got.members) != 1 || got.members[0].CollectionID != fixture.collection.ID || got.members[0].Position != 2 {
		t.Fatalf("产物应追加到原片所在作品集的末尾: %+v", got.members)
	}
	if !got.hasSubtitle || got.subtitle != enhancementFollowupSubtitle {
		t.Fatalf("产物应带上与原片一致的同名字幕: %q", got.subtitle)
	}
}

// MEDIA-11：关闭 copy_metadata（请求不带这个字段同样是关闭），产物什么都不继承。
func TestEnhancementCopyMetadataOffCopiesNothingMEDIA11(t *testing.T) {
	setupVideoServiceTestDB(t)
	source := createEnhancementSourceVideo(t, "movie.mp4")
	seedEnhancementSourceMetadata(t, source)
	service := newEnhancementTestService(t, fakeEnhancementCommands(t, source.Path, 250, 24))

	view, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: source.ID, Profile: "general"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	waitEnhancementTask(t, view.ID, models.EnhancementStatusCompleted)
	service.StopAndWait()
	// 状态在发布事务里就变成 completed，外挂字幕与警告在事务之后：等 worker 退出再读任务行。
	task := reloadEnhancementTask(t, view.ID)
	if task.CopyMetadata {
		t.Fatalf("未开启时任务行应为 false: %+v", task)
	}
	assertEnhancementOutputInheritsNothing(t, loadEnhancementOutputVideo(t, task))
}

// MEDIA-11：历史任务（创建时没有这个选项，行里 copy_metadata 取列默认值 false）发布时不复制。
func TestEnhancementHistoricalTaskWithoutCopyMetadataCopiesNothingMEDIA11(t *testing.T) {
	setupVideoServiceTestDB(t)
	source := createEnhancementSourceVideo(t, "movie.mp4")
	seedEnhancementSourceMetadata(t, source)
	info, err := os.Stat(source.Path)
	if err != nil {
		t.Fatal(err)
	}
	service := newEnhancementTestService(t, fakeEnhancementCommands(t, source.Path, 250, 24))
	historical := models.VideoEnhancementTask{
		VideoID: source.ID, Profile: "general", Scale: 2,
		Status: models.EnhancementStatusQueued, Phase: models.EnhancementPhasePreflight,
		SourceSize: info.Size(), SourceModTimeNS: info.ModTime().UnixNano(),
		RuntimeVersion: EnhancementRuntimeIdentity, ModelVersion: EnhancementProfiles["general"].ModelName,
		OutputBasename: EnhancementOutputBasename(source.Path, "general"),
		// 写 true 再 Omit：证明落库的值来自列默认值，而不是结构体零值。
		CopyMetadata: true,
	}
	if err := database.DB.Omit("copy_metadata").Create(&historical).Error; err != nil {
		t.Fatal(err)
	}
	service.ensureWorker()
	waitEnhancementTask(t, historical.ID, models.EnhancementStatusCompleted)
	service.StopAndWait()
	task := reloadEnhancementTask(t, historical.ID)
	if task.CopyMetadata {
		t.Fatalf("历史任务的 copy_metadata 应为列默认值 false: %+v", task)
	}
	assertEnhancementOutputInheritsNothing(t, loadEnhancementOutputVideo(t, task))
}

// MEDIA-11：外挂字幕复制失败只是一句警告：任务照样完成、标签等照样继承，警告留在 error_summary、
// 不含路径，error_code 保持为空。
func TestEnhancementSubtitleCopyFailureIsOnlyAWarningMEDIA11(t *testing.T) {
	setupVideoServiceTestDB(t)
	source := createEnhancementSourceVideo(t, "movie.mp4")
	fixture := seedEnhancementSourceMetadata(t, source)
	// 产物的同名 .srt 被一个目录占着：写入器拒绝覆盖。
	blocked := filepath.Join(source.Directory, "movie.enhanced-general-2x.srt")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	service := newEnhancementTestService(t, fakeEnhancementCommands(t, source.Path, 250, 24))

	view, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: source.ID, Profile: "general", CopyMetadata: true})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	waitEnhancementTask(t, view.ID, models.EnhancementStatusCompleted)
	service.StopAndWait()
	// 状态在发布事务里就变成 completed，外挂字幕与警告在事务之后：等 worker 退出再读任务行。
	task := reloadEnhancementTask(t, view.ID)
	if task.OutputVideoID == nil || task.ErrorCode != "" {
		t.Fatalf("字幕复制失败不该影响任务成功: %+v", task)
	}
	if task.ErrorSummary == "" || strings.Contains(task.ErrorSummary, source.Directory) || strings.Contains(task.ErrorSummary, "/") {
		t.Fatalf("应留下一句不含路径的警告: %q", task.ErrorSummary)
	}
	got := loadEnhancementOutputInheritance(t, loadEnhancementOutputVideo(t, task))
	if !reflect.DeepEqual(got.tagIDs, []uint{fixture.tag.ID}) || !reflect.DeepEqual(got.personIDs, []uint{fixture.person.ID}) {
		t.Fatalf("字幕复制失败不该影响标签与人物的继承: %+v", got)
	}
	if info, err := os.Stat(blocked); err != nil || !info.IsDir() {
		t.Fatalf("占位目录应原样保留: %v", err)
	}
}

// MEDIA-11：重试沿用创建时的 copy_metadata。空间不足失败 → 重试续跑 → 发布时仍然复制。
func TestEnhancementRetryKeepsCopyMetadataMEDIA11(t *testing.T) {
	setupVideoServiceTestDB(t)
	source := createEnhancementSourceVideo(t, "movie.mp4")
	fixture := seedEnhancementSourceMetadata(t, source)
	service := newEnhancementTestService(t, fakeEnhancementCommands(t, source.Path, 250, 24))
	var starve atomic.Bool
	starve.Store(true)
	service.diskFree = func(path string) (uint64, error) {
		if starve.Load() && enhancementSegmentsExist(path) {
			return 0, nil
		}
		return 1 << 62, nil
	}

	view, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: source.ID, Profile: "general", CopyMetadata: true})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	failed := waitEnhancementTask(t, view.ID, models.EnhancementStatusFailed)
	service.StopAndWait()
	if failed.ErrorCode != enhancementCodeDiskInsufficient || !failed.CopyMetadata {
		t.Fatalf("应当空间不足失败且保留 copy_metadata: %+v", failed)
	}

	starve.Store(false)
	if _, err := service.RetryTask(failed.ID); err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	var queued models.VideoEnhancementTask
	if err := database.DB.First(&queued, failed.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !queued.CopyMetadata {
		t.Fatalf("重试不该改掉 copy_metadata: %+v", queued)
	}
	done := waitEnhancementTask(t, failed.ID, models.EnhancementStatusCompleted)
	service.StopAndWait()
	got := loadEnhancementOutputInheritance(t, loadEnhancementOutputVideo(t, done))
	if !reflect.DeepEqual(got.tagIDs, []uint{fixture.tag.ID}) || !got.hasSubtitle {
		t.Fatalf("重试后发布仍应复制原片信息: %+v", got)
	}
}

// seedRetainedEnhancementTask 造一条保留了检查点的已结束任务：工作目录里有清单与一个已提交分段。
func seedRetainedEnhancementTask(t *testing.T, video models.Video, status, code string) models.VideoEnhancementTask {
	t.Helper()
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatal(err)
	}
	task := models.VideoEnhancementTask{
		VideoID: video.ID, Profile: "general", Scale: 2,
		Status: status, Phase: models.EnhancementPhaseEnhance,
		SourceSize: info.Size(), SourceModTimeNS: info.ModTime().UnixNano(), SourceSHA256: "abc",
		RuntimeVersion: EnhancementRuntimeIdentity, ModelVersion: EnhancementProfiles["general"].ModelName,
		OutputBasename: EnhancementOutputBasename(video.Path, "general"),
		ErrorCode:      code, TotalFrames: 250, CommittedFrames: 120,
	}
	if err := database.DB.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	workdir := enhancementWorkdir(models.VideoEnhancementTask{ID: task.ID, Video: video})
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"segments.json": `{"segments":[]}`, "seg-00000.cispart": "segment"} {
		if err := os.WriteFile(filepath.Join(workdir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return task
}

func reloadEnhancementTask(t *testing.T, id uint) models.VideoEnhancementTask {
	t.Helper()
	var task models.VideoEnhancementTask
	if err := database.DB.First(&task, id).Error; err != nil {
		t.Fatal(err)
	}
	return task
}

// MEDIA-03：取消保留了检查点 → 为同一视频新建任务 → 旧工作目录被删除，旧任务标为不可续跑；
// 旧任务再重试时从头开始（sidecar 调用次数证明没有复用已删除的检查点）。清理发生在磁盘下限
// 检查之前：旧检查点占着的空间算进这次的可用空间。
func TestEnhancementNewTaskDiscardsRetainedCheckpointMEDIA03(t *testing.T) {
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
	first, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"})
	if err != nil {
		t.Fatal(err)
	}
	<-secondStarted
	if err := service.CancelTask(first.ID); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	waitEnhancementTask(t, first.ID, models.EnhancementStatusCancelled)
	service.StopAndWait()
	oldWorkdir := enhancementWorkdir(models.VideoEnhancementTask{ID: first.ID, Video: video})
	if _, err := os.Stat(filepath.Join(oldWorkdir, "seg-00000.cispart")); err != nil {
		t.Fatalf("取消应保留检查点: %v", err)
	}

	// 旧检查点还在时，卷上的空间差一个字节才够下限。
	floor := EnhancementRequiredDiskBytes(video.Size, 320, 240)
	service.diskFree = func(string) (uint64, error) {
		if _, err := os.Stat(oldWorkdir); err == nil {
			return uint64(floor - 1), nil
		}
		return 1 << 62, nil
	}
	service.stopping = true // 新任务只排队不跑，专注清理语义
	second, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "anime"})
	if err != nil {
		t.Fatalf("新建同视频任务失败（旧检查点应先被清理）: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("应当新建任务，而不是返回旧任务")
	}
	if _, err := os.Stat(oldWorkdir); !os.IsNotExist(err) {
		t.Fatalf("新建同视频任务后旧工作目录应被删除: %v", err)
	}
	discarded := reloadEnhancementTask(t, first.ID)
	if discarded.Status != models.EnhancementStatusCancelled || discarded.ErrorCode != enhancementCodeCheckpointDiscarded {
		t.Fatalf("旧任务应保持已取消、结束码改为 checkpoint_discarded: %+v", discarded)
	}
	if discarded.ErrorSummary == "" || strings.Contains(discarded.ErrorSummary, "/") {
		t.Fatalf("旧任务应带一句不含路径的说明: %q", discarded.ErrorSummary)
	}

	// 让出活跃名额，再重试旧任务：不能从已删除的检查点续跑。worker 先停着，排队后的行不被抢跑改写。
	if err := service.CancelTask(second.ID); err != nil {
		t.Fatal(err)
	}
	waitEnhancementTask(t, second.ID, models.EnhancementStatusCancelled)
	service.diskFree = func(string) (uint64, error) { return 1 << 62, nil }
	blockSecond.Store(false)
	if _, err := service.RetryTask(first.ID); err != nil {
		t.Fatalf("重试旧任务失败: %v", err)
	}
	queued := reloadEnhancementTask(t, first.ID)
	if queued.Status != models.EnhancementStatusQueued || queued.CommittedFrames != 0 || queued.SourceSHA256 != "" || queued.ErrorCode != "" {
		t.Fatalf("检查点已清理的任务重试应清零进度: %+v", queued)
	}
	service.stopping = false
	service.ensureWorker()
	done := waitEnhancementTask(t, first.ID, models.EnhancementStatusCompleted)
	service.StopAndWait()
	if done.CommittedFrames != 250 {
		t.Fatalf("重试应当完成: %+v", done)
	}
	// 1（第一批）+ 1（被取消的第二批）+ 3（从头重跑）；从检查点续跑会是 4。
	if got := sidecarCalls.Load(); got != 5 {
		t.Fatalf("检查点已被清理，重试应从头开始: sidecar 调用 %d 次", got)
	}
}

// MEDIA-03：空间不足保留的检查点同样在新建同视频任务时清理（状态仍为 failed）。
func TestEnhancementNewTaskDiscardsDiskInsufficientCheckpointMEDIA03(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createEnhancementSourceVideo(t, "movie.mp4")
	service := newEnhancementTestService(t, fakeEnhancementCommands(t, video.Path, 250, 24))
	service.stopping = true
	retained := seedRetainedEnhancementTask(t, video, models.EnhancementStatusFailed, enhancementCodeDiskInsufficient)
	if _, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: video.ID, Profile: "general"}); err != nil {
		t.Fatalf("新建任务失败: %v", err)
	}
	if _, err := os.Stat(enhancementWorkdir(models.VideoEnhancementTask{ID: retained.ID, Video: video})); !os.IsNotExist(err) {
		t.Fatalf("空间不足保留的工作目录应被删除: %v", err)
	}
	reloaded := reloadEnhancementTask(t, retained.ID)
	if reloaded.Status != models.EnhancementStatusFailed || reloaded.ErrorCode != enhancementCodeCheckpointDiscarded || !strings.Contains(reloaded.ErrorSummary, "空间不足") {
		t.Fatalf("旧任务应保持失败、改为 checkpoint_discarded 并保留原因: %+v", reloaded)
	}
}

// MEDIA-03：清理只针对「为同一视频新建了任务」：别的视频的保留任务不动、仍可从检查点续跑；
// 创建时该视频已有活跃任务（幂等返回、没有新建）也不动；普通失败的任务本来就没有检查点，不改写。
func TestEnhancementCreateKeepsCheckpointsItDoesNotSupersedeMEDIA03(t *testing.T) {
	setupVideoServiceTestDB(t)
	videoX := createEnhancementSourceVideo(t, "x.mp4")
	videoY := createEnhancementSourceVideo(t, "y.mp4")
	videoZ := createEnhancementSourceVideo(t, "z.mp4")
	service := newEnhancementTestService(t, fakeEnhancementCommands(t, videoX.Path, 250, 24))
	service.stopping = true

	// 别的视频：为 Y 新建任务，X 的保留任务不受影响。
	retainedX := seedRetainedEnhancementTask(t, videoX, models.EnhancementStatusCancelled, enhancementCodeCancelled)
	plainFailedY := seedRetainedEnhancementTask(t, videoY, models.EnhancementStatusFailed, "inference_failed")
	if _, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: videoY.ID, Profile: "general"}); err != nil {
		t.Fatalf("为 Y 新建任务失败: %v", err)
	}
	workdirX := enhancementWorkdir(models.VideoEnhancementTask{ID: retainedX.ID, Video: videoX})
	if _, err := os.Stat(filepath.Join(workdirX, "seg-00000.cispart")); err != nil {
		t.Fatalf("别的视频的检查点不该被清理: %v", err)
	}
	if got := reloadEnhancementTask(t, retainedX.ID); got.ErrorCode != enhancementCodeCancelled {
		t.Fatalf("别的视频的保留任务不该被改写: %+v", got)
	}
	if got := reloadEnhancementTask(t, plainFailedY.ID); got.ErrorCode != "inference_failed" {
		t.Fatalf("普通失败的任务不属于保留检查点，不该被改写: %+v", got)
	}

	// 已有活跃任务：幂等返回它，Z 的保留任务不动。
	retainedZ := seedRetainedEnhancementTask(t, videoZ, models.EnhancementStatusCancelled, enhancementCodeCancelled)
	activeZ := models.VideoEnhancementTask{
		VideoID: videoZ.ID, Profile: "general", Scale: 2,
		Status: models.EnhancementStatusQueued, Phase: models.EnhancementPhasePreflight,
		SourceSize: videoZ.Size, OutputBasename: EnhancementOutputBasename(videoZ.Path, "general"),
		RuntimeVersion: EnhancementRuntimeIdentity,
	}
	if err := database.DB.Create(&activeZ).Error; err != nil {
		t.Fatal(err)
	}
	returned, err := service.CreateTask(context.Background(), EnhancementCreateRequest{VideoID: videoZ.ID, Profile: "general"})
	if err != nil || returned.ID != activeZ.ID {
		t.Fatalf("已有活跃任务时应幂等返回它: %+v err=%v", returned, err)
	}
	if _, err := os.Stat(filepath.Join(enhancementWorkdir(models.VideoEnhancementTask{ID: retainedZ.ID, Video: videoZ}), "seg-00000.cispart")); err != nil {
		t.Fatalf("没有新建任务时不该清理检查点: %v", err)
	}
	if got := reloadEnhancementTask(t, retainedZ.ID); got.ErrorCode != enhancementCodeCancelled {
		t.Fatalf("没有新建任务时保留任务不该被改写: %+v", got)
	}

	// 未被清理的保留任务照常从检查点续跑：进度与固化的源哈希保留。
	if _, err := service.RetryTask(retainedX.ID); err != nil {
		t.Fatalf("重试 X 失败: %v", err)
	}
	resumed := reloadEnhancementTask(t, retainedX.ID)
	if resumed.Status != models.EnhancementStatusQueued || resumed.CommittedFrames != 120 || resumed.SourceSHA256 != "abc" {
		t.Fatalf("未被清理的保留任务重试应从检查点续跑: %+v", resumed)
	}
	if _, err := os.Stat(filepath.Join(workdirX, "seg-00000.cispart")); err != nil {
		t.Fatalf("续跑重试不该删检查点: %v", err)
	}
}
