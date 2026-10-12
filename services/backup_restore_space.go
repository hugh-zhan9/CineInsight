package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"video-master/database"
)

// 恢复前的空间检查（4.2，审阅 R1）：恢复过程中要新写的东西都在进入维护之前算进去，按卷合计——
// 同一块盘上的需求相加，不同的盘各算各的：
//   - 恢复用的临时副本（copyVerifiedBackup，写在备份目录）：快照大小；
//   - 恢复前的安全备份（写在备份目录）：当前库的快照 + 当前整理文件的文件包；
//   - SQLite 的临时库（写在库文件所在目录）：快照大小；
//   - 文件包解出来的整理文件（写在各收录目录的同级）：每个条目按 4 KiB 向上取整。
// 有一块盘放不下就以 backup_insufficient_space 拒绝，什么都还没动。

const restoreSpaceBlock = 4096

func roundUpRestoreBlock(size int64) int64 {
	if size <= 0 {
		return 0
	}
	return (size + restoreSpaceBlock - 1) / restoreSpaceBlock * restoreSpaceBlock
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

// add 把 bytes 记到 path（或它最近一个存在的上级目录）所在的卷上。
func (p *restoreSpacePlan) add(path string, bytes int64) error {
	if bytes <= 0 {
		return nil
	}
	path = p.service.existingAncestor(path)
	volume, err := p.service.volumeKey(path)
	if err != nil {
		return err
	}
	need := p.volumes[volume]
	if need == nil {
		need = &restoreVolumeNeed{path: path}
		p.volumes[volume] = need
		p.order = append(p.order, volume)
	}
	need.bytes += bytes
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
	snapshot := roundUpRestoreBlock(request.Size)
	livePath := database.SQLitePath(s.dataDir)
	if backend == database.BackendSQLite {
		database.SweepSQLiteRestoreTemps(livePath)
		if err := plan.add(filepath.Dir(livePath), snapshot); err != nil {
			return errRestoreSpaceUnknown
		}
	}
	safety, err := s.estimateSafetyBackup(directory, request, backend, livePath)
	if err != nil {
		return errRestoreSpaceUnknown
	}
	if err := plan.add(directory, snapshot+safety); err != nil {
		return errRestoreSpaceUnknown
	}
	if manifest != nil {
		for _, entry := range manifest.Files.Entries {
			target := filepath.Join(s.dataDir, filepath.FromSlash(backupPackDirectoryOf(entry.Path)))
			if err := plan.add(filepath.Dir(target), roundUpRestoreBlock(entry.Size)); err != nil {
				return errRestoreSpaceUnknown
			}
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

// estimateSafetyBackup 估算恢复前那份安全备份的大小：快照 + 当前整理文件的文件包，都按块取整。
// SQLite 的快照按库文件（含 WAL）算；Postgres 的转储大小无从预知，取「待恢复的转储」与备份目录里最近一份
// 转储中较大的那个——两者都是同一份库的转储，量级一致。
func (s *BackupService) estimateSafetyBackup(directory string, request BackupRestoreRequest, backend database.Backend, livePath string) (int64, error) {
	var snapshot int64
	if backend == database.BackendSQLite {
		for _, path := range []string{livePath, livePath + "-wal"} {
			if info, err := os.Stat(path); err == nil {
				snapshot += info.Size()
			} else if !os.IsNotExist(err) {
				return 0, err
			}
		}
	} else {
		snapshot = request.Size
		entries, err := s.scanBackupEntries(directory)
		if err != nil {
			return 0, err
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
		return 0, err
	}
	return roundUpRestoreBlock(snapshot) + roundUpRestoreBlock(files), nil
}
