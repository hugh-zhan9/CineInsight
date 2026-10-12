package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"video-master/database"
)

// BackupReasonFilesInvalid：清单说有文件包，但文件包缺失、大小或哈希对不上、条目读不出（恢复前检查，4.2）。
const BackupReasonFilesInvalid = "backup_files_invalid"

// errBackupFilesNotRestored 是文件包还原失败时交给界面的话（4.2）：数据库已经替换，不回滚；
// 改名留下的 .pre-restore-* 目录保留不删。不带底层错误原文与路径（G-3），原文只进日志。
var errBackupFilesNotRestored = errors.New("数据库已恢复，整理过的文件未能还原，可从恢复前的安全备份中取回")

var errBackupFilesInvalid = fmt.Errorf("%s: 这份备份的文件包缺失或已损坏，数据库未被修改；可先「校验」确认，或换一份备份", BackupReasonFilesInvalid)

// BackupReasonFilesTargetUnavailable：文件包要还原的某个收录目录现在不可用（失效的链接、没挂载的盘）。
const BackupReasonFilesTargetUnavailable = "backup_files_target_unavailable"

var errBackupFilesTargetUnavailable = fmt.Errorf("%s: 整理文件所在的某个目录现在不可用（链接失效或磁盘没挂载），为免把文件还原到错误的位置，数据库未被修改；接上磁盘或修好链接后再恢复",
	BackupReasonFilesTargetUnavailable)

const (
	backupFilesStagingPrefix = ".cineinsight-restore-"
	backupFilesAsideMarker   = ".pre-restore-"
)

// restorableManifest 读出待恢复备份的清单并做恢复前的全部检查（4.2），都在进入维护之前：
//   - 旧格式备份返回 nil（只恢复数据库、不动整理文件目录）；清单坏了拒绝；
//   - 清单 models 里有当前版本不认识的表或列 → backup_from_newer_version；
//   - 文件包缺失、大小或哈希与清单不符、条目读不出 → backup_files_invalid；
//   - 要还原的收录目录现在不可用 → backup_files_target_unavailable。
//
// 空间由调用方随后的 checkRestoreSpace 按卷合计（backup_insufficient_space）。
//
// 任何一条不过都不碰数据库与文件，用户拿到的是一句说得清的话。
func (s *BackupService) restorableManifest(ctx context.Context, directory, name string, backend database.Backend) (*BackupManifest, error) {
	if filepath.Base(name) != name || !isBackupFileName(name) {
		return nil, nil // 文件名由 copyVerifiedBackup 统一拒绝
	}
	manifest, err := loadBackupManifest(directory, name)
	if errors.Is(err, errBackupManifestMissing) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s，数据库未被修改", err.Error())
	}
	unknown, err := backupSchemaUnknown(manifest)
	if err != nil {
		return nil, fmt.Errorf("读取当前表结构失败，数据库未被修改: %w", err)
	}
	if len(unknown) > 0 {
		if len(unknown) > 5 {
			unknown = append(unknown[:5], "…")
		}
		return nil, fmt.Errorf("%s: 这份备份来自更新版本的应用（当前版本不认识：%s），数据库未被修改",
			BackupReasonFromNewerVersion, strings.Join(unknown, "、"))
	}
	if err := s.checkFilesPackBeforeRestore(directory, manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// checkFilesPackBeforeRestore 在动任何东西之前确认文件包可用：存在、大小与哈希与清单一致、每个条目都能读出且
// 都在清单里（与「校验」同一套读法）；并且文件包要还原的每个收录目录现在都不是「不可用」——失效的链接或没挂载
// 的盘上还原，等于用备份内容顶掉用户的链接（审阅 R2）；链接过去的目录还要写得进去。空间由 checkRestoreSpace 按卷合计。
func (s *BackupService) checkFilesPackBeforeRestore(directory string, manifest *BackupManifest) error {
	packPath := filepath.Join(directory, manifest.Files.Name)
	info, err := os.Lstat(packPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != manifest.Files.Size {
		return errBackupFilesInvalid
	}
	packHash, entryErr, ioErr := readBackupFilesPack(packPath, manifest.Files, nil)
	if ioErr != nil || entryErr != nil || packHash != manifest.Files.SHA256 {
		return errBackupFilesInvalid
	}
	for _, dir := range restoredBackupDirectories(manifest) {
		placement, state, err := s.placeCuratedRoot(dir, false)
		if err != nil || state == curatedRootUnavailable {
			return errBackupFilesTargetUnavailable
		}
		// 按合并还原的目录（链接到别处，或目录里有符号链接）：先试着在里面建、删一个临时文件，写不进去就现在拒绝（第四轮 D1）。
		if placement.Merge && !s.probeLinkedTarget(dir) {
			return errBackupFilesTargetUnavailable
		}
	}
	return nil
}

// restoredBackupDirectories 是这份备份要还原的收录目录：备份时不可用、没打包的目录不在其中，还原时不动它们。
func restoredBackupDirectories(manifest *BackupManifest) []string {
	skipped := map[string]bool{}
	for _, dir := range manifest.UnavailableRoots {
		skipped[dir] = true
	}
	dirs := make([]string, 0, len(backupFilesDirectories))
	for _, dir := range backupFilesDirectories {
		if !skipped[dir] {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// existingAncestor 返回 dir 自身或最近一个存在的上级目录（还没建出来的 media-details 落在数据目录上）。
func (s *BackupService) existingAncestor(dir string) string {
	for current := dir; ; current = filepath.Dir(current) {
		if _, err := os.Stat(current); err == nil || current == filepath.Dir(current) {
			return current
		}
	}
}

func (s *BackupService) renamePath(from, to string) error {
	if s.rename != nil {
		return s.rename(from, to)
	}
	return os.Rename(from, to)
}

// restoreBackupFiles 在维护围栏内还原文件包（4.2）。manifest 为 nil（旧格式备份）时不动这些目录。
// 失败时原文只进日志，交给调用方的是不带路径的固定文案。
func (s *BackupService) restoreBackupFiles(directory string, manifest *BackupManifest) error {
	if manifest == nil {
		return nil
	}
	err := s.swapInBackupFiles(directory, manifest)
	if err != nil {
		log.Printf("还原备份文件包失败 backup=%s err=%v", manifest.Snapshot.Name, err)
	}
	return err
}

// swapInBackupFiles 还原文件包（4.2）。每个要还原的收录目录（备份时不可用的不在其中）先解析到真实位置：
//   - 在数据目录里的：同级建暂存目录，整目录换入（真实目录改名为 <名字>.pre-restore-<时间>，暂存目录就位）；
//   - 链接到数据目录之外的：暂存目录建在数据目录里，解好后逐个文件合并进链接指向的目录（backup_restore_merge.go）。
//
// 顺序：先写 staging 标记，再建暂存目录、把文件包完整解进去并逐条校验；标记改成 swapping 后逐个目录处理，每做完一个
// 更新它在标记里的状态。处理前再查一次该目录仍可用、仍在原来的位置，变了就不动它、这次还原记为不完整（审阅 M6）；
// 换入失败时把刚改走的原目录改回来。全部换好才删掉 .pre-restore-*；有目录没还原（不完整或失败）时已换好目录的
// .pre-restore-* 一律保留（审阅第四轮 D5）。改回也失败时标记留着，下次启动按标记改回。
func (s *BackupService) swapInBackupFiles(directory string, manifest *BackupManifest) error {
	if s.filesRestoreMarkerExists() {
		return errFilesRestoreRecoveryPending // 不覆盖上次没收拾完的现场
	}
	dataRoot, err := s.resolvedDataDir()
	if err != nil {
		return err
	}
	stamp := s.now().Format("20060102-150405.000000000")
	partial := false
	marker := filesRestoreMarker{State: filesRestoreStaging, Stamp: stamp}
	for _, dir := range restoredBackupDirectories(manifest) {
		placement, state, err := s.placeCuratedRoot(dir, true)
		if err != nil {
			return err
		}
		if state == curatedRootUnavailable {
			partial = true
			continue
		}
		target := filesRestoreTarget{Dir: dir, Mode: filesRestoreModeSwap, Parent: placement.Parent, Base: placement.Base,
			Staging: placement.stagingName(stamp), State: filesTargetPending}
		if placement.Merge {
			target.Mode, target.Parent, target.Base = filesRestoreModeMerge, dataRoot, filepath.Base(filepath.FromSlash(dir))
			target.Staging = backupFilesMergeStagingPrefix + target.Base + "-" + stamp
		} else if placement.Exists {
			target.Aside = placement.asideName(stamp)
		}
		marker.Targets = append(marker.Targets, target)
	}
	if err := s.writeFilesRestoreMarker(marker); err != nil {
		return err
	}
	if s.afterMarkerWritten != nil {
		s.afterMarkerWritten()
	}
	keepMarker := false
	defer func() {
		for _, target := range marker.Targets {
			_ = os.RemoveAll(filepath.Join(target.Parent, target.Staging))
		}
		if !keepMarker {
			s.removeFilesRestoreMarker()
		}
	}()
	staged := map[string]string{}
	for _, target := range marker.Targets {
		staged[target.Dir] = filepath.Join(target.Parent, target.Staging)
		if err := os.MkdirAll(staged[target.Dir], 0755); err != nil {
			return pathlessError(err)
		}
	}
	if err := s.unpackFilesPack(directory, manifest, staged); err != nil {
		return err
	}
	marker.State = filesRestoreSwapping
	if err := s.writeFilesRestoreMarker(marker); err != nil {
		return err
	}
	for index := range marker.Targets {
		done, err := s.restoreOneCuratedRoot(&marker, index, staged[marker.Targets[index].Dir])
		if err != nil {
			keepMarker = errors.Is(err, errFilesRenameBackFailed)
			return err
		}
		partial = partial || !done
	}
	if partial {
		return errors.New("有收录目录在还原时不可用、位置变了或没能全部写进去，没有完整还原")
	}
	for index, target := range marker.Targets {
		if target.Mode == filesRestoreModeSwap && target.Aside != "" {
			if err := os.RemoveAll(filepath.Join(target.Parent, target.Aside)); err != nil {
				log.Printf("删除恢复前的旧目录失败 err=%v", err)
				continue
			}
			marker.Targets[index].State = filesTargetAsideDeleted
			s.updateFilesRestoreMarker(marker)
		}
	}
	return nil
}

// restoreOneCuratedRoot 处理一个目录（换入或合并），返回它是否完整还原。处理前再查一次：目录不可用、位置变了、
// 是否链接变了，就不动它（返回未完成）。换入失败返回错误；改回也失败时错误带 errFilesRenameBackFailed，标记要留着。
func (s *BackupService) restoreOneCuratedRoot(marker *filesRestoreMarker, index int, staged string) (bool, error) {
	target := &marker.Targets[index]
	current, state, err := s.placeCuratedRoot(target.Dir, false)
	if err != nil || state == curatedRootUnavailable || current.Merge != (target.Mode == filesRestoreModeMerge) {
		return false, nil
	}
	if target.Mode == filesRestoreModeMerge {
		complete := s.mergeLinkedRoot(target.Dir, staged)
		target.State = filesTargetMerged
		s.updateFilesRestoreMarker(*marker)
		return complete, nil
	}
	original := filepath.Join(target.Parent, target.Base)
	if current.target() != original || current.Exists != (target.Aside != "") {
		return false, nil
	}
	aside := ""
	if target.Aside != "" {
		aside = filepath.Join(target.Parent, target.Aside)
		if err := s.renamePath(original, aside); err != nil {
			return false, err
		}
	}
	if err := s.renamePath(staged, original); err != nil {
		if aside != "" {
			if backErr := s.renamePath(aside, original); backErr != nil {
				log.Printf("文件包就位失败后改回原目录也失败，留待下次启动按标记改回 dir=%s err=%v", target.Dir, backErr)
				return false, fmt.Errorf("%w: %v", errFilesRenameBackFailed, err)
			}
		}
		return false, err
	}
	target.State = filesTargetSwapped
	s.updateFilesRestoreMarker(*marker)
	return true, nil
}

// updateFilesRestoreMarker 记下某个目录的新状态；写不进去只记日志（恢复时按更保守的旧状态处理）。
func (s *BackupService) updateFilesRestoreMarker(marker filesRestoreMarker) {
	if err := s.writeFilesRestoreMarker(marker); err != nil {
		log.Printf("更新整理文件还原标记失败 err=%v", err)
	}
}

var errFilesRenameBackFailed = errors.New("文件包就位失败后改回原目录也失败")

// unpackFilesPack 把文件包完整解进各目录的暂存目录并逐条校验；不还原的目录的条目只校验、不写出。
func (s *BackupService) unpackFilesPack(directory string, manifest *BackupManifest, staged map[string]string) error {
	if s.beforeUnpack != nil {
		s.beforeUnpack()
	}
	extractTo := func(name string) string {
		dir := backupPackDirectoryOf(name)
		root, ok := staged[dir]
		if !ok {
			return ""
		}
		return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(name, dir+"/")))
	}
	packHash, entryErr, ioErr := readBackupFilesPack(filepath.Join(directory, manifest.Files.Name), manifest.Files, extractTo)
	switch {
	case ioErr != nil:
		return ioErr
	case entryErr != nil:
		return entryErr
	case packHash != manifest.Files.SHA256:
		return errors.New("文件包的哈希与清单不一致")
	}
	return nil
}

// backupPackDirectoryOf 返回包内路径所属的收录目录（validBackupPackPath 已保证有一个）。
func backupPackDirectoryOf(name string) string {
	for _, dir := range backupFilesDirectories {
		if strings.HasPrefix(name, dir+"/") {
			return dir
		}
	}
	return path.Dir(name)
}
