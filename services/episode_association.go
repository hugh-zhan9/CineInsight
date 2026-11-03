package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"os"
	"path/filepath"
	"sort"
	"time"
	"video-master/database"
	"video-master/models"
)

var ErrEpisodeAlreadyFilled = errors.New("episode_already_filled: 这一集已补齐，新下载的文件保留为未关联")

type EpisodeAssociationChoice struct {
	FileID                 string `json:"file_id"`
	OriginID               string `json:"origin_id"`
	ExpectedOriginRevision int64  `json:"expected_origin_revision"`
	Season                 int    `json:"season"`
	Episode                int    `json:"episode"`
}

func (r *EpisodeAssociationChoice) UnmarshalJSON(data []byte) error {
	if err := requireEpisodeFields(data, "season", "episode"); err != nil {
		return err
	}
	type plain EpisodeAssociationChoice
	return json.Unmarshal(data, (*plain)(r))
}

type EpisodeAssociationRequest struct {
	TaskID string                     `json:"task_id"`
	Items  []EpisodeAssociationChoice `json:"items"`
}
type EpisodeAssociationItem struct {
	OriginID             string `json:"origin_id"`
	FileID               string `json:"file_id"`
	VideoID              uint   `json:"video_id"`
	Title                string `json:"title"`
	Season               int    `json:"season"`
	Episode              int    `json:"episode"`
	ExistingSeason       *int   `json:"existing_season"`
	ExistingEpisode      *int   `json:"existing_episode"`
	DifferentFromRequest bool   `json:"different_from_request"`
	AlreadyLinked        bool   `json:"already_linked"`
	Conflict             string `json:"conflict"`
	Message              string `json:"message"`
}
type EpisodeAssociationPreview struct {
	PreviewToken    string                   `json:"preview_token"`
	ExpiresAt       time.Time                `json:"expires_at" ts_type:"string"`
	CollectionID    uint                     `json:"collection_id"`
	CollectionTitle string                   `json:"collection_title"`
	Items           []EpisodeAssociationItem `json:"items"`
	Conflicts       int                      `json:"conflicts"`
}
type EpisodeAssociationResult struct {
	CollectionID uint   `json:"collection_id"`
	LinkedCount  int    `json:"linked_count"`
	VideoIDs     []uint `json:"video_ids"`
}
type episodeAssociationEntry struct {
	origin  models.EpisodeAcquisitionOrigin
	file    models.BTFile
	videoID uint
	source  string
	choice  EpisodeAssociationChoice
}

func episodeAssociationState(report *CollectionEpisodeReport, videoID uint, season, episode int) (bool, string, *CollectionEpisodeMember) {
	var own *CollectionEpisodeMember
	for i := range report.allMembers {
		if report.allMembers[i].VideoID == videoID {
			own = &report.allMembers[i]
			break
		}
	}
	if own != nil {
		matches := !own.Excluded && own.Season != nil && own.Episode != nil && *own.Season == season && *own.Episode == episode
		if matches {
			return true, "", own
		}
		if own.Excluded || own.Revision > 0 {
			return false, "episode_manual_conflict", own
		}
	}
	for _, member := range report.allMembers {
		if member.VideoID != videoID && !member.Excluded && member.Season != nil && member.Episode != nil && *member.Season == season && *member.Episode == episode {
			return false, "episode_already_filled", own
		}
	}
	return false, "", own
}
func episodeConflictMessage(code string) string {
	switch code {
	case "episode_already_filled":
		return "这一集已补齐，新文件保留为未关联"
	case "episode_manual_conflict":
		return "已有人工季集归属，请先到作品集核对"
	case "episode_duplicate_selection":
		return "本次不能把不同视频关联到同一季集"
	case "episode_source_changed":
		return "来源或文件已变化，请重新核对"
	}
	return ""
}
func validAssociationRequest(request EpisodeAssociationRequest) error {
	if !btTaskUUID(request.TaskID) || len(request.Items) == 0 || len(request.Items) > 200 {
		return errors.New("invalid_input: 请选择1至200个已入库文件")
	}
	seen := map[string]bool{}
	for _, item := range request.Items {
		if !btTaskUUID(item.OriginID) || len(item.FileID) != 64 || seen[item.FileID] || item.ExpectedOriginRevision < 1 || item.ExpectedOriginRevision >= experienceMaxSafeInteger || item.Season < 0 || item.Season > 999 || item.Episode < 1 || item.Episode > 1000000 {
			return errors.New("invalid_input: 文件与季集映射无效或重复")
		}
		seen[item.FileID] = true
	}
	return nil
}

func episodeVideoSource(tx *gorm.DB, id uint) (models.Video, os.FileInfo, error) {
	return bookmarkSourceInScope(tx, id)
}
func episodeFileReady(file models.BTFile) bool {
	return file.VideoID != nil && file.ImportState == "imported" && (file.PublicationState == "" || file.PublicationState == BTImportPublicationDone) && btFileKind(file.RelativePath) == "video"
}

func episodeSourceForFile(tx *gorm.DB, file models.BTFile) (models.Video, os.FileInfo, error) {
	if !episodeFileReady(file) {
		return models.Video{}, nil, ErrBookmarkSourceUnavailable
	}
	video, info, err := episodeVideoSource(tx, *file.VideoID)
	if err != nil {
		return video, nil, err
	}
	var task models.BTTask
	if err := tx.First(&task, "id = ?", file.TaskID).Error; err != nil {
		return video, nil, err
	}
	expected := filepath.Join(task.SaveRoot, filepath.FromSlash(file.RelativePath))
	real, err := filepath.EvalSymlinks(video.Path)
	if err != nil || filepath.Clean(video.Path) != filepath.Clean(expected) || !btWithin(expected, task.SaveRoot) || !btWithin(real, task.CanonicalRoot) || info.Size() != file.Size || info.Size() != file.ObservedSize || info.ModTime().UnixNano() != file.ObservedMtime {
		return video, nil, ErrBookmarkSourceChanged
	}
	return video, info, nil
}

// PreviewEpisodeAssociation freezes explicit mappings; a conflicting item never
// disappears from the preview and prevents the entire confirmation.
func (s *CollectionService) PreviewEpisodeAssociation(owner context.Context, request EpisodeAssociationRequest) (*EpisodeAssociationPreview, error) {
	if err := validAssociationRequest(request); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(owner, 30*time.Second)
	defer cancel()
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	result := &EpisodeAssociationPreview{Items: []EpisodeAssociationItem{}}
	snapshot := &episodeAssociationSnapshot{owner: owner, db: database.DB, expires: time.Now().Add(10 * time.Minute)}
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			var report *CollectionEpisodeReport
			seenTargets := map[string]uint{}
			seenVideos := map[uint]string{}
			for _, choice := range request.Items {
				var origin models.EpisodeAcquisitionOrigin
				if err := tx.Where("id = ? AND task_id = ? AND file_id = ?", choice.OriginID, request.TaskID, choice.FileID).First(&origin).Error; err != nil {
					return err
				}
				if report == nil {
					var err error
					report, err = collectionEpisodeReportFrom(ctx, tx, origin.CollectionID, 0, 200)
					if err != nil {
						return err
					}
					snapshot.collectionID = origin.CollectionID
					snapshot.rootRevision = report.sourceRevision
					snapshot.reportSignature = report.sourceSignature
					result.CollectionID = origin.CollectionID
					result.CollectionTitle = report.sourceName
				}
				if origin.CollectionID != snapshot.collectionID {
					return errors.New("invalid_input: 请按作品集分别确认")
				}
				item := EpisodeAssociationItem{OriginID: origin.ID, FileID: origin.FileID, Season: choice.Season, Episode: choice.Episode}
				if origin.Revision != choice.ExpectedOriginRevision {
					item.Conflict = "episode_source_changed"
				}
				var file models.BTFile
				if err := tx.First(&file, "task_id = ? AND manifest_file_id = ?", request.TaskID, choice.FileID).Error; err != nil {
					return err
				}
				item.Title = file.RelativePath
				entry := episodeAssociationEntry{origin: origin, file: file, choice: choice}
				if !episodeFileReady(file) {
					item.Conflict = "episode_source_changed"
				} else {
					video, info, err := episodeSourceForFile(tx, file)
					if err != nil {
						if !errors.Is(err, ErrBookmarkSourceUnavailable) && !errors.Is(err, ErrBookmarkSourceChanged) {
							return err
						}
						item.Conflict = "episode_source_changed"
					} else {
						item.VideoID = video.ID
						entry.videoID = video.ID
						entry.source = videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano())
						linked, conflict, own := episodeAssociationState(report, video.ID, choice.Season, choice.Episode)
						item.AlreadyLinked = linked
						if conflict != "" {
							item.Conflict = conflict
						}
						if own != nil {
							item.ExistingSeason = own.Season
							item.ExistingEpisode = own.Episode
						}
						key := fmt.Sprintf("%d:%d", choice.Season, choice.Episode)
						if id, exists := seenTargets[key]; exists && id != video.ID && item.Conflict == "" {
							item.Conflict = "episode_duplicate_selection"
						}
						if previous, exists := seenVideos[video.ID]; exists && previous != key && item.Conflict == "" {
							item.Conflict = "episode_duplicate_selection"
						}
						seenTargets[key] = video.ID
						seenVideos[video.ID] = key
					}
				}
				item.DifferentFromRequest = choice.Season != origin.RequestedSeason || !requestedEpisodeContains(origin.RequestedEpisodesJSON, choice.Episode)
				item.Message = episodeConflictMessage(item.Conflict)
				if item.Conflict != "" {
					result.Conflicts++
				}
				result.Items = append(result.Items, item)
				snapshot.items = append(snapshot.items, entry)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	if result.Conflicts > 0 {
		return result, nil
	}
	s.episodeMu.Lock()
	defer s.episodeMu.Unlock()
	s.pruneEpisodePreviewsLocked()
	if len(s.episodeAssociations) >= 16 {
		return nil, errors.New("invalid_input: 待确认关联过多，请稍后重试")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.episodeAssociations == nil {
		s.episodeAssociations = map[string]*episodeAssociationSnapshot{}
	}
	result.PreviewToken = uuid.NewString()
	result.ExpiresAt = snapshot.expires
	s.episodeAssociations[result.PreviewToken] = snapshot
	return result, nil
}

// ConfirmEpisodeAssociation commits membership, manual mapping and origin CAS in
// one transaction. A competing completion leaves every new file unassociated.
func (s *CollectionService) ConfirmEpisodeAssociation(owner context.Context, token string) (*EpisodeAssociationResult, error) {
	ctx, cancel := context.WithTimeout(owner, 30*time.Second)
	defer cancel()
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	s.episodeMu.Lock()
	defer s.episodeMu.Unlock()
	s.pruneEpisodePreviewsLocked()
	snapshot := s.episodeAssociations[token]
	if snapshot == nil {
		return nil, ErrEpisodeContextChanged
	}
	if snapshot.result != nil {
		return cloneEpisodeAssociationResult(snapshot.result), nil
	}
	result := &EpisodeAssociationResult{CollectionID: snapshot.collectionID, VideoIDs: []uint{}}
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			report, err := collectionEpisodeReportFrom(ctx, tx, snapshot.collectionID, 0, 200)
			if err != nil {
				return err
			}
			for _, entry := range snapshot.items {
				_, conflict, _ := episodeAssociationState(report, entry.videoID, entry.choice.Season, entry.choice.Episode)
				if conflict == "episode_already_filled" {
					return ErrEpisodeAlreadyFilled
				}
				if conflict != "" {
					return ErrEpisodeContextChanged
				}
			}
			if report.sourceSignature != snapshot.reportSignature {
				return ErrEpisodeContextChanged
			}
			if err := claimCollectionMutation(tx, snapshot.collectionID, snapshot.rootRevision); err != nil {
				return err
			}
			var position int
			if err := tx.Model(&models.CollectionVideo{}).Where("collection_id = ?", snapshot.collectionID).Select("COALESCE(MAX(position),0)").Scan(&position).Error; err != nil {
				return err
			}
			seen := map[uint]bool{}
			for _, entry := range snapshot.items {
				var file models.BTFile
				if err := tx.First(&file, "task_id = ? AND manifest_file_id = ?", entry.file.TaskID, entry.file.ManifestFileID).Error; err != nil {
					return err
				}
				if !episodeFileReady(file) || *file.VideoID != entry.videoID || file.EligibilityRevision != entry.file.EligibilityRevision {
					return ErrEpisodeContextChanged
				}
				video, info, err := episodeSourceForFile(tx, file)
				if err != nil {
					if !errors.Is(err, ErrBookmarkSourceUnavailable) && !errors.Is(err, ErrBookmarkSourceChanged) {
						return err
					}
					return ErrEpisodeContextChanged
				}
				if videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano()) != entry.source {
					return ErrEpisodeContextChanged
				}
				if !seen[video.ID] {
					var count int64
					if err := tx.Model(&models.CollectionVideo{}).Where("collection_id = ? AND video_id = ?", snapshot.collectionID, video.ID).Count(&count).Error; err != nil {
						return err
					}
					if count == 0 {
						position++
						if err := tx.Create(&models.CollectionVideo{CollectionID: snapshot.collectionID, VideoID: video.ID, Position: position}).Error; err != nil {
							return err
						}
					}
					var assignment models.CollectionEpisodeAssignment
					err = tx.First(&assignment, "collection_id = ? AND video_id = ?", snapshot.collectionID, video.ID).Error
					if errors.Is(err, gorm.ErrRecordNotFound) {
						created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.CollectionEpisodeAssignment{CollectionID: snapshot.collectionID, VideoID: video.ID, Season: &entry.choice.Season, Episode: &entry.choice.Episode, Revision: 1})
						if created.Error != nil {
							return created.Error
						}
						if created.RowsAffected != 1 {
							return ErrEpisodeContextChanged
						}
					} else if err != nil {
						return err
					} else if assignment.Excluded || assignment.Season == nil || assignment.Episode == nil || *assignment.Season != entry.choice.Season || *assignment.Episode != entry.choice.Episode {
						return ErrEpisodeContextChanged
					}
					seen[video.ID] = true
					result.VideoIDs = append(result.VideoIDs, video.ID)
				}
				changed := tx.Model(&models.EpisodeAcquisitionOrigin{}).Where("id = ? AND revision = ?", entry.origin.ID, entry.origin.Revision).Updates(map[string]any{"mapped_season": entry.choice.Season, "mapped_episode": entry.choice.Episode, "confirmed_mapping": true, "revision": entry.origin.Revision + 1})
				if changed.Error != nil {
					return changed.Error
				}
				if changed.RowsAffected != 1 {
					return ErrEpisodeContextChanged
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result.VideoIDs, func(i, j int) bool { return result.VideoIDs[i] < result.VideoIDs[j] })
	result.LinkedCount = len(result.VideoIDs)
	snapshot.result = cloneEpisodeAssociationResult(result)
	return result, nil
}

func cloneEpisodeAssociationResult(result *EpisodeAssociationResult) *EpisodeAssociationResult {
	cloned := *result
	cloned.VideoIDs = append([]uint{}, result.VideoIDs...)
	return &cloned
}
