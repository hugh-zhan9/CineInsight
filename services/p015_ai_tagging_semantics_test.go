package services

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// P-015：AI 打标语义（D-PC28）、批量批准（D-PC29）、空闲门（D-PC19）。

func p015Video(t *testing.T, name string) models.Video {
	t.Helper()
	video := models.Video{Name: name, Path: "/tmp/" + name, Directory: "/tmp"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	return video
}

func p015Tag(t *testing.T, name, namespace string) models.Tag {
	t.Helper()
	tag := models.Tag{Name: name, Color: "#fff", Namespace: namespace, IsSystem: true, IsActive: true}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	return tag
}

func p015Candidate(t *testing.T, videoID uint, tag models.Tag, confidence, status string) models.AITagCandidate {
	t.Helper()
	tagID := tag.ID
	candidate := models.AITagCandidate{
		VideoID: videoID, SuggestedName: tag.Name, NormalizedName: normalizeAITagName(tag.Name),
		MatchedTagID: &tagID, Confidence: confidence, Status: status,
	}
	if err := database.DB.Create(&candidate).Error; err != nil {
		t.Fatalf("创建候选失败: %v", err)
	}
	return candidate
}

func p015CandidateStatus(t *testing.T, id uint) string {
	t.Helper()
	var candidate models.AITagCandidate
	if err := database.DB.First(&candidate, id).Error; err != nil {
		t.Fatalf("读取候选失败: %v", err)
	}
	return candidate.Status
}

func p015AttachManualVideoTag(t *testing.T, videoID uint) {
	t.Helper()
	manual := models.Tag{Name: "我自己打的", Color: "#333"}
	if err := database.DB.Create(&manual).Error; err != nil {
		t.Fatalf("创建人工标签失败: %v", err)
	}
	if err := database.DB.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?)", videoID, manual.ID).Error; err != nil {
		t.Fatalf("关联人工标签失败: %v", err)
	}
}

// META-01：用户拒绝过的 (视频, 标签) 不再生成候选。
func TestAITaggingPersistSuggestionsSkipsRejectedTagMETA01(t *testing.T) {
	setupVideoServiceTestDB(t)
	rejectedTag := p015Tag(t, "动作", "custom")
	otherTag := p015Tag(t, "悬疑", "custom")
	video := p015Video(t, "reject-memory.mp4")
	p015Candidate(t, video.ID, rejectedTag, models.AITagConfidenceHigh, models.AITagCandidateStatusRejected)

	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	created, err := svc.persistSuggestions(video, []models.Tag{rejectedTag, otherTag}, AITaggingEvidence{}, []AITagSuggestion{
		{Label: "动作", MatchedExistingName: "动作", Confidence: models.AITagConfidenceHigh},
		{Label: "悬疑", MatchedExistingName: "悬疑", Confidence: models.AITagConfidenceHigh},
	})
	if err != nil {
		t.Fatalf("写候选失败: %v", err)
	}
	if created != 1 {
		t.Fatalf("只应为未被拒绝的标签生成候选，实际 %d", created)
	}
	var pending []models.AITagCandidate
	if err := database.DB.Where("video_id = ? AND status = ?", video.ID, models.AITagCandidateStatusPending).Find(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].MatchedTagID == nil || *pending[0].MatchedTagID != otherTag.ID {
		t.Fatalf("待审候选应只剩未被拒绝的标签: %+v", pending)
	}

	// 拒绝记忆跟着标签 id 走：改名后依然有效。
	if err := database.DB.Model(&models.Tag{}).Where("id = ?", rejectedTag.ID).Update("name", "动作片").Error; err != nil {
		t.Fatal(err)
	}
	renamed := rejectedTag
	renamed.Name = "动作片"
	created, err = svc.persistSuggestions(video, []models.Tag{renamed}, AITaggingEvidence{}, []AITagSuggestion{
		{Label: "动作片", MatchedExistingName: "动作片", Confidence: models.AITagConfidenceHigh},
	})
	if err != nil || created != 0 {
		t.Fatalf("改名后的被拒绝标签仍不应生成候选: created=%d err=%v", created, err)
	}
}

// META-01：词表指纹只看 (id, name, trimmed namespace)，改颜色 / 更新时间不变，改名与改分类会变。
func TestAITaggingTagLibraryHashIgnoresColorMETA01(t *testing.T) {
	base := []models.Tag{
		{ID: 2, Name: "悬疑", Namespace: "类型", Color: "#111", UpdatedAt: time.Unix(100, 0)},
		{ID: 1, Name: "动作", Namespace: " 类型 ", Color: "#222", UpdatedAt: time.Unix(100, 0)},
	}
	want := tagLibraryHash(base)

	recolored := []models.Tag{
		{ID: 1, Name: "动作", Namespace: "类型", Color: "#abcdef", UpdatedAt: time.Unix(999, 0)},
		{ID: 2, Name: "悬疑", Namespace: "类型", Color: "#000", UpdatedAt: time.Unix(999, 0)},
	}
	if got := tagLibraryHash(recolored); got != want {
		t.Fatal("只改颜色 / 更新时间 / 输入顺序，指纹必须不变")
	}
	renamed := []models.Tag{base[0], {ID: 1, Name: "动作片", Namespace: "类型"}}
	if tagLibraryHash(renamed) == want {
		t.Fatal("改名必须改变指纹")
	}
	moved := []models.Tag{base[0], {ID: 1, Name: "动作", Namespace: "题材"}}
	if tagLibraryHash(moved) == want {
		t.Fatal("改分类必须改变指纹")
	}
}

// META-06：手动加标签时，只作废同视频 pending 且 matched_tag_id 相同的候选。
func TestSupersedeCandidatesForManualTagOnlyMatchingPendingMETA06(t *testing.T) {
	setupVideoServiceTestDB(t)
	tagA := p015Tag(t, "动作", "custom")
	tagB := p015Tag(t, "悬疑", "custom")
	video := p015Video(t, "manual-add.mp4")
	other := p015Video(t, "other.mp4")
	matching := p015Candidate(t, video.ID, tagA, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	unrelated := p015Candidate(t, video.ID, tagB, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	rejected := p015Candidate(t, video.ID, tagA, models.AITagConfidenceHigh, models.AITagCandidateStatusRejected)
	otherVideo := p015Candidate(t, other.ID, tagA, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)

	var ids []uint
	err := database.Transaction(func(tx *gorm.DB) error {
		var err error
		ids, err = SupersedeCandidatesForManualTag(tx, video.ID, tagA.ID)
		return err
	})
	if err != nil {
		t.Fatalf("作废失败: %v", err)
	}
	if len(ids) != 1 || ids[0] != matching.ID {
		t.Fatalf("应只返回匹配候选 id: %v", ids)
	}
	if got := p015CandidateStatus(t, matching.ID); got != models.AITagCandidateStatusSuperseded {
		t.Fatalf("匹配候选应 superseded，实际 %s", got)
	}
	for name, id := range map[string]uint{"同视频其他标签": unrelated.ID, "其他视频": otherVideo.ID} {
		if got := p015CandidateStatus(t, id); got != models.AITagCandidateStatusPending {
			t.Fatalf("%s的候选不应被动，实际 %s", name, got)
		}
	}
	if got := p015CandidateStatus(t, rejected.ID); got != models.AITagCandidateStatusRejected {
		t.Fatalf("已拒绝候选不应被改写，实际 %s", got)
	}
}

// IMG-08（图片侧规则 2）：手动加标签只作废同图匹配候选。
func TestSupersedeImageCandidatesForManualTagOnlyMatchingPendingIMG08(t *testing.T) {
	newImageAITaggingTestService(t, imageTaggingClientFunc(nil))
	library := imageAITaggingTestLibrary(t, "海边", "日落")
	img := imageAITaggingTestImage(t, "heic")
	matching := seedImageAITagCandidate(t, img.ID, library[0], models.AITagConfidenceHigh)
	unrelated := seedImageAITagCandidate(t, img.ID, library[1], models.AITagConfidenceHigh)

	err := database.Transaction(func(tx *gorm.DB) error {
		ids, err := SupersedeImageCandidatesForManualTag(tx, img.ID, library[0].ID)
		if err == nil && (len(ids) != 1 || ids[0] != matching.ID) {
			t.Fatalf("应只返回匹配候选: %v", ids)
		}
		return err
	})
	if err != nil {
		t.Fatalf("作废失败: %v", err)
	}
	statusOf := func(id uint) string {
		var c models.ImageAITagCandidate
		if err := database.DB.First(&c, id).Error; err != nil {
			t.Fatal(err)
		}
		return c.Status
	}
	if statusOf(matching.ID) != models.AITagCandidateStatusSuperseded || statusOf(unrelated.ID) != models.AITagCandidateStatusPending {
		t.Fatalf("状态不符: matching=%s unrelated=%s", statusOf(matching.ID), statusOf(unrelated.ID))
	}
}

// META-11：批量批准逐项返回结果，一条失败不影响其余，重复 id 只处理一次。
func TestApproveAITagCandidatesBatchPerItemResultMETA11(t *testing.T) {
	setupVideoServiceTestDB(t)
	tagA := p015Tag(t, "动作", "custom")
	tagB := p015Tag(t, "悬疑", "custom")
	video := p015Video(t, "batch.mp4")
	okA := p015Candidate(t, video.ID, tagA, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	okB := p015Candidate(t, video.ID, tagB, models.AITagConfidenceMedium, models.AITagCandidateStatusPending)
	rejected := p015Candidate(t, video.ID, tagA, models.AITagConfidenceHigh, models.AITagCandidateStatusRejected)

	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	result := svc.ApproveCandidates([]uint{okA.ID, rejected.ID, okB.ID, okA.ID, 999999})
	if result.Requested != 4 || result.Succeeded != 2 || result.Failed != 2 || len(result.Results) != 4 {
		t.Fatalf("汇总不符: %+v", result)
	}
	byID := map[uint]AITagBatchItemResult{}
	for _, item := range result.Results {
		byID[item.ID] = item
	}
	if !byID[okA.ID].OK || byID[okA.ID].Item == nil || !byID[okB.ID].OK {
		t.Fatalf("应批准成功的候选未成功: %+v", result.Results)
	}
	if byID[rejected.ID].OK || byID[rejected.ID].Message == "" || byID[999999].OK {
		t.Fatalf("不可批准的候选应逐项报错: %+v", result.Results)
	}
	if got := countRows(t, "video_tags"); got != 2 {
		t.Fatalf("应写入两条正式关联，实际 %d", got)
	}
}

// META-11：按筛选批准 —— 只支持 tag_id + 可选 confidence 精确匹配，已删除视频不参与，
// 计数预览与实际批准数一致，空筛选拒绝执行。
func TestApproveAITagCandidatesByFilterMETA11(t *testing.T) {
	setupVideoServiceTestDB(t)
	tagA := p015Tag(t, "动作", "custom")
	tagB := p015Tag(t, "悬疑", "custom")
	v1 := p015Video(t, "alpha_one.mp4")
	v2 := p015Video(t, "beta.mp4")
	deleted := p015Video(t, "alpha_deleted.mp4")
	c1 := p015Candidate(t, v1.ID, tagA, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	c2 := p015Candidate(t, v2.ID, tagA, models.AITagConfidenceMedium, models.AITagCandidateStatusPending)
	c3 := p015Candidate(t, v2.ID, tagB, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	c4 := p015Candidate(t, deleted.ID, tagA, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	if err := database.DB.Delete(&deleted).Error; err != nil {
		t.Fatal(err)
	}

	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	// 空筛选拒绝：不能一键批准全部待审。
	if _, err := svc.ApproveCandidatesByFilter(AITagCandidateFilter{}); !errors.Is(err, ErrAITagFilterTagRequired) {
		t.Fatalf("空筛选应被拒绝: %v", err)
	}
	if _, err := svc.CountCandidatesByFilter(AITagCandidateFilter{}); !errors.Is(err, ErrAITagFilterTagRequired) {
		t.Fatalf("空筛选计数也应被拒绝: %v", err)
	}
	// 计数预览：标签 A 下 c1(high) + c2(medium)，c4 视频已删除。
	count, err := svc.CountCandidatesByFilter(AITagCandidateFilter{TagID: tagA.ID})
	if err != nil || count != 2 {
		t.Fatalf("预览应为 2: %d %v", count, err)
	}
	// 只要 high + 标签 A：只有 c1，预览与批准数一致。
	filter := AITagCandidateFilter{TagID: tagA.ID, Confidence: models.AITagConfidenceHigh}
	if count, err := svc.CountCandidatesByFilter(filter); err != nil || count != 1 {
		t.Fatalf("high 预览应为 1: %d %v", count, err)
	}
	result, err := svc.ApproveCandidatesByFilter(filter)
	if err != nil {
		t.Fatalf("按筛选批准失败: %v", err)
	}
	if result.Requested != 1 || result.Succeeded != 1 || result.Results[0].ID != c1.ID {
		t.Fatalf("筛选结果不符: %+v", result)
	}
	// 剩余：标签 A 的 medium。
	count, _ = svc.CountCandidatesByFilter(AITagCandidateFilter{TagID: tagA.ID})
	result, err = svc.ApproveCandidatesByFilter(AITagCandidateFilter{TagID: tagA.ID})
	if err != nil || result.Succeeded != int(count) || count != 1 {
		t.Fatalf("预览与批准数应一致: count=%d %+v err=%v", count, result, err)
	}
	if got := p015CandidateStatus(t, c2.ID); got != models.AITagCandidateStatusApproved {
		t.Fatalf("c2 应 approved，实际 %s", got)
	}
	if got := p015CandidateStatus(t, c3.ID); got != models.AITagCandidateStatusPending {
		t.Fatalf("其他标签的候选不应被批准，实际 %s", got)
	}
	if got := p015CandidateStatus(t, c4.ID); got != models.AITagCandidateStatusPending {
		t.Fatalf("已删除视频的候选不应被批准，实际 %s", got)
	}
}

// META-12：「人物」分类的标签不进入词表，视频与图片两侧一致（含首尾空白的分类名）。
func TestAITaggingVocabularyExcludesPersonNamespaceMETA12(t *testing.T) {
	svc := newImageAITaggingTestService(t, imageTaggingClientFunc(nil))
	keep := p015Tag(t, "悬疑", "类型")
	noNamespace := p015Tag(t, "无分类", "")
	p015Tag(t, "张三", personTagNamespace)
	p015Tag(t, "李四", " "+personTagNamespace+" ")

	videoSvc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	videoTags, err := videoSvc.loadActiveTags()
	if err != nil {
		t.Fatal(err)
	}
	imageTags, err := svc.loadActiveLibraryTags(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, tags := range map[string][]models.Tag{"视频": videoTags, "图片": imageTags} {
		got := map[uint]bool{}
		for _, tag := range tags {
			got[tag.ID] = true
		}
		if len(tags) != 2 || !got[keep.ID] || !got[noNamespace.ID] {
			t.Fatalf("%s词表应只含非人物标签: %+v", name, tags)
		}
	}
}

// IMG-08（视频侧规则 5）：显式 RetryVideo 对已有人工标签的视频生效；自动路径仍不打。
func TestAITaggingRetryVideoAnalyzesManuallyTaggedVideoIMG08(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := p015Tag(t, "动作", "custom")
	video := p015Video(t, "manual-retry.mp4")
	p015AttachManualVideoTag(t, video.ID)
	client := &fakeAITaggingClient{suggestions: []AITagSuggestion{{
		Label: tag.Name, MatchedExistingName: tag.Name, Confidence: models.AITagConfidenceHigh,
	}}}
	svc := newTestAITaggingService(client, nil)

	// 自动路径：没有显式重试标记，有人工标签的视频不进目标集。
	svc.runWorkerOnce(context.Background())
	if client.calls != 0 {
		t.Fatalf("自动路径不应给已有人工标签的视频打标，AI 调用 %d 次", client.calls)
	}

	if err := svc.RetryVideo(video.ID); err != nil {
		t.Fatalf("显式重新分析失败: %v", err)
	}
	svc.runWorkerOnce(context.Background())
	if client.calls != 1 {
		t.Fatalf("显式重新分析应调用 AI 一次，实际 %d", client.calls)
	}
	var count int64
	database.DB.Model(&models.AITagCandidate{}).
		Where("video_id = ? AND status = ?", video.ID, models.AITagCandidateStatusPending).Count(&count)
	if count != 1 {
		t.Fatalf("应产生 1 条待审候选，实际 %d", count)
	}
	var state models.AITaggingState
	if err := database.DB.Where("video_id = ?", video.ID).First(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state.Status != models.AITaggingStateStatusCompleted || state.SkipReason != "" {
		t.Fatalf("状态应为 completed 且清掉重试标记: %+v", state)
	}
}

// IMG-08：图片显式重新分析对已有手工标签的图片生效；批量（自动）路径仍跳过。
func TestImageAITaggingRetryAnalyzesManuallyTaggedImageIMG08(t *testing.T) {
	var calls int32
	client := imageTaggingClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) ([]AITagSuggestion, error) {
		atomic.AddInt32(&calls, 1)
		return []AITagSuggestion{{Label: "海边", Confidence: "high"}}, nil
	})
	svc := newImageAITaggingTestService(t, client)
	imageAITaggingTestLibrary(t, "海边")
	img := imageAITaggingTestImage(t, "heic")
	manual := models.Tag{Name: "我自己打的"}
	if err := database.DB.Create(&manual).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Exec("INSERT INTO image_tags(image_id, tag_id) VALUES (?, ?)", img.ID, manual.ID).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := svc.StartImageAITagging(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	waitImageAITagging(t, svc)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("批量路径不应给有手工标签的图片打标，调用 %d 次", got)
	}

	candidates, err := svc.RetryImageAITagging(img.ID)
	if err != nil {
		t.Fatalf("显式重新分析失败: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 || len(candidates) != 1 {
		t.Fatalf("显式重新分析应调用 AI 并产出候选: calls=%d candidates=%d", got, len(candidates))
	}
}

// IMG-14：已删除图片的「全部拒绝」允许执行；待审计数只算活跃图片。
func TestImageAITaggingRejectAllOnDeletedImageAndSummaryIMG14(t *testing.T) {
	svc := newImageAITaggingTestService(t, imageTaggingClientFunc(nil))
	library := imageAITaggingTestLibrary(t, "海边", "日落")
	active := imageAITaggingTestImage(t, "heic")
	deleted := imageAITaggingTestImage(t, "heic")
	seedImageAITagCandidate(t, active.ID, library[0], models.AITagConfidenceHigh)
	seedImageAITagCandidate(t, deleted.ID, library[0], models.AITagConfidenceHigh)
	seedImageAITagCandidate(t, deleted.ID, library[1], models.AITagConfidenceMedium)
	if err := database.DB.Delete(deleted).Error; err != nil {
		t.Fatal(err)
	}

	summary, err := svc.GetImageAITaggingSummary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Pending != 1 || summary.PendingImages != 1 {
		t.Fatalf("待审计数应排除已删除图片: %+v", summary)
	}

	rejected, err := svc.RejectPendingImageAITagCandidatesByImage(deleted.ID)
	if err != nil || rejected != 2 {
		t.Fatalf("已删除图片应可整体拒绝: rejected=%d err=%v", rejected, err)
	}
	// 批准路径的活跃校验不变。
	other := seedImageAITagCandidate(t, deleted.ID, library[0], models.AITagConfidenceHigh)
	if _, err := svc.ApproveImageAITagCandidate(other.ID); err == nil {
		t.Fatal("已删除图片的候选仍不允许批准")
	}
}

// APP-04：空闲门挡住时启动批次不执行，显式唤醒直通并摘掉门口的等待。
func TestAITaggingWorkerGatedRoundWaitsExplicitTriggerPassesAPP04(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := p015Tag(t, "动作", "custom")
	video := p015Video(t, "gated.mp4")
	client := &fakeAITaggingClient{suggestions: []AITagSuggestion{{
		Label: tag.Name, MatchedExistingName: tag.Name, Confidence: models.AITagConfidenceHigh,
	}}}
	svc := newTestAITaggingService(client, nil)
	gate := busyGate()
	svc.SetIdleGate(gate)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)
	defer svc.StopAndWait()

	pendingCount := func() int64 {
		var count int64
		database.DB.Model(&models.AITagCandidate{}).
			Where("video_id = ? AND status = ?", video.ID, models.AITagCandidateStatusPending).Count(&count)
		return count
	}
	waitFor(t, 2*time.Second, "启动批次应在门口登记等待", func() bool {
		waiting := gate.GetIdleSchedulerStatus().Waiting
		return len(waiting) == 1 && waiting[0].TaskKey == string(BackgroundTaskAITagging)
	})
	time.Sleep(50 * time.Millisecond)
	if pendingCount() != 0 {
		t.Fatal("用户活跃时定时/启动轮次不应执行")
	}

	if !svc.Trigger() {
		t.Fatal("显式唤醒应被接受")
	}
	waitFor(t, 3*time.Second, "显式唤醒应直通并处理视频", func() bool { return pendingCount() == 1 })
	waitFor(t, 2*time.Second, "显式唤醒应摘掉门口的等待", func() bool {
		return len(gate.GetIdleSchedulerStatus().Waiting) == 0
	})
}

// APP-04：机器空闲下来后，被挡住的自动轮次放行并处理视频。
func TestAITaggingWorkerGatedRoundRunsWhenIdleAPP04(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := p015Tag(t, "动作", "custom")
	video := p015Video(t, "gated-idle.mp4")
	client := &fakeAITaggingClient{suggestions: []AITagSuggestion{{
		Label: tag.Name, MatchedExistingName: tag.Name, Confidence: models.AITagConfidenceHigh,
	}}}
	svc := newTestAITaggingService(client, nil)
	var mu sync.Mutex
	idle := time.Second
	gate := newTestIdleGate(
		IdleSchedulerSettings{Enabled: true, ThresholdMinutes: 5},
		func(context.Context) (IdleSample, error) {
			mu.Lock()
			defer mu.Unlock()
			return IdleSample{Idle: idle, OnACPower: true}, nil
		},
	)
	svc.SetIdleGate(gate)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)
	defer svc.StopAndWait()

	waitFor(t, 2*time.Second, "应先在门口等待", func() bool { return len(gate.GetIdleSchedulerStatus().Waiting) == 1 })
	mu.Lock()
	idle = 30 * time.Minute
	mu.Unlock()
	waitFor(t, 3*time.Second, "空闲后应放行并处理视频", func() bool {
		var count int64
		database.DB.Model(&models.AITagCandidate{}).
			Where("video_id = ? AND status = ?", video.ID, models.AITagCandidateStatusPending).Count(&count)
		return count == 1
	})
}

func waitFor(t *testing.T, timeout time.Duration, message string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(message)
}
