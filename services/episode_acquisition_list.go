package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"time"
	"video-master/database"
	"video-master/models"
)

type EpisodeAcquisitionCursor struct {
	Signature string    `json:"signature"`
	CreatedAt time.Time `json:"created_at" ts_type:"string"`
	ID        string    `json:"id"`
}
type EpisodeAcquisitionRequest struct {
	CollectionID uint                      `json:"collection_id"`
	TaskID       string                    `json:"task_id"`
	Cursor       *EpisodeAcquisitionCursor `json:"cursor"`
	Limit        int                       `json:"limit"`
}
type EpisodeAcquisitionItem struct {
	OriginID          string `json:"origin_id"`
	Revision          int64  `json:"revision"`
	TaskID            string `json:"task_id"`
	FileID            string `json:"file_id"`
	CollectionID      uint   `json:"collection_id"`
	CollectionTitle   string `json:"collection_title"`
	Title             string `json:"title"`
	RequestedSeason   int    `json:"requested_season"`
	RequestedEpisodes []int  `json:"requested_episodes"`
	Season            *int   `json:"season"`
	Episode           *int   `json:"episode"`
	VideoID           *uint  `json:"video_id"`
	State             string `json:"state"`
	Reason            string `json:"reason"`
	CanAssociate      bool   `json:"can_associate"`
}
type EpisodeAcquisitionPage struct {
	Items      []EpisodeAcquisitionItem  `json:"items"`
	NextCursor *EpisodeAcquisitionCursor `json:"next_cursor"`
}

// ListEpisodeAcquisitions is independent of the existing BT progress response.
func (s *CollectionService) ListEpisodeAcquisitions(ctx context.Context, request EpisodeAcquisitionRequest) (*EpisodeAcquisitionPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if request.Limit == 0 {
		request.Limit = 50
	}
	if request.CollectionID > experienceMaxSafeInteger || request.CollectionID == 0 && request.TaskID == "" || request.TaskID != "" && !btTaskUUID(request.TaskID) || request.Limit < 1 || request.Limit > 200 {
		return nil, errors.New("invalid_input: 来源查询范围无效")
	}
	signature := fmt.Sprintf("%d|%s", request.CollectionID, request.TaskID)
	if request.Cursor != nil && (request.Cursor.Signature != signature || request.Cursor.CreatedAt.IsZero() || !btTaskUUID(request.Cursor.ID)) {
		return nil, errors.New("invalid_input: 来源列表游标已失效")
	}
	result := &EpisodeAcquisitionPage{Items: []EpisodeAcquisitionItem{}}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			query := tx.Model(&models.EpisodeAcquisitionOrigin{})
			if request.CollectionID != 0 {
				query = query.Where("collection_id = ?", request.CollectionID)
			}
			if request.TaskID != "" {
				var task models.BTTask
				if err := tx.Select("id").First(&task, "id = ?", request.TaskID).Error; err != nil {
					return err
				}
				query = query.Where("task_id = ?", request.TaskID)
			}
			if request.Cursor != nil {
				query = query.Where("created_at < ? OR (created_at = ? AND id < ?)", request.Cursor.CreatedAt, request.Cursor.CreatedAt, request.Cursor.ID)
			}
			var origins []models.EpisodeAcquisitionOrigin
			if err := query.Order("created_at DESC, id DESC").Limit(request.Limit + 1).Find(&origins).Error; err != nil {
				return err
			}
			if len(origins) > request.Limit {
				origins = origins[:request.Limit]
				last := origins[len(origins)-1]
				result.NextCursor = &EpisodeAcquisitionCursor{Signature: signature, CreatedAt: last.CreatedAt, ID: last.ID}
			}
			reports := map[uint]*CollectionEpisodeReport{}
			checked := map[uint]bool{}
			for _, origin := range origins {
				if !checked[origin.CollectionID] {
					var collection models.MediaCollection
					err := tx.First(&collection, origin.CollectionID).Error
					if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
						return err
					}
					if err == nil {
						report, err := collectionEpisodeReportFrom(ctx, tx, origin.CollectionID, 0, 200)
						if err != nil {
							return err
						}
						reports[origin.CollectionID] = report
					}
					checked[origin.CollectionID] = true
				}
				var file models.BTFile
				var task models.BTTask
				if err := tx.First(&file, "task_id = ? AND manifest_file_id = ?", origin.TaskID, origin.FileID).Error; err != nil {
					return err
				}
				if err := tx.First(&task, "id = ?", origin.TaskID).Error; err != nil {
					return err
				}
				item := EpisodeAcquisitionItem{OriginID: origin.ID, Revision: origin.Revision, TaskID: origin.TaskID, FileID: origin.FileID, CollectionID: origin.CollectionID, CollectionTitle: origin.CollectionTitle, Title: file.RelativePath, RequestedSeason: origin.RequestedSeason, RequestedEpisodes: []int{}, Season: origin.MappedSeason, Episode: origin.MappedEpisode, State: "selected"}
				if err := json.Unmarshal([]byte(origin.RequestedEpisodesJSON), &item.RequestedEpisodes); err != nil {
					return errors.New("来源记录不可读，请检查片库数据")
				}
				report := reports[origin.CollectionID]
				switch {
				case report == nil:
					item.State = "detached"
					item.Reason = "作品集已删除，下载与已入库文件保留"
				case episodeFileReady(file):
					item.State = "imported_unlinked"
					query, err := visibleVideoQuery(tx.Model(&models.Video{}))
					if err != nil {
						return err
					}
					var count int64
					if err := query.Where("videos.id = ?", *file.VideoID).Count(&count).Error; err != nil {
						return err
					}
					if count != 1 {
						item.Reason = "原片已不可用或不在当前范围"
						break
					}
					item.VideoID = file.VideoID
					item.CanAssociate = true
					if origin.ConfirmedMapping && origin.MappedSeason != nil && origin.MappedEpisode != nil {
						linked, conflict, _ := episodeAssociationState(report, *file.VideoID, *origin.MappedSeason, *origin.MappedEpisode)
						if linked {
							item.State = "linked"
							item.CanAssociate = false
						} else if conflict != "" {
							item.Reason = episodeConflictMessage(conflict)
						}
					}
				case file.ImportState == "failed" || file.ImportState == "blocked_user_delete":
					item.State = "import_failed"
					item.Reason = "文件尚未成功入库，请在下载详情核对"
				case task.Stage == "active":
					var connection models.BTConnection
					if err := tx.Select("id", "slot", "enabled").First(&connection, "id = ?", task.ConnectionID).Error; err != nil {
						return err
					}
					if connection.Slot != "local" || !connection.Enabled {
						item.Reason = "下载连接已停用或更换，请查看历史任务"
						break
					}
					item.State = "transferring"
					if file.ImportState == "imported" {
						item.Reason = "正在完成入库验证"
					}
				default:
					if task.Stage == "released" || task.Stage == "cancelled" || task.Stage == "abandoned" {
						item.Reason = "下载已取消或退出管理，文件尚未成功入库"
					} else if task.Stage == "uncertain" || task.Stage == "missing" || task.Stage == "conflict" {
						item.Reason = "下载状态待核实，请查看任务详情"
					}
				}
				result.Items = append(result.Items, item)
			}
			return nil
		})
	})
	return result, err
}
