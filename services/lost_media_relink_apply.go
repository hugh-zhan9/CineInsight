package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// LostRelinkPair 是一条要执行的找回：把记录 ID 改到 NewPath。
type LostRelinkPair struct {
	ID      uint   `json:"id"`
	NewPath string `json:"new_path"`
}

// LostRelinkApplyRequest 是执行找回的请求（预览里勾选的候选）。
type LostRelinkApplyRequest struct {
	Kind  string           `json:"kind"`
	Pairs []LostRelinkPair `json:"pairs"`
}

// 找回与清除共用的跳过原因码。
const (
	LostSkipNoLongerLost      = "no_longer_lost"
	LostSkipVolumeOffline     = "volume_offline"
	LostSkipFilePresent       = "file_present"
	LostSkipEnhancementActive = "enhancement_active"
	LostSkipTargetMissing     = "target_missing"
	LostSkipSizeMismatch      = "size_mismatch"
	LostSkipOutsideRoots      = "outside_roots"
	LostSkipOccupied          = "occupied"
)

// errLostSkip 是逐条复核得出的「跳过」（不是失败）。
type errLostSkip struct{ reason string }

func (e errLostSkip) Error() string { return "skipped: " + e.reason }

func lostSkip(reason string) error { return errLostSkip{reason: reason} }

// recordLostOutcome 把一条的处理结果记进批结果：nil 为完成，errLostSkip 为跳过，其余为失败（原文只进日志，G-3）。
func recordLostOutcome(result *LostMediaBatchResult, operation string, id uint, err error) {
	var skip errLostSkip
	switch {
	case err == nil:
		result.Done++
	case errors.As(err, &skip):
		result.skip(id, skip.reason)
	default:
		log.Printf("失联%s失败 id=%d err=%v", operation, id, err)
		result.fail(id)
	}
}

// lostSkipForReason 是归类已不可处理时的跳过原因：卷未挂载报 volume_offline，其余（读取出错）报 no_longer_lost。
func lostSkipForReason(reason string) error {
	if reason == LostReasonOffline {
		return lostSkip(LostSkipVolumeOffline)
	}
	return lostSkip(LostSkipNoLongerLost)
}

// lockLostKind 拿这类媒体的路径写锁（视频经 lockLibraryPaths，维护围栏生效时立即返回 ErrMaintenance）。
func lockLostKind(kind string) (func(), error) {
	if kind == LostMediaKindVideo {
		return lockLibraryPaths()
	}
	imagePathMutationMu.Lock()
	return imagePathMutationMu.Unlock, nil
}

// ApplyLostRelink 逐条在路径锁内复核候选条件后改路径，保持记录 id（D-LD-03）。不因一条失败中断整批；
// 取消时已处理的保留，结果标 Cancelled。
func (s *LostMediaService) ApplyLostRelink(parent context.Context, request LostRelinkApplyRequest) (*LostMediaBatchResult, error) {
	if err := validLostMediaKind(request.Kind); err != nil {
		return nil, err
	}
	if len(request.Pairs) == 0 {
		return nil, lostMediaInvalid("没有选择要找回的条目")
	}
	ctx, finish, err := s.begin(parent, "relink", len(request.Pairs))
	if err != nil {
		return nil, err
	}
	defer finish()
	result := &LostMediaBatchResult{Items: []LostMediaItemOutcome{}}
	for _, pair := range request.Pairs {
		if ctx.Err() != nil {
			result.Cancelled = true
			break
		}
		recordLostOutcome(result, "找回", pair.ID, s.relinkOne(ctx, request.Kind, pair))
		s.advance()
	}
	log.Printf("失联找回 kind=%s requested=%d done=%d skipped=%d failed=%d cancelled=%t",
		request.Kind, len(request.Pairs), result.Done, result.Skipped, result.Failed, result.Cancelled)
	return result, nil
}

func (s *LostMediaService) videoService() *VideoService {
	if s.videos != nil {
		return s.videos
	}
	return &VideoService{}
}

func (s *LostMediaService) imageService() *ImageService {
	if s.images != nil {
		return s.images
	}
	return NewImageService()
}

// relinkOne 在路径写锁内复核一条：仍是可找回的失联条目、新路径是文件且大小与记录相同、在当前扫描根内、
// 没被另一条活动记录占用；然后失效行走 relocateVideoFrom / relocateImage，扫描器回收站行走 restoreLostAt。
func (s *LostMediaService) relinkOne(ctx context.Context, kind string, pair LostRelinkPair) error {
	unlock, err := lockLostKind(kind)
	if err != nil {
		return err
	}
	defer unlock()
	found, err := loadLostRowsByID(ctx, kind, []uint{pair.ID})
	if err != nil {
		return err
	}
	row, ok := found[pair.ID]
	if !ok {
		return lostSkip(LostSkipNoLongerLost)
	}
	classifier, err := newLostClassifier(kind)
	if err != nil {
		return err
	}
	if reason := classifier.reason(row); !lostReasonActionable(reason) {
		return lostSkipForReason(reason)
	}
	newPath := filepath.Clean(pair.NewPath)
	info, statErr := os.Stat(newPath)
	if !filepath.IsAbs(newPath) || statErr != nil || !info.Mode().IsRegular() {
		return lostSkip(LostSkipTargetMissing)
	}
	if info.Size() != row.Size {
		return lostSkip(LostSkipSizeMismatch)
	}
	roots, err := activeMediaRoots(kind)
	if err != nil {
		return err
	}
	if !pathInRoots(newPath, roots) {
		return lostSkip(LostSkipOutsideRoots)
	}
	owners, err := activePathOwners(kind, []string{newPath})
	if err != nil {
		return err
	}
	if owner, taken := owners[newPath]; taken && owner != row.ID {
		return lostSkip(LostSkipOccupied)
	}
	if row.deleted() {
		return s.restoreLostAt(ctx, kind, row, newPath)
	}
	if kind == LostMediaKindVideo {
		return database.WithOperationContext(ctx, func(db *gorm.DB) error {
			return s.videoService().relocateVideoFrom(db, row.ID, newPath)
		})
	}
	return s.imageService().relocateImage(row.ID, newPath)
}

// restoreLostAt 把一条扫描器回收站行在新路径上恢复（详细设计 1.2）：同一事务里取消软删、改路径并清除失效、
// 拿掉它的 missing 回收站条目（视频）；提交后视频再补探测。记录 id 不变。
//
// 回收站条目按 restoreTrashEntry 的成功语义直接删除：retireTrashEntryTx 对 missing 条目会连同媒体记录一起硬删，
// 只适用于清除，不适用于找回。图片的扫描器软删没有回收站条目（deleteMissingImageRecord）。
func (s *LostMediaService) restoreLostAt(ctx context.Context, kind string, row lostRow, newPath string) error {
	err := database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		if kind == LostMediaKindVideo {
			result := tx.Unscoped().Model(&models.Video{}).
				Where("id = ? AND deleted_at IS NOT NULL AND deleted_by = ?", row.ID, "scanner").
				Updates(map[string]any{"deleted_at": nil, "is_stale": false, "stale_reason": "", "path": newPath,
					"directory": filepath.Dir(newPath), "name": filepath.Base(newPath)})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("视频记录已不再处于可找回状态: %d", row.ID)
			}
			entry := tx.Exec("DELETE FROM video_trash_entries WHERE video_id = ? AND deleted_by = ? AND file_moved = ? AND state = ? AND mode IN ?",
				row.ID, "scanner", false, trashStateDeleted, []string{models.TrashModeMissing, ""})
			if entry.Error != nil {
				return entry.Error
			}
			if entry.RowsAffected != 1 {
				return fmt.Errorf("回收站条目状态已变化: video=%d", row.ID)
			}
			return syncShortVideoTagForVideo(tx, row.ID)
		}
		result := tx.Unscoped().Model(&models.Image{}).
			Where("id = ? AND deleted_at IS NOT NULL AND deleted_by = ? AND is_stale = ?", row.ID, "scanner", true).
			Updates(map[string]any{"deleted_at": nil, "is_stale": false, "path": newPath,
				"directory": filepath.Dir(newPath), "name": filepath.Base(newPath)})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("图片记录已不再处于可找回状态: %d", row.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if kind == LostMediaKindVideo {
		_ = database.WithOperationContext(ctx, func(db *gorm.DB) error {
			s.videoService().refreshRelocatedVideo(db, row.ID, newPath)
			return nil
		})
	}
	log.Printf("失联找回（扫描器回收站行） kind=%s id=%d", kind, row.ID)
	return nil
}
