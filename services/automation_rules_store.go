package services

import (
	"encoding/json"
	"fmt"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 规则存取（需求设计文档 §3.5）：列表、新建/更新（带校验）、删除、排序、启停。

// decodeAutomationRule 解析库里的条件与动作 JSON；解析失败说明数据坏了，调用方按 invalid_condition 处理。
func decodeAutomationRule(rule models.AutomationRule) ([]AutomationRuleCondition, []AutomationRuleAction, error) {
	conditions := []AutomationRuleCondition{}
	actions := []AutomationRuleAction{}
	if err := json.Unmarshal([]byte(rule.ConditionsJSON), &conditions); err != nil {
		return nil, nil, fmt.Errorf("解析规则条件失败 rule=%d: %w", rule.ID, err)
	}
	if rule.ActionsJSON != "" {
		if err := json.Unmarshal([]byte(rule.ActionsJSON), &actions); err != nil {
			return nil, nil, fmt.Errorf("解析规则动作失败 rule=%d: %w", rule.ID, err)
		}
	}
	return conditions, actions, nil
}

func encodeAutomationJSON(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

// ListAutomationRules 按 position 返回全部规则，动作带上目标当前名称。
func (s *AutomationRuleService) ListAutomationRules() ([]AutomationRuleView, error) {
	var rules []models.AutomationRule
	if err := database.DB.Order("position ASC").Order("id ASC").Find(&rules).Error; err != nil {
		return nil, err
	}
	tagNames, collectionNames, err := automationTargetNames(rules)
	if err != nil {
		return nil, err
	}
	views := make([]AutomationRuleView, 0, len(rules))
	for _, rule := range rules {
		conditions, actions, decodeErr := decodeAutomationRule(rule)
		if decodeErr != nil {
			conditions, actions = []AutomationRuleCondition{}, []AutomationRuleAction{}
		}
		view := AutomationRuleView{
			ID: rule.ID, Name: rule.Name, MediaKind: rule.MediaKind, Enabled: rule.Enabled, Position: rule.Position,
			Conditions: conditions, Actions: make([]AutomationRuleActionView, 0, len(actions)),
			DisabledReason: rule.DisabledReason, LastRunAt: rule.LastRunAt, CreatedAt: rule.CreatedAt, UpdatedAt: rule.UpdatedAt,
		}
		for _, action := range actions {
			name := tagNames[action.TagID]
			if action.Type == automationActionAddToCollection {
				name = collectionNames[action.CollectionID]
			}
			view.Actions = append(view.Actions, AutomationRuleActionView{AutomationRuleAction: action, TargetName: name})
		}
		views = append(views, view)
	}
	return views, nil
}

// automationTargetNames 一次取出全部规则引用的标签与作品集名称（只取未删除的）。
func automationTargetNames(rules []models.AutomationRule) (map[uint]string, map[uint]string, error) {
	tagIDs, collectionIDs := []uint{}, []uint{}
	for _, rule := range rules {
		_, actions, err := decodeAutomationRule(rule)
		if err != nil {
			continue
		}
		for _, action := range actions {
			if action.Type == automationActionAddToCollection {
				collectionIDs = append(collectionIDs, action.CollectionID)
			} else {
				tagIDs = append(tagIDs, action.TagID)
			}
		}
	}
	tagNames, collectionNames := map[uint]string{}, map[uint]string{}
	if len(tagIDs) > 0 {
		var tags []models.Tag
		if err := database.DB.Select("id", "name").Where("id IN ?", uniqueUintIDs(tagIDs)).Find(&tags).Error; err != nil {
			return nil, nil, err
		}
		for _, tag := range tags {
			tagNames[tag.ID] = tag.Name
		}
	}
	if len(collectionIDs) > 0 {
		var collections []models.MediaCollection
		if err := database.DB.Select("id", "name").Where("id IN ?", uniqueUintIDs(collectionIDs)).Find(&collections).Error; err != nil {
			return nil, nil, err
		}
		for _, collection := range collections {
			collectionNames[collection.ID] = collection.Name
		}
	}
	return tagNames, collectionNames, nil
}

// checkAutomationTargetsTx 确认动作引用的标签与作品集存在且可用：标签未删除、不是自动标签；作品集未删除。
func checkAutomationTargetsTx(tx *gorm.DB, actions []AutomationRuleAction) error {
	for index, action := range actions {
		field := fmt.Sprintf("actions[%d]", index)
		if action.Type == automationActionAddToCollection {
			var count int64
			if err := tx.Model(&models.MediaCollection{}).Where("id = ?", action.CollectionID).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return invalidRule(field+".collection_id", "作品集不存在或已删除")
			}
			continue
		}
		var tag models.Tag
		if err := tx.Select("id", "automatic_kind").Where("id = ?", action.TagID).Limit(1).Find(&tag).Error; err != nil {
			return err
		}
		if tag.ID == 0 {
			return invalidRule(field+".tag_id", "标签不存在或已删除")
		}
		// 两个自动标签由应用按时长/分辨率维护并有覆盖表，规则不往里写（与图片侧拒绝自动标签同口径）。
		if tag.AutomaticKind != "" {
			return invalidRule(field+".tag_id", "自动标签由应用维护，不能用在规则里")
		}
	}
	return nil
}

// SaveAutomationRule 新建（ID 为 0）或更新一条规则。保存成功即清掉停用原因；新建排在最后。
// 新建或修改规则不会自动扫一遍现有内容（D-RA-05），要用户点「对现有内容运行」。
func (s *AutomationRuleService) SaveAutomationRule(input AutomationRuleInput) (AutomationRuleView, error) {
	normalized, err := normalizeAutomationRuleInput(input)
	if err != nil {
		return AutomationRuleView{}, err
	}
	conditionsJSON, err := encodeAutomationJSON(normalized.Conditions)
	if err != nil {
		return AutomationRuleView{}, err
	}
	actionsJSON, err := encodeAutomationJSON(normalized.Actions)
	if err != nil {
		return AutomationRuleView{}, err
	}
	var savedID uint
	err = database.Transaction(func(tx *gorm.DB) error {
		if err := checkAutomationTargetsTx(tx, normalized.Actions); err != nil {
			return err
		}
		if normalized.ID == 0 {
			var maxPosition int
			if err := tx.Model(&models.AutomationRule{}).Select("COALESCE(MAX(position), 0)").Scan(&maxPosition).Error; err != nil {
				return err
			}
			rule := models.AutomationRule{
				Name: normalized.Name, MediaKind: normalized.MediaKind, Enabled: normalized.Enabled, Position: maxPosition + 1,
				ConditionsJSON: conditionsJSON, ActionsJSON: actionsJSON,
			}
			// Enabled 没有 gorm default，零值 false 也照常写进 INSERT。
			if err := tx.Create(&rule).Error; err != nil {
				return err
			}
			savedID = rule.ID
			return nil
		}
		var existing models.AutomationRule
		if err := tx.Where("id = ?", normalized.ID).Limit(1).Find(&existing).Error; err != nil {
			return err
		}
		if existing.ID == 0 {
			return ErrAutomationRuleNotFound
		}
		if existing.MediaKind != normalized.MediaKind {
			// 账本按媒体类型记，换类型等于另一条规则；让用户新建，旧账本不跟着失真。
			return invalidRule("media_kind", "已保存的规则不能更换媒体类型，请新建一条")
		}
		savedID = existing.ID
		return tx.Model(&models.AutomationRule{}).Where("id = ?", existing.ID).Updates(map[string]any{
			"name": normalized.Name, "enabled": normalized.Enabled, "conditions_json": conditionsJSON,
			"actions_json": actionsJSON, "disabled_reason": "", "updated_at": time.Now(),
		}).Error
	})
	if err != nil {
		return AutomationRuleView{}, err
	}
	return s.automationRuleView(savedID)
}

func (s *AutomationRuleService) automationRuleView(id uint) (AutomationRuleView, error) {
	views, err := s.ListAutomationRules()
	if err != nil {
		return AutomationRuleView{}, err
	}
	for _, view := range views {
		if view.ID == id {
			return view, nil
		}
	}
	return AutomationRuleView{}, ErrAutomationRuleNotFound
}

// DeleteAutomationRule 删除规则与它的账本（D-RA-03）；它加过的标签与作品集关系不撤。
// 账本外键带级联，这里仍在同一事务里显式删账本，不依赖连接上是否开着外键检查。
func (s *AutomationRuleService) DeleteAutomationRule(id uint) error {
	return database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("rule_id = ?", id).Delete(&models.AutomationRuleWrite{}).Error; err != nil {
			return err
		}
		result := tx.Where("id = ?", id).Delete(&models.AutomationRule{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrAutomationRuleNotFound
		}
		return nil
	})
}

// ReorderAutomationRules 按给定顺序重排全部规则（position 从 1 起）。ids 必须恰好是现有规则的全集。
func (s *AutomationRuleService) ReorderAutomationRules(ids []uint) error {
	return database.Transaction(func(tx *gorm.DB) error {
		var existing []uint
		if err := tx.Model(&models.AutomationRule{}).Pluck("id", &existing).Error; err != nil {
			return err
		}
		if !sameUniqueIDSet(existing, ids) {
			return invalidRule("ids", "规则列表已变化，请刷新后再排序")
		}
		for index, id := range ids {
			if err := tx.Model(&models.AutomationRule{}).Where("id = ?", id).Update("position", index+1).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// SetAutomationRuleEnabled 启用或停用规则。启用前按保存时的口径重新校验（动作被删光、目标失效、
// 条件坏了的规则不能直接启用，要先编辑）；启停都清掉停用原因。
func (s *AutomationRuleService) SetAutomationRuleEnabled(id uint, enabled bool) (AutomationRuleView, error) {
	err := database.Transaction(func(tx *gorm.DB) error {
		var rule models.AutomationRule
		if err := tx.Where("id = ?", id).Limit(1).Find(&rule).Error; err != nil {
			return err
		}
		if rule.ID == 0 {
			return ErrAutomationRuleNotFound
		}
		if enabled {
			conditions, actions, err := decodeAutomationRule(rule)
			if err != nil {
				return invalidRule("conditions", "规则内容已损坏，请编辑后重新保存")
			}
			input := AutomationRuleInput{Name: rule.Name, MediaKind: rule.MediaKind, Conditions: conditions, Actions: actions}
			normalized, err := normalizeAutomationRuleInput(input)
			if err != nil {
				return err
			}
			if err := checkAutomationTargetsTx(tx, normalized.Actions); err != nil {
				return err
			}
		}
		return tx.Model(&models.AutomationRule{}).Where("id = ?", id).Updates(map[string]any{
			"enabled": enabled, "disabled_reason": "", "updated_at": time.Now(),
		}).Error
	})
	if err != nil {
		return AutomationRuleView{}, err
	}
	return s.automationRuleView(id)
}
