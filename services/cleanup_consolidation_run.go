package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

var (
	ErrConsolidationBusy     = errors.New("已有集中整理任务正在运行")
	ErrConsolidationConflict = errors.New("集中整理任务状态已变化，请刷新查看")
)

// ConfigureConsolidation 接入应用数据目录和进度事件。必须在接受任务之前调用。
// onChange 是通知，数据库仍是唯一任务状态源；回调不应阻塞或调用生命周期方法。
func (s *CleanupService) ConfigureConsolidation(dataDir string, onChange func(CleanupConsolidationSummary)) error {
	s.consolidationMu.Lock()
	defer s.consolidationMu.Unlock()
	if s.consolidationDone != nil {
		return ErrConsolidationBusy
	}
	if strings.TrimSpace(dataDir) == "" {
		return fmt.Errorf("集中整理缺少应用数据目录")
	}
	canonical, owner, err := consolidationOwnerScope(dataDir)
	if err != nil {
		return err
	}
	directory, err := snapshotMigrationDirectory(canonical)
	if err != nil {
		return err
	}
	s.consolidationDirectory = directory
	s.consolidationDataDir, s.consolidationOwner, s.consolidationOnChange = canonical, owner, onChange
	return nil
}

// StopConsolidationsAndWait 先关闭接受围栏，再取消并等待所有文件收尾与终态持久化。
// 维护必须在此方法返回后才立数据库围栏或关闭数据库。
func (s *CleanupService) StopConsolidationsAndWait() error {
	s.consolidationMu.Lock()
	s.consolidationBlocked = true
	done := s.consolidationDone
	if s.consolidationCancel != nil {
		s.consolidationCancel()
	}
	s.consolidationMu.Unlock()
	if done != nil {
		<-done
	}
	return nil
}

// ResumeConsolidations 仅在维护失败、数据库仍可使用时重新接受新任务。
func (s *CleanupService) ResumeConsolidations() {
	s.consolidationMu.Lock()
	s.consolidationBlocked = false
	s.consolidationMu.Unlock()
}

func consolidationStatus(task models.CleanupConsolidationTask) (*CleanupConsolidationStatus, error) {
	plan, journal, err := decodeConsolidation(context.Background(), task)
	if err != nil {
		return nil, err
	}
	result := &CleanupConsolidationStatus{CleanupConsolidationTask: task, Preview: plan.Preview, Items: []CleanupConsolidationItemStatus{}}
	for i, item := range journal.Items {
		state := CleanupConsolidationItemStatus{VideoID: item.VideoID, Source: plan.Preview.Items[i].SourcePath, Destination: plan.Preview.Items[i].DestinationPath, Phase: item.Phase, Error: item.Error, RetainedPaths: []string{}}
		for _, file := range item.Files {
			// 日志保留所有任务位置，即便外部改动使核验失败，用户仍能定位。
			for _, path := range []string{file.TemporaryPath, file.StagedSourcePath} {
				if path != "" {
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						state.RetainedPaths = append(state.RetainedPaths, path)
					}
				}
			}
		}
		result.Items = append(result.Items, state)
	}
	return result, nil
}

// ConsolidationStatus 查询指定任务；taskID 为零时返回最新任务，无记录时返回 nil。
func (s *CleanupService) ConsolidationStatus(taskID uint) (*CleanupConsolidationStatus, error) {
	var task models.CleanupConsolidationTask
	query := database.DB.Order("id DESC")
	if taskID != 0 {
		query = query.Where("id = ?", taskID)
	}
	if err := query.First(&task).Error; err != nil {
		if taskID == 0 && errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return consolidationStatus(task)
}

// ListConsolidations 按新到旧返回持久任务，limit 范围为 1..100。
func (s *CleanupService) ListConsolidations(limit int) ([]CleanupConsolidationSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var rows []models.CleanupConsolidationTask
	if err := database.DB.Select("id", "preview_id", "status", "version", "total", "completed", "bytes_done", "bytes_total", "error", "created_at", "updated_at", "finished_at").Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]CleanupConsolidationSummary, 0, len(rows))
	for _, row := range rows {
		result = append(result, consolidationSummary(row))
	}
	return result, nil
}

func consolidationSummary(task models.CleanupConsolidationTask) CleanupConsolidationSummary {
	task.OwnerScope = ""
	task.ActiveSlot = nil
	return CleanupConsolidationSummary{CleanupConsolidationTask: task}
}

// CancelConsolidation 仅取消本实例拥有的执行者。其他实例只能读取状态。
func (s *CleanupService) CancelConsolidation() error {
	s.consolidationMu.Lock()
	defer s.consolidationMu.Unlock()
	if s.consolidationCancel == nil {
		return fmt.Errorf("本实例没有正在运行的集中整理任务")
	}
	s.consolidationCancel()
	return nil
}

// StartConsolidation 根据服务持有的确认令牌启动。PreviewID 幂等，活动槽唯一。
func (s *CleanupService) StartConsolidation(ctx context.Context, previewID string, addToScanRoots bool) (*CleanupConsolidationStatus, error) {
	s.consolidationMu.Lock()
	defer s.consolidationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.consolidationBlocked {
		return nil, fmt.Errorf("集中整理已停止接受任务")
	}
	if s.consolidationDataDir == "" {
		return nil, fmt.Errorf("集中整理运行配置未初始化")
	}
	var existing models.CleanupConsolidationTask
	err := database.DB.WithContext(ctx).Where("preview_id = ?", previewID).First(&existing).Error
	if err == nil {
		return consolidationStatus(existing)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if s.consolidationDone != nil {
		return nil, ErrConsolidationBusy
	}
	s.mu.Lock()
	plan := s.consolidationPreview
	valid := plan != nil && previewID != "" && plan.Preview.PreviewID == previewID && plan.Preview.AnalysisVersion == s.runID && !s.status.Stale && !s.status.Running && s.status.Completed
	videoService, registry := s.consolidationVideo, s.registry
	s.mu.Unlock()
	if !valid || videoService == nil {
		return nil, fmt.Errorf("预览已失效，请重新预览")
	}
	// 对同一 PreviewID 先持内核锁再插入任务，恢复者无法接管插入后尚未执行的窗口。
	release, err := acquireConsolidationLock(s.consolidationDataDir, previewID)
	if err != nil {
		if errors.Is(err, ErrConsolidationBusy) && database.DB.Where("preview_id = ?", previewID).First(&existing).Error == nil {
			return consolidationStatus(existing)
		}
		return nil, err
	}
	if err := validateConsolidationDirectory(s.consolidationDirectory); err != nil {
		release()
		return nil, err
	}
	owned := false
	defer func() {
		if !owned {
			release()
		}
	}()
	if _, err := validateConsolidationSnapshot(ctx, plan, nil); err != nil {
		return nil, err
	}
	if err := videoService.validateConsolidationFiles(ctx, plan.Preview); err != nil {
		return nil, err
	}
	s.mu.Lock()
	valid = s.consolidationPreview == plan && plan.Preview.AnalysisVersion == s.runID && !s.status.Stale && !s.status.Running
	s.mu.Unlock()
	if !valid {
		return nil, fmt.Errorf("分析或选择已变化，请重新预览")
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	journal := newConsolidationJournal(plan.Preview)
	slot := "video_cleanup"
	task := models.CleanupConsolidationTask{PreviewID: previewID, ActiveSlot: &slot, OwnerScope: s.consolidationOwner, Status: "running", Version: 1, Total: len(plan.Preview.Items), BytesTotal: plan.Preview.MoveBytes}
	// 新建扫描根是显式选项，与任务认领同事务。文件动作尚未开始。
	addRoot := false
	if addToScanRoots {
		check, err := videoService.CheckMoveTarget(plan.Preview.Destination)
		if err != nil {
			return nil, err
		}
		addRoot = !check.InScanRoots
	}
	err = database.Transaction(func(tx *gorm.DB) error {
		if err := createConsolidationRecords(ctx, tx, &task, string(encoded), journal); err != nil {
			return err
		}
		if addRoot {
			return tx.Create(&models.ScanDirectory{Path: plan.Preview.Destination}).Error
		}
		return nil
	})
	if err != nil {
		if database.DB.Where("preview_id = ?", previewID).First(&existing).Error == nil {
			return consolidationStatus(existing)
		}
		var active int64
		if database.DB.Model(&models.CleanupConsolidationTask{}).Where("active_slot = ?", slot).Count(&active).Error == nil && active > 0 {
			return nil, ErrConsolidationBusy
		}
		return nil, err
	}
	result, err := consolidationStatus(task)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(s.baseContext())
	done := make(chan struct{})
	s.consolidationDone, s.consolidationCancel = done, cancel
	hooks, onChange := s.consolidationHooks, s.consolidationOnChange
	owned = true
	registry.Begin(BackgroundTaskCleanupConsolidation)
	go func() {
		defer func() {
			cancel()
			release()
			registry.End(BackgroundTaskCleanupConsolidation)
			s.consolidationMu.Lock()
			s.consolidationCancel = nil
			s.consolidationDone = nil
			close(done)
			s.consolidationMu.Unlock()
		}()
		execution := consolidationExecution{task: task, plan: *plan, journal: journal, hooks: hooks, onChange: onChange}
		execution.run(runCtx, videoService)
		if execution.task.Completed > 0 {
			s.InvalidateAnalysis()
		}
	}()
	return result, nil
}

type consolidationExecution struct {
	task                 models.CleanupConsolidationTask
	plan                 cleanupConsolidationPlan
	journal              cleanupConsolidationJournal
	hooks                *consolidationExecutionHooks
	onChange             func(CleanupConsolidationSummary)
	lastProgress         time.Time
	completedSourceNames map[string]map[string]bool
}

// 注入点仅限同包故障测试，不形成客户端可修改的执行选项。
type consolidationExecutionHooks struct {
	targetMetadata func(string) error
	inventoryPath  func(context.Context, string) error
	reconcile      func(context.Context) error
	checkpoint     func(string, uint, int) error
	chunk          func(context.Context, int64) error
	forceCopy      bool
	freeBytes      func(string) (uint64, error)
}

func (e *consolidationExecution) checkpoint(phase string, id uint, index int) error {
	if e.hooks != nil && e.hooks.checkpoint != nil {
		return e.hooks.checkpoint(phase, id, index)
	}
	return nil
}

func (e *consolidationExecution) notify() {
	if e.onChange != nil {
		e.onChange(consolidationSummary(e.task))
	}
}

// progress 只持久化标量，复制节流点不反复编码整份文件日志。
func (e *consolidationExecution) progress(ctx context.Context, n int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.task.BytesDone += n
	if e.hooks != nil && e.hooks.chunk != nil {
		if err := e.hooks.chunk(ctx, n); err != nil {
			return err
		}
	}
	if time.Since(e.lastProgress) < 250*time.Millisecond {
		return nil
	}
	e.lastProgress = time.Now()
	changed := database.DB.Model(&models.CleanupConsolidationTask{}).Where("id = ? AND version = ? AND status = ?", e.task.ID, e.task.Version, "running").Updates(map[string]interface{}{"bytes_done": e.task.BytesDone, "version": e.task.Version + 1, "updated_at": e.lastProgress})
	if changed.Error != nil {
		return changed.Error
	}
	if changed.RowsAffected != 1 {
		return ErrConsolidationConflict
	}
	e.task.Version++
	e.task.UpdatedAt = e.lastProgress
	e.notify()
	return nil
}

func (e *consolidationExecution) finish(status string, err error) error {
	e.task.Status = status
	if err != nil {
		e.task.Error = err.Error()
	}
	now := time.Now()
	e.task.FinishedAt = &now
	e.task.ActiveSlot = nil
	return e.persist()
}

func (e *consolidationExecution) run(ctx context.Context, video *VideoService) {
	var runErr error
	currentIndex := -1
	for i := range e.plan.Preview.Items {
		if runErr = ctx.Err(); runErr != nil {
			break
		}
		currentIndex = i
		if runErr = video.executeConsolidationItem(ctx, e, i); runErr != nil {
			e.journal.Items[i].Error = runErr.Error()
			break
		}

		item := e.plan.Preview.Items[i]
		if !item.Stay {
			if e.completedSourceNames == nil {
				e.completedSourceNames = make(map[string]map[string]bool)
			}
			dir := filepath.Dir(item.Files[0].Source.RealPath)
			if e.completedSourceNames[dir] == nil {
				e.completedSourceNames[dir] = make(map[string]bool)
			}
			e.completedSourceNames[dir][filepath.Base(item.Files[0].Source.RealPath)] = true
		}
		currentIndex = -1
	}
	if errors.Is(runErr, ErrConsolidationConflict) {
		return
	}
	if runErr != nil {
		// 所有失败清理先完成，再释放活动槽；这里之后没有 deferred 文件删除。
		if currentIndex >= 0 {
			// 本轮执行者已确认的先前项不再摘要扫描；只收尾发生错误的当前家庭。
			committed, reconcileErr := video.reconcileConsolidationItem(context.WithoutCancel(ctx), e, currentIndex)
			if errors.Is(reconcileErr, ErrConsolidationConflict) {
				return
			}
			if committed {
				e.task.Completed = currentIndex + 1
			}
			runErr = errors.Join(runErr, reconcileErr)
			if reconcileErr != nil {
				e.journal.Items[currentIndex].Error = runErr.Error()
			}
			if err := e.persistItem(currentIndex); err != nil {
				log.Printf("[CleanupConsolidation] task=%d final item persistence failed: %v", e.task.ID, errors.Join(runErr, err))
				return
			}
		}
	}
	status := "completed"
	if runErr != nil {
		status = "failed"
		if errors.Is(runErr, context.Canceled) {
			status = "cancelled"
		}
	}
	if err := e.finish(status, runErr); err != nil {
		log.Printf("[CleanupConsolidation] task=%d terminal persistence failed: %v", e.task.ID, errors.Join(runErr, err))
	}
}

func newConsolidationJournal(preview CleanupConsolidationPreview) cleanupConsolidationJournal {
	journal := cleanupConsolidationJournal{SchemaVersion: cleanupConsolidationSchemaVersion, Items: make([]cleanupConsolidationItemJournal, len(preview.Items))}
	for i, item := range preview.Items {
		journal.Items[i] = cleanupConsolidationItemJournal{VideoID: item.VideoID, Phase: "planned", Files: make([]cleanupConsolidationFileJournal, len(item.Files))}
		for j, file := range item.Files {
			journal.Items[i].Files[j] = cleanupConsolidationFileJournal{SourcePath: file.Source.RealPath, Destination: file.Destination, SourceIdentity: file.Source.Identity, Phase: "planned"}
			if !item.Stay {
				suffix := fmt.Sprintf(".cineinsight-consolidation-%s-%d-%d", preview.PreviewID, item.VideoID, j)
				journal.Items[i].Files[j].TemporaryPath = filepath.Join(filepath.Dir(file.Destination), suffix+".part")
				if !file.CopyOnly {
					journal.Items[i].Files[j].StagedSourcePath = filepath.Join(filepath.Dir(file.Source.RealPath), suffix+".source")
				}
			}
		}
	}
	return journal
}
