package services

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
	"video-master/models"
	"video-master/services/btmanifest"
)

func btPathKey(path string) string {
	return btDigest([]byte(cases.Fold().String(norm.NFC.String(filepath.ToSlash(path)))))
}
func btMagnetHashes(raw string) (string, string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "magnet" || len(raw) > 8192 {
		return "", "", ErrBTTask
	}
	v1, v2 := "", ""
	for _, xt := range u.Query()["xt"] {
		if strings.HasPrefix(xt, "urn:btih:") {
			value := strings.TrimPrefix(xt, "urn:btih:")
			if len(value) == 32 {
				decoded, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(value))
				if e != nil {
					return "", "", ErrBTTask
				}
				value = hex.EncodeToString(decoded)
			}
			if len(value) != 40 {
				return "", "", ErrBTTask
			}
			if _, e := hex.DecodeString(value); e != nil {
				return "", "", ErrBTTask
			}
			if v1 != "" && v1 != strings.ToLower(value) {
				return "", "", ErrBTTask
			}
			v1 = strings.ToLower(value)
		}
		if strings.HasPrefix(xt, "urn:btmh:1220") {
			value := strings.TrimPrefix(xt, "urn:btmh:1220")
			if len(value) != 64 {
				return "", "", ErrBTTask
			}
			if _, e := hex.DecodeString(value); e != nil {
				return "", "", ErrBTTask
			}
			if v2 != "" && v2 != strings.ToLower(value) {
				return "", "", ErrBTTask
			}
			v2 = strings.ToLower(value)
		}
	}
	if v1 == "" && v2 == "" {
		return "", "", ErrBTTask
	}
	return v1, v2, nil
}
func btManifestFiles(taskID string, m btmanifest.Manifest) []models.BTFile {
	files := make([]models.BTFile, 0, len(m.Files))
	for i, f := range m.Files {
		row := models.BTFile{TaskID: taskID, ManifestFileID: f.ID, Position: i, RelativePath: f.Path, Size: f.Size, Padding: f.Padding, Selectable: f.Selectable, ImportState: "pending"}
		if !f.Padding {
			key := btPathKey(f.Path)
			row.PathKey = &key
		}
		files = append(files, row)
	}
	defaults := btDefaultSelection(files)
	for i := range files {
		files[i].Selected = defaults[files[i].ManifestFileID]
	}
	return files
}
func btFindExistingIdentity(tx *gorm.DB, connectionID, v1, v2 string) (*models.BTTask, error) {
	q := tx.Where("connection_id = ?", connectionID)
	if v1 != "" && v2 != "" {
		q = q.Where("infohash_v1 = ? OR infohash_v2 = ?", v1, v2)
	} else if v1 != "" {
		q = q.Where("infohash_v1 = ?", v1)
	} else {
		q = q.Where("infohash_v2 = ?", v2)
	}
	var rows []models.BTTask
	if q.Find(&rows).Error != nil {
		return nil, ErrBTTask
	}
	if len(rows) > 1 {
		return nil, ErrBTConflict
	}
	if len(rows) == 1 {
		return &rows[0], nil
	}
	return nil, nil
}
func (s *BTService) Preview(ctx context.Context, token string) (BTTaskView, error) {
	ctx, leave, err := s.enter(ctx)
	if err != nil {
		return BTTaskView{}, err
	}
	defer leave()
	if err = s.loadClients(ctx); err != nil {
		return BTTaskView{}, err
	}
	if err = s.writeAllowed(ctx); err != nil {
		return BTTaskView{}, err
	}
	description, err := s.search.Describe(token)
	if err != nil {
		return BTTaskView{}, err
	}
	result, err := s.search.FetchProxy(ctx, token)
	if err != nil {
		return BTTaskView{}, err
	}
	data := result.Torrent
	var manifest btmanifest.Manifest
	stage := "draft"
	if result.Magnet != "" {
		if err = s.remote.CheckQBMetadataProfile(ctx); err != nil {
			return BTTaskView{}, ErrBTMetadata
		}
		v1, v2, e := btMagnetHashes(result.Magnet)
		if e != nil {
			return BTTaskView{}, e
		}
		data = []byte(result.Magnet)
		manifest = btmanifest.Manifest{Name: description.Title, InfoHashV1: v1, InfoHashV2: v2, Digest: btDigest(data)}
		stage = "metadata_adding"
	} else {
		manifest, err = btmanifest.Parse(data)
		if err == nil {
			manifest, err = btOriginalLayout(manifest)
		}
		if err != nil {
			return BTTaskView{}, err
		}
	}
	connection, err := s.connection.current(ctx)
	if err != nil || connection.ID != s.loadedID {
		return BTTaskView{}, ErrBTConflict
	}
	var existing *models.BTTask
	err = btTaskOperation(ctx, func(db *gorm.DB) error {
		existing, err = btFindExistingIdentity(db, connection.ID, manifest.InfoHashV1, manifest.InfoHashV2)
		return err
	})
	if err != nil {
		return BTTaskView{}, err
	}
	id := uuid.NewString()
	hadExisting := existing != nil
	if hadExisting {
		id = existing.ID
	}
	var view BTTaskView
	err = s.withTaskCache(ctx, id, func(scoped context.Context, db *gorm.DB, _ *btCacheFiles) error {
		var current models.BTConnection
		if db.First(&current, "id = ? AND slot = ? AND revision = ? AND enabled = ?", connection.ID, "local", connection.Revision, true).Error != nil {
			return ErrBTConflict
		}
		latest, e := btFindExistingIdentity(db, current.ID, manifest.InfoHashV1, manifest.InfoHashV2)
		if e != nil {
			return e
		}
		if latest != nil && latest.ID != id || hadExisting && latest == nil {
			return ErrBTConflict
		}
		view, e = s.persistPreview(scoped, id, current, latest, description, data, manifest, stage, result.Magnet != "")
		return e
	})
	return view, err
}
func (s *BTService) persistPreview(ctx context.Context, id string, connection models.BTConnection, existing *models.BTTask, description BTResource, data []byte, manifest btmanifest.Manifest, stage string, magnet bool) (BTTaskView, error) {
	var err error
	if existing != nil {
		// A magnet's initial cache becomes exported torrent metadata when ready.
		// Repeating its valid token must still return the resolved owned draft;
		// it must not replace that manifest or start another metadata operation.
		if magnet && existing.ManifestDigest != manifest.Digest {
			known := (manifest.InfoHashV1 == "" || existing.InfoHashV1 != nil && *existing.InfoHashV1 == manifest.InfoHashV1) && (manifest.InfoHashV2 == "" || existing.InfoHashV2 != nil && *existing.InfoHashV2 == manifest.InfoHashV2)
			if known && existing.PendingAction == "metadata" && existing.SelectionDigest == "" && (existing.Stage == "uncertain" || existing.Stage == "abandoned") {
				// Re-search can restore the original metadata operation's immutable magnet
				// without changing the current exported manifest pointer.
				if e := s.writeCache(ctx, existing.ID, data); e != nil {
					return BTTaskView{}, e
				}
				return s.Get(ctx, existing.ID)
			}
			if known && (existing.Stage == "metadata_ready" || existing.Stage == "active" || existing.Stage == "cancelled" || existing.Stage == "released") {
				return s.Get(ctx, existing.ID)
			}
		}
		if existing.ManifestDigest != manifest.Digest {
			return BTTaskView{}, ErrBTConflict
		}
		if existing.Stage == "cancelled" && stage == "draft" && existing.AttemptGeneration == 0 && existing.QBHash == "" && !existing.UnresolvedWrite && existing.PendingAction == "" {
			if _, e := s.readCache(ctx, *existing); e != nil {
				path, pathErr := s.cachePath(ctx, existing.ID, existing.ManifestDigest)
				if pathErr != nil {
					return BTTaskView{}, pathErr
				}
				if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
					return BTTaskView{}, ErrBTPath
				}
				if e = s.writeCache(ctx, existing.ID, data); e != nil {
					return BTTaskView{}, e
				}
			}
			if e := s.reactivateTask(ctx, existing, map[string]any{"stage": "draft", "updated_at": time.Now().UTC(), "error_code": ""}); e != nil {
				return BTTaskView{}, e
			}
			return s.Get(ctx, existing.ID)
		}

		// Re-previewing the exact manifest restores the private cache discarded by
		// abandon, while retaining the original task/root/tag and selection.
		if existing.Stage == "abandoned" || existing.Stage == "uncertain" || existing.Stage == "conflict" || existing.Stage == "draft" {
			if _, e := s.readCache(ctx, *existing); e != nil {
				path, pathErr := s.cachePath(ctx, existing.ID, existing.ManifestDigest)
				if pathErr != nil {
					return BTTaskView{}, pathErr
				}
				if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
					return BTTaskView{}, ErrBTPath
				}
				if e = s.writeCache(ctx, existing.ID, data); e != nil {
					return BTTaskView{}, e
				}
			}
		}
		if existing.Stage == "uncertain" && existing.PendingAction == "restore" && existing.SelectionDigest == "" {
			_, files, e := btLoadTask(ctx, existing.ID)
			if e != nil {
				return BTTaskView{}, e
			}
			if e = s.prepareRestoredDraft(ctx, existing, files); e != nil {
				return BTTaskView{}, e
			}
		}
		return s.Get(ctx, existing.ID)
	}
	if err = s.deps.ValidateRoot(ctx, connection.DownloadRoot); err != nil {
		return BTTaskView{}, btRootValidationError(err)
	}
	namespace, err := s.deps.EnsureNamespace(connection.DownloadRoot, connection.ID)
	if err != nil {
		return BTTaskView{}, ErrBTPath
	}
	namespace, err = filepath.EvalSymlinks(namespace)
	if err != nil {
		return BTTaskView{}, ErrBTPath
	}
	root := filepath.Join(namespace, id)
	saveRoot := filepath.Join(connection.DownloadRoot, "CineInsight", id)
	lexicalNamespace, err := filepath.EvalSymlinks(filepath.Dir(saveRoot))
	if err != nil || filepath.Clean(lexicalNamespace) != filepath.Clean(namespace) {
		return BTTaskView{}, ErrBTPath
	}
	if _, err = os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		return BTTaskView{}, ErrBTPath
	}
	title := description.Title
	if strings.TrimSpace(title) == "" {
		title = manifest.Name
	}
	identity := manifest.InfoHashV1
	if identity == "" {
		identity = manifest.InfoHashV2
	}
	task := models.BTTask{ID: id, ConnectionID: connection.ID, Revision: 1, Title: title, IndexerID: description.IndexerID, IndexerName: description.Indexer, IntentKey: connection.ID + ":" + identity, OwnerTag: "cineinsight-" + id, ManifestDigest: manifest.Digest, InfoHashV1: btString(manifest.InfoHashV1), InfoHashV2: btString(manifest.InfoHashV2), SaveRoot: saveRoot, CanonicalRoot: root, RootKey: btPathKey(root), Stage: stage}
	files := btManifestFiles(id, manifest)
	if err = s.writeCache(ctx, id, data); err != nil {
		return BTTaskView{}, err
	}
	err = btTaskTransaction(ctx, func(tx *gorm.DB) error {
		if err := ClaimBTTaskEpoch(tx, connection.ID, connection.Revision, connection.TaskEpoch); err != nil {
			return err
		}
		if tx.Omit("Connection").Create(&task).Error != nil {
			return ErrBTConflict
		}
		if len(files) > 0 && tx.Omit("Task").Create(&files).Error != nil {
			return ErrBTTask
		}
		return nil
	})
	if err != nil {
		_ = s.removeCacheVersion(ctx, id, btDigest(data))
		return BTTaskView{}, err
	}
	s.changed(task)
	if stage == "metadata_adding" {
		s.notify()
	}
	return btView(task, files), nil
}
func (s *BTService) Submit(ctx context.Context, r BTSubmitRequest) (BTTaskView, error) {
	ctx, leave, err := s.enter(ctx)
	if err != nil {
		return BTTaskView{}, err
	}
	defer leave()
	if err = s.loadClients(ctx); err != nil {
		return BTTaskView{}, err
	}
	if err = s.writeAllowed(ctx); err != nil {
		return BTTaskView{}, err
	}
	var view BTTaskView
	err = s.withTaskCache(ctx, r.DraftID, func(scoped context.Context, _ *gorm.DB, _ *btCacheFiles) error {
		var err error
		view, err = s.submitTaskLocked(scoped, r)
		return err
	})
	return view, err
}
func (s *BTService) submitTaskLocked(ctx context.Context, r BTSubmitRequest) (BTTaskView, error) {
	task, files, err := btLoadTask(ctx, r.DraftID)
	if err != nil {
		return BTTaskView{}, err
	}
	if task.ConnectionID != s.loadedID {
		return BTTaskView{}, ErrBTConflict
	}
	wanted := map[string]bool{}
	for _, id := range r.SelectedFileIDs {
		if wanted[id] {
			return BTTaskView{}, ErrBTSelection
		}
		wanted[id] = true
	}
	if len(wanted) == 0 {
		return BTTaskView{}, ErrBTSelection
	}
	found := 0
	for _, f := range files {
		if wanted[f.ManifestFileID] {
			if !f.Selectable || f.Padding {
				return BTTaskView{}, ErrBTSelection
			}
			found++
		}
	}
	if found != len(wanted) {
		return BTTaskView{}, ErrBTSelection
	}
	digest := btSelectionDigest(r.SelectedFileIDs)
	if task.Stage != "draft" && task.Stage != "metadata_ready" {
		if task.SelectionDigest == digest {
			return s.Get(ctx, task.ID)
		}
		return BTTaskView{}, ErrBTConflict
	}
	if task.Revision != r.ExpectedRevision {
		return BTTaskView{}, ErrBTConflict
	}
	if time.Now().After(task.UpdatedAt.Add(24 * time.Hour)) {
		return BTTaskView{}, ErrBTResultExpired
	}
	if _, err = s.readCache(ctx, task); err != nil {
		return BTTaskView{}, err
	}
	if _, _, err = s.findRemote(ctx, task); err != nil && !errors.Is(err, errBTMissing) {
		return BTTaskView{}, err
	}
	err = btTaskTransaction(ctx, func(tx *gorm.DB) error {
		result := tx.Model(&models.BTTask{}).Where("id = ? AND revision = ? AND stage = ?", task.ID, task.Revision, task.Stage).Updates(map[string]any{"stage": "queued_add", "pending_action": "add", "selection_digest": digest, "revision": task.Revision + 1, "error_code": ""})
		if result.Error != nil {
			return ErrBTTask
		}
		if result.RowsAffected != 1 {
			return ErrBTConflict
		}
		if tx.Model(&models.BTFile{}).Where("task_id = ?", task.ID).Update("selected", false).Error != nil {
			return ErrBTTask
		}
		return tx.Model(&models.BTFile{}).Where("task_id = ? AND manifest_file_id IN ?", task.ID, r.SelectedFileIDs).Update("selected", true).Error
	})
	if err != nil {
		return BTTaskView{}, err
	}
	s.notify()
	return s.Get(ctx, task.ID)
}

// prepareRestoredDraft never treats default Selected flags as confirmation.
// Its result is a file-choice draft or a metadata-only preparation intent.
func (s *BTService) prepareRestoredDraft(ctx context.Context, task *models.BTTask, files []models.BTFile) error {
	data, err := s.readCache(ctx, *task)
	if err != nil {
		return err
	}
	stage, action := "draft", "restore"
	if btMagnetMatchesTask(data, *task) {
		if s.remote.CheckQBMetadataProfile(ctx) != nil {
			return ErrBTMetadata
		}
		stage, action = "metadata_adding", "metadata"
	} else {
		manifest, e := btmanifest.Parse(data)
		if e != nil {
			return e
		}
		manifest, e = btOriginalLayout(manifest)
		if e != nil {
			return e
		}
		if (task.InfoHashV1 != nil && *task.InfoHashV1 != manifest.InfoHashV1) || (task.InfoHashV2 != nil && *task.InfoHashV2 != manifest.InfoHashV2) || len(files) != len(manifest.Files) {
			return ErrBTChanged
		}
		for i, file := range files {
			mf := manifest.Files[i]
			if file.ManifestFileID != mf.ID || file.RelativePath != mf.Path || file.Size != mf.Size || file.Padding != mf.Padding {
				return ErrBTChanged
			}
		}
	}
	fields := map[string]any{"stage": stage, "pending_action": action, "unresolved_write": true, "updated_at": time.Now().UTC(), "error_code": "write_history_unresolved"}
	if task.Stage == "abandoned" {
		return s.reactivateTask(ctx, task, fields)
	}
	return s.updateTask(ctx, task, fields)
}
