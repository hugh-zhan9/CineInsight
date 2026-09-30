package main

import (
	"context"
	"log"
	"video-master/models"
	"video-master/services"
)

// ===== Library Methods =====

// ListRecentlyPlayedWithFilter 按当前片库条件稳定分页返回最近播放视频。
func (a *App) ListRecentlyPlayedWithFilter(filter services.LibraryFilter, cursorLastPlayedAt string, cursorID uint, limit int) ([]models.Video, error) {
	videos, err := a.videoService.ListRecentlyPlayedWithFilter(filter, cursorLastPlayedAt, cursorID, limit)
	log.Printf("API ListRecentlyPlayedWithFilter cursorLastPlayedAt=%q cursorID=%d count=%d err=%v", cursorLastPlayedAt, cursorID, len(videos), err)
	return videos, err
}

// GetLibrarySubtitleHits 为当前片库页补充首个字幕命中片段。
func (a *App) GetLibrarySubtitleHits(keyword string, videoIDs []uint) ([]services.LibrarySubtitleHit, error) {
	hits, err := a.videoService.GetLibrarySubtitleHits(keyword, videoIDs)
	log.Printf("API GetLibrarySubtitleHits videos=%d hits=%d err=%v", len(videoIDs), len(hits), err)
	return hits, err
}

func (a *App) SearchLibraryVideoPage(request services.LibraryVideoPageRequest) (*services.LibraryVideoPage, error) {
	return a.videoService.SearchLibraryVideoPage(request.Filter, request.Cursor, request.Limit)
}

// GetAutomaticOverrideKinds 批量返回视频的「手动」角标数据（D-PC36），键为视频 ID。
// 片库分页结果已自带这份数据；最近播放、语义搜索等返回视频数组的页面用它补齐。
func (a *App) GetAutomaticOverrideKinds(videoIDs []uint) (map[uint][]string, error) {
	return a.videoService.GetAutomaticOverrideKinds(videoIDs)
}

// CountLibraryVideos 返回当前筛选命中的视频条数，供片库结果条回显。
func (a *App) CountLibraryVideos(filter services.LibraryFilter) (int64, error) {
	return a.videoService.CountLibraryVideos(filter)
}

// GetLibraryCounts 返回应用头部展示的视频与图片总数。
func (a *App) GetLibraryCounts() (*services.LibraryCounts, error) {
	return a.videoService.GetLibraryCounts()
}

func (a *App) GetLibraryInsights() (*services.LibraryStats, error) {
	return a.libraryStatsService.GetStats()
}

// RerollRandom 是随机结果条上的「换一个」：丢弃上一次随机的统计并在同一范围内抽下一条；
// 令牌已失效时返回 reason_code=reroll_expired。
func (a *App) RerollRandom(token string) (*services.PlaybackAttemptResult, error) {
	result, err := a.videoService.RerollRandom(token)
	logRandomPlaybackResult("RerollRandom", "", result, err)
	return result, err
}

// RecordViewEvent 记录一次有效观看（source=inline_view），同一会话只记一次；返回本次是否新写入了事件。
func (a *App) RecordViewEvent(videoID uint, source string, sessionID string) (bool, error) {
	recorded, err := a.videoService.RecordViewEvent(videoID, source, sessionID)
	log.Printf("API RecordViewEvent id=%d source=%s recorded=%v err=%v", videoID, source, recorded, err)
	return recorded, err
}

func logRandomPlaybackResult(api, mode string, result *services.PlaybackAttemptResult, err error) {
	if result == nil {
		log.Printf("API %s mode=%s result=nil err=%v", api, mode, err)
		return
	}
	videoID := uint(0)
	if result.Video != nil {
		videoID = result.Video.ID
	}
	log.Printf("API %s mode=%s id=%d dispatch=%v reason=%s err=%v", api, mode, videoID, result.DispatchSucceeded, result.ReasonCode, err)
}

// ListSavedLibraryViews 返回用户保存的片库筛选。
func (a *App) ListSavedLibraryViews() ([]models.SavedLibraryView, error) {
	views, err := a.videoService.ListSavedLibraryViews()
	log.Printf("API ListSavedLibraryViews count=%d err=%v", len(views), err)
	return views, err
}

// SaveLibraryView 创建用户命名的片库筛选。
func (a *App) SaveLibraryView(input services.SavedLibraryViewInput) (*models.SavedLibraryView, error) {
	view, err := a.videoService.SaveLibraryView(input)
	log.Printf("API SaveLibraryView name=%q err=%v", input.Name, err)
	return view, err
}

// UpdateSavedLibraryView 用新名称与筛选条件覆盖已有视图；重名返回 saved_view_name_taken。
func (a *App) UpdateSavedLibraryView(viewID uint, name string, filter services.LibraryFilter) (*models.SavedLibraryView, error) {
	view, err := a.videoService.UpdateSavedLibraryView(viewID, name, filter)
	log.Printf("API UpdateSavedLibraryView id=%d name=%q err=%v", viewID, name, err)
	return view, err
}

// FilterActiveTagIDs 剔除不存在或已删除的标签 ID，返回保留的 ID 与被剔除的数量。
func (a *App) FilterActiveTagIDs(tagIDs []uint) (*services.ActiveTagIDsResult, error) {
	return a.videoService.FilterActiveTagIDs(tagIDs)
}

// FilterActivePersonIDs 剔除已不存在的人物 ID，返回保留的 ID 与被剔除的数量（与 FilterActiveTagIDs 同语义）。
func (a *App) FilterActivePersonIDs(personIDs []uint) (*services.ActivePersonIDsResult, error) {
	return a.videoService.FilterActivePersonIDs(personIDs)
}

// ListStaleReasonCounts 返回路径失效记录按原因的计数，空原因归入 unknown。
func (a *App) ListStaleReasonCounts() (map[string]int, error) {
	counts, err := a.videoService.ListStaleReasonCounts()
	log.Printf("API ListStaleReasonCounts reasons=%d err=%v", len(counts), err)
	return counts, err
}

// DeleteSavedLibraryView 删除用户保存的片库筛选。
func (a *App) DeleteSavedLibraryView(viewID uint) error {
	err := a.videoService.DeleteSavedLibraryView(viewID)
	log.Printf("API DeleteSavedLibraryView id=%d err=%v", viewID, err)
	return err
}

// SyncScanDirectories 全量扫描所有扫描根。trigger 由前端给出：启动时的那次传 "startup"，
// 扫描条上的手动扫描传 "manual"；为空或不认识时按 "manual" 处理。完成后按 trigger 发
// library-scan-summary（D-PC09）。
func (a *App) SyncScanDirectories(trigger string) (*services.ScanSyncResult, error) {
	if trigger != services.ScanTriggerStartup {
		trigger = services.ScanTriggerManual
	}
	dirs, err := a.directoryService.GetAllDirectories()
	if err != nil {
		log.Printf("API SyncScanDirectories load dirs err=%v", err)
		return nil, err
	}
	result := a.videoService.SyncScanDirectories(dirs)
	if a.cleanupService != nil && (result.Added > 0 || result.Relocated > 0 || result.Deleted > 0 || result.MetadataRefreshed > 0) {
		a.cleanupService.InvalidateAnalysis()
	}
	// 扫到新片后唤醒 AI 打标：这是扫描的自动后果，与下面的扫描后自动任务同一口径，
	// 因此同样经空闲门（D-030）。日志里记的是"唤醒已提交"——机器不空闲时它会在门后
	// 等着，真正叫醒 worker 是放行之后的事；用户显式的 TriggerAITagging 不走这里。
	aiWakeQueued := false
	if result.Added > 0 && a.aiTaggingService != nil {
		go a.triggerAITaggingAuto("scan")
		aiWakeQueued = true
	}
	log.Printf("API SyncScanDirectories trigger=%s dirs=%d scanned=%d added=%d relocated=%d deleted=%d refreshed=%d skipped=%d errors=%d ai_wake_queued=%v",
		trigger, result.Directories, result.Scanned, result.Added, result.Relocated, result.Deleted, result.MetadataRefreshed, result.Skipped, len(result.Errors), aiWakeQueued)
	a.emitLibraryScanSummary(trigger, result)
	a.runPostScanAutomation(result)
	return result, nil
}

// runPostScanAutomation 按设置在扫描之后接着跑几件杂活。
// 后台执行：这些任务都吃 CPU/IO；每个服务自己有"已在运行就不重复启动"的保护，
// 这里不再另加锁。库没有变化时什么都不做——没有新东西要补，跑一遍纯属浪费。
//
// 四件事各起一个 goroutine：它们都要各自过空闲门（D-030），排成一串的话
// 后面几个连"等待空闲"都登记不上，面板上就只看得到第一个在等。
func (a *App) runPostScanAutomation(result *services.ScanSyncResult) {
	if result == nil {
		return
	}
	changed := result.Added > 0 || result.Relocated > 0 || result.Deleted > 0 || result.MetadataRefreshed > 0
	if !changed {
		return
	}
	settings, err := a.settingsService.GetSettings()
	if err != nil {
		log.Printf("扫描后自动任务读取设置失败 err=%v", err)
		return
	}
	if settings.AutoTechnicalBackfill {
		go func() {
			if err := a.runGatedAutoTask(
				string(services.BackgroundTaskTechnical),
				func(ctx context.Context, hook services.TaskPauseHook) error {
					_, err := a.technicalBackfill.StartWithPauseHook(ctx, hook)
					return err
				},
			); err != nil {
				log.Printf("扫描后自动补全技术元数据失败 err=%v", err)
			}
		}()
	}
	if settings.AutoPerceptualHash {
		go func() {
			if err := a.runGatedAutoTask(
				string(services.BackgroundTaskPerceptualHash),
				func(ctx context.Context, hook services.TaskPauseHook) error {
					_, err := a.perceptualHash.StartWithPauseHook(ctx, hook)
					return err
				},
			); err != nil {
				log.Printf("扫描后自动补全感知哈希失败 err=%v", err)
			}
		}()
	}
	if settings.AutoCleanupAnalysis {
		go func() {
			// 清理分析没有取消入口，也没有逐项循环，因此只过门、不装项间检查点。
			if err := a.runGatedAutoTask(
				string(services.BackgroundTaskCleanup),
				func(context.Context, services.TaskPauseHook) error {
					_, err := a.cleanupService.StartAnalysisFromSettings()
					return err
				},
			); err != nil {
				log.Printf("扫描后自动清理分析失败 err=%v", err)
			}
		}()
	}
	if settings.AutoCollectionSuggestions {
		go func() {
			// 剧集分析是轻任务（只读库 + 内存解析），不经 MediaWorkSlot；
			// 它一趟跑完没有逐项循环，因此同样只过门、不装项间检查点（D-030）。
			if err := a.runGatedAutoTask(
				string(services.BackgroundTaskCollectionSuggest),
				func(ctx context.Context, _ services.TaskPauseHook) error {
					_, err := a.collectionSuggestions.Analyze(ctx)
					return err
				},
			); err != nil {
				log.Printf("扫描后自动分析剧集候选失败 err=%v", err)
			}
		}()
	}
	if settings.AutoFrameHashSequence {
		go func() {
			// 帧哈希回填是逐项循环的重任务：既过门，也带项间检查点，
			// 与感知哈希同口径（D-030）。
			if err := a.runGatedAutoTask(
				string(services.BackgroundTaskFrameHash),
				func(ctx context.Context, hook services.TaskPauseHook) error {
					_, err := a.frameHash.StartWithPauseHook(ctx, hook)
					return err
				},
			); err != nil {
				log.Printf("扫描后自动补全帧哈希失败 err=%v", err)
			}
		}()
	}
	if settings.AutoFaceAnalysis {
		go func() {
			// 人脸分析是逐项循环的重任务：既过门，也带项间检查点（D-022、D-030）。
			// 运行时没准备好就静默跳过——原因在设置页的人脸分区里说清楚，
			// 这里再报一次只会在日志里刷噪音。
			if !a.faceRuntime.Available() {
				return
			}
			if err := a.runGatedAutoTask(
				string(services.BackgroundTaskFace),
				func(ctx context.Context, hook services.TaskPauseHook) error {
					_, err := a.faceAnalysis.StartWithPauseHook(ctx, services.FaceAnalysisScopeAll, hook)
					return err
				},
			); err != nil {
				log.Printf("扫描后自动人脸分析失败 err=%v", err)
			}
		}()
	}
	if settings.AutoCompatibilityProxy && len(result.AddedVideoIDs) > 0 {
		// 只给本次新增的视频做代理（D-006）：候选集是"这一批新加进来的"，
		// 被 LRU 淘汰过的老视频不在里面，自动模式因此不会跟淘汰打乒乓。
		// 代理任务没有项间检查点，只在 Run 层过门（与清理分析同口径）——
		// 每一项处理前它自己会抢 MediaWorkSlot，那已经是最细的让位粒度。
		addedIDs := append([]uint(nil), result.AddedVideoIDs...)
		go func() {
			if err := a.runGatedAutoTask(
				string(services.BackgroundTaskProxy),
				func(ctx context.Context, _ services.TaskPauseHook) error {
					_, err := a.playbackProxies.EnqueueAutoCandidates(ctx, addedIDs)
					return err
				},
			); err != nil {
				log.Printf("扫描后自动生成播放代理失败 err=%v", err)
			}
		}()
	}
}
