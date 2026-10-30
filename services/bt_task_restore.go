package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"video-master/database"
	"video-master/models"
)

var ErrBTRestorePending = errors.New("bt_restore_pending: 下载管理的恢复核实尚未完成，请稍后重试或重新打开应用")

type btRestoreMarker struct {
	Schema int    `json:"schema"`
	Scope  string `json:"scope"`
	Nonce  string `json:"nonce"`
}

// BTRestoreTicket only permits clearing a marker created by this attempt.
// It contains public DB identity, never a connection string or secret.
type BTRestoreTicket struct {
	Scope, Nonce string
	Created      bool
	additional   []BTRestoreTicket
}
type BTRestoreFence struct {
	dataDir       string
	scope         func(context.Context, *gorm.DB) (string, error)
	syncDirectory func(string) error
}

func NewBTRestoreFence(dataDir string) *BTRestoreFence {
	return &BTRestoreFence{dataDir: dataDir, scope: BTCredentialScope, syncDirectory: btSyncDirectory}
}
func (f *BTRestoreFence) markerPath(scope string) string {
	return filepath.Join(f.dataDir, "bt-recovery", scope+".json")
}
func (f *BTRestoreFence) lock(scope string) (func(), error) {
	if !filepath.IsAbs(f.dataDir) || !btCacheDigest(scope) {
		return nil, ErrBTRestorePending
	}
	for _, dir := range []string{filepath.Join(f.dataDir, "bt-recovery"), filepath.Join(f.dataDir, "bt-recovery", ".locks")} {
		if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, ErrBTRestorePending
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrBTRestorePending
		}
	}
	unlock, err := (BTFileCredentialLock{Path: filepath.Join(f.dataDir, "bt-recovery", ".locks", scope+".lock")}).TryLock()
	if err != nil {
		return nil, ErrBTRestorePending
	}
	return unlock, nil
}
func (f *BTRestoreFence) read(scope string) (btRestoreMarker, error) {
	path := f.markerPath(scope)
	info, err := os.Lstat(path)
	if err != nil {
		return btRestoreMarker{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 2048 {
		return btRestoreMarker{}, ErrBTRestorePending
	}
	file, err := os.Open(path)
	if err != nil {
		return btRestoreMarker{}, ErrBTRestorePending
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 2049))
	var marker btRestoreMarker
	if err != nil || len(raw) > 2048 || json.Unmarshal(raw, &marker) != nil || marker.Schema != 1 || marker.Scope != scope || !btTaskUUID(marker.Nonce) {
		return marker, ErrBTRestorePending
	}
	return marker, nil
}
func btSyncDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

// Prepare uses the supplied, already validated DB handle. In config-only switching
// this is the target handle; it is deliberately not the global current database.
func (f *BTRestoreFence) Prepare(ctx context.Context, db *gorm.DB) (BTRestoreTicket, error) {
	scope, err := f.scope(ctx, db)
	if err != nil {
		return BTRestoreTicket{}, ErrBTRestorePending
	}
	return f.prepareScope(scope)
}

// PrepareRestore also guards the identity that SQLite's existing replace operation
// will open next. The final component deliberately is not followed: replacing a
// symlink removes that link while leaving its old target and its scope separate.
// A partial error returns ownership tickets so the caller can clear only markers
// created by this attempt before any destructive restore has begun.
func (f *BTRestoreFence) PrepareRestore(ctx context.Context, db *gorm.DB) (BTRestoreTicket, error) {
	current, err := f.scope(ctx, db)
	if err != nil {
		return BTRestoreTicket{}, ErrBTRestorePending
	}
	planned := current
	if db.Dialector.Name() == "sqlite" {
		absolute, err := filepath.Abs(database.SQLitePath(f.dataDir))
		if err != nil {
			return BTRestoreTicket{}, ErrBTRestorePending
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
		if err != nil {
			return BTRestoreTicket{}, ErrBTRestorePending
		}
		planned = btSQLiteDatabaseScope(filepath.Join(parent, filepath.Base(absolute)))
	}
	ticket, err := f.prepareScope(current)
	if err != nil {
		return ticket, err
	}
	if planned != current {
		next, nextErr := f.prepareScope(planned)
		ticket.additional = append(ticket.additional, next)
		return ticket, nextErr
	}
	return ticket, nil
}
func (f *BTRestoreFence) prepareScope(scope string) (BTRestoreTicket, error) {
	unlock, err := f.lock(scope)
	if err != nil {
		return BTRestoreTicket{}, err
	}
	defer unlock()
	if existing, err := f.read(scope); err == nil {
		// A previous publication may have linked the marker but failed fsync.
		// Existence alone never authorizes destructive restore or config publication.
		ticket := BTRestoreTicket{Scope: scope, Nonce: existing.Nonce}
		if f.syncDirectory(filepath.Dir(f.markerPath(scope))) != nil || f.syncDirectory(f.dataDir) != nil {
			return ticket, ErrBTRestorePending
		}
		return ticket, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return BTRestoreTicket{}, ErrBTRestorePending
	}
	marker := btRestoreMarker{Schema: 1, Scope: scope, Nonce: uuid.NewString()}
	raw, _ := json.Marshal(marker)
	file, err := os.CreateTemp(filepath.Dir(f.markerPath(scope)), ".restore-*")
	if err != nil {
		return BTRestoreTicket{}, ErrBTRestorePending
	}
	defer func() { file.Close(); _ = os.Remove(file.Name()) }()
	if _, err = file.Write(raw); err != nil {
		return BTRestoreTicket{}, ErrBTRestorePending
	}
	if file.Sync() != nil || file.Close() != nil {
		return BTRestoreTicket{}, ErrBTRestorePending
	}
	if err = os.Link(file.Name(), f.markerPath(scope)); err != nil {
		return BTRestoreTicket{}, ErrBTRestorePending
	}
	ticket := BTRestoreTicket{Scope: scope, Nonce: marker.Nonce, Created: true}
	if f.syncDirectory(filepath.Dir(f.markerPath(scope))) != nil || f.syncDirectory(f.dataDir) != nil {
		return ticket, ErrBTRestorePending
	}
	return ticket, nil
}

// ClearOwned is only called after the owning lifecycle positively proves that
// no destructive restore/config publication ran. Unknown errors retain the file.
func (f *BTRestoreFence) ClearOwned(ticket BTRestoreTicket) error {
	err := f.clearOwnedMarker(ticket)
	for _, next := range ticket.additional {
		err = errors.Join(err, f.clearOwnedMarker(next))
	}
	return err
}
func (f *BTRestoreFence) clearOwnedMarker(ticket BTRestoreTicket) error {
	if !ticket.Created {
		return nil
	}
	unlock, err := f.lock(ticket.Scope)
	if err != nil {
		return err
	}
	defer unlock()
	marker, err := f.read(ticket.Scope)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || marker.Nonce != ticket.Nonce {
		return ErrBTRestorePending
	}
	if os.Remove(f.markerPath(ticket.Scope)) != nil || btSyncDirectory(filepath.Dir(f.markerPath(ticket.Scope))) != nil {
		return ErrBTRestorePending
	}
	return nil
}

// Consume marks restored intent under the task cache guards before allowing BT
// execution. Repeating a partially completed pass is conservative and safe.
func (f *BTRestoreFence) Consume(ctx context.Context, service *BTService) error {
	if service == nil {
		return ErrBTRestorePending
	}
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		scope, err := f.scope(ctx, db)
		if err != nil {
			return ErrBTRestorePending
		}
		if _, err = f.read(scope); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return ErrBTRestorePending
		}
		unlock, err := f.lock(scope)
		if err != nil {
			return err
		}
		defer unlock()
		marker, err := f.read(scope)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return ErrBTRestorePending
		}
		cursor := ""
		for {
			var rows []models.BTTask
			if err = db.Where("stage <> ? AND id > ?", "released", cursor).Order("id ASC").Limit(200).Find(&rows).Error; err != nil {
				return ErrBTRestorePending
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				cursor = row.ID
				err = service.withResolvedTaskCache(ctx, db, scope, row.ID, func(scoped context.Context, pinned *gorm.DB, _ *btCacheFiles) error {
					var current models.BTTask
					if pinned.First(&current, "id = ?", row.ID).Error != nil {
						return ErrBTRestorePending
					}
					if current.Stage == "released" {
						return nil
					}
					action := current.PendingAction
					if action == "" {
						action = "restore"
					}
					return service.updateTask(scoped, &current, map[string]any{"stage": "uncertain", "pending_action": action, "unresolved_write": true, "error_code": "restore_reconcile"})
				})
				if err != nil {
					return ErrBTRestorePending
				}
			}
		}
		latest, err := f.read(scope)
		if err != nil || latest.Nonce != marker.Nonce {
			return ErrBTRestorePending
		}
		if os.Remove(f.markerPath(scope)) != nil || btSyncDirectory(filepath.Dir(f.markerPath(scope))) != nil {
			return ErrBTRestorePending
		}
		return nil
	})
}

// InitializeLocal performs only local recovery. Call after schema initialization
// and before UI admission; external polling starts separately with Start.
func (s *BTService) InitializeLocal(ctx context.Context, fence *BTRestoreFence) error {
	if err := s.Quiesce(ctx); err != nil {
		return err
	}
	if fence != nil {
		if err := fence.Consume(ctx, s); err != nil {
			return err
		}
	}
	if err := s.recoverTransient(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.needsRecovery = false
	s.mu.Unlock()
	return nil
}
