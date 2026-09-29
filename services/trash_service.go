package services

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	// ErrTrashEntryNotRestorable：条目当前状态不允许恢复。
	ErrTrashEntryNotRestorable = errors.New("该条目当前不可恢复")
)

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
	TrashResultNotRestorable     = "not_restorable"
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
	if isTrashPath(srcPath) {
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
	case errors.Is(err, ErrTrashPathOccupied):
		return TrashResultPathOccupied
	case errors.Is(err, ErrTrashIdentityMismatch):
		return TrashResultIdentityMismatch
	case errors.Is(err, ErrTrashNotPurgeable), errors.Is(err, ErrPermanentDeleteNotActive), errors.Is(err, ErrPermanentDeleteHasTrashEntry):
		return TrashResultNotPurgeable
	case errors.Is(err, ErrTrashFileGone):
		return TrashResultFileGone
	case errors.Is(err, ErrTrashEntryNotRestorable):
		return TrashResultNotRestorable
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
	kind      string
	table     string
	entityCol string
	nameCol   string
}

var (
	videoTrashKind = trashKindSpec{kind: trashKindVideo, table: "video_trash_entries", entityCol: "video_id", nameCol: "video_name"}
	imageTrashKind = trashKindSpec{kind: trashKindImage, table: "image_trash_entries", entityCol: "image_id", nameCol: "image_name"}
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
	Actions       []string  `json:"actions"`
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
func (s *TrashService) ListTrashEntries(filter TrashFilter) (*TrashPage, error) {
	spec, err := trashKindFor(filter.Kind)
	if err != nil {
		return nil, err
	}
	limit := normalizeEntityPageLimit(filter.Limit)
	query := database.DB.Table(spec.table).Select(spec.selectColumns())
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
	for _, row := range rows {
		if (row.State == trashStateDeleted || row.State == models.TrashStateFileGone) && row.holdsTrashFile() {
			outcome, err := s.reconcileListedRow(spec, &row)
			if err != nil {
				return nil, err
			}
			if outcome == listedRowRestored {
				// 用户在访达里「放回原处」：记录已就地恢复，条目随之消失，不再出现在回收站列表里。
				continue
			}
		}
		page.Items = append(page.Items, TrashEntryView{
			ID: row.ID, Kind: spec.kind, EntityID: row.EntityID, Name: row.Name,
			OriginalPath: row.OriginalPath, TrashPath: row.TrashPath, FileMoved: row.FileMoved,
			FileSize: row.FileSize, State: row.State, Mode: row.Mode, DeletedBy: row.DeletedBy,
			DeleteBatchID: row.DeleteBatchID, LastError: row.LastError, CreatedAt: row.CreatedAt,
			Actions: row.actions(),
		})
	}
	if len(page.Items) > 0 {
		page.NextCursor = page.Items[len(page.Items)-1].ID
	}
	return page, nil
}

type listedRowOutcome int

const (
	listedRowUnchanged listedRowOutcome = iota
	listedRowMarked
	listedRowRestored
)

// reconcileListedRow 对列表当页里「应有文件」的行做对账（详细设计 §2.1）：
//
//   - 废纸篓里的文件还在且身份一致 → 不动；
//   - 文件还在但身份不符（被别的同名文件占了）→ file_gone + 「废纸篓中的文件已被替换」；
//   - 文件不在：先确认所在卷在线（卷离线不是「文件没了」，保持原状，I1）；
//     原路径上有身份一致的文件 → 用户在访达里放回了原处，就地恢复记录（I3）；
//     否则 file_gone。
//
// 状态转换全部是条件更新（WHERE state='deleted'）。
func (s *TrashService) reconcileListedRow(spec trashKindSpec, row *trashRow) (listedRowOutcome, error) {
	want := trashFileID{Size: row.FileSize, ModTimeNS: row.FileModTime, Identity: row.FileIdentity}
	if row.State == models.TrashStateFileGone {
		return s.reviveGoneRow(spec, row, want)
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
	if canDetectPutBack(row.Mode, want) && strings.TrimSpace(row.OriginalPath) != "" {
		if info, exists, err := regularFileState(row.OriginalPath); err == nil && exists && want.strictMatch(info) {
			if restoreErr := s.restoreOne(spec.kind, row.ID); restoreErr == nil {
				return listedRowRestored, nil
			}
			// 原位置已有别的记录等情形：文件确实在原路径，不能标成 file_gone，保持原样等用户处理。
			return listedRowUnchanged, nil
		}
	}
	return s.markListedRowGone(spec, row, "")
}

// reviveGoneRow 处理已标成 file_gone 的行：文件重新出现（身份一致）时改回 deleted——
// 要么废纸篓路径上又有了它（例如卷重新挂载），要么被放回了原处（此时直接恢复记录）。
func (s *TrashService) reviveGoneRow(spec trashKindSpec, row *trashRow, want trashFileID) (listedRowOutcome, error) {
	revive := func() (listedRowOutcome, error) {
		result := database.DB.Table(spec.table).
			Where("id = ? AND state = ?", row.ID, models.TrashStateFileGone).
			Updates(map[string]interface{}{"state": trashStateDeleted, "last_error": ""})
		if result.Error != nil {
			return listedRowUnchanged, fmt.Errorf("对账回收站条目失败: %w", result.Error)
		}
		if result.RowsAffected == 1 {
			row.State = trashStateDeleted
			row.LastError = ""
		}
		return listedRowMarked, nil
	}
	if strings.TrimSpace(row.TrashPath) != "" {
		if info, err := os.Stat(row.TrashPath); err == nil && !info.IsDir() && trashRowFileMatches(*row, want, info) && !want.empty() {
			return revive()
		}
	}
	if canDetectPutBack(row.Mode, want) && strings.TrimSpace(row.OriginalPath) != "" {
		if info, exists, err := regularFileState(row.OriginalPath); err == nil && exists && want.strictMatch(info) {
			if outcome, err := revive(); err != nil {
				return outcome, err
			}
			if restoreErr := s.restoreOne(spec.kind, row.ID); restoreErr == nil {
				return listedRowRestored, nil
			}
		}
	}
	return listedRowUnchanged, nil
}

// canDetectPutBack 报告能否用身份判定「访达放回原处」：只有 trash 模式（record_only 的文件从未移动过）且记录了 inode 与 mtime
// 的条目才有足够的身份；legacy_trash 与历史行只有大小，不足以认定原路径上的文件就是它。
func canDetectPutBack(mode string, want trashFileID) bool {
	return mode == models.TrashModeTrash && want.Identity != "" && want.ModTimeNS != 0 && want.Size != 0
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

// pathVolumeOnline 判断 path 所在的卷是否在线：（macOS 上）卷确实挂载，且父目录可 Stat。
func pathVolumeOnline(path string) bool {
	path = filepath.Clean(path)
	if scanVolumeAvailable(path) != nil {
		return false
	}
	info, err := os.Stat(filepath.Dir(path))
	return err == nil && info.IsDir()
}

// scanRootsOfflineForPath 报告 path 是否属于至少一个视频或图片扫描根，且这些根都不在线。
// 读不到扫描目录时按离线处理：宁可不改写状态。不属于任何根时返回 false（那是文件位置问题，不是盘没插）。
func scanRootsOfflineForPath(path string) bool {
	path = filepath.Clean(path)
	var roots []string
	var videoDirs []models.ScanDirectory
	if err := database.DB.Find(&videoDirs).Error; err != nil {
		return true
	}
	roots = append(roots, cleanScanRoots(videoDirs)...)
	var imageDirs []models.ImageDirectory
	if err := database.DB.Find(&imageDirs).Error; err != nil {
		return true
	}
	for _, dir := range imageDirs {
		root := filepath.Clean(strings.TrimSpace(dir.Path))
		if root != "" && root != "." {
			roots = append(roots, root)
		}
	}
	containing := 0
	for _, root := range roots {
		if path != root && !strings.HasPrefix(path, scanRootChildPrefix(root)) {
			continue
		}
		containing++
		if scanRootOnline(root) {
			return false
		}
	}
	return containing > 0
}

// mediaPathOffline 判断媒体文件所在位置当前是否离线：卷未挂载，或包含它的扫描根（视频或图片）
// 都不在线。找不到归属的根时按在线处理（那是「文件缺失」，不是「盘没插」）。用于「文件不在」时
// 区分文件真的没了与卷没挂载：后者不能当成删除依据，也不能降级（I6）。
func mediaPathOffline(path string) bool {
	return scanVolumeAvailable(filepath.Clean(path)) != nil || scanRootsOfflineForPath(path)
}

// filePathOnline 是「文件所在卷在线」的统一判断：卷已挂载、父目录可 Stat、所属扫描根在线。
func filePathOnline(path string) bool {
	return pathVolumeOnline(path) && !scanRootsOfflineForPath(path)
}

// trashRowVolumeOnline 判断条目文件所在的卷是否在线。废纸篓里的文件以 trash_path 为准；
// legacy_trash 行的 trash/ 目录可能已被用户删掉，以原目录为准。
func trashRowVolumeOnline(row trashRow) bool {
	anchor := row.TrashPath
	if isLegacyTrashMode(row.Mode) || strings.TrimSpace(anchor) == "" {
		anchor = row.OriginalPath
	}
	return filePathOnline(anchor) && !scanRootsOfflineForPath(row.OriginalPath)
}

// TrashKindUsage 是某一类媒体的回收站用量。BytesInTrash 只计 state=deleted 且文件真的在
// 废纸篓（trash / legacy_trash）里的条目。
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

func trashKindUsage(spec trashKindSpec) (TrashKindUsage, error) {
	var usage TrashKindUsage
	modes := []string{models.TrashModeTrash, models.TrashModeLegacyTrash}
	scan := func(dest *int64, expr string, query *gorm.DB) error {
		return query.Select(expr).Scan(dest).Error
	}
	db := func() *gorm.DB { return database.DB.Table(spec.table) }
	if err := scan(&usage.Count, "COUNT(*)", db().Where("state IN ?", []string{trashStateDeleted, models.TrashStateFileGone})); err != nil {
		return usage, err
	}
	if err := scan(&usage.BytesInTrash, "CAST(COALESCE(SUM(file_size), 0) AS BIGINT)", db().Where("state = ? AND mode IN ?", trashStateDeleted, modes)); err != nil {
		return usage, err
	}
	if err := scan(&usage.GoneCount, "COUNT(*)", db().Where("state = ?", models.TrashStateFileGone)); err != nil {
		return usage, err
	}
	if err := scan(&usage.LegacyCount, "COUNT(*)", db().Where("state = ? AND mode = ?", trashStateDeleted, models.TrashModeLegacyTrash)); err != nil {
		return usage, err
	}
	if err := scan(&usage.LegacyBytes, "CAST(COALESCE(SUM(file_size), 0) AS BIGINT)", db().Where("state = ? AND mode = ?", trashStateDeleted, models.TrashModeLegacyTrash)); err != nil {
		return usage, err
	}
	return usage, nil
}

// GetTrashUsage 返回回收站用量：视频、图片、迁移残留。
func (s *TrashService) GetTrashUsage() (*TrashUsage, error) {
	usage := &TrashUsage{}
	var err error
	if usage.Video, err = trashKindUsage(videoTrashKind); err != nil {
		return nil, fmt.Errorf("统计视频回收站失败: %w", err)
	}
	if usage.Image, err = trashKindUsage(imageTrashKind); err != nil {
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

// PurgeTrashEntries 清除废纸篓里的文件并硬删媒体记录。仅对 mode 为 trash / legacy_trash 且
// state 为 deleted / file_gone 的条目生效；文件存在时先核对身份再删除。
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

// RemoveGoneTrashEntries 移除「废纸篓文件已被清除」的条目：硬删记录与条目，不动文件。
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

func lockTrashKind(spec trashKindSpec) func() {
	if spec.kind == trashKindVideo {
		libraryPathMutationMu.Lock()
		return libraryPathMutationMu.Unlock
	}
	imagePathMutationMu.Lock()
	return imagePathMutationMu.Unlock
}

func loadTrashRow(spec trashKindSpec, id uint) (trashRow, trashFileID, string, error) {
	var row trashRow
	result := database.DB.Table(spec.table).Select(spec.selectColumns()).Where("id = ?", id).Limit(1).Scan(&row)
	if result.Error != nil {
		return row, trashFileID{}, "", result.Error
	}
	if result.RowsAffected == 0 {
		return row, trashFileID{}, "", fmt.Errorf("回收站条目不存在: %d", id)
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
func hardDeleteEntityTx(tx *gorm.DB, spec trashKindSpec, row trashRow, states []string) error {
	result := tx.Exec("DELETE FROM "+spec.table+" WHERE id = ? AND state IN ?", row.ID, states)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("回收站条目状态已变化: %d", row.ID)
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
	unlock := lockTrashKind(spec)
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
	if strings.TrimSpace(row.TrashPath) != "" {
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
			// 文件已经不在：只清记录。
		default:
			return fmt.Errorf("检查回收站文件失败: %w", statErr)
		}
	}
	return database.Transaction(func(tx *gorm.DB) error {
		return hardDeleteEntityTx(tx, spec, row, []string{trashStateDeleted, models.TrashStateFileGone})
	})
}

func (s *TrashService) removeGoneOne(spec trashKindSpec, id uint) error {
	unlock := lockTrashKind(spec)
	defer unlock()

	row, _, _, err := loadTrashRow(spec, id)
	if err != nil {
		return err
	}
	if row.State != models.TrashStateFileGone {
		return ErrTrashNotPurgeable
	}
	return database.Transaction(func(tx *gorm.DB) error {
		return hardDeleteEntityTx(tx, spec, row, []string{models.TrashStateFileGone})
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
		// 卷离线不是「文件没了」：不置 cleaned（I1）。
		if !filePathOnline(source.StagedPath) {
			return "", ErrTrashVolumeOffline
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
