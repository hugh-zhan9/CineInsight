package services

import (
	"context"
	"log"
	"video-master/database"
	"video-master/models"
)

// 新入库队列（需求设计文档 §3.3）：内存队列 + 一个工作协程，结构同下载查重队列，带独立的取消上下文。
// 入队方只记 id（创建入口持有扫描的路径读锁，那里不做任何写），评估在工作协程里做。
// 入队方：addVideoOrRestorePutBack 新建的行（恢复回放的行不入队）、addImage 新建的图片、技术信息补探测成功的视频。
// 回收站恢复、文件搬家纠偏、视频工作台与超分的产出都不经过这几处，不触发规则（D-RA-05）。

type automationQueueItem struct {
	Kind string
	ID   uint
}

// EnqueueVideo / EnqueueImage 把一条媒体排进规则队列；服务已停止（恢复维护、退出）时什么都不做。
func (s *AutomationRuleService) EnqueueVideo(id uint) {
	s.enqueue(automationQueueItem{Kind: AutomationMediaVideo, ID: id})
}

func (s *AutomationRuleService) EnqueueImage(id uint) {
	s.enqueue(automationQueueItem{Kind: AutomationMediaImage, ID: id})
}

func (s *AutomationRuleService) enqueue(item automationQueueItem) {
	if s == nil || item.ID == 0 {
		return
	}
	s.mu.Lock()
	if s.queueStopped {
		s.mu.Unlock()
		return
	}
	if s.queueCtx == nil {
		base := s.baseCtx
		if base == nil {
			base = context.Background()
		}
		s.queueCtx, s.queueCancel = context.WithCancel(base)
	}
	s.queue = append(s.queue, item)
	ctx := s.queueCtx
	start := !s.queueRunning
	if start {
		s.queueRunning = true
		s.worker.Add(1)
	}
	s.mu.Unlock()
	if start {
		go s.runQueue(ctx)
	}
}

// runQueue 每次取同一媒体类型的至多 500 条一起评估，队列空了就退出（下次入队再起）。
func (s *AutomationRuleService) runQueue(ctx context.Context) {
	defer s.worker.Done()
	for {
		s.mu.Lock()
		if len(s.queue) == 0 || ctx.Err() != nil {
			s.queue = nil
			s.queueRunning = false
			s.mu.Unlock()
			return
		}
		kind := s.queue[0].Kind
		ids := make([]uint, 0, min(len(s.queue), automationBatchSize))
		var rest []automationQueueItem
		for _, item := range s.queue {
			if item.Kind == kind && len(ids) < automationBatchSize {
				ids = append(ids, item.ID)
				continue
			}
			rest = append(rest, item)
		}
		s.queue = rest
		s.mu.Unlock()
		if err := s.evaluateNewMedia(ctx, kind, uniqueUintIDs(ids)); err != nil && ctx.Err() == nil {
			log.Printf("规则评估失败 kind=%s count=%d err=%v", kind, len(ids), err)
		}
	}
}

// evaluateNewMedia 用启用中、同媒体类型的规则（按 position）评估一批媒体并执行待写动作。
func (s *AutomationRuleService) evaluateNewMedia(ctx context.Context, kind string, ids []uint) error {
	rules, err := loadEnabledAutomationRules(ctx, kind)
	if err != nil || len(rules) == 0 {
		return err
	}
	_, _, err = evaluateAutomationBatch(ctx, kind, rules, ids)
	return err
}

// evaluateAutomationBatch 是新入库与手动运行共用的一步：取属性 → 求值 → 查账本 → 写入。
// 返回有待执行动作的媒体 id 与真正插入的关系数。
func evaluateAutomationBatch(ctx context.Context, kind string, rules []*compiledAutomationRule, ids []uint) ([]uint, int, error) {
	needCodec := false
	ruleIDs := make([]uint, 0, len(rules))
	for _, rule := range rules {
		needCodec = needCodec || rule.usesCodec()
		ruleIDs = append(ruleIDs, rule.ID)
	}
	facts, err := loadAutomationFacts(ctx, kind, ids, needCodec)
	if err != nil {
		return nil, 0, err
	}
	ledger, err := loadAutomationLedger(ctx, kind, ruleIDs, ids)
	if err != nil {
		return nil, 0, err
	}
	writes, matched := automationPendingFor(rules, facts, ledger)
	added, err := applyAutomationWrites(ctx, kind, writes)
	return matched, added, err
}

// loadEnabledAutomationRules 取启用中、同媒体类型的规则（按 position）。解析或校验不过的规则
// （库里数据坏了）就地停用并注明 invalid_condition，不让它反复报错。
func loadEnabledAutomationRules(ctx context.Context, kind string) ([]*compiledAutomationRule, error) {
	var rows []models.AutomationRule
	if err := database.DB.WithContext(ctx).Where("media_kind = ? AND enabled = ?", kind, true).
		Order("position ASC").Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	rules := make([]*compiledAutomationRule, 0, len(rows))
	for _, row := range rows {
		compiled, err := compileAutomationRule(row)
		if err != nil {
			log.Printf("规则无法求值，已停用 rule=%d err=%v", row.ID, err)
			if updateErr := database.DB.WithContext(ctx).Model(&models.AutomationRule{}).Where("id = ?", row.ID).
				Updates(map[string]any{"enabled": false, "disabled_reason": automationDisabledInvalidCondition}).Error; updateErr != nil {
				return nil, updateErr
			}
			continue
		}
		rules = append(rules, compiled)
	}
	return rules, nil
}

// Start 接上运行时上下文：队列与手动运行都从它派生（退出路径仍要显式 StopAndWait）。
func (s *AutomationRuleService) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.baseCtx = ctx
	s.mu.Unlock()
}

// StopAndWait 停止并等待进行中的工作（进入恢复维护与退出时调用）：清空队列、取消评估与手动运行，
// 之后不再接受入队，直到 Resume。
func (s *AutomationRuleService) StopAndWait() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.queueStopped = true
	s.queue = nil
	queueCancel, runCancel := s.queueCancel, s.runCancel
	s.mu.Unlock()
	if queueCancel != nil {
		queueCancel()
	}
	if runCancel != nil {
		runCancel()
	}
	s.worker.Wait()
	s.mu.Lock()
	s.queueCtx, s.queueCancel = nil, nil
	s.mu.Unlock()
}

// Resume 在数据库恢复失败续跑时重新接受入队。
func (s *AutomationRuleService) Resume() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.queueStopped = false
	s.mu.Unlock()
}
