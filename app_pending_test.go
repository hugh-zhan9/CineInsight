package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"
)

// P-024 待处理收件箱（D-PC27、META-08）。

// META-08：清理候选按视频对去重——已在同一个精确 / 近似重复组里的一对，在同源或截取片段里
// 不再另算；同源与截取片段里的同一对只算一次；极短 / 极低清按视频逐条算。
func TestMETA08CleanupCandidatesDedupeByVideoPair(t *testing.T) {
	video := func(id uint) models.Video { return models.Video{ID: id} }
	analysis := &services.CleanupAnalysis{
		DuplicateGroups:     []services.CleanupDuplicateGroup{{Original: video(1), Candidates: []models.Video{video(2)}}},
		NearDuplicateGroups: []services.CleanupDuplicateGroup{{Original: video(3), Candidates: []models.Video{video(4), video(5)}}},
		SameSourceGroups: []services.CleanupSameSourceGroup{
			{Preferred: video(2), Alternative: video(1)}, // 已在精确重复组里
			{Preferred: video(5), Alternative: video(3)}, // 已在近似重复组里
			{Preferred: video(6), Alternative: video(7)},
		},
		ClipGroups: []services.CleanupClipGroup{
			{Full: video(7), Clip: video(6)}, // 与同源里的那一对相同
			{Full: video(8), Clip: video(9)},
		},
		LowDuration:   []models.Video{video(10)},
		LowResolution: []models.Video{video(10), video(11)},
	}
	if got := countCleanupReviewEntries(analysis); got != 7 {
		t.Fatalf("去重后应为 2 组 + 2 对 + 3 个单项 = 7，实际 %d", got)
	}
	if got := countCleanupReviewEntries(nil); got != 0 {
		t.Fatalf("没有分析结果时应为 0，实际 %d", got)
	}
}

// META-08：各项计数与对应审阅面板背后的服务接口同一口径：软删的媒体、已处理的候选、已确认
// 或已否决的同源、已忽略的清理候选都不计；Total 不含失败的 AI 打标。
func TestMETA08PendingWorkSummaryMatchesServiceCounts(t *testing.T) {
	setupAppTestDB(t)
	root := t.TempDir()
	if err := database.DB.Create(&models.ScanDirectory{Path: root, Alias: "片库"}).Error; err != nil {
		t.Fatal(err)
	}
	writeVideo := func(name string, content []byte, duration float64, width, height int) models.Video {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		video := models.Video{
			Name: name, Path: path, Directory: root, Size: int64(len(content)),
			Duration: duration, Resolution: "x", Width: width, Height: height,
		}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		return video
	}
	duplicate := bytes.Repeat([]byte("same-content-"), 4096)
	dupA := writeVideo("dup-a.mp4", duplicate, 600, 1920, 1080)
	dupB := writeVideo("dup-b.mp4", duplicate, 600, 1920, 1080)
	short := writeVideo("short.mp4", bytes.Repeat([]byte("short-"), 100), 2, 1920, 1080)
	lowRes := writeVideo("low.mp4", bytes.Repeat([]byte("low-"), 300), 600, 100, 80)
	trashed := writeVideo("trashed.mp4", bytes.Repeat([]byte("trashed-"), 50), 600, 1920, 1080)
	if err := database.DB.Delete(&trashed).Error; err != nil {
		t.Fatal(err)
	}

	create := func(value interface{}) {
		t.Helper()
		if err := database.DB.Create(value).Error; err != nil {
			t.Fatalf("写入 %T 失败: %v", value, err)
		}
	}
	// 视频 AI 候选：dupA 的两条 pending 计入；已批准的、软删视频上的不计。
	create(&models.AITagCandidate{VideoID: dupA.ID, SuggestedName: "海边", NormalizedName: "海边", Confidence: "high", Status: models.AITagCandidateStatusPending})
	create(&models.AITagCandidate{VideoID: dupA.ID, SuggestedName: "夜景", NormalizedName: "夜景", Confidence: "high", Status: models.AITagCandidateStatusPending})
	create(&models.AITagCandidate{VideoID: short.ID, SuggestedName: "城市", NormalizedName: "城市", Confidence: "high", Status: models.AITagCandidateStatusApproved})
	create(&models.AITagCandidate{VideoID: trashed.ID, SuggestedName: "森林", NormalizedName: "森林", Confidence: "high", Status: models.AITagCandidateStatusPending})
	// 打标失败：dupB 计入；软删视频上的不计；完成的不计。
	create(&models.AITaggingState{VideoID: dupB.ID, Status: models.AITaggingStateStatusFailed})
	create(&models.AITaggingState{VideoID: short.ID, Status: models.AITaggingStateStatusCompleted})
	create(&models.AITaggingState{VideoID: trashed.ID, Status: models.AITaggingStateStatusFailed})
	// 图片 AI 候选：活跃图片上的一条计入，软删图片上的不计。
	image := models.Image{Name: "a.jpg", Path: filepath.Join(root, "a.jpg"), Directory: root}
	trashedImage := models.Image{Name: "b.jpg", Path: filepath.Join(root, "b.jpg"), Directory: root}
	create(&image)
	create(&trashedImage)
	if err := database.DB.Delete(&trashedImage).Error; err != nil {
		t.Fatal(err)
	}
	create(&models.ImageAITagCandidate{ImageID: image.ID, SuggestedName: "猫", NormalizedName: "猫", Confidence: "high", Status: models.AITagCandidateStatusPending})
	create(&models.ImageAITagCandidate{ImageID: trashedImage.ID, SuggestedName: "狗", NormalizedName: "狗", Confidence: "high", Status: models.AITagCandidateStatusPending})
	// 同源：未确认的一对计入；已确认、已否决、涉及软删视频的不计。
	reviewed := time.Now()
	relation := func(a, b uint, status string, reviewedAt *time.Time) *models.VideoSameSourceRelation {
		return &models.VideoSameSourceRelation{
			VideoAID: a, VideoBID: b, VideoAFingerprint: "fa", VideoBFingerprint: "fb",
			Status: status, DetectionVersion: "test", IsUnread: true, ReviewedAt: reviewedAt,
		}
	}
	unconfirmed := relation(dupA.ID, dupB.ID, models.VideoSameSourceStatusDetected, nil)
	create(unconfirmed)
	// 已读但未确认的同样是待处理（is_unread 带 gorm default:true，零值写不进去，建完再改）。
	if err := database.DB.Model(unconfirmed).Update("is_unread", false).Error; err != nil {
		t.Fatal(err)
	}
	create(relation(dupA.ID, short.ID, models.VideoSameSourceStatusDetected, &reviewed))
	create(relation(short.ID, trashed.ID, models.VideoSameSourceStatusDetected, nil))
	create(relation(dupB.ID, short.ID, models.VideoSameSourceStatusRejected, &reviewed))
	// 建议作品集：待审的一条计入，已忽略的不计。
	pending := models.CollectionSuggestion{ScanRoot: root, SeriesName: "系列", NormalizedSeries: "系列", Status: models.CollectionSuggestionStatusPending, Fingerprint: "fp-pending"}
	dismissed := models.CollectionSuggestion{ScanRoot: root, SeriesName: "旧系列", NormalizedSeries: "旧系列", Status: models.CollectionSuggestionStatusDismissed, Fingerprint: "fp-dismissed"}
	create(&pending)
	create(&dismissed)
	create(&models.CollectionSuggestionMember{SuggestionID: pending.ID, VideoID: short.ID, Position: 1})
	create(&models.CollectionSuggestionMember{SuggestionID: pending.ID, VideoID: lowRes.ID, Position: 2})
	create(&models.CollectionSuggestionMember{SuggestionID: dismissed.ID, VideoID: dupA.ID, Position: 1})
	create(&models.CollectionSuggestionMember{SuggestionID: dismissed.ID, VideoID: dupB.ID, Position: 2})
	// 本地资料有更新：dupA 计入；已是最新的、软删视频上的不计。
	checked := time.Now()
	create(&models.VideoLocalMetadataState{VideoID: dupA.ID, Status: services.LocalMetadataStateUpdateAvailable, LastCheckedAt: checked})
	create(&models.VideoLocalMetadataState{VideoID: dupB.ID, Status: services.LocalMetadataStateCurrent, LastCheckedAt: checked})
	create(&models.VideoLocalMetadataState{VideoID: trashed.ID, Status: services.LocalMetadataStateUpdateAvailable, LastCheckedAt: checked})

	app := NewApp()
	app.resetImageAITaggingService()
	t.Cleanup(func() {
		if svc := app.imageAITaggingService(); svc != nil {
			svc.StopAndWait()
		}
	})

	// 清理：精确重复一组（dupA/dupB，它们那条未确认的同源关系与这一组是同一对，只算一次）+
	// 已确认同源的 dupA/short 一对（清理中心照样列出，由用户决定删哪个）+ 极短一条；
	// 极低清那一条随后被忽略，回读时滤掉。
	if _, err := app.StartCleanupAnalysisFromSettings(); err != nil {
		t.Fatalf("启动清理分析失败: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for status := app.GetCleanupStatus(); status.Running || !status.Completed; status = app.GetCleanupStatus() {
		if time.Now().After(deadline) {
			t.Fatalf("清理分析未结束: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := app.DismissCleanupVideo(lowRes.ID, models.CleanupDismissalCategoryLow); err != nil {
		t.Fatalf("忽略极低清候选失败: %v", err)
	}

	analysis := app.GetCleanupStatus().Analysis
	if analysis == nil || len(analysis.DuplicateGroups) != 1 || len(analysis.SameSourceGroups) != 1 ||
		len(analysis.LowDuration) != 1 || len(analysis.LowResolution) != 0 {
		t.Fatalf("前置：清理结果应为一组精确重复 + 一对已确认同源 + 一条极短（极低清已忽略）: %+v", analysis)
	}
	if pair := analysis.SameSourceGroups[0]; cleanupPairKey(pair.Preferred.ID, pair.Alternative.ID) != cleanupPairKey(dupA.ID, short.ID) {
		t.Fatalf("前置：清理里的同源应只剩已确认的 dupA/short: %+v", pair)
	}

	summary, err := app.GetPendingWorkSummary()
	if err != nil {
		t.Fatalf("读取待处理汇总失败: %v", err)
	}
	want := PendingWorkSummary{
		AIVideoCandidates: 2, AIVideoFailedRuns: 1, AIImageCandidates: 1, SameSourceUnconfirmed: 1,
		CollectionSuggestions: 1, CleanupCandidates: 3, LocalMetadataUpdates: 1, Total: 9,
	}
	if summary != want {
		t.Fatalf("待处理汇总不对:\n got %+v\nwant %+v", summary, want)
	}

	// 与各面板背后的服务接口逐项核对口径。
	aiSummary, err := app.GetAITaggingStatusSummary()
	if err != nil || int(aiSummary.Pending) != summary.AIVideoCandidates || int(aiSummary.Failed) != summary.AIVideoFailedRuns {
		t.Fatalf("视频 AI 口径应与 StatusSummary 一致: %+v err=%v", aiSummary, err)
	}
	imageSummary, err := app.GetImageAITaggingSummary()
	if err != nil || int(imageSummary.Pending) != summary.AIImageCandidates {
		t.Fatalf("图片 AI 口径应与 GetImageAITaggingSummary 一致: %+v err=%v", imageSummary, err)
	}
	relations, err := app.ListSameSourceRelations(models.VideoSameSourceStatusDetected, false)
	if err != nil || len(relations) != summary.SameSourceUnconfirmed {
		t.Fatalf("同源口径应与审阅列表一致: %d err=%v", len(relations), err)
	}
	suggestions, err := app.ListCollectionSuggestions()
	if err != nil || len(suggestions) != summary.CollectionSuggestions {
		t.Fatalf("建议作品集口径应与面板一致: %d err=%v", len(suggestions), err)
	}
	localUpdates, err := app.CountLibraryVideos(services.LibraryFilter{SmartView: services.LibraryViewLocalMetadataUpdated})
	if err != nil || int(localUpdates) != summary.LocalMetadataUpdates {
		t.Fatalf("本地资料口径应与智能视图一致: %d err=%v", localUpdates, err)
	}
}
