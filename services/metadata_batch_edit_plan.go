package services

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 批量编辑的校验、目标解析与「哪些条目真正会变」的计算。预览与执行共用这里的计算，
// 保证预览里的数字就是执行时写入的条数。

// batchFieldPlan 是一个字段上的操作：标量字段至多一个 set / clear，关联字段至多一个 add 与一个 remove
// （同字段同动作的多个操作已合并）。值是 batchPlan.ops 的下标，-1 表示没有。
type batchFieldPlan struct {
	field               string
	scalar, add, remove int
}

type batchPlan struct {
	kind   string
	scope  string
	ops    []MetadataBatchOpCount
	fields []batchFieldPlan
}

func invalidBatchEdit(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidBatchEdit, reason)
}

// batchEditFieldActions 是 §2.4 的操作表：媒体类型 → 字段 → 允许的动作。
var batchEditFieldActions = map[string]map[string][]string{
	BatchEditKindVideo: {
		BatchEditFieldYear:       {BatchEditActionSet, BatchEditActionClear},
		BatchEditFieldRating:     {BatchEditActionSet, BatchEditActionClear},
		BatchEditFieldTag:        {BatchEditActionAdd, BatchEditActionRemove},
		BatchEditFieldPerson:     {BatchEditActionAdd, BatchEditActionRemove},
		BatchEditFieldCollection: {BatchEditActionAdd, BatchEditActionRemove},
	},
	BatchEditKindImage: {
		BatchEditFieldRating: {BatchEditActionSet, BatchEditActionClear},
		BatchEditFieldTag:    {BatchEditActionAdd, BatchEditActionRemove},
		BatchEditFieldPerson: {BatchEditActionAdd, BatchEditActionRemove},
	},
}

func batchFieldIsScalar(field string) bool {
	return field == BatchEditFieldYear || field == BatchEditFieldRating
}

// buildBatchPlan 校验请求并合并操作；任何一条不合规都返回 invalid_batch_edit。
func buildBatchPlan(req MetadataBatchEditRequest) (*batchPlan, error) {
	actions, ok := batchEditFieldActions[req.Kind]
	if !ok {
		return nil, invalidBatchEdit("不支持的媒体类型")
	}
	plan := &batchPlan{kind: req.Kind}
	scope, err := batchTargetScope(req.Kind, req.Target)
	if err != nil {
		return nil, err
	}
	plan.scope = scope
	if len(req.Ops) == 0 {
		return nil, invalidBatchEdit("至少要有一个操作")
	}
	fieldIndex := map[string]int{}
	for _, op := range req.Ops {
		if !slices.Contains(actions[op.Field], op.Action) {
			return nil, invalidBatchEdit("不支持的操作")
		}
		position, seen := fieldIndex[op.Field]
		if !seen {
			position = len(plan.fields)
			fieldIndex[op.Field] = position
			plan.fields = append(plan.fields, batchFieldPlan{field: op.Field, scalar: -1, add: -1, remove: -1})
		}
		if err := plan.addOp(&plan.fields[position], op); err != nil {
			return nil, err
		}
	}
	for _, field := range plan.fields {
		if field.add >= 0 && field.remove >= 0 && idsOverlap(plan.ops[field.add].IDs, plan.ops[field.remove].IDs) {
			return nil, invalidBatchEdit("同一个对象不能同时加上和去掉")
		}
	}
	return plan, nil
}

func batchTargetScope(kind string, target MetadataBatchEditTarget) (string, error) {
	hasFilter := target.Filter != nil
	if kind == BatchEditKindImage {
		hasFilter = target.ImageFilter != nil
	}
	wrongFilter := (kind == BatchEditKindVideo && target.ImageFilter != nil) || (kind == BatchEditKindImage && target.Filter != nil)
	hasIDs := len(target.IDs) > 0
	if wrongFilter || hasIDs == hasFilter || (hasIDs && len(uniqueUintIDs(target.IDs)) == 0) {
		return "", invalidBatchEdit("范围要么是所选条目，要么是当前筛选")
	}
	if hasIDs {
		return "ids", nil
	}
	return "filter", nil
}

func idsOverlap(left, right []uint) bool {
	set := idSet(left)
	for _, id := range right {
		if _, ok := set[id]; ok {
			return true
		}
	}
	return false
}

// addOp 把一个操作并入字段计划：标量字段只能出现一次（同时设为与清空、或设两个值都算矛盾），
// 关联字段同动作的 ID 合并。
func (p *batchPlan) addOp(field *batchFieldPlan, op MetadataBatchOp) error {
	if batchFieldIsScalar(op.Field) {
		if field.scalar >= 0 {
			return invalidBatchEdit("同一字段只能设为或清空一次")
		}
		value, err := batchScalarValue(op)
		if err != nil {
			return err
		}
		field.scalar = len(p.ops)
		p.ops = append(p.ops, MetadataBatchOpCount{Field: op.Field, Action: op.Action, Value: value, IDs: []uint{}, Names: []string{}})
		return nil
	}
	ids := uniqueUintIDs(op.IDs)
	if len(ids) == 0 {
		return invalidBatchEdit("请选择要加上或去掉的对象")
	}
	slot := &field.add
	if op.Action == BatchEditActionRemove {
		slot = &field.remove
	}
	if *slot >= 0 {
		merged := uniqueUintIDs(append(append([]uint{}, p.ops[*slot].IDs...), ids...))
		sort.Slice(merged, func(i, j int) bool { return merged[i] < merged[j] })
		p.ops[*slot].IDs = merged
		return nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	*slot = len(p.ops)
	p.ops = append(p.ops, MetadataBatchOpCount{Field: op.Field, Action: op.Action, IDs: ids, Names: []string{}})
	return nil
}

// batchScalarValue 校验 set 的取值：年份是 1870–2100 的整数，评分 0–10、步长 0.5。clear 不带值。
func batchScalarValue(op MetadataBatchOp) (*float64, error) {
	if op.Action == BatchEditActionClear {
		return nil, nil
	}
	if op.Value == nil {
		return nil, invalidBatchEdit("设为的值不能为空")
	}
	value := *op.Value
	if op.Field == BatchEditFieldYear {
		if math.IsNaN(value) || value != math.Trunc(value) || value < videoYearMin || value > videoYearMax {
			return nil, invalidBatchEdit("年份要在 1870–2100 之间")
		}
		return &value, nil
	}
	if err := validateRatingValue(&value); err != nil {
		return nil, invalidBatchEdit("评分要在 0–10 之间，步长 0.5")
	}
	return &value, nil
}

// resolveBatchReferences 确认引用的标签、人物、作品集都存在（标签不能是自动标签），并记下名字供摘要显示。
func resolveBatchReferences(db *gorm.DB, plan *batchPlan) error {
	for index := range plan.ops {
		op := &plan.ops[index]
		if batchFieldIsScalar(op.Field) {
			continue
		}
		names := map[uint]string{}
		switch op.Field {
		case BatchEditFieldTag:
			var tags []models.Tag
			if err := db.Where("id IN ?", op.IDs).Find(&tags).Error; err != nil {
				return err
			}
			for _, tag := range tags {
				if tag.AutomaticKind != "" {
					return invalidBatchEdit("自动标签由应用维护，不能批量修改")
				}
				names[tag.ID] = tag.Name
			}
		case BatchEditFieldPerson:
			var people []models.Person
			if err := db.Where("id IN ?", op.IDs).Find(&people).Error; err != nil {
				return err
			}
			for _, person := range people {
				names[person.ID] = person.DisplayName
			}
		case BatchEditFieldCollection:
			var collections []models.MediaCollection
			if err := db.Where("id IN ?", op.IDs).Find(&collections).Error; err != nil {
				return err
			}
			for _, collection := range collections {
				names[collection.ID] = collection.Name
			}
		}
		if len(names) != len(op.IDs) {
			return invalidBatchEdit("引用的标签、人物或作品集不存在，请刷新后重试")
		}
		op.Names = make([]string, 0, len(op.IDs))
		for _, id := range op.IDs {
			op.Names = append(op.Names, names[id])
		}
	}
	return nil
}

func batchMediaModel(kind string) any {
	if kind == BatchEditKindImage {
		return &models.Image{}
	}
	return &models.Video{}
}

// resolveBatchTargets 把范围解析成目标 ID（升序）。所选条目只留仍在库里（未删除）的；
// 筛选走片库 / 图片库列表的同一套条件（默认可见范围、失效只在「路径失效」视图里），每次调用都重新解析。
func resolveBatchTargets(kind string, target MetadataBatchEditTarget) ([]uint, error) {
	db := database.DB
	if db == nil {
		return nil, gorm.ErrInvalidDB
	}
	ids := []uint{}
	switch {
	case len(target.IDs) > 0:
		for _, chunk := range chunkUintIDs(uniqueUintIDs(target.IDs), metadataBatchChunk) {
			var found []uint
			if err := db.Model(batchMediaModel(kind)).Where("id IN ?", chunk).Pluck("id", &found).Error; err != nil {
				return nil, err
			}
			ids = append(ids, found...)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	case kind == BatchEditKindVideo:
		filter, err := normalizeLibraryFilter(*target.Filter)
		if err != nil {
			return nil, invalidBatchEdit(err.Error())
		}
		if libraryFilterNeedsSubtitleSync(filter) {
			if err := syncSubtitleIndexesFromFilesystem(); err != nil {
				return nil, err
			}
		}
		query, err := applyLibraryFilter(db.Model(&models.Video{}), filter, time.Now())
		if err != nil {
			return nil, err
		}
		if err := query.Order("videos.id ASC").Pluck("videos.id", &ids).Error; err != nil {
			return nil, err
		}
	default:
		filter, err := normalizeImageFilter(*target.ImageFilter)
		if err != nil {
			return nil, invalidBatchEdit(err.Error())
		}
		if err := applyImageFilter(db.Model(&models.Image{}), filter).Order("images.id ASC").Pluck("images.id", &ids).Error; err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// batchRelationTable 是关联字段对应的连接表与两列。
type batchRelationTable struct {
	name, mediaColumn, relColumn string
}

func batchRelationTableFor(kind, field string) batchRelationTable {
	if kind == BatchEditKindImage {
		if field == BatchEditFieldTag {
			return batchRelationTable{"image_tags", "image_id", "tag_id"}
		}
		return batchRelationTable{"image_people", "image_id", "person_id"}
	}
	switch field {
	case BatchEditFieldTag:
		return batchRelationTable{"video_tags", "video_id", "tag_id"}
	case BatchEditFieldPerson:
		return batchRelationTable{"video_people", "video_id", "person_id"}
	default:
		return batchRelationTable{"collection_videos", "video_id", "collection_id"}
	}
}

func batchScalarColumn(field string) string {
	if field == BatchEditFieldYear {
		return "year"
	}
	return "personal_rating"
}

// relIDs 是一个关联字段上 add 与 remove 涉及的全部对象 ID。
func (p *batchPlan) relIDs(field batchFieldPlan) []uint {
	ids := []uint{}
	if field.add >= 0 {
		ids = append(ids, p.ops[field.add].IDs...)
	}
	if field.remove >= 0 {
		ids = append(ids, p.ops[field.remove].IDs...)
	}
	return uniqueUintIDs(ids)
}

// summaryText 只用于日志：字段与动作，不含名字。
func (p *batchPlan) summaryText() string {
	parts := make([]string, 0, len(p.ops))
	for _, op := range p.ops {
		parts = append(parts, op.Field+":"+op.Action)
	}
	return strings.Join(parts, ",")
}
