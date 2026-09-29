package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"video-master/models"
	"video-master/services"
)

// 待处理收件箱（D-PC27、META-08）。
//
// 与任务中心一样只做读侧聚合：每一项都调用对应审阅面板背后的那个服务接口，计数口径因此
// 与面板一致，App 层不另写查询、不新建状态源。

// PendingWorkSummary 是顶栏「待处理」角标与工作台的数据。Total 是各项之和，
// AIVideoFailedRuns 除外（失败的打标不是待审阅的事项，单独提示）。
type PendingWorkSummary struct {
	AIVideoCandidates     int `json:"ai_video_candidates"`
	AIVideoFailedRuns     int `json:"ai_video_failed_runs"`
	AIImageCandidates     int `json:"ai_image_candidates"`
	SameSourceUnconfirmed int `json:"same_source_unconfirmed"`
	FaceUnnamed           int `json:"face_unnamed"`
	FaceAppendPending     int `json:"face_append_pending"`
	CollectionSuggestions int `json:"collection_suggestions"`
	CleanupCandidates     int `json:"cleanup_candidates"`
	LocalMetadataUpdates  int `json:"local_metadata_updates"`
	Total                 int `json:"total"`
}

// GetPendingWorkSummary 汇总各类待处理事项（D-PC27）。各项口径：
//   - AIVideoCandidates / AIVideoFailedRuns：AITaggingService.StatusSummary 的 pending 候选数与
//     failed 状态数（都只算未删除的视频）；
//   - AIImageCandidates：GetImageAITaggingSummary 的 pending 候选数（只算活跃图片）；
//   - SameSourceUnconfirmed：同源审阅列表同口径（status=detected 且未确认，两端视频都未删除）的总数；
//   - FaceUnnamed / FaceAppendPending：人脸审阅面板「未命名」与「待确认追加」两类簇的数量；
//   - CollectionSuggestions：建议作品集面板列出的候选数；
//   - CleanupCandidates：最近一次清理分析的缓存经审阅决定过滤后的待审条目，按视频对去重；
//   - LocalMetadataUpdates：智能视图「本地资料有更新」的视频数。
//
// 数据库不可读（维护中、待重启、未初始化）时返回中文说明，不去读库。
func (a *App) GetPendingWorkSummary() (PendingWorkSummary, error) {
	if reason := a.databaseUnavailableReason(); reason != "" {
		return PendingWorkSummary{}, errors.New(reason)
	}
	var summary PendingWorkSummary
	fail := func(label string, err error) (PendingWorkSummary, error) {
		log.Printf("API GetPendingWorkSummary %s err=%v", label, err)
		return PendingWorkSummary{}, fmt.Errorf("读取%s失败", label)
	}

	aiSummary, err := a.aiTaggingService.StatusSummary()
	if err != nil {
		return fail("视频 AI 候选数量", err)
	}
	summary.AIVideoCandidates = int(aiSummary.Pending)
	summary.AIVideoFailedRuns = int(aiSummary.Failed)

	imageAI := a.imageAITaggingService()
	if imageAI == nil {
		return fail("图片 AI 候选数量", errors.New("image ai tagging service not ready"))
	}
	imageSummary, err := imageAI.GetImageAITaggingSummary()
	if err != nil {
		return fail("图片 AI 候选数量", err)
	}
	summary.AIImageCandidates = int(imageSummary.Pending)

	sameSource, err := a.aiTaggingService.UnconfirmedSameSourceCount()
	if err != nil {
		return fail("待确认的同源关系", err)
	}
	summary.SameSourceUnconfirmed = int(sameSource)

	if a.faceReview != nil {
		unnamed, appendPending, err := a.faceReview.CountPendingReview(context.Background())
		if err != nil {
			return fail("待审阅的人脸", err)
		}
		summary.FaceUnnamed = unnamed
		summary.FaceAppendPending = appendPending
	}

	suggestions, err := a.collectionSuggestions.List()
	if err != nil {
		return fail("建议作品集", err)
	}
	summary.CollectionSuggestions = len(suggestions)

	cleanup := a.cleanupService.Status()
	if cleanup.Completed && cleanup.Analysis == nil {
		// 分析跑完了结果却是空的，只会是回读时审阅决定没读出来（Status 把缓存扣下了）。
		return fail("清理候选", errors.New(cleanup.Error))
	}
	summary.CleanupCandidates = countCleanupReviewEntries(cleanup.Analysis)

	localUpdates, err := a.videoService.CountLibraryVideos(services.LibraryFilter{SmartView: services.LibraryViewLocalMetadataUpdated})
	if err != nil {
		return fail("本地资料更新", err)
	}
	summary.LocalMetadataUpdates = int(localUpdates)

	summary.Total = summary.AIVideoCandidates + summary.AIImageCandidates + summary.SameSourceUnconfirmed +
		summary.FaceUnnamed + summary.FaceAppendPending + summary.CollectionSuggestions +
		summary.CleanupCandidates + summary.LocalMetadataUpdates
	return summary, nil
}

// countCleanupReviewEntries 数清理中心里要用户决定的条目：每个精确 / 近似重复组、每对同源、
// 每对截取片段、每个极短 / 极低清视频各算一条。按视频对去重：同一对视频已经在某个重复组里
// 一起出现时，它在同源或截取片段里的那一条不再另算；同源与截取片段里的同一对也只算一次。
// 已忽略的候选在 CleanupService.Status 回读时已经滤掉。
func countCleanupReviewEntries(analysis *services.CleanupAnalysis) int {
	if analysis == nil {
		return 0
	}
	count := 0
	grouped := map[[2]uint]struct{}{}
	countGroup := func(group services.CleanupDuplicateGroup) {
		members := make([]uint, 0, len(group.Candidates)+1)
		seen := map[uint]struct{}{}
		for _, video := range append([]models.Video{group.Original}, group.Candidates...) {
			if _, duplicate := seen[video.ID]; video.ID == 0 || duplicate {
				continue
			}
			seen[video.ID] = struct{}{}
			members = append(members, video.ID)
		}
		if len(members) < 2 {
			return
		}
		count++
		for i := range members {
			for j := i + 1; j < len(members); j++ {
				grouped[cleanupPairKey(members[i], members[j])] = struct{}{}
			}
		}
	}
	for _, group := range analysis.DuplicateGroups {
		countGroup(group)
	}
	for _, group := range analysis.NearDuplicateGroups {
		countGroup(group)
	}
	pairs := map[[2]uint]struct{}{}
	countPair := func(left, right uint) {
		if left == 0 || right == 0 || left == right {
			return
		}
		key := cleanupPairKey(left, right)
		if _, inGroup := grouped[key]; inGroup {
			return
		}
		if _, counted := pairs[key]; counted {
			return
		}
		pairs[key] = struct{}{}
		count++
	}
	for _, group := range analysis.SameSourceGroups {
		countPair(group.Preferred.ID, group.Alternative.ID)
	}
	for _, group := range analysis.ClipGroups {
		countPair(group.Full.ID, group.Clip.ID)
	}
	return count + len(analysis.LowDuration) + len(analysis.LowResolution)
}

func cleanupPairKey(left, right uint) [2]uint {
	if left > right {
		left, right = right, left
	}
	return [2]uint{left, right}
}
