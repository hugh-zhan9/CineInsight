package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// semanticIndexService 以读锁返回当前语义索引服务指针；恢复失败路径会
// 重建该指针，绑定读取与重建之间需要同步。
func (a *App) semanticIndexService() *services.SemanticIndexService {
	a.semanticMu.RLock()
	defer a.semanticMu.RUnlock()
	return a.semanticIndex
}

func (a *App) StartSemanticIndex(request services.SemanticIndexBuildRequest) (services.SemanticIndexStatus, error) {
	svc := a.semanticIndexService()
	if svc == nil {
		return services.SemanticIndexStatus{}, services.ErrSemanticIndexUnavailable
	}
	// 全局互斥（D-010）：图片语义索引运行中时拒绝视频任务。
	if image := a.imageSemanticIndexService(); image != nil && image.Status().Running {
		return services.SemanticIndexStatus{}, fmt.Errorf("已有语义索引任务运行中（图片），请先取消后再启动视频索引")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return svc.Start(ctx, request)
}

func (a *App) GetSemanticIndexStatus() services.SemanticIndexStatus {
	svc := a.semanticIndexService()
	if svc == nil {
		return services.SemanticIndexStatus{Available: false, Unavailable: "数据库未初始化"}
	}
	return svc.Status()
}

func (a *App) CancelSemanticIndex() error {
	svc := a.semanticIndexService()
	if svc == nil {
		return services.ErrSemanticIndexUnavailable
	}
	return svc.Cancel()
}

func (a *App) SearchSemanticVideos(request services.SemanticSearchRequest) (*services.SemanticSearchPage, error) {
	svc := a.semanticIndexService()
	if svc == nil {
		return nil, services.ErrSemanticIndexUnavailable
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return svc.Search(ctx, request)
}

func (a *App) FindSimilarVideos(request services.SemanticSimilarRequest) (*services.SemanticSearchPage, error) {
	svc := a.semanticIndexService()
	if svc == nil {
		return nil, services.ErrSemanticIndexUnavailable
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return svc.FindSimilar(ctx, request)
}

func (a *App) GetAITagLibrary() ([]models.Tag, error) {
	tags, err := a.tagService.GetAITagLibrary()
	log.Printf("API GetAITagLibrary result=%d err=%v", len(tags), err)
	return tags, err
}

func (a *App) SaveAITagLibrary(inputs []services.AITagLibraryInput) ([]models.Tag, error) {
	tags, err := a.tagService.SaveAITagLibrary(inputs)
	log.Printf("API SaveAITagLibrary requested=%d result=%d err=%v", len(inputs), len(tags), err)
	return tags, err
}

func (a *App) ClearAITagLibrary() ([]models.Tag, error) {
	tags, err := a.tagService.ClearAITagLibrary()
	log.Printf("API ClearAITagLibrary result=%d err=%v", len(tags), err)
	return tags, err
}

// ===== AI Tagging Methods =====

func (a *App) ListAITagCandidates(videoID uint, confidence string, status string) ([]services.AITaggingReviewItem, error) {
	items, err := a.aiTaggingService.ListCandidates(videoID, confidence, status)
	log.Printf("API ListAITagCandidates videoID=%d confidence=%s status=%s result=%d err=%v", videoID, confidence, status, len(items), err)
	return items, err
}

// ListAITagCandidatePage 是审阅工作台的取数入口：候选没有上限，一次全量下发
// 在大库上既压 IPC 又要前端渲染上千行。cursorID 为 0 取第一页，limit<=0 用服务端默认。
func (a *App) ListAITagCandidatePage(videoID uint, confidence string, status string, cursorID uint, limit int) (*services.AITagCandidatePage, error) {
	page, err := a.aiTaggingService.ListCandidatePage(videoID, confidence, status, cursorID, limit)
	count, next := 0, uint(0)
	if page != nil {
		count, next = len(page.Items), page.NextID
	}
	log.Printf("API ListAITagCandidatePage videoID=%d confidence=%s status=%s cursor=%d limit=%d result=%d next=%d err=%v", videoID, confidence, status, cursorID, limit, count, next, err)
	return page, err
}

func (a *App) ApproveAITagCandidate(candidateID uint) (*services.AITaggingReviewItem, error) {
	item, err := a.aiTaggingService.ApproveCandidate(candidateID)
	log.Printf("API ApproveAITagCandidate candidateID=%d err=%v", candidateID, err)
	return item, err
}

func (a *App) RejectAITagCandidate(candidateID uint) error {
	err := a.aiTaggingService.RejectCandidate(candidateID)
	log.Printf("API RejectAITagCandidate candidateID=%d err=%v", candidateID, err)
	return err
}

func (a *App) RejectAITagCandidatesByVideo(videoID uint) (int64, error) {
	count, err := a.aiTaggingService.RejectPendingCandidatesByVideo(videoID)
	log.Printf("API RejectAITagCandidatesByVideo videoID=%d rejected=%d err=%v", videoID, count, err)
	return count, err
}

func (a *App) RetryAITagging(videoID uint) error {
	err := a.aiTaggingService.RetryVideo(videoID)
	triggered := false
	if err == nil {
		triggered = a.aiTaggingService.Trigger()
	}
	log.Printf("API RetryAITagging videoID=%d ai_triggered=%v err=%v", videoID, triggered, err)
	return err
}

func (a *App) TriggerAITagging() bool {
	triggered := a.aiTaggingService.Trigger()
	log.Printf("API TriggerAITagging triggered=%v", triggered)
	return triggered
}

func (a *App) GetAITaggingStatusSummary() (*services.AITaggingStatusSummary, error) {
	summary, err := a.aiTaggingService.StatusSummary()
	log.Printf("API GetAITaggingStatusSummary err=%v summary=%+v", err, summary)
	return summary, err
}

func (a *App) GetAIQualityReport(filter services.AIQualityFilter) (*services.AIQualityReport, error) {
	startedAt := time.Now()
	report, err := a.aiQualityService.Report(filter)
	var tagSamples, sameSourceSamples, runs int64
	if report != nil {
		tagSamples = report.TagSummary.Decided
		sameSourceSamples = report.SameSourceSummary.Decided
		runs = report.RunSummary.Total
	}
	log.Printf("API GetAIQualityReport window=%s filters={tag:%v confidence:%v model:%v tag_prompt:%v comparison_prompt:%v detection:%v} samples={tag:%d same_source:%d runs:%d} duration_ms=%d failed=%v",
		filter.Window, filter.TagID > 0, filter.Confidence != "", filter.ModelIdentifier != "", filter.PromptSchemaVersion != "", filter.ComparisonPromptVersion != "", filter.DetectionVersion != "",
		tagSamples, sameSourceSamples, runs, time.Since(startedAt).Milliseconds(), err != nil)
	return report, err
}

func (a *App) ListSameSourceRelations(status string, unreadOnly bool) ([]services.VideoSameSourceReviewItem, error) {
	items, err := a.aiTaggingService.ListSameSourceRelations(status, unreadOnly)
	log.Printf("API ListSameSourceRelations status=%s unreadOnly=%v result=%d err=%v", status, unreadOnly, len(items), err)
	return items, err
}

func (a *App) MarkSameSourceRelationRead(relationID uint) error {
	err := a.aiTaggingService.MarkSameSourceRelationRead(relationID)
	log.Printf("API MarkSameSourceRelationRead relationID=%d err=%v", relationID, err)
	return err
}

func (a *App) ConfirmSameSourceRelation(relationID uint) error {
	err := a.aiTaggingService.ConfirmSameSourceRelation(relationID)
	log.Printf("API ConfirmSameSourceRelation relationID=%d err=%v", relationID, err)
	return err
}

func (a *App) RejectSameSourceRelation(relationID uint) error {
	err := a.aiTaggingService.RejectSameSourceRelation(relationID)
	if err == nil {
		a.cleanupService.InvalidateAnalysis()
	}
	log.Printf("API RejectSameSourceRelation relationID=%d err=%v", relationID, err)
	return err
}

func (a *App) resetSemanticIndexService() {
	if database.DB == nil {
		a.semanticMu.Lock()
		a.semanticIndex = nil
		a.semanticMu.Unlock()
		return
	}
	capability := database.PrepareSemanticVectorStorage(database.DB)
	provider := services.SemanticIndexConfigProviderFunc(func() (services.SemanticIndexConfig, error) {
		config, err := (services.SettingsAITaggingConfigProvider{}).Load()
		if err != nil {
			return services.SemanticIndexConfig{}, err
		}
		semanticConfig := services.SemanticIndexConfigFromAITagging(config)
		var settings models.Settings
		if err := database.DB.First(&settings).Error; err == nil && strings.TrimSpace(settings.SemanticEmbeddingModel) != "" {
			semanticConfig.Model = strings.TrimSpace(settings.SemanticEmbeddingModel)
		}
		return semanticConfig, nil
	})
	service := services.NewSemanticIndexService(database.DB, capability, provider)
	service.SetBackgroundTaskRegistry(a.backgroundTasks)
	service.SetDesktopNotifier(a.desktopNotify)
	service.SetEventEmitter(func(status services.SemanticIndexStatus) {
		if a.ctx != nil && a.ctx.Err() == nil {
			runtime.EventsEmit(a.ctx, "semantic-index-state", status)
		}
	})
	a.semanticMu.Lock()
	a.semanticIndex = service
	a.semanticMu.Unlock()
}

// ===== 人脸识别（D-016..D-020、D-022）=====
//
// 人脸数据（向量、裁剪图）只留在本机：这里没有任何一条会把它们发出去的路径，
// 唯一的网络出口是运行时准备时的模型包下载。

// GetFaceRuntimeStatus 返回运行时状态与缺什么的原因（7.2）。
func (a *App) GetFaceRuntimeStatus() services.FaceRuntimeStatus {
	return a.faceRuntime.Status()
}

// PrepareFaceRuntime 显式准备运行时：托管 Python、venv、依赖、模型（7.2）。
func (a *App) PrepareFaceRuntime() (services.FaceRuntimeStatus, error) {
	status, err := a.faceRuntime.Prepare(a.backgroundContext())
	log.Printf("API PrepareFaceRuntime state=%s preparing=%v err=%v", status.State, status.Preparing, err)
	return status, err
}

// CancelFaceRuntimePrepare 取消正在进行的准备（7.2）。
func (a *App) CancelFaceRuntimePrepare() error {
	err := a.faceRuntime.CancelPrepare()
	log.Printf("API CancelFaceRuntimePrepare err=%v", err)
	return err
}

// StartFaceAnalysis 显式启动人脸分析（7.2）。scope: all / videos / images。
// 显式启动永不经空闲门（D-030）。
func (a *App) StartFaceAnalysis(scope string) (services.FaceAnalysisStatus, error) {
	status, err := a.faceAnalysis.Start(a.backgroundContext(), scope)
	log.Printf("API StartFaceAnalysis scope=%s running=%v err=%v", scope, status.Running, err)
	return status, err
}

// GetFaceAnalysisStatus 返回分析任务状态（7.2）。
func (a *App) GetFaceAnalysisStatus() services.FaceAnalysisStatus {
	return a.faceAnalysis.Status()
}

// CancelFaceAnalysis 取消分析（7.2）。
func (a *App) CancelFaceAnalysis() error {
	err := a.faceAnalysis.Cancel()
	log.Printf("API CancelFaceAnalysis err=%v", err)
	return err
}

// ClearFaceData 删除全部人脸数据并返回清空后的占用（7.2、D-020）。
// 不触碰 people / video_people / image_people。
func (a *App) ClearFaceData() (services.FaceDataUsage, error) {
	usage, err := a.faceAnalysis.ClearFaceData()
	log.Printf("API ClearFaceData err=%v", err)
	return usage, err
}

// GetFaceDataUsage 返回人脸数据占用（7.2、D-020）。
func (a *App) GetFaceDataUsage() (services.FaceDataUsage, error) {
	usage, err := a.faceAnalysis.DataUsage()
	if err != nil {
		log.Printf("API GetFaceDataUsage err=%v", err)
	}
	return usage, err
}

// ===== 人脸审阅（D-019）=====
//
// 这六个绑定是人脸链路上唯一会让 video_people / image_people 变化的入口，而且每一个
// 都对应用户在面板上的一次点击。审阅动作改完之后发 face-review-changed，面板据此
// 局部刷新，不必整页重载。

// ListFaceClusters 返回审阅面板要的簇视图（7.2）。
func (a *App) ListFaceClusters(filter services.FaceClusterFilter) ([]services.FaceClusterView, error) {
	views, err := a.faceReview.ListFaceClusters(a.backgroundContext(), filter)
	if err != nil {
		log.Printf("API ListFaceClusters status=%q media_kind=%q err=%v", filter.Status, filter.MediaKind, err)
	}
	return views, err
}

// NameFaceCluster 命名未命名簇：建人物并把簇内媒体关联上去（7.3.1）。
func (a *App) NameFaceCluster(clusterID uint, displayName, originalName string) (services.FaceClusterView, error) {
	view, err := a.faceReview.NameFaceCluster(a.backgroundContext(), clusterID, displayName, originalName)
	log.Printf("API NameFaceCluster cluster_id=%d err=%v", clusterID, err)
	if err == nil {
		a.emitFaceReviewChanged()
	}
	return view, err
}

// LinkFaceCluster 把未命名簇关联到现有人物（7.2）。
func (a *App) LinkFaceCluster(clusterID, personID uint) (services.FaceClusterView, error) {
	view, err := a.faceReview.LinkFaceCluster(a.backgroundContext(), clusterID, personID)
	log.Printf("API LinkFaceCluster cluster_id=%d person_id=%d err=%v", clusterID, personID, err)
	if err == nil {
		a.emitFaceReviewChanged()
	}
	return view, err
}

// IgnoreFaceCluster 忽略簇：观测保留，源不变就不再提示（AC-12）。
func (a *App) IgnoreFaceCluster(clusterID uint) error {
	err := a.faceReview.IgnoreFaceCluster(a.backgroundContext(), clusterID)
	log.Printf("API IgnoreFaceCluster cluster_id=%d err=%v", clusterID, err)
	if err == nil {
		a.emitFaceReviewChanged()
	}
	return err
}

// ConfirmFaceClusterAppend 确认已命名簇的追加候选：这一步才写关系（D-019）。
func (a *App) ConfirmFaceClusterAppend(clusterID uint) error {
	err := a.faceReview.ConfirmFaceClusterAppend(a.backgroundContext(), clusterID)
	log.Printf("API ConfirmFaceClusterAppend cluster_id=%d err=%v", clusterID, err)
	if err == nil {
		a.emitFaceReviewChanged()
	}
	return err
}

// DismissFaceClusterAppend 忽略追加候选：同一条观测不再提示，关系不动（D-019）。
func (a *App) DismissFaceClusterAppend(clusterID uint) error {
	err := a.faceReview.DismissFaceClusterAppend(a.backgroundContext(), clusterID)
	log.Printf("API DismissFaceClusterAppend cluster_id=%d err=%v", clusterID, err)
	if err == nil {
		a.emitFaceReviewChanged()
	}
	return err
}

// emitFaceReviewChanged 通知前端审阅数据变了。只在动作成功后发：失败时面板上的
// 数据还是对的，白刷一次只会把用户正在看的卡片抽走。
func (a *App) emitFaceReviewChanged() {
	if a.ctx != nil && a.ctx.Err() == nil {
		runtime.EventsEmit(a.ctx, "face-review-changed")
	}
}
