package services

import (
	"errors"
	"fmt"
	"log"
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

// restorePutBackVideo 判断同路径的软删视频里，是否有一条的文件被用户放回了原路径
// （putBackDetected：trash 模式经访达「放回原处」，legacy_trash 旧行从旧版 trash/ 挪回，I-1），
// 有则只在数据库里恢复那一条原记录（原 ID、标签、人物），条目按既有恢复成功语义移除。
// 遍历同路径的全部软删行、取身份一致的那条，而不是只看最新一条（Minor 2）。
// 条目此前若因废纸篓里找不到文件被标成 file_gone，先条件更新回 deleted 再走既有恢复。
// 原路径用 Lstat 读取：是符号链接时一律不认定放回（m1）。legacy_trash 行按大小 + inode 认定放回后，条目记录了
// file_sha256 的再核对一次内容哈希，不一致不认定（legacyPutBackContentConfirmed，修复 I m-b）。
// 调用方持有路径读锁（扫描或 AddVideo）。
func (s *VideoService) restorePutBackVideo(path string) (*models.Video, bool, error) {
	info := originalRegularFile(path)
	if info == nil {
		return nil, false, nil
	}
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
		if result.RowsAffected == 0 || !putBackDetectedFor(videoEntryFacts(entry), info) ||
			!legacyPutBackContentConfirmed(videoEntryFacts(entry), entry.FileSHA256, path) {
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

// restoreImageIfPutBack 是 restorePutBackVideo 的图片版本（I3、Minor 2、I-1、m1）。
func (s *ImageService) restoreImageIfPutBack(path string) (bool, error) {
	info := originalRegularFile(path)
	if info == nil {
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
		if result.RowsAffected == 0 || !putBackDetectedFor(imageEntryFacts(entry), info) ||
			!legacyPutBackContentConfirmed(imageEntryFacts(entry), entry.FileSHA256, path) {
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

// putBackDetected 是「文件已放回原处」的唯一判定（扫描、手动添加、回收站列表、清除、移除记录与用量共用，
// Minor 2 / M9 / I-1）。条目状态必须是 deleted / file_gone，且：
//
//   - trash 模式：记录了大小、mtime、inode（canDetectPutBack），原路径上文件的三者（inode 不含设备号）与条目全部一致；
//   - legacy_trash（及回填前 file_moved=true 的旧行）：旧行没有可信的 mtime，只比大小与 inode（不含设备号），
//     两者都记录了才判定。
//
// 只凭原路径上的文件身份判定，不读废纸篓一侧（I-A）：同一 inode 就是当初删掉的那个文件，废纸篓里
// 同时还有一个名字（硬链接）也是同一个文件；读不到废纸篓（EPERM 等）不能反过来让扫描把它当新文件收录。
// 只有大小（+ mtime）一致而 inode 不同，是另一个文件，不得认定为放回。identity 必须取自原路径上的普通文件
// （Lstat）：原路径是符号链接时调用方传空，一律不认定（m1）。
func putBackDetected(facts softDeletedEntryFacts, size, mtimeNS int64, identity string) bool {
	if facts.State != trashStateDeleted && facts.State != models.TrashStateFileGone {
		return false
	}
	return entryFileAtOriginal(facts, size, mtimeNS, identity)
}

// entryFileAtOriginal 是 putBackDetected 的身份部分（不看条目状态）：原路径上的这个普通文件是不是条目当初删掉的
// 那个文件。恢复途中（state=restoring）也用它判定「文件已在原处」。
func entryFileAtOriginal(facts softDeletedEntryFacts, size, mtimeNS int64, identity string) bool {
	if identity == "" {
		return false
	}
	switch {
	case facts.Mode == models.TrashModeTrash:
		want := trashFileID{Size: facts.FileSize, ModTimeNS: facts.FileModTime, Identity: facts.FileIdentity}
		if !canDetectPutBack(facts.Mode, want) {
			return false
		}
		return want.Size == size && want.ModTimeNS == mtimeNS && identityInode(want.Identity) == identityInode(identity)
	case isLegacyTrashFacts(facts):
		// 已知边界：FAT / exFAT / SMB 等卷上的 inode 号不稳定、可能被复用（文件删掉后同一个号码分给新文件），
		// 大小恰好也相同时这里会把另一个文件误认成放回的原文件。恢复原记录之前由 legacyPutBackContentConfirmed
		// 再核对一次内容哈希（条目记录了 file_sha256 时，修复 I m-b）；没有记录哈希的旧行仍只能按大小 + inode 判定。
		return facts.FileSize != 0 && facts.FileIdentity != "" && facts.FileSize == size &&
			identityInode(facts.FileIdentity) == identityInode(identity)
	}
	return false
}

// isLegacyTrashFacts 报告条目是不是旧版同目录 trash/ 的条目：mode=legacy_trash，或回填之前 file_moved=true 的空 mode 旧行。
func isLegacyTrashFacts(facts softDeletedEntryFacts) bool {
	return facts.Mode == models.TrashModeLegacyTrash || (facts.Mode == "" && facts.FileMoved)
}

// legacyPutBackContentConfirmed 是 legacy_trash 行「按大小 + inode 认定放回」之后、恢复原记录之前的内容核对
// （修复 I m-b）：条目记录了 file_sha256（旧版删除时算过整文件哈希）时，对原路径上的文件重算一次，不一致（或读不了）
// 就不认定放回——那是 inode 被复用的另一个文件，或放回之后被改过的文件。没有记录哈希的条目、非 legacy 条目一律返回
// true（保持现状；trash 模式的放回判定另有 mtime，不需要哈希）。
//
// 只在「要恢复原记录」的路径上调用（扫描与手动添加的放回恢复、显式恢复），列表、清除、用量的放回判定不做整文件哈希。
func legacyPutBackContentConfirmed(facts softDeletedEntryFacts, sha, path string) bool {
	if !isLegacyTrashFacts(facts) || strings.TrimSpace(sha) == "" {
		return true
	}
	digest, err := fileSHA256Hex(path)
	return err == nil && digest == sha
}

// putBackDetectedFor 用原路径上文件的 FileInfo 做放回判定。info 必须来自 Lstat：符号链接、目录一律不认定（m1）。
func putBackDetectedFor(facts softDeletedEntryFacts, info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() {
		return false
	}
	return putBackDetected(facts, info.Size(), info.ModTime().UnixNano(), stableFileIdentity(info))
}

// putBackAtPath 读取 path 上的普通文件（Lstat，不跟随符号链接）并做放回判定；文件不在、读不到或不是普通文件时
// 返回 false。
func putBackAtPath(facts softDeletedEntryFacts, path string) bool {
	return putBackDetectedFor(facts, originalRegularFile(path))
}

// originalPathIdentity 返回原路径上普通文件的稳定身份（Lstat）；不是普通文件（例如符号链接）或读不到时返回空，
// 让放回判定一律不认定（m1）。
func originalPathIdentity(path string) string {
	info := originalRegularFile(path)
	if info == nil {
		return ""
	}
	return stableFileIdentity(info)
}

// putBackIdentity 返回同路径软删行判定（decideSoftDeletedPath）用的原路径身份：原路径上普通文件（Lstat）的稳定身份，
// 是符号链接或读不到时为空（m1）。legacy_trash 行按大小 + inode 会认定放回、但内容哈希与条目记录的 file_sha256 不一致时
// 同样返回空（修复 I m-b）：不认定放回，按新文件处理，而不是既不恢复也不收录。
func putBackIdentity(facts *softDeletedEntryFacts, sha, path string, info os.FileInfo) string {
	identity := originalPathIdentity(path)
	if facts == nil || identity == "" || info == nil {
		return identity
	}
	if putBackDetected(*facts, info.Size(), info.ModTime().UnixNano(), identity) && !legacyPutBackContentConfirmed(*facts, sha, path) {
		return ""
	}
	return identity
}

// restoringTrashEntityAt 查原路径 path 上有没有恢复中断（state=restoring）的条目，有则返回它引用的媒体 ID（修复 I m-c）。
// 按 state 索引过滤，restoring 行正常情况下为零。
func restoringTrashEntityAt(spec trashKindSpec, path string) (uint, bool, error) {
	var ids []uint
	if err := database.DB.Table(spec.table).
		Where("state = ? AND original_path = ?", trashStateRestoring, path).
		Order("id DESC").Limit(1).Pluck(spec.entityCol, &ids).Error; err != nil {
		return 0, false, err
	}
	if len(ids) == 0 {
		return 0, false, nil
	}
	return ids[0], true, nil
}

func videoEntryFacts(entry models.VideoTrashEntry) softDeletedEntryFacts {
	return softDeletedEntryFacts{Mode: entry.Mode, DeletedBy: entry.DeletedBy, FileSize: entry.FileSize, FileModTime: entry.FileModTime,
		FileIdentity: entry.FileIdentity, DeleteBatchID: entry.DeleteBatchID, State: entry.State, FileMoved: entry.FileMoved}
}

func imageEntryFacts(entry models.ImageTrashEntry) softDeletedEntryFacts {
	return softDeletedEntryFacts{Mode: entry.Mode, DeletedBy: entry.DeletedBy, FileSize: entry.FileSize, FileModTime: entry.FileModTime,
		FileIdentity: entry.FileIdentity, DeleteBatchID: entry.DeleteBatchID, State: entry.State, FileMoved: entry.FileMoved}
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
	// softDeletedPutBack：用户把废纸篓（或旧版 trash/）里的文件放回了原处（putBackDetected），
	// 应就地恢复原记录，而不是新建（I3、I-1）。
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
	// FileMoved 只用来认出回填之前（mode 为空）的旧版行：file_moved=true 即 legacy_trash。
	FileMoved bool
}

// decideSoftDeletedPath 按 §2.2 的表依次判定：
//
//  1. mode=missing 且 deleted_by=scanner → 自动恢复；
//  2. mode=record_only（新时代行，delete_batch_id 非空），且大小与 mtime 都没变 → 屏蔽；
//  3. mode 为 trash / legacy_trash（及回填前 file_moved=true 的旧行）且满足放回判定（putBackDetected，
//     与列表、手动添加同一个条件；legacy 行比大小 + inode，I-1）→ 放回原处，就地恢复；
//     其余情形 → 新建（原文件在废纸篓，这是新文件）；
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
		case models.TrashModeTrash, models.TrashModeLegacyTrash, "":
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
//
// 原路径上有一条恢复中断（state=restoring）的条目时一律跳过、不新建（修复 I m-c）：文件多半已经被这次恢复移回了
// 原处，新建一条记录会让启动对账续做恢复时永远撞上 path_occupied。交给启动对账（ReconcileTrashEntries）完成恢复。
func softDeletedVideoPathSkip(path string, info os.FileInfo) (row *models.Video, skip error, err error) {
	if restoring, found, err := restoringTrashEntityAt(videoTrashKind, path); err != nil {
		return nil, nil, err
	} else if found {
		var deleted models.Video
		if err := database.DB.Unscoped().Where("id = ?", restoring).Limit(1).Find(&deleted).Error; err != nil {
			return nil, nil, err
		}
		log.Printf("原路径上有恢复中断的回收站条目，交给启动对账，不新建记录 video_id=%d", restoring)
		return &deleted, ErrVideoExists, nil
	}
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
	// 放回判定的身份取自原路径上的普通文件（Lstat）：原路径是符号链接时不认定放回（m1）。
	switch decideSoftDeletedPath(facts, deleted.Size, info.Size(), info.ModTime().UnixNano(), putBackIdentity(facts, entry.FileSHA256, path, info)) {
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
//
// 恢复中断（restoring）的条目同视频侧：一律跳过、交给启动对账（修复 I m-c）。
func softDeletedImagePathSkip(path string, info os.FileInfo) (row *models.Image, skip error, err error) {
	if restoring, found, err := restoringTrashEntityAt(imageTrashKind, path); err != nil {
		return nil, nil, err
	} else if found {
		var deleted models.Image
		if err := database.DB.Unscoped().Where("id = ?", restoring).Limit(1).Find(&deleted).Error; err != nil {
			return nil, nil, err
		}
		log.Printf("原路径上有恢复中断的图片回收站条目，交给启动对账，不新建记录 image_id=%d", restoring)
		return &deleted, ErrImageExists, nil
	}
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
	// 放回判定的身份取自原路径上的普通文件（Lstat）：原路径是符号链接时不认定放回（m1）。
	switch decideSoftDeletedPath(facts, deleted.Size, info.Size(), info.ModTime().UnixNano(), putBackIdentity(facts, entry.FileSHA256, path, info)) {
	case softDeletedBlocked:
		return &deleted, fmt.Errorf("%w: %w", ErrImageBlockedByUserDelete, ErrImageExists), nil
	case softDeletedAutoRestore, softDeletedPutBack:
		return &deleted, ErrImageExists, nil
	}
	return nil, nil, nil
}
