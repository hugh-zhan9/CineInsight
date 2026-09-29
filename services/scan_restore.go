package services

import (
	"errors"
	"fmt"
	"os"
	"strings"
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
	// 访达「放回原处」由 addVideoOrRestorePutBack 统一处理（与手动添加同一条路，Minor 2）。
	return s.addVideoOrRestorePutBack(path)
}

// restorePutBackVideo 判断同路径的软删视频里，是否有一条的文件被用户在访达里放回了原路径
// （putBackDetected），有则只在数据库里恢复那一条原记录（原 ID、标签、人物），条目按既有恢复成功
// 语义移除。遍历同路径的全部软删行、取身份一致的那条，而不是只看最新一条（Minor 2）。
// 条目此前若因废纸篓里找不到文件被标成 file_gone，先条件更新回 deleted 再走既有恢复。
// 调用方持有路径读锁（扫描或 AddVideo）。
func (s *VideoService) restorePutBackVideo(path string, info os.FileInfo) (*models.Video, bool, error) {
	var deleted []models.Video
	if err := database.DB.Unscoped().Select("id").Where("path = ? AND deleted_at IS NOT NULL", path).
		Order("id DESC").Find(&deleted).Error; err != nil {
		return nil, false, err
	}
	for _, row := range deleted {
		var entry models.VideoTrashEntry
		result := database.DB.Where("video_id = ?", row.ID).Limit(1).Find(&entry)
		if result.Error != nil {
			return nil, false, result.Error
		}
		if result.RowsAffected == 0 || !putBackDetectedFor(videoEntryFacts(entry), info) {
			continue
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
	return nil, false, nil
}

// restoreImageIfPutBack 是 restorePutBackVideo 的图片版本（I3、Minor 2）。
func (s *ImageService) restoreImageIfPutBack(path string) (bool, error) {
	info, statErr := os.Stat(path)
	if statErr != nil || info.IsDir() {
		return false, nil
	}
	var deleted []models.Image
	if err := database.DB.Unscoped().Select("id").Where("path = ? AND deleted_at IS NOT NULL", path).
		Order("id DESC").Find(&deleted).Error; err != nil {
		return false, err
	}
	for _, row := range deleted {
		var entry models.ImageTrashEntry
		result := database.DB.Where("image_id = ?", row.ID).Limit(1).Find(&entry)
		if result.Error != nil {
			return false, result.Error
		}
		if result.RowsAffected == 0 || !putBackDetectedFor(imageEntryFacts(entry), info) {
			continue
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
	return false, nil
}

// putBackDetected 是「访达放回原处」的唯一判定（扫描、手动添加、回收站列表与清除共用，Minor 2）：
//
//   - 条目是 trash 模式、状态为 deleted / file_gone，且记录了大小、mtime、inode（canDetectPutBack）；
//   - 原路径上文件的大小、mtime、inode（不含设备号）与条目全部一致；
//   - 废纸篓里的那一份已经不在，或与原路径上的是同一个文件（同一 inode 的两个名字）。
//
// 只有大小 + mtime 一致而 inode 不同，是另一个文件，不得认定为放回。
func putBackDetected(facts softDeletedEntryFacts, size, mtimeNS int64, identity string) bool {
	if facts.State != trashStateDeleted && facts.State != models.TrashStateFileGone {
		return false
	}
	want := trashFileID{Size: facts.FileSize, ModTimeNS: facts.FileModTime, Identity: facts.FileIdentity}
	if !canDetectPutBack(facts.Mode, want) {
		return false
	}
	if want.Size != size || want.ModTimeNS != mtimeNS || identity == "" || identityInode(want.Identity) != identityInode(identity) {
		return false
	}
	trashPath := strings.TrimSpace(facts.TrashPath)
	if trashPath == "" {
		return true
	}
	trashInfo, err := os.Stat(trashPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return true
	case err != nil:
		// 读不到废纸篓那一侧（权限等）：证据不足，不认定。
		return false
	}
	return !trashInfo.IsDir() && stableFileIdentity(trashInfo) == identity
}

// putBackDetectedFor 用原路径上文件的 FileInfo 做放回判定。
func putBackDetectedFor(facts softDeletedEntryFacts, info os.FileInfo) bool {
	if info == nil || info.IsDir() {
		return false
	}
	return putBackDetected(facts, info.Size(), info.ModTime().UnixNano(), stableFileIdentity(info))
}

// putBackAtPath 读取 path 上的文件并做放回判定；文件不在或读不到时返回 false。
func putBackAtPath(facts softDeletedEntryFacts, path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, exists, err := regularFileState(path)
	if err != nil || !exists {
		return false
	}
	return putBackDetectedFor(facts, info)
}

func videoEntryFacts(entry models.VideoTrashEntry) softDeletedEntryFacts {
	return softDeletedEntryFacts{Mode: entry.Mode, DeletedBy: entry.DeletedBy, FileSize: entry.FileSize, FileModTime: entry.FileModTime,
		FileIdentity: entry.FileIdentity, DeleteBatchID: entry.DeleteBatchID, State: entry.State, TrashPath: entry.TrashPath}
}

func imageEntryFacts(entry models.ImageTrashEntry) softDeletedEntryFacts {
	return softDeletedEntryFacts{Mode: entry.Mode, DeletedBy: entry.DeletedBy, FileSize: entry.FileSize, FileModTime: entry.FileModTime,
		FileIdentity: entry.FileIdentity, DeleteBatchID: entry.DeleteBatchID, State: entry.State, TrashPath: entry.TrashPath}
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
	// TrashPath 只用于放回判定的「废纸篓那一份已不在」一项。
	TrashPath string
}

// decideSoftDeletedPath 按 §2.2 的表依次判定：
//
//  1. mode=missing 且 deleted_by=scanner → 自动恢复；
//  2. mode=record_only（新时代行，delete_batch_id 非空），且大小与 mtime 都没变 → 屏蔽；
//  3. mode=trash 且满足放回判定（putBackDetected，与列表、手动添加同一个条件）→ 访达放回，就地恢复；
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
			if putBackDetected(*entry, size, mtimeNS, identity) {
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
		entryFacts := videoEntryFacts(entry)
		facts = &entryFacts
	}
	switch decideSoftDeletedPath(facts, deleted.Size, info.Size(), info.ModTime().UnixNano(), stableFileIdentity(info)) {
	case softDeletedBlocked:
		return &deleted, fmt.Errorf("%w: %w", ErrVideoBlockedByUserDelete, ErrVideoExists), nil
	case softDeletedAutoRestore, softDeletedPutBack:
		// 自动恢复由 addScannedVideo、访达放回由 addVideoOrRestorePutBack 在建记录之前处理；
		// 走到这里说明那一步没有恢复（或并发改变了状态），不新建重复记录。
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
		entryFacts := imageEntryFacts(entry)
		facts = &entryFacts
	}
	switch decideSoftDeletedPath(facts, deleted.Size, info.Size(), info.ModTime().UnixNano(), stableFileIdentity(info)) {
	case softDeletedBlocked:
		return &deleted, fmt.Errorf("%w: %w", ErrImageBlockedByUserDelete, ErrImageExists), nil
	case softDeletedAutoRestore, softDeletedPutBack:
		return &deleted, ErrImageExists, nil
	}
	return nil, nil, nil
}
