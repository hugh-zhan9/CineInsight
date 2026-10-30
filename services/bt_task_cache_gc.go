package services

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"gorm.io/gorm"
	"video-master/database"
	"video-master/models"
)

// GC is scoped to the currently leased physical DB. A cursor is never reused
// after a DB switch, and every ref check plus unlink shares the publisher lock.
func (s *BTService) expireOrphanCaches(ctx context.Context) error {
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		scope, err := s.resolveCacheScope(ctx, db)
		if err != nil {
			return err
		}
		if s.cacheSweepScope != scope {
			if s.cacheSweep != nil {
				_ = s.cacheSweep.Close()
				s.cacheSweep = nil
			}
			s.cacheSweepScope = scope
		}
		root := filepath.Join(s.dataDir, "bt-previews", scope)
		if s.cacheSweep == nil {
			info, e := os.Lstat(root)
			if errors.Is(e, os.ErrNotExist) {
				return nil
			}
			if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return ErrBTPath
			}
			s.cacheSweep, e = os.Open(root)
			if e != nil {
				return ErrBTTask
			}
		}
		dirs, readErr := s.cacheSweep.ReadDir(50)
		if readErr != nil {
			_ = s.cacheSweep.Close()
			s.cacheSweep = nil
			if !errors.Is(readErr, io.EOF) {
				return ErrBTTask
			}
		}
		cutoff := time.Now().Add(-24 * time.Hour)
		for _, dir := range dirs {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			id := dir.Name()
			if !dir.IsDir() || !btTaskUUID(id) {
				continue
			}
			err = s.withResolvedTaskCache(ctx, db, scope, id, func(_ context.Context, lockedDB *gorm.DB, files *btCacheFiles) error {
				entries, e := files.cacheEntries(id)
				if e != nil {
					return e
				}
				var task models.BTTask
				found := false
				e = lockedDB.Select("id", "stage", "manifest_digest", "unresolved_write", "pending_action", "revision").First(&task, "id = ?", id).Error
				if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
					return ErrBTTask
				}
				found = e == nil
				for _, entry := range entries {
					info, e := entry.Info()
					if e != nil || !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
						continue
					}
					digest, version := btVersionName(entry.Name())
					if version {
						if found && (digest == task.ManifestDigest || task.UnresolvedWrite || task.Stage == "uncertain") {
							continue
						}
					} else if !btCacheTemp(entry.Name()) {
						continue
					}
					if e = os.Remove(filepath.Join(files.cacheDir(id), entry.Name())); e != nil && !errors.Is(e, os.ErrNotExist) {
						return ErrBTTask
					}
				}
				_ = os.Remove(files.cacheDir(id))
				return nil
			})
			if errors.Is(err, ErrBTTaskBusy) {
				continue
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}
