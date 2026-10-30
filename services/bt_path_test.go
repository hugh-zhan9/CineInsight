package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func btImportFixture(t *testing.T) (*VideoService, *BTPathPolicy, models.BTTask, models.BTFile, string) {
	t.Helper()
	setupVideoServiceTestDB(t)
	namespace, err := WriteBTNamespaceMarker(t.TempDir(), "instance")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(namespace, "task")
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	connection := models.BTConnection{ID: "instance", Slot: "active", Revision: 1, DownloadRoot: filepath.Dir(namespace)}
	if err = database.DB.Create(&connection).Error; err != nil {
		t.Fatal(err)
	}
	task := models.BTTask{ID: "task", ConnectionID: connection.ID, Revision: 1, AttemptGeneration: 1, IntentKey: "intent", OwnerTag: "owner", SaveRoot: root, CanonicalRoot: root, RootKey: "root", Stage: "active", RemoteState: "uploading"}
	if err = database.DB.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a", 64)
	index := 0
	file := models.BTFile{TaskID: task.ID, ManifestFileID: key, PathKey: &key, RelativePath: "film.mp4", Size: 1, Selected: true, Selectable: true, Progress: 1, EligibilityRevision: 1, ImportState: "pending", QBFileIndex: &index}
	if err = database.DB.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, file.RelativePath)
	mustCreateFile(t, path)
	mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
	if err = database.DB.Create(&models.ScanDirectory{Path: filepath.Dir(namespace)}).Error; err != nil {
		t.Fatal(err)
	}
	policy := NewBTPathPolicy(func(ctx context.Context) (*gorm.DB, error) { return database.DB.WithContext(ctx), nil })
	svc := &VideoService{}
	svc.SetBTPathPolicy(policy)
	return svc, policy, task, file, path
}
func TestBTUnknownNamespaceAndDirectAdd(t *testing.T) {
	setupVideoServiceTestDB(t)
	namespace, err := WriteBTNamespaceMarker(t.TempDir(), "unknown")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(namespace, "orphan", "film.mp4")
	mustCreateFile(t, path)
	svc := &VideoService{}
	if _, err = svc.AddVideo(path); !errors.Is(err, ErrBTPending) {
		t.Fatalf("unknown marker bypass: %v", err)
	}
	result := svc.SyncAffectedDirectories([]models.ScanDirectory{{Path: filepath.Dir(namespace)}}, []string{namespace})
	if len(result.Errors) != 0 || result.Added != 0 || result.SkipBreakdown.BTPending != 1 {
		t.Fatalf("unknown namespace scan: %+v", result)
	}
	before, _ := os.ReadFile(filepath.Join(namespace, BTNamespaceMarker))
	if _, err = WriteBTNamespaceMarker(filepath.Dir(namespace), "other"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(namespace, BTNamespaceMarker))
	if string(before) != string(after) {
		t.Fatal("namespace marker overwritten")
	}
	if _, err = svc.AddVideo(path); !errors.Is(err, ErrBTPending) {
		t.Fatalf("reuse released unknown files: %v", err)
	}
}
func TestBTImportGrantIsPerCallAndPreservesFiles(t *testing.T) {
	svc, policy, task, file, path := btImportFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	verifier := BTImportVerifyFunc(func(ctx context.Context, got models.BTTask, f models.BTFile) error {
		calls++
		if got.ID != task.ID || f.ManifestFileID != file.ManifestFileID {
			t.Fatal("wrong snapshot")
		}
		if _, err := svc.AddVideo(path); !errors.Is(err, ErrBTPending) {
			t.Fatalf("ordinary add leaked grant: %v", err)
		}
		if calls == 2 {
			var video models.Video
			if err := database.DB.Where("path = ?", path).First(&video).Error; err != nil || !video.IsStale {
				t.Fatalf("new row visible before post-check: %+v %v", video, err)
			}
		}
		return nil
	})
	result, err := NewBTImporter(svc, policy, verifier).Import(context.Background(), task.ID, file.ManifestFileID)
	if err != nil || result.State != "imported" || result.VideoID == 0 || calls != 2 {
		t.Fatalf("import %+v %v calls%d", result, err, calls)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("payload changed")
	}
	var video models.Video
	if err = database.DB.First(&video, result.VideoID).Error; err != nil || video.IsStale {
		t.Fatalf("video not released %+v %v", video, err)
	}
	if _, err = svc.AddVideo(path); !errors.Is(err, ErrVideoExists) {
		t.Fatalf("imported identity: %v", err)
	}
	// Crash after AddVideo but before association: repairing reuses the same record.
	database.DB.Model(&models.BTFile{}).Where("task_id = ?", task.ID).Updates(map[string]any{"import_state": "pending", "video_id": nil})
	again, err := NewBTImporter(svc, policy, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil })).Import(context.Background(), task.ID, file.ManifestFileID)
	if err != nil || again.VideoID != result.VideoID {
		t.Fatalf("duplicate after recovery: %+v %v", again, err)
	}
}
func TestBTWithheldNotMissingOrRestored(t *testing.T) {
	for _, narrow := range []bool{false, true} {
		t.Run(map[bool]string{true: "narrow", false: "full"}[narrow], func(t *testing.T) {
			svc, _, task, _, path := btImportFixture(t)
			video := models.Video{Name: "film.mp4", Path: path, Directory: task.SaveRoot, Size: 1, IsStale: true}
			if err := database.DB.Create(&video).Error; err != nil {
				t.Fatal(err)
			}
			scan := func() *ScanSyncResult {
				dirs := []models.ScanDirectory{{Path: filepath.Dir(task.SaveRoot)}}
				if narrow {
					return svc.SyncAffectedDirectories(dirs, []string{task.SaveRoot})
				}
				return svc.SyncScanDirectories(dirs)
			}
			result := scan()
			if result.Added != 0 || result.Restored != 0 || result.Deleted != 0 {
				t.Fatalf("withheld changed: %+v", result)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			result = scan()
			if result.Deleted != 0 {
				t.Fatalf("withheld considered missing: %+v", result)
			}
			var existing models.Video
			if err := database.DB.First(&existing, video.ID).Error; err != nil || !existing.IsStale {
				t.Fatalf("record changed %+v %v", existing, err)
			}
		})
	}
}
func TestBTImportRejectsChangesAndUserDeletion(t *testing.T) {
	t.Run("post_verify_changed", func(t *testing.T) {
		svc, p, task, file, path := btImportFixture(t)
		calls := 0
		verifier := BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error {
			calls++
			if calls == 2 {
				return os.WriteFile(path, []byte("changed"), 0600)
			}
			return nil
		})
		result, err := NewBTImporter(svc, p, verifier).Import(context.Background(), task.ID, file.ManifestFileID)
		if err == nil || result.ErrorCode != "external_changed" {
			t.Fatalf("changed import accepted %+v %v", result, err)
		}
		var video models.Video
		database.DB.Where("path = ?", path).First(&video)
		if !video.IsStale {
			t.Fatal("changed new record remained playable")
		}
	})
	t.Run("user_delete", func(t *testing.T) {
		svc, p, task, file, path := btImportFixture(t)
		video := models.Video{Name: "film.mp4", Path: path, Directory: task.SaveRoot, Size: 1}
		database.DB.Create(&video)
		if err := svc.DeleteVideo(video.ID, false); err != nil {
			t.Fatal(err)
		}
		result, err := NewBTImporter(svc, p, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil })).Import(context.Background(), task.ID, file.ManifestFileID)
		if !errors.Is(err, ErrVideoBlockedByUserDelete) || result.State != "blocked_user_delete" {
			t.Fatalf("user deletion revived %+v %v", result, err)
		}
	})
}
func TestBTPathSymlinkAndDatabaseFailure(t *testing.T) {
	svc, p, task, file, path := btImportFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.mp4")
	mustCreateFile(t, outside)
	os.Remove(path)
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBTImporter(svc, p, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil })).Import(context.Background(), task.ID, file.ManifestFileID); !errors.Is(err, ErrBTPathUnsafe) {
		t.Fatal(err)
	}
	svc.SetBTPathPolicy(NewBTPathPolicy(func(context.Context) (*gorm.DB, error) { return nil, errors.New("offline") }))
	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: task.SaveRoot}})
	if len(result.Errors) == 0 || result.Deleted != 0 {
		t.Fatalf("DB error became empty snapshot: %+v", result)
	}
}

func TestBTNamespaceRefusesExistingUnmarkedContent(t *testing.T) {
	root := t.TempDir()
	namespace := filepath.Join(root, BTNamespaceDirectory)
	existing := filepath.Join(namespace, "user.mp4")
	mustCreateFile(t, existing)
	before, _ := os.ReadFile(existing)
	if _, err := WriteBTNamespaceMarker(root, "new"); !errors.Is(err, ErrBTPathUnsafe) {
		t.Fatalf("occupied directory adopted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(namespace, BTNamespaceMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("guard created over user files")
	}
	after, _ := os.ReadFile(existing)
	if string(before) != string(after) {
		t.Fatal("user bytes changed")
	}
}

func TestBTImportScopeAndCheckingState(t *testing.T) {
	svc, p, task, file, path := btImportFixture(t)
	alias := filepath.Join(t.TempDir(), "library-link")
	root := filepath.Dir(filepath.Dir(task.SaveRoot))
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.ScanDirectory{}).Where("path = ?", root).Update("path", alias).Error; err != nil {
		t.Fatal(err)
	}
	code, err := btImportScope(database.DB, path)
	if err == nil || code != "path_not_in_library" {
		t.Fatalf("canonical alias incorrectly widened lexical library scope %s %v", code, err)
	}
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.Settings{}).Where("id > 0").Update("scan_exclude_paths", filepath.Join(alias, BTNamespaceDirectory)).Error; err != nil {
		t.Fatal(err)
	}
	if code, err := btImportScope(database.DB, path); err == nil || code != "path_not_in_library" {
		t.Fatalf("blacklist alias ignored %s %v", code, err)
	}
	database.DB.Model(&models.Settings{}).Where("id > 0").Update("scan_exclude_paths", "")
	database.DB.Model(&models.BTTask{}).Where("id = ?", task.ID).Update("remote_state", "checkingUP")
	calls := 0
	_, err = NewBTImporter(svc, p, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { calls++; return nil })).Import(context.Background(), task.ID, file.ManifestFileID)
	if !errors.Is(err, ErrBTPending) || calls != 0 {
		t.Fatalf("checking state granted import %v calls=%d", err, calls)
	}
}

func TestBTPendingBlocksRestoreRelocateAndRefresh(t *testing.T) {
	svc, _, task, _, path := btImportFixture(t)
	video := models.Video{Name: "film.mp4", Path: path, Directory: task.SaveRoot, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.RefreshVideoMetadata(video.ID); !errors.Is(err, ErrBTPending) {
		t.Fatalf("refresh bypass %v", err)
	}
	target := filepath.Join(t.TempDir(), "target.mp4")
	mustCreateFile(t, target)
	if err := svc.RelocateVideo(video.ID, target); !errors.Is(err, ErrBTPending) {
		t.Fatalf("relocation bypass %v", err)
	}
	if err := database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Updates(map[string]any{"deleted_by": "scanner", "deleted_at": time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.addScannedVideo(path); !errors.Is(err, ErrBTPending) {
		t.Fatalf("restore bypass %v", err)
	}
	var after models.Video
	if err := database.DB.Unscoped().First(&after, video.ID).Error; err != nil || !after.DeletedAt.IsValid() {
		t.Fatalf("scanner row restored %+v %v", after, err)
	}
}

func TestBTImportPinsDatabaseUntilMaintenance(t *testing.T) {
	svc, p, task, file, _ := btImportFixture(t)
	oldDB := database.DB
	nextDB := dbtest.Open(t)
	entered := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan error, 1)
	calls := 0
	verifier := BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error {
		calls++
		if calls == 1 {
			close(entered)
			<-resume
		}
		return nil
	})
	go func() {
		_, err := NewBTImporter(svc, p, verifier).Import(context.Background(), task.ID, file.ManifestFileID)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("import never reached verifier")
	}
	if libraryPathMutationMu.TryLock() {
		libraryPathMutationMu.Unlock()
		close(resume)
		<-done
		t.Fatal("import acquired DB/verifier before holding library read lease")
	}
	swapped := make(chan struct{})
	go func() {
		releasePaths := BeginLibraryMaintenance()
		releaseDB := database.BeginMaintenance()
		database.DB = nextDB
		releaseDB()
		releasePaths()
		close(swapped)
	}()
	close(resume)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("import/maintenance deadlocked")
	}
	select {
	case <-swapped:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance did not resume")
	}
	defer func() { database.DB = oldDB }()
	var oldCount, newCount int64
	oldDB.Model(&models.Video{}).Count(&oldCount)
	nextDB.Model(&models.Video{}).Count(&newCount)
	if oldCount != 1 || newCount != 0 {
		t.Fatalf("cross-database write: old=%d new=%d", oldCount, newCount)
	}
}

func TestBTImportedQualificationCanBeRevoked(t *testing.T) {
	svc, p, task, file, path := btImportFixture(t)
	result, err := NewBTImporter(svc, p, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil })).Import(context.Background(), task.ID, file.ManifestFileID)
	if err != nil || result.VideoID == 0 {
		t.Fatal(err)
	}
	for _, change := range []map[string]any{{"progress": 0.5}, {"progress": 1.0, "eligibility_revision": 0}} {
		if err := database.DB.Model(&models.BTFile{}).Where("task_id = ?", task.ID).Updates(change).Error; err != nil {
			t.Fatal(err)
		}
		if err := svc.checkBTPath(path); !errors.Is(err, ErrBTPending) {
			t.Fatalf("revoked qualification still allowed: %v", err)
		}
	}
}

func TestBTExplicitReleaseDoesNotReleaseUnknownNeighbors(t *testing.T) {
	svc, p, task, _, path := btImportFixture(t)
	if err := database.DB.Model(&models.BTTask{}).Where("id = ?", task.ID).Update("stage", "released").Error; err != nil {
		t.Fatal(err)
	}
	snap, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if held, err := snap.Withheld(path); err != nil || held {
		t.Fatalf("explicit release remained withheld: %v %v", held, err)
	}
	unknown := filepath.Join(filepath.Dir(task.SaveRoot), "other", "unknown.mp4")
	mustCreateFile(t, unknown)
	if held, err := snap.Withheld(unknown); err != nil || !held {
		t.Fatalf("release leaked to neighbor: %v %v", held, err)
	}
	outside := t.TempDir()
	link := filepath.Join(task.SaveRoot, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if held, err := snap.Withheld(filepath.Join(link, "missing.mp4")); err != nil || !held {
		t.Fatalf("released symlink escape: %v %v", held, err)
	}
	broken := filepath.Join(task.SaveRoot, "broken")
	if err := os.Symlink(filepath.Join(outside, "absent"), broken); err != nil {
		t.Fatal(err)
	}
	if held, err := snap.Withheld(filepath.Join(broken, "missing.mp4")); !held || !errors.Is(err, ErrBTPathUnsafe) {
		t.Fatalf("broken link treated as safe missing: %v %v", held, err)
	}
	video, err := svc.AddVideo(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if held, err := snap.Withheld(path); err != nil || held {
		t.Fatalf("released missing path withheld: %v %v", held, err)
	}
	result := svc.SyncAffectedDirectories([]models.ScanDirectory{{Path: filepath.Dir(task.SaveRoot)}}, []string{task.SaveRoot})
	if result.Stale != 1 || result.Deleted != 0 {
		t.Fatalf("released root did not restore normal narrow missing reconciliation: %+v", result)
	}
	result = svc.SyncScanDirectories([]models.ScanDirectory{{Path: filepath.Dir(task.SaveRoot)}})
	if result.Deleted != 1 {
		t.Fatalf("released root did not restore normal full missing reconciliation: %+v", result)
	}
	var deleted models.Video
	if err := database.DB.Unscoped().First(&deleted, video.ID).Error; err != nil || !deleted.DeletedAt.IsValid() {
		t.Fatalf("released missing video not deleted: %+v %v", deleted, err)
	}
}

func TestBTCancelledAndMissingKeepProtection(t *testing.T) {
	svc, p, task, _, path := btImportFixture(t)
	_ = svc
	for _, stage := range []string{"cancelled", "missing", "uncertain", "abandoned"} {
		if err := database.DB.Model(&models.BTTask{}).Where("id = ?", task.ID).Update("stage", stage).Error; err != nil {
			t.Fatal(err)
		}
		snap, err := p.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if held, err := snap.Withheld(path); err != nil || !held {
			t.Fatalf("%s released protection: %v %v", stage, held, err)
		}
	}
}

func TestBTGrantPutBackStaysIsolatedUntilPostCheck(t *testing.T) {
	svc, p, task, file, path := btImportFixture(t)
	old := models.Video{Name: "film.mp4", Path: path, Directory: task.SaveRoot, Size: 1}
	if err := database.DB.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteVideo(old.ID, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	var entry models.VideoTrashEntry
	if err := database.DB.Where("video_id = ?", old.ID).First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&entry).Updates(map[string]any{"mode": models.TrashModeTrash, "file_moved": true, "file_size": info.Size(), "file_mod_time": info.ModTime().UnixNano(), "file_identity": stableFileIdentity(info), "trash_path": filepath.Join(t.TempDir(), "absent.mp4")}).Error; err != nil {
		t.Fatal(err)
	}
	database.DB.First(&entry, entry.ID)
	if !putBackDetectedFor(videoEntryFacts(entry), info) {
		t.Fatal("fixture must be a valid put-back")
	}
	calls := 0
	newEvents := 0
	svc.SetNewVideoObserver(func(uint) { newEvents++ })
	verifier := BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error {
		calls++
		if calls == 2 {
			var seen models.Video
			if err := database.DB.First(&seen, old.ID).Error; err != nil || !seen.IsStale {
				t.Fatalf("put-back became visible before postcheck: %+v %v", seen, err)
			}
			return errors.New("verification failed")
		}
		return nil
	})
	result, err := NewBTImporter(svc, p, verifier).Import(context.Background(), task.ID, file.ManifestFileID)
	if err == nil || result.ErrorCode != "external_changed" || newEvents != 0 {
		t.Fatalf("failed put-back published %+v %v notifications=%d", result, err, newEvents)
	}
	var after models.Video
	if err := database.DB.First(&after, old.ID).Error; err != nil || !after.IsStale {
		t.Fatalf("failed put-back not isolated %+v %v", after, err)
	}
	var pending models.BTFile
	database.DB.First(&pending, "task_id = ?", task.ID)
	if pending.PublicationState != BTImportPublicationPendingRestore {
		t.Fatalf("restore obligation lost %+v", pending)
	}
	database.DB.Model(&models.BTFile{}).Where("task_id = ?", task.ID).Update("eligibility_revision", 2)
	result, err = NewBTImporter(svc, p, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil })).Import(context.Background(), task.ID, file.ManifestFileID)
	if err != nil || result.VideoID != old.ID || newEvents != 0 {
		t.Fatalf("restore retry triggered new-video rules %+v %v count%d", result, err, newEvents)
	}
	database.DB.First(&pending, "task_id = ?", task.ID)
	if pending.PublicationState != BTImportPublicationDone {
		t.Fatalf("restore receipt missing %+v", pending)
	}

}
func TestBTPublicRestoreRequiresQualification(t *testing.T) {
	svc, _, task, _, path := btImportFixture(t)
	old := models.Video{Name: "film.mp4", Path: path, Directory: task.SaveRoot, Size: 1}
	database.DB.Create(&old)
	if err := svc.DeleteVideo(old.ID, false); err != nil {
		t.Fatal(err)
	}
	var entry models.VideoTrashEntry
	database.DB.Where("video_id = ?", old.ID).First(&entry)
	if _, err := svc.RestoreTrashEntry(entry.ID); !errors.Is(err, ErrBTPending) {
		t.Fatalf("unqualified public restore allowed: %v", err)
	}
	info, _ := os.Stat(path)
	if err := database.DB.Model(&models.BTFile{}).Where("task_id = ?", task.ID).Updates(map[string]any{"import_state": "imported", "video_id": old.ID, "observed_size": info.Size(), "observed_mtime": info.ModTime().UnixNano()}).Error; err != nil {
		t.Fatal(err)
	}
	restored, err := svc.RestoreTrashEntry(entry.ID)
	if err != nil || restored.ID != old.ID || restored.IsStale {
		t.Fatalf("qualified public restore rejected %+v %v", restored, err)
	}
}
func TestBTImportRejectsAliasIntoExcludedTarget(t *testing.T) {
	svc, p, task, file, path := btImportFixture(t)
	private := filepath.Join(task.SaveRoot, "private")
	target := filepath.Join(private, "film.mp4")
	mustCreateFile(t, target)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&models.Settings{}).Where("id > 0").Update("scan_exclude_paths", private).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	result, err := NewBTImporter(svc, p, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { calls++; return nil })).Import(context.Background(), task.ID, file.ManifestFileID)
	if err == nil || result.ErrorCode != "path_not_in_library" || calls != 0 {
		t.Fatalf("excluded real target imported %+v %v", result, err)
	}
}

func TestBTPublicationRecoversFirstCreateWithoutMemory(t *testing.T) {
	svc, p, task, file, path := btImportFixture(t)
	info, _ := os.Stat(path)
	real, _ := filepath.EvalSymlinks(path)
	created := 0
	svc.SetNewVideoObserver(func(uint) { created++ })
	grant := &btImportGrant{path: path, real: real, size: info.Size(), mtime: info.ModTime().UnixNano(), taskID: task.ID, fileID: file.ManifestFileID, taskRevision: task.Revision, attemptGeneration: task.AttemptGeneration, eligibilityRevision: file.EligibilityRevision}
	unlock, err := rLockLibraryPaths()
	if err != nil {
		t.Fatal(err)
	}
	video, _, err := svc.addVideoWithBTGrant(path, grant)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	var pending models.BTFile
	database.DB.First(&pending, "task_id = ? AND manifest_file_id = ?", task.ID, file.ManifestFileID)
	if created != 0 || pending.VideoID == nil || *pending.VideoID != video.ID || pending.PublicationState != BTImportPublicationPendingNew || !video.IsStale {
		t.Fatalf("creation was not durable and isolated: %+v %+v count%d", pending, video, created)
	}
	restarted := &VideoService{}
	restarted.SetBTPathPolicy(p)
	restarted.SetNewVideoObserver(func(uint) { created++ })
	importer := NewBTImporter(restarted, p, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil }))
	result, err := importer.Import(context.Background(), task.ID, file.ManifestFileID)
	if err != nil || result.VideoID != video.ID || created != 1 {
		t.Fatalf("restart lost first publication: %+v %v count%d", result, err, created)
	}
	if _, err = importer.Import(context.Background(), task.ID, file.ManifestFileID); err != nil || created != 1 {
		t.Fatalf("normal repeat published twice: %v count%d", err, created)
	}
	database.DB.First(&pending, "task_id = ? AND manifest_file_id = ?", task.ID, file.ManifestFileID)
	if pending.PublicationState != BTImportPublicationDone {
		t.Fatalf("receipt missing %+v", pending)
	}
}
func TestBTPublicationAfterFailedPostVerify(t *testing.T) {
	svc, p, task, file, _ := btImportFixture(t)
	events := 0
	svc.SetNewVideoObserver(func(uint) { events++ })
	calls := 0
	failed := BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error {
		calls++
		if calls == 2 {
			return errors.New("postverify unavailable")
		}
		return nil
	})
	if _, err := NewBTImporter(svc, p, failed).Import(context.Background(), task.ID, file.ManifestFileID); err == nil {
		t.Fatal("expected postverify failure")
	}
	var pending models.BTFile
	database.DB.First(&pending, "task_id = ? AND manifest_file_id = ?", task.ID, file.ManifestFileID)
	if events != 0 || pending.PublicationState != BTImportPublicationPendingNew || pending.VideoID == nil {
		t.Fatalf("failed first creation lost pending publication: %+v count%d", pending, events)
	}
	if err := database.DB.Model(&models.BTFile{}).Where("task_id = ?", task.ID).Update("eligibility_revision", 2).Error; err != nil {
		t.Fatal(err)
	}
	importer := NewBTImporter(svc, p, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil }))
	if _, err := importer.Import(context.Background(), task.ID, file.ManifestFileID); err != nil || events != 1 {
		t.Fatalf("retry did not publish new row once: %v count%d", err, events)
	}
}
func TestBTExistingVideoAssociationDoesNotPublishNew(t *testing.T) {
	svc, p, task, file, path := btImportFixture(t)
	video := models.Video{Name: "film.mp4", Path: path, Directory: task.SaveRoot, Size: 1}
	database.DB.Create(&video)
	events := 0
	svc.SetNewVideoObserver(func(uint) { events++ })
	importer := NewBTImporter(svc, p, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil }))
	for n := 0; n < 2; n++ {
		result, err := importer.Import(context.Background(), task.ID, file.ManifestFileID)
		if err != nil || result.VideoID != video.ID {
			t.Fatalf("existing association failed %+v %v", result, err)
		}
	}
	if events != 0 {
		t.Fatalf("preexisting row published as new: %d", events)
	}
}
func TestBTImportFailurePersistenceIsScoped(t *testing.T) {
	t.Run("scope", func(t *testing.T) {
		svc, p, task, file, _ := btImportFixture(t)
		database.DB.Where("id > 0").Delete(&models.ScanDirectory{})
		result, err := NewBTImporter(svc, p, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil })).Import(context.Background(), task.ID, file.ManifestFileID)
		if err == nil || result.ErrorCode != "path_not_in_library" {
			t.Fatalf("scope failure missing %+v %v", result, err)
		}
		var current models.BTFile
		database.DB.First(&current, "task_id = ?", task.ID)
		if current.ImportState != "failed" || current.ErrorCode != "path_not_in_library" {
			t.Fatalf("failure not persisted %+v", current)
		}
	})
	t.Run("new_generation", func(t *testing.T) {
		svc, p, task, file, _ := btImportFixture(t)
		verifier := BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error {
			database.DB.Model(&models.BTTask{}).Where("id = ?", task.ID).Update("revision", 2)
			database.DB.Model(&models.BTFile{}).Where("task_id = ?", task.ID).Update("error_code", "new_generation")
			return errors.New("late failure")
		})
		NewBTImporter(svc, p, verifier).Import(context.Background(), task.ID, file.ManifestFileID)
		var current models.BTFile
		database.DB.First(&current, "task_id = ?", task.ID)
		if current.ErrorCode != "new_generation" || current.ImportState != "pending" {
			t.Fatalf("late failure overwrote current generation %+v", current)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		svc, p, task, file, _ := btImportFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		verifier := BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { cancel(); return ctx.Err() })
		NewBTImporter(svc, p, verifier).Import(ctx, task.ID, file.ManifestFileID)
		var current models.BTFile
		database.DB.First(&current, "task_id = ?", task.ID)
		if current.ErrorCode != "" || current.ImportState != "pending" {
			t.Fatalf("cancel became business failure %+v", current)
		}
	})
}

func TestBTPublicationReplaysAfterCommittedAssociation(t *testing.T) {
	svc, p, task, file, _ := btImportFixture(t)
	svc.SetNewVideoObserver(func(uint) { panic("simulated process exit before callback work") })
	interrupted := false
	func() {
		defer func() {
			if recover() != nil {
				interrupted = true
			}
		}()
		NewBTImporter(svc, p, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil })).Import(context.Background(), task.ID, file.ManifestFileID)
	}()
	if !interrupted {
		t.Fatal("fixture did not interrupt publication")
	}
	var pending models.BTFile
	database.DB.First(&pending, "task_id = ?", task.ID)
	if pending.ImportState != "imported" || pending.PublicationState != BTImportPublicationPendingNew {
		t.Fatalf("committed import lost unacknowledged publication %+v", pending)
	}
	restarted := &VideoService{}
	restarted.SetBTPathPolicy(p)
	events := 0
	restarted.SetNewVideoObserver(func(uint) { events++ })
	if _, err := NewBTImporter(restarted, p, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil })).Import(context.Background(), task.ID, file.ManifestFileID); err != nil || events != 1 {
		t.Fatalf("committed pending event not replayed %v count%d", err, events)
	}
}

func TestBTGrantRestoresScannerDeletedIdentity(t *testing.T) {
	for _, entryPresent := range []bool{false, true} {
		t.Run(map[bool]string{false: "without_entry", true: "missing_entry"}[entryPresent], func(t *testing.T) {
			svc, p, task, file, path := btImportFixture(t)
			video := models.Video{Name: "film.mp4", Path: path, Directory: task.SaveRoot, Size: 1, DeletedBy: "scanner"}
			if err := database.DB.Create(&video).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.DB.Delete(&video).Error; err != nil {
				t.Fatal(err)
			}
			if entryPresent {
				entry := models.VideoTrashEntry{VideoID: video.ID, OriginalPath: path, VideoName: video.Name, DeletedBy: "scanner", Mode: models.TrashModeMissing, State: trashStateDeleted}
				if err := database.DB.Create(&entry).Error; err != nil {
					t.Fatal(err)
				}
			}
			calls, events := 0, 0
			svc.SetNewVideoObserver(func(uint) { events++ })
			verifier := BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error {
				calls++
				if calls == 2 {
					var pending models.Video
					if err := database.DB.First(&pending, video.ID).Error; err != nil || !pending.IsStale {
						t.Fatalf("scanner restore visible before verification %+v %v", pending, err)
					}
					return errors.New("postverify failure")
				}
				return nil
			})
			if _, err := NewBTImporter(svc, p, verifier).Import(context.Background(), task.ID, file.ManifestFileID); err == nil {
				t.Fatal("expected failure after restore")
			}
			var pending models.BTFile
			database.DB.First(&pending, "task_id = ?", task.ID)
			if pending.VideoID == nil || *pending.VideoID != video.ID || pending.PublicationState != BTImportPublicationPendingRestore {
				t.Fatalf("scanner identity/publication lost %+v", pending)
			}
			database.DB.Model(&models.BTFile{}).Where("task_id = ?", task.ID).Update("eligibility_revision", 2)
			result, err := NewBTImporter(svc, p, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil })).Import(context.Background(), task.ID, file.ManifestFileID)
			if err != nil || result.VideoID != video.ID || events != 0 {
				t.Fatalf("scanner retry changed identity/rules %+v %v count%d", result, err, events)
			}
			var restored models.Video
			if err := database.DB.First(&restored, video.ID).Error; err != nil || restored.IsStale {
				t.Fatalf("scanner restore not published %+v %v", restored, err)
			}
		})
	}
}

func TestBTGrantScannerRestoreRejectsChangedIdentity(t *testing.T) {
	svc, p, task, file, path := btImportFixture(t)
	old := models.Video{Name: "film.mp4", Path: path, Directory: task.SaveRoot, Size: 2, DeletedBy: "scanner"}
	database.DB.Create(&old)
	database.DB.Delete(&old)
	entry := models.VideoTrashEntry{VideoID: old.ID, OriginalPath: path, VideoName: old.Name, DeletedBy: "scanner", Mode: models.TrashModeMissing, State: trashStateDeleted}
	database.DB.Create(&entry)
	if _, err := NewBTImporter(svc, p, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil })).Import(context.Background(), task.ID, file.ManifestFileID); err == nil {
		t.Fatal("changed scanner identity restored")
	}
	var current models.Video
	database.DB.Unscoped().First(&current, old.ID)
	if !current.DeletedAt.IsValid() {
		t.Fatal("mismatched historical record made active")
	}
}

func TestBTExplicitUndoAfterWorkerRecordsUserDeletion(t *testing.T) {
	svc, p, task, file, _ := btImportFixture(t)
	events := 0
	svc.SetNewVideoObserver(func(uint) { events++ })
	importer := NewBTImporter(svc, p, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil }))
	first, err := importer.Import(context.Background(), task.ID, file.ManifestFileID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteVideo(first.VideoID, false); err != nil {
		t.Fatal(err)
	}
	blocked, err := importer.Import(context.Background(), task.ID, file.ManifestFileID)
	if !errors.Is(err, ErrVideoBlockedByUserDelete) || blocked.State != "blocked_user_delete" {
		t.Fatalf("worker did not retain user deletion %+v %v", blocked, err)
	}
	var entry models.VideoTrashEntry
	database.DB.Where("video_id = ?", first.VideoID).First(&entry)
	if _, _, err := svc.addScannedVideo(entry.OriginalPath); !errors.Is(err, ErrBTPending) {
		t.Fatalf("automatic restore escaped blocked state %v", err)
	}
	restored, err := svc.RestoreTrashEntry(entry.ID)
	if err != nil || restored.ID != first.VideoID || restored.IsStale {
		t.Fatalf("explicit undo rejected %+v %v", restored, err)
	}
	var current models.BTFile
	database.DB.First(&current, "task_id = ?", task.ID)
	if current.ImportState != "imported" || current.PublicationState != BTImportPublicationDone || current.ErrorCode != "" {
		t.Fatalf("explicit undo did not repair import projection %+v", current)
	}
	if events != 1 {
		t.Fatalf("explicit undo triggered new-video rules %d", events)
	}
}

func TestBTImportLatePostcheckCannotRevokeNewGeneration(t *testing.T) {
	for _, column := range []string{"revision", "attempt_generation"} {
		for _, postFails := range []bool{false, true} {
			t.Run(column+map[bool]string{false: "/unchanged", true: "/verifier"}[postFails], func(t *testing.T) {
				svc, policy, task, file, _ := btImportFixture(t)
				calls := 0
				var published BTImportResult
				verifier := BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error {
					calls++
					if calls != 2 {
						return nil
					}
					if err := database.DB.Model(&models.BTTask{}).Where("id = ?", task.ID).UpdateColumn(column, 2).Error; err != nil {
						t.Fatal(err)
					}
					var err error
					published, err = NewBTImporter(svc, policy, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil })).Import(context.Background(), task.ID, file.ManifestFileID)
					if err != nil {
						t.Fatalf("new generation publish: %v", err)
					}
					if postFails {
						return errors.New("old remote snapshot failed")
					}
					return nil
				})
				if _, err := NewBTImporter(svc, policy, verifier).Import(context.Background(), task.ID, file.ManifestFileID); err == nil {
					t.Fatal("old generation succeeded")
				}
				var current models.BTFile
				if err := database.DB.First(&current, "task_id = ?", task.ID).Error; err != nil {
					t.Fatal(err)
				}
				if current.EligibilityRevision != file.EligibilityRevision || current.ImportState != "imported" || current.ErrorCode != "" || current.PublicationState != BTImportPublicationDone {
					t.Errorf("old failure revoked new qualification %+v", current)
				}
				var video models.Video
				if err := database.DB.First(&video, published.VideoID).Error; err != nil || video.IsStale {
					t.Fatalf("old failure hid new published video %+v %v", video, err)
				}
			})
		}
	}
}

func TestBTImportLateUserDeleteCannotOverwriteNewGeneration(t *testing.T) {
	svc, policy, task, file, _ := btImportFixture(t)
	verify := BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil })
	importer := NewBTImporter(svc, policy, verify)
	initial, err := importer.Import(context.Background(), task.ID, file.ManifestFileID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteVideo(initial.VideoID, false); err != nil {
		t.Fatal(err)
	}
	advanced := false
	callback := "test:bt_late_user_delete"
	if err := database.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if advanced || tx.Statement.Table != "videos" {
			return
		}
		advanced = true
		if err := database.DB.Model(&models.BTTask{}).Where("id = ?", task.ID).Updates(map[string]any{"revision": 2, "attempt_generation": 2}).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.DB.Model(&models.BTFile{}).Where("task_id = ?", task.ID).Updates(map[string]any{"import_state": "pending", "error_code": "new_generation"}).Error; err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer database.DB.Callback().Query().Remove(callback)
	if _, err := importer.Import(context.Background(), task.ID, file.ManifestFileID); !errors.Is(err, ErrVideoBlockedByUserDelete) {
		t.Fatalf("expected old user-delete result: %v", err)
	}
	var current models.BTFile
	if err := database.DB.First(&current, "task_id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !advanced || current.ImportState != "pending" || current.ErrorCode != "new_generation" || current.EligibilityRevision != file.EligibilityRevision {
		t.Fatalf("old user-delete overwrote new generation %+v", current)
	}
}

func TestBTNestedTaskOwnershipBeatsReleasedAncestor(t *testing.T) {
	svc, policy, outer, _, _ := btImportFixture(t)
	if err := database.DB.Model(&models.BTTask{}).Where("id = ?", outer.ID).Update("stage", "released").Error; err != nil {
		t.Fatal(err)
	}
	namespace, err := WriteBTNamespaceMarker(outer.SaveRoot, "nested")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(namespace, "child")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "nested.mp4")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	child := models.BTTask{ID: "child", ConnectionID: outer.ConnectionID, Revision: 1, AttemptGeneration: 1, IntentKey: "child", OwnerTag: "child", SaveRoot: root, CanonicalRoot: root, RootKey: "child", Stage: "active", RemoteState: "uploading"}
	if err := database.DB.Create(&child).Error; err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		snap, err := policy.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, reverse := range []bool{false, true} {
			if reverse {
				for a, b := 0, len(snap.tasks)-1; a < b; a, b = a+1, b-1 {
					snap.tasks[a], snap.tasks[b] = snap.tasks[b], snap.tasks[a]
				}
			}
			if held, err := snap.Withheld(path); err != nil || held != want {
				t.Fatalf("nested ownership reverse=%v held=%v want=%v err=%v", reverse, held, want, err)
			}
		}
	}
	check(true)
	if _, err := svc.AddVideo(path); !errors.Is(err, ErrBTPending) {
		t.Fatalf("nested pending add escaped: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "nested.mp4", Path: path, Directory: root, Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("b", 64)
	file := models.BTFile{TaskID: child.ID, ManifestFileID: key, PathKey: &key, RelativePath: "nested.mp4", Size: 1, Selected: true, Selectable: true, Progress: 1, EligibilityRevision: 1, ImportState: "imported", VideoID: &video.ID, ObservedSize: 1, ObservedMtime: info.ModTime().UnixNano()}
	if err := database.DB.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	check(false)
	if err := database.DB.Model(&file).Update("observed_mtime", info.ModTime().UnixNano()-1).Error; err != nil {
		t.Fatal(err)
	}
	check(true)
}

func TestBTReleasedAncestorDoesNotReleaseUnknownNestedNamespace(t *testing.T) {
	_, policy, outer, _, normal := btImportFixture(t)
	if err := database.DB.Model(&models.BTTask{}).Where("id = ?", outer.ID).Update("stage", "released").Error; err != nil {
		t.Fatal(err)
	}
	snap, err := policy.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Cache the outer marker first: it must not be mistaken for a nested marker.
	if marked, err := snap.hasMarker(normal); err != nil || !marked {
		t.Fatalf("outer marker %v %v", marked, err)
	}
	if held, err := snap.Withheld(normal); err != nil || held {
		t.Fatalf("normal released file blocked %v %v", held, err)
	}
	namespace, err := WriteBTNamespaceMarker(outer.SaveRoot, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(namespace, "unknown-task")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "pending.mp4")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if held, err := snap.Withheld(path); err != nil || !held {
		t.Fatalf("unknown nested namespace escaped %v %v", held, err)
	}
	alias := filepath.Join(outer.SaveRoot, "alias.mp4")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if held, err := snap.Withheld(alias); err != nil || !held {
		t.Fatalf("unknown nested real namespace escaped %v %v", held, err)
	}
}

func TestBTReleasedShortAliasCannotOverrideRealPendingOwner(t *testing.T) {
	svc, policy, outer, _, normal := btImportFixture(t)
	if err := database.DB.Model(&models.BTTask{}).Where("id = ?", outer.ID).Update("stage", "released").Error; err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(outer.SaveRoot, "deep", BTNamespaceDirectory, "child")
	path := filepath.Join(root, "sub", "dir", "video.mp4")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	// No marker survives, but the local child ownership row still protects it.
	child := models.BTTask{ID: "child", ConnectionID: outer.ConnectionID, Revision: 1, AttemptGeneration: 1, IntentKey: "child", OwnerTag: "child", SaveRoot: root, CanonicalRoot: root, RootKey: "child", Stage: "active", RemoteState: "downloading"}
	if err := database.DB.Create(&child).Error; err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(outer.SaveRoot, "alias.mp4")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	reverse := filepath.Join(root, "escape.mp4")
	if err := os.Symlink(normal, reverse); err != nil {
		t.Fatal(err)
	}
	snap, err := policy.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, order := range []string{"original", "reversed"} {
		if order == "reversed" {
			for a, b := 0, len(snap.tasks)-1; a < b; a, b = a+1, b-1 {
				snap.tasks[a], snap.tasks[b] = snap.tasks[b], snap.tasks[a]
			}
		}
		for _, p := range []string{alias, reverse, path} {
			if held, err := snap.Withheld(p); err != nil || !held {
				t.Errorf("%s pending ownership escaped %s held=%v err=%v", order, filepath.Base(p), held, err)
			}
		}
	}
	if _, err := svc.AddVideo(alias); !errors.Is(err, ErrBTPending) {
		t.Fatalf("short alias direct Add escaped: %v", err)
	}
}
