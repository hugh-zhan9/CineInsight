package main

import (
	"context"
	"log"
	"time"
	"video-master/services"
)

// GetCleanupCandidates 获取清理候选（轻量规则）
func (a *App) GetCleanupCandidates(minDurationSeconds int, minWidth int, minHeight int) (*services.CleanupAnalysis, error) {
	criteria := services.CleanupCriteria{
		MinDuration: time.Duration(minDurationSeconds) * time.Second,
		MinWidth:    minWidth,
		MinHeight:   minHeight,
	}
	startedAt := time.Now()
	log.Printf("API GetCleanupCandidates begin duration=%d width=%d height=%d", minDurationSeconds, minWidth, minHeight)
	analysis, err := a.cleanupService.AnalyzeCleanupCandidates(criteria)
	if err != nil {
		log.Printf("API GetCleanupCandidates duration=%d width=%d height=%d elapsed=%s err=%v",
			minDurationSeconds, minWidth, minHeight, time.Since(startedAt).Round(time.Millisecond), err)
		return nil, err
	}

	log.Printf("API GetCleanupCandidates duration=%d width=%d height=%d elapsed=%s duplicate_groups=%d low_duration=%d low_resolution=%d",
		minDurationSeconds, minWidth, minHeight,
		time.Since(startedAt).Round(time.Millisecond),
		len(analysis.DuplicateGroups), len(analysis.LowDuration), len(analysis.LowResolution),
	)
	return analysis, nil
}

// StartCleanupAnalysis 是旧绑定的薄包装（P-040 统一删除）：阈值改由设置提供（D-PC36），
// 传入的三个参数不再生效，转调 StartCleanupAnalysisFromSettings。
func (a *App) StartCleanupAnalysis(minDurationSeconds int, minWidth int, minHeight int) (*services.CleanupStatus, error) {
	log.Printf("API StartCleanupAnalysis (legacy) ignored duration=%d width=%d height=%d", minDurationSeconds, minWidth, minHeight)
	return a.StartCleanupAnalysisFromSettings()
}

// StartCleanupAnalysisFromSettings 启动视频清理分析，「极短片段 / 极低分辨率」阈值读设置
// cleanup_short_seconds / cleanup_low_width / cleanup_low_height（≤0 用默认 5 / 480 / 320）。
func (a *App) StartCleanupAnalysisFromSettings() (*services.CleanupStatus, error) {
	status, err := a.cleanupService.StartAnalysisFromSettings()
	log.Printf("API StartCleanupAnalysisFromSettings running=%v completed=%v err=%v",
		status != nil && status.Running, status != nil && status.Completed, err)
	return status, err
}

// CancelCleanupAnalysis 取消进行中的视频清理分析（D-PC51）。没有在跑时返回错误。
func (a *App) CancelCleanupAnalysis() error {
	err := a.cleanupService.CancelAnalysis()
	log.Printf("API CancelCleanupAnalysis err=%v", err)
	return err
}

// CancelImageCleanupAnalysis 取消进行中的图片清理分析（D-PC51）。没有在跑时返回错误。
func (a *App) CancelImageCleanupAnalysis() error {
	err := a.imageCleanupService.CancelImageCleanupAnalysis()
	log.Printf("API CancelImageCleanupAnalysis err=%v", err)
	return err
}

func (a *App) GetCleanupStatus() *services.CleanupStatus {
	status := a.cleanupService.Status()
	log.Printf("API GetCleanupStatus running=%v completed=%v hasAnalysis=%v err=%q",
		status.Running, status.Completed, status.Analysis != nil, status.Error)
	return status
}

// DismissNearDuplicateGroup 持久忽略一组近似重复视频，后续分析不再报出。
func (a *App) DismissNearDuplicateGroup(videoIDs []uint) error {
	err := services.DismissNearDuplicateGroup(videoIDs)
	log.Printf("API DismissNearDuplicateGroup videos=%d err=%v", len(videoIDs), err)
	return err
}

// DismissNearDuplicateMember 把一个视频移出近似重复组（只否决它与组内其他成员的配对，D-PC31）。
func (a *App) DismissNearDuplicateMember(groupVideoIDs []uint, memberID uint) error {
	err := services.DismissNearDuplicateMember(groupVideoIDs, memberID)
	log.Printf("API DismissNearDuplicateMember videos=%d member=%d err=%v", len(groupVideoIDs), memberID, err)
	return err
}

// DismissImageNearDuplicateMember 把一张图片移出近似重复组（D-PC31）。
func (a *App) DismissImageNearDuplicateMember(groupImageIDs []uint, memberID uint) error {
	err := services.DismissImageNearDuplicateMember(groupImageIDs, memberID)
	log.Printf("API DismissImageNearDuplicateMember images=%d member=%d err=%v", len(groupImageIDs), memberID, err)
	if err == nil && a.imageCleanupService != nil {
		a.imageCleanupService.InvalidateAnalysis()
	}
	return err
}

// DismissCleanupVideo 忽略一个「极短片段」（category=short）或「极低分辨率」（category=low）候选（D-PC31）。
func (a *App) DismissCleanupVideo(videoID uint, category string) error {
	err := services.DismissCleanupVideo(videoID, category)
	log.Printf("API DismissCleanupVideo video=%d category=%s err=%v", videoID, category, err)
	return err
}

// ListCleanupDismissals 分页列出某一类忽略记录（「已忽略」页签）。kind：near_duplicate / clip /
// short / low / image_near_duplicate；cursor 为上一页的 next_cursor（首页传 0）。
func (a *App) ListCleanupDismissals(kind string, cursor uint, limit int) (*services.CleanupDismissalPage, error) {
	page, err := services.ListCleanupDismissals(kind, cursor, limit)
	if err != nil {
		log.Printf("API ListCleanupDismissals kind=%s err=%v", kind, err)
		return nil, err
	}
	log.Printf("API ListCleanupDismissals kind=%s result=%d hasMore=%v", kind, len(page.Items), page.HasMore)
	return page, nil
}

// UndoCleanupDismissals 撤销某一类里的若干条忽略记录；已缓存的分析结果随之标为可能过期。
func (a *App) UndoCleanupDismissals(kind string, ids []uint) (*services.CleanupDismissalUndoResult, error) {
	result, err := services.UndoCleanupDismissals(kind, ids)
	log.Printf("API UndoCleanupDismissals kind=%s ids=%d err=%v", kind, len(ids), err)
	if err != nil {
		return nil, err
	}
	if result.Removed > 0 {
		a.invalidateCleanupAnalysisFor(kind == services.CleanupDismissalKindImageNearDuplicate)
	}
	return result, nil
}

// MergeMediaMetadata 把被合并项的整理成果合并到保留项（D-PC48）。kind：video / image。
// 清理中心在删除确认后、调用删除之前按组单独调用；返回错误时不要进入删除。
// options：截取片段组传 skip_playback_state / skip_subtitle 都为 true，其余类别都为 false（§9.1）。
func (a *App) MergeMediaMetadata(kind string, keeperID uint, sourceIDs []uint, options services.MediaMetadataMergeOptions) (*services.MediaMetadataMergeResult, error) {
	deps := services.MediaMetadataMergeDeps{Watched: a.videoService, Options: options}
	if a.subtitleService != nil {
		deps.Subtitles = services.NewSubtitleFileWriter(a.subtitleService.BaseDir)
	}
	result, err := services.MergeMediaMetadata(kind, keeperID, sourceIDs, deps)
	if err != nil {
		log.Printf("API MergeMediaMetadata kind=%s keeper=%d sources=%d skip_playback_state=%v skip_subtitle=%v err=%v",
			kind, keeperID, len(sourceIDs), options.SkipPlaybackState, options.SkipSubtitle, err)
		return nil, err
	}
	log.Printf("API MergeMediaMetadata kind=%s keeper=%d sources=%d skip_playback_state=%v skip_subtitle=%v tags=%d people=%d collections=%d watched=%v subtitle=%v warnings=%d",
		kind, keeperID, len(sourceIDs), options.SkipPlaybackState, options.SkipSubtitle,
		result.TagsAdded, result.PeopleAdded, result.CollectionsAdded, result.WatchedChanged, result.SubtitleMoved, len(result.Warnings))
	// 整理项变了，缓存结果里的保留建议与整理图标可能过期。
	a.invalidateCleanupAnalysisFor(kind == services.MediaMergeKindImage)
	return result, nil
}

func (a *App) invalidateCleanupAnalysisFor(image bool) {
	if image {
		if a.imageCleanupService != nil {
			a.imageCleanupService.InvalidateAnalysis()
		}
		return
	}
	if a.cleanupService != nil {
		a.cleanupService.InvalidateAnalysis()
	}
}

// ===== 帧哈希序列与截取片段识别（D-026、D-028）=====

// StartFrameHashBackfill 显式启动帧哈希回填。
// Start 会在服务锁内摘掉当前这一轮的项间检查点，用户点的任务永不被空闲门挡住（D-030）。
func (a *App) StartFrameHashBackfill() (services.FrameHashStatus, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	status, err := a.frameHash.Start(ctx)
	log.Printf("API StartFrameHashBackfill running=%v total=%d err=%v", status.Running, status.Total, err)
	return status, err
}

func (a *App) GetFrameHashBackfillStatus() services.FrameHashStatus {
	return a.frameHash.Status()
}

func (a *App) CancelFrameHashBackfill() error {
	err := a.frameHash.Cancel()
	log.Printf("API CancelFrameHashBackfill err=%v", err)
	return err
}

// DismissClipCandidate 持久忽略一对"完整片 + 截取片段"，双方文件都没变时不再报出。
func (a *App) DismissClipCandidate(fullID uint, clipID uint) error {
	err := services.DismissClipCandidate(fullID, clipID)
	log.Printf("API DismissClipCandidate full=%d clip=%d err=%v", fullID, clipID, err)
	return err
}
