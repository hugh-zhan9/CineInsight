package services

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// 规则校验（需求设计文档 §3.2）：条件非空、条件类型与媒体类型匹配、正则 ≤200 字符且能编译、
// 至少一个动作、加入作品集只用于视频。目标是否存在在 store 里另查（要读库）。

// automationConditionKinds 是每种条件适用的媒体类型。
var automationConditionKinds = map[string]map[string]bool{
	automationCondUnderDirectory: {AutomationMediaVideo: true, AutomationMediaImage: true},
	automationCondNameContains:   {AutomationMediaVideo: true, AutomationMediaImage: true},
	automationCondNameGlob:       {AutomationMediaVideo: true, AutomationMediaImage: true},
	automationCondNameRegex:      {AutomationMediaVideo: true, AutomationMediaImage: true},
	automationCondExtensionIn:    {AutomationMediaVideo: true, AutomationMediaImage: true},
	automationCondSizeBetween:    {AutomationMediaVideo: true, AutomationMediaImage: true},
	automationCondDuration:       {AutomationMediaVideo: true},
	automationCondHeight:         {AutomationMediaVideo: true},
	automationCondVideoCodecIn:   {AutomationMediaVideo: true},
	automationCondHasGPS:         {AutomationMediaImage: true},
}

// normalizeAutomationRuleInput 校验并规整入参（去空白、扩展名去点转小写、目录 Clean）。
func normalizeAutomationRuleInput(input AutomationRuleInput) (AutomationRuleInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return input, invalidRule("name", "请填写规则名称")
	}
	if utf8.RuneCountInString(input.Name) > automationRuleNameMaxRunes {
		return input, invalidRule("name", fmt.Sprintf("规则名称最多 %d 个字", automationRuleNameMaxRunes))
	}
	if input.MediaKind != AutomationMediaVideo && input.MediaKind != AutomationMediaImage {
		return input, invalidRule("media_kind", "请选择视频或图片")
	}
	if len(input.Conditions) == 0 {
		return input, invalidRule("conditions", "至少需要一个条件")
	}
	conditions := make([]AutomationRuleCondition, 0, len(input.Conditions))
	for index, condition := range input.Conditions {
		normalized, err := normalizeAutomationCondition(input.MediaKind, condition, fmt.Sprintf("conditions[%d]", index))
		if err != nil {
			return input, err
		}
		conditions = append(conditions, normalized)
	}
	input.Conditions = conditions
	actions, err := normalizeAutomationActions(input.MediaKind, input.Actions)
	if err != nil {
		return input, err
	}
	input.Actions = actions
	return input, nil
}

func normalizeAutomationCondition(kind string, c AutomationRuleCondition, field string) (AutomationRuleCondition, error) {
	kinds, known := automationConditionKinds[c.Type]
	if !known {
		return c, invalidRule(field+".type", "不认识的条件类型")
	}
	if !kinds[kind] {
		return c, invalidRule(field+".type", "这个条件不适用于"+automationMediaLabel(kind))
	}
	out := AutomationRuleCondition{Type: c.Type}
	switch c.Type {
	case automationCondUnderDirectory:
		dir := strings.TrimSpace(c.Path)
		if dir == "" || !filepath.IsAbs(dir) {
			return c, invalidRule(field+".path", "请选择一个文件夹")
		}
		out.Path = filepath.Clean(dir)
	case automationCondNameContains:
		if out.Text = strings.TrimSpace(c.Text); out.Text == "" {
			return c, invalidRule(field+".text", "请填写要包含的文字")
		}
	case automationCondNameGlob:
		out.Pattern = strings.TrimSpace(c.Pattern)
		if out.Pattern == "" {
			return c, invalidRule(field+".pattern", "请填写通配符")
		}
		if _, err := path.Match(out.Pattern, ""); err != nil {
			return c, invalidRule(field+".pattern", "通配符写法有误")
		}
	case automationCondNameRegex:
		out.Pattern = c.Pattern
		if strings.TrimSpace(out.Pattern) == "" {
			return c, invalidRule(field+".pattern", "请填写正则表达式")
		}
		if utf8.RuneCountInString(out.Pattern) > automationRegexMaxRunes {
			return c, invalidRule(field+".pattern", fmt.Sprintf("正则表达式最多 %d 个字符", automationRegexMaxRunes))
		}
		if _, err := regexp.Compile(out.Pattern); err != nil {
			return c, invalidRule(field+".pattern", "正则表达式写法有误")
		}
	case automationCondExtensionIn:
		out.Exts = normalizeAutomationList(c.Exts, true)
		if len(out.Exts) == 0 {
			return c, invalidRule(field+".exts", "请至少填写一个扩展名")
		}
	case automationCondVideoCodecIn:
		out.Codecs = normalizeAutomationList(c.Codecs, false)
		if len(out.Codecs) == 0 {
			return c, invalidRule(field+".codecs", "请至少填写一个编码")
		}
	case automationCondSizeBetween, automationCondDuration, automationCondHeight:
		if c.Min == nil && c.Max == nil {
			return c, invalidRule(field+".min", "请至少填写下限或上限")
		}
		if (c.Min != nil && *c.Min < 0) || (c.Max != nil && *c.Max < 0) {
			return c, invalidRule(field+".min", "不能是负数")
		}
		if c.Min != nil && c.Max != nil && *c.Min > *c.Max {
			return c, invalidRule(field+".max", "上限不能小于下限")
		}
		out.Min, out.Max = c.Min, c.Max
	case automationCondHasGPS:
		if c.Value == nil {
			return c, invalidRule(field+".value", "请选择有或没有地理位置")
		}
		out.Value = c.Value
	}
	return out, nil
}

// normalizeAutomationList 去空白、转小写、去重；stripDot 时去掉扩展名开头的点。
func normalizeAutomationList(values []string, stripDot bool) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if stripDot {
			value = strings.TrimPrefix(value, ".")
		}
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// normalizeAutomationActions 校验动作：至少一项、类型已知、目标 id 非零、作品集只用于视频；完全相同的动作去重。
func normalizeAutomationActions(kind string, actions []AutomationRuleAction) ([]AutomationRuleAction, error) {
	if len(actions) == 0 {
		return nil, invalidRule("actions", "至少需要一个动作")
	}
	out := make([]AutomationRuleAction, 0, len(actions))
	seen := make(map[string]bool, len(actions))
	for index, action := range actions {
		field := fmt.Sprintf("actions[%d]", index)
		switch action.Type {
		case automationActionAddTag:
			if action.TagID == 0 {
				return nil, invalidRule(field+".tag_id", "请选择标签")
			}
			action = AutomationRuleAction{Type: action.Type, TagID: action.TagID}
		case automationActionAddToCollection:
			if kind != AutomationMediaVideo {
				return nil, invalidRule(field+".type", "图片不能加入作品集")
			}
			if action.CollectionID == 0 {
				return nil, invalidRule(field+".collection_id", "请选择作品集")
			}
			action = AutomationRuleAction{Type: action.Type, CollectionID: action.CollectionID}
		default:
			return nil, invalidRule(field+".type", "不认识的动作类型")
		}
		key := fmt.Sprintf("%s:%d", action.writeKind(), action.targetID())
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, action)
	}
	return out, nil
}

func automationMediaLabel(kind string) string {
	if kind == AutomationMediaImage {
		return "图片"
	}
	return "视频"
}
