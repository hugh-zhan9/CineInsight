package services

import (
	"context"
	"log"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// PreviewMetadataBatchEdit 给出目标条目数与每个操作实际会改变的条目数（只读）。
func (s *MetadataBatchEditService) PreviewMetadataBatchEdit(req MetadataBatchEditRequest) (*MetadataBatchEditPreview, error) {
	plan, err := prepareBatchPlan(req)
	if err != nil {
		return nil, err
	}
	ids, err := resolveBatchTargets(plan.kind, req.Target)
	if err != nil {
		return nil, err
	}
	preview := &MetadataBatchEditPreview{Kind: plan.kind, TargetCount: len(ids), Ops: copyBatchOps(plan.ops)}
	for _, chunk := range chunkUintIDs(ids, metadataBatchChunk) {
		if len(chunk) == 0 {
			continue
		}
		state, err := loadBatchMediaState(database.DB, plan, chunk)
		if err != nil {
			return nil, err
		}
		_, counts, changed := computeBatchChanges(plan, state)
		addBatchCounts(preview.Ops, counts)
		preview.ChangedCount += changed
	}
	return preview, nil
}

// prepareBatchPlan 校验请求，并确认引用的对象都存在。
func prepareBatchPlan(req MetadataBatchEditRequest) (*batchPlan, error) {
	plan, err := buildBatchPlan(req)
	if err != nil {
		return nil, err
	}
	if database.DB == nil {
		return nil, gorm.ErrInvalidDB
	}
	if err := resolveBatchReferences(database.DB, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func copyBatchOps(ops []MetadataBatchOpCount) []MetadataBatchOpCount {
	copied := make([]MetadataBatchOpCount, len(ops))
	copy(copied, ops)
	for index := range copied {
		copied[index].Changes = 0
	}
	return copied
}

func addBatchCounts(ops []MetadataBatchOpCount, counts []int) {
	for index := range ops {
		ops[index].Changes += counts[index]
	}
}

// ApplyMetadataBatchEdit 执行批量编辑（D-MC-04）：筛选此时重新解析成 ID，每 500 条一个事务，
// 只写真正改变的行，并在同一事务里写撤销明细。批与批之间检查取消，已提交的批次保留。
func (s *MetadataBatchEditService) ApplyMetadataBatchEdit(parent context.Context, req MetadataBatchEditRequest) (result *MetadataBatchEditResult, err error) {
	plan, err := prepareBatchPlan(req)
	if err != nil {
		return nil, err
	}
	ctx, end, err := s.begin(parent, 0)
	if err != nil {
		return nil, err
	}
	defer func() { end(err) }()
	ids, err := resolveBatchTargets(plan.kind, req.Target)
	if err != nil {
		return nil, err
	}
	s.setTotal(len(ids))
	result = &MetadataBatchEditResult{Kind: plan.kind, TargetCount: len(ids), Ops: copyBatchOps(plan.ops)}
	run := &batchApplyRun{plan: plan, result: result}
	for _, chunk := range chunkUintIDs(ids, metadataBatchChunk) {
		if ctx.Err() != nil {
			result.Cancelled = true
			break
		}
		if len(chunk) == 0 {
			continue
		}
		if err := database.Transaction(func(tx *gorm.DB) error { return run.applyChunk(tx, chunk) }); err != nil {
			log.Printf("批量编辑写入失败 batch_id=%d processed=%d ops=%s err=%v", result.BatchID, result.Processed, plan.summaryText(), err)
			return nil, err
		}
		run.commit()
		result.Processed += len(chunk)
		s.progress(result.Processed)
	}
	if result.Cancelled && result.BatchID != 0 {
		if err := database.Transaction(func(tx *gorm.DB) error { return run.writeSummary(tx) }); err != nil {
			return nil, err
		}
	}
	log.Printf("批量编辑完成 batch_id=%d kind=%s targets=%d processed=%d changed=%d cancelled=%v ops=%s",
		result.BatchID, plan.kind, result.TargetCount, result.Processed, result.ChangedCount, result.Cancelled, plan.summaryText())
	return result, nil
}

func (s *MetadataBatchEditService) setTotal(total int) {
	s.mu.Lock()
	s.status.Total = total
	s.mu.Unlock()
}

// batchApplyRun 累计一次执行的结果。next 是当前事务提交后的累计，事务失败就丢掉；
// commit 在事务提交之后把它转正。
type batchApplyRun struct {
	plan      *batchPlan
	result    *MetadataBatchEditResult
	items     int
	next      MetadataBatchEditResult
	nextItems int
}

func (r *batchApplyRun) applyChunk(tx *gorm.DB, chunk []uint) error {
	r.next = *r.result
	r.next.Ops = append([]MetadataBatchOpCount(nil), r.result.Ops...)
	r.nextItems = r.items
	state, err := loadBatchMediaState(tx, r.plan, chunk)
	if err != nil {
		return err
	}
	changes, counts, changed := computeBatchChanges(r.plan, state)
	// 没有改变也要走一遍：批量加人物对已有的关系同样释放人脸链路的写入记录（同 AddPersonVideos）。
	if err := writeBatchChanges(tx, r.plan, state, changes); err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	addBatchCounts(r.next.Ops, counts)
	r.next.ChangedCount += changed
	if r.next.BatchID == 0 {
		batchID, err := createBatchEditRecord(tx, r.plan.kind)
		if err != nil {
			return err
		}
		r.next.BatchID = batchID
	}
	if err := insertBatchEditItems(tx, r.next.BatchID, changes); err != nil {
		return err
	}
	r.nextItems += len(changes)
	return r.writeSummaryOf(tx, &r.next, r.nextItems)
}

func (r *batchApplyRun) commit() {
	processed := r.result.Processed
	*r.result = r.next
	r.result.Processed = processed
	r.items = r.nextItems
}

// writeSummary 在取消后补记一次摘要（带上「已取消」）。
func (r *batchApplyRun) writeSummary(tx *gorm.DB) error {
	return r.writeSummaryOf(tx, r.result, r.items)
}

func (r *batchApplyRun) writeSummaryOf(tx *gorm.DB, result *MetadataBatchEditResult, items int) error {
	summary := MetadataBatchEditSummary{
		Scope: r.plan.scope, TargetCount: result.TargetCount, ChangedCount: result.ChangedCount,
		Ops: result.Ops, Cancelled: result.Cancelled,
	}
	return tx.Model(&models.MetadataBatchEdit{}).Where("id = ?", result.BatchID).
		Updates(map[string]any{"summary_json": mustBatchJSON(summary), "item_count": items}).Error
}

// createBatchEditRecord 写入一条批量编辑记录，并删掉最近 20 次以外的旧记录（D-MC-05）；
// 明细随外键 ON DELETE CASCADE 一并删除（SQLite 连接开着 _foreign_keys，D-009）。
func createBatchEditRecord(tx *gorm.DB, kind string) (uint, error) {
	record := models.MetadataBatchEdit{Kind: kind, SummaryJSON: "{}"}
	if err := tx.Create(&record).Error; err != nil {
		return 0, err
	}
	var keep []uint
	if err := tx.Model(&models.MetadataBatchEdit{}).Order("id DESC").Limit(metadataBatchKeep).Pluck("id", &keep).Error; err != nil {
		return 0, err
	}
	if err := tx.Where("id NOT IN ?", keep).Delete(&models.MetadataBatchEdit{}).Error; err != nil {
		return 0, err
	}
	return record.ID, nil
}

func insertBatchEditItems(tx *gorm.DB, batchID uint, changes []batchItemChange) error {
	items := make([]models.MetadataBatchEditItem, 0, len(changes))
	for _, change := range changes {
		before, after := change.itemJSON()
		items = append(items, models.MetadataBatchEditItem{BatchID: batchID, MediaID: change.mediaID, Field: change.field, BeforeJSON: before, AfterJSON: after})
	}
	return tx.CreateInBatches(&items, 200).Error
}
