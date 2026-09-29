package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// errScanRootUnavailable 标记「根离线、卷未挂载或根身份变化」这一类失败，
// 与权限等读取失败区分开（决定失效原因是 offline_root 还是 read_error）。
var errScanRootUnavailable = errors.New("扫描根不可用")

// A missing file is deletable only while the same successfully scanned root is
// still available. In particular, an ejected volume must not look like an empty
// directory. Keep the snapshot from before traversal through final deletion.
//
// 挂载检查走 mediaVolumeAvailable（先解析符号链接）：扫描根是指向 /Volumes 下某块盘的软链接时，
// 要检查的是链接那头的盘，否则盘被拔出后留下的空挂载点会让整根的视频被当成「扫描删除」（LIB-07）。
type scanRemovalGuard map[string]os.FileInfo

func (g scanRemovalGuard) capture(root string) error {
	if err := mediaVolumeAvailable(root); err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("扫描根路径不是目录: %s", root)
	}
	g[root] = info
	return nil
}

func (g scanRemovalGuard) verify(root string) error {
	if err := mediaVolumeAvailable(root); err != nil {
		return err
	}
	after, err := os.Stat(root)
	if err != nil {
		return err
	}
	before, ok := g[root]
	if !ok || !after.IsDir() || !os.SameFile(before, after) {
		return fmt.Errorf("扫描根已变化: %s", root)
	}
	return nil
}

func (g scanRemovalGuard) missing(path string, excluded []string) (bool, error) {
	if isScanPathExcluded(path, excluded) {
		return false, nil
	}
	if err := mediaVolumeAvailable(path); err != nil {
		return false, fmt.Errorf("%w: %w", errScanRootUnavailable, err)
	}
	available := false
	for root := range g {
		if !pathBelongsToAny(filepath.Clean(path), []string{root}) {
			continue
		}
		if g.verify(root) == nil {
			available = true
			break
		}
	}
	if !available {
		return false, fmt.Errorf("%w: 扫描根已离线或发生变化，保留记录: %s", errScanRootUnavailable, path)
	}
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return false, err
}
