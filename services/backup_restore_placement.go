package services

import (
	"os"
	"path/filepath"
)

// filesPlacement 是一个收录目录在磁盘上的实际位置（审阅 I-1）：收录目录本身或它的上级可能是指向别的盘的符号链接，
// 还原要动的是链接指向的那个真实目录，链接本身原样保留。暂存目录与改名留下的旧目录都建在真实目录的同级
// （同一个卷，改名不会跨盘失败；空间也记在那个卷上）。
type filesPlacement struct {
	Dir    string // 收录目录（相对数据目录，如 media-details/videos）
	Parent string // 真实目录所在的上级（绝对路径，已解析符号链接）
	Base   string // 真实目录的名字（链接指向的目录可以叫别的名字）
	Exists bool   // 真实目录此刻存在
}

func (p filesPlacement) target() string { return filepath.Join(p.Parent, p.Base) }

func (p filesPlacement) stagingName(stamp string) string {
	return backupFilesStagingPrefix + p.Base + "-" + stamp
}

func (p filesPlacement) asideName(stamp string) string {
	return p.Base + backupFilesAsideMarker + stamp
}

// placeCuratedRoot 解析收录目录的实际位置；不可用（失效链接、没挂载的盘、读不到）时返回该状态、位置为空。
// 收录目录还不存在时：create 为真把上级建出来（还原用），否则落在最近一个存在的上级上（只用来算空间）。
func (s *BackupService) placeCuratedRoot(dir string, create bool) (filesPlacement, curatedRootStatus, error) {
	state, resolved := curatedRootState(s.dataDir, dir)
	switch state {
	case curatedRootUnavailable:
		return filesPlacement{}, state, nil
	case curatedRootPresent:
		return filesPlacement{Dir: dir, Parent: filepath.Dir(resolved), Base: filepath.Base(resolved), Exists: true}, state, nil
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
	return filesPlacement{Dir: dir, Parent: resolvedParent, Base: filepath.Base(link)}, state, nil
}
