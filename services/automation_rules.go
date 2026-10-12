package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// 本地规则自动化（docs/loopx/design/2026-10-12-data-care §3，D-RA-01..05）。
// 一条规则 = 媒体类型 + 一组同时满足的条件 + 一到多个动作（加标签 / 加入作品集），只加不撤：
// 账本 automation_rule_writes 记「哪条规则对哪条媒体的哪个目标处理过」，每个组合至多处理一次，
// 用户手动去掉的标签因此不会被加回。
//
// 文件分工：automation_rules.go 类型与校验、automation_rules_store.go 规则存取、
// automation_rules_eval.go 条件求值与取数、automation_rules_apply.go 账本与写入、
// automation_rules_queue.go 新入库队列、automation_rules_run.go 预览与手动运行、
// automation_rules_targets.go 标签/作品集删除与合并时的引用改写、AI 打标的排除口径。

const (
	AutomationMediaVideo = "video"
	AutomationMediaImage = "image"

	automationActionAddTag          = "add_tag"
	automationActionAddToCollection = "add_to_collection"
	// 账本 action 列的取值（D-RA-03）。
	automationWriteTag        = "tag"
	automationWriteCollection = "collection"

	automationDisabledTargetDeleted    = "target_deleted"
	automationDisabledInvalidCondition = "invalid_condition"

	automationRuleNameMaxRunes  = 100
	automationRegexMaxRunes     = 200
	automationBatchSize         = 500
	automationPreviewSampleSize = 50
)

// 条件类型（需求设计文档 §3.2 表格）。
const (
	automationCondUnderDirectory = "under_directory"
	automationCondNameContains   = "name_contains"
	automationCondNameGlob       = "name_glob"
	automationCondNameRegex      = "name_regex"
	automationCondExtensionIn    = "extension_in"
	automationCondSizeBetween    = "size_between"
	automationCondDuration       = "duration_between"
	automationCondHeight         = "height_between"
	automationCondVideoCodecIn   = "video_codec_in"
	automationCondHasGPS         = "has_gps"
)

var (
	ErrAutomationRuleInvalid  = errors.New("invalid_rule")
	ErrAutomationRuleNotFound = errors.New("rule_not_found")
	ErrAutomationRunBusy      = errors.New("automation_run_busy")
)

// AutomationRuleValidationError 是 invalid_rule 的字段说明：Error() 形如「invalid_rule: conditions[1].pattern: 正则写法有误」，
// 前端按前缀识别错误码、按字段把说明放到对应的行旁边。文案不含路径（G-3）。
type AutomationRuleValidationError struct {
	Field   string
	Message string
}

func (e *AutomationRuleValidationError) Error() string {
	return fmt.Sprintf("%s: %s: %s", ErrAutomationRuleInvalid.Error(), e.Field, e.Message)
}

func (e *AutomationRuleValidationError) Unwrap() error { return ErrAutomationRuleInvalid }

func invalidRule(field, message string) error {
	return &AutomationRuleValidationError{Field: field, Message: message}
}

// AutomationRuleCondition 是 conditions_json 的一项；按 type 只用其中几个参数。
// Min / Max 对 size_between 是字节，对 duration_between 是秒，对 height_between 是像素；任一端可省略（闭区间）。
type AutomationRuleCondition struct {
	Type    string   `json:"type"`
	Path    string   `json:"path,omitempty"`
	Text    string   `json:"text,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
	Exts    []string `json:"exts,omitempty"`
	Codecs  []string `json:"codecs,omitempty"`
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Value   *bool    `json:"value,omitempty"`
}

// AutomationRuleAction 是 actions_json 的一项：add_tag 用 TagID，add_to_collection 用 CollectionID。
type AutomationRuleAction struct {
	Type         string `json:"type"`
	TagID        uint   `json:"tag_id,omitempty"`
	CollectionID uint   `json:"collection_id,omitempty"`
}

// writeKind / targetID 把动作翻成账本的 (action, target_id)。
func (a AutomationRuleAction) writeKind() string {
	if a.Type == automationActionAddToCollection {
		return automationWriteCollection
	}
	return automationWriteTag
}

func (a AutomationRuleAction) targetID() uint {
	if a.Type == automationActionAddToCollection {
		return a.CollectionID
	}
	return a.TagID
}

// AutomationRuleInput 是 SaveAutomationRule 的入参：ID 为 0 时新建。
type AutomationRuleInput struct {
	ID         uint                      `json:"id"`
	Name       string                    `json:"name"`
	MediaKind  string                    `json:"media_kind"`
	Enabled    bool                      `json:"enabled"`
	Conditions []AutomationRuleCondition `json:"conditions"`
	Actions    []AutomationRuleAction    `json:"actions"`
}

// AutomationRuleActionView 在动作之外带上目标的当前名称，供列表摘要显示。
type AutomationRuleActionView struct {
	AutomationRuleAction
	TargetName string `json:"target_name"`
}

// AutomationRuleView 是规则列表的一项。
type AutomationRuleView struct {
	ID             uint                       `json:"id"`
	Name           string                     `json:"name"`
	MediaKind      string                     `json:"media_kind"`
	Enabled        bool                       `json:"enabled"`
	Position       int                        `json:"position"`
	Conditions     []AutomationRuleCondition  `json:"conditions"`
	Actions        []AutomationRuleActionView `json:"actions"`
	DisabledReason string                     `json:"disabled_reason"`
	LastRunAt      *time.Time                 `json:"last_run_at" ts_type:"string"`
	CreatedAt      time.Time                  `json:"created_at" ts_type:"string"`
	UpdatedAt      time.Time                  `json:"updated_at" ts_type:"string"`
}

// AutomationRuleService 是本地规则自动化（D-RA-*）：规则存取、新入库队列与手动运行。
type AutomationRuleService struct {
	registry *BackgroundTaskRegistry

	mu      sync.Mutex
	baseCtx context.Context
	emitter func(AutomationRuleRunStatus)
	worker  sync.WaitGroup

	// 新入库队列（结构同下载查重队列）：只记 id，一个工作协程逐条评估。
	queue        []automationQueueItem
	queueRunning bool
	queueStopped bool
	queueCtx     context.Context
	queueCancel  context.CancelFunc

	// 手动运行：同一时刻只允许一次。
	run       AutomationRuleRunStatus
	runCancel context.CancelFunc
	// afterRunPage 只在单测里设置：每页提交之后调用，用来在两页之间取消。
	afterRunPage func()
}

func NewAutomationRuleService(dataDir string) *AutomationRuleService {
	return &AutomationRuleService{run: AutomationRuleRunStatus{RuleIDs: []uint{}}}
}

func (s *AutomationRuleService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	if s != nil {
		s.mu.Lock()
		s.registry = registry
		s.mu.Unlock()
	}
}

// SetEventEmitter 接上手动运行进度的推送。
func (s *AutomationRuleService) SetEventEmitter(emitter func(AutomationRuleRunStatus)) {
	if s != nil {
		s.mu.Lock()
		s.emitter = emitter
		s.mu.Unlock()
	}
}
