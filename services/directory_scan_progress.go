package services

import (
	"io"
	"os"
	"path/filepath"
	"time"
)

// DirectoryScanProgress reports discovery and guarded reconciliation progress.
type DirectoryScanProgress struct {
	Added       int    `json:"added"`
	Restored    int    `json:"restored"`
	Deleted     int    `json:"deleted"`
	Skipped     int    `json:"skipped"`
	Processed   int    `json:"processed"`
	Total       int    `json:"total"`
	Phase       string `json:"phase"`
	CurrentPath string `json:"current_path"`
	Visited     int    `json:"visited"`
	Found       int    `json:"found"`
}

type directoryScanReporter struct {
	state    DirectoryScanProgress
	callback func(DirectoryScanProgress)
	lastEmit time.Time
}

func (r *directoryScanReporter) update(phase, path string, force bool) {
	if r.callback == nil {
		return
	}
	r.state.Phase, r.state.CurrentPath = phase, path
	if force || time.Since(r.lastEmit) >= 250*time.Millisecond {
		r.lastEmit = time.Now()
		r.callback(r.state)
	}
}

func (s *VideoService) ScanDirectoryWithProgress(dir string, progress func(DirectoryScanProgress)) ([]string, error) {
	files, err := s.scanDirectoryWithProgress(dir, true, progress)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(files))
	for i, file := range files {
		paths[i] = file.Path
	}
	return paths, nil
}

// Unlike filepath.Walk, report a directory before reading it and consume names
// in batches without waiting for the entire directory or sorting its contents.
// Readdirnames + Lstat retain Walk's symlink and file metadata semantics.
func walkDirectoryInBatches(root string, visit filepath.WalkFunc) error {
	info, err := os.Lstat(root)
	if err != nil {
		return visit(root, nil, err)
	}
	return walkDirectoryBatchEntry(root, info, visit)
}

func walkDirectoryBatchEntry(path string, info os.FileInfo, visit filepath.WalkFunc) error {
	if err := visit(path, info, nil); err != nil {
		if err == filepath.SkipDir && info.IsDir() {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return visit(path, info, err)
	}
	defer dir.Close()
	// Finish this directory before its descendants, keeping just one open handle.
	var children []string
	for {
		names, readErr := dir.Readdirnames(128)
		for _, name := range names {
			child := filepath.Join(path, name)
			childInfo, err := os.Lstat(child)
			if err != nil {
				if err := visit(child, nil, err); err != nil {
					return err
				}
				continue
			}
			if childInfo.IsDir() {
				children = append(children, child)
				continue
			}
			if err := visit(child, childInfo, nil); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return visit(path, info, readErr)
		}
	}
	if err := dir.Close(); err != nil {
		return visit(path, info, err)
	}
	for _, child := range children {
		if err := walkDirectoryInBatches(child, visit); err != nil {
			return err
		}
	}
	return nil
}
