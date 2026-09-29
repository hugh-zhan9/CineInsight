package services

import (
	"errors"
	"fmt"
	"os"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// Caller holds the scan/path locks. Historical unknown and user deletions are
// intentionally left alone: file presence does not establish deletion intent.
func (s *VideoService) addScannedVideo(path string) (*models.Video, bool, error) {
	var video models.Video
	err := database.DB.Unscoped().Where("path = ?", path).
		Order("deleted_at IS NULL DESC, id DESC").First(&video).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	if err == nil && video.DeletedAt.IsValid() && video.DeletedBy == "scanner" {
		var entry models.VideoTrashEntry
		if err := database.DB.Where("video_id = ? AND deleted_by = ? AND file_moved = ? AND state = ? AND mode IN ?",
			video.ID, "scanner", false, trashStateDeleted, []string{models.TrashModeMissing, ""}).First(&entry).Error; err != nil {
			return nil, false, err
		}
		restored, err := s.restoreTrashEntry(&entry)
		return restored, err == nil, err
	}
	if err == nil && video.DeletedAt.IsValid() {
		// 访达「放回原处」：原路径上的文件与回收站条目身份一致，就地恢复原记录（I3）。
		if restored, ok, putBackErr := s.restoreVideoIfPutBack(&video, path); putBackErr != nil {
			return nil, false, putBackErr
		} else if ok {
			return restored, true, nil
		}
	}
	added, err := s.addVideo(path)
	return added, false, err
}

// restoreVideoIfPutBack 判断软删视频的文件是否被用户在访达里放回了原路径（inode + 大小 + mtime 一致，
// 设备号不参与），是则只在数据库里恢复原记录（原 ID、标签、人物），条目按既有恢复成功语义移除。
// 条目此前若因废纸篓里找不到文件被标成 file_gone，先条件更新回 deleted 再走既有恢复。
func (s *VideoService) restoreVideoIfPutBack(deleted *models.Video, path string) (*models.Video, bool, error) {
	info, statErr := os.Stat(path)
	if statErr != nil || info.IsDir() {
		return nil, false, nil
	}
	var entry models.VideoTrashEntry
	result := database.DB.Where("video_id = ?", deleted.ID).Limit(1).Find(&entry)
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 0 || !isPutBack(entry.Mode, entry.State, entry.FileSize, entry.FileModTime, entry.FileIdentity, info) {
		return nil, false, nil
	}
	if entry.State == models.TrashStateFileGone {
		revive := database.DB.Model(&models.VideoTrashEntry{}).
			Where("id = ? AND state = ?", entry.ID, models.TrashStateFileGone).
			Updates(map[string]interface{}{"state": trashStateDeleted, "last_error": ""})
		if revive.Error != nil {
			return nil, false, revive.Error
		}
		entry.State = trashStateDeleted
	}
	restored, err := s.restoreTrashEntry(&entry)
	if err != nil {
		return nil, false, err
	}
	return restored, true, nil
}

// restoreImageIfPutBack 是 restoreVideoIfPutBack 的图片版本（I3）。
func (s *ImageService) restoreImageIfPutBack(path string) (bool, error) {
	info, statErr := os.Stat(path)
	if statErr != nil || info.IsDir() {
		return false, nil
	}
	var deleted models.Image
	found := database.DB.Unscoped().Where("path = ? AND deleted_at IS NOT NULL", path).Order("id DESC").Limit(1).Find(&deleted)
	if found.Error != nil {
		return false, found.Error
	}
	if found.RowsAffected == 0 {
		return false, nil
	}
	var entry models.ImageTrashEntry
	result := database.DB.Where("image_id = ?", deleted.ID).Limit(1).Find(&entry)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 || !isPutBack(entry.Mode, entry.State, entry.FileSize, entry.FileModTime, entry.FileIdentity, info) {
		return false, nil
	}
	if entry.State == models.TrashStateFileGone {
		revive := database.DB.Model(&models.ImageTrashEntry{}).
			Where("id = ? AND state = ?", entry.ID, models.TrashStateFileGone).
			Updates(map[string]interface{}{"state": trashStateDeleted, "last_error": ""})
		if revive.Error != nil {
			return false, revive.Error
		}
		entry.State = trashStateDeleted
	}
	if _, err := s.restoreImageTrashEntry(&entry); err != nil {
		return false, err
	}
	return true, nil
}

// isPutBack 是「访达放回原处」的判定：只有 trash 模式、状态为 deleted / file_gone、记录了 inode 与 mtime
// 的条目才有足够身份；当前文件的大小、mtime、inode 必须全部一致。
func isPutBack(mode, state string, size, mtimeNS int64, identity string, info os.FileInfo) bool {
	if state != trashStateDeleted && state != models.TrashStateFileGone {
		return false
	}
	want := trashFileID{Size: size, ModTimeNS: mtimeNS, Identity: identity}
	return canDetectPutBack(mode, want) && want.strictMatch(info)
}

// 同路径软删行的判定结果（详细设计 §2.2）。
type softDeletedPathAction int

const (
	// softDeletedCreateNew：同路径的文件是新文件（或身份已变），照常新建记录。
	softDeletedCreateNew softDeletedPathAction = iota
	// softDeletedAutoRestore：扫描器因文件缺失软删的行，沿用既有的自动恢复。
	softDeletedAutoRestore
	// softDeletedBlocked：用户「只删记录」留下的行且文件身份未变，不得重新收录。
	softDeletedBlocked
	// softDeletedPutBack：用户在访达里把废纸篓里的文件放回了原处（inode + 大小 + mtime 一致），
	// 应就地恢复原记录，而不是新建（I3）。
	softDeletedPutBack
)

// softDeletedEntryFacts 是判定所需的条目事实（视频与图片条目字段一致）。
type softDeletedEntryFacts struct {
	Mode          string
	DeletedBy     string
	FileSize      int64
	FileModTime   int64
	FileIdentity  string
	DeleteBatchID string
	State         string
}

// decideSoftDeletedPath 按 §2.2 的表依次判定：
//
//  1. mode=missing 且 deleted_by=scanner → 自动恢复；
//  2. mode=record_only（新时代行，delete_batch_id 非空），且大小与 mtime 都没变 → 屏蔽；
//  3. mode=trash 且原路径上的文件与条目身份（inode + 大小 + mtime）一致 → 访达放回，就地恢复；
//     mode 为 trash / legacy_trash 的其余情形 → 新建（原文件在废纸篓，这是新文件）；
//  4. 没有条目、或历史回填行（delete_batch_id 为空的 record_only），且记录大小等于文件大小 → 屏蔽；
//  5. 其他 → 新建。
//
// 第 2 行有一个设计没写的退化情形：条目建于文件已不在的时候（mtime 记为 0），没有 mtime 可比，
// 此时只比大小。这个兜底只对新时代行生效；历史回填行的条目没有可信的 mtime 与大小，按第 4 行
// 只比较视频/图片记录上的大小，不与第 2 行混用（Minor 3）。
func decideSoftDeletedPath(entry *softDeletedEntryFacts, rowSize, size, mtimeNS int64, identity string) softDeletedPathAction {
	if entry != nil {
		switch entry.Mode {
		case models.TrashModeMissing:
			if entry.DeletedBy == "scanner" {
				return softDeletedAutoRestore
			}
		case models.TrashModeRecordOnly:
			if entry.DeleteBatchID == "" {
				break // 历史回填行：落到下面的第 4 行
			}
			if entry.FileSize == size && (entry.FileModTime == mtimeNS || entry.FileModTime == 0) {
				return softDeletedBlocked
			}
			return softDeletedCreateNew
		case models.TrashModeTrash:
			want := trashFileID{Size: entry.FileSize, ModTimeNS: entry.FileModTime, Identity: entry.FileIdentity}
			if (entry.State == trashStateDeleted || entry.State == models.TrashStateFileGone) && canDetectPutBack(entry.Mode, want) &&
				want.Size == size && want.ModTimeNS == mtimeNS && identity != "" && identityInode(want.Identity) == identityInode(identity) {
				return softDeletedPutBack
			}
		}
		if entry.Mode != models.TrashModeRecordOnly {
			return softDeletedCreateNew
		}
	}
	if rowSize == size {
		return softDeletedBlocked
	}
	return softDeletedCreateNew
}

// softDeletedVideoPathSkip 在同路径没有活跃记录时，判定是否要跳过新建。skip 非 nil 时返回同路径
// 的软删行与应返回给调用方的错误：被屏蔽时同时满足 ErrVideoBlockedByUserDelete 与 ErrVideoExists
// （既有的扫描调用方按后者计入 skipped，P-011 按前者分项），自动恢复情形只返回 ErrVideoExists。
func softDeletedVideoPathSkip(path string, info os.FileInfo) (row *models.Video, skip error, err error) {
	var deleted models.Video
	result := database.DB.Unscoped().Where("path = ? AND deleted_at IS NOT NULL", path).
		Order("id DESC").Limit(1).Find(&deleted)
	if result.Error != nil {
		return nil, nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil, nil
	}
	var entry models.VideoTrashEntry
	entryResult := database.DB.Where("video_id = ?", deleted.ID).Limit(1).Find(&entry)
	if entryResult.Error != nil {
		return nil, nil, entryResult.Error
	}
	var facts *softDeletedEntryFacts
	if entryResult.RowsAffected == 1 {
		facts = &softDeletedEntryFacts{Mode: entry.Mode, DeletedBy: entry.DeletedBy, FileSize: entry.FileSize, FileModTime: entry.FileModTime,
			FileIdentity: entry.FileIdentity, DeleteBatchID: entry.DeleteBatchID, State: entry.State}
	}
	switch decideSoftDeletedPath(facts, deleted.Size, info.Size(), info.ModTime().UnixNano(), stableFileIdentity(info)) {
	case softDeletedBlocked:
		return &deleted, fmt.Errorf("%w: %w", ErrVideoBlockedByUserDelete, ErrVideoExists), nil
	case softDeletedAutoRestore, softDeletedPutBack:
		// 自动恢复与访达放回都由扫描流程在 addVideo 之前处理；直接 AddVideo 时不新建重复记录。
		return &deleted, ErrVideoExists, nil
	}
	return nil, nil, nil
}

// ErrImageBlockedByUserDelete 与 ErrVideoBlockedByUserDelete 对应：同路径的软删图片行是用户
// 「只删记录」留下的，且文件身份未变，扫描不得重新收录。
var ErrImageBlockedByUserDelete = errors.New("该图片已被你从图库移除，扫描不会重新收录")

// softDeletedImagePathSkip 是 softDeletedVideoPathSkip 的图片版本。图片的扫描器软删不建条目、
// 靠 is_stale 恢复标记自动恢复（restoreStaleImage 在 addImage 之前执行），所以这里遇到的
// 软删行要么是用户删的，要么是恢复标记已失效的历史行。
func softDeletedImagePathSkip(path string, info os.FileInfo) (row *models.Image, skip error, err error) {
	var deleted models.Image
	result := database.DB.Unscoped().Where("path = ? AND deleted_at IS NOT NULL", path).
		Order("id DESC").Limit(1).Find(&deleted)
	if result.Error != nil {
		return nil, nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil, nil
	}
	var entry models.ImageTrashEntry
	entryResult := database.DB.Where("image_id = ?", deleted.ID).Limit(1).Find(&entry)
	if entryResult.Error != nil {
		return nil, nil, entryResult.Error
	}
	var facts *softDeletedEntryFacts
	if entryResult.RowsAffected == 1 {
		facts = &softDeletedEntryFacts{Mode: entry.Mode, DeletedBy: entry.DeletedBy, FileSize: entry.FileSize, FileModTime: entry.FileModTime,
			FileIdentity: entry.FileIdentity, DeleteBatchID: entry.DeleteBatchID, State: entry.State}
	}
	switch decideSoftDeletedPath(facts, deleted.Size, info.Size(), info.ModTime().UnixNano(), stableFileIdentity(info)) {
	case softDeletedBlocked:
		return &deleted, fmt.Errorf("%w: %w", ErrImageBlockedByUserDelete, ErrImageExists), nil
	case softDeletedAutoRestore, softDeletedPutBack:
		return &deleted, ErrImageExists, nil
	}
	return nil, nil, nil
}
