package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestBTRestoreMarkerOwnedAndScopeIsolation(t *testing.T) {
	s, _ := newBTTaskTest(t)
	ctx := context.Background()
	dbA := database.DB
	fence := NewBTRestoreFence(s.dataDir)
	ticket, err := fence.Prepare(ctx, dbA)
	if err != nil || !ticket.Created {
		t.Fatal(ticket, err)
	}
	again, err := fence.Prepare(ctx, dbA)
	if err != nil || again.Created || again.Nonce != ticket.Nonce {
		t.Fatal("existing pending marker replaced", err)
	}
	if err = fence.ClearOwned(again); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(fence.markerPath(ticket.Scope)); err != nil {
		t.Fatal("old pending marker cleared")
	}
	dbB := dbtest.Open(t)
	other, err := fence.Prepare(ctx, dbB)
	if err != nil || other.Scope == ticket.Scope {
		t.Fatal("different physical DB reused marker", err)
	}
	if err = fence.ClearOwned(other); err != nil {
		t.Fatal(err)
	}
	if _, err = fence.read(ticket.Scope); err != nil {
		t.Fatal("other scope cleared current marker")
	}
	if err = fence.ClearOwned(ticket); err != nil {
		t.Fatal(err)
	}
}
func TestBTRestoreConsumeFreezesBeforeAdmissionAndKeepsReleased(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	view := btSubmitFirst(t, s)
	s.Poll(ctx)
	var task models.BTTask
	database.DB.First(&task, "id = ?", view.ID)
	before := task.Revision
	fence := NewBTRestoreFence(s.dataDir)
	ticket, err := fence.Prepare(ctx, database.DB)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.InitializeLocal(ctx, fence); err != nil {
		t.Fatal(err)
	}
	current, _, _ := btLoadTask(ctx, view.ID)
	if current.Stage != "uncertain" || !current.UnresolvedWrite || current.PendingAction != "restore" || current.Revision <= before {
		t.Fatal("restored active intent not frozen", current.Stage)
	}
	if _, err = fence.read(ticket.Scope); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("completed marker retained", err)
	}
	count := len(r.actions)
	if err = s.Poll(ctx); !errors.Is(err, ErrBTMaintenance) {
		t.Fatal("bootstrap opened external admission", err)
	}
	s.Resume()
	s.Poll(ctx)
	if len(r.actions) != count {
		t.Fatal("restore replayed external command")
	}
	database.DB.Model(&models.BTTask{}).Where("id = ?", view.ID).Updates(map[string]any{"stage": "released", "pending_action": "", "unresolved_write": false})
	if _, err = fence.Prepare(ctx, database.DB); err != nil {
		t.Fatal(err)
	}
	if err = s.InitializeLocal(ctx, fence); err != nil {
		t.Fatal(err)
	}
	current, _, _ = btLoadTask(ctx, view.ID)
	if current.Stage != "released" || current.UnresolvedWrite {
		t.Fatal("released intent reopened")
	}
}
func TestBTRestorePartialFailureKeepsMarker(t *testing.T) {
	s, _ := newBTTaskTest(t)
	ctx := context.Background()
	view := btPreviewTest(t, s)
	fence := NewBTRestoreFence(s.dataDir)
	ticket, err := fence.Prepare(ctx, database.DB)
	if err != nil {
		t.Fatal(err)
	}
	var second models.BTTask
	if err := database.DB.First(&second, "id = ?", view.ID).Error; err != nil {
		t.Fatal(err)
	}
	second.ID = uuid.NewString()
	second.IntentKey = second.ID
	second.OwnerTag = second.ID
	second.RootKey = strings.Repeat("b", 64)
	second.InfoHashV1 = nil
	second.InfoHashV2 = nil
	if err := database.DB.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	updates := 0
	name := "test:bt_restore_reject"
	database.DB.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "bt_tasks" {
			updates++
			if updates < 2 {
				return
			}
			tx.AddError(errors.New("fixture failure"))
		}
	})
	if err = fence.Consume(ctx, s); err == nil {
		t.Fatal("failed quarantine accepted")
	}
	var frozen int64
	database.DB.Model(&models.BTTask{}).Where("unresolved_write = ?", true).Count(&frozen)
	if frozen != 1 {
		t.Fatal("fixture did not interrupt a partial pass", frozen)
	}
	database.DB.Callback().Update().Remove(name)
	if _, err = fence.read(ticket.Scope); err != nil {
		t.Fatal("partial marker lost")
	}
	if err = fence.Consume(ctx, s); err != nil {
		t.Fatal(err)
	}
	task, _, _ := btLoadTask(ctx, view.ID)
	if !task.UnresolvedWrite {
		t.Fatal("retry did not freeze task")
	}
}
func TestBTPlannedNamespaceUsesAuthorizedChildNotParent(t *testing.T) {
	_, _ = newBTTaskTest(t)
	ctx := context.Background()
	root := t.TempDir()
	namespace := filepath.Join(root, BTNamespaceDirectory)
	database.DB.Create(&models.Settings{})
	database.DB.Create(&models.ScanDirectory{Path: namespace})
	if err := ValidateBTDownloadRoot(ctx, root); err != nil {
		t.Fatal("authorized planned namespace rejected", err)
	}
	database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", namespace)
	if err := ValidateBTDownloadRoot(ctx, root); !errors.Is(err, ErrBTPathNotInLibrary) {
		t.Fatal("blacklisted namespace allowed", err)
	}
	database.DB.Model(&models.Settings{}).Where("1 = 1").Update("scan_exclude_paths", "")
	database.DB.Model(&models.ScanDirectory{}).Where("1 = 1").Update("path", root)
	outside := t.TempDir()
	if err := os.Symlink(outside, namespace); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBTDownloadRoot(ctx, root); !errors.Is(err, ErrBTPathNotInLibrary) {
		t.Fatal("physical scope escape allowed", err)
	}
}
func TestBTDuplicateWorkerPersistsOnlyMatchingImportedSource(t *testing.T) {
	svc, policy, task, file, path := btImportFixture(t)
	ctx := context.Background()
	importer := NewBTImporter(svc, policy, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil }))
	result, err := importer.Import(ctx, task.ID, file.ManifestFileID)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	worker := NewBTDuplicateService(func(ctx context.Context, p string, id uint) (DownloadDuplicateResult, error) {
		calls++
		info, _ := os.Stat(p)
		return DownloadDuplicateResult{Status: DownloadDuplicateNone, VideoID: id, SourceSize: info.Size(), SourceModTimeNS: info.ModTime().UnixNano(), CheckedAt: time.Now()}, nil
	}, nil)
	if found, err := worker.checkNext(ctx); err != nil || !found {
		t.Fatal(err)
	}
	database.DB.First(&file, "task_id = ? AND manifest_file_id = ?", task.ID, file.ManifestFileID)
	if file.ImportState != "imported" || file.VideoID == nil || *file.VideoID != result.VideoID || file.DuplicateResult == "" || calls != 1 {
		t.Fatal("duplicate changed import or lost result")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "x" {
		t.Fatal("duplicate changed payload")
	}
	worker.cursorTask = ""
	worker.cursorFile = ""
	worker.checkNext(ctx)
	if calls != 1 {
		t.Fatal("checked completed result twice")
	}
}

func TestBTDuplicatePauseCancelsAndWaitsWithoutChangingImport(t *testing.T) {
	svc, policy, task, file, _ := btImportFixture(t)
	importer := NewBTImporter(svc, policy, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil }))
	if _, err := importer.Import(context.Background(), task.ID, file.ManifestFileID); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	exited := make(chan struct{})
	worker := NewBTDuplicateService(func(ctx context.Context, _ string, _ uint) (DownloadDuplicateResult, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return DownloadDuplicateResult{}, ctx.Err()
	}, nil)
	worker.Resume()
	worker.Start(context.Background())
	defer worker.Stop(context.Background())
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("checker did not start")
	}
	pause, cancelPause := context.WithTimeout(context.Background(), time.Second)
	defer cancelPause()
	if err := worker.Quiesce(pause); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("quiesce returned before checker ended")
	}
	database.DB.First(&file, "task_id = ? AND manifest_file_id = ?", task.ID, file.ManifestFileID)
	if file.ImportState != "imported" || file.DuplicateResult != "" {
		t.Fatal("cancellation changed import or published failure")
	}
}
func TestBTDuplicateFailureAndDeletedSourceProjection(t *testing.T) {
	svc, policy, task, file, _ := btImportFixture(t)
	importer := NewBTImporter(svc, policy, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil }))
	if _, err := importer.Import(context.Background(), task.ID, file.ManifestFileID); err != nil {
		t.Fatal(err)
	}
	worker := NewBTDuplicateService(func(context.Context, string, uint) (DownloadDuplicateResult, error) {
		return DownloadDuplicateResult{}, errors.New("private /path/secret")
	}, nil)
	if _, err := worker.checkNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	database.DB.First(&file, "task_id = ? AND manifest_file_id = ?", task.ID, file.ManifestFileID)
	if file.ImportState != "imported" || file.DuplicateResult == "" {
		t.Fatal("failure lost import or result")
	}
	view, err := btVisibleView(database.DB, task, []models.BTFile{file})
	if err != nil || view.Files[0].Duplicate == nil || view.Files[0].Duplicate.Status != DownloadDuplicateFailed {
		t.Fatal("failed result missing", err)
	}
	if strings.Contains(view.Files[0].Duplicate.Error, "secret") {
		t.Fatal("raw checker error exposed")
	}
	database.DB.Delete(&models.Video{}, *file.VideoID)
	view, err = btVisibleView(database.DB, task, []models.BTFile{file})
	if err != nil || view.Files[0].Duplicate != nil || view.Files[0].VideoID != nil {
		t.Fatal("deleted source kept playable duplicate", err)
	}
}
func TestBTTaskSummaryIsBoundedAndSeparatesImportCompletion(t *testing.T) {
	s, _ := newBTTaskTest(t)
	view := btSubmitFirst(t, s)
	database.DB.Model(&models.BTTask{}).Where("id = ?", view.ID).Updates(map[string]any{"stage": "active", "remote_state": "uploading"})
	database.DB.Model(&models.BTFile{}).Where("task_id = ?", view.ID).Update("progress", 1)
	rows, err := s.RecentSummaries(context.Background())
	if err != nil || len(rows) != 1 || rows[0].Status != "importing" {
		t.Fatal(rows, err)
	}
	database.DB.Model(&models.BTFile{}).Where("task_id = ?", view.ID).Update("import_state", "imported")
	rows, err = s.RecentSummaries(context.Background())
	if err != nil || rows[0].Status != "done" {
		t.Fatal(rows, err)
	}
	database.DB.Model(&models.BTFile{}).Where("task_id = ?", view.ID).Updates(map[string]any{"import_state": "failed", "error_code": "path_not_in_library"})
	rows, err = s.RecentSummaries(context.Background())
	if err != nil || rows[0].Status != "failed" || rows[0].Message == "" {
		t.Fatal(rows, err)
	}
}

func TestBTDuplicateSourceChangedDuringCheckDoesNotPublish(t *testing.T) {
	svc, policy, task, file, path := btImportFixture(t)
	importer := NewBTImporter(svc, policy, BTImportVerifyFunc(func(context.Context, models.BTTask, models.BTFile) error { return nil }))
	if _, err := importer.Import(context.Background(), task.ID, file.ManifestFileID); err != nil {
		t.Fatal(err)
	}
	worker := NewBTDuplicateService(func(ctx context.Context, p string, id uint) (DownloadDuplicateResult, error) {
		before, _ := os.Stat(p)
		if err := os.WriteFile(p, []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
		return DownloadDuplicateResult{Status: DownloadDuplicateNone, VideoID: id, SourceSize: before.Size(), SourceModTimeNS: before.ModTime().UnixNano(), CheckedAt: time.Now()}, nil
	}, nil)
	if _, err := worker.checkNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	database.DB.First(&file, "task_id = ? AND manifest_file_id = ?", task.ID, file.ManifestFileID)
	if file.DuplicateResult != "" || file.ImportState != "imported" {
		t.Fatal("stale duplicate result persisted or import overwritten")
	}
	if raw, _ := os.ReadFile(path); string(raw) != "changed" {
		t.Fatal("worker rewrote changed file")
	}
}

func TestBTRestoreFenceExcludedFromBackupAndOwnerOnlyPermissions(t *testing.T) {
	s, _ := newBTTaskTest(t)
	f := NewBTRestoreFence(s.dataDir)
	ticket, err := f.Prepare(context.Background(), database.DB)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f.markerPath(ticket.Scope))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("marker permission", err)
	}
	for _, root := range backupFilesDirectories {
		if root == "bt-recovery" || strings.HasPrefix("bt-recovery", root+"/") {
			t.Fatal("restore fence enters backup")
		}
	}
}

func TestBTRestoreExistingMarkerMustBeDurableOnRetry(t *testing.T) {
	for _, failParent := range []bool{false, true} {
		t.Run(fmt.Sprint(failParent), func(t *testing.T) {
			s, _ := newBTTaskTest(t)
			f := NewBTRestoreFence(s.dataDir)
			fail := true
			calls := 0
			f.syncDirectory = func(path string) error {
				calls++
				if fail && ((path == s.dataDir) == failParent) {
					return errors.New("sync failed")
				}
				return btSyncDirectory(path)
			}
			if _, err := f.Prepare(context.Background(), database.DB); err == nil {
				t.Fatal("first failed sync accepted")
			}
			scope, err := BTCredentialScope(context.Background(), database.DB)
			if err != nil {
				t.Fatal(err)
			}
			marker, err := f.read(scope)
			if err != nil {
				t.Fatal("fixture did not publish before sync failure", err)
			}
			before := calls
			if _, err = f.Prepare(context.Background(), database.DB); err == nil {
				t.Fatal("existing unsynced marker bypassed durability on retry")
			}
			if calls == before {
				t.Fatal("retry did not attempt sync")
			}
			fail = false
			before = calls
			ticket, err := f.Prepare(context.Background(), database.DB)
			if err != nil || ticket.Created || ticket.Nonce != marker.Nonce || calls-before != 2 {
				t.Fatal("retry did not sync both directories preserving ownership", ticket, err, calls-before)
			}
		})
	}
}
func TestBTPlannedNamespaceBlacklistResolvesMissingAliasTarget(t *testing.T) {
	newBTTaskTest(t)
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	namespace := filepath.Join(root, BTNamespaceDirectory)
	database.DB.Create(&models.Settings{ScanExcludePaths: filepath.Join(alias, BTNamespaceDirectory)})
	database.DB.Create(&models.ScanDirectory{Path: namespace})
	if err := ValidateBTDownloadRoot(context.Background(), root); !errors.Is(err, ErrBTPathNotInLibrary) {
		t.Fatal("blacklisted future real namespace allowed", err)
	}
	if _, err := os.Stat(namespace); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("validation created namespace")
	}
}
func TestBTTaskSummaryCompletedSeedsCannotHidePendingWork(t *testing.T) {
	s, _ := newBTTaskTest(t)
	view := btSubmitFirst(t, s)
	var base models.BTTask
	database.DB.First(&base, "id = ?", view.ID)
	var files []models.BTFile
	database.DB.Where("task_id = ?", view.ID).Find(&files)
	database.DB.Model(&models.BTTask{}).Where("id = ?", view.ID).Updates(map[string]any{"stage": "active", "remote_state": "downloading", "updated_at": time.Now().Add(-time.Hour)})
	for i := 0; i < 105; i++ {
		row := base
		row.ID = uuid.NewString()
		row.IntentKey = row.ID
		row.OwnerTag = row.ID
		row.RootKey = fmt.Sprintf("%064x", i+1000)
		row.InfoHashV1 = nil
		row.InfoHashV2 = nil
		row.Stage = "active"
		row.RemoteState = "uploading"
		row.PendingAction = ""
		row.UnresolvedWrite = false
		row.UpdatedAt = time.Now().Add(time.Hour)
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			file.TaskID = row.ID
			file.Progress = 1
			file.ImportState = "imported"
			file.ErrorCode = ""
			if err := database.DB.Create(&file).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	check := func(want string) {
		t.Helper()
		rows, err := s.RecentSummaries(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.ID == view.ID {
				if r.Status != want {
					t.Fatal(r)
				}
				return
			}
		}
		t.Fatal("completed seeding history hid pending task")
	}
	check("running")
	database.DB.Model(&models.BTFile{}).Where("task_id = ?", view.ID).Updates(map[string]any{"progress": 1, "import_state": "failed", "error_code": "import_failed"})
	check("failed")
}
func TestBTTaskSummaryRemoteStatePrecedesStaleCompletion(t *testing.T) {
	s, _ := newBTTaskTest(t)
	view := btSubmitFirst(t, s)
	database.DB.Model(&models.BTFile{}).Where("task_id = ?", view.ID).Updates(map[string]any{"progress": 1, "import_state": "imported"})
	for state, want := range map[string]string{"error": "failed", "missingFiles": "failed", "checkingUP": "running", "checkingDL": "running", "checkingResumeData": "running", "moving": "running"} {
		database.DB.Model(&models.BTTask{}).Where("id = ?", view.ID).Updates(map[string]any{"stage": "active", "remote_state": state})
		rows, err := s.RecentSummaries(context.Background())
		if err != nil || len(rows) != 1 || rows[0].Status != want || rows[0].Message == "" {
			t.Fatal(state, rows, err)
		}
	}
}

func TestBTRestoreSQLiteReplacementTicketsPreservePartialOwnership(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("SQLite final-component replacement")
	}
	for _, existing := range []string{"none", "current", "both"} {
		t.Run(existing, func(t *testing.T) {
			s, _ := newBTTaskTest(t)
			f := NewBTRestoreFence(s.dataDir)
			ctx := context.Background()
			var rows []struct {
				Name string
				File string
			}
			database.DB.Raw("PRAGMA database_list").Scan(&rows)
			actual := ""
			for _, r := range rows {
				if r.Name == "main" {
					actual = r.File
				}
			}
			alias := filepath.Join(t.TempDir(), "alias.db")
			if err := os.Symlink(actual, alias); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SQLITE_PATH", alias)
			current, _ := BTCredentialScope(ctx, database.DB)
			parent, _ := filepath.EvalSymlinks(filepath.Dir(alias))
			planned := btSQLiteDatabaseScope(filepath.Join(parent, filepath.Base(alias)))
			oldNonces := map[string]string{}
			if existing == "current" {
				ticket, err := f.Prepare(ctx, database.DB)
				if err != nil {
					t.Fatal(err)
				}
				oldNonces[current] = ticket.Nonce
			}
			if existing == "both" {
				ticket, err := f.PrepareRestore(ctx, database.DB)
				if err != nil || len(ticket.additional) != 1 {
					t.Fatal(ticket, err)
				}
				oldNonces[current] = ticket.Nonce
				oldNonces[planned] = ticket.additional[0].Nonce
			}
			calls := 0
			f.syncDirectory = func(path string) error {
				calls++
				if calls == 3 {
					return errors.New("second scope sync failed")
				}
				return btSyncDirectory(path)
			}
			ticket, err := f.PrepareRestore(ctx, database.DB)
			if err == nil || len(ticket.additional) != 1 || ticket.Scope != current || ticket.additional[0].Scope != planned {
				t.Fatal("partial ownership lost", ticket, err)
			}
			if err = f.ClearOwned(ticket); err != nil {
				t.Fatal(err)
			}
			for _, scope := range []string{current, planned} {
				marker, err := f.read(scope)
				if nonce, old := oldNonces[scope]; old {
					if err != nil || marker.Nonce != nonce {
						t.Fatal("existing marker removed", scope, err)
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatal("owned marker leaked after known-unstarted failure", scope, err)
				}
			}
		})
	}
}
func TestBTRestoreSQLiteNormalPathDeduplicatesAndConfigOnlyKeepsPhysicalScope(t *testing.T) {
	if dbtest.IsPostgres() {
		t.Skip("SQLite path identities")
	}
	s, _ := newBTTaskTest(t)
	f := NewBTRestoreFence(s.dataDir)
	ctx := context.Background()
	var rows []struct {
		Name string
		File string
	}
	database.DB.Raw("PRAGMA database_list").Scan(&rows)
	actual := ""
	for _, r := range rows {
		if r.Name == "main" {
			actual = r.File
		}
	}
	t.Setenv("SQLITE_PATH", actual)
	ticket, err := f.PrepareRestore(ctx, database.DB)
	if err != nil || len(ticket.additional) != 0 {
		t.Fatal("regular file did not deduplicate", ticket, err)
	}
	if err = f.ClearOwned(ticket); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias.db")
	if err = os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SQLITE_PATH", alias)
	ticket, err = f.Prepare(ctx, database.DB)
	if err != nil || len(ticket.additional) != 0 {
		t.Fatal("config-only acquired replacement scope", ticket, err)
	}
	markers, err := filepath.Glob(filepath.Join(s.dataDir, "bt-recovery", "*.json"))
	if err != nil || len(markers) != 1 {
		t.Fatal(markers, err)
	}
}
