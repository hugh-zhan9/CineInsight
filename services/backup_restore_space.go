package services

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"video-master/database"
)

// 恢复前的空间检查（4.2，审阅 R1 / M4）：恢复过程中要新写的东西都在进入维护之前算进去，按卷合计——
// 同一块盘上的需求相加，不同的盘各算各的：
//   - 恢复用的临时副本（copyVerifiedBackup，写在备份目录）：快照大小；
//   - 恢复前的安全备份（写在备份目录）：当前库的快照 + 当前整理文件的文件包；
//   - SQLite 的临时库（写在库文件所在目录）：快照大小；
//   - 文件包解出来的整理文件（写在各收录目录真实位置的同级）：每个文件、每个目录都按那块盘的分配块向上取整。
// 有一块盘放不下就以 backup_insufficient_space 拒绝，什么都还没动。
//
// 已知局限（审阅 M5，只记录）：卷按设备号分组。APFS 同一容器里的多个卷、btrfs 的子卷设备号不同却共用空间，
// 会被当成互不相干的盘；Windows 上挂在文件夹里的卷按盘符归到所在的盘上。这些情况下检查可能偏松。

const restoreSpaceBlock = 4096

func roundUpToBlock(size, block int64) int64 {
	if size <= 0 {
		return 0
	}
	return (size + block - 1) / block * block
}

// blockSize 是 path 所在卷的分配块大小；取不到（或在 Windows 上）时按 4 KiB 算。
func (s *BackupService) blockSize(path string) int64 {
	size := int64(0)
	if s.blockSizeOf != nil {
		size = s.blockSizeOf(path)
	} else {
		size = backupBlockSize(path)
	}
	if size <= 0 {
		return restoreSpaceBlock
	}
	return size
}

type restoreVolumeNeed struct {
	path  string // 这块盘上第一个落点，用来查剩余空间
	bytes int64
}

type restoreSpacePlan struct {
	service *BackupService
	order   []string
	volumes map[string]*restoreVolumeNeed
}

// add 把要写的几样东西记到 location（或它最近一个存在的上级目录）所在的卷上，每样按那块盘的分配块向上取整。
func (p *restoreSpacePlan) add(location string, sizes ...int64) error {
	location = p.service.existingAncestor(location)
	volume, err := p.service.volumeKey(location)
	if err != nil {
		return err
	}
	need := p.volumes[volume]
	if need == nil {
		need = &restoreVolumeNeed{path: location}
		p.volumes[volume] = need
		p.order = append(p.order, volume)
	}
	block := p.service.blockSize(location)
	for _, size := range sizes {
		need.bytes += roundUpToBlock(size, block)
	}
	return nil
}

func (s *BackupService) volumeKey(path string) (string, error) {
	if s.volumeOf != nil {
		return s.volumeOf(path)
	}
	return backupVolumeID(path)
}

var errRestoreSpaceUnknown = fmt.Errorf("无法检查恢复所需的磁盘空间，数据库未被修改")

// checkRestoreSpace 在进入维护之前按卷核对恢复所需的全部空间（见文件头）。SQLite 先清掉上次崩溃留下的临时库，
// 再算空间——那份临时库本身就占着一整个库文件的大小。
func (s *BackupService) checkRestoreSpace(directory string, request BackupRestoreRequest, manifest *BackupManifest, backend database.Backend) error {
	plan := &restoreSpacePlan{service: s, volumes: map[string]*restoreVolumeNeed{}}
	livePath := database.SQLitePath(s.dataDir)
	if backend == database.BackendSQLite {
		database.SweepSQLiteRestoreTemps(livePath)
		if err := plan.add(filepath.Dir(livePath), request.Size); err != nil {
			return errRestoreSpaceUnknown
		}
	}
	safetySnapshot, safetyFiles, err := s.estimateSafetyBackup(directory, request, backend, livePath)
	if err != nil {
		return errRestoreSpaceUnknown
	}
	if err := plan.add(directory, request.Size, safetySnapshot, safetyFiles); err != nil {
		return errRestoreSpaceUnknown
	}
	if manifest != nil {
		if err := s.planUnpackedFiles(plan, manifest); err != nil {
			return errRestoreSpaceUnknown
		}
	}
	diskFree := s.diskFree
	if diskFree == nil {
		diskFree = enhancementDiskFree
	}
	for _, volume := range plan.order {
		need := plan.volumes[volume]
		free, err := diskFree(need.path)
		if err != nil {
			return errRestoreSpaceUnknown
		}
		if free < uint64(need.bytes) {
			return fmt.Errorf("%s: 磁盘剩余空间不足以完成恢复（恢复用的临时文件、恢复前的安全备份与整理过的文件需要约 %s，可用 %s），数据库未被修改",
				BackupReasonInsufficientSpace, formatEnhancementBytes(uint64(need.bytes)), formatEnhancementBytes(free))
		}
	}
	return nil
}

// estimateSafetyBackup 估算恢复前那份安全备份的两个文件：快照与当前整理文件的文件包。SQLite 的快照按库文件
// （含 WAL）算；Postgres 的转储大小无从预知，取「待恢复的转储」与备份目录里最近一份转储中较大的那个——两者都是
// 同一份库的转储，量级一致。
func (s *BackupService) estimateSafetyBackup(directory string, request BackupRestoreRequest, backend database.Backend, livePath string) (int64, int64, error) {
	var snapshot int64
	if backend == database.BackendSQLite {
		for _, file := range []string{livePath, livePath + "-wal"} {
			if info, err := os.Stat(file); err == nil {
				snapshot += info.Size()
			} else if !os.IsNotExist(err) {
				return 0, 0, err
			}
		}
	} else {
		snapshot = request.Size
		entries, err := s.scanBackupEntries(directory)
		if err != nil {
			return 0, 0, err
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.name, backupFileSuffix) {
				snapshot = max(snapshot, entry.size)
				break // 按时间倒序，第一份就是最近一份
			}
		}
	}
	files, err := estimateBackupFilesPack(s.dataDir)
	if err != nil {
		return 0, 0, err
	}
	return snapshot, files, nil
}

// planUnpackedFiles 把文件包解出来要占的空间记到各收录目录真实位置的那块盘上（审阅 I-1 / M4）：每个文件按块取整，
// 暂存目录本身与其下每个子目录各占一块。合并还原的目录记在真实目录所在的卷上，另在数据目录那块盘上记一份暂存。
func (s *BackupService) planUnpackedFiles(plan *restoreSpacePlan, manifest *BackupManifest) error {
	sizes := map[string][]int64{}
	directories := map[string]map[string]bool{}
	for _, dir := range restoredBackupDirectories(manifest) {
		directories[dir] = map[string]bool{".": true}
	}
	for _, entry := range manifest.Files.Entries {
		dir := backupPackDirectoryOf(entry.Path)
		if directories[dir] == nil {
			continue
		}
		sizes[dir] = append(sizes[dir], entry.Size)
		for parent := path.Dir(strings.TrimPrefix(entry.Path, dir+"/")); parent != "."; parent = path.Dir(parent) {
			directories[dir][parent] = true
		}
	}
	for dir, subdirs := range directories {
		placement, state, err := s.placeCuratedRoot(dir, false)
		if err != nil {
			return err
		}
		if state == curatedRootUnavailable {
			continue // checkFilesPackBeforeRestore 已经拒绝
		}
		all := append([]int64{}, sizes[dir]...)
		for range subdirs {
			all = append(all, 1) // 一个目录占一块
		}
		if !placement.Merge {
			if err := plan.add(placement.Parent, all...); err != nil { // 暂存目录建在真实目录的同级
				return err
			}
			continue
		}
		// 合并还原：文件先解到数据目录里的暂存目录，再逐个写进真实目录——两处都要放得下。写入目标按真实目录本身
		// 所在的卷算（它可能是另一块盘的挂载点，审阅第五轮 M6）；还不存在时按最近一个存在的上级算。
		destination := placement.Parent
		if placement.Exists {
			destination = placement.target()
		}
		if err := plan.add(destination, all...); err != nil {
			return err
		}
		if err := plan.add(s.dataDir, all...); err != nil {
			return err
		}
	}
	return nil
}
