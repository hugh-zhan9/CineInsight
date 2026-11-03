package services

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"time"
	"video-master/database"
	"video-master/models"
)

type EpisodeCandidatesRequest struct {
	OriginToken  string   `json:"origin_token"`
	ResultTokens []string `json:"result_tokens"`
}
type EpisodeCandidateHint struct {
	ResultToken    string `json:"result_token"`
	Hint           string `json:"hint"`
	LibraryStatus  string `json:"library_status"`
	DownloadStatus string `json:"download_status"`
}
type EpisodeCandidatePage struct {
	Items []EpisodeCandidateHint `json:"items"`
}

// DescribeEpisodeCandidates reads cached titles and local facts; it does not query a site.
// An episode number is only a title hint, never proof that a resource is the same work.
func (s *BTService) DescribeEpisodeCandidates(ctx context.Context, collections *CollectionService, request EpisodeCandidatesRequest) (*EpisodeCandidatePage, error) {
	if collections == nil || len(request.ResultTokens) == 0 || len(request.ResultTokens) > 200 {
		return nil, errors.New("invalid_input: 请选择1至200项资源")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ctx, leave, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	collections.episodeMu.Lock()
	defer collections.episodeMu.Unlock()
	collections.pruneEpisodePreviewsLocked()
	source := collections.episodeSearches[request.OriginToken]
	if source == nil {
		return nil, ErrEpisodeContextChanged
	}
	resources := make([]BTResource, 0, len(request.ResultTokens))
	seen := map[string]bool{}
	for _, token := range request.ResultTokens {
		if seen[token] || token == "" {
			return nil, errors.New("invalid_input: 资源不能重复或为空")
		}
		seen[token] = true
		resource, err := s.search.Describe(token)
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}
	result := &EpisodeCandidatePage{Items: []EpisodeCandidateHint{}}
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			report, err := collectionEpisodeReportFrom(ctx, tx, source.view.CollectionID, 0, 200)
			if err != nil {
				return err
			}
			for _, resource := range resources {
				item := EpisodeCandidateHint{ResultToken: resource.Token, Hint: "季集待核对：标题不足以确认单集"}
				parsed := parseCollectionEpisode(models.Video{Name: resource.Title + ".mkv"})
				if parsed.OK && parsed.Episode != nil && parsed.Season != nil && *parsed.Season <= 999 && *parsed.Episode <= 1000000 {
					item.Hint = fmt.Sprintf("标题提示：可能包含第%d季第%d集，作品归属待核对", *parsed.Season, *parsed.Episode)
					for _, member := range report.allMembers {
						if !member.Excluded && member.Season != nil && member.Episode != nil && *member.Season == *parsed.Season && *member.Episode == *parsed.Episode {
							if member.Offline {
								item.LibraryStatus = "当前作品集此集已在库（含离线文件）"
							} else {
								item.LibraryStatus = "当前作品集此集已在库"
							}
							break
						}
					}
					var downloading int64
					err := tx.Model(&models.EpisodeAcquisitionOrigin{}).Joins("JOIN bt_tasks ON bt_tasks.id = episode_acquisition_origins.task_id").Joins("JOIN bt_connections ON bt_connections.id = bt_tasks.connection_id AND bt_connections.slot = 'local' AND bt_connections.enabled = ?", true).Joins("JOIN bt_files ON bt_files.task_id = episode_acquisition_origins.task_id AND bt_files.manifest_file_id = episode_acquisition_origins.file_id").Where("episode_acquisition_origins.collection_id = ? AND confirmed_mapping = ? AND mapped_season = ? AND mapped_episode = ?", source.view.CollectionID, true, *parsed.Season, *parsed.Episode).Where("bt_tasks.stage = ? AND bt_tasks.unresolved_write = ? AND bt_tasks.pending_action = ?", "active", false, "").Where("bt_files.selected = ? AND bt_files.progress < ?", true, 1).Count(&downloading).Error
					if err != nil {
						return err
					}
					if downloading > 0 {
						item.DownloadStatus = "当前作品集此集有受管理下载（按已确认文件映射）"
					}
				}
				result.Items = append(result.Items, item)
			}
			return nil
		})
	})
	return result, err
}
