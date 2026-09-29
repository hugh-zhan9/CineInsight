package services

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// DefaultTrashDirName 是旧版同目录回收站目录名。新的删除不再创建它（D-PC01）；
// 它只用于识别历史遗留的 legacy_trash 条目与扫描跳过规则。
const DefaultTrashDirName = "trash"

var ErrTrashTargetExists = errors.New("回收站目标已存在")

// 回收站接口的哨兵错误。文案面向用户，不含绝对路径（G-3）。
var (
	// ErrTrashPathOccupied：恢复时原路径已有活跃记录或文件（D-PC01 的 path_occupied）。
	ErrTrashPathOccupied = errors.New("原位置已收录了新文件")
	// ErrTrashVolumeOffline：文件所在的扫描根当前不可访问。删除到废纸篓时不降级为只删记录。
	ErrTrashVolumeOffline = errors.New("文件所在磁盘当前不可访问，未做任何改动")
	// ErrTrashNotPurgeable：只删记录或文件本就不在的条目没有可清除的文件。
	ErrTrashNotPurgeable = errors.New("该条目没有可清除的废纸篓文件")
	// ErrTrashFileGone：废纸篓里的文件已被外部清除，只能移除记录。
	ErrTrashFileGone = errors.New("废纸篓里的文件已不存在")
	// ErrPermanentDeleteNotActive：永久删除只接受仍在片库中（未软删）的记录（I5）。
	ErrPermanentDeleteNotActive = errors.New("只能永久删除仍在库中的记录")
	// ErrPermanentDeleteHasTrashEntry：已有回收站条目的记录要在回收站里清除，不能绕过条目直接永久删除（I5）。
	ErrPermanentDeleteHasTrashEntry = errors.New("该记录已有回收站条目，请在回收站中清除")
	// ErrTrashEntryNotFound：回收站条目不存在。墓碑（trashStateRemoved）对回收站的任何操作（含恢复）一律按不存在
	// 处理（修复 L m6）。返回的错误文案为「回收站条目不存在: <id>」，结果码为 error；errors.Is(err, gorm.ErrRecordNotFound)
	// 同样成立，手机端撤销删除等既有调用方按「不存在」映射。
	ErrTrashEntryNotFound = errors.New("回收站条目不存在")
	// ErrTrashPutBack：文件已被放回原处（原路径上的文件身份与条目一致）。清除或移除记录只会删掉记录而
	// 文件还在原地，应改用恢复（M2）。结果码为 not_purgeable。
	ErrTrashPutBack = errors.New("文件已被放回原处，请改用恢复")
	// ErrTrashOriginalNotRegular：原位置上是符号链接（或不是普通文件），恢复不会跟随它，也不会覆盖它（m1）。
	// 结果码为 path_occupied。
	ErrTrashOriginalNotRegular = errors.New("原位置是符号链接或不是普通文件，未做任何改动")
	// ErrTrashRecordNotRemovable：条目还停在中断的删除或恢复里（pending_move / restoring / rollback），
	// 「仍然移除记录」不处理这类条目（m3）。结果码为 not_purgeable。
	ErrTrashRecordNotRemovable = errors.New("该条目的删除或恢复尚未完成，不能移除记录")
	// ErrTrashForceRemoveUnconfirmed：「仍然移除记录」没有收到确认文案（m3）。整批拒绝，不处理任何条目。
	ErrTrashForceRemoveUnconfirmed = errors.New("请输入「" + TrashForceRemoveConfirmText + "」确认")
	// ErrTrashForceRemoveModeNotAllowed：「仍然移除记录」只对移到废纸篓（trash）、旧版回收站（legacy_trash）与
	// 扫描发现缺失（missing）的条目开放（修复 I m-e）。「只删记录」（record_only）留下的是按文件身份的屏蔽，
	// 移除记录会让屏蔽静默消失，要解除屏蔽应改用恢复（「允许重新收录」）。结果码为 not_purgeable。
	ErrTrashForceRemoveModeNotAllowed = errors.New("「只删记录」留下的屏蔽不能用「仍然移除记录」移除；如需重新收录，请使用「允许重新收录」")
	// ErrTrashLocationPermissionDenied：解析文件所在位置（符号链接、挂载点、扫描根）时遇到 EPERM / EACCES（m3）。
	// 这不是「磁盘未连接」，结果码为 permission_denied；errors.Is(err, ErrTrashPermissionDenied) 成立，
	// 手机端等既有调用方沿用同一映射。
	ErrTrashLocationPermissionDenied error = trashLocationPermissionError{}
)

// TrashForceRemoveConfirmText 是「仍然移除记录（不动文件）」必须原样传回的确认文案（m3）。
const TrashForceRemoveConfirmText = "移除记录"

// trashStateRemoved 是 legacy 条目（legacy_trash，或回填之前 file_moved=true 的空 mode 旧行）被「仍然移除记录」、
// 「移除记录」（claimed_by_active 或 file_gone）或「清除」（文件删掉之后）移除后的墓碑状态（修复 I I-A；清除与
// file_gone 的移除记录见修复 K）。旧版 trash/ 目录是本应用自己的目录：条目一旦硬删，那个目录就可能失去登记，里面残留的
// 文件（还没清掉的原文件、与原路径同 inode 的另一个名字、同目录里其他旧版删除留下的文件）会被下一轮扫描当成新文件收录。
// 墓碑保留条目（含 trash_path），让 loadLegacyTrashDirs 继续把该目录算作「已登记」；媒体记录保持软删、不可见，
// 也不再挂任何标签。
//
// 墓碑不出现在任何列表、计数、用量或待处理汇总里：回收站接口一律把它当作不存在（loadTrashRow、ListTrashEntries），
// 各统计只数 deleted / file_gone，不会数到它。它没有出口，也不可恢复。
const trashStateRemoved = "removed"

type trashLocationPermissionError struct{}

func (trashLocationPermissionError) Error() string {
	return "没有权限访问文件所在位置，未做任何改动"
}

func (trashLocationPermissionError) Is(target error) bool { return target == ErrTrashPermissionDenied }

// trashEntryNotFoundError 是 ErrTrashEntryNotFound 带条目 ID 的形式（修复 L m6）。
type trashEntryNotFoundError struct{ id uint }

func (e trashEntryNotFoundError) Error() string {
	return fmt.Sprintf("%s: %d", ErrTrashEntryNotFound.Error(), e.id)
}

func (trashEntryNotFoundError) Is(target error) bool {
	return target == ErrTrashEntryNotFound || target == gorm.ErrRecordNotFound
}

// errTrashEntryNotFound 返回「回收站条目不存在」：条目不存在，或是墓碑（修复 L m6）。
func errTrashEntryNotFound(id uint) error { return trashEntryNotFoundError{id: id} }

// errTrashLocationUnknown：系统废纸篓报告移动成功，但没有返回文件在废纸篓里的位置。
// 文件已经不在原处，调用方必须保留 pending_move 条目，走崩溃恢复的「按身份查找」，不能撤销条目。
var errTrashLocationUnknown = errors.New("移到废纸篓成功，但系统没有返回废纸篓中的位置")

// errTrashEntryStateChanged：条件更新影响行数为 0，条目状态已被他方改变。
var errTrashEntryStateChanged = errors.New("回收站条目状态已变化")

// 删除与回收站接口的单项结果码（D-PC01 / D-PC04）。
const (
	TrashResultOK                = "ok"
	TrashResultTrashUnsupported  = "trash_unsupported"
	TrashResultFileMissing       = "file_missing"
	TrashResultPermissionDenied  = "permission_denied"
	TrashResultVolumeOffline     = "volume_offline"
	TrashResultError             = "error"
	TrashResultCancelled         = "cancelled"
	TrashResultPathOccupied      = "path_occupied"
	TrashResultIdentityMismatch  = "identity_mismatch"
	TrashResultNotPurgeable      = "not_purgeable"
	TrashResultFileGone          = "file_gone"
	trashKindVideo               = "video"
	trashKindImage               = "image"
	trashUnknownLocationMessage  = "文件位置未知"
	trashRecoveryFileGoneMessage = "废纸篓里的文件已被清除"
	trashReplacedMessage         = "废纸篓中的文件已被替换"
)

// systemTrashMove 是移入系统废纸篓的入口，单测里替换成替身（darwin+cgo 的真实实现见
// system_trash_darwin.go）。
var systemTrashMove = moveToSystemTrash

// trashLookupDirs 返回某个原路径对应的「用户废纸篓顶层目录」候选，崩溃恢复按文件身份在这些
// 目录里找。单测替换成临时目录。
var trashLookupDirs = defaultTrashLookupDirs

// originalFileState 读取原路径一侧的状态，不跟随符号链接（m1）。原路径上是符号链接（或目录等非普通文件）
// 时返回 ErrTrashOriginalNotRegular：它可能正指向废纸篓（或旧版 trash/）里的那个文件，跟随它会把「废纸篓里的
// 文件」误当成「已在原处」，随后清理残留名字时就会删掉真正的文件。
func originalFileState(path string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, true, ErrTrashOriginalNotRegular
	}
	return info, true, nil
}

// originalRegularFile 返回原路径上的普通文件（Lstat）；不存在、读不到、是符号链接或不是普通文件时返回 nil。
// 放回判定只认这样的文件（m1）。
func originalRegularFile(path string) os.FileInfo {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	info, exists, err := originalFileState(path)
	if err != nil || !exists {
		return nil
	}
	return info
}

// hardLinkedRegularNames 报告 a 与 b 是否是同一个普通文件的两个名字（硬链接，m1）：两侧都用 Lstat 读取、
// 不跟随符号链接，都必须是普通文件、inode 相同，且硬链接数 ≥ 2。只有这时删掉其中一个名字才不会丢掉文件内容；
// 读不到、是符号链接、读不到硬链接数时一律返回 false（调用方不得删除）。
//
// a 与 b 清理后是同一个路径时直接返回 false（修复 I m-f）：那是同一个名字，硬链接数 ≥ 2 只说明别处还有名字，
// 调用方删掉「其中一个」就是删掉这唯一的一个。
func hardLinkedRegularNames(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return false
	}
	infoA, errA := os.Lstat(a)
	infoB, errB := os.Lstat(b)
	if errA != nil || errB != nil || !infoA.Mode().IsRegular() || !infoB.Mode().IsRegular() || !os.SameFile(infoA, infoB) {
		return false
	}
	links, ok := fileLinkCount(infoA)
	return ok && links >= 2
}

// originalSymlinkToTrashFile 报告原路径 P 是不是指向废纸篓（或旧版 trash/）文件 T 的符号链接（修复 I m-a）：
// Lstat(P) 是符号链接，且 os.Stat(P) 与 os.Stat(T) 是同一个文件。这时清除 T 就是删掉用户经 P 仍在使用的那唯一一份
// 内容，清除必须拒绝（path_occupied）；恢复同样因原位置不是普通文件而拒绝（ErrTrashOriginalNotRegular）。
// 任何一侧读不到时返回 false。
func originalSymlinkToTrashFile(originalPath, trashPath string) bool {
	if strings.TrimSpace(originalPath) == "" || strings.TrimSpace(trashPath) == "" {
		return false
	}
	linkInfo, err := os.Lstat(originalPath)
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		return false
	}
	target, err := os.Stat(originalPath)
	if err != nil {
		return false
	}
	trash, err := os.Stat(trashPath)
	return err == nil && os.SameFile(target, trash)
}

// errLegacyResidueNotHardLink：中断对账要清掉旧版 trash/ 与原路径里「同一个文件的另一个名字」，但两侧并不是
// 同一个普通文件的两个硬链接（例如原路径是指向 trash/ 里文件的符号链接）。删掉任何一侧都可能丢文件，什么都不做（m1）。
var errLegacyResidueNotHardLink = errors.New("原路径与旧版回收站里的文件不是同一文件的两个名字（可能是符号链接），未删除任何文件")

type TrashService struct {
	TrashDirName string
	now          func() time.Time
	// videos / images 只在回收站接口（列表、恢复、清除……）里使用；纯文件移动不需要。
	videos *VideoService
	images *ImageService
}

func NewTrashService() *TrashService {
	return &TrashService{
		TrashDirName: DefaultTrashDirName,
		now:          time.Now,
	}
}

// NewTrashCenter 构造带媒体服务的 TrashService，供 App 层的回收站接口使用。
func NewTrashCenter(videos *VideoService, images *ImageService) *TrashService {
	service := NewTrashService()
	service.videos = videos
	service.images = images
	return service
}

// MoveToTrash 把文件移入系统废纸篓，返回它在废纸篓里的实际路径。不再创建同目录 trash/。
func (s *TrashService) MoveToTrash(srcPath string) (string, error) {
	srcPath = filepath.Clean(strings.TrimSpace(srcPath))
	if srcPath == "." {
		return "", fmt.Errorf("源文件路径为空")
	}

	info, err := os.Stat(srcPath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("不支持移动目录到回收站: %s", srcPath)
	}
	// 只有确实登记过的回收站位置（系统废纸篓目录、有 legacy_trash 条目的旧版目录）才原样放行；
	// 仅凭启发式认出的旧版 trash/ 目录只用于扫描跳过，不影响删除（I-3）。
	if isRecordedTrashPath(srcPath) {
		return srcPath, nil
	}
	return systemTrashMove(srcPath)
}

// mapSystemTrashError 把 trashItemAtURL 的 NSError 映射成本包的错误（详细设计 §2.1 表）。
// 平台无关，方便单测；path 只用来从文案里擦掉。
func mapSystemTrashError(isCocoa bool, code, posix int, message, path string) error {
	if isCocoa {
		switch code {
		case 4, 260: // NSFileNoSuchFileError / NSFileReadNoSuchFileError
			return fmt.Errorf("文件不存在: %w", os.ErrNotExist)
		case 3328: // NSFeatureUnsupportedError
			return ErrTrashUnsupportedVolume
		case 513: // NSFileWriteNoPermissionError
			return ErrTrashPermissionDenied
		}
		if posix == int(syscall.ENOTSUP) || posix == int(syscall.EXDEV) {
			return ErrTrashUnsupportedVolume
		}
	}
	return fmt.Errorf("移到废纸篓失败: %s", scrubPlaybackProxyPaths(scrubFacePaths(message, path)))
}

// trashFileID 是删除时记录的文件身份。trash / record_only 模式只用它，不再整文件 SHA-256。
type trashFileID struct {
	Size      int64
	ModTimeNS int64
	Identity  string
}

func trashFileIDOf(info os.FileInfo) trashFileID {
	return trashFileID{Size: info.Size(), ModTimeNS: info.ModTime().UnixNano(), Identity: stableFileIdentity(info)}
}

// empty 表示删除时文件已不在，没有任何可核对的身份。
func (id trashFileID) empty() bool {
	return id.Size == 0 && id.ModTimeNS == 0 && id.Identity == ""
}

// identityInode 取 stableFileIdentity 记录值（"dev:ino"）里的 inode 部分。
// 设备号不参与身份核对：macOS 外置盘重插后 dev 会变，而 inode、大小、mtime 不变（I2）。
func identityInode(identity string) string {
	if index := strings.LastIndex(identity, ":"); index >= 0 {
		return identity[index+1:]
	}
	return identity
}

// sameFileInode 报告 info 的 inode 是否等于记录的身份。任何一侧读不到身份都算不符。
func sameFileInode(recorded string, info os.FileInfo) bool {
	current := stableFileIdentity(info)
	if recorded == "" || current == "" {
		return false
	}
	inode := identityInode(recorded)
	return inode != "" && inode == identityInode(current)
}

// strictMatch 要求记录了的每一项都相等：大小、mtime、inode（不含设备号）。
// 系统废纸篓是同卷重命名，三者都不会变；任何一项不符都说明这不是当初删掉的那个文件。
func (id trashFileID) strictMatch(info os.FileInfo) bool {
	if info == nil || id.empty() {
		return false
	}
	if id.Size != 0 && info.Size() != id.Size {
		return false
	}
	if id.ModTimeNS != 0 && info.ModTime().UnixNano() != id.ModTimeNS {
		return false
	}
	if id.Identity != "" && !sameFileInode(id.Identity, info) {
		return false
	}
	return true
}

// legacyFileMatches 是 legacy_trash 旧行的校验：大小一致，强身份命中即通过，否则回退 SHA-256。
func legacyTrashFileMatches(path string, info os.FileInfo, id trashFileID, sha string) bool {
	if info == nil {
		return false
	}
	if id.Size != 0 && info.Size() != id.Size {
		return false
	}
	if id.Identity != "" && sameFileInode(id.Identity, info) {
		return true
	}
	if sha == "" {
		return false
	}
	digest, err := fileSHA256Hex(path)
	return err == nil && digest == sha
}

// isLegacyTrashMode 报告条目是否走旧版语义：mode 为 legacy_trash，或迁移回填之前的空 mode。
func isLegacyTrashMode(mode string) bool {
	return mode == "" || mode == "legacy_trash"
}

// legacyRestoreResidue 报告恢复时旧版 trash/ 里是否留着与原路径同一个文件的另一个名字（旧版恢复在 link 之后、
// remove 之前中断留下的硬链接，或用户以硬链接放回）：只对 legacy_trash 旧行（及回填前的空 mode 行）、file_moved=true、
// 两侧都是普通文件、同 inode 且硬链接数 ≥ 2 时成立（hardLinkedRegularNames，m1）。系统废纸篓里的名字一律不算（M1）。
func legacyRestoreResidue(mode string, fileMoved bool, originalPath, trashPath string) bool {
	return isLegacyTrashMode(mode) && fileMoved && strings.TrimSpace(trashPath) != "" && hardLinkedRegularNames(originalPath, trashPath)
}

// removeLegacyTrashLinkAfterRestore 在恢复事务提交之后，清掉旧版 trash/ 里与原路径同一个文件的残留名字
// （legacyRestoreResidue）。只对 legacy_trash 旧行做：那是本应用自己的 trash/ 目录，残留名字失去登记后会被扫描
// 当成新文件收录；系统废纸篓里的名字一律不动（M1）。在提交之后做，恢复失败时两个名字都保持原样。
//
// 只在原路径是普通文件、与残留名字同 inode 且硬链接数 ≥ 2 时才删（hardLinkedRegularNames，m1）：原路径是指向
// trash/ 里文件的符号链接时，跟随链接的 Stat 也会报「同一个文件」，删掉残留名字就是永久删除。
// 没有要清的残留、或已清掉（含名字已不在）时返回 nil；删除失败时返回错误，调用方据此保留墓碑（修复 L m5）。
func removeLegacyTrashLinkAfterRestore(mode string, fileMoved bool, originalPath, trashPath string) error {
	if !legacyRestoreResidue(mode, fileMoved, originalPath, trashPath) {
		return nil
	}
	if err := os.Remove(trashPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// pathlessError 去掉 *os.PathError 里的路径，只留底层的系统错误（EACCES / EIO……），让拼进面向用户的文案里不带绝对路径
// （G-3）；errors.Is(err, os.ErrPermission) 等判定照常成立。不是 PathError 时原样返回。
func pathlessError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

// errTrashRestoreResidueRemains：恢复成功之后，旧版 trash/ 里与原文件同一个文件的另一个名字没能清掉（修复 L m5）。
// 条目保留为墓碑、目录继续登记；再次删除或永久删除这条记录之前要先清掉它。文案不含路径。
var errTrashRestoreResidueRemains = errors.New("上次恢复后，同目录旧版回收站文件夹（trash）里还留着这个文件的另一个名字，没能清理；请检查该文件夹的权限后重试，未做任何改动")

// retireRestoredEntryAsTombstoneTx 是恢复事务里「条目从回收站拿掉」的另一种写法（修复 L m5）：旧版 trash/ 里还有要在
// 提交之后清理的残留名字（legacyRestoreResidue）时，条目不硬删，而是用条件更新（WHERE state='restoring'）改为墓碑，
// 保留 trash_path，让 loadLegacyTrashDirs 继续把那个目录算作「已登记」。提交之后清理成功才由
// settleRestoredTrashTombstone 删掉墓碑；进程在两者之间退出、或清理失败时墓碑保留，残留名字不会被扫描当成新文件收录。
func retireRestoredEntryAsTombstoneTx(tx *gorm.DB, spec trashKindSpec, entryID uint) error {
	result := tx.Table(spec.table).
		Where("id = ? AND state = ?", entryID, trashStateRestoring).
		Updates(map[string]interface{}{"state": trashStateRemoved, "last_error": "", "updated_at": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: %d", errTrashEntryStateChanged, entryID)
	}
	return nil
}

// settleRestoredTrashTombstone 收尾一条「记录已恢复（活跃）、条目是墓碑」的行（修复 L m5）：先补做残留名字的清理
// （removeLegacyTrashLinkAfterRestore），残留名字已不在时才删掉墓碑（条件删除 WHERE state='removed'，只删条目、
// 不动记录与文件）；删不掉（权限等）或名字还在（不再是原文件的另一个名字，不能删）时墓碑保留，返回
// errTrashRestoreResidueRemains。恢复提交之后调用；再次删除、永久删除这条记录之前也先调用它（记录上挂着墓碑时
// 不能再建新条目，video_id / image_id 唯一）。
func settleRestoredTrashTombstone(spec trashKindSpec, entryID uint, mode string, fileMoved bool, originalPath, trashPath string) error {
	if err := removeLegacyTrashLinkAfterRestore(mode, fileMoved, originalPath, trashPath); err != nil {
		return fmt.Errorf("%w: %w", errTrashRestoreResidueRemains, pathlessError(err))
	}
	if strings.TrimSpace(trashPath) != "" {
		if _, err := os.Lstat(trashPath); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return fmt.Errorf("%w: %w", errTrashRestoreResidueRemains, pathlessError(err))
			}
			return errTrashRestoreResidueRemains
		}
	}
	if err := database.DB.Exec("DELETE FROM "+spec.table+" WHERE id = ? AND state = ?", entryID, trashStateRemoved).Error; err != nil {
		return fmt.Errorf("清理回收站墓碑失败: %w", err)
	}
	return nil
}

// finishRestoredLegacyResidue 是恢复提交之后的收尾（修复 L m5）：事务里留了墓碑（residue=true）时调用
// settleRestoredTrashTombstone；失败只记日志——恢复本身已经完成，墓碑保留，旧版 trash/ 目录继续登记。
func finishRestoredLegacyResidue(spec trashKindSpec, residue bool, entryID uint, mode string, fileMoved bool, originalPath, trashPath string) {
	if !residue {
		return
	}
	if err := settleRestoredTrashTombstone(spec, entryID, mode, fileMoved, originalPath, trashPath); err != nil {
		log.Printf("恢复后清理旧版回收站残留名字失败，条目保留为墓碑 kind=%s entry=%d err=%v", spec.kind, entryID, err)
	}
}

// RestoreFromTrashVerified 在恢复前核对废纸篓文件的身份，不符返回 ErrTrashIdentityMismatch。
func (s *TrashService) RestoreFromTrashVerified(trashPath string, targetPath string, want trashFileID) error {
	info, err := os.Stat(strings.TrimSpace(trashPath))
	if err != nil {
		return fmt.Errorf("检查回收站文件失败: %w", err)
	}
	if !want.strictMatch(info) {
		return ErrTrashIdentityMismatch
	}
	return s.RestoreFromTrash(trashPath, targetPath)
}

// pendingTrashLocation 是 pending_move 条目里文件实际所在的位置。
type pendingTrashLocation int

const (
	pendingFileAtOriginal pendingTrashLocation = iota
	pendingFileInTrash
	pendingFileUnknown
)

// resolvePendingTrashMove 判定一条 mode=trash 的 pending_move 条目里，文件现在在哪（D-PC01 崩溃恢复）。
//
// 顺序：原路径仍在且身份一致 → 没移动过；条目里已记了 trash_path 且那里的文件身份一致 → 已移入；
// 否则在用户废纸篓顶层按稳定身份找（同卷重命名 inode 不变）；都找不到 → 位置未知。
func resolvePendingTrashMove(originalPath, trashPath string, id trashFileID) (pendingTrashLocation, string, error) {
	originalInfo, originalExists, err := regularFileState(originalPath)
	if err != nil {
		return pendingFileUnknown, "", err
	}
	if originalExists && id.strictMatch(originalInfo) {
		return pendingFileAtOriginal, "", nil
	}
	if strings.TrimSpace(trashPath) != "" {
		trashInfo, trashExists, err := regularFileState(trashPath)
		if err != nil {
			return pendingFileUnknown, "", err
		}
		if trashExists && id.strictMatch(trashInfo) {
			return pendingFileInTrash, trashPath, nil
		}
	}
	if found := findInUserTrash(originalPath, id); found != "" {
		return pendingFileInTrash, found, nil
	}
	return pendingFileUnknown, "", nil
}

// findInUserTrash 只看废纸篓顶层，不递归：系统把刚移入的文件直接放在顶层。
func findInUserTrash(originalPath string, id trashFileID) string {
	if id.empty() {
		return ""
	}
	for _, dir := range trashLookupDirs(originalPath) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if id.strictMatch(info) {
				return filepath.Join(dir, entry.Name())
			}
		}
	}
	return ""
}

// defaultTrashLookupDirs 返回 ~/.Trash，以及原路径所在外置卷上的 <卷根>/.Trashes/<uid>。
func defaultTrashLookupDirs(originalPath string) []string {
	dirs := make([]string, 0, 2)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".Trash"))
	}
	if root := volumeRootOf(originalPath); root != "" && root != string(os.PathSeparator) {
		dirs = append(dirs, filepath.Join(root, ".Trashes", strconv.Itoa(os.Getuid())))
	}
	return dirs
}

// volumeRootOf 沿父目录向上走，直到再往上一层就换了设备。读不到设备号时返回空。
func volumeRootOf(path string) string {
	deviceOf := func(p string) string {
		info, err := os.Stat(p)
		if err != nil {
			return ""
		}
		id := stableFileIdentity(info)
		if id == "" {
			return ""
		}
		return strings.SplitN(id, ":", 2)[0]
	}
	current := filepath.Dir(filepath.Clean(path))
	for current != "" {
		device := deviceOf(current)
		if device == "" {
			return ""
		}
		parent := filepath.Dir(current)
		if parent == current || deviceOf(parent) != device {
			return current
		}
		current = parent
	}
	return ""
}

// newDeleteBatchID 生成同一次删除操作共享的 32 位十六进制标识。
func newDeleteBatchID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 在受支持平台上不会失败；失败时用时间纳秒兜底，仍是 32 位十六进制。
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// TrashTargetPath 返回旧版同目录回收站路径（仅供 legacy_trash 旧行的对账续做使用，新删除不再调用）。
// 原说明：返回指定尝试次数对应的确定性回收站路径。
func (s *TrashService) TrashTargetPath(srcPath string, attempt int) string {
	srcPath = filepath.Clean(strings.TrimSpace(srcPath))
	trashDir := filepath.Join(filepath.Dir(srcPath), s.TrashDirName)
	baseName := filepath.Base(srcPath)
	if attempt <= 0 {
		return filepath.Join(trashDir, baseName)
	}
	ext := filepath.Ext(baseName)
	name := strings.TrimSuffix(baseName, ext)
	suffix := s.now().Format("20060102150405")
	if attempt > 1 {
		suffix = fmt.Sprintf("%s_%d", suffix, attempt)
	}
	return filepath.Join(trashDir, fmt.Sprintf("%s_%s%s", name, suffix, ext))
}

// MoveToTrashAt 将文件移动到指定路径，目标已存在时绝不覆盖。
func (s *TrashService) MoveToTrashAt(srcPath string, targetPath string) error {
	srcPath = filepath.Clean(strings.TrimSpace(srcPath))
	targetPath = filepath.Clean(strings.TrimSpace(targetPath))
	if srcPath == "." || targetPath == "." {
		return fmt.Errorf("回收站路径为空")
	}
	if srcPath == targetPath {
		return fmt.Errorf("源路径与回收站路径相同: %s", srcPath)
	}
	info, err := os.Stat(srcPath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("不支持移动目录到回收站: %s", srcPath)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return err
	}

	if err := os.Link(srcPath, targetPath); err == nil {
		if err := os.Remove(srcPath); err != nil {
			_ = os.Remove(targetPath)
			return err
		}
		return nil
	} else if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%w: %s", ErrTrashTargetExists, targetPath)
	}

	if err := s.copyToExclusiveAndDelete(srcPath, targetPath, info); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s", ErrTrashTargetExists, targetPath)
		}
		return err
	}
	return nil
}

// RestoreFromTrash 在不覆盖现有目标的前提下恢复回收站文件。
func (s *TrashService) RestoreFromTrash(trashPath string, targetPath string) error {
	trashPath = filepath.Clean(strings.TrimSpace(trashPath))
	targetPath = filepath.Clean(strings.TrimSpace(targetPath))
	if trashPath == "." || targetPath == "." {
		return fmt.Errorf("恢复路径为空")
	}
	if trashPath == targetPath {
		return fmt.Errorf("回收站路径与恢复路径相同: %s", trashPath)
	}

	info, err := os.Stat(trashPath)
	if err != nil {
		return fmt.Errorf("检查回收站文件失败: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("不支持恢复目录: %s", trashPath)
	}
	if _, err := os.Stat(targetPath); err == nil {
		return fmt.Errorf("恢复目标已存在: %s", targetPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查恢复目标失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("创建恢复目录失败: %w", err)
	}

	if err := os.Link(trashPath, targetPath); err == nil {
		if err := os.Remove(trashPath); err != nil {
			_ = os.Remove(targetPath)
			return fmt.Errorf("清理回收站源文件失败: %w", err)
		}
		return nil
	}
	if err := s.copyToExclusiveAndDelete(trashPath, targetPath, info); err != nil {
		return fmt.Errorf("恢复回收站文件失败: %w", err)
	}
	return nil
}

func (s *TrashService) copyToExclusiveAndDelete(srcPath string, targetPath string, info os.FileInfo) error {
	source, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer source.Close()

	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	removeTarget := true
	defer func() {
		_ = target.Close()
		if removeTarget {
			_ = os.Remove(targetPath)
		}
	}()

	copiedHash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(target, copiedHash), source); err != nil {
		return err
	}
	if err := target.Sync(); err != nil {
		return err
	}
	if err := target.Close(); err != nil {
		return err
	}
	if err := os.Chtimes(targetPath, info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	currentInfo, err := source.Stat()
	if err != nil {
		return err
	}
	if currentInfo.Size() != info.Size() || !currentInfo.ModTime().Equal(info.ModTime()) {
		return fmt.Errorf("复制期间源文件发生变化")
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return err
	}
	currentHash := sha256.New()
	if _, err := io.Copy(currentHash, source); err != nil {
		return err
	}
	verifiedInfo, err := source.Stat()
	if err != nil {
		return err
	}
	if verifiedInfo.Size() != info.Size() || !verifiedInfo.ModTime().Equal(info.ModTime()) || !bytes.Equal(currentHash.Sum(nil), copiedHash.Sum(nil)) {
		return fmt.Errorf("复制校验失败，源文件发生变化")
	}
	if err := source.Close(); err != nil {
		return err
	}
	if err := os.Remove(srcPath); err != nil {
		return err
	}
	removeTarget = false
	return nil
}

// ===== 批量结果、进度与取消（D-PC04 / D-PC51） =====

// BatchItemResult 是一次批量操作里单个条目的结果。
type BatchItemResult struct {
	ID      uint   `json:"id"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// BatchResult 是删除、永久删除、恢复、清除共用的结果结构。BatchID 是本次删除操作的
// delete_batch_id（恢复、清除时为空或沿用被恢复的批次）。
type BatchResult struct {
	BatchID   string            `json:"batch_id"`
	Requested int               `json:"requested"`
	Succeeded int               `json:"succeeded"`
	Failed    int               `json:"failed"`
	Cancelled int               `json:"cancelled"`
	Items     []BatchItemResult `json:"items"`
}

func newBatchResult(requested int, batchID string) *BatchResult {
	return &BatchResult{BatchID: batchID, Requested: requested, Items: make([]BatchItemResult, 0, requested)}
}

// addCode 记录一个已知结果码的条目。ok / file_missing 计成功，cancelled 计取消，其余计失败。
func (r *BatchResult) addCode(id uint, code, message string) {
	switch code {
	case TrashResultOK, TrashResultFileMissing:
		r.Succeeded++
	case TrashResultCancelled:
		r.Cancelled++
	default:
		r.Failed++
	}
	r.Items = append(r.Items, BatchItemResult{ID: id, Code: code, Message: message})
}

// addOutcome 记录 (code, err) 形式的结果：err 为空时用 code（空则 ok），否则由错误推出结果码。
func (r *BatchResult) addOutcome(id uint, code string, err error) {
	if err == nil {
		if code == "" {
			code = TrashResultOK
		}
		r.addCode(id, code, "")
		return
	}
	r.addCode(id, trashResultCodeForError(err), scrubPlaybackProxyPaths(err.Error()))
}

// trashResultCodeForError 把删除、恢复、清除路径上的错误映射成单项结果码。
func trashResultCodeForError(err error) string {
	switch {
	case errors.Is(err, ErrTrashUnsupportedVolume):
		return TrashResultTrashUnsupported
	case errors.Is(err, ErrTrashPermissionDenied):
		return TrashResultPermissionDenied
	case errors.Is(err, ErrTrashVolumeOffline):
		return TrashResultVolumeOffline
	case errors.Is(err, ErrTrashPathOccupied), errors.Is(err, ErrTrashOriginalNotRegular):
		return TrashResultPathOccupied
	case errors.Is(err, ErrTrashIdentityMismatch):
		return TrashResultIdentityMismatch
	case errors.Is(err, ErrTrashNotPurgeable), errors.Is(err, ErrTrashPutBack), errors.Is(err, ErrTrashRecordNotRemovable),
		errors.Is(err, ErrTrashForceRemoveModeNotAllowed),
		errors.Is(err, ErrPermanentDeleteNotActive), errors.Is(err, ErrPermanentDeleteHasTrashEntry):
		return TrashResultNotPurgeable
	case errors.Is(err, ErrTrashFileGone):
		return TrashResultFileGone
	case errors.Is(err, os.ErrPermission):
		// 读废纸篓或原路径时的 EPERM / EACCES（例如没有「完全磁盘访问」）：是权限问题，不是一般错误（m3）。
		return TrashResultPermissionDenied
	}
	return TrashResultError
}

// BatchDeleteOptions 是批量删除的可选参数：RequestID 非空时登记为可取消，Progress 每处理完一项回调一次。
type BatchDeleteOptions struct {
	RequestID string
	Progress  func(done, total int)
}

var batchDeleteCancels sync.Map // requestID -> *atomic.Bool

// begin 登记可取消的请求，返回「是否已被取消」的检查函数与收尾函数。
func (o BatchDeleteOptions) begin() (cancelled func() bool, finish func()) {
	requestID := strings.TrimSpace(o.RequestID)
	if requestID == "" {
		return func() bool { return false }, func() {}
	}
	flag := &atomic.Bool{}
	batchDeleteCancels.Store(requestID, flag)
	return flag.Load, func() { batchDeleteCancels.Delete(requestID) }
}

func (o BatchDeleteOptions) report(done, total int) {
	if o.Progress != nil {
		o.Progress(done, total)
	}
}

// CancelBatchDelete 请求取消一个进行中的批量删除：在两项之间生效，已完成的项保留，
// 未处理的项计为 cancelled。找不到 requestID（已结束或从未登记）时返回 false。
func CancelBatchDelete(requestID string) bool {
	value, ok := batchDeleteCancels.Load(strings.TrimSpace(requestID))
	if !ok {
		return false
	}
	value.(*atomic.Bool).Store(true)
	return true
}

// ===== 条目路径登记 =====

// claimTrashPathTx 让 entryID 独占废纸篓路径 path。系统废纸篓在用户清空后会重新使用同名路径，
// 旧条目（通常是 file_gone）仍带着这个路径，会撞 trash_path 的部分唯一索引：文件此刻确实
// 在 path 上，所以别的条目在这里一定已经不是「文件还在」——先把 deleted 的置为 file_gone，
// 再清空它们的路径。
func claimTrashPathTx(tx *gorm.DB, table string, entryID uint, path string) error {
	if err := tx.Table(table).
		Where("trash_path = ? AND id <> ? AND state = ?", path, entryID, trashStateDeleted).
		Update("state", models.TrashStateFileGone).Error; err != nil {
		return err
	}
	return tx.Table(table).
		Where("trash_path = ? AND id <> ?", path, entryID).
		Update("trash_path", "").Error
}

// recordTrashedPath 把系统返回的废纸篓实际路径写进 pending_move 条目。
func recordTrashedPath(table string, entryID uint, path string) error {
	return database.Transaction(func(tx *gorm.DB) error {
		if err := claimTrashPathTx(tx, table, entryID, path); err != nil {
			return err
		}
		result := tx.Table(table).
			Where("id = ? AND state = ?", entryID, trashStatePendingMove).
			Update("trash_path", path)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: %d", errTrashEntryStateChanged, entryID)
		}
		return nil
	})
}

// ===== 回收站读写接口（视频与图片统一，D-PC04） =====

type trashKindSpec struct {
	kind       string
	table      string
	entityCol  string
	nameCol    string
	mediaTable string
}

var (
	videoTrashKind = trashKindSpec{kind: trashKindVideo, table: "video_trash_entries", entityCol: "video_id", nameCol: "video_name", mediaTable: "videos"}
	imageTrashKind = trashKindSpec{kind: trashKindImage, table: "image_trash_entries", entityCol: "image_id", nameCol: "image_name", mediaTable: "images"}
)

func trashKindFor(kind string) (trashKindSpec, error) {
	switch kind {
	case trashKindVideo:
		return videoTrashKind, nil
	case trashKindImage:
		return imageTrashKind, nil
	}
	return trashKindSpec{}, fmt.Errorf("不支持的回收站类型: %q", kind)
}

// TrashFilter 是回收站列表的筛选条件；CursorID 为上一页最后一条的 id（0 表示第一页）。
type TrashFilter struct {
	Kind      string `json:"kind"`
	Mode      string `json:"mode"`
	DeletedBy string `json:"deleted_by"`
	Query     string `json:"query"`
	CursorID  uint   `json:"cursor_id"`
	Limit     int    `json:"limit"`
}

// 回收站条目可用的操作。
const (
	TrashActionRestore      = "restore"
	TrashActionPurge        = "purge"
	TrashActionRemoveRecord = "remove_record"
)

// TrashEntryView 是列表里的一项，视频与图片同构（EntityID 是视频或图片的 ID）。
type TrashEntryView struct {
	ID            uint      `json:"id"`
	Kind          string    `json:"kind"`
	EntityID      uint      `json:"entity_id"`
	Name          string    `json:"name"`
	OriginalPath  string    `json:"original_path"`
	TrashPath     string    `json:"trash_path"`
	FileMoved     bool      `json:"file_moved"`
	FileSize      int64     `json:"file_size"`
	State         string    `json:"state"`
	Mode          string    `json:"mode"`
	DeletedBy     string    `json:"deleted_by"`
	DeleteBatchID string    `json:"delete_batch_id"`
	LastError     string    `json:"last_error"`
	CreatedAt     time.Time `json:"created_at" ts_type:"string"`
	// PutBack：文件已被用户在访达里「放回原处」（身份与条目一致）。列表只做判定、不自动恢复，
	// 由显式的恢复动作把记录还原（Minor 1）；此时 actions 只有 restore。
	PutBack bool `json:"put_back"`
	// ClaimedByActive：文件已放回原处，但原位置已由另一条活跃记录收录（旧版放回后被扫描新建、或修复 D 之前
	// 读不到废纸篓时重复收录，I-1）。这一行只是重复的旧记录：不报 put_back，actions 只有 remove_record，
	// 移除时只硬删这一行及其条目（legacy_trash 行改为墓碑，I-A），不动任何文件。恢复中断（state=restoring）、
	// 文件已在原处而原位置已被另一条活跃记录收录的行同样报 claimed_by_active（修复 I m-c）。
	ClaimedByActive bool `json:"claimed_by_active"`
	// OriginalSymlink：原位置上是符号链接（修复 I m-a）。恢复不会跟随它，所以 actions 里不提供 restore；
	// 它还正指向废纸篓里的这个文件时，清除会删掉经它仍在使用的唯一一份内容，purge 也不提供。
	OriginalSymlink bool     `json:"original_symlink"`
	Actions         []string `json:"actions"`
}

// TrashPage 是一页回收站条目。NextCursor 作为下一次的 CursorID；HasMore 为 false 时没有更多。
type TrashPage struct {
	Items      []TrashEntryView `json:"items"`
	NextCursor uint             `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

type trashRow struct {
	ID            uint
	EntityID      uint
	Name          string
	OriginalPath  string
	TrashPath     string
	FileMoved     bool
	FileSize      int64
	FileModTime   int64
	FileIdentity  string
	State         string
	Mode          string
	DeletedBy     string
	DeleteBatchID string
	LastError     string
	CreatedAt     time.Time
}

func (spec trashKindSpec) selectColumns() string {
	return "id, " + spec.entityCol + " AS entity_id, " + spec.nameCol + " AS name, original_path, trash_path, " +
		"file_moved, file_size, file_mod_time, file_identity, state, mode, deleted_by, delete_batch_id, last_error, created_at"
}

// holdsTrashFile 报告这一行的文件应当躺在系统废纸篓（或旧版 trash/）里。
func (r trashRow) holdsTrashFile() bool {
	return r.Mode == models.TrashModeTrash || r.Mode == models.TrashModeLegacyTrash || (r.Mode == "" && r.FileMoved)
}

func (r trashRow) actions() []string {
	switch r.State {
	case trashStateDeleted:
		switch r.Mode {
		case models.TrashModeTrash, models.TrashModeLegacyTrash:
			return []string{TrashActionRestore, TrashActionPurge}
		}
		return []string{TrashActionRestore}
	case models.TrashStateFileGone:
		return []string{TrashActionRemoveRecord}
	case trashStatePendingMove, trashStateRollback:
		// 上次删除被打断：恢复就是取消那次删除。
		return []string{TrashActionRestore}
	}
	return []string{}
}

// ListTrashEntries 按 id 倒序分页列出条目，并对当页里应有文件的行做对账：废纸篓里的文件
// 已不存在的，条件更新为 file_gone。
//
// 列表路径不拿全局路径写锁、也不执行恢复（Minor 1）：访达「放回原处」只做判定，
// 以 put_back=true 返回，由显式的恢复动作去还原记录。原位置已由另一条活跃记录收录了这个文件时
// 改报 claimed_by_active=true、actions=[remove_record]（I-1；恢复中断的 restoring 行同样，修复 I m-c）。
// 原位置是符号链接时不提供必然失败的 restore，它指向废纸篓里这个文件时也不提供 purge（修复 I m-a）。
// 墓碑（trashStateRemoved）不列出（I-A）。
func (s *TrashService) ListTrashEntries(filter TrashFilter) (*TrashPage, error) {
	spec, err := trashKindFor(filter.Kind)
	if err != nil {
		return nil, err
	}
	limit := normalizeEntityPageLimit(filter.Limit)
	query := database.DB.Table(spec.table).Select(spec.selectColumns()).Where("state <> ?", trashStateRemoved)
	if filter.CursorID > 0 {
		query = query.Where("id < ?", filter.CursorID)
	}
	if mode := strings.TrimSpace(filter.Mode); mode != "" {
		query = query.Where("mode = ?", mode)
	}
	if deletedBy := strings.TrimSpace(filter.DeletedBy); deletedBy != "" {
		query = query.Where("deleted_by = ?", deletedBy)
	}
	if text := strings.TrimSpace(filter.Query); text != "" {
		like := "%" + escapeSQLLikePrefix(strings.ToLower(text)) + "%"
		query = query.Where(`(LOWER(`+spec.nameCol+`) LIKE ? ESCAPE '\' OR LOWER(original_path) LIKE ? ESCAPE '\')`, like, like)
	}
	var rows []trashRow
	if err := query.Order("id DESC").Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("列出回收站条目失败: %w", err)
	}
	page := &TrashPage{Items: make([]TrashEntryView, 0, len(rows))}
	if len(rows) > limit {
		page.HasMore = true
		rows = rows[:limit]
	}
	// 放回判定之前先查清原路径是否已被另一条活跃记录占用（I-1），当页一次查询。
	occupants, err := activeOccupantsOf(spec, rows)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		putBack, claimed := false, false
		occupied := occupants.otherThan(row.OriginalPath, row.EntityID)
		switch {
		case (row.State == trashStateDeleted || row.State == models.TrashStateFileGone) && row.holdsTrashFile():
			outcome, err := s.reconcileListedRow(spec, &row, occupied)
			if err != nil {
				return nil, err
			}
			putBack = outcome == listedRowPutBack
			claimed = outcome == listedRowClaimedByActive
		case row.State == trashStateRestoring && row.holdsTrashFile():
			// 恢复中断、文件已在原处、原位置却已被另一条活跃记录收录（修复 I m-c）：启动对账每次都会因
			// path_occupied 失败，这一行只能移除（只删记录，不动文件）。只判定、不改状态。
			claimed = occupied && trashRowFileAtOriginal(row)
		}
		actions := row.actions()
		originalSymlink := false
		switch {
		case claimed:
			// 原位置的文件已归另一条活跃记录：这一行只能移除（只删记录，不动文件）。
			actions = []string{TrashActionRemoveRecord}
		case putBack:
			// 文件已回到原处：只能恢复记录；清除会把记录删掉而文件还在原地。
			actions = []string{TrashActionRestore}
		case row.State == trashStateDeleted && row.FileMoved:
			actions, originalSymlink = actionsForOriginalNotRegular(row, actions)
		}
		page.Items = append(page.Items, TrashEntryView{
			ID: row.ID, Kind: spec.kind, EntityID: row.EntityID, Name: row.Name,
			OriginalPath: row.OriginalPath, TrashPath: row.TrashPath, FileMoved: row.FileMoved,
			FileSize: row.FileSize, State: row.State, Mode: row.Mode, DeletedBy: row.DeletedBy,
			DeleteBatchID: row.DeleteBatchID, LastError: row.LastError, CreatedAt: row.CreatedAt,
			PutBack: putBack, ClaimedByActive: claimed, OriginalSymlink: originalSymlink, Actions: actions,
		})
	}
	// 游标取本页扫描到的最后一行（Minor 8）：即使某一行将来被过滤掉，也不会出现
	// NextCursor=0 && HasMore=true 让前端从头再翻。
	if len(rows) > 0 {
		page.NextCursor = rows[len(rows)-1].ID
	}
	return page, nil
}

type listedRowOutcome int

const (
	listedRowUnchanged listedRowOutcome = iota
	listedRowMarked
	// listedRowPutBack：文件被放回了原处，条目可恢复（只判定，不恢复）。
	listedRowPutBack
	// listedRowClaimedByActive：文件放回了原处，但原位置已由另一条活跃记录收录（I-1）。状态不变，只能移除记录。
	listedRowClaimedByActive
)

// reconcileListedRow 对列表当页里「应有文件」的行做对账（详细设计 §2.1）：
//
//   - 原路径已被另一条活跃记录占用（occupied，调用方在放回判定之前查好），且原路径上的文件满足放回判定 →
//     这个文件已归那条活跃记录，这一行是重复的旧记录：不报可恢复、不改状态（file_gone 也不复活），只能移除（I-1）；
//   - 原路径上的文件满足放回判定（trashRowPutBack，只看原路径的文件身份，I-A / M9）→ 报告可恢复，
//     不改状态、不恢复（Minor 1）；废纸篓那一侧此时读不读得到都不影响结论；
//   - 废纸篓里的文件还在且身份一致 → 不动；读不到（权限等）→ 不动；
//   - 文件还在但身份不符（被别的同名文件占了）→ file_gone + 「废纸篓中的文件已被替换」；
//   - 文件不在：先确认所在卷在线（卷离线不是「文件没了」，保持原状，I1），否则 file_gone。
//
// 状态转换全部是条件更新（WHERE state='deleted'）。
func (s *TrashService) reconcileListedRow(spec trashKindSpec, row *trashRow, occupied bool) (listedRowOutcome, error) {
	want := trashFileID{Size: row.FileSize, ModTimeNS: row.FileModTime, Identity: row.FileIdentity}
	putBack := trashRowPutBack(*row)
	if putBack && occupied {
		return listedRowClaimedByActive, nil
	}
	if row.State == models.TrashStateFileGone {
		return s.reviveGoneRow(spec, row, want, putBack)
	}
	if putBack {
		return listedRowPutBack, nil
	}
	if strings.TrimSpace(row.TrashPath) != "" {
		info, statErr := os.Stat(row.TrashPath)
		switch {
		case statErr == nil:
			if info.IsDir() || !trashRowFileMatches(*row, want, info) {
				return s.markListedRowGone(spec, row, trashReplacedMessage)
			}
			return listedRowUnchanged, nil
		case !errors.Is(statErr, os.ErrNotExist):
			return listedRowUnchanged, nil
		}
	}
	// 文件不在废纸篓里。卷离线时什么都不改。
	if !trashRowVolumeOnline(*row) {
		return listedRowUnchanged, nil
	}
	return s.markListedRowGone(spec, row, "")
}

// reviveGoneRow 处理已标成 file_gone 的行：文件重新出现（身份一致）时改回 deleted——
// 要么被放回了原处（putBack，此时报告可恢复，由显式恢复还原记录），要么废纸篓路径上又有了它（例如卷重新挂载）。
// 放回但原位置已被另一条活跃记录占用的行由调用方先行排除，不会走到这里被复活（I-1）。
func (s *TrashService) reviveGoneRow(spec trashKindSpec, row *trashRow, want trashFileID, putBack bool) (listedRowOutcome, error) {
	revive := func(outcome listedRowOutcome) (listedRowOutcome, error) {
		result := database.DB.Table(spec.table).
			Where("id = ? AND state = ?", row.ID, models.TrashStateFileGone).
			Updates(map[string]interface{}{"state": trashStateDeleted, "last_error": ""})
		if result.Error != nil {
			return listedRowUnchanged, fmt.Errorf("对账回收站条目失败: %w", result.Error)
		}
		if result.RowsAffected == 1 {
			row.State = trashStateDeleted
			row.LastError = ""
			return outcome, nil
		}
		// 条目状态已被他方改变：按原样返回，不报告可恢复。
		return listedRowUnchanged, nil
	}
	if putBack {
		return revive(listedRowPutBack)
	}
	if strings.TrimSpace(row.TrashPath) != "" {
		if info, err := os.Stat(row.TrashPath); err == nil && !info.IsDir() && trashRowFileMatches(*row, want, info) && !want.empty() {
			return revive(listedRowMarked)
		}
	}
	return listedRowUnchanged, nil
}

// putBackFacts 把列表行转成放回判定所需的条目事实。
func (r trashRow) putBackFacts() softDeletedEntryFacts {
	return softDeletedEntryFacts{Mode: r.Mode, DeletedBy: r.DeletedBy, FileSize: r.FileSize, FileModTime: r.FileModTime,
		FileIdentity: r.FileIdentity, DeleteBatchID: r.DeleteBatchID, State: r.State, FileMoved: r.FileMoved}
}

// trashRowPutBack 是列表、清除、移除记录与用量共用的放回判定：与扫描、手动添加同一个 putBackDetected
// （trash 模式比 inode + 大小 + mtime，legacy_trash 比大小 + inode），只看原路径上的普通文件（Lstat，I-A / m1）。
func trashRowPutBack(row trashRow) bool {
	return putBackAtPath(row.putBackFacts(), row.OriginalPath)
}

// trashRowFileAtOriginal 报告原路径上的普通文件（Lstat）是不是这一行当初删掉的那个文件：deleted / file_gone 用放回
// 判定（trashRowPutBack）；恢复中断的 restoring 行（修复 I m-c）不经放回判定的状态门，直接比身份（entryFileAtOriginal，
// 与放回判定同一口径）。其余状态一律 false。
func trashRowFileAtOriginal(row trashRow) bool {
	switch row.State {
	case trashStateDeleted, models.TrashStateFileGone:
		return trashRowPutBack(row)
	case trashStateRestoring:
		return entryFileAtOriginalPath(row.putBackFacts(), row.OriginalPath)
	}
	return false
}

// entryFileAtOriginalPath 读取原路径上的普通文件（Lstat）并用 entryFileAtOriginal 判定它是不是条目当初删掉的那个文件；
// 文件不在、读不到或不是普通文件时返回 false。
func entryFileAtOriginalPath(facts softDeletedEntryFacts, path string) bool {
	info := originalRegularFile(path)
	if info == nil {
		return false
	}
	return entryFileAtOriginal(facts, info.Size(), info.ModTime().UnixNano(), stableFileIdentity(info))
}

// releaseOccupiedRestoringEntry 处理恢复途中发现原位置已被另一条活跃记录占用的 restoring 行（修复 L m2）：
//   - 文件已在原处（entryFileAtOriginalPath）：这是 claimed_by_active 的重复旧记录，保持 restoring，由列表的
//     remove_record（或「仍然移除记录」）处理（修复 I m-c）；
//   - 文件不在原处（多半还在废纸篓里，或只删记录 / 文件缺失的条目）：用条件更新（WHERE state='restoring'）退回
//     deleted，成为普通条目——清除、恢复（原位置空出来之后）、仍然移除记录都可用；否则启动对账每次都报
//     path_occupied，列表也不给任何动作，这一行就永远卡住。
//
// 只改条目状态，不动文件。并发改变了状态（影响 0 行）时什么都不做。
func releaseOccupiedRestoringEntry(spec trashKindSpec, id uint, facts softDeletedEntryFacts, originalPath string) error {
	if entryFileAtOriginalPath(facts, originalPath) {
		return nil
	}
	result := database.DB.Table(spec.table).
		Where("id = ? AND state = ?", id, trashStateRestoring).
		Updates(map[string]interface{}{"state": trashStateDeleted, "last_error": ErrTrashPathOccupied.Error(), "updated_at": time.Now()})
	if result.Error != nil {
		return fmt.Errorf("退回中断的恢复失败: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		log.Printf("恢复中断且原位置已被占用、文件不在原处，条目退回 deleted kind=%s entry=%d", spec.kind, id)
	}
	return nil
}

// actionsForOriginalNotRegular 处理原位置上不是普通文件（通常是符号链接）的 deleted 行（修复 I m-a）：
//   - 恢复只接受普通文件的原位置（ErrTrashOriginalNotRegular），不提供必然失败的 restore；
//   - 符号链接正指向废纸篓里的这个文件时，清除同样拒绝（purgeOne），也不提供 purge。
//
// 第二个返回值报告原位置是否是符号链接（TrashEntryView.OriginalSymlink）。原位置不存在或读不到时原样返回。
func actionsForOriginalNotRegular(row trashRow, actions []string) ([]string, bool) {
	info, err := os.Lstat(row.OriginalPath)
	if err != nil || info.Mode().IsRegular() {
		return actions, false
	}
	blockPurge := originalSymlinkToTrashFile(row.OriginalPath, row.TrashPath)
	kept := make([]string, 0, len(actions))
	for _, action := range actions {
		if action == TrashActionRestore || (blockPurge && action == TrashActionPurge) {
			continue
		}
		kept = append(kept, action)
	}
	return kept, info.Mode()&os.ModeSymlink != 0
}

// canDetectPutBack 报告 trash 模式的条目能否用完整身份判定「访达放回原处」：记录了大小、mtime 与 inode。
// legacy_trash 旧行没有可信的 mtime，按大小 + inode 判定，见 putBackDetected。
func canDetectPutBack(mode string, want trashFileID) bool {
	return mode == models.TrashModeTrash && want.Identity != "" && want.ModTimeNS != 0 && want.Size != 0
}

// activeOccupants 是一页回收站行的原路径上现有的活跃记录（路径 → 媒体 ID）。
type activeOccupants map[string][]uint

// otherThan 报告 path 上是否有 entityID 以外的活跃记录。
func (o activeOccupants) otherThan(path string, entityID uint) bool {
	for _, id := range o[path] {
		if id != entityID {
			return true
		}
	}
	return false
}

// activeOccupantsOf 一次查出 rows 的原路径上现有的活跃记录（I-1：放回判定之前先查占用）。
func activeOccupantsOf(spec trashKindSpec, rows []trashRow) (activeOccupants, error) {
	paths := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.OriginalPath == "" || !row.holdsTrashFile() {
			continue
		}
		if _, ok := seen[row.OriginalPath]; ok {
			continue
		}
		seen[row.OriginalPath] = struct{}{}
		paths = append(paths, row.OriginalPath)
	}
	occupants := make(activeOccupants)
	if len(paths) == 0 {
		return occupants, nil
	}
	var found []struct {
		ID   uint
		Path string
	}
	if err := database.DB.Table(spec.mediaTable).Select("id, path").
		Where("deleted_at IS NULL AND path IN ?", paths).Scan(&found).Error; err != nil {
		return nil, fmt.Errorf("检查原路径活跃记录失败: %w", err)
	}
	for _, item := range found {
		occupants[item.Path] = append(occupants[item.Path], item.ID)
	}
	return occupants, nil
}

// activeOccupantAt 报告 path 上是否有 entityID 以外的活跃记录（清除、移除记录用的单行版本）。
func activeOccupantAt(spec trashKindSpec, path string, entityID uint) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, nil
	}
	var count int64
	if err := database.DB.Table(spec.mediaTable).
		Where("deleted_at IS NULL AND path = ? AND id <> ?", path, entityID).Count(&count).Error; err != nil {
		return false, fmt.Errorf("检查原路径活跃记录失败: %w", err)
	}
	return count > 0, nil
}

// claimedByActive 报告这一行是不是「文件已放回原处、却已由原位置上另一条活跃记录收录」的重复旧记录（I-1）。
// 先查占用，再做放回判定。恢复中断（restoring）、文件已在原处、原位置已被占用的行同样算（修复 I m-c）：
// 启动对账对它每次都报 path_occupied，不在这里放行它就永远无法处理。
func claimedByActive(spec trashKindSpec, row trashRow) (bool, error) {
	if row.State != trashStateDeleted && row.State != models.TrashStateFileGone && row.State != trashStateRestoring {
		return false, nil
	}
	occupied, err := activeOccupantAt(spec, row.OriginalPath, row.EntityID)
	if err != nil || !occupied {
		return false, err
	}
	return trashRowFileAtOriginal(row), nil
}

func (s *TrashService) markListedRowGone(spec trashKindSpec, row *trashRow, message string) (listedRowOutcome, error) {
	updates := map[string]interface{}{"state": models.TrashStateFileGone}
	if message != "" {
		updates["last_error"] = message
	}
	result := database.DB.Table(spec.table).
		Where("id = ? AND state = ?", row.ID, trashStateDeleted).
		Updates(updates)
	if result.Error != nil {
		return listedRowUnchanged, fmt.Errorf("对账回收站条目失败: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		row.State = models.TrashStateFileGone
		if message != "" {
			row.LastError = message
		}
		return listedRowMarked, nil
	}
	return listedRowUnchanged, nil
}

// trashRowFileMatches 核对废纸篓路径上的文件是否仍是当初删掉的那个。trash 模式要求大小、mtime、
// inode 都相符；legacy_trash 行列表阶段不做整文件哈希，只比记录了的大小。
func trashRowFileMatches(row trashRow, want trashFileID, info os.FileInfo) bool {
	if isLegacyTrashMode(row.Mode) {
		return want.Size == 0 || info.Size() == want.Size
	}
	return want.strictMatch(info)
}

// mediaVolumeAvailableFn 是卷挂载检查的替身入口，只在单测里设置（模拟「卷未挂载」）；为 nil 时
// 使用真实的 scanVolumeAvailable。用原子指针是因为后台 goroutine（播放重定位、监听巡检）会并发读它。
var mediaVolumeAvailableFn atomic.Pointer[func(string) error]

// mediaVolumeAvailable 是「/Volumes 下的卷确实挂载」检查（非 macOS 恒为可用）。判定前先解析符号链接
// （M3）：路径（或它的某一级父目录）是指向 /Volumes 下某块盘的软链接时，要检查的是链接指向的那块盘。
// 路径本身可以不存在（文件已不在），此时解析最近的存在的父目录再拼上其余部分；解析失败（包括途中
// 遇到悬空的软链接）按离线处理——宁可不改写状态，也不把「链接那头的盘没插」当成「文件没了」。
// 返回的错误保留原因：解析遇到 EPERM / EACCES 时 errors.Is(err, os.ErrPermission) 成立，由
// mediaPathUnavailable 报成权限问题而不是离线（m3）。
func mediaVolumeAvailable(path string) error {
	clean := filepath.Clean(path)
	resolved, err := resolveMediaPathSymlinks(clean)
	if err != nil {
		return fmt.Errorf("无法确认所在磁盘已连接: %w", err)
	}
	return volumeMountedCheck(clean, resolved)
}

// volumeMountedCheck 对原路径与解析后的路径都做 /Volumes 挂载检查（单测替身或真实的 scanVolumeAvailable），
// 任一报告未挂载即未挂载。原路径那一次对真实实现是多余的（解析后的路径已经覆盖），保留它是为了让按原路径
// 前缀注入的替身照常生效。
func volumeMountedCheck(raw, resolved string) error {
	check := scanVolumeAvailable
	if fn := mediaVolumeAvailableFn.Load(); fn != nil {
		check = *fn
	}
	if err := check(raw); err != nil {
		return err
	}
	if resolved != raw {
		return check(resolved)
	}
	return nil
}

// resolveMediaPathSymlinks 解析 path 的符号链接。path 不存在时解析最近的存在的父目录，再拼上其余部分；
// 途中某一级「自身存在（Lstat 成功）却解析不了」说明它是悬空的软链接，返回错误。
func resolveMediaPathSymlinks(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	rest := make([]string, 0, 4)
	current := path
	for {
		if _, lstatErr := os.Lstat(current); lstatErr == nil {
			return "", fmt.Errorf("符号链接指向的位置不存在: %w", os.ErrNotExist)
		} else if !errors.Is(lstatErr, os.ErrNotExist) {
			return "", lstatErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		rest = append(rest, filepath.Base(current))
		current = parent
		parentResolved, parentErr := filepath.EvalSymlinks(current)
		if parentErr == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				parentResolved = filepath.Join(parentResolved, rest[i])
			}
			return parentResolved, nil
		}
		if !errors.Is(parentErr, os.ErrNotExist) {
			return "", parentErr
		}
	}
}

// configuredMediaRoots 读出全部视频与图片扫描根（已清理、未去重）。
func configuredMediaRoots() ([]string, error) {
	var videoDirs []models.ScanDirectory
	if err := database.DB.Find(&videoDirs).Error; err != nil {
		return nil, err
	}
	roots := cleanScanRoots(videoDirs)
	var imageDirs []models.ImageDirectory
	if err := database.DB.Find(&imageDirs).Error; err != nil {
		return nil, err
	}
	for _, dir := range imageDirs {
		root := filepath.Clean(strings.TrimSpace(dir.Path))
		if root != "" && root != "." {
			roots = append(roots, root)
		}
	}
	return roots, nil
}

// scanRootsUnavailableForPath 判断包含 path 的视频或图片扫描根是否都不可用：有一个可用即 nil；都不可用时，
// 只要有一个是因为权限（EPERM / EACCES）读不到就返回 ErrTrashLocationPermissionDenied（m3），否则返回
// ErrTrashVolumeOffline。读不到扫描目录时按离线处理：宁可不改写状态。不属于任何根时返回 nil
// （那是文件位置问题，不是盘没插）。
func scanRootsUnavailableForPath(path string) error {
	path = filepath.Clean(path)
	roots, err := configuredMediaRoots()
	if err != nil {
		return ErrTrashVolumeOffline
	}
	containing, permission := 0, false
	for _, root := range roots {
		if path != root && !strings.HasPrefix(path, scanRootChildPrefix(root)) {
			continue
		}
		containing++
		rootErr := scanRootAvailability(root)
		if rootErr == nil {
			return nil
		}
		if errors.Is(rootErr, os.ErrPermission) {
			permission = true
		}
	}
	switch {
	case containing == 0:
		return nil
	case permission:
		return ErrTrashLocationPermissionDenied
	}
	return ErrTrashVolumeOffline
}

// mediaPathUnavailable 是「文件所在位置不可用」的唯一判断（I-2）：只看两件事——
//   - /Volumes 下的卷是否确实挂载（mediaVolumeAvailable，先解析符号链接）；
//   - 包含它的扫描根（视频或图片）是否都不在线。
//
// 可用时返回 nil。解析路径时遇到 EPERM / EACCES 返回 ErrTrashLocationPermissionDenied：那是权限问题，
// 不是「磁盘未连接」（m3）；其余不可用返回 ErrTrashVolumeOffline。卷已挂载但父目录不存在不算不可用，而是
// 「文件已不在」。找不到归属的根时按在线处理（那是「文件缺失」，不是「盘没插」）。不可用不能当成删除依据，
// 也不能降级（I6）。
func mediaPathUnavailable(path string) error {
	clean := filepath.Clean(path)
	if err := mediaVolumeAvailable(clean); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return ErrTrashLocationPermissionDenied
		}
		return ErrTrashVolumeOffline
	}
	return scanRootsUnavailableForPath(clean)
}

// mediaPathOffline 报告文件所在位置是否不可用（离线或因权限读不到），见 mediaPathUnavailable。
func mediaPathOffline(path string) bool {
	return mediaPathUnavailable(path) != nil
}

// filePathOnline 是 mediaPathOffline 的反面，供「文件不在时要不要落库」的各处判定使用
// （崩溃恢复、迁移残留、废纸篓条目）。
func filePathOnline(path string) bool {
	return !mediaPathOffline(path)
}

// trashEntryUnavailable 判断回收站条目涉及的位置是否可用：原路径所在位置，以及废纸篓路径（非空时）所在的
// 卷与扫描根。可用返回 nil，否则返回 mediaPathUnavailable 的原因（离线或权限）。
func trashEntryUnavailable(originalPath, trashPath string) error {
	if err := mediaPathUnavailable(originalPath); err != nil {
		return err
	}
	if trashPath = strings.TrimSpace(trashPath); trashPath != "" {
		return mediaPathUnavailable(trashPath)
	}
	return nil
}

// trashRowVolumeOnline 判断条目文件所在的位置是否可用（原路径与废纸篓路径都要可用）。
func trashRowVolumeOnline(row trashRow) bool {
	return trashEntryUnavailable(row.OriginalPath, row.TrashPath) == nil
}

// trashUsageVolumes 是一次用量统计里各个「卷」的可用性缓存（m5）：每个卷只做一次挂载检查，离线（或读不到）
// 卷上的行不逐条 stat 原路径，按仍在废纸篓计入。
//
// 卷按字符串前缀认：包含路径的最长扫描根（根可能是指向外置盘的软链接，检查时会解析它），否则是 /Volumes/<名>；
// 都不是时（本机磁盘上、不属于任何根）不做挂载检查，照常逐行判定。
type trashUsageVolumes struct {
	roots     []string
	available map[string]bool
}

func newTrashUsageVolumes() (*trashUsageVolumes, error) {
	roots, err := configuredMediaRoots()
	if err != nil {
		return nil, err
	}
	return &trashUsageVolumes{roots: roots, available: make(map[string]bool)}, nil
}

func (v *trashUsageVolumes) volumeOf(path string) string {
	clean := filepath.Clean(path)
	best := ""
	for _, root := range v.roots {
		if (clean == root || strings.HasPrefix(clean, scanRootChildPrefix(root))) && len(root) > len(best) {
			best = root
		}
	}
	if best != "" {
		return best
	}
	if rest, ok := strings.CutPrefix(clean, "/Volumes/"); ok && rest != "" {
		return "/Volumes/" + strings.SplitN(rest, "/", 2)[0]
	}
	return ""
}

// pathAvailable 报告 path 所在的卷是否可用；同一个卷只检查一次。
func (v *trashUsageVolumes) pathAvailable(path string) bool {
	volume := v.volumeOf(path)
	if volume == "" {
		return true
	}
	available, known := v.available[volume]
	if !known {
		available = mediaPathUnavailable(volume) == nil
		v.available[volume] = available
	}
	return available
}

// TrashKindUsage 是某一类媒体的回收站用量。BytesInTrash 只计 state=deleted 且文件真的在
// 废纸篓（trash / legacy_trash）里的条目：文件已被放回原处（与列表的 put_back 同一判定）的行不计，
// 清空废纸篓并不会释放它们的空间（M10）。LegacyBytes 是其中 legacy_trash 的部分，同样不计已放回的行。
type TrashKindUsage struct {
	Count        int64 `json:"count"`
	BytesInTrash int64 `json:"bytes_in_trash"`
	GoneCount    int64 `json:"gone_count"`
	LegacyCount  int64 `json:"legacy_count"`
	LegacyBytes  int64 `json:"legacy_bytes"`
}

// TrashStagedUsage 是迁移残留（待清理的暂存源文件）的用量。
type TrashStagedUsage struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

// TrashUsage 是回收站用量总览。
type TrashUsage struct {
	Video  TrashKindUsage   `json:"video"`
	Image  TrashKindUsage   `json:"image"`
	Staged TrashStagedUsage `json:"staged"`
}

func trashKindUsage(spec trashKindSpec, volumes *trashUsageVolumes) (TrashKindUsage, error) {
	var usage TrashKindUsage
	modes := []string{models.TrashModeTrash, models.TrashModeLegacyTrash}
	scan := func(dest *int64, expr string, query *gorm.DB) error {
		return query.Select(expr).Scan(dest).Error
	}
	db := func() *gorm.DB { return database.DB.Table(spec.table) }
	if err := scan(&usage.Count, "COUNT(*)", db().Where("state IN ?", []string{trashStateDeleted, models.TrashStateFileGone})); err != nil {
		return usage, err
	}
	if err := scan(&usage.GoneCount, "COUNT(*)", db().Where("state = ?", models.TrashStateFileGone)); err != nil {
		return usage, err
	}
	if err := scan(&usage.LegacyCount, "COUNT(*)", db().Where("state = ? AND mode = ?", trashStateDeleted, models.TrashModeLegacyTrash)); err != nil {
		return usage, err
	}
	// 字节数要逐行排除已放回原处的条目：放回与否取决于磁盘上的文件身份，库里没有这一列，
	// 所以按 id 分批读出候选行、在 Go 里判定（可用卷上每行一次原路径 stat；离线卷上的行不 stat，
	// 按未放回计入，每个卷只检查一次，m5）。
	const batchSize = 500
	var afterID uint
	for {
		var rows []trashRow
		if err := db().Select(spec.selectColumns()).
			Where("state = ? AND mode IN ? AND id > ?", trashStateDeleted, modes, afterID).
			Order("id ASC").Limit(batchSize).Scan(&rows).Error; err != nil {
			return usage, err
		}
		for _, row := range rows {
			if volumes.pathAvailable(row.OriginalPath) && trashRowPutBack(row) {
				continue
			}
			usage.BytesInTrash += row.FileSize
			if row.Mode == models.TrashModeLegacyTrash {
				usage.LegacyBytes += row.FileSize
			}
		}
		if len(rows) < batchSize {
			return usage, nil
		}
		afterID = rows[len(rows)-1].ID
	}
}

// GetTrashUsage 返回回收站用量：视频、图片、迁移残留。
func (s *TrashService) GetTrashUsage() (*TrashUsage, error) {
	usage := &TrashUsage{}
	// 视频与图片共用一份卷可用性缓存：同一块盘在这一次统计里只检查一次（m5）。
	volumes, err := newTrashUsageVolumes()
	if err != nil {
		return nil, fmt.Errorf("读取扫描目录失败: %w", err)
	}
	if usage.Video, err = trashKindUsage(videoTrashKind, volumes); err != nil {
		return nil, fmt.Errorf("统计视频回收站失败: %w", err)
	}
	if usage.Image, err = trashKindUsage(imageTrashKind, volumes); err != nil {
		return nil, fmt.Errorf("统计图片回收站失败: %w", err)
	}
	staged := database.DB.Model(&models.MigrationStagedSource{}).Where("state = ?", models.MigrationStagedStatePending)
	if err := staged.Select("COUNT(*)").Scan(&usage.Staged.Count).Error; err != nil {
		return nil, fmt.Errorf("统计迁移残留失败: %w", err)
	}
	staged = database.DB.Model(&models.MigrationStagedSource{}).Where("state = ?", models.MigrationStagedStatePending)
	if err := staged.Select("CAST(COALESCE(SUM(size), 0) AS BIGINT)").Scan(&usage.Staged.Bytes).Error; err != nil {
		return nil, fmt.Errorf("统计迁移残留失败: %w", err)
	}
	return usage, nil
}

func (s *TrashService) restoreOne(kind string, entryID uint) error {
	switch kind {
	case trashKindVideo:
		if s.videos == nil {
			return errors.New("视频服务未就绪")
		}
		_, err := s.videos.RestoreTrashEntry(entryID)
		return err
	case trashKindImage:
		if s.images == nil {
			return errors.New("图片服务未就绪")
		}
		_, err := s.images.RestoreImageTrashEntry(entryID)
		return err
	}
	return fmt.Errorf("不支持的回收站类型: %q", kind)
}

// RestoreTrashEntries 逐项恢复。record_only 只还原数据库；原路径已有活跃记录时该项返回 path_occupied。
func (s *TrashService) RestoreTrashEntries(kind string, ids []uint) (*BatchResult, error) {
	if _, err := trashKindFor(kind); err != nil {
		return nil, err
	}
	result := newBatchResult(len(ids), "")
	for _, id := range ids {
		result.addOutcome(id, "", s.restoreOne(kind, id))
	}
	return result, nil
}

// RestoreTrashBatch 恢复同一次删除操作留下的全部可恢复条目（撤销本次删除）。
func (s *TrashService) RestoreTrashBatch(kind, batchID string) (*BatchResult, error) {
	spec, err := trashKindFor(kind)
	if err != nil {
		return nil, err
	}
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil, errors.New("批次标识为空")
	}
	var ids []uint
	if err := database.DB.Table(spec.table).
		Where("delete_batch_id = ? AND state IN ?", batchID, []string{trashStateDeleted, models.TrashStateFileGone}).
		Order("id ASC").Pluck("id", &ids).Error; err != nil {
		return nil, fmt.Errorf("读取批次条目失败: %w", err)
	}
	result, err := s.RestoreTrashEntries(kind, ids)
	if result != nil {
		result.BatchID = batchID
	}
	return result, err
}

// PurgeTrashEntries 清除废纸篓里的文件并硬删媒体记录（legacy_trash 行改为墓碑、媒体记录保持软删，修复 K）。
// 仅对 mode 为 trash / legacy_trash 且 state 为 deleted / file_gone 的条目生效；文件存在时先核对身份再删除。
func (s *TrashService) PurgeTrashEntries(kind string, ids []uint) (*BatchResult, error) {
	spec, err := trashKindFor(kind)
	if err != nil {
		return nil, err
	}
	result := newBatchResult(len(ids), "")
	for _, id := range ids {
		result.addOutcome(id, "", s.purgeOne(spec, id))
	}
	return result, nil
}

// RemoveGoneTrashEntries 是列表项 remove_record 动作的入口：移除「废纸篓文件已被清除」（file_gone）的条目，
// 以及文件已放回原处、却已由原位置上另一条活跃记录收录的重复条目（claimed_by_active，deleted、file_gone，
// 或恢复中断的 restoring，I-1 / 修复 I m-c）。硬删记录与条目（legacy 行改为墓碑：claimed 见修复 I I-A，file_gone 见修复 K），
// 不动文件。
func (s *TrashService) RemoveGoneTrashEntries(kind string, ids []uint) (*BatchResult, error) {
	spec, err := trashKindFor(kind)
	if err != nil {
		return nil, err
	}
	result := newBatchResult(len(ids), "")
	for _, id := range ids {
		result.addOutcome(id, "", s.removeGoneOne(spec, id))
	}
	return result, nil
}

// ForceRemoveTrashRecords 是「仍然移除记录（不动文件）」（m3）：离线卷、读不到的位置等清除与移除记录都拒绝的
// 条目的出口。只硬删媒体记录与条目（legacy_trash 行改为墓碑、媒体记录保持软删，修复 I I-A），不做任何文件操作，
// 也不要求卷在线；文件（若还在废纸篓或原处）原样保留，由用户自行处理。confirmText 必须原样等于
// TrashForceRemoveConfirmText（「移除记录」），否则整批拒绝。
//
// 只对 trash / legacy_trash / missing 模式开放（修复 I m-e）：record_only 的条目是「只删记录」留下的按身份屏蔽，
// 返回 ErrTrashForceRemoveModeNotAllowed（not_purgeable，文案说明改用「允许重新收录」）。
//
// 只接受 deleted / file_gone 的条目：停在中断的删除或恢复里的条目（pending_move / restoring / rollback）
// 对应的记录可能仍是活跃的，要先由恢复或启动对账处理完（结果码 not_purgeable）。唯一的例外是恢复中断、
// 文件已在原处、原位置已由另一条活跃记录收录的 restoring 行（claimed_by_active，修复 I m-c）：启动对账永远
// 处理不了它，这里按 remove_record 同样移除。
func (s *TrashService) ForceRemoveTrashRecords(kind string, ids []uint, confirmText string) (*BatchResult, error) {
	spec, err := trashKindFor(kind)
	if err != nil {
		return nil, err
	}
	if confirmText != TrashForceRemoveConfirmText {
		return nil, ErrTrashForceRemoveUnconfirmed
	}
	result := newBatchResult(len(ids), "")
	for _, id := range ids {
		result.addOutcome(id, "", s.forceRemoveOne(spec, id))
	}
	return result, nil
}

func (s *TrashService) forceRemoveOne(spec trashKindSpec, id uint) error {
	unlock, err := lockTrashKind(spec)
	if err != nil {
		return err
	}
	defer unlock()

	row, _, _, err := loadTrashRow(spec, id)
	if err != nil {
		return err
	}
	if !forceRemovableMode(row) {
		return ErrTrashForceRemoveModeNotAllowed
	}
	switch row.State {
	case trashStateDeleted, models.TrashStateFileGone:
	case trashStateRestoring:
		claimed, err := claimedByActive(spec, row)
		if err != nil {
			return err
		}
		if !claimed {
			return ErrTrashRecordNotRemovable
		}
		return removeClaimedDuplicate(spec, row)
	default:
		return ErrTrashRecordNotRemovable
	}
	if err := database.Transaction(func(tx *gorm.DB) error {
		return removeTrashRecordTx(tx, spec, row)
	}); err != nil {
		return err
	}
	log.Printf("仍然移除回收站记录（不动文件） kind=%s entry=%d entity=%d mode=%s state=%s", spec.kind, row.ID, row.EntityID, row.Mode, row.State)
	return nil
}

// forceRemovableMode 报告条目的模式是否允许「仍然移除记录」（修复 I m-e）：trash、legacy_trash（含回填之前
// file_moved=true 的空 mode 旧行）与 missing。record_only 与回填之前的其他空 mode 行都不允许。
func forceRemovableMode(row trashRow) bool {
	switch row.Mode {
	case models.TrashModeTrash, models.TrashModeLegacyTrash, models.TrashModeMissing:
		return true
	}
	return row.isLegacyTrash()
}

// isLegacyTrash 报告这一行是不是旧版同目录 trash/ 的条目（isLegacyTrashFacts，与 loadLegacyTrashDirs 的登记口径一致）。
func (r trashRow) isLegacyTrash() bool {
	return isLegacyTrashFacts(r.putBackFacts())
}

// removeTrashRecordTx 是「移除记录」类操作（仍然移除记录、claimed_by_active 的 remove_record、file_gone 的
// remove_record）对一行的数据库处理，不做任何文件操作。条件是这一行此刻仍是读出时的状态（row.State），
// 处理方式见 retireTrashEntryTx。
func removeTrashRecordTx(tx *gorm.DB, spec trashKindSpec, row trashRow) error {
	return retireTrashEntryTx(tx, spec, row, []string{row.State})
}

// retireTrashEntryTx 是回收站条目「从回收站里拿掉」的唯一数据库实现（移除记录各入口与清除共用），条件是条目此刻的
// 状态仍在 states 之中：
//   - legacy 行（legacy_trash，或回填之前 file_moved=true 的空 mode 旧行）改为墓碑（tombstoneLegacyTrashEntryTx，
//     修复 I I-A；清除与 file_gone 的移除记录同样如此，修复 K）：条目与 trash_path 保留、旧版 trash/ 目录继续登记，
//     媒体记录保持软删；
//   - 其余模式硬删条目与媒体记录（hardDeleteEntityTx，级联生效）。
func retireTrashEntryTx(tx *gorm.DB, spec trashKindSpec, row trashRow, states []string) error {
	if row.isLegacyTrash() {
		return tombstoneLegacyTrashEntryTx(tx, spec, row, states)
	}
	return hardDeleteEntityTx(tx, spec, row, states)
}

// tombstoneLegacyTrashEntryTx 把一条 legacy_trash 条目置为墓碑（trashStateRemoved，修复 I I-A），不硬删：
//   - 条目用条件更新（WHERE state IN states），状态已变化则整笔回滚；
//   - 条目引用的媒体记录此刻若是活跃的（状态不一致），整笔回滚——与硬删同一条安全线；
//   - 媒体记录保持软删（不硬删、不可见）；它不会再被恢复，所以一并解除它的标签关联，让标签用量里的
//     「回收站中」计数（GetTagUsageCounts 的 trashed_*）不再数到它，与硬删时的级联一致；
//   - trash_path 保留：loadLegacyTrashDirs 靠它把旧版 trash/ 目录继续算作「已登记」，里面残留的文件（原文件、
//     与原路径同 inode 的另一个名字）不会被扫描当成新文件收录。
//
// 不做任何文件操作。
func tombstoneLegacyTrashEntryTx(tx *gorm.DB, spec trashKindSpec, row trashRow, states []string) error {
	result := tx.Table(spec.table).
		Where("id = ? AND state IN ?", row.ID, states).
		Updates(map[string]interface{}{"state": trashStateRemoved, "last_error": "", "updated_at": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("回收站条目状态已变化: %d", row.ID)
	}
	var active int64
	if err := tx.Table(spec.mediaTable).Where("id = ? AND deleted_at IS NULL", row.EntityID).Count(&active).Error; err != nil {
		return err
	}
	if active != 0 {
		return fmt.Errorf("回收站条目对应的记录仍在库中，未做任何改动: %d", row.ID)
	}
	if spec.kind == trashKindVideo {
		return tx.Exec("DELETE FROM video_tags WHERE video_id = ?", row.EntityID).Error
	}
	return tx.Exec("DELETE FROM image_tags WHERE image_id = ?", row.EntityID).Error
}

// sweepGoneTrashTombstones 清理旧版 trash/ 目录已经不存在的墓碑（修复 L m4），由启动对账（ReconcileTrashEntries /
// ReconcileImageTrashEntries，持路径写锁）调用。墓碑唯一的用处是让 loadLegacyTrashDirs 把 filepath.Dir(trash_path)
// 算作「已登记」的旧版回收站目录；目录已经不在，就没有什么可登记、也没有残留文件可挡，墓碑可以删：
//   - 条目硬删；它引用的媒体记录若仍是软删（墓碑的常态），一并硬删（hardDeleteVideoTx / hardDeleteImageTx，级联生效）；
//   - 媒体记录是活跃的（恢复成功但残留名字没清掉留下的墓碑，修复 L m5）只删条目，记录不动。
//
// 「目录不存在」按代码现有口径判定（tombstoneTrashDirGone）：Lstat 报不存在，且那个位置所在的卷与扫描根可用
// （mediaPathUnavailable 为 nil）——卷离线或因权限读不到时 Lstat 同样报不存在，那不是「目录没了」，墓碑保留。
// trash_path 为空的墓碑不登记任何目录，同样清理。每个目录只检查一次。单行失败只记入返回的错误，不影响其他行。
func sweepGoneTrashTombstones(spec trashKindSpec) error {
	var rows []trashRow
	if err := database.DB.Table(spec.table).Select(spec.selectColumns()).
		Where("state = ?", trashStateRemoved).Order("id ASC").Scan(&rows).Error; err != nil {
		return fmt.Errorf("读取回收站墓碑失败: %w", err)
	}
	gone := make(map[string]bool)
	var sweepErrors []error
	for _, row := range rows {
		if !tombstoneTrashDirGone(row, gone) {
			continue
		}
		if err := database.Transaction(func(tx *gorm.DB) error {
			return dropTrashTombstoneTx(tx, spec, row)
		}); err != nil {
			sweepErrors = append(sweepErrors, fmt.Errorf("清理回收站墓碑 %d 失败: %w", row.ID, err))
			continue
		}
		log.Printf("旧版回收站目录已不存在，清理墓碑 kind=%s entry=%d entity=%d", spec.kind, row.ID, row.EntityID)
	}
	return errors.Join(sweepErrors...)
}

// tombstoneTrashDirGone 报告墓碑登记的旧版 trash/ 目录是否已经不存在（见 sweepGoneTrashTombstones）；cache 按目录记住结果。
func tombstoneTrashDirGone(row trashRow, cache map[string]bool) bool {
	trashPath := strings.TrimSpace(row.TrashPath)
	if trashPath == "" {
		return true
	}
	dir := filepath.Dir(filepath.Clean(trashPath))
	if gone, known := cache[dir]; known {
		return gone
	}
	gone := false
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		gone = mediaPathUnavailable(dir) == nil
	}
	cache[dir] = gone
	return gone
}

// dropTrashTombstoneTx 删掉一条墓碑（条件删除 WHERE state='removed'，状态已变化则整笔回滚），媒体记录仍是软删时一并硬删；
// 媒体记录是活跃的（修复 L m5 的墓碑）只删条目。不做任何文件操作。
func dropTrashTombstoneTx(tx *gorm.DB, spec trashKindSpec, row trashRow) error {
	result := tx.Exec("DELETE FROM "+spec.table+" WHERE id = ? AND state = ?", row.ID, trashStateRemoved)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: %d", errTrashEntryStateChanged, row.ID)
	}
	var softDeleted int64
	if err := tx.Table(spec.mediaTable).Where("id = ? AND deleted_at IS NOT NULL", row.EntityID).Count(&softDeleted).Error; err != nil {
		return err
	}
	if softDeleted == 0 {
		return nil
	}
	if spec.kind == trashKindVideo {
		return hardDeleteVideoTx(tx, row.EntityID)
	}
	return hardDeleteImageTx(tx, row.EntityID)
}

// lockTrashKind 为一项回收站写操作拿对应媒体的路径写锁。视频锁经 lockLibraryPaths：维护围栏生效时立即返回
// database.ErrMaintenance，不在「待重启」终态下永久等锁（修复 K）。图片锁不被维护入口持有，照常等待。
func lockTrashKind(spec trashKindSpec) (func(), error) {
	if spec.kind == trashKindVideo {
		return lockLibraryPaths()
	}
	imagePathMutationMu.Lock()
	return imagePathMutationMu.Unlock, nil
}

func loadTrashRow(spec trashKindSpec, id uint) (trashRow, trashFileID, string, error) {
	var row trashRow
	result := database.DB.Table(spec.table).Select(spec.selectColumns()).Where("id = ?", id).Limit(1).Scan(&row)
	if result.Error != nil {
		return row, trashFileID{}, "", result.Error
	}
	// 墓碑（I-A）对回收站接口一律视为不存在：它不出现在列表里，也没有任何可做的操作（修复 L m6：与恢复同一个错误）。
	if result.RowsAffected == 0 || row.State == trashStateRemoved {
		return row, trashFileID{}, "", errTrashEntryNotFound(id)
	}
	var extra struct {
		FileIdentity string
		FileSHA256   string
	}
	if err := database.DB.Table(spec.table).Select("file_identity, file_sha256").Where("id = ?", id).Scan(&extra).Error; err != nil {
		return row, trashFileID{}, "", err
	}
	return row, trashFileID{Size: row.FileSize, ModTimeNS: row.FileModTime, Identity: extra.FileIdentity}, extra.FileSHA256, nil
}

// hardDeleteEntityTx 硬删条目与媒体记录（级联生效）。条目用条件删除：状态已变化则不删。
// 条目引用的媒体记录此刻若是活跃的（状态不一致），整笔回滚：回收站入口绝不硬删一条仍在库里的记录。
func hardDeleteEntityTx(tx *gorm.DB, spec trashKindSpec, row trashRow, states []string) error {
	result := tx.Exec("DELETE FROM "+spec.table+" WHERE id = ? AND state IN ?", row.ID, states)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("回收站条目状态已变化: %d", row.ID)
	}
	var active int64
	if err := tx.Table(spec.mediaTable).Where("id = ? AND deleted_at IS NULL", row.EntityID).Count(&active).Error; err != nil {
		return err
	}
	if active != 0 {
		return fmt.Errorf("回收站条目对应的记录仍在库中，未做任何改动: %d", row.ID)
	}
	if spec.kind == trashKindVideo {
		return hardDeleteVideoTx(tx, row.EntityID)
	}
	return hardDeleteImageTx(tx, row.EntityID)
}

// ErrVideoHasActiveEnhancement 表示视频还有未结束的超分任务，不能永久删除。
var ErrVideoHasActiveEnhancement = errors.New("该视频还有进行中的超分任务，请先取消或等待完成")

// activeEnhancementStatuses 是超分任务的非终态。
var activeEnhancementStatuses = []string{
	models.EnhancementStatusQueued, models.EnhancementStatusRunning, models.EnhancementStatusCancelRequested,
}

// ensureNoActiveEnhancement 在动文件之前检查：超分任务以 RESTRICT 外键引用源视频，
// 进行中的任务还会读源文件，所以只要有非终态任务就拒绝永久删除。
func ensureNoActiveEnhancement(db *gorm.DB, videoID uint) error {
	var count int64
	if err := db.Model(&models.VideoEnhancementTask{}).
		Where("video_id = ? AND status IN ?", videoID, activeEnhancementStatuses).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrVideoHasActiveEnhancement
	}
	return nil
}

// hardDeleteVideoTx 硬删视频记录。大多数关联表是 ON DELETE CASCADE，但标签关联表
// （video_tags）是 NO ACTION、超分任务（video_enhancement_tasks）是 RESTRICT，必须先清掉，
// 否则删不掉。超分任务只在已结束时随源视频删除（它们是历史记录，产物是独立视频不受影响）；
// 事务内复查一次非终态，与开事务前的检查之间若有新任务入队则整体回滚。
func hardDeleteVideoTx(tx *gorm.DB, id uint) error {
	if err := ensureNoActiveEnhancement(tx, id); err != nil {
		return err
	}
	if err := tx.Where("video_id = ?", id).Delete(&models.VideoEnhancementTask{}).Error; err != nil {
		return err
	}
	if err := tx.Exec("DELETE FROM video_tags WHERE video_id = ?", id).Error; err != nil {
		return err
	}
	return tx.Unscoped().Delete(&models.Video{}, id).Error
}

// hardDeleteImageTx 硬删图片记录，同样要先清 image_tags（NO ACTION）。
func hardDeleteImageTx(tx *gorm.DB, id uint) error {
	if err := tx.Exec("DELETE FROM image_tags WHERE image_id = ?", id).Error; err != nil {
		return err
	}
	return tx.Unscoped().Delete(&models.Image{}, id).Error
}

func (s *TrashService) purgeOne(spec trashKindSpec, id uint) error {
	unlock, err := lockTrashKind(spec)
	if err != nil {
		return err
	}
	defer unlock()

	row, want, sha, err := loadTrashRow(spec, id)
	if err != nil {
		return err
	}
	if !(row.Mode == models.TrashModeTrash || row.Mode == models.TrashModeLegacyTrash) ||
		!(row.State == trashStateDeleted || row.State == models.TrashStateFileGone) {
		return ErrTrashNotPurgeable
	}
	if spec.kind == trashKindVideo {
		if err := ensureNoActiveEnhancement(database.DB, row.EntityID); err != nil {
			return err
		}
	}
	// 放回判定之前先查占用（I-1）：文件已放回原处、却已由原位置上另一条活跃记录收录时，这一行只是重复的旧记录，
	// 只硬删这一行及其条目，绝不动任何文件（原路径上的文件属于那条活跃记录）。
	claimed, err := claimedByActive(spec, row)
	if err != nil {
		return err
	}
	if claimed {
		return removeClaimedDuplicate(spec, row)
	}
	// 文件被放回了原处（只看原路径上的文件身份，I-A）：清除只会删掉记录而文件还在原地，应改用恢复。
	// 废纸篓里同时还有一个同 inode 的名字（硬链接）时同样拒绝，不能删掉那个名字后再硬删记录。
	if trashRowPutBack(row) {
		return ErrTrashPutBack
	}
	// 原位置是指向废纸篓里这个文件的符号链接（修复 I m-a）：清除就是删掉用户经原位置仍在使用的唯一一份内容，
	// 拒绝（path_occupied）。恢复同样拒绝（原位置不是普通文件），列表对这种行两者都不提供。
	if originalSymlinkToTrashFile(row.OriginalPath, row.TrashPath) {
		return ErrTrashOriginalNotRegular
	}
	if strings.TrimSpace(row.TrashPath) == "" {
		// trash_path 为空（崩溃恢复分支 3「文件位置未知」）：与其他清除入口一样，硬删前确认位置可用（M2）。
		if err := trashEntryUnavailable(row.OriginalPath, ""); err != nil {
			return err
		}
	} else {
		info, statErr := os.Stat(row.TrashPath)
		switch {
		case statErr == nil:
			if info.IsDir() {
				return ErrTrashIdentityMismatch
			}
			matched := want.strictMatch(info)
			if row.Mode == models.TrashModeLegacyTrash {
				matched = legacyTrashFileMatches(row.TrashPath, info, want, sha)
			}
			if !matched {
				return ErrTrashIdentityMismatch
			}
			if err := os.Remove(row.TrashPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				if errors.Is(err, os.ErrPermission) {
					return ErrTrashPermissionDenied
				}
				return fmt.Errorf("清除文件失败: %w", err)
			}
		case errors.Is(statErr, os.ErrNotExist):
			// 卷离线时 stat 也报不存在：那不是「文件已消失」，不能据此硬删记录（Minor 3）。
			if err := trashEntryUnavailable(row.OriginalPath, row.TrashPath); err != nil {
				return err
			}
			// 文件确实已经不在（放回原处已在上面排除）：只清记录。
		default:
			return fmt.Errorf("检查回收站文件失败: %w", statErr)
		}
	}
	// 文件已删掉（或确实已不在）之后：非 legacy 行硬删条目与媒体记录；legacy 行改为墓碑、媒体记录保持软删，
	// 让旧版 trash/ 目录继续登记，里面残留的其他文件不会被下一轮扫描当成新文件收录（修复 K，与 I-A 同一口径）。
	return database.Transaction(func(tx *gorm.DB) error {
		return retireTrashEntryTx(tx, spec, row, []string{trashStateDeleted, models.TrashStateFileGone})
	})
}

// removeClaimedDuplicate 移除一条「文件已由原位置上另一条活跃记录收录」的重复旧记录（I-1）：硬删这一行及其条目；
// legacy_trash 行改为墓碑、媒体记录保持软删（修复 I I-A）。只动数据库：原路径上的文件属于那条活跃记录，
// 废纸篓（或旧版 trash/）一侧若还有同 inode 的名字也不碰——旧版 trash/ 目录因墓碑继续登记，扫描不会把那个名字
// 当成新文件收录。条件是这一行此刻仍是读出时的状态（deleted、file_gone，或恢复中断的 restoring，修复 I m-c）。
func removeClaimedDuplicate(spec trashKindSpec, row trashRow) error {
	if err := database.Transaction(func(tx *gorm.DB) error {
		return removeTrashRecordTx(tx, spec, row)
	}); err != nil {
		return err
	}
	log.Printf("移除已由活跃记录收录的重复回收站条目 kind=%s entry=%d entity=%d mode=%s state=%s", spec.kind, row.ID, row.EntityID, row.Mode, row.State)
	return nil
}

func (s *TrashService) removeGoneOne(spec trashKindSpec, id uint) error {
	unlock, err := lockTrashKind(spec)
	if err != nil {
		return err
	}
	defer unlock()

	row, _, _, err := loadTrashRow(spec, id)
	if err != nil {
		return err
	}
	// 放回判定之前先查占用（I-1）：列表对这类行（deleted 或 file_gone）只提供 remove_record，
	// 这里只硬删这一行及其条目，不动文件。
	claimed, err := claimedByActive(spec, row)
	if err != nil {
		return err
	}
	if claimed {
		return removeClaimedDuplicate(spec, row)
	}
	if row.State != models.TrashStateFileGone {
		return ErrTrashNotPurgeable
	}
	// 与清除共用硬删前的检查（M2）：位置离线时 file_gone 的依据不可靠，不硬删；文件被放回了原处时
	// 硬删会让原地的文件失去记录，应改用恢复。
	if err := trashEntryUnavailable(row.OriginalPath, row.TrashPath); err != nil {
		return err
	}
	if trashRowPutBack(row) {
		return ErrTrashPutBack
	}
	// 非 legacy 行硬删；legacy 行改为墓碑，旧版 trash/ 目录继续登记（修复 K，与 I-A 同一口径）。
	return database.Transaction(func(tx *gorm.DB) error {
		return removeTrashRecordTx(tx, spec, row)
	})
}

// ===== 迁移残留（D-PC05 的移动部分；登记由 P-011 负责） =====

// TrashStagedSources 把迁移残留的暂存源文件经系统废纸篓移走，成功后置 cleaned。
// 文件已经不在的行直接置 cleaned（等同已清理）。状态用条件更新。
func (s *TrashService) TrashStagedSources(ids []uint) *BatchResult {
	result := newBatchResult(len(ids), "")
	for _, id := range ids {
		code, err := s.trashStagedSource(id)
		result.addOutcome(id, code, err)
	}
	return result
}

func (s *TrashService) trashStagedSource(id uint) (string, error) {
	var source models.MigrationStagedSource
	find := database.DB.Where("id = ?", id).Limit(1).Find(&source)
	if find.Error != nil {
		return "", find.Error
	}
	if find.RowsAffected == 0 {
		return "", fmt.Errorf("迁移残留不存在: %d", id)
	}
	if source.State != models.MigrationStagedStatePending {
		return TrashResultOK, nil
	}
	code := TrashResultOK
	// 不走 MoveToTrash：它对「路径里带 trash 目录名」的文件原样放行、不真的移动，这里必须真的移走。
	// 暂存源可能是文件也可能是文件夹（跨盘迁移文件夹时），trashItemAtURL 两者都支持（I4）。
	if _, statErr := os.Lstat(source.StagedPath); statErr != nil {
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("检查迁移残留失败: %w", statErr)
		}
		// 卷离线（或因权限读不到）不是「文件没了」：不置 cleaned（I1、m3）。
		if err := mediaPathUnavailable(source.StagedPath); err != nil {
			return "", err
		}
		code = TrashResultFileMissing
	} else if _, err := systemTrashMove(source.StagedPath); err != nil {
		return "", err
	}
	now := s.now()
	result := database.DB.Model(&models.MigrationStagedSource{}).
		Where("id = ? AND state = ?", source.ID, models.MigrationStagedStatePending).
		Updates(map[string]interface{}{"state": models.MigrationStagedStateCleaned, "cleaned_at": now})
	if result.Error != nil {
		return "", result.Error
	}
	return code, nil
}
