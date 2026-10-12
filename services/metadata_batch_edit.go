package services

import (
	"context"
	"errors"
	"sync"
	"time"
)

// 元数据批量编辑与撤销（D-MC-04 / D-MC-05，详细设计 §2.4）。
// 本文件是请求 / 结果类型、错误码与「同一时刻只跑一个」的运行登记；
// 校验与目标解析在 metadata_batch_edit_plan.go，逐批写入在 metadata_batch_edit_apply.go，
// 撤销在 metadata_batch_edit_undo.go。

// 批量编辑的错误码（消息即错误码，前端按码分支）。
var (
	ErrInvalidBatchEdit     = errors.New("invalid_batch_edit")
	ErrBatchEditBusy        = errors.New("batch_edit_busy")
	ErrBatchAlreadyUndone   = errors.New("batch_already_undone")
	ErrMetadataBatchMissing = errors.New("batch_edit_not_found")
)

// 媒体类型、字段与动作（§2.4 的操作表）。
const (
	BatchEditKindVideo = "video"
	BatchEditKindImage = "image"

	BatchEditFieldYear       = "year"
	BatchEditFieldRating     = "rating"
	BatchEditFieldTag        = "tag"
	BatchEditFieldPerson     = "person"
	BatchEditFieldCollection = "collection"

	BatchEditActionSet    = "set"
	BatchEditActionClear  = "clear"
	BatchEditActionAdd    = "add"
	BatchEditActionRemove = "remove"
)

// metadataBatchChunk 是每个写事务处理的目标条数（SQLite 单写者，公共约定「每批最多 500 条」）。
// 撤销按明细行分批，也用这个数。
const metadataBatchChunk = 500

// metadataBatchKeep 是保留的批量编辑记录数（D-MC-05「最近 20 次可以撤销」）。
const metadataBatchKeep = 20

// MetadataBatchEditTarget 是作用范围，二选一：所选条目（IDs），或当前筛选（视频用 Filter，图片用 ImageFilter）。
// 筛选在执行时由后端重新解析成 ID（D-MC-04）。
type MetadataBatchEditTarget struct {
	IDs         []uint         `json:"ids"`
	Filter      *LibraryFilter `json:"filter"`
	ImageFilter *ImageFilter   `json:"image_filter"`
}

// MetadataBatchOp 是一个明确的操作。set 用 Value（年份或评分），add / remove 用 IDs（标签、人物、作品集）。
type MetadataBatchOp struct {
	Field  string   `json:"field"`
	Action string   `json:"action"`
	Value  *float64 `json:"value"`
	IDs    []uint   `json:"ids"`
}

type MetadataBatchEditRequest struct {
	Kind   string                  `json:"kind"`
	Target MetadataBatchEditTarget `json:"target"`
	Ops    []MetadataBatchOp       `json:"ops"`
}

// MetadataBatchOpCount 是一个操作「实际会改变 / 实际改变了」的条目数。
// 同一字段同一动作的多个操作在校验时合并为一个，结果按合并后的顺序给出。
type MetadataBatchOpCount struct {
	Field   string   `json:"field"`
	Action  string   `json:"action"`
	Value   *float64 `json:"value"`
	IDs     []uint   `json:"ids"`
	Names   []string `json:"names"`
	Changes int      `json:"changes"`
}

// MetadataBatchEditPreview 是执行前的预览：目标条目数与每个操作实际会改变的条目数。
type MetadataBatchEditPreview struct {
	Kind         string                 `json:"kind"`
	TargetCount  int                    `json:"target_count"`
	ChangedCount int                    `json:"changed_count"`
	Ops          []MetadataBatchOpCount `json:"ops"`
}

// MetadataBatchEditResult 是执行结果。BatchID 为 0 表示没有任何条目真正改变（不留记录）。
// 被取消时已提交的批次保留，Processed 是已处理完的目标条数。
type MetadataBatchEditResult struct {
	BatchID      uint                   `json:"batch_id"`
	Kind         string                 `json:"kind"`
	TargetCount  int                    `json:"target_count"`
	Processed    int                    `json:"processed"`
	ChangedCount int                    `json:"changed_count"`
	Ops          []MetadataBatchOpCount `json:"ops"`
	Cancelled    bool                   `json:"cancelled"`
}

// MetadataBatchUndoResult 是撤销结果：Restored 条（条目，字段）已恢复，ChangedSince 条之后又被改过而跳过。
type MetadataBatchUndoResult struct {
	BatchID      uint `json:"batch_id"`
	Restored     int  `json:"restored"`
	ChangedSince int  `json:"changed_since"`
	Cancelled    bool `json:"cancelled"`
}

// MetadataBatchEditSummary 是存进 summary_json 的摘要：范围、目标数与各操作（含名字，免得之后改名或删除后看不懂）。
type MetadataBatchEditSummary struct {
	Scope        string                 `json:"scope"`
	TargetCount  int                    `json:"target_count"`
	ChangedCount int                    `json:"changed_count"`
	Ops          []MetadataBatchOpCount `json:"ops"`
	Cancelled    bool                   `json:"cancelled"`
}

// MetadataBatchEditListItem 是「最近的批量编辑」一行。
type MetadataBatchEditListItem struct {
	ID        uint                     `json:"id"`
	Kind      string                   `json:"kind"`
	CreatedAt time.Time                `json:"created_at" ts_type:"string"`
	UndoneAt  *time.Time               `json:"undone_at" ts_type:"string"`
	ItemCount int                      `json:"item_count"`
	Summary   MetadataBatchEditSummary `json:"summary"`
	Undoable  bool                     `json:"undoable"`
}

// MetadataBatchEditStatus 是任务中心用的运行状态与上一轮结果。
type MetadataBatchEditStatus struct {
	Running    bool       `json:"running"`
	Done       int        `json:"done"`
	Total      int        `json:"total"`
	FinishedAt *time.Time `json:"finished_at"`
	LastError  string     `json:"last_error"`
}

// MetadataBatchEditService 是元数据批量编辑与撤销（D-MC-04 / D-MC-05）。
// 执行与撤销共用一个运行位：同一时刻只允许一个，第二个请求返回 batch_edit_busy。
type MetadataBatchEditService struct {
	registry *BackgroundTaskRegistry

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	status MetadataBatchEditStatus
	// afterChunk 只给测试用：每提交一批之后调用（用来在批与批之间取消）。
	afterChunk func(processed int)
}

func NewMetadataBatchEditService(dataDir string) *MetadataBatchEditService {
	return &MetadataBatchEditService{}
}

func (s *MetadataBatchEditService) SetBackgroundTaskRegistry(registry *BackgroundTaskRegistry) {
	if s != nil {
		s.registry = registry
	}
}

// begin 占住运行位并登记任务键；已有一个在跑时返回 batch_edit_busy。
// 返回的 ctx 会被 StopAndWait 取消，end 必须调用（释放运行位、记下上一轮结果）。
func (s *MetadataBatchEditService) begin(parent context.Context, total int) (context.Context, func(error), error) {
	s.mu.Lock()
	if s.done != nil {
		s.mu.Unlock()
		return nil, nil, ErrBatchEditBusy
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	s.cancel, s.done = cancel, done
	s.status = MetadataBatchEditStatus{Running: true, Total: total}
	s.mu.Unlock()
	s.registry.Begin(BackgroundTaskMetadataBatchEdit)
	end := func(err error) {
		s.registry.End(BackgroundTaskMetadataBatchEdit)
		now := time.Now()
		s.mu.Lock()
		s.status.Running = false
		s.status.FinishedAt = &now
		s.status.LastError = ""
		if err != nil {
			s.status.LastError = metadataBatchErrorText(err)
		}
		s.cancel, s.done = nil, nil
		s.mu.Unlock()
		cancel()
		close(done)
	}
	return ctx, end, nil
}

// progress 记下已处理的目标条数（任务中心进度）。
func (s *MetadataBatchEditService) progress(done int) {
	s.mu.Lock()
	s.status.Done = done
	s.mu.Unlock()
	if s.afterChunk != nil {
		s.afterChunk(done)
	}
}

// Status 是任务中心适配读取的运行状态与上一轮结果。
func (s *MetadataBatchEditService) Status() MetadataBatchEditStatus {
	if s == nil {
		return MetadataBatchEditStatus{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// StopAndWait 取消进行中的执行或撤销并等它收尾（进入恢复维护与退出时调用）。
// 取消在批与批之间生效：已提交的批次保留。
func (s *MetadataBatchEditService) StopAndWait() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// metadataBatchErrorText 是上一轮失败在任务中心的文案：不带路径与底层错误原文（G-3）。
func metadataBatchErrorText(err error) string {
	if errors.Is(err, ErrInvalidBatchEdit) {
		return "批量编辑的筛选条件无效"
	}
	return "批量编辑写入失败"
}
