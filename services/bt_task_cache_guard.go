package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"

	"gorm.io/gorm"
	"video-master/database"
	"video-master/models"
)

type btCacheLeaseKey struct{}
type btCacheLease struct {
	owner         *BTService
	db            *gorm.DB
	scope, taskID string
	files         *btCacheFiles
	closed        atomic.Bool
}

// The private context is synchronous and may not escape this callback. Lock
// order is operation lease -> public scope -> nonblocking OS lock -> SQL txn.
// No maintenance bypass or database application lock is used.
func (s *BTService) withTaskCache(ctx context.Context, id string, fn func(context.Context, *gorm.DB, *btCacheFiles) error) error {
	if !btTaskUUID(id) {
		return ErrBTTask
	}
	if lease, ok := ctx.Value(btCacheLeaseKey{}).(*btCacheLease); ok {
		if lease.closed.Load() || lease.owner != s || lease.taskID != id {
			return ErrBTConflict
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if database.MaintenanceActive() {
			return database.ErrMaintenance
		}
		return fn(ctx, lease.db.WithContext(ctx), lease.files)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		scope, err := s.resolveCacheScope(ctx, db)
		if err != nil {
			return err
		}
		return s.withResolvedTaskCache(ctx, db, scope, id, fn)
	})
}
func (s *BTService) resolveCacheScope(ctx context.Context, db *gorm.DB) (string, error) {
	resolve := s.deps.CacheScope
	if resolve == nil {
		resolve = BTCredentialScope
	}
	scope, err := resolve(ctx, db)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrBTTask
	}
	if database.MaintenanceActive() {
		return "", database.ErrMaintenance
	}
	if !btCacheDigest(scope) {
		return "", ErrBTTask
	}
	return scope, nil
}
func (s *BTService) withResolvedTaskCache(ctx context.Context, db *gorm.DB, scope, id string, fn func(context.Context, *gorm.DB, *btCacheFiles) error) error {
	if !filepath.IsAbs(s.dataDir) || !btCacheDigest(scope) || !btTaskUUID(id) {
		return ErrBTPath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if database.MaintenanceActive() {
		return database.ErrMaintenance
	}
	root := filepath.Join(s.dataDir, "bt-previews", scope)
	// Validate each app-owned parent before descending; a lock never lives in the
	// task directory that a cancellation may remove.
	for _, dir := range []string{filepath.Join(s.dataDir, "bt-previews"), root, filepath.Join(root, ".locks")} {
		if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return ErrBTTask
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrBTPath
		}
	}
	unlock, err := (BTFileCredentialLock{Path: filepath.Join(root, ".locks", id+".lock")}).TryLock()
	if err != nil {
		if errors.Is(err, ErrBTCredentialsBusy) {
			return ErrBTTaskBusy
		}
		return ErrBTTask
	}
	defer unlock()
	lease := &btCacheLease{owner: s, db: db, scope: scope, taskID: id, files: &btCacheFiles{root: root, taskID: id}}
	defer lease.closed.Store(true)
	scoped := context.WithValue(ctx, btCacheLeaseKey{}, lease)
	return fn(scoped, db.WithContext(scoped), lease.files)
}

func btTaskOperation(ctx context.Context, fn func(*gorm.DB) error) error {
	if lease, ok := ctx.Value(btCacheLeaseKey{}).(*btCacheLease); ok {
		if lease.closed.Load() {
			return ErrBTConflict
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if database.MaintenanceActive() {
			return database.ErrMaintenance
		}
		return fn(lease.db.WithContext(ctx))
	}
	return database.WithOperationContext(ctx, fn)
}
func btTaskTransaction(ctx context.Context, fn func(*gorm.DB) error) error {
	return btTaskOperation(ctx, func(db *gorm.DB) error { return db.Transaction(fn) })
}
func (s *BTService) taskTransaction(ctx context.Context, id string, fn func(context.Context, *gorm.DB) error) error {
	return s.withTaskCache(ctx, id, func(scoped context.Context, db *gorm.DB, _ *btCacheFiles) error {
		return db.Transaction(func(tx *gorm.DB) error { return fn(scoped, tx) })
	})
}
func (s *BTService) cachePath(ctx context.Context, id, digest string) (string, error) {
	var path string
	err := s.withTaskCache(ctx, id, func(_ context.Context, _ *gorm.DB, files *btCacheFiles) error {
		path = files.cachePath(id, digest)
		return nil
	})
	return path, err
}
func (s *BTService) writeCache(ctx context.Context, id string, data []byte) error {
	return s.withTaskCache(ctx, id, func(_ context.Context, _ *gorm.DB, files *btCacheFiles) error { return files.writeCache(id, data) })
}
func (s *BTService) readCache(ctx context.Context, task models.BTTask) ([]byte, error) {
	var data []byte
	err := s.withTaskCache(ctx, task.ID, func(_ context.Context, db *gorm.DB, files *btCacheFiles) error {
		var current models.BTTask
		if db.First(&current, "id = ?", task.ID).Error != nil {
			return ErrBTTask
		}
		if !sameBTCacheIntent(current, task) {
			return ErrBTConflict
		}
		var err error
		data, err = files.readCache(task)
		return err
	})
	return data, err
}
func (s *BTService) readMetadataCache(ctx context.Context, task models.BTTask) ([]byte, error) {
	var data []byte
	err := s.withTaskCache(ctx, task.ID, func(_ context.Context, db *gorm.DB, files *btCacheFiles) error {
		var current models.BTTask
		if db.First(&current, "id = ?", task.ID).Error != nil {
			return ErrBTTask
		}
		if !sameBTCacheIntent(current, task) {
			return ErrBTConflict
		}
		var err error
		data, err = files.readMetadataCache(task)
		return err
	})
	return data, err
}
func (s *BTService) removeCacheVersion(ctx context.Context, id, digest string) error {
	return s.withTaskCache(ctx, id, func(_ context.Context, db *gorm.DB, files *btCacheFiles) error {
		var task models.BTTask
		err := db.First(&task, "id = ?", id).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrBTTask
		}
		if err == nil && (task.ManifestDigest == digest || task.UnresolvedWrite || task.Stage == "uncertain") {
			return ErrBTConflict
		}
		return files.removeCacheVersion(id, digest)
	})
}
func (s *BTService) removeTaskCaches(ctx context.Context, id string) error {
	return s.withTaskCache(ctx, id, func(_ context.Context, _ *gorm.DB, files *btCacheFiles) error { return files.removeTaskCaches(id) })
}

func sameBTCacheIntent(current, expected models.BTTask) bool {
	return current.ID == expected.ID && current.ConnectionID == expected.ConnectionID && current.Revision == expected.Revision && current.Stage == expected.Stage && current.AttemptGeneration == expected.AttemptGeneration && current.ManifestDigest == expected.ManifestDigest && current.PendingAction == expected.PendingAction && current.UnresolvedWrite == expected.UnresolvedWrite
}
func (s *BTService) cleanupTaskCaches(ctx context.Context, expected models.BTTask) error {
	return s.withTaskCache(ctx, expected.ID, func(_ context.Context, db *gorm.DB, files *btCacheFiles) error {
		var current models.BTTask
		if db.First(&current, "id = ?", expected.ID).Error != nil {
			return ErrBTTask
		}
		if !sameBTCacheIntent(current, expected) {
			return ErrBTConflict
		}
		allowed := current.Stage == "cancelled" || current.Stage == "abandoned" || current.Stage == "released" || (current.Stage == "active" && !current.UnresolvedWrite && current.PendingAction == "")
		if !allowed {
			return ErrBTConflict
		}
		return files.removeTaskCaches(current.ID)
	})
}
