package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// dataHealthCheck 是一项检查：run 只读；repair 非空的项才有修复（D-DH-05）。
type dataHealthCheck struct {
	key    string
	run    func(ctx context.Context, db *gorm.DB) (DataHealthCheckResult, error)
	repair func(ctx context.Context, db *gorm.DB) (int64, error)
}

func dataHealthIsSQLite(db *gorm.DB) bool {
	return db.Dialector.Name() == string(database.BackendSQLite)
}

// checks 按后端给出检查清单：数据库文件完整性与外键检查只在 SQLite 上有；孤儿文件汇总没接上时不列。
func (s *DataHealthService) checks(db *gorm.DB) []dataHealthCheck {
	list := []dataHealthCheck{}
	if dataHealthIsSQLite(db) {
		list = append(list,
			dataHealthCheck{key: DataHealthCheckSQLiteIntegrity, run: checkSQLiteIntegrity},
			dataHealthCheck{key: DataHealthCheckForeignKeys, run: checkSQLiteForeignKeys},
		)
	}
	list = append(list,
		dataHealthCheck{key: DataHealthCheckDanglingRefs, run: checkDanglingRefs, repair: repairDanglingRefs},
		dataHealthCheck{key: DataHealthCheckMissingUniqueIndexes, run: checkMissingUniqueIndexes},
		dataHealthCheck{key: DataHealthCheckDerivedCounts, run: checkDerivedCounts, repair: repairDerivedCounts},
		dataHealthCheck{key: DataHealthCheckManagedPaths, run: s.checkManagedPaths, repair: s.repairManagedPaths},
		dataHealthCheck{key: DataHealthCheckBackups, run: s.checkBackups},
	)
	if s.orphans != nil {
		list = append(list, dataHealthCheck{key: DataHealthCheckOrphanFiles, run: s.checkOrphanFiles})
	}
	return list
}

// Run 跑一次完整体检并保存报告。取消时不保存（半份报告会误导），返回 ErrDataHealthCancelled。
func (s *DataHealthService) Run(parent context.Context) (*DataHealthReportView, error) {
	db := database.DB
	if db == nil {
		return nil, errors.New("数据库未初始化")
	}
	ctx, err := s.begin(parent, "check")
	if err != nil {
		return nil, err
	}
	defer s.end()
	started := s.now()
	checks := s.checks(db)
	results := make([]DataHealthCheckResult, 0, len(checks))
	for index, check := range checks {
		s.setProgress(check.key, index, len(checks))
		result := s.runCheck(ctx, db, check)
		if ctx.Err() != nil {
			return nil, ErrDataHealthCancelled
		}
		results = append(results, result)
	}
	s.setProgress("", len(checks), len(checks))
	report := &DataHealthReportView{Backend: db.Dialector.Name(), StartedAt: started, FinishedAt: s.now(), Checks: results}
	if err := saveDataHealthReport(ctx, db, report); err != nil {
		return nil, fmt.Errorf("保存体检报告失败: %w", err)
	}
	return report, nil
}

// runCheck 给单项一个 60 秒窗口：超时记 timeout、出错记 error（原文只进日志），都继续下一项。
func (s *DataHealthService) runCheck(ctx context.Context, db *gorm.DB, check dataHealthCheck) DataHealthCheckResult {
	checkCtx, cancel := context.WithTimeout(ctx, s.checkTimeout)
	defer cancel()
	started := time.Now()
	result, err := check.run(checkCtx, db.WithContext(checkCtx))
	result.Check = check.key
	result.DurationMS = time.Since(started).Milliseconds()
	switch {
	case ctx.Err() != nil:
	case errors.Is(checkCtx.Err(), context.DeadlineExceeded):
		result = DataHealthCheckResult{Check: check.key, Status: DataHealthStatusTimeout,
			Summary: "检查超过 60 秒未完成，已跳过", DurationMS: result.DurationMS}
	case err != nil:
		log.Printf("数据体检 %s 失败 err=%v", check.key, err)
		result = DataHealthCheckResult{Check: check.key, Status: DataHealthStatusError,
			Summary: "检查没能完成", DurationMS: result.DurationMS}
	}
	if result.Examples == nil {
		result.Examples = []string{}
	}
	if result.Groups == nil {
		result.Groups = []DataHealthGroup{}
	}
	return result
}

// Repair 执行一项检查的修复（只接受有修复的三项），修完重跑该项并更新最近一份报告里的这一项。
// 被取消时已提交的批次保留，结果里报告已修复多少条，不重跑。
func (s *DataHealthService) Repair(parent context.Context, key string) (DataHealthRepairResult, error) {
	db := database.DB
	if db == nil {
		return DataHealthRepairResult{}, errors.New("数据库未初始化")
	}
	var target *dataHealthCheck
	for _, check := range s.checks(db) {
		if check.key == key && check.repair != nil {
			target = &check
			break
		}
	}
	if target == nil {
		return DataHealthRepairResult{}, errors.New("这一项没有可执行的修复")
	}
	ctx, err := s.begin(parent, "repair")
	if err != nil {
		return DataHealthRepairResult{}, err
	}
	defer s.end()
	s.setProgress(key, 0, 1)
	repaired, err := target.repair(ctx, db.WithContext(ctx))
	if ctx.Err() != nil {
		return DataHealthRepairResult{Check: key, Repaired: repaired, Cancelled: true}, nil
	}
	if err != nil {
		log.Printf("数据体检修复 %s 失败 repaired=%d err=%v", key, repaired, err)
		return DataHealthRepairResult{Check: key, Repaired: repaired}, fmt.Errorf("修复中途失败，已修复 %d 条", repaired)
	}
	result := s.runCheck(ctx, db, *target)
	if err := updateLatestDataHealthReport(ctx, db, result); err != nil {
		log.Printf("更新体检报告失败 err=%v", err)
	}
	s.setProgress(key, 1, 1)
	return DataHealthRepairResult{Check: key, Repaired: repaired, Result: result}, nil
}

// Latest 返回运行状态与最近一份报告（没有报告时为空）。
func (s *DataHealthService) Latest(ctx context.Context) (DataHealthState, error) {
	s.mu.Lock()
	state := DataHealthState{Running: s.running, Progress: s.progress}
	s.mu.Unlock()
	if database.DB == nil {
		return state, errors.New("数据库未初始化")
	}
	var row models.DataHealthReport
	err := database.DB.WithContext(ctx).Order("id DESC").Limit(1).Find(&row).Error
	if err != nil {
		return state, err
	}
	if row.ID == 0 {
		return state, nil
	}
	report, err := decodeDataHealthReport(row)
	if err != nil {
		return state, err
	}
	state.Report = report
	return state, nil
}

type dataHealthSummary struct {
	Backend string                  `json:"backend"`
	Checks  []DataHealthCheckResult `json:"checks"`
}

func decodeDataHealthReport(row models.DataHealthReport) (*DataHealthReportView, error) {
	var summary dataHealthSummary
	if err := json.Unmarshal([]byte(row.SummaryJSON), &summary); err != nil {
		return nil, errors.New("体检报告已损坏")
	}
	report := &DataHealthReportView{ID: row.ID, Backend: summary.Backend, StartedAt: row.StartedAt, Checks: summary.Checks}
	if row.FinishedAt != nil {
		report.FinishedAt = *row.FinishedAt
	}
	return report, nil
}

// saveDataHealthReport 写入报告并只留最近 10 份（一个短事务）。
func saveDataHealthReport(ctx context.Context, db *gorm.DB, report *DataHealthReportView) error {
	data, err := json.Marshal(dataHealthSummary{Backend: report.Backend, Checks: report.Checks})
	if err != nil {
		return err
	}
	finished := report.FinishedAt
	row := models.DataHealthReport{StartedAt: report.StartedAt, FinishedAt: &finished, SummaryJSON: string(data)}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		report.ID = row.ID
		return tx.Exec(`DELETE FROM data_health_reports WHERE id NOT IN
			(SELECT id FROM data_health_reports ORDER BY id DESC LIMIT ?)`, dataHealthReportKeep).Error
	})
}

// updateLatestDataHealthReport 把修复后重跑的结果替换进最近一份报告里的同名一项。
func updateLatestDataHealthReport(ctx context.Context, db *gorm.DB, result DataHealthCheckResult) error {
	var row models.DataHealthReport
	if err := db.WithContext(ctx).Order("id DESC").Limit(1).Find(&row).Error; err != nil || row.ID == 0 {
		return err
	}
	report, err := decodeDataHealthReport(row)
	if err != nil {
		return err
	}
	for index := range report.Checks {
		if report.Checks[index].Check == result.Check {
			report.Checks[index] = result
		}
	}
	data, err := json.Marshal(dataHealthSummary{Backend: report.Backend, Checks: report.Checks})
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Model(&models.DataHealthReport{}).Where("id = ?", row.ID).
		Update("summary_json", string(data)).Error
}
