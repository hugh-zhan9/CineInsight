package services

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"video-master/database"
	"video-master/models"
)

// BTTaskSummary avoids expanding manifests for the frequently refreshed task center.
type BTTaskSummary struct {
	ID        string
	Title     string
	Status    string
	ErrorCode string
	Message   string
	UpdatedAt time.Time
}
type btSummaryCounts struct {
	TaskID     string
	Total      int
	Incomplete int
	Failed     int
	Pending    int
	Awaiting   int
}

func btSummaryVideoCondition() (string, []any) {
	terms := []string{}
	args := []any{}
	for _, ext := range strings.Split(defaultVideoExtensions, ",") {
		terms = append(terms, "LOWER(relative_path) LIKE ?")
		args = append(args, "%"+ext)
	}
	return strings.Join(terms, " OR "), args
}
func (s *BTService) RecentSummaries(ctx context.Context) ([]BTTaskSummary, error) {
	out := []BTTaskSummary{}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		videoCondition, videoArgs := btSummaryVideoCondition()
		// Completed seeds are history, not active work. Filter before applying the bound
		// so arbitrarily many old completed torrents cannot hide a pending download.
		selected := func() *gorm.DB {
			return db.Model(&models.BTFile{}).Select("1").Where("bt_files.task_id = bt_tasks.id AND selected = ? AND padding = ?", true, false)
		}
		pending := selected().Where("progress < 1 OR error_code <> '' OR publication_state IN ('pending_new','pending_restore') OR (("+videoCondition+") AND import_state <> 'imported')", videoArgs...)
		var tasks []models.BTTask
		if err := db.Where("stage NOT IN ?", []string{"draft", "cancelled", "released", "abandoned"}).Where("stage <> 'active' OR unresolved_write = ? OR remote_state IN ('error','missingFiles','moving') OR remote_state LIKE 'checking%' OR EXISTS (?) OR NOT EXISTS (?)", true, pending, selected()).Order("updated_at DESC,id DESC").Limit(100).Find(&tasks).Error; err != nil {
			return ErrBTTask
		}
		var recent []models.BTTask
		if err := db.Order("updated_at DESC,id DESC").Limit(20).Find(&recent).Error; err != nil {
			return ErrBTTask
		}
		seen := map[string]bool{}
		for _, t := range tasks {
			seen[t.ID] = true
		}
		for _, t := range recent {
			if !seen[t.ID] {
				tasks = append(tasks, t)
				seen[t.ID] = true
			}
		}
		if len(tasks) == 0 {
			return nil
		}
		ids := make([]string, 0, len(tasks))
		for _, t := range tasks {
			ids = append(ids, t.ID)
		}
		var counts []btSummaryCounts
		err := db.Model(&models.BTFile{}).Select("task_id,COUNT(*) AS total,SUM(CASE WHEN progress < 1 THEN 1 ELSE 0 END) AS incomplete,SUM(CASE WHEN error_code <> '' THEN 1 ELSE 0 END) AS failed,SUM(CASE WHEN publication_state IN ('pending_new','pending_restore') THEN 1 ELSE 0 END) AS pending,SUM(CASE WHEN ("+videoCondition+") AND import_state <> 'imported' THEN 1 ELSE 0 END) AS awaiting", videoArgs...).Where("task_id IN ? AND selected = ? AND padding = ?", ids, true, false).Group("task_id").Scan(&counts).Error
		if err != nil {
			return ErrBTTask
		}
		byID := map[string]btSummaryCounts{}
		for _, c := range counts {
			byID[c.TaskID] = c
		}
		for _, t := range tasks {
			out = append(out, classifyBTTaskSummary(t, byID[t.ID]))
		}
		return nil
	})
	return out, err
}
func classifyBTTaskSummary(t models.BTTask, c btSummaryCounts) BTTaskSummary {
	status, message := "queued", ""
	switch {
	case t.Stage == "cancelled" || t.Stage == "abandoned" || t.Stage == "released":
		status = t.Stage
	case t.UnresolvedWrite || t.Stage == "uncertain" || t.Stage == "conflict":
		status = "needs_confirmation"
	case t.Stage == "missing" || t.RemoteState == "error" || t.RemoteState == "missingFiles":
		status = "failed"
		message = "客户端任务或文件不可用，请在下载详情中核实"
	case strings.HasPrefix(t.RemoteState, "checking"):
		status = "running"
		message = "客户端正在校验文件"
	case t.RemoteState == "moving":
		status = "running"
		message = "客户端正在移动文件"
	case c.Failed > 0:
		status = "failed"
		message = "文件入库失败，请在下载详情中查看"
	case t.Stage == "active" && c.Total > 0 && c.Incomplete == 0:
		status = "done"
		if c.Pending > 0 || c.Awaiting > 0 {
			status = "importing"
		}
	case t.Stage == "active" && strings.HasPrefix(t.RemoteState, "stopped"):
		status = "stopped"
	case t.Stage == "active":
		status = "running"
	}
	return BTTaskSummary{ID: t.ID, Title: t.Title, Status: status, ErrorCode: t.ErrorCode, Message: message, UpdatedAt: t.UpdatedAt}
}
