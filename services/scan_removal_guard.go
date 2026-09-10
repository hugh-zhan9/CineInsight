package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// A missing file is deletable only while the same successfully scanned root is
// still available. In particular, an ejected volume must not look like an empty
// directory. Keep the snapshot from before traversal through final deletion.
type scanRemovalGuard map[string]os.FileInfo

func (g scanRemovalGuard) capture(root string) error {
	if err := scanVolumeAvailable(root); err != nil {
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
	if err := scanVolumeAvailable(root); err != nil {
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
	if err := scanVolumeAvailable(path); err != nil {
		return false, err
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
		return false, fmt.Errorf("扫描根已离线或发生变化，保留记录: %s", path)
	}
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return false, err
}
