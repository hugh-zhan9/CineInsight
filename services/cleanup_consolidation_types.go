package services

import "video-master/models"

const cleanupConsolidationSchemaVersion = 1

// CleanupConsolidationGroup 明确授权本组保留项集中到目标；SelectedIDs 仅是后续
// 独立清理的意向，迁移执行器不能据此删除文件。
type CleanupConsolidationGroup struct {
	Kind         string `json:"kind"`
	MemberIDs    []uint `json:"member_ids"`
	KeeperID     uint   `json:"keeper_id"`
	KeeperPinned bool   `json:"keeper_pinned"`
	SelectedIDs  []uint `json:"selected_ids"`
}

// CleanupConsolidationProtection 覆盖当前完整分析，包含未纳入整理组的保留及
// 本组不删决定。低清/极短单项的 KeeperID 必须为零。
type CleanupConsolidationProtection struct {
	Kind         string `json:"kind"`
	MemberIDs    []uint `json:"member_ids"`
	KeeperID     uint   `json:"keeper_id"`
	KeeperPinned bool   `json:"keeper_pinned"`
	Skipped      bool   `json:"skipped"`
}

// CleanupConsolidationRequest 的移动范围和删除意向彼此独立；空 Destination 请求建议目录。
type CleanupConsolidationRequest struct {
	Destination string                           `json:"destination"`
	Groups      []CleanupConsolidationGroup      `json:"groups"`
	Protections []CleanupConsolidationProtection `json:"protections"`
}

// CleanupConsolidationPreview 是只读、不可由客户端改写的确认结果。Errors 非空时
// PreviewID 为空，不能启动；成功令牌只指向服务保存的最新清单。
type CleanupConsolidationPreview struct {
	PreviewID        string                      `json:"preview_id"`
	AnalysisVersion  uint64                      `json:"analysis_version"`
	Destination      string                      `json:"destination"`
	DestinationInfo  FileMigrationDirectory      `json:"destination_info"`
	InScanRoots      bool                        `json:"in_scan_roots"`
	Groups           []CleanupConsolidationGroup `json:"groups"`
	Items            []FileMigrationItem         `json:"items"`
	MoveBytes        int64                       `json:"move_bytes"`
	CrossVolumeBytes int64                       `json:"cross_volume_bytes"`
	CopyBytes        int64                       `json:"copy_bytes"`
	AvailableBytes   uint64                      `json:"available_bytes"`
	Warnings         []string                    `json:"warnings"`
	Errors           []string                    `json:"errors"`
}

// 私有持久合同。整个确认清单（含完整保护）在任何媒体变更前写进 PlanJSON；
// 文件执行方只消费 Preview.Items，不接触 SelectedIDs。
type cleanupConsolidationPlan struct {
	SchemaVersion int                               `json:"schema_version"`
	Preview       CleanupConsolidationPreview       `json:"preview"`
	Protections   []CleanupConsolidationProtection  `json:"protections"`
	Sources       []cleanupConsolidationVideoSource `json:"sources"`
	Analysis      *CleanupAnalysis                  `json:"analysis"`
}

type cleanupConsolidationVideoSource struct {
	Video models.Video        `json:"video"`
	File  FileMigrationSource `json:"file"`
}

// JournalJSON 与 PlanJSON 分开版本化：计划不可改，日志随已发生的文件阶段
// 用任务 Version 条件更新。执行器必须先记录意图，再执行相应磁盘变更。
type cleanupConsolidationJournal struct {
	SchemaVersion int                               `json:"schema_version"`
	Items         []cleanupConsolidationItemJournal `json:"items"`
}

// 每条明细独立版本化，不在热任务行中保存整批日志。
type cleanupConsolidationStoredItem struct {
	SchemaVersion int                             `json:"schema_version"`
	Item          cleanupConsolidationItemJournal `json:"item"`
}

type cleanupConsolidationItemJournal struct {
	VideoID uint                              `json:"video_id"`
	Phase   string                            `json:"phase"`
	Files   []cleanupConsolidationFileJournal `json:"files"`
	Error   string                            `json:"error"`
}

type cleanupConsolidationFileJournal struct {
	SourcePath        string              `json:"source_path"`
	Destination       string              `json:"destination"`
	TemporaryPath     string              `json:"temporary_path"`
	StagedSourcePath  string              `json:"staged_source_path"`
	SourceIdentity    string              `json:"source_identity"`
	TemporaryIdentity string              `json:"temporary_identity"`
	PublishedIdentity string              `json:"published_identity"`
	SHA256            string              `json:"sha256"`
	Copied            bool                `json:"copied"`
	TargetSnapshot    FileMigrationSource `json:"target_snapshot"`
	Phase             string              `json:"phase"`
}

// CleanupConsolidationStatus 把持久任务及只读确认路径投影给页面和任务中心。
// Plan/Journal 的原始 JSON 不允许通过 API 改写。
type CleanupConsolidationStatus struct {
	models.CleanupConsolidationTask
	Preview CleanupConsolidationPreview      `json:"preview"`
	Items   []CleanupConsolidationItemStatus `json:"items"`
}

// CleanupConsolidationItemStatus 保留成功与未完成项的确切位置。
type CleanupConsolidationItemStatus struct {
	VideoID       uint     `json:"video_id"`
	Source        string   `json:"source"`
	Destination   string   `json:"destination"`
	Phase         string   `json:"phase"`
	Error         string   `json:"error"`
	RetainedPaths []string `json:"retained_paths"`
}

// CleanupConsolidationReview 只提供本轮组的独立清理审阅，不触发合并或删除。
type CleanupConsolidationReview struct {
	TaskID    uint                        `json:"task_id"`
	Groups    []CleanupConsolidationGroup `json:"groups"`
	Analysis  *CleanupAnalysis            `json:"analysis"`
	LockedIDs []uint                      `json:"locked_ids"`
}

// CleanupConsolidationSummary 用于高频进度事件和历史列表，不携带完整文件清单。
// JSON 隐藏的任务内部字段也在投影时清空，避免保留大份计划字符串。
type CleanupConsolidationSummary struct {
	models.CleanupConsolidationTask
}
