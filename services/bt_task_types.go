package services

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"video-master/services/btclient"
)

var ErrBTTaskBusy = errors.New("bt_task_busy: 下载任务正在处理中，请稍后重试")

var ErrBTTask = errors.New("bt_task_unavailable: 下载任务不可用")
var ErrBTSelection = errors.New("selection_invalid: 请重新选择有效文件")
var ErrBTExternal = errors.New("existing_external_torrent: 客户端已有非本应用任务")
var ErrBTChanged = errors.New("external_changed: 客户端任务已变化，请核实")
var ErrBTPath = errors.New("path_unsafe: 下载保存路径不可用")
var ErrBTMaintenance = errors.New("maintenance: 下载协调已暂停")
var ErrBTMetadata = errors.New("metadata_preview_unsupported: 尚未验证客户端仅取元数据的能力")

// BTRemote is an explicit-hash client seam. Implementations must bound requests.
type BTRemote interface {
	CheckQBProfile(context.Context) error
	CheckQBMetadataProfile(context.Context) error
	QBTorrents(context.Context, string) ([]btclient.QBTorrent, error)
	QBProperties(context.Context, string) (btclient.QBProperties, error)
	QBFiles(context.Context, string) ([]btclient.QBFile, error)
	QBAddStopped(context.Context, btclient.QBAddOptions) (string, error)
	QBRun(context.Context, string, bool) error
	QBPriority(context.Context, string, []int, int) error
	QBDeleteMetadata(context.Context, string) error
	QBExport(context.Context, string) ([]byte, error)
}
type BTServiceDeps struct {
	Credentials     BTCredentialFactory
	CacheScope      func(context.Context, *gorm.DB) (string, error)
	EnsureNamespace func(string, string) (string, error)
	ValidateRoot    func(context.Context, string) error
	ImportFile      func(context.Context, string, string) error
	Changed         func(string, uint64)
	RemoteFactory   func(*btclient.Client) BTRemote
	SearchFactory   func(*btclient.Client) BTSearchSource
}
type BTIndexerView struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Enabled  bool   `json:"enabled"`
	Health   string `json:"health"`
}
type BTSubmitRequest struct {
	DraftID          string   `json:"draft_id"`
	ExpectedRevision uint64   `json:"expected_revision"`
	SelectedFileIDs  []string `json:"selected_file_ids"`
}
type BTControlRequest struct {
	TaskID           string `json:"task_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	Action           string `json:"action"`
	Confirmed        bool   `json:"confirmed"`
}
type BTFileView struct {
	Duplicate   *BTDuplicateView `json:"duplicate"`
	ID          string           `json:"id"`
	Path        string           `json:"path"`
	Size        string           `json:"size"`
	Selected    bool             `json:"selected"`
	Selectable  bool             `json:"selectable"`
	Padding     bool             `json:"padding"`
	Kind        string           `json:"kind"`
	Progress    float64          `json:"progress"`
	ImportState string           `json:"import_state"`
	VideoID     *uint            `json:"video_id"`
	ErrorCode   string           `json:"error_code"`
}
type BTTaskView struct {
	ID                 string       `json:"id"`
	Revision           uint64       `json:"revision"`
	Title              string       `json:"title"`
	IndexerID          int          `json:"indexer_id"`
	IndexerName        string       `json:"indexer_name"`
	Stage              string       `json:"stage"`
	RemoteState        string       `json:"remote_state"`
	SavePath           string       `json:"save_path"`
	ErrorCode          string       `json:"error_code"`
	PendingAction      string       `json:"pending_action"`
	UnresolvedWrite    bool         `json:"unresolved_write"`
	ReadOnlyReason     string       `json:"read_only_reason,omitempty"`
	Files              []BTFileView `json:"files"`
	SelectedBytes      string       `json:"selected_bytes"`
	TotalBytes         string       `json:"total_bytes"`
	Progress           float64      `json:"progress"`
	DownloadComplete   bool         `json:"download_complete"`
	SelectedVideoCount int          `json:"selected_video_count"`
	ImportedCount      int          `json:"imported_count"`
	FailedCount        int          `json:"failed_count"`
	ExpiresAt          *time.Time   `json:"expires_at" ts_type:"string"`
	Actions            []string     `json:"actions"`
}
