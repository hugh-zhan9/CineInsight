package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"gorm.io/gorm"
	"video-master/models"
	"video-master/services/btclient"
	"video-master/services/btmanifest"
)

func (s *BTService) Poll(ctx context.Context) error {
	ctx, leave, err := s.enter(ctx)
	if err != nil {
		return err
	}
	defer leave()
	if err = s.loadClients(ctx); err != nil {
		return err
	}
	if err = s.writeAllowed(ctx); err != nil {
		return err
	}
	var tasks []models.BTTask
	load := func() error {
		return btTaskOperation(ctx, func(db *gorm.DB) error {
			query := db.Where("connection_id = ? AND stage NOT IN ?", s.loadedID, []string{"draft", "cancelled", "released"})
			if s.pollID != "" {
				query = query.Where("created_at > ? OR (created_at = ? AND id > ?)", s.pollCursor, s.pollCursor, s.pollID)
			}
			return query.Order("created_at ASC,id ASC").Limit(200).Find(&tasks).Error
		})
	}
	if err = load(); err != nil {
		return ErrBTTask
	}
	if len(tasks) == 0 && s.pollID != "" {
		s.pollCursor = time.Time{}
		s.pollID = ""
		if err = load(); err != nil {
			return ErrBTTask
		}
	}
	for _, task := range tasks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Advancing on per-task errors prevents an old broken task from starving new work.
		s.pollCursor = task.CreatedAt
		s.pollID = task.ID
		_ = s.advance(ctx, task)
	}
	if len(tasks) < 200 {
		s.pollCursor = time.Time{}
		s.pollID = ""
	}

	return nil
}
func (s *BTService) failTask(ctx context.Context, t *models.BTTask, stage string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	code := btErrorCode(err)
	if t.Stage == stage && t.ErrorCode == code {
		return nil
	}
	return s.updateTask(ctx, t, map[string]any{"stage": stage, "error_code": code})
}
func (s *BTService) failObservation(ctx context.Context, t *models.BTTask, stage string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	code := btErrorCode(err)
	if t.Stage == stage && t.ErrorCode == code {
		return nil
	}
	return s.updateObservedTask(ctx, t, map[string]any{"stage": stage, "error_code": code})
}

func (s *BTService) failObservedWrite(ctx context.Context, t *models.BTTask, err error) error {
	stage := "uncertain"
	if errors.Is(err, ErrBTExternal) || errors.Is(err, ErrBTChanged) || errors.Is(err, ErrBTPath) || errors.Is(err, ErrBTSelection) {
		stage = "conflict"
	}
	return s.failTask(ctx, t, stage, err)
}

func (s *BTService) advance(ctx context.Context, task models.BTTask) error {
	_, files, err := btLoadTask(ctx, task.ID)
	if err != nil {
		return err
	}
	if btSentStage(task.Stage) {
		return s.updateTask(ctx, &task, map[string]any{"stage": "uncertain", "unresolved_write": true, "error_code": "submission_uncertain"})
	}
	switch task.Stage {
	case "queued_add":
		return s.addTask(ctx, &task, files, false)
	case "queued_metadata":
		return s.addTask(ctx, &task, files, true)
	case "metadata_adding":
		if task.AttemptGeneration == 0 {
			return s.addTask(ctx, &task, files, true)
		}
		return s.reconcileTask(ctx, &task, files)
	case "selecting", "priority_select_ready":
		return s.selectAndStart(ctx, &task, files)
	case "start_ready", "queued_stop", "queued_start":
		return s.runControl(ctx, &task, files, task.Stage != "queued_stop")
	case "cancel_pending":
		return s.cancelRemoteMetadata(ctx, &task)
	case "metadata_wait":
		return s.finishMetadata(ctx, &task)
	case "active":
		return s.observeFiles(ctx, &task, files)
	case "starting", "adding":
		return s.updateTask(ctx, &task, map[string]any{"stage": "uncertain", "pending_action": btActionForStage(task.Stage), "unresolved_write": true, "error_code": "submission_uncertain"})
	case "uncertain", "missing", "conflict", "abandoned":
		return s.reconcileTask(ctx, &task, files)
	}
	return nil
}
func (s *BTService) addTask(ctx context.Context, task *models.BTTask, files []models.BTFile, metadata bool) error {
	if !metadata && !btConfirmedSelection(*task, files) {
		return s.failTask(ctx, task, "conflict", ErrBTSelection)
	}
	if metadata && s.remote.CheckQBMetadataProfile(ctx) != nil {
		return s.failTask(ctx, task, "conflict", ErrBTMetadata)
	}
	connection, err := s.connection.current(ctx)
	if err != nil || connection.ID != task.ConnectionID {
		return ErrBTConflict
	}
	if e := s.deps.ValidateRoot(ctx, connection.DownloadRoot); e != nil {
		return s.failTask(ctx, task, "conflict", btRootValidationError(e))
	}
	namespace, err := s.deps.EnsureNamespace(connection.DownloadRoot, connection.ID)
	if err != nil {
		return s.failTask(ctx, task, "conflict", ErrBTPath)
	}
	realNamespace, err := filepath.EvalSymlinks(namespace)
	if err != nil || filepath.Clean(task.CanonicalRoot) != filepath.Join(realNamespace, task.ID) {
		return s.failTask(ctx, task, "conflict", ErrBTPath)
	}
	remote, _, err := s.findRemote(ctx, *task)
	if err == nil {
		return s.continueOwnedAddition(ctx, task, files, metadata, remote)
	}
	if !errors.Is(err, errBTMissing) {
		return s.failTask(ctx, task, "conflict", err)
	}
	data, err := s.readCache(ctx, *task)
	if metadata {
		data, err = s.readMetadataCache(ctx, *task)
	}
	action := "add"
	if metadata {
		action = "metadata"
	}
	if err != nil {
		return s.updateTask(ctx, task, map[string]any{"stage": "uncertain", "pending_action": action, "error_code": btErrorCode(err)})
	}
	if _, err = os.Lstat(task.CanonicalRoot); errors.Is(err, os.ErrNotExist) {
		if os.Mkdir(task.CanonicalRoot, 0700) != nil || btVerifyRoot(*task) != nil {
			return s.failTask(ctx, task, "conflict", ErrBTPath)
		}
	} else if err != nil || btVerifyRoot(*task) != nil {
		return s.failTask(ctx, task, "conflict", ErrBTPath)
	} else if task.AttemptGeneration == 0 {
		entries, e := os.ReadDir(task.CanonicalRoot)
		if e != nil || len(entries) != 0 {
			return s.failTask(ctx, task, "conflict", ErrBTPath)
		}
	}
	claim, err := s.claimWrite(ctx, task, action)
	if err != nil {
		return err
	}
	// This narrows the add race, but qB has no atomic "only if absent" operation.
	remote, _, err = s.findRemote(ctx, *task)
	if err == nil {
		stage := "selecting"
		if metadata {
			stage = "metadata_wait"
		} else if !btStopped(remote.State) {
			return s.failTask(ctx, task, "conflict", ErrBTChanged)
		}
		fields := map[string]any{"qb_hash": remote.Hash}
		if metadata {
			fields["pending_action"] = "metadata"
		}
		if err = s.acknowledgeWrite(ctx, task, claim, stage, fields); err != nil {
			return err
		}
		if metadata {
			return nil
		}
		return s.selectAndStart(ctx, task, files)
	}
	if !errors.Is(err, errBTMissing) {
		return s.failTask(ctx, task, "uncertain", err)
	}
	options := btclient.QBAddOptions{SavePath: task.CanonicalRoot, OwnerTag: task.OwnerTag, MetadataOnly: metadata}
	if metadata {
		options.Magnet = string(data)
	} else {
		options.Torrent = data
	}
	hash, err := s.remote.QBAddStopped(ctx, options)
	if err != nil {
		return s.failTask(ctx, task, "uncertain", err)
	}
	if err = s.updateTask(ctx, task, map[string]any{"qb_hash": hash}); err != nil {
		return err
	}
	remote, _, err = s.findRemote(ctx, *task)
	if err != nil {
		return s.failTask(ctx, task, "uncertain", err)
	}
	stage := "metadata_wait"
	if !metadata {
		remote, err = s.waitAddedStopped(ctx, *task, remote)
		if err != nil {
			return s.failObservedWrite(ctx, task, err)
		}
		if _, _, err = s.remoteFiles(ctx, *task, files, true, 0); err != nil {
			return s.failTask(ctx, task, "conflict", err)
		}
		stage = "selecting"
	}
	fields := map[string]any{"qb_hash": remote.Hash}
	if metadata {
		fields["pending_action"] = "metadata"
	}
	if err = s.acknowledgeWrite(ctx, task, claim, stage, fields); err != nil {
		return err
	}
	if metadata {
		return nil
	}
	return s.selectAndStart(ctx, task, files)
}

// A successful stopped-add can initially report checkingResumeData. Stay inside
// the original sent claim and perform only bounded reads; no request is replayed.
// Stopped readiness still flows through the full manifest/priority verification.
func (s *BTService) waitAddedStopped(ctx context.Context, task models.BTTask, remote btclient.QBTorrent) (btclient.QBTorrent, error) {
	waiting, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := waiting.Err(); err != nil {
			return remote, err
		}
		if btStopped(remote.State) {
			return remote, nil
		}
		if remote.State != "checkingResumeData" {
			return remote, ErrBTChanged
		}
		select {
		case <-waiting.Done():
			return remote, waiting.Err()
		case <-ticker.C:
			var err error
			remote, _, err = s.findRemote(waiting, task)
			if err != nil {
				return remote, err
			}
		}
	}
}

func (s *BTService) continueOwnedAddition(ctx context.Context, task *models.BTTask, files []models.BTFile, metadata bool, remote btclient.QBTorrent) error {
	if metadata {
		return s.updateTask(ctx, task, map[string]any{"stage": "metadata_wait", "qb_hash": remote.Hash, "pending_action": "metadata"})
	}
	if !btStopped(remote.State) {
		return s.failTask(ctx, task, "conflict", ErrBTChanged)
	}
	if err := s.updateTask(ctx, task, map[string]any{"stage": "selecting", "qb_hash": remote.Hash, "pending_action": "priority_zero"}); err != nil {
		return err
	}
	return s.selectAndStart(ctx, task, files)
}

// mode 0 verifies manifest, 1 also requires all priorities zero, 2 verifies the
// saved selection. Every write uses a fresh snapshot; this is not a remote lock.
func (s *BTService) remoteFiles(ctx context.Context, task models.BTTask, files []models.BTFile, stopped bool, mode int) (btclient.QBTorrent, map[string]btclient.QBFile, error) {
	remote, _, err := s.findRemote(ctx, task)
	if err != nil {
		return remote, nil, err
	}
	rows, err := s.remote.QBFiles(ctx, remote.Hash)
	if err != nil {
		return remote, nil, err
	}
	mapping, err := btMapFiles(files, rows)
	if err != nil {
		return remote, nil, err
	}
	if mode == 1 {
		for _, row := range mapping {
			if row.Priority != 0 {
				return remote, nil, ErrBTChanged
			}
		}
	}
	if mode == 2 && !btSelectionMatches(files, mapping) {
		return remote, nil, ErrBTChanged
	}
	fresh, _, err := s.findRemote(ctx, task)
	if err != nil {
		return fresh, nil, err
	}
	if stopped && !btStopped(fresh.State) {
		return fresh, nil, ErrBTChanged
	}
	return fresh, mapping, nil
}
func (s *BTService) selectAndStart(ctx context.Context, task *models.BTTask, files []models.BTFile) error {
	if !btConfirmedSelection(*task, files) {
		return s.failTask(ctx, task, "conflict", ErrBTSelection)
	}
	if task.Stage == "selecting" {
		claim, err := s.claimWrite(ctx, task, "priority_zero")
		if err != nil {
			return err
		}
		remote, mapping, err := s.remoteFiles(ctx, *task, files, true, 0)
		if err != nil {
			return s.failObservedWrite(ctx, task, err)
		}
		all := []int{}
		selected := 0
		for _, f := range files {
			if f.Padding {
				continue
			}
			all = append(all, mapping[f.ManifestFileID].Index)
			if f.Selected {
				selected++
			}
		}
		if selected == 0 {
			return s.failTask(ctx, task, "conflict", ErrBTSelection)
		}
		if err = s.remote.QBPriority(ctx, remote.Hash, all, 0); err != nil {
			return s.failObservedWrite(ctx, task, err)
		}
		if _, _, err = s.remoteFiles(ctx, *task, files, true, 1); err != nil {
			return s.failObservedWrite(ctx, task, err)
		}
		if err = s.acknowledgeWrite(ctx, task, claim, "priority_select_ready", nil); err != nil {
			return err
		}
	}
	if task.Stage == "priority_select_ready" {
		claim, err := s.claimWrite(ctx, task, "priority_select")
		if err != nil {
			return err
		}
		remote, mapping, err := s.remoteFiles(ctx, *task, files, true, 1)
		if err != nil {
			return s.failObservedWrite(ctx, task, err)
		}
		selected := []int{}
		for _, f := range files {
			if !f.Padding && f.Selected {
				selected = append(selected, mapping[f.ManifestFileID].Index)
			}
		}
		if len(selected) == 0 {
			return s.failTask(ctx, task, "conflict", ErrBTSelection)
		}
		if err = s.remote.QBPriority(ctx, remote.Hash, selected, 1); err != nil {
			return s.failObservedWrite(ctx, task, err)
		}
		_, mapping, err = s.remoteFiles(ctx, *task, files, true, 2)
		if err != nil {
			return s.failObservedWrite(ctx, task, err)
		}
		// Index observations and the next ready phase share the generation claim.
		err = btTaskTransaction(ctx, func(tx *gorm.DB) error {
			guard := tx.Model(&models.BTTask{}).Where("id = ? AND connection_id = ? AND revision = ? AND stage = ? AND attempt_generation = ?", task.ID, task.ConnectionID, task.Revision, task.Stage, task.AttemptGeneration).UpdateColumn("revision", task.Revision)
			if guard.Error != nil {
				return ErrBTTask
			}
			if guard.RowsAffected != 1 {
				return ErrBTConflict
			}
			for _, f := range files {
				if f.Padding {
					continue
				}
				if tx.Model(&models.BTFile{}).Where("task_id = ? AND manifest_file_id = ?", task.ID, f.ManifestFileID).Update("qb_file_index", mapping[f.ManifestFileID].Index).Error != nil {
					return ErrBTTask
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if err = s.acknowledgeWrite(ctx, task, claim, "start_ready", nil); err != nil {
			return err
		}
	}
	if task.Stage == "start_ready" {
		return s.runControl(ctx, task, files, true)
	}
	return ErrBTConflict
}
func (s *BTService) runControl(ctx context.Context, task *models.BTTask, files []models.BTFile, start bool) error {
	if start && !btConfirmedSelection(*task, files) {
		return s.failTask(ctx, task, "conflict", ErrBTSelection)
	}
	action := "stop"
	if start {
		action = "start"
	}
	claim, err := s.claimWrite(ctx, task, action)
	if err != nil {
		return err
	}
	remote, _, err := s.remoteFiles(ctx, *task, files, false, 2)
	if err != nil {
		return s.failObservedWrite(ctx, task, err)
	}
	sent := false
	if btStopped(remote.State) == start {
		if err = s.remote.QBRun(ctx, remote.Hash, start); err != nil {
			return s.failObservedWrite(ctx, task, err)
		}
		sent = true
	}
	fresh, _, err := s.remoteFiles(ctx, *task, files, false, 2)
	if err != nil {
		return s.failObservedWrite(ctx, task, err)
	}
	if sent {
		fresh, err = s.waitRunTarget(ctx, *task, files, fresh, start)
		if err != nil {
			return s.failObservedWrite(ctx, task, err)
		}
	} else if btStopped(fresh.State) == start {
		return s.failTask(ctx, task, "uncertain", ErrBTChanged)
	}
	return s.acknowledgeAndCleanup(ctx, task, claim, "active", map[string]any{"qb_hash": fresh.Hash, "remote_state": fresh.State})
}

// qB acknowledges a start/stop request before its state necessarily reflects it.
// Only a successful request from this claim authorizes bounded confirmation;
// every read still verifies identity, paths, manifest and the saved priorities.
func (s *BTService) waitRunTarget(ctx context.Context, task models.BTTask, files []models.BTFile, remote btclient.QBTorrent, start bool) (btclient.QBTorrent, error) {
	waiting, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := waiting.Err(); err != nil {
			return remote, err
		}
		if btStopped(remote.State) != start {
			return remote, nil
		}
		select {
		case <-waiting.Done():
			return remote, waiting.Err()
		case <-ticker.C:
			var err error
			remote, _, err = s.remoteFiles(waiting, task, files, false, 2)
			if err != nil {
				return remote, err
			}
		}
	}
}

func (s *BTService) reconcileTask(ctx context.Context, task *models.BTTask, files []models.BTFile) error {
	remote, _, err := s.findRemote(ctx, *task)
	unresolved := task.UnresolvedWrite || task.PendingAction != "" || task.Stage == "uncertain" || task.Stage == "abandoned"
	if errors.Is(err, errBTMissing) {
		if unresolved {
			return nil
		}
		return s.failObservation(ctx, task, "missing", err)
	}
	if err != nil {
		return s.failObservation(ctx, task, "conflict", err)
	}
	if unresolved {
		// Observing the requested state does not prove a timed-out earlier command
		// cannot arrive later. Only an explicit user operation may issue another write.
		return s.recordObservation(ctx, task, files, nil, remote.State)
	}
	if len(files) == 0 {
		return s.finishMetadata(ctx, task)
	}
	if !btConfirmedSelection(*task, files) {
		return s.failObservation(ctx, task, "conflict", ErrBTSelection)
	}
	rows, err := s.remote.QBFiles(ctx, remote.Hash)
	if err != nil {
		return err
	}
	mapping, err := btMapFiles(files, rows)
	if err != nil || !btSelectionMatches(files, mapping) {
		return s.failObservation(ctx, task, "conflict", ErrBTChanged)
	}
	if err = s.updateObservedTask(ctx, task, map[string]any{"stage": "active", "qb_hash": remote.Hash, "remote_state": remote.State, "error_code": ""}); err != nil {
		return err
	}
	if !task.UnresolvedWrite {
		_ = s.cleanupTaskCaches(ctx, *task)
	}
	return nil
}
func (s *BTService) observeFiles(ctx context.Context, task *models.BTTask, files []models.BTFile) error {
	if !btConfirmedSelection(*task, files) {
		return s.failObservation(ctx, task, "conflict", ErrBTSelection)
	}
	remote, _, err := s.findRemote(ctx, *task)
	if err != nil {
		stage := "conflict"
		if errors.Is(err, errBTMissing) {
			stage = "missing"
		}
		return s.failObservation(ctx, task, stage, err)
	}
	rows, err := s.remote.QBFiles(ctx, remote.Hash)
	if err != nil {
		return err
	}
	mapping, err := btMapFiles(files, rows)
	if err != nil || !btSelectionMatches(files, mapping) {
		return s.failObservation(ctx, task, "conflict", ErrBTChanged)
	}
	if err = s.recordObservation(ctx, task, files, mapping, remote.State); err != nil {
		return err
	}
	for _, file := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if file.Padding || !file.Selected || btFileKind(file.RelativePath) != "video" || mapping[file.ManifestFileID].Progress != 1 || (file.ImportState == "imported" && file.PublicationState != "pending_new" && file.PublicationState != "pending_restore") || file.ImportState == "blocked_user_delete" {
			continue
		}
		// Importer is the only owner of import/publication outcomes. Re-read its
		// committed result even on error, rather than inventing a competing state.
		importErr := s.deps.ImportFile(ctx, task.ID, file.ManifestFileID)
		fresh, _, readErr := btLoadTask(ctx, task.ID)
		if readErr != nil {
			return readErr
		}
		s.changed(fresh)
		if importErr != nil && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}
func (s *BTService) finishMetadata(ctx context.Context, task *models.BTTask) error {
	remote, properties, err := s.findRemote(ctx, *task)
	if err != nil {
		return err
	}
	if properties.TotalDownloaded != 0 {
		return s.failObservation(ctx, task, "conflict", ErrBTMetadata)
	}
	if !btStopped(remote.State) {
		return nil
	}
	data, err := s.remote.QBExport(ctx, remote.Hash)
	if err != nil {
		return err
	}
	m, err := btmanifest.Parse(data)
	if err == nil {
		m, err = btOriginalLayout(m)
	}
	if err != nil {
		return s.failObservation(ctx, task, "conflict", err)
	}
	if (task.InfoHashV1 != nil && *task.InfoHashV1 != m.InfoHashV1) || (task.InfoHashV2 != nil && *task.InfoHashV2 != m.InfoHashV2) {
		return s.failObservation(ctx, task, "conflict", ErrBTChanged)
	}
	return s.withTaskCache(ctx, task.ID, func(ctx context.Context, db *gorm.DB, _ *btCacheFiles) error {
		var current models.BTTask
		if db.First(&current, "id = ?", task.ID).Error != nil {
			return ErrBTTask
		}
		if !sameBTCacheIntent(current, *task) || current.ObservationEpoch != task.ObservationEpoch {
			return ErrBTConflict
		}
		// Publish the new version before the pointer CAS, under the same task guard.
		if err = s.writeCache(ctx, task.ID, data); err != nil {
			return err
		}
		files := btManifestFiles(task.ID, m)
		err = btTaskTransaction(ctx, func(tx *gorm.DB) error {
			existing, err := btFindExistingIdentity(tx, task.ConnectionID, m.InfoHashV1, m.InfoHashV2)
			if err != nil {
				return err
			}
			if existing != nil && existing.ID != task.ID {
				return ErrBTConflict
			}
			result := tx.Model(&models.BTTask{}).Where("id = ? AND connection_id = ? AND revision = ? AND stage = ? AND attempt_generation = ? AND observation_epoch = ?", task.ID, task.ConnectionID, task.Revision, task.Stage, task.AttemptGeneration, task.ObservationEpoch).Updates(map[string]any{"manifest_digest": m.Digest, "infohash_v1": btString(m.InfoHashV1), "infohash_v2": btString(m.InfoHashV2), "qb_hash": remote.Hash, "stage": "metadata_ready", "remote_state": remote.State, "revision": task.Revision + 1})
			if result.Error != nil {
				return ErrBTConflict
			}
			if result.RowsAffected != 1 {
				return ErrBTConflict
			}
			if tx.Where("task_id = ?", task.ID).Delete(&models.BTFile{}).Error != nil {
				return ErrBTTask
			}
			if tx.Omit("Task").Create(&files).Error != nil {
				return ErrBTTask
			}
			return nil
		})
		if err != nil {
			return s.failObservation(ctx, task, "conflict", err)
		}
		if !task.UnresolvedWrite && task.ManifestDigest != m.Digest {
			_ = s.removeCacheVersion(ctx, task.ID, task.ManifestDigest)
		}
		if db.First(task, "id = ?", task.ID).Error != nil {
			return ErrBTTask
		}
		s.changed(*task)
		return nil
	})
}

func (s *BTService) cancelRemoteMetadata(ctx context.Context, task *models.BTTask) error {
	remote, _, err := s.findRemote(ctx, *task)
	if err != nil && !errors.Is(err, errBTMissing) {
		return s.failTask(ctx, task, "conflict", err)
	}
	if errors.Is(err, errBTMissing) {
		return s.withTaskCache(ctx, task.ID, func(ctx context.Context, _ *gorm.DB, _ *btCacheFiles) error {
			if err := s.updateTask(ctx, task, map[string]any{"stage": "cancelled", "pending_action": ""}); err != nil {
				return err
			}
			_ = s.cleanupTaskCaches(ctx, *task)
			return nil
		})
	}
	if !btStopped(remote.State) {
		claim, e := s.claimWrite(ctx, task, "cancel_stop")
		if e != nil {
			return e
		}
		remote, _, err = s.findRemote(ctx, *task)
		if err != nil {
			return s.failTask(ctx, task, "uncertain", err)
		}
		if !btStopped(remote.State) {
			if err = s.remote.QBRun(ctx, remote.Hash, false); err != nil {
				return s.failTask(ctx, task, "uncertain", err)
			}
		}
		fresh, _, e := s.findRemote(ctx, *task)
		if e != nil || !btStopped(fresh.State) {
			return s.failTask(ctx, task, "uncertain", ErrBTChanged)
		}
		if err = s.acknowledgeWrite(ctx, task, claim, "cancel_pending", nil); err != nil {
			return err
		}
	}
	claim, err := s.claimWrite(ctx, task, "cancel_delete")
	if err != nil {
		return err
	}
	fresh, _, err := s.findRemote(ctx, *task)
	if err != nil && !errors.Is(err, errBTMissing) {
		return s.failTask(ctx, task, "uncertain", err)
	}
	if err == nil {
		if !btStopped(fresh.State) {
			return s.failTask(ctx, task, "uncertain", ErrBTChanged)
		}
		if err = s.remote.QBDeleteMetadata(ctx, fresh.Hash); err != nil {
			return s.failTask(ctx, task, "uncertain", err)
		}
		if _, _, err = s.findRemote(ctx, *task); !errors.Is(err, errBTMissing) {
			return s.failTask(ctx, task, "uncertain", ErrBTChanged)
		}
	}
	return s.acknowledgeAndCleanup(ctx, task, claim, "cancelled", nil)
}
func btNoForeignFiles(task models.BTTask, files []models.BTFile) error {
	allowed := map[string]int64{}
	for _, file := range files {
		if file.Padding {
			continue
		}
		if file.Selected && file.Progress == 1 {
			allowed[filepath.Clean(filepath.Join(task.CanonicalRoot, filepath.FromSlash(file.RelativePath)))] = file.Size
		}
	}
	if _, err := os.Lstat(task.CanonicalRoot); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := btVerifyRoot(task); err != nil {
		return err
	}
	return filepath.WalkDir(task.CanonicalRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return ErrBTPath
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrBTPath
		}
		if entry.IsDir() {
			return nil
		}
		size, ok := allowed[filepath.Clean(path)]
		if !ok {
			return ErrBTChanged
		}
		info, e := entry.Info()
		if e != nil || !info.Mode().IsRegular() || info.Size() != size {
			return ErrBTChanged
		}
		return nil
	})
}
