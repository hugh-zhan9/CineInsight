package services

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// filesPlacement 是一个收录目录在磁盘上的实际位置。两种还原方式（4.2，集成方裁决 2026-10-12 / 10-13）：
//   - 真实目录就在数据目录里（路径上没有符号链接）、且目录里任何一层都没有符号链接：整目录换入——同级建暂存目录，
//     原目录改名为 .pre-restore-*，暂存目录就位（目录里不在备份中的文件，包括系统或应用留下的点文件，会随旧目录一起换走）；
//   - 其余情况（收录目录本身或上级是符号链接、真实目录在数据目录之外，或目录里含有符号链接）：合并——文件先解到数据目录
//     里的暂存目录，再逐个「同目录临时文件 + 改名覆盖」写进真实目录；那里不建暂存目录、不改名、不删任何东西。
type filesPlacement struct {
	Dir    string // 收录目录（相对数据目录，如 media-details/videos）
	Parent string // 真实目录所在的上级（绝对路径，已解析符号链接）
	Base   string // 真实目录的名字（链接指向的目录可以叫别的名字）
	Exists bool   // 真实目录此刻存在
	Linked bool   // 真实位置不在数据目录里
	Merge  bool   // 按合并还原：Linked，或目录里含有符号链接（换入会把这些链接一起换走，审阅第五轮 E1）
}

func (p filesPlacement) target() string { return filepath.Join(p.Parent, p.Base) }

func (p filesPlacement) stagingName(stamp string) string {
	return backupFilesStagingPrefix + p.Base + "-" + stamp
}

func (p filesPlacement) asideName(stamp string) string {
	return p.Base + backupFilesAsideMarker + stamp
}

// resolvedDataDir 是解析过符号链接的数据目录（标记里记的位置都要落在它下面）。
func (s *BackupService) resolvedDataDir() (string, error) {
	resolved, err := filepath.EvalSymlinks(s.dataDir)
	if err != nil {
		return "", pathlessError(err)
	}
	return resolved, nil
}

// insideDirectory：path 是 root 本身或在它下面（两者都是规整的绝对路径）。
func insideDirectory(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// placeCuratedRoot 解析收录目录的实际位置；不可用（失效链接、没挂载的盘、读不到）时返回该状态、位置为空。
// 收录目录还不存在时：create 为真把上级建出来（还原用），否则落在最近一个存在的上级上（只用来算空间）。
// 真实位置与「数据目录 + 收录目录」对不上（路径上有符号链接）时 Linked 为真。
func (s *BackupService) placeCuratedRoot(dir string, create bool) (filesPlacement, curatedRootStatus, error) {
	dataRoot, err := s.resolvedDataDir()
	if err != nil {
		return filesPlacement{}, curatedRootUnavailable, err
	}
	canonical := filepath.Join(dataRoot, filepath.FromSlash(dir))
	state, resolved := curatedRootState(s.dataDir, dir)
	switch state {
	case curatedRootUnavailable:
		return filesPlacement{}, state, nil
	case curatedRootPresent:
		linked := resolved != canonical
		return filesPlacement{Dir: dir, Parent: filepath.Dir(resolved), Base: filepath.Base(resolved), Exists: true,
			Linked: linked, Merge: linked || containsSymlink(resolved)}, state, nil
	}
	link := filepath.Join(s.dataDir, filepath.FromSlash(dir))
	parent := filepath.Dir(link)
	if create {
		if err := os.MkdirAll(parent, 0755); err != nil {
			return filesPlacement{}, state, pathlessError(err)
		}
	} else {
		parent = s.existingAncestor(parent)
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return filesPlacement{}, state, pathlessError(err)
	}
	rel, err := filepath.Rel(s.dataDir, parent)
	if err != nil {
		return filesPlacement{}, state, pathlessError(err)
	}
	linked := resolvedParent != filepath.Join(dataRoot, rel)
	return filesPlacement{Dir: dir, Parent: resolvedParent, Base: filepath.Base(link), Linked: linked, Merge: linked}, state, nil
}

// containsSymlink：目录里（任何一层）有符号链接。走不完（读不到）时也按「有」处理——宁可合并，不整目录换走。
func containsSymlink(root string) bool {
	found := false
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found || err != nil
}
