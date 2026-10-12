package services

import (
	"encoding/json"
	"math"

	"gorm.io/gorm"
)

// batchMediaState 是一批目标的当前值：标量按字段、关联只取本次操作涉及的对象。
// ids 是这批里仍在库里的目标（解析之后被删掉的不再处理）。
type batchMediaState struct {
	ids    []uint
	scalar map[string]map[uint]*float64
	rels   map[string]map[uint]map[uint]struct{}
}

type batchScalarRow struct {
	ID             uint
	Year           *int
	PersonalRating *float64
}

type batchRelationPair struct {
	MediaID uint
	RelID   uint
}

func loadBatchMediaState(db *gorm.DB, plan *batchPlan, ids []uint) (*batchMediaState, error) {
	state := &batchMediaState{
		ids:    []uint{},
		scalar: map[string]map[uint]*float64{BatchEditFieldYear: {}, BatchEditFieldRating: {}},
		rels:   map[string]map[uint]map[uint]struct{}{},
	}
	columns := "id, personal_rating"
	if plan.kind == BatchEditKindVideo {
		columns = "id, year, personal_rating"
	}
	var rows []batchScalarRow
	if err := db.Model(batchMediaModel(plan.kind)).Select(columns).Where("id IN ?", ids).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		state.ids = append(state.ids, row.ID)
		if row.Year != nil {
			year := float64(*row.Year)
			state.scalar[BatchEditFieldYear][row.ID] = &year
		}
		state.scalar[BatchEditFieldRating][row.ID] = row.PersonalRating
	}
	if len(state.ids) == 0 {
		return state, nil
	}
	for _, field := range plan.fields {
		if batchFieldIsScalar(field.field) {
			continue
		}
		table := batchRelationTableFor(plan.kind, field.field)
		var pairs []batchRelationPair
		if err := db.Table(table.name).
			Select(table.mediaColumn+" AS media_id, "+table.relColumn+" AS rel_id").
			Where(table.mediaColumn+" IN ? AND "+table.relColumn+" IN ?", state.ids, plan.relIDs(field)).
			Scan(&pairs).Error; err != nil {
			return nil, err
		}
		present := map[uint]map[uint]struct{}{}
		for _, pair := range pairs {
			if present[pair.MediaID] == nil {
				present[pair.MediaID] = map[uint]struct{}{}
			}
			present[pair.MediaID][pair.RelID] = struct{}{}
		}
		state.rels[field.field] = present
	}
	return state, nil
}

// batchItemChange 是一个（条目，字段）上真正发生的改变。
// 标量记改前改后的值；关联只记本次真正加上（added）与去掉（removed）的对象。
type batchItemChange struct {
	mediaID       uint
	field         string
	before, after *float64
	added         []uint
	removed       []uint
}

// itemJSON 是写进撤销明细的 before_json / after_json。关联字段：before 是本次去掉的 ID（改前在、改后不在），
// after 是本次加上的 ID（改前不在、改后在）。
func (c batchItemChange) itemJSON() (string, string) {
	if batchFieldIsScalar(c.field) {
		return mustBatchJSON(c.before), mustBatchJSON(c.after)
	}
	return mustBatchJSON(nonNilIDs(c.removed)), mustBatchJSON(nonNilIDs(c.added))
}

func mustBatchJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(data)
}

func nonNilIDs(ids []uint) []uint {
	if ids == nil {
		return []uint{}
	}
	return ids
}

func sameBatchScalar(left, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return math.Abs(*left-*right) < 1e-9
}

// computeBatchChanges 算出这批目标上真正会改变的（条目，字段），以及每个操作影响的条目数和改变了的条目数。
// 已经是目标值、已有或本来没有该关联的不算（§2.4 预览口径）。
func computeBatchChanges(plan *batchPlan, state *batchMediaState) ([]batchItemChange, []int, int) {
	counts := make([]int, len(plan.ops))
	changes := []batchItemChange{}
	changedMedia := 0
	for _, id := range state.ids {
		mediaChanged := false
		for _, field := range plan.fields {
			change := batchItemChange{mediaID: id, field: field.field}
			if field.scalar >= 0 {
				target := plan.ops[field.scalar].Value
				current := state.scalar[field.field][id]
				if sameBatchScalar(current, target) {
					continue
				}
				change.before, change.after = current, target
				counts[field.scalar]++
			} else {
				present := state.rels[field.field][id]
				if field.add >= 0 {
					for _, relID := range plan.ops[field.add].IDs {
						if _, ok := present[relID]; !ok {
							change.added = append(change.added, relID)
						}
					}
				}
				if field.remove >= 0 {
					for _, relID := range plan.ops[field.remove].IDs {
						if _, ok := present[relID]; ok {
							change.removed = append(change.removed, relID)
						}
					}
				}
				if len(change.added) > 0 {
					counts[field.add]++
				}
				if len(change.removed) > 0 {
					counts[field.remove]++
				}
				if len(change.added) == 0 && len(change.removed) == 0 {
					continue
				}
			}
			changes = append(changes, change)
			mediaChanged = true
		}
		if mediaChanged {
			changedMedia++
		}
	}
	return changes, counts, changedMedia
}
