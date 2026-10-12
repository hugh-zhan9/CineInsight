package services

import (
	"context"
	"errors"
	"sync"
	"time"
)

// 数据体检（D-DH-04 / D-DH-05，docs/loopx/design/2026-10-12-data-care 4.3）：手动运行，全程只读，
// 可取消，逐项给出结论、数量与示例；单项超时 60 秒记 timeout 后继续下一项。结果写进
// data_health_reports，只留最近 10 份。修复只给结果确定的三项（悬挂引用、派生计数、托管路径），
// 由用户逐项点击，每 500 条一批、写入条件与检查条件相同，修完重跑该项。

const (
	DataHealthCheckSQLiteIntegrity      = "sqlite_integrity"
	DataHealthCheckForeignKeys          = "foreign_keys"
	DataHealthCheckDanglingRefs         = "dangling_refs"
	DataHealthCheckMissingUniqueIndexes = "missing_unique_indexes"
	DataHealthCheckDerivedCounts        = "derived_counts"
	DataHealthCheckManagedPaths         = "managed_paths"
	DataHealthCheckBackups              = "backups"
	DataHealthCheckOrphanFiles          = "orphan_files"

	DataHealthStatusOK      = "ok"
	DataHealthStatusWarning = "warning"
	DataHealthStatusProblem = "problem"
	DataHealthStatusTimeout = "timeout"
	DataHealthStatusError   = "error"

	dataHealthCheckTimeout = 60 * time.Second
	dataHealthReportKeep   = 10
	dataHealthBatchSize    = 500
	dataHealthExampleLimit = 5
)

var (
	ErrDataHealthBusy      = errors.New("数据体检或修复正在进行，请等它结束")
	ErrDataHealthCancelled = errors.New("数据体检已取消")
)

// DataHealthGroup 是一项检查里的一个分组（一张表、一个索引、一个计数列……）。
type DataHealthGroup struct {
	Key        string   `json:"key"`
	Label      string   `json:"label"`
	Count      int64    `json:"count"`
	Detail     string   `json:"detail"`
	Examples   []string `json:"examples"`
	Repairable bool     `json:"repairable"`
}

// DataHealthCheckResult 是一项检查的结论。Repairable 表示现在点「修复」会改动数据；
// Link 是界面上的跳转（settings:backup、data-care:lost）。
type DataHealthCheckResult struct {
	Check      string            `json:"check"`
	Status     string            `json:"status"`
	Count      int64             `json:"count"`
	Bytes      int64             `json:"bytes"`
	Summary    string            `json:"summary"`
	Examples   []string          `json:"examples"`
	Groups     []DataHealthGroup `json:"groups"`
	Repairable bool              `json:"repairable"`
	Link       string            `json:"link"`
	DurationMS int64             `json:"duration_ms"`
}

type DataHealthReportView struct {
	ID         uint                    `json:"id"`
	Backend    string                  `json:"backend"`
	StartedAt  time.Time               `json:"started_at" ts_type:"string"`
	FinishedAt time.Time               `json:"finished_at" ts_type:"string"`
	Checks     []DataHealthCheckResult `json:"checks"`
}

// DataHealthProgress 随 data-health-progress 事件推给前端。Phase：check（体检中）、repair（修复中）、idle。
type DataHealthProgress struct {
	Phase string `json:"phase"`
	Check string `json:"check"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

type DataHealthState struct {
	Running  bool                  `json:"running"`
	Progress DataHealthProgress    `json:"progress"`
	Report   *DataHealthReportView `json:"report"`
}

type DataHealthRepairResult struct {
	Check     string                `json:"check"`
	Repaired  int64                 `json:"repaired"`
	Cancelled bool                  `json:"cancelled"`
	Result    DataHealthCheckResult `json:"result"`
}

// DataHealthService 是数据体检（D-DH-04 / D-DH-05）：只读检查与用户点击的修复。
type DataHealthService struct {
	dataDir      string
	registry     *BackgroundTaskRegistry
	backups      *BackupService
	orphans      func(ctx context.Context) (files int, bytes int64, err error)
	emit         func(DataHealthProgress)
	now          func() time.Time
	checkTimeout time.Duration

	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	progress DataHealthProgress
	wg       sync.WaitGroup
}

func NewDataHealthService(dataDir string) *DataHealthService {
	return &DataHealthService{dataDir: dataDir, now: time.Now, checkTimeout: dataHealthCheckTimeout,
		progress: DataHealthProgress{Phase: "idle"}}
}

func (s *DataHealthService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	if s != nil {
		s.registry = registry
	}
}

// SetBackupService 接上备份服务：「备份」检查项读它的状态与校验记录。
func (s *DataHealthService) SetBackupService(backups *BackupService) {
	if s != nil {
		s.backups = backups
	}
}

// SetOrphanFileSummary 接上孤儿文件汇总（失联页签的孤儿文件报告）。为空时体检不含这一项。
func (s *DataHealthService) SetOrphanFileSummary(summary func(ctx context.Context) (files int, bytes int64, err error)) {
	if s != nil {
		s.orphans = summary
	}
}

// SetProgressEmitter 接上进度推送（data-health-progress 事件）。
func (s *DataHealthService) SetProgressEmitter(emit func(DataHealthProgress)) {
	if s != nil {
		s.emit = emit
	}
}

// begin 占住「体检或修复」这一个运行位并登记任务键；同一时刻只允许一个。
func (s *DataHealthService) begin(parent context.Context, phase string) (context.Context, error) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil, ErrDataHealthBusy
	}
	ctx, cancel := context.WithCancel(parent)
	s.running = true
	s.cancel = cancel
	s.progress = DataHealthProgress{Phase: phase}
	s.wg.Add(1)
	s.mu.Unlock()
	// 登记表的回调在服务锁之外跑（与备份服务同一口径）。
	s.registry.Begin(BackgroundTaskDataHealth)
	return ctx, nil
}

func (s *DataHealthService) end() {
	s.registry.End(BackgroundTaskDataHealth)
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.running = false
	s.cancel = nil
	s.progress = DataHealthProgress{Phase: "idle"}
	progress := s.progress
	s.mu.Unlock()
	s.wg.Done()
	s.publish(progress)
}

func (s *DataHealthService) setProgress(check string, done, total int) {
	s.mu.Lock()
	s.progress.Check, s.progress.Done, s.progress.Total = check, done, total
	progress := s.progress
	s.mu.Unlock()
	s.publish(progress)
}

func (s *DataHealthService) publish(progress DataHealthProgress) {
	if s.emit != nil {
		s.emit(progress)
	}
}

// Cancel 取消进行中的体检或修复；修复已提交的批次保留。
func (s *DataHealthService) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

// StopAndWait 停止并等待进行中的工作（进入恢复维护与退出时调用）。
func (s *DataHealthService) StopAndWait() {
	if s == nil {
		return
	}
	s.Cancel()
	s.wg.Wait()
}
