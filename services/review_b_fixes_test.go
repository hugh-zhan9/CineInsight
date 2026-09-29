package services

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"

	"golang.org/x/text/encoding/simplifiedchinese"
	"gorm.io/gorm"
)

// 本文件验收复审 B 的修复项（字幕、AI 批量批准、标签重排、片单撤销）；测试名里的问题 ID
// 与问题清单一一对应。

// ===== MEDIA-01：替换失败连续两次不丢「已带译文」 =====

func TestMEDIA01ReplaceFailingTwiceKeepsTranslationAppliedAndTranslatesOnce(t *testing.T) {
	setupVideoServiceTestDB(t)
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", []byte(writerTestSRT))
	server, prompts := startPromptCapturingLLMServer(t, func(string) string {
		return `{"choices":[{"message":{"content":"{\"translations\":[\"你好\",\"克诺比将军\"]}"}}]}`
	})
	// BaseDir 为空时已有字幕无法备份，Replace 必然失败：正是「收尾替换失败」的场景。
	service := NewSubtitleService("")
	service.glossaryResolver = func(uint, string) ([]GlossaryTerm, error) { return nil, nil }
	req := SubtitleGenerateRequest{VideoID: video.ID, Engine: SubtitleEngineWhisperX, SourceLang: "en"}
	options := SubtitleGenerateOptions{
		BilingualEnabled: true, BilingualLang: "zh",
		TranslationConfig: SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"},
	}

	// 第一次：校验通过，译文合并进临时文件，替换失败。
	if _, err := service.commitTranscription(context.Background(), 1, req, video.Path, "en", realSegments(), options); err == nil {
		t.Fatal("第一次收尾的替换应失败")
	}
	if artifact := service.peekPendingSubtitle(video.ID); artifact == nil || !artifact.TranslationApplied {
		t.Fatalf("第一次失败后登记应带「已合并译文」: %+v", artifact)
	}
	// 第二次：强制重试（这一轮关掉了双语），替换仍失败。
	force := options
	force.ForceGenerate = true
	if _, err := service.executeSubtitleTask(context.Background(), 2, req, video.Path, force); err == nil {
		t.Fatal("第二次（强制重试）替换仍应失败")
	}
	if artifact := service.peekPendingSubtitle(video.ID); artifact == nil || !artifact.TranslationApplied {
		t.Fatalf("连续第二次失败后登记仍必须带「已合并译文」: %+v", artifact)
	}
	// 第三次：成功。
	service.BaseDir = t.TempDir()
	result, err := service.executeSubtitleTask(context.Background(), 3, req, video.Path, force)
	if err != nil || result.Status != SubtitleResultStatusSuccess {
		t.Fatalf("第三次应收尾成功: %+v err=%v", result, err)
	}
	if len(*prompts) != 1 {
		t.Fatalf("翻译器只应被调用一次，实际 %d 次", len(*prompts))
	}
	segments, err := subtitleparser.ParseFile(srtPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Hello there\n你好", "General Kenobi\n克诺比将军"}
	if len(segments) != len(want) {
		t.Fatalf("最终字幕条数不符: %+v", segments)
	}
	for i, segment := range segments {
		if segment.Text != want[i] {
			t.Fatalf("第 %d 条应是原文加一行译文（不是双语的双语）:\n got %q\nwant %q", i+1, segment.Text, want[i])
		}
	}
}

// ===== MEDIA-01：pending 临时字幕是同目录隐藏文件，不被当成旁挂字幕 =====

func TestMEDIA01PendingSubtitleIsHiddenInSameDirectoryAndNotASidecar(t *testing.T) {
	setupVideoServiceTestDB(t)
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", []byte(writerTestSRT))
	dir := filepath.Dir(srtPath)
	service := NewSubtitleService(t.TempDir())
	req := SubtitleGenerateRequest{VideoID: video.ID, Engine: SubtitleEngineWhisperX, SourceLang: "en"}

	result, err := service.commitTranscription(context.Background(), 1, req, video.Path, "en", hallucinatedSegments(), SubtitleGenerateOptions{})
	if err != nil || result.Status != SubtitleResultStatusValidationFailed {
		t.Fatalf("应校验失败并保留 pending: %+v err=%v", result, err)
	}
	pendingPath := filepath.Join(dir, ".movie.cineinsight-pending.srt")
	if got := subtitlePendingPath(srtPath); got != pendingPath {
		t.Fatalf("pending 应是同目录的隐藏文件: %s", got)
	}
	if _, err := os.Stat(pendingPath); err != nil {
		t.Fatalf("pending 临时文件应在同目录: %v", err)
	}
	if artifact := service.peekPendingSubtitle(video.ID); artifact == nil || artifact.SRTPath != pendingPath {
		t.Fatalf("登记应指向隐藏的 pending 文件: %+v", artifact)
	}
	// 旧命名的遗留文件与新命名都不算旁挂字幕。
	if err := os.WriteFile(filepath.Join(dir, "movie.cineinsight-pending.srt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if names, err := ListSidecarSubtitles(video.Path); err != nil || len(names) != 0 {
		t.Fatalf("应用临时文件不算旁挂字幕: %v %v", names, err)
	}
	for _, name := range []string{".movie.cineinsight-pending.srt", "movie.cineinsight-pending.srt", ".MOVIE.CINEINSIGHT-PENDING.SRT"} {
		if IsSidecarSubtitleName(video.Path, name) {
			t.Fatalf("%s 不应被判为旁挂字幕", name)
		}
	}
	// 放弃时删掉的是隐藏文件。
	if err := service.DiscardPendingSubtitle(video.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("放弃后隐藏的 pending 应删除: %v", err)
	}
}

func TestMEDIA01PendingPathRoundTripRejectsLegacyAndMalformedNames(t *testing.T) {
	if got := subtitlePendingPath("/a/movie.srt"); got != "/a/.movie.cineinsight-pending.srt" {
		t.Fatalf("pending 路径: %s", got)
	}
	for _, srt := range []string{"/a/movie.srt", "/a/.hidden.srt", "/a/b.c.srt"} {
		got, err := subtitleFinalPathForPending(subtitlePendingPath(srt))
		if err != nil || got != srt {
			t.Fatalf("往返 %s: %q %v", srt, got, err)
		}
	}
	for _, bad := range []string{"/a/movie.cineinsight-pending.srt", "/a/.cineinsight-pending.srt", "/a/..cineinsight-pending.srt", "/a/.movie.srt"} {
		if _, err := subtitleFinalPathForPending(bad); err == nil {
			t.Fatalf("%s 不是 pending 形态，必须报错", bad)
		}
	}
}

// ===== MEDIA-05：字幕文件锁的键不区分大小写 =====

func TestMEDIA05SubtitleLockKeyIgnoresCase(t *testing.T) {
	unlock := lockSubtitleFile("/Media/Movie.srt")
	released := false
	defer func() {
		if !released {
			unlock()
		}
	}()
	acquired := make(chan func(), 1)
	go func() { acquired <- lockSubtitleFile("/media/./movie.SRT") }()
	select {
	case release := <-acquired:
		release()
		t.Fatal("大小写不同的同一 .srt（大小写不敏感文件系统上是同一个文件）必须互斥")
	case <-time.After(150 * time.Millisecond):
	}
	released = true
	unlock()
	select {
	case release := <-acquired:
		release()
	case <-time.After(5 * time.Second):
		t.Fatal("释放后应能拿到锁")
	}
}

// ===== MEDIA-02：歧义编码不给选择就拒绝转换 =====

func TestMEDIA02ConvertAmbiguousGBKWithoutChoiceIsRejectedWithCandidates(t *testing.T) {
	setupVideoServiceTestDB(t)
	encodeGBK := func(s string) []byte {
		t.Helper()
		raw, err := simplifiedchinese.GBK.NewEncoder().String(s)
		if err != nil {
			t.Fatal(err)
		}
		return []byte(raw)
	}
	// 实测（见 subtitleparser 的 TestDetectSubtitleEncodingGBKSampleAmbiguityMEDIA02）：这句短 GBK
	// 字幕按 Big5 也能「干净」解码，判为歧义。
	short := "1\n00:00:00,000 --> 00:00:01,000\n“你好！”他说。\n"
	raw := encodeGBK(short)
	video, srtPath := mustCreateSubtitleVideo(t, "short.mp4", raw)
	workbench := NewSubtitleWorkbenchService(nil)
	workbench.dataDir = t.TempDir()

	_, err := workbench.ConvertToUTF8(video, "")
	var coded *SubtitleCodedError
	if !errors.As(err, &coded) || coded.Code != SubtitleErrorEncodingAmbiguous {
		t.Fatalf("歧义且未选编码时应返回 subtitle_encoding_ambiguous: %v", err)
	}
	if coded.DetectedEncoding != subtitleparser.EncodingGB18030 || len(coded.Candidates) != 2 ||
		coded.Candidates[0].Encoding != subtitleparser.EncodingGB18030 || coded.Candidates[1].Encoding != subtitleparser.EncodingBig5 {
		t.Fatalf("应带回两个候选供用户选择: %+v", coded)
	}
	if !bytes.Equal(mustReadBytes(t, srtPath), raw) {
		t.Fatal("拒绝转换时文件不得改动")
	}
	if backups, _ := workbench.writer().ListBackups(video.ID); len(backups) != 0 {
		t.Fatalf("拒绝转换时不应产生备份: %+v", backups)
	}
	// 用户选定 gb18030 之后照常转换。
	result, err := workbench.ConvertToUTF8(video, subtitleparser.EncodingGB18030)
	if err != nil || result.Encoding != subtitleparser.EncodingGB18030 {
		t.Fatalf("选定编码后应能转换: %+v err=%v", result, err)
	}
	if got := string(mustReadBytes(t, srtPath)); got != short {
		t.Fatalf("写回的应是正确的简体文字: %q", got)
	}

	// 多行、带标点的常见 GBK 字幕不歧义，不选编码也能直接转换。
	long := "1\n00:00:00,000 --> 00:00:01,000\n你好，世界。今天天气很好，我们一起去公园散步吧。\n\n2\n00:00:01,000 --> 00:00:02,000\n这是第二句字幕，用来提高识别的可靠性。\n"
	longVideo, longPath := mustCreateSubtitleVideo(t, "long.mp4", encodeGBK(long))
	if result, err := workbench.ConvertToUTF8(longVideo, ""); err != nil || result.Encoding != subtitleparser.EncodingGB18030 {
		t.Fatalf("不歧义的 GBK 字幕应直接转换: %+v err=%v", result, err)
	}
	if got := string(mustReadBytes(t, longPath)); got != long {
		t.Fatalf("写回内容不符: %q", got)
	}
}

// ===== META-01：删除词表外标签只做定向重排 =====

func TestTagPersonConversionMETA01ConvertResetsOnlyVideosLeftWithoutManualTags(t *testing.T) {
	tag, videos, _ := conversionFixture(t)
	personOnly := videos[0]
	mixed := p017Video(t, "mixed.mp4")
	manual := models.Tag{Name: "科幻"}
	if err := database.DB.Create(&manual).Error; err != nil {
		t.Fatal(err)
	}
	for _, link := range [][2]uint{{mixed.ID, tag.ID}, {mixed.ID, manual.ID}} {
		if err := database.DB.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?)", link[0], link[1]).Error; err != nil {
			t.Fatal(err)
		}
	}
	untagged := p017Video(t, "untagged.mp4")
	for _, id := range []uint{personOnly.ID, mixed.ID, untagged.ID} {
		p017AIState(t, id)
	}
	// 词表内标签的待审候选不受人物标签转换影响。
	other := p015Candidate(t, untagged.ID, manual, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)

	if _, err := (&TagService{}).ConvertTagToPerson(TagPersonConversionRequest{TagID: tag.ID, TagName: tag.Name, CreateNew: true}); err != nil {
		t.Fatal(err)
	}
	if got := p017Fingerprint(t, personOnly.ID); got != "" {
		t.Fatalf("只挂该人物标签的视频转换后没有人工标签，应重置: fingerprint=%q", got)
	}
	for name, id := range map[string]uint{"仍有人工标签的视频": mixed.ID, "本来就没标签的视频": untagged.ID} {
		var state models.AITaggingState
		if err := database.DB.Where("video_id = ?", id).First(&state).Error; err != nil {
			t.Fatal(err)
		}
		if state.EvidenceFingerprint != "fp" || state.Status != models.AITaggingStateStatusCompleted {
			t.Fatalf("%s不得被重排: %+v", name, state)
		}
	}
	if got := p015CandidateStatus(t, other.ID); got != models.AITagCandidateStatusPending {
		t.Fatalf("无关候选不应被作废: %s", got)
	}
}

// 删除词表外标签时，指向它的待审候选（视频、图片两侧）照样失效。
func TestTagServiceMETA01DeletePersonTagSupersedesItsPendingCandidates(t *testing.T) {
	setupVideoServiceTestDB(t)
	person := models.Tag{Name: "王五", Namespace: personTagNamespace}
	keep := models.Tag{Name: "悬疑"}
	for _, tag := range []*models.Tag{&person, &keep} {
		if err := database.DB.Create(tag).Error; err != nil {
			t.Fatal(err)
		}
	}
	video := p017Video(t, "legacy.mp4")
	stale := p015Candidate(t, video.ID, person, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	unrelated := p015Candidate(t, video.ID, keep, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	image := models.Image{Name: "a.jpg", Path: "/review-b/a.jpg"}
	if err := database.DB.Create(&image).Error; err != nil {
		t.Fatal(err)
	}
	personID := person.ID
	imageCandidate := models.ImageAITagCandidate{ImageID: image.ID, SuggestedName: person.Name, NormalizedName: person.Name,
		MatchedTagID: &personID, Confidence: models.AITagConfidenceHigh, Status: models.AITagCandidateStatusPending}
	if err := database.DB.Create(&imageCandidate).Error; err != nil {
		t.Fatal(err)
	}

	if err := (&TagService{}).DeleteTag(person.ID); err != nil {
		t.Fatal(err)
	}
	if got := p015CandidateStatus(t, stale.ID); got != models.AITagCandidateStatusSuperseded {
		t.Fatalf("指向已删标签的视频候选应失效: %s", got)
	}
	if got := p015CandidateStatus(t, unrelated.ID); got != models.AITagCandidateStatusPending {
		t.Fatalf("其他标签的候选不应被动: %s", got)
	}
	var reloaded models.ImageAITagCandidate
	if err := database.DB.First(&reloaded, imageCandidate.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != models.AITagCandidateStatusSuperseded {
		t.Fatalf("指向已删标签的图片候选应失效: %s", reloaded.Status)
	}
}

// ===== META-11：批量批准的 superseded 文案按真实原因给；计数预览同视频去重 =====

func TestApproveAITagCandidatesMETA11SupersededMessagesFollowRealCause(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	tagSvc := &TagService{}
	tag := p015Tag(t, "动作", "类型")

	// 同标签的另一条候选先被批准。
	v1 := p015Video(t, "sibling.mp4")
	first := p015Candidate(t, v1.ID, tag, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	second := p015Candidate(t, v1.ID, tag, models.AITagConfidenceMedium, models.AITagCandidateStatusPending)
	// 用户手动加了该标签。
	v2 := p015Video(t, "manual.mp4")
	manual := p015Candidate(t, v2.ID, tag, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	if err := database.Transaction(func(tx *gorm.DB) error {
		_, err := SupersedeCandidatesForManualTag(tx, v2.ID, tag.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// 手动重新分析作废了待审候选。
	v3 := p015Video(t, "retry.mp4")
	reanalysed := p015Candidate(t, v3.ID, tag, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	if err := svc.RetryVideo(v3.ID); err != nil {
		t.Fatal(err)
	}
	// 标签被删除（词表内，全量对账作废候选）。
	deletedTag := p015Tag(t, "悬疑", "类型")
	v4 := p015Video(t, "deleted.mp4")
	deletedCandidate := p015Candidate(t, v4.ID, deletedTag, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	if err := tagSvc.DeleteTag(deletedTag.ID); err != nil {
		t.Fatal(err)
	}
	// 标签改进「人物」分类，出了 AI 词表。
	movedTag := p015Tag(t, "赵六", "类型")
	v5 := p015Video(t, "moved.mp4")
	movedCandidate := p015Candidate(t, v5.ID, movedTag, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	if err := tagSvc.UpdateTagWithCategory(movedTag.ID, movedTag.Name, movedTag.Color, personTagNamespace); err != nil {
		t.Fatal(err)
	}
	// 标签被软删、候选却还挂着 pending（绕过对账的历史数据）：不得落成「候选不存在」。
	softTag := p015Tag(t, "爱情", "类型")
	v6 := p015Video(t, "soft.mp4")
	softCandidate := p015Candidate(t, v6.ID, softTag, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	if err := database.DB.Delete(&softTag).Error; err != nil {
		t.Fatal(err)
	}

	result := svc.ApproveCandidates([]uint{first.ID, second.ID, manual.ID, reanalysed.ID, deletedCandidate.ID, movedCandidate.ID, softCandidate.ID})
	byID := map[uint]AITagBatchItemResult{}
	for _, item := range result.Results {
		byID[item.ID] = item
	}
	if !byID[first.ID].OK {
		t.Fatalf("第一条应批准成功: %+v", byID[first.ID])
	}
	for _, tc := range []struct {
		name    string
		id      uint
		message string
	}{
		{"同标签其他候选已批准", second.ID, "同标签的其他候选已批准"},
		{"已手动添加", manual.ID, "已手动添加该标签"},
		{"重新分析", reanalysed.ID, "视频已重新分析（词表变化或手动重新分析），这条候选已失效"},
		{"标签已删除", deletedCandidate.ID, "标签已删除或已合并"},
		{"出了词表", movedCandidate.ID, "候选标签已不在 AI 词表中"},
	} {
		if got := byID[tc.id]; got.OK || !got.Superseded || got.Message != tc.message {
			t.Errorf("%s：应为 superseded「%s」，实际 %+v", tc.name, tc.message, got)
		}
	}
	if got := byID[softCandidate.ID]; got.OK || got.Superseded || got.Message != "标签已删除或已合并，无法批准" {
		t.Errorf("标签已软删的 pending 候选应报「标签已删除或已合并」而不是「候选不存在」: %+v", got)
	}
	if result.Succeeded != 1 || result.Superseded != 5 || result.Failed != 1 || result.Requested != 7 {
		t.Fatalf("计数应为 成功1/作废5/失败1: %+v", result)
	}
}

func TestCountAITagCandidatesByFilterMETA11DedupesSameVideoSameTag(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := newTestAITaggingService(&fakeAITaggingClient{}, nil)
	tag := p015Tag(t, "动作", "类型")
	v1 := p015Video(t, "dup.mp4")
	v2 := p015Video(t, "single.mp4")
	p015Candidate(t, v1.ID, tag, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	p015Candidate(t, v1.ID, tag, models.AITagConfidenceMedium, models.AITagCandidateStatusPending)
	p015Candidate(t, v2.ID, tag, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)

	filter := AITagCandidateFilter{TagID: tag.ID}
	count, err := svc.CountCandidatesByFilter(filter)
	if err != nil || count != 2 {
		t.Fatalf("同视频同标签的两条只算一次，预览应为 2: %d %v", count, err)
	}
	result, err := svc.ApproveCandidatesByFilter(filter)
	if err != nil {
		t.Fatal(err)
	}
	if result.Succeeded != count || result.Superseded != 1 || result.Requested != 3 || result.Failed != 0 {
		t.Fatalf("实际批准数应与预览一致（其余一条被同标签批准作废）: count=%d %+v", count, result)
	}

	// 标签被软删后，挂在它上面的 pending 候选批不了，预览也不计。
	softTag := p015Tag(t, "爱情", "类型")
	p015Candidate(t, v2.ID, softTag, models.AITagConfidenceHigh, models.AITagCandidateStatusPending)
	if err := database.DB.Delete(&softTag).Error; err != nil {
		t.Fatal(err)
	}
	if count, err := svc.CountCandidatesByFilter(AITagCandidateFilter{TagID: softTag.ID}); err != nil || count != 0 {
		t.Fatalf("已删除标签的候选不应计入预览: %d %v", count, err)
	}
}

// ===== APP-07：复用条目以 reuse 精确认领 =====

// 榜单 want 复用用户条目：认领写被复用条目的 ID 与 reuse；手动取消、改标记、看完视频的自动路径
// 都只清认领、不删条目；片单页仍标「手动」。
func TestAPP07ReuseClaimIsExactAndChartUndoNeverDeletesUserEntry(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	manual, err := h.watchlist.Create("沙丘", models.WatchlistKindMovie)
	if err != nil {
		t.Fatal(err)
	}
	h.seedEntry("201", "沙丘", "2021-10-22")
	statements := h.watchStatements()
	assertClaim := func(stage string, wantEntryID uint, wantOrigin string) {
		t.Helper()
		row, _ := h.markRow("201")
		if row.WatchlistEntryID != wantEntryID || row.WatchlistEntryOrigin != wantOrigin {
			t.Fatalf("%s：认领应为 (%d, %q)，实际 %+v", stage, wantEntryID, wantOrigin, row)
		}
	}
	assertEntryKept := func(stage string) {
		t.Helper()
		entries := h.watchlistEntries()
		if len(entries) != 1 || entries[0].ID != manual.ID {
			t.Fatalf("%s：用户的条目必须保留: %+v", stage, entries)
		}
	}

	result, err := h.service.MarkEntry("201", models.MovieChartMarkWant)
	if err != nil || !result.WatchlistConflict || result.WatchlistCreated {
		t.Fatalf("复用应报冲突、不报新建: %+v err=%v", result, err)
	}
	assertClaim("复用", manual.ID, movieChartOriginReuse)
	page, err := h.watchlist.List("", 0, 50)
	if err != nil || page.Origins[manual.ID] != WatchlistOriginManual {
		t.Fatalf("被复用的用户条目仍标手动: %+v err=%v", page, err)
	}

	// 看完视频的自动路径：want → watched，条目保留、认领清空。
	video := h.seedVideo("沙丘.mkv", "沙丘")
	if err := h.service.LinkMovieToVideo("201", video.ID); err != nil {
		t.Fatal(err)
	}
	h.service.OnVideoWatchedChanged(video.ID, true)
	if row, _ := h.markRow("201"); row.Mark != models.MovieChartMarkWatched {
		t.Fatalf("自动路径应标已看: %+v", row)
	}
	assertClaim("自动已看后", 0, "")
	assertEntryKept("自动已看")

	// 再想看（按豆瓣 ID 复用同一条），改成不想看：条目保留。
	if _, err := h.service.MarkEntry("201", models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	assertClaim("再次复用", manual.ID, movieChartOriginReuse)
	if _, err := h.service.MarkEntry("201", models.MovieChartMarkSkip); err != nil {
		t.Fatal(err)
	}
	assertClaim("改标记后", 0, "")
	assertEntryKept("改标记")

	// 再想看，然后手动取消：条目保留。
	if _, err := h.service.MarkEntry("201", models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	if err := h.service.ClearMark("201"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.markRow("201"); ok {
		t.Fatal("取消想看应删除标记行")
	}
	assertEntryKept("取消想看")
	if deleted := statements.count(deleteWatchlistStatement); deleted != 0 {
		t.Fatalf("整个用例不应对 watchlist_entries 发出任何 DELETE: %v", statements.snapshot())
	}
}

// 复用后改名：认领包含 reuse，来源不清；再删除条目仍按 ID 撤销那个 want。
func TestAPP07DeleteReusedEntryAfterRenameStillRevokesWant(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	manual := mustCreateEnrichedEntry(t, "沙丘", "tmdb", "438631")
	h.seedEntry("201", "沙丘", "2021-10-22")
	if _, err := h.service.MarkEntry("201", models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	if err := h.watchlist.Update(manual.ID, "沙丘：第一部"); err != nil {
		t.Fatal(err)
	}
	if got := reloadWatchlistEntry(t, manual.ID); got.SourceName != "tmdb" || got.SourceItemID != "438631" {
		t.Fatalf("被复用认领的条目改名不得清来源: %+v", got)
	}
	if err := h.watchlist.Delete(manual.ID); err != nil {
		t.Fatal(err)
	}
	row, _ := h.markRow("201")
	if row.Mark != "" || row.WatchlistEntryID != 0 || row.WatchlistEntryOrigin != "" {
		t.Fatalf("改名后删除复用条目仍应撤销 want: %+v", row)
	}
}

// 同名三部片：同一条 TMDB 条目被三部同名片复用，删除它三个 want 都撤销；另一条同名的豆瓣
// 条目被改选来回之后，旧认领释放、不误删，删除时只撤销当下认领它的那个。
func TestAPP07SameTitleThreeFilmsAndReselectBoundaries(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	tmdbEntry := mustCreateEnrichedEntry(t, "沙丘", "tmdb", "438631")
	for _, spec := range [][2]string{{"1984", "1984-12-14"}, {"2000", "2000-12-03"}, {"2021", "2021-10-22"}} {
		h.seedEntry(spec[0], "沙丘", spec[1])
		if _, err := h.service.MarkEntry(spec[0], models.MovieChartMarkWant); err != nil {
			t.Fatal(err)
		}
		if row, _ := h.markRow(spec[0]); row.WatchlistEntryID != tmdbEntry.ID || row.WatchlistEntryOrigin != movieChartOriginReuse {
			t.Fatalf("%s 应复用同一条 TMDB 条目: %+v", spec[0], row)
		}
	}
	if got := len(h.watchlistEntries()); got != 1 {
		t.Fatalf("三部同名片都复用同一条，不得新建: %d", got)
	}
	if err := h.watchlist.Delete(tmdbEntry.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1984", "2000", "2021"} {
		if row, _ := h.markRow(id); row.Mark != "" || row.WatchlistEntryID != 0 {
			t.Fatalf("删除被复用的条目，%s 的 want 应撤销: %+v", id, row)
		}
	}

	// 来回改选：手动条目被 1984 复用（补写豆瓣来源）→ 改选 2021 → 旧 reuse 认领释放。
	manual, err := h.watchlist.Create("沙丘", models.WatchlistKindMovie)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.MarkEntry("1984", models.MovieChartMarkWant); err != nil {
		t.Fatal(err)
	}
	if row, _ := h.markRow("1984"); row.WatchlistEntryID != manual.ID || row.WatchlistEntryOrigin != movieChartOriginReuse {
		t.Fatalf("1984 应复用手动条目: %+v", row)
	}
	h.useWatchlistSources(map[WatchlistMetadataKind][]WatchlistMetadataSource{
		WatchlistMetadataKindMovie: {staticWatchlistSource(WatchlistMetadataSourceDouban, WatchlistMetadataDetail{
			WatchlistMetadataCandidate: WatchlistMetadataCandidate{SourceItemID: "2021", Title: "沙丘", Year: 2021},
		})},
	})
	if err := h.watchlist.ApplyCandidate(manual.ID, "2021"); err != nil {
		t.Fatal(err)
	}
	if row, _ := h.markRow("1984"); row.Mark != "" || row.WatchlistEntryID != 0 || row.WatchlistEntryOrigin != "" {
		t.Fatalf("改选后 1984 的复用认领应释放: %+v", row)
	}
	if err := h.service.ClearMark("1984"); err != nil {
		t.Fatal(err)
	}
	if got := len(h.watchlistEntries()); got != 1 {
		t.Fatalf("释放后的旧标记被撤销不得删除条目: %d", got)
	}
	// 1984 再想看：条目已改选成 2021，不再是它的复用候选，另建一条。
	result, err := h.service.MarkEntry("1984", models.MovieChartMarkWant)
	if err != nil || !result.WatchlistCreated {
		t.Fatalf("1984 应另建条目: %+v err=%v", result, err)
	}
	owned, _ := h.markRow("1984")
	// 删除改选后的手动条目：只撤销当下认领它的 2021，1984 的新条目与标记不动。
	if err := h.watchlist.Delete(manual.ID); err != nil {
		t.Fatal(err)
	}
	if row, _ := h.markRow("2021"); row.Mark != "" || row.WatchlistEntryID != 0 {
		t.Fatalf("2021 的 want 应随条目删除而撤销: %+v", row)
	}
	if row, _ := h.markRow("1984"); row.Mark != models.MovieChartMarkWant || row.WatchlistEntryID != owned.WatchlistEntryID || owned.WatchlistEntryID == 0 {
		t.Fatalf("1984 的 want 不得受影响: %+v", row)
	}
}

// 历史行（复用时未记认领）仍按片名兜底撤销：两边去首尾空白后比较；已认领别的条目的 want、
// 片名不同的历史行都不动。
func TestAPP07LegacyUnclaimedWantStillRevokedByTrimmedTitle(t *testing.T) {
	h := newMovieChartMarkHarness(t)
	manual := mustCreateEnrichedEntry(t, "沙丘", "tmdb", "438631")
	other := mustCreateEnrichedEntry(t, "别的条目", "tmdb", "1")
	now := h.service.now()
	rows := []models.MovieChartMark{
		{DoubanID: "201", Mark: models.MovieChartMarkWant, Title: "  沙丘 ", MarkedAt: now},
		{DoubanID: "202", Mark: models.MovieChartMarkWant, Title: "别的片", MarkedAt: now},
		{DoubanID: "203", Mark: models.MovieChartMarkWant, Title: "沙丘", MarkedAt: now,
			WatchlistEntryID: other.ID, WatchlistEntryOrigin: models.MovieChartOriginChart},
	}
	for i := range rows {
		if err := h.db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := h.watchlist.Delete(manual.ID); err != nil {
		t.Fatal(err)
	}
	if row, _ := h.markRow("201"); row.Mark != "" {
		t.Fatalf("历史行应按去空白后的片名撤销: %+v", row)
	}
	if row, _ := h.markRow("202"); row.Mark != models.MovieChartMarkWant {
		t.Fatalf("片名不同的历史行不得撤销: %+v", row)
	}
	if row, _ := h.markRow("203"); row.Mark != models.MovieChartMarkWant || row.WatchlistEntryID != other.ID {
		t.Fatalf("认领着别的条目的 want 不得撤销: %+v", row)
	}
}
