package services

import (
	"context"
	"log"
	"path/filepath"
	"time"
	"video-master/database"
	"video-master/models"
)

// 预览与手动运行（需求设计文档 §3.3「手动运行」）：候选取可见范围内的条目，SQL 粗筛后在 Go 侧精筛，
// 账本里已有的组合不算、不重做。执行在 automation_rules 任务键下分批进行，可取消，同一时刻只允许一次。

// AutomationRuleSample 是预览里的一条示例；Directory 只用于界面上辨认条目。
type AutomationRuleSample struct {
	MediaKind string `json:"media_kind"`
	ID        uint   `json:"id"`
	Name      string `json:"name"`
	Directory string `json:"directory"`
}

// AutomationRulePreview 是 PreviewAutomationRule 的结果：会执行的条目数与前 50 条示例。
type AutomationRulePreview struct {
	RuleID  uint                   `json:"rule_id"`
	Count   int                    `json:"count"`
	Samples []AutomationRuleSample `json:"samples"`
}

// AutomationRuleRunStatus 是手动运行的进度，经事件 automation-rules-run 推给规则页。
type AutomationRuleRunStatus struct {
	Running    bool       `json:"running"`
	Cancelled  bool       `json:"cancelled"`
	Completed  bool       `json:"completed"`
	RuleIDs    []uint     `json:"rule_ids"`
	Total      int        `json:"total"`
	Processed  int        `json:"processed"`
	Matched    int        `json:"matched"`
	Added      int        `json:"added"`
	Error      string     `json:"error"`
	StartedAt  *time.Time `json:"started_at" ts_type:"string"`
	FinishedAt *time.Time `json:"finished_at" ts_type:"string"`
}

// automationRunFailedText 是运行失败时给界面的文案；底层错误只进日志（G-3）。
const automationRunFailedText = "规则运行中断：读写数据库失败，已完成的部分保留"

// loadAutomationRulesByID 按 position 顺序取出给定的规则并编译；有不存在的 id 报 rule_not_found，
// 条件坏了的报 invalid_rule（要先编辑）。
func loadAutomationRulesByID(ctx context.Context, ids []uint) ([]*compiledAutomationRule, error) {
	ids = uniqueUintIDs(ids)
	if len(ids) == 0 {
		return nil, invalidRule("rule_ids", "请选择要运行的规则")
	}
	var rows []models.AutomationRule
	if err := database.DB.WithContext(ctx).Where("id IN ?", ids).Order("position ASC").Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != len(ids) {
		return nil, ErrAutomationRuleNotFound
	}
	rules := make([]*compiledAutomationRule, 0, len(rows))
	for _, row := range rows {
		compiled, err := compileAutomationRule(row)
		if err != nil {
			if _, ok := err.(*AutomationRuleValidationError); ok {
				return nil, err
			}
			return nil, invalidRule("conditions", "规则内容已损坏，请编辑后重新保存")
		}
		rules = append(rules, compiled)
	}
	return rules, nil
}

// PreviewAutomationRule 返回这条规则对现有内容会执行的条目数与前 50 条示例（启用与否都能预览）。
func (s *AutomationRuleService) PreviewAutomationRule(ctx context.Context, ruleID uint) (AutomationRulePreview, error) {
	preview := AutomationRulePreview{RuleID: ruleID, Samples: []AutomationRuleSample{}}
	rules, err := loadAutomationRulesByID(ctx, []uint{ruleID})
	if err != nil {
		return preview, err
	}
	rule := rules[0]
	candidates, err := automationCandidateIDs(ctx, rule)
	if err != nil {
		return preview, err
	}
	for start := 0; start < len(candidates); start += automationBatchSize {
		if err := ctx.Err(); err != nil {
			return preview, err
		}
		page := candidates[start:min(start+automationBatchSize, len(candidates))]
		facts, err := loadAutomationFacts(ctx, rule.MediaKind, page, rule.usesCodec())
		if err != nil {
			return preview, err
		}
		ledger, err := loadAutomationLedger(ctx, rule.MediaKind, []uint{rule.ID}, page)
		if err != nil {
			return preview, err
		}
		_, matched := automationPendingFor(rules, facts, ledger)
		preview.Count += len(matched)
		preview.Samples = appendAutomationSamples(preview.Samples, rule.MediaKind, facts, matched)
	}
	return preview, nil
}

func appendAutomationSamples(samples []AutomationRuleSample, kind string, facts []automationMediaFacts, matched []uint) []AutomationRuleSample {
	if len(samples) >= automationPreviewSampleSize || len(matched) == 0 {
		return samples
	}
	byID := make(map[uint]automationMediaFacts, len(facts))
	for _, item := range facts {
		byID[item.ID] = item
	}
	for _, id := range matched {
		if len(samples) >= automationPreviewSampleSize {
			break
		}
		item := byID[id]
		samples = append(samples, AutomationRuleSample{MediaKind: kind, ID: id, Name: filepath.Base(item.Path), Directory: item.Directory})
	}
	return samples
}

func cloneAutomationRunStatus(status AutomationRuleRunStatus) AutomationRuleRunStatus {
	status.RuleIDs = append([]uint{}, status.RuleIDs...)
	return status
}

// GetAutomationRuleRunStatus 返回手动运行的当前进度（规则页打开时取一次，之后跟事件）。
func (s *AutomationRuleService) GetAutomationRuleRunStatus() AutomationRuleRunStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneAutomationRunStatus(s.run)
}

func (s *AutomationRuleService) emitRun() {
	s.mu.Lock()
	status, emitter := cloneAutomationRunStatus(s.run), s.emitter
	s.mu.Unlock()
	if emitter != nil {
		emitter(status)
	}
}

func (s *AutomationRuleService) updateRun(update func(status *AutomationRuleRunStatus)) {
	s.mu.Lock()
	update(&s.run)
	s.mu.Unlock()
	s.emitRun()
}

// RunAutomationRules 对现有内容运行给定的规则（启用与否都可以，按 position 依次跑）。异步执行，
// 立即返回起始状态；已有一次在跑时报 automation_run_busy。
func (s *AutomationRuleService) RunAutomationRules(ruleIDs []uint) (AutomationRuleRunStatus, error) {
	rules, err := loadAutomationRulesByID(context.Background(), ruleIDs)
	if err != nil {
		return s.GetAutomationRuleRunStatus(), err
	}
	s.mu.Lock()
	if s.run.Running {
		status := cloneAutomationRunStatus(s.run)
		s.mu.Unlock()
		return status, ErrAutomationRunBusy
	}
	if s.queueStopped {
		status := cloneAutomationRunStatus(s.run)
		s.mu.Unlock()
		return status, database.ErrMaintenance
	}
	base := s.baseCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	now := time.Now()
	ids := make([]uint, 0, len(rules))
	for _, rule := range rules {
		ids = append(ids, rule.ID)
	}
	s.run = AutomationRuleRunStatus{Running: true, RuleIDs: ids, StartedAt: &now}
	s.runCancel = cancel
	registry := s.registry
	s.worker.Add(1)
	status := cloneAutomationRunStatus(s.run)
	s.mu.Unlock()
	registry.Begin(BackgroundTaskAutomationRules)
	s.emitRun()
	go func() {
		defer s.worker.Done()
		defer registry.End(BackgroundTaskAutomationRules)
		s.executeRun(ctx, rules)
	}()
	return status, nil
}

// CancelAutomationRuleRun 取消正在进行的手动运行；已提交的批次保留。
func (s *AutomationRuleService) CancelAutomationRuleRun() AutomationRuleRunStatus {
	s.mu.Lock()
	cancel := s.runCancel
	status := cloneAutomationRunStatus(s.run)
	s.mu.Unlock()
	if cancel != nil && status.Running {
		cancel()
	}
	return status
}

func (s *AutomationRuleService) executeRun(ctx context.Context, rules []*compiledAutomationRule) {
	failure := ""
	candidates := make([][]uint, len(rules))
	total := 0
	for index, rule := range rules {
		ids, err := automationCandidateIDs(ctx, rule)
		if err != nil {
			failure = automationRunFailure(ctx, err)
			break
		}
		candidates[index] = ids
		total += len(ids)
	}
	s.updateRun(func(status *AutomationRuleRunStatus) { status.Total = total })
	for index, rule := range rules {
		if failure != "" || ctx.Err() != nil {
			break
		}
		if err := s.runOneRule(ctx, rule, candidates[index]); err != nil {
			failure = automationRunFailure(ctx, err)
		}
	}
	s.mu.Lock()
	now := time.Now()
	s.run.Running = false
	s.run.Completed = true
	s.run.Cancelled = ctx.Err() != nil
	s.run.Error = failure
	s.run.FinishedAt = &now
	cancel := s.runCancel
	s.runCancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.emitRun()
}

// runOneRule 分页（每页 500 条）求值并写入；整条规则跑完才记 last_run_at。
func (s *AutomationRuleService) runOneRule(ctx context.Context, rule *compiledAutomationRule, ids []uint) error {
	for start := 0; start < len(ids); start += automationBatchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		page := ids[start:min(start+automationBatchSize, len(ids))]
		matched, added, err := evaluateAutomationBatch(ctx, rule.MediaKind, []*compiledAutomationRule{rule}, page)
		s.updateRun(func(status *AutomationRuleRunStatus) {
			if err == nil {
				status.Processed += len(page)
			}
			status.Matched += len(matched)
			status.Added += added
		})
		if err != nil {
			return err
		}
		if s.afterRunPage != nil {
			s.afterRunPage()
		}
	}
	now := time.Now()
	return database.DB.WithContext(ctx).Model(&models.AutomationRule{}).Where("id = ?", rule.ID).Update("last_run_at", &now).Error
}

// automationRunFailure 把运行中的错误翻成界面文案：取消不算失败。
func automationRunFailure(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return ""
	}
	log.Printf("规则手动运行失败 err=%v", err)
	return automationRunFailedText
}
