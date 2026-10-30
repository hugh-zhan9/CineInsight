package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gorm.io/gorm"
	"video-master/database"
	"video-master/models"
)

const BTNamespaceDirectory = "CineInsight"
const BTNamespaceMarker = ".cineinsight-bt-namespace.json"

var ErrBTPending = errors.New("bt_pending: managed download is not eligible for library reconciliation")
var ErrBTPathUnsafe = errors.New("bt_path_unsafe: managed download path is invalid")

// BTDatabase resolves the current database after maintenance/reconnect, not a stale handle.
type BTDatabase func(context.Context) (*gorm.DB, error)
type BTPathPolicy struct{ db BTDatabase }

// NewBTPathPolicy accepts a provider for the current library database.
func NewBTPathPolicy(db BTDatabase) *BTPathPolicy {
	if db == nil {
		db = func(ctx context.Context) (*gorm.DB, error) {
			if database.DB == nil {
				return nil, errors.New("bt_database_unavailable")
			}
			return database.DB.WithContext(ctx), nil
		}
	}
	return &BTPathPolicy{db: db}
}

// SetBTPathPolicy installs the local guard before scans or external downloads start.
func (s *VideoService) SetBTPathPolicy(p *BTPathPolicy) { s.btPolicy.Store(p) }

type btNamespaceMarker struct {
	Schema     int    `json:"schema"`
	InstanceID string `json:"instance_id"`
}

// WriteBTNamespaceMarker creates the persistent guard before any external add.
// instanceID seeds the stable namespace creation identity, not the current
// connection owner. An existing valid marker is reused unchanged across retired
// connections. Only a new or empty unmarked namespace may be initialized.
func WriteBTNamespaceMarker(downloadRoot, instanceID string) (string, error) {
	if !filepath.IsAbs(downloadRoot) || instanceID == "" || len(instanceID) > 80 {
		return "", ErrBTPathUnsafe
	}
	base, err := filepath.EvalSymlinks(downloadRoot)
	if err != nil {
		return "", err
	}
	root := filepath.Join(base, BTNamespaceDirectory)
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil || real != root {
		return "", ErrBTPathUnsafe
	}
	path := filepath.Join(root, BTNamespaceMarker)
	if _, err := readBTMarker(path); err == nil {
		return root, syncBTNamespace(root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", ErrBTPathUnsafe
	}
	directory, err := os.Open(root)
	if err != nil {
		return "", err
	}
	entries, readErr := directory.ReadDir(1)
	closeDirErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", readErr
	}
	if closeDirErr != nil {
		return "", closeDirErr
	}
	if len(entries) > 0 {
		return "", ErrBTPathUnsafe
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		_, e := readBTMarker(path)
		if e != nil {
			return "", ErrBTPathUnsafe
		}
		return root, syncBTNamespace(root)
	}
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(btNamespaceMarker{Schema: 1, InstanceID: instanceID})
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return root, syncBTNamespace(root)
}

func syncBTNamespace(root string) error {
	for _, path := range []string{filepath.Join(root, BTNamespaceMarker), root, filepath.Dir(root)} {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		syncErr := f.Sync()
		closeErr := f.Close()
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
func readBTMarker(path string) (btNamespaceMarker, error) {
	var m btNamespaceMarker
	info, err := os.Lstat(path)
	if err != nil {
		return m, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1024 {
		return m, ErrBTPathUnsafe
	}
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil || len(data) > 1024 {
		return m, ErrBTPathUnsafe
	}
	if json.Unmarshal(data, &m) != nil || m.Schema != 1 || m.InstanceID == "" || len(m.InstanceID) > 80 {
		return m, ErrBTPathUnsafe
	}
	return m, nil
}

type btPathTask struct {
	task  models.BTTask
	files map[string]models.BTFile
}

// BTPathSnapshot is one scan generation of local task/file eligibility.
type BTPathSnapshot struct {
	tasks   []btPathTask
	mu      sync.Mutex
	markers map[string]bool
}

// Snapshot fails closed if ownership rows cannot be read.
func (p *BTPathPolicy) Snapshot(ctx context.Context) (*BTPathSnapshot, error) {
	snap := &BTPathSnapshot{markers: map[string]bool{}}
	if p == nil {
		return snap, nil
	}
	db, err := p.db(ctx)
	if err != nil {
		return nil, err
	}
	if db == nil {
		return nil, gorm.ErrInvalidDB
	}
	var tasks []models.BTTask
	if err = db.Find(&tasks).Error; err != nil {
		return nil, err
	}
	var files []models.BTFile
	if err = db.Where("import_state IN ? AND video_id IS NOT NULL", []string{"imported", "blocked_user_delete"}).Find(&files).Error; err != nil {
		return nil, err
	}
	byID := map[string]int{}
	for _, task := range tasks {
		if task.SaveRoot == "" {
			continue
		}
		byID[task.ID] = len(snap.tasks)
		snap.tasks = append(snap.tasks, btPathTask{task: task, files: map[string]models.BTFile{}})
	}
	for _, f := range files {
		if i, ok := byID[f.TaskID]; ok && !f.Padding {
			snap.tasks[i].files[filepath.Clean(filepath.Join(snap.tasks[i].task.SaveRoot, filepath.FromSlash(f.RelativePath)))] = f
		}
	}
	return snap, nil
}
func btWithin(path, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func (snap *BTPathSnapshot) hasMarker(path string) (bool, error) {
	snap.mu.Lock()
	defer snap.mu.Unlock()
	dirs := []string{}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if value, ok := snap.markers[dir]; ok {
			for _, d := range dirs {
				snap.markers[d] = value
			}
			return value, nil
		}
		dirs = append(dirs, dir)
		if strings.EqualFold(filepath.Base(dir), BTNamespaceDirectory) {
			_, err := readBTMarker(filepath.Join(dir, BTNamespaceMarker))
			if err == nil {
				for _, d := range dirs {
					snap.markers[d] = true
				}
				return true, nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return true, err
			}
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return false, nil
}

// btMarkerWithin ignores the namespace above the released task root. It does
// not use hasMarker's ancestor cache, whose positive may belong to that outer
// namespace; a nested marker can also be created after this snapshot was read.
func btMarkerWithin(path, root string) (bool, error) {
	for dir := filepath.Dir(path); btWithin(dir, root); dir = filepath.Dir(dir) {
		if strings.EqualFold(filepath.Base(dir), BTNamespaceDirectory) {
			if _, err := readBTMarker(filepath.Join(dir, BTNamespaceMarker)); err == nil {
				return true, nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return true, err
			}
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return false, nil
}

// Withheld is distinct from missing. Callers must not perform deletion, restore,
// relocation or metadata refresh for a withheld path.
func (snap *BTPathSnapshot) Withheld(path string) (bool, error) {
	return snap.withheld(path, 0, nil)
}
func (snap *BTPathSnapshot) withheld(path string, restoreID uint, authorization **btRestoreGrant) (bool, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return true, err
	}
	real, realErr := filepath.EvalSymlinks(abs)
	// Lexical and canonical distances belong to different coordinate systems:
	// a short alias must not outrank a deeper real path inside a protected task.
	owners := [2]int{-1, -1}
	for kind, candidate := range []string{abs, real} {
		if kind == 1 && realErr != nil {
			continue
		}
		distance, ambiguous := int(^uint(0)>>1), false
		for index, entry := range snap.tasks {
			root := entry.task.SaveRoot
			if kind == 1 {
				root = entry.task.CanonicalRoot
			}
			if !btWithin(candidate, root) {
				continue
			}
			rel, _ := filepath.Rel(root, candidate)
			depth := len(strings.Split(rel, string(filepath.Separator)))
			if depth < distance {
				owners[kind], distance, ambiguous = index, depth, false
			} else if depth == distance && owners[kind] != index {
				ambiguous = true
			}
		}
		if ambiguous {
			return true, nil
		}
	}
	for kind, owner := range owners {
		if owner < 0 {
			candidate := abs
			if kind == 1 {
				if realErr != nil {
					continue
				}
				candidate = real
			}
			if marked, err := snap.hasMarker(candidate); marked || err != nil {
				return true, err
			}
			continue
		}
		if kind == 1 && owner == owners[0] {
			continue
		}
		if held, err := snap.tasks[owner].withheld(abs, real, realErr, restoreID, authorization); held || err != nil {
			return true, err
		}
	}

	return false, nil
}

// Each applicable owner must independently permit the path.
func (entry btPathTask) withheld(abs, real string, realErr error, restoreID uint, authorization **btRestoreGrant) (bool, error) {
	root := filepath.Clean(entry.task.SaveRoot)
	canonical := entry.task.CanonicalRoot
	if entry.task.Stage == "released" {
		resolved, err := btCanonicalIncludingMissing(abs)
		if err != nil {
			return true, err
		}
		if !btWithin(resolved, canonical) {
			return true, nil
		}
		// Releasing this task cannot release a newer nested namespace,
		// including one whose task rows were lost in a backup restore.
		if marked, err := btMarkerWithin(abs, root); marked || err != nil {
			return true, err
		}
		return btMarkerWithin(resolved, canonical)
	}
	f, ok := entry.files[abs]
	if !ok || !f.Selected || !f.Selectable || f.Padding || (f.ImportState != "imported" && !(restoreID != 0 && f.ImportState == "blocked_user_delete")) || f.VideoID == nil || f.Progress != 1 || f.EligibilityRevision == 0 {
		return true, nil
	}
	if restoreID != 0 && (*f.VideoID != restoreID || f.ObservedSize <= 0) {
		return true, nil
	}
	if realErr != nil || !btWithin(real, canonical) {
		return true, nil
	}
	if entry.task.Stage == "conflict" || entry.task.Stage == "uncertain" || strings.HasPrefix(entry.task.RemoteState, "checking") || entry.task.RemoteState == "moving" || entry.task.RemoteState == "error" || entry.task.RemoteState == "missingFiles" {
		return true, nil
	}
	info, err := os.Stat(abs)
	if err != nil {
		return true, nil
	}
	held := !info.Mode().IsRegular() || info.Size() != f.Size || info.Size() != f.ObservedSize || info.ModTime().UnixNano() != f.ObservedMtime
	if !held && authorization != nil && restoreID != 0 {
		*authorization = &btRestoreGrant{task: entry.task, file: f, identity: btImportGrant{path: abs, real: real, size: info.Size(), mtime: info.ModTime().UnixNano()}}
	}
	return held, nil
}
func (s *VideoService) btPathSnapshot(ctx context.Context) (*BTPathSnapshot, error) {
	return s.btPolicy.Load().Snapshot(ctx)
}
func (s *VideoService) checkBTPath(path string) error {
	snap, err := s.btPathSnapshot(context.Background())
	if err != nil {
		return err
	}
	withheld, err := snap.Withheld(path)
	if err != nil {
		return err
	}
	if withheld {
		return ErrBTPending
	}
	return nil
}

// Resolve the nearest existing ancestor so released missing paths can retain
// normal missing-file reconciliation without admitting a symlink escape.
func btCanonicalIncludingMissing(path string) (string, error) {
	current := path
	missing := []string{}
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if info, statErr := os.Lstat(current); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", ErrBTPathUnsafe
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

// btRestoreGrant is issued only for an explicit user restoration of a previously
// imported identity. It does not grant ordinary Add/scan permission.
type btRestoreGrant struct {
	task     models.BTTask
	file     models.BTFile
	identity btImportGrant
}

func (s *VideoService) authorizeBTRestore(path string, videoID uint) (*btRestoreGrant, error) {
	snap, err := s.btPathSnapshot(context.Background())
	if err != nil {
		return nil, err
	}
	var grant *btRestoreGrant
	held, err := snap.withheld(path, videoID, &grant)
	if err != nil {
		return nil, err
	}
	if held {
		return nil, ErrBTPending
	}
	return grant, nil
}
func (g *btRestoreGrant) release(tx *gorm.DB, videoID uint) error {
	if g.file.VideoID == nil || *g.file.VideoID != videoID {
		return ErrBTPending
	}
	if err := g.identity.check(g.identity.path); err != nil {
		return err
	}
	task := tx.Model(&models.BTTask{}).Where("id = ? AND revision = ? AND attempt_generation = ?", g.task.ID, g.task.Revision, g.task.AttemptGeneration).UpdateColumn("revision", g.task.Revision)
	if task.Error != nil {
		return task.Error
	}
	if task.RowsAffected != 1 {
		return ErrBTPending
	}
	file := tx.Model(&models.BTFile{}).Where("task_id = ? AND manifest_file_id = ? AND video_id = ? AND eligibility_revision = ? AND selected = ? AND progress = ? AND import_state = ? AND observed_size = ? AND observed_mtime = ?", g.file.TaskID, g.file.ManifestFileID, videoID, g.file.EligibilityRevision, true, 1, g.file.ImportState, g.file.ObservedSize, g.file.ObservedMtime).Updates(map[string]any{"import_state": "imported", "error_code": ""})
	if file.Error != nil {
		return file.Error
	}
	if file.RowsAffected != 1 {
		return ErrBTPending
	}
	return nil
}
