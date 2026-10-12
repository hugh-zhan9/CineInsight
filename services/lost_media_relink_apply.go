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

// recordLostOutcome 把一条的处理结果记进批结果：nil 为完成，errLostSkip 为跳过，因取消（或进入维护）没处理完的
// 记为 cancelled（事务已回滚，这条没有动过），其余为失败（原文只进日志，G-3）。返回 true 表示整批应就此停下。
func recordLostOutcome(ctx context.Context, result *LostMediaBatchResult, operation string, id uint, err error) bool {
	var skip errLostSkip
	switch {
	case err == nil:
		result.Done++
	case errors.As(err, &skip):
		result.skip(id, skip.reason)
	case lostItemInterrupted(ctx, err):
		result.Cancelled = true
		result.Items = append(result.Items, LostMediaItemOutcome{ID: id, Outcome: LostOutcomeCancelled, Reason: LostOutcomeCancelled})
		return true
	default:
		log.Printf("失联%s失败 id=%d err=%v", operation, id, err)
		result.fail(id)
	}
	return false
}

// LostOutcomeCancelled 是「取消时正在处理、没有处理完」的条目的结果与原因码。
const LostOutcomeCancelled = "cancelled"

// lostItemInterrupted 报告一条是不是被取消或数据库维护打断的（等锁时、事务里），而不是自身出错。
func lostItemInterrupted(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, database.ErrMaintenance)
}

// lostSkipForReason 是归类已不可处理时的跳过原因：卷未挂载报 volume_offline，其余（读取出错）报 no_longer_lost。
func lostSkipForReason(reason string) error {
	if reason == LostReasonOffline {
		return lostSkip(LostSkipVolumeOffline)
	}
	return lostSkip(LostSkipNoLongerLost)
}

// lockLostKind 拿这类媒体的路径写锁，等锁可以被取消（acquireLibraryPathContext）：视频是全局路径锁，
// 维护围栏生效时返回 ErrMaintenance；图片是 imagePathMutationMu（排在图片扫描之后时也能取消）。
func lockLostKind(ctx context.Context, kind string) (func(), error) {
	if kind == LostMediaKindVideo {
		return lockLibraryPathsContext(ctx)
	}
	return acquireLibraryPathContext(ctx, imagePathMutationMu.TryLock, imagePathMutationMu.Lock, imagePathMutationMu.Unlock)
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
		stop := recordLostOutcome(ctx, result, "找回", pair.ID, s.relinkOne(ctx, request.Kind, pair))
		s.advance()
		if stop {
			break
		}
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

// relinkOne 找回一条：在路径写锁内复核并改写记录（relinkLocked），放开锁之后视频再补探测。补探测读 ffprobe
// 可能要几秒，不在全局路径写锁里做；路径已经提交，探测失败或被取消都只记日志、不算这一条失败。
func (s *LostMediaService) relinkOne(ctx context.Context, kind string, pair LostRelinkPair) error {
	newPath, err := s.relinkLocked(ctx, kind, pair)
	if err != nil {
		return err
	}
	if kind == LostMediaKindVideo {
		_ = database.WithOperationContext(ctx, func(db *gorm.DB) error {
			s.videoService().refreshRelocatedVideo(db, pair.ID, newPath)
			return nil
		})
	}
	return nil
}

// relinkLocked 在路径写锁内复核一条：仍是可找回的失联条目、新路径是文件且大小与记录相同、在当前扫描根内、
// 没被另一条活动记录占用；然后失效行走 relocateVideoRecord / relocateImage，扫描器回收站行走 restoreLostRecord。
// 返回清理过的新路径。
func (s *LostMediaService) relinkLocked(ctx context.Context, kind string, pair LostRelinkPair) (string, error) {
	unlock, err := lockLostKind(ctx, kind)
	if err != nil {
		return "", err
	}
	defer unlock()
	found, err := loadLostRowsByID(ctx, kind, []uint{pair.ID})
	if err != nil {
		return "", err
	}
	row, ok := found[pair.ID]
	if !ok {
		return "", lostSkip(LostSkipNoLongerLost)
	}
	classifier, err := newLostClassifier(kind)
	if err != nil {
		return "", err
	}
	if reason := classifier.reason(row); !lostReasonActionable(reason) {
		return "", lostSkipForReason(reason)
	}
	newPath := filepath.Clean(pair.NewPath)
	info, statErr := os.Stat(newPath)
	if !filepath.IsAbs(newPath) || statErr != nil || !info.Mode().IsRegular() {
		return "", lostSkip(LostSkipTargetMissing)
	}
	if info.Size() != row.Size {
		return "", lostSkip(LostSkipSizeMismatch)
	}
	roots, err := activeMediaRoots(kind)
	if err != nil {
		return "", err
	}
	if !pathInRoots(newPath, roots) {
		return "", lostSkip(LostSkipOutsideRoots)
	}
	owners, err := activePathOwners(kind, []string{newPath})
	if err != nil {
		return "", err
	}
	if owner, taken := owners[newPath]; taken && owner != row.ID {
		return "", lostSkip(LostSkipOccupied)
	}
	switch {
	case row.deleted():
		err = restoreLostRecord(ctx, kind, row, newPath)
	case kind == LostMediaKindVideo:
		err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
			return s.videoService().relocateVideoRecord(db, row.ID, newPath)
		})
	default:
		err = s.imageService().relocateImage(row.ID, newPath)
	}
	return newPath, err
}

// restoreLostRecord 把一条扫描器回收站行在新路径上恢复（详细设计 1.2）：同一事务里取消软删、改路径并清除失效、
// 拿掉它的 missing 回收站条目并按新路径重建字幕索引（视频；扫描器软删时字幕索引已被清掉，与两条既有恢复路径同口径）。
// 记录 id 不变；补探测由调用方在放开路径锁之后做。
//
// 回收站条目按 restoreTrashEntry 的成功语义直接删除：retireTrashEntryTx 对 missing 条目会连同媒体记录一起硬删，
// 只适用于清除，不适用于找回。图片的扫描器软删没有回收站条目（deleteMissingImageRecord）。
func restoreLostRecord(ctx context.Context, kind string, row lostRow, newPath string) error {
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
			var restored models.Video
			if err := tx.First(&restored, row.ID).Error; err != nil {
				return err
			}
			if err := rebuildSubtitleIndexTx(tx, restored); err != nil {
				return fmt.Errorf("重建字幕索引失败: %w", err)
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
	if err == nil {
		log.Printf("失联找回（扫描器回收站行） kind=%s id=%d", kind, row.ID)
	}
	return err
}
