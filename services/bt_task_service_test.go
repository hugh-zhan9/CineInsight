package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"video-master/models"

	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/services/btclient"
	"video-master/services/btmanifest"
)

func btTestBencode(v any) []byte {
	var b bytes.Buffer
	switch x := v.(type) {
	case string:
		fmt.Fprintf(&b, "%d:%s", len(x), x)
	case int:
		fmt.Fprintf(&b, "i%de", x)
	case []any:
		b.WriteByte('l')
		for _, item := range x {
			b.Write(btTestBencode(item))
		}
		b.WriteByte('e')
	case map[string]any:
		b.WriteByte('d')
		keys := []string{}
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.Write(btTestBencode(k))
			b.Write(btTestBencode(x[k]))
		}
		b.WriteByte('e')
	}
	return b.Bytes()
}
func btTaskFixtureTorrent() []byte {
	return btTestBencode(map[string]any{"info": map[string]any{"name": "season", "piece length": 16384, "pieces": strings.Repeat("x", 20), "files": []any{map[string]any{"length": 4, "path": []any{"one.mkv"}}, map[string]any{"length": 4, "path": []any{"two.mkv"}}, map[string]any{"length": 1, "path": []any{"readme.txt"}}}}})
}

type btTaskFakeSearch struct {
	data   []byte
	magnet string
}

func (f *btTaskFakeSearch) Indexers(context.Context) ([]btclient.Indexer, error) {
	return []btclient.Indexer{{ID: 1, Name: "fixture", Enable: true, Protocol: "torrent"}}, nil
}
func (f *btTaskFakeSearch) Search(context.Context, string, int, int) ([]btclient.Release, error) {
	return []btclient.Release{{Title: "fixture season", IndexerID: 1, Indexer: "fixture", Size: 9, Protocol: "torrent", DownloadURL: "opaque-secret"}}, nil
}
func (f *btTaskFakeSearch) Torrent(context.Context, string, int) ([]byte, error) { return f.data, nil }
func (f *btTaskFakeSearch) FetchTorrent(context.Context, string, int) (btclient.ProxyResult, error) {
	return btclient.ProxyResult{Torrent: f.data, Magnet: f.magnet}, nil
}

type btFakeRemoteTask struct {
	info       btclient.QBTorrent
	properties btclient.QBProperties
	files      []btclient.QBFile
	data       []byte
}
type btTaskFakeRemote struct {
	mu            sync.Mutex
	tasks         map[string]*btFakeRemoteTask
	adds          int
	actions       []string
	addErr        error
	readErr       error
	priorityFail  int
	startErr      error
	beforeAdd     func(context.Context)
	runNoop       bool
	deletes       int
	metadataData  []byte
	profileErr    error
	afterPriority func(int, *btFakeRemoteTask)
	importWrites  []string
}

func (f *btTaskFakeRemote) CheckQBProfile(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.profileErr
}
func (f *btTaskFakeRemote) CheckQBMetadataProfile(context.Context) error {
	if len(f.metadataData) > 0 {
		return nil
	}
	return ErrBTMetadata
}
func (f *btTaskFakeRemote) QBTorrents(_ context.Context, hash string) ([]btclient.QBTorrent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	if row := f.tasks[hash]; row != nil {
		return []btclient.QBTorrent{row.info}, nil
	}
	return nil, nil
}
func (f *btTaskFakeRemote) QBProperties(_ context.Context, hash string) (btclient.QBProperties, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if row := f.tasks[hash]; row != nil {
		return row.properties, nil
	}
	return btclient.QBProperties{}, errBTMissing
}
func (f *btTaskFakeRemote) QBFiles(_ context.Context, hash string) ([]btclient.QBFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if row := f.tasks[hash]; row != nil {
		return append([]btclient.QBFile{}, row.files...), nil
	}
	return nil, errBTMissing
}
func (f *btTaskFakeRemote) QBAddStopped(ctx context.Context, o btclient.QBAddOptions) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adds++
	if f.beforeAdd != nil {
		f.beforeAdd(ctx)
	}
	data := o.Torrent
	if o.MetadataOnly {
		data = f.metadataData
	}
	m, err := btmanifest.Parse(data)
	if err != nil {
		return "", err
	}
	hash := m.InfoHashV1
	if m.InfoHashV2 != "" {
		hash = m.InfoHashV2[:40]
	}
	row := &btFakeRemoteTask{info: btclient.QBTorrent{Hash: hash, SavePath: o.SavePath, Tags: o.OwnerTag, Category: "CineInsight", State: "stoppedDL"}, properties: btclient.QBProperties{Hash: hash, InfoHashV1: m.InfoHashV1, InfoHashV2: m.InfoHashV2}, data: data}
	for i, file := range m.Files {
		if !file.Padding {
			row.files = append(row.files, btclient.QBFile{Index: 4 + i*2, Name: file.Path, Size: file.Size, Priority: 1})
		}
	}
	f.tasks[hash] = row
	return hash, f.addErr
}
func (f *btTaskFakeRemote) QBRun(_ context.Context, hash string, start bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.tasks[hash]
	if row == nil {
		return errBTMissing
	}
	if f.runNoop {
		return nil
	}
	if start {
		f.actions = append(f.actions, "start")
		row.info.State = "downloading"
	} else {
		f.actions = append(f.actions, "stop")
		row.info.State = "stoppedDL"
	}
	return f.startErr
}
func (f *btTaskFakeRemote) QBPriority(_ context.Context, hash string, indices []int, priority int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.tasks[hash]
	if row == nil {
		return errBTMissing
	}
	f.actions = append(f.actions, fmt.Sprintf("priority:%d", priority))
	if f.priorityFail == priority+1 {
		return errors.New("priority timeout")
	}
	for _, index := range indices {
		found := false
		for i := range row.files {
			if row.files[i].Index == index {
				row.files[i].Priority = priority
				found = true
			}
		}
		if !found {
			return errors.New("wrong file index")
		}
	}
	if f.afterPriority != nil {
		f.afterPriority(priority, row)
	}
	return nil
}
func (f *btTaskFakeRemote) QBDeleteMetadata(_ context.Context, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	delete(f.tasks, hash)
	return nil
}
func (f *btTaskFakeRemote) QBExport(_ context.Context, hash string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if row := f.tasks[hash]; row != nil {
		return row.data, nil
	}
	return nil, errBTMissing
}
func newBTTaskTest(t *testing.T) (*BTService, *btTaskFakeRemote) {
	t.Helper()
	old := database.DB
	database.DB = dbtest.Open(t)
	t.Cleanup(func() { database.DB = old })
	store := btMemorySecrets{}
	remote := &btTaskFakeRemote{tasks: map[string]*btFakeRemoteTask{}}
	source := &btTaskFakeSearch{data: btTaskFixtureTorrent()}
	deps := BTServiceDeps{Credentials: func(context.Context, *gorm.DB) (*BTCredentialManager, error) {
		return NewBTCredentialManager(store), nil
	}, ValidateRoot: func(context.Context, string) error { return nil }, EnsureNamespace: func(root, id string) (string, error) {
		ns := filepath.Join(root, "CineInsight")
		return ns, os.MkdirAll(ns, 0700)
	}, ImportFile: func(ctx context.Context, task, file string) error {
		remote.importWrites = append(remote.importWrites, file)
		return nil
	}, RemoteFactory: func(*btclient.Client) BTRemote { return remote }, SearchFactory: func(*btclient.Client) BTSearchSource { return source }}
	s := NewBTService(t.TempDir(), deps)
	if _, err := s.Connection().Configure(context.Background(), BTConnectionRequest{ProwlarrBase: "http://127.0.0.1:9696", QBBase: "http://127.0.0.1:8081", Enabled: true, DownloadRoot: t.TempDir(), ProwlarrKey: BTSecretChange{Operation: "replace", Value: "test-key"}}); err != nil {
		t.Fatal(err)
	}
	return s, remote
}
func btPreviewTest(t *testing.T, s *BTService) BTTaskView {
	t.Helper()
	found, err := s.Search(context.Background(), "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.Preview(context.Background(), found.Resources[0].Token)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Preview(context.Background(), found.Resources[0].Token)
	if err != nil || again.ID != preview.ID {
		t.Fatal("preview not idempotent", err)
	}
	return preview
}
func TestBTTaskSelectiveDownloadAndImport(t *testing.T) {
	s, remote := newBTTaskTest(t)
	ctx := context.Background()
	preview := btPreviewTest(t, s)
	if len(preview.Files) != 3 || remote.adds != 0 {
		t.Fatal("preview started download")
	}
	first := preview.Files[0].ID
	task, err := s.Submit(ctx, BTSubmitRequest{DraftID: preview.ID, ExpectedRevision: preview.Revision, SelectedFileIDs: []string{first}})
	if err != nil {
		t.Fatal(err)
	}
	if remote.adds != 0 {
		t.Fatal("Submit bypassed serialized worker")
	}
	if err = s.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	task, err = s.Get(ctx, task.ID)
	if err != nil || task.Stage != "active" {
		t.Fatalf("%+v %v", task, err)
	}
	if strings.Join(remote.actions, ",") != "priority:0,priority:1,start" {
		t.Fatal(remote.actions)
	}
	var row *btFakeRemoteTask
	for _, r := range remote.tasks {
		row = r
	}
	if row.files[0].Priority != 1 || row.files[1].Priority != 0 || row.files[2].Priority != 0 {
		t.Fatal("selection was not honored")
	}
	row.files[0].Progress = 1
	row.info.State = "stalledUP"
	if err = s.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(remote.importWrites) != 1 || remote.importWrites[0] != first {
		t.Fatal("wrong import scope", remote.importWrites)
	}
	task, _ = s.Get(ctx, task.ID)
	if !task.DownloadComplete || task.SelectedBytes != "4" {
		t.Fatalf("selected completion: %+v", task)
	}
}
func TestBTTaskUncertainNeverBlindlyReadds(t *testing.T) {
	s, remote := newBTTaskTest(t)
	preview := btPreviewTest(t, s)
	remote.addErr = errors.New("timeout after add")
	task, err := s.Submit(context.Background(), BTSubmitRequest{DraftID: preview.ID, ExpectedRevision: preview.Revision, SelectedFileIDs: []string{preview.Files[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Poll(context.Background())
	_ = s.Poll(context.Background())
	if remote.adds != 1 {
		t.Fatal("uncertain add replayed")
	}
	task, _ = s.Get(context.Background(), task.ID)
	if task.Stage != "conflict" && task.Stage != "uncertain" {
		t.Fatal(task.Stage)
	}
}
func TestBTTaskRejectsExternalAndEmptySelection(t *testing.T) {
	s, remote := newBTTaskTest(t)
	preview := btPreviewTest(t, s)
	if _, err := s.Submit(context.Background(), BTSubmitRequest{DraftID: preview.ID, ExpectedRevision: preview.Revision}); !errors.Is(err, ErrBTSelection) {
		t.Fatal(err)
	}
	manifest, _ := btmanifest.Parse(btTaskFixtureTorrent())
	remote.tasks[manifest.InfoHashV1] = &btFakeRemoteTask{info: btclient.QBTorrent{Hash: manifest.InfoHashV1, Tags: "user", SavePath: "/elsewhere"}}
	if _, err := s.Submit(context.Background(), BTSubmitRequest{DraftID: preview.ID, ExpectedRevision: preview.Revision, SelectedFileIDs: []string{preview.Files[0].ID}}); !errors.Is(err, ErrBTExternal) {
		t.Fatal(err)
	}
	if remote.adds != 0 || len(remote.actions) != 0 {
		t.Fatal("external task mutated")
	}
}

func btSubmitFirst(t *testing.T, s *BTService) BTTaskView {
	t.Helper()
	p := btPreviewTest(t, s)
	result, err := s.Submit(context.Background(), BTSubmitRequest{DraftID: p.ID, ExpectedRevision: p.Revision, SelectedFileIDs: []string{p.Files[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func btRemoteFirst(t *testing.T, r *btTaskFakeRemote) *btFakeRemoteTask {
	t.Helper()
	for _, row := range r.tasks {
		return row
	}
	t.Fatal("remote absent")
	return nil
}

func TestBTTaskAbandonedRepreviewRestoresOriginalIntent(t *testing.T) {
	s, remote := newBTTaskTest(t)
	ctx := context.Background()
	task := btSubmitFirst(t, s)
	if err := database.DB.Model(&models.BTTask{}).Where("id = ?", task.ID).Updates(map[string]any{"stage": "uncertain"}).Error; err != nil {
		t.Fatal(err)
	}
	task, _ = s.Get(ctx, task.ID)
	abandoned, err := s.Control(ctx, BTControlRequest{TaskID: task.ID, ExpectedRevision: task.Revision, Action: "abandon", Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(btTaskCacheFile(t, s, task.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("abandon retained cache")
	}
	restored := btPreviewTest(t, s)
	if restored.ID != task.ID || restored.SavePath != task.SavePath || restored.Revision != abandoned.Revision {
		t.Fatal("repreview changed original intent")
	}
	task, err = s.Control(ctx, BTControlRequest{TaskID: restored.ID, ExpectedRevision: restored.Revision, Action: "reconfirm_submit", Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if remote.adds != 1 {
		t.Fatal("original intent not resumed")
	}
}
func TestBTTaskMaintenanceRestoredQueuedCommandsOnlyReconcile(t *testing.T) {
	for _, stage := range []string{"queued_add", "queued_metadata", "adding", "selecting", "starting", "metadata_adding", "metadata_wait", "queued_stop", "queued_start", "cancel_pending"} {
		t.Run(stage, func(t *testing.T) {
			s, remote := newBTTaskTest(t)
			ctx := context.Background()
			task := btSubmitFirst(t, s)
			if err := s.Quiesce(ctx); err != nil {
				t.Fatal(err)
			}
			// Simulate restoring an older DB row after the worker has drained.
			if err := database.DB.Model(&models.BTTask{}).Where("id = ?", task.ID).Update("stage", stage).Error; err != nil {
				t.Fatal(err)
			}
			if err := s.Poll(ctx); !errors.Is(err, ErrBTMaintenance) {
				t.Fatal(err)
			}
			s.Resume()
			if err := s.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			fresh, _ := s.Get(ctx, task.ID)
			if fresh.Stage != "uncertain" || remote.adds != 0 || len(remote.actions) != 0 || remote.deletes != 0 {
				t.Fatalf("restored command replayed: %+v", fresh)
			}
		})
	}
}
func TestBTTaskCrashAtExternalBoundariesNeverRepeatsAdd(t *testing.T) {
	for _, boundary := range []string{"add", "zero_priority", "selected_priority", "start"} {
		t.Run(boundary, func(t *testing.T) {
			s, r := newBTTaskTest(t)
			ctx := context.Background()
			task := btSubmitFirst(t, s)
			switch boundary {
			case "add":
				r.addErr = errors.New("response lost")
			case "zero_priority":
				r.priorityFail = 1
			case "selected_priority":
				r.priorityFail = 2
			case "start":
				r.startErr = errors.New("response lost")
			}
			if err := s.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			if r.adds != 1 {
				t.Fatal("missing single add")
			}
			s.Quiesce(ctx)
			s.Resume()
			r.addErr = nil
			r.priorityFail = 0
			r.startErr = nil
			if err := s.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			if r.adds != 1 {
				t.Fatal("crash recovery re-added")
			}
			fresh, _ := s.Get(ctx, task.ID)
			if fresh.Stage != "uncertain" || !fresh.UnresolvedWrite {
				t.Fatal("lost write was washed by observation", fresh.Stage)
			}
		})
	}
}
func TestBTTaskPollRotatesPastTwoHundredOldTasks(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	target := btSubmitFirst(t, s)
	var original models.BTTask
	if err := database.DB.First(&original, "id = ?", target.ID).Error; err != nil {
		t.Fatal(err)
	}
	old := make([]models.BTTask, 201)
	for i := range old {
		id := uuid.NewString()
		old[i] = models.BTTask{ID: id, ConnectionID: original.ConnectionID, Revision: 1, IntentKey: id, OwnerTag: id, SaveRoot: filepath.Join(original.SaveRoot, id), CanonicalRoot: filepath.Join(original.SaveRoot, id), RootKey: btDigest([]byte(id)), Stage: "abandoned", CreatedAt: original.CreatedAt.Add(-time.Hour)}
	}
	if err := database.DB.Omit("Connection").Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if r.adds != 0 {
		t.Fatal("first bounded page unexpectedly reached target")
	}
	if err := s.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if r.adds != 1 {
		t.Fatal("old tasks starved target")
	}
}
func TestBTTaskExternalChangesStopImportWithoutMutatingClient(t *testing.T) {
	for _, change := range []string{"selection", "path", "hash", "missing", "offline"} {
		t.Run(change, func(t *testing.T) {
			s, r := newBTTaskTest(t)
			ctx := context.Background()
			task := btSubmitFirst(t, s)
			s.Poll(ctx)
			row := btRemoteFirst(t, r)
			row.files[0].Progress = 1
			actions := len(r.actions)
			switch change {
			case "selection":
				row.files[1].Priority = 1
			case "path":
				row.info.SavePath += "-moved"
			case "hash":
				row.properties.InfoHashV1 = strings.Repeat("0", 40)
			case "missing":
				delete(r.tasks, row.info.Hash)
			case "offline":
				r.readErr = errors.New("service unavailable")
			}
			s.Poll(ctx)
			fresh, _ := s.Get(ctx, task.ID)
			if fresh.Stage == "released" || len(r.importWrites) != 0 || len(r.actions) != actions || r.adds != 1 {
				t.Fatalf("unsafe external reconciliation %+v", fresh)
			}
		})
	}
}
func TestBTTaskPendingPublicationRetriesAndHidesVideo(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	task := btSubmitFirst(t, s)
	s.Poll(ctx)
	row := btRemoteFirst(t, r)
	row.files[0].Progress = 1
	row.info.State = "stalledUP"
	id := task.Files[0].ID
	video := uint(42)
	if err := database.DB.Model(&models.BTFile{}).Where("task_id = ? AND manifest_file_id = ?", task.ID, id).Updates(map[string]any{"import_state": "imported", "publication_state": "pending_new", "video_id": video}).Error; err != nil {
		t.Fatal(err)
	}
	view, _ := s.Get(ctx, task.ID)
	if view.ImportedCount != 0 || view.Files[0].VideoID != nil || view.Files[0].ImportState == "imported" {
		t.Fatal("unpublished video exposed")
	}
	s.Poll(ctx)
	if len(r.importWrites) != 1 {
		t.Fatal("pending publication not retried")
	}
	// P003 commits the publication result; P002 must preserve it.
	database.DB.Model(&models.BTFile{}).Where("task_id = ? AND manifest_file_id = ?", task.ID, id).Update("publication_state", "done")
	s.Poll(ctx)
	if len(r.importWrites) != 1 {
		t.Fatal("published video reimported")
	}
	view, _ = s.Get(ctx, task.ID)
	if view.ImportedCount != 1 || view.Files[0].VideoID != nil {
		t.Fatal("missing logical video reference exposed")
	}
}
func TestBTTaskCancelMetadataRequiresStoppedReadback(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	task := btSubmitFirst(t, s)
	s.Poll(ctx)
	row := btRemoteFirst(t, r)
	database.DB.Model(&models.BTTask{}).Where("id = ?", task.ID).Update("stage", "metadata_wait")
	task, _ = s.Get(ctx, task.ID)
	if _, err := s.Control(ctx, BTControlRequest{TaskID: task.ID, ExpectedRevision: task.Revision, Action: "cancel_draft"}); err != nil {
		t.Fatal(err)
	}
	r.runNoop = true
	s.Poll(ctx)
	if r.deletes != 0 {
		t.Fatal("deleted before stop readback")
	}
	row.info.State = "stoppedDL"
	database.DB.Model(&models.BTTask{}).Where("id = ?", task.ID).Update("stage", "cancel_pending")
	s.Poll(ctx)
	fresh, _ := s.Get(ctx, task.ID)
	if r.deletes != 1 || fresh.Stage != "cancelled" {
		t.Fatal("owned stopped draft not cancelled")
	}
	if _, err := os.Stat(task.SavePath); err != nil {
		t.Fatal("cancel removed data directory")
	}
}
func TestBTTaskReleaseRejectsResidueAndRequiresConfirmation(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	task := btSubmitFirst(t, s)
	s.Poll(ctx)
	row := btRemoteFirst(t, r)
	row.files[0].Progress = 1
	row.info.State = "stoppedUP"
	s.Poll(ctx)
	task, _ = s.Get(ctx, task.ID)
	path := filepath.Join(task.SavePath, filepath.FromSlash(task.Files[0].Path))
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte("1234"), 0600)
	req := BTControlRequest{TaskID: task.ID, ExpectedRevision: task.Revision, Action: "release"}
	if _, err := s.Control(ctx, req); err == nil {
		t.Fatal("release accepted without confirmation")
	}
	req.Confirmed = true
	residue := filepath.Join(task.SavePath, "unselected.part")
	os.WriteFile(residue, []byte("x"), 0600)
	if _, err := s.Control(ctx, req); err == nil {
		t.Fatal("released unselected residue")
	}
	os.Remove(residue)
	os.Remove(path)
	os.Symlink(t.TempDir(), path)
	if _, err := s.Control(ctx, req); err == nil {
		t.Fatal("released symlink")
	}
	os.Remove(path)
	os.WriteFile(path, []byte("1234"), 0600)
	result, err := s.Control(ctx, req)
	if err != nil || result.Stage != "released" {
		t.Fatal("safe release failed", err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("release deleted data")
	}
}
func TestBTTaskImportFailureOwnerResultIsPreserved(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	task := btSubmitFirst(t, s)
	s.Poll(ctx)
	row := btRemoteFirst(t, r)
	row.files[0].Progress = 1
	s.deps.ImportFile = func(ctx context.Context, taskID, fileID string) error {
		if err := database.DB.Model(&models.BTFile{}).Where("task_id = ? AND manifest_file_id = ?", taskID, fileID).Updates(map[string]any{"import_state": "failed", "error_code": "path_not_in_library"}).Error; err != nil {
			t.Fatal(err)
		}
		return errors.New("controlled import failure")
	}
	s.Poll(ctx)
	view, _ := s.Get(ctx, task.ID)
	if view.FailedCount != 1 || view.Files[0].ErrorCode != "path_not_in_library" {
		t.Fatal("import outcome lost")
	}
}

func TestBTTaskDrainCancelsUnknownWriteAndWaitsWorker(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	task := btSubmitFirst(t, s)
	entered := make(chan struct{})
	returned := make(chan struct{})
	r.beforeAdd = func(ctx context.Context) { close(entered); <-ctx.Done(); r.addErr = ctx.Err() }
	done := make(chan error, 1)
	go func() { defer close(returned); done <- s.Poll(ctx) }()
	<-entered
	drainCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.Quiesce(drainCtx); err != nil {
		t.Fatal(err)
	}
	<-returned
	<-done
	if r.adds != 1 {
		t.Fatal("expected one in-flight attempt")
	}
	s.Resume()
	r.beforeAdd = nil
	r.addErr = nil
	if err := s.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if r.adds != 1 {
		t.Fatal("cancelled unknown write was replayed")
	}
	view, _ := s.Get(ctx, task.ID)
	if view.Stage == "released" {
		t.Fatal("unknown write lost root protection")
	}
}
func TestBTTaskRootReplacementBlocksExternalAdd(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	task := btSubmitFirst(t, s)
	namespace := filepath.Dir(task.SavePath)
	if err := os.Remove(namespace); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, namespace); err != nil {
		t.Fatal(err)
	}
	if err := s.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if r.adds != 0 {
		t.Fatal("added into replaced namespace")
	}
	view, _ := s.Get(ctx, task.ID)
	if view.Stage != "conflict" || view.ErrorCode != "path_unsafe" {
		t.Fatal("unsafe root not surfaced")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("created directory through symlink")
	}
}

func TestBTTaskUnverifiedMagnetNeverCreatesPayloadTask(t *testing.T) {
	s, r := newBTTaskTest(t)
	source := &btTaskFakeSearch{magnet: "magnet:?xt=urn:btih:" + strings.Repeat("a", 40)}
	s.deps.SearchFactory = func(*btclient.Client) BTSearchSource { return source }
	found, err := s.Search(context.Background(), "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Preview(context.Background(), found.Resources[0].Token); !errors.Is(err, ErrBTMetadata) {
		t.Fatal("unverified metadata capability enabled", err)
	}
	var count int64
	database.DB.Model(&models.BTTask{}).Count(&count)
	if count != 0 || r.adds != 0 {
		t.Fatal("unsupported magnet started download")
	}
}
func TestBTTaskVerifierIsReadOnlyAndRejectsSymlink(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	view := btSubmitFirst(t, s)
	s.Poll(ctx)
	row := btRemoteFirst(t, r)
	row.files[0].Progress = 1
	row.info.State = "stalledUP"
	s.Poll(ctx)
	task, files, err := btLoadTask(ctx, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	file := files[0]
	path := filepath.Join(task.CanonicalRoot, filepath.FromSlash(file.RelativePath))
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte("1234"), 0600)
	if err := s.VerifyBTImport(ctx, task, file); err != nil {
		t.Fatal(err)
	}
	fresh, current, _ := btLoadTask(ctx, task.ID)
	if fresh.Revision != task.Revision || current[0].EligibilityRevision != file.EligibilityRevision {
		t.Fatal("verifier mutated generations")
	}
	row.info.State = "checkingUP"
	if err := s.VerifyBTImport(ctx, task, file); err == nil {
		t.Fatal("state mismatch accepted")
	}
	row.info.State = task.RemoteState
	outside := filepath.Join(t.TempDir(), "outside.mkv")
	os.WriteFile(outside, []byte("1234"), 0600)
	os.Remove(path)
	os.Symlink(outside, path)
	if err := s.VerifyBTImport(ctx, task, file); !errors.Is(err, ErrBTPath) {
		t.Fatal("escaped file accepted", err)
	}
}

func TestBTTaskDraftExpiryKeepsRootProtection(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		t.Run(fmt.Sprint(metadata), func(t *testing.T) {
			s, r := newBTTaskTest(t)
			ctx := context.Background()
			view := btPreviewTest(t, s)
			stage := "draft"
			if metadata {
				stage = "metadata_ready"
			}
			if err := database.DB.Model(&models.BTTask{}).Where("id = ?", view.ID).Updates(map[string]any{"created_at": time.Now().Add(-25 * time.Hour), "updated_at": time.Now().Add(-25 * time.Hour), "stage": stage}).Error; err != nil {
				t.Fatal(err)
			}
			if err := s.ExpireDrafts(ctx); err != nil {
				t.Fatal(err)
			}
			fresh, _ := s.Get(ctx, view.ID)
			if metadata {
				if fresh.Stage != "cancel_pending" {
					t.Fatal("metadata expiry bypassed remote reconciliation")
				}
				s.Poll(ctx)
				fresh, _ = s.Get(ctx, view.ID)
			}
			if fresh.Stage != "cancelled" || fresh.SavePath != view.SavePath || r.adds != 0 {
				t.Fatal("expiry lost root tombstone")
			}
			if _, err := os.Stat(btTaskCacheFile(t, s, view.ID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("expired cache retained")
			}
		})
	}
}

func TestBTTaskConflictCannotReconfirmOrAbandonSelection(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	task := btSubmitFirst(t, s)
	s.Poll(ctx)
	row := btRemoteFirst(t, r)
	row.info.State = "stoppedDL"
	row.files[1].Priority = 1
	s.Poll(ctx)
	task, _ = s.Get(ctx, task.ID)
	if task.Stage != "conflict" {
		t.Fatal(task.Stage)
	}
	count := len(r.actions)
	for _, action := range []string{"reconfirm_submit", "abandon"} {
		if _, err := s.Control(ctx, BTControlRequest{TaskID: task.ID, ExpectedRevision: task.Revision, Action: action, Confirmed: true}); !errors.Is(err, ErrBTConflict) {
			t.Fatal("conflict bypass", action, err)
		}
	}
	if len(r.actions) != count || row.files[1].Priority != 1 {
		t.Fatal("external choice overwritten")
	}
}

func TestBTTaskMagnetMetadataThenSelectionReusesOwnedTask(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	data := btTaskFixtureTorrent()
	m, err := btmanifest.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	r.metadataData = data
	source := &btTaskFakeSearch{magnet: "magnet:?xt=urn:btih:" + m.InfoHashV1}
	s.deps.SearchFactory = func(*btclient.Client) BTSearchSource { return source }
	found, err := s.Search(ctx, "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.Preview(ctx, found.Resources[0].Token)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Stage != "metadata_adding" || len(preview.Files) != 0 || r.adds != 0 {
		t.Fatal("metadata intent not persisted first")
	}
	if err = s.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if r.adds != 1 || len(r.actions) != 0 {
		t.Fatal("metadata flow issued a separate start")
	}
	if err = s.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	ready, err := s.Get(ctx, preview.ID)
	if err != nil || ready.Stage != "metadata_ready" || len(ready.Files) != 3 {
		t.Fatal("metadata not prepared", ready.Stage, err)
	}
	again, err := s.Preview(ctx, found.Resources[0].Token)
	if err != nil || again.ID != ready.ID || again.Revision != ready.Revision {
		t.Fatal("resolved magnet preview lost idempotency", err)
	}
	if _, err = s.Submit(ctx, BTSubmitRequest{DraftID: ready.ID, ExpectedRevision: ready.Revision, SelectedFileIDs: []string{ready.Files[0].ID}}); err != nil {
		t.Fatal(err)
	}
	if err = s.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if r.adds != 1 || strings.Join(r.actions, ",") != "priority:0,priority:1,start" {
		t.Fatal("metadata resubmitted or started before priority confirmation", r.actions)
	}
	final, _ := s.Get(ctx, preview.ID)
	if final.Stage != "active" || final.SavePath != preview.SavePath {
		t.Fatal("metadata task identity changed")
	}
}

func TestBTTaskCommandsRequireRequestedStateReadback(t *testing.T) {
	for _, start := range []bool{false, true} {
		t.Run(fmt.Sprint(start), func(t *testing.T) {
			s, r := newBTTaskTest(t)
			ctx := context.Background()
			task := btSubmitFirst(t, s)
			s.Poll(ctx)
			row := btRemoteFirst(t, r)
			action := "stop"
			if start {
				row.info.State = "stoppedDL"
				action = "start"
			}
			task, _ = s.Get(ctx, task.ID)
			if _, err := s.Control(ctx, BTControlRequest{TaskID: task.ID, ExpectedRevision: task.Revision, Action: action}); err != nil {
				t.Fatal(err)
			}
			r.runNoop = true
			if err := s.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			result, _ := s.Get(ctx, task.ID)
			if result.Stage != "uncertain" {
				t.Fatal("unconfirmed state reported success", result.Stage)
			}
		})
	}
}

func TestBTTaskRealImporterSelectedFileAndUserDelete(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	if err := database.DB.Create(&models.Settings{}).Error; err != nil {
		t.Fatal(err)
	}
	s.deps.EnsureNamespace = WriteBTNamespaceMarker
	connection, err := s.Connection().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = database.DB.Create(&models.ScanDirectory{Path: connection.DownloadRoot}).Error; err != nil {
		t.Fatal(err)
	}
	policy := NewBTPathPolicy(func(ctx context.Context) (*gorm.DB, error) { return database.DB.WithContext(ctx), nil })
	videos := &VideoService{}
	videos.SetBTPathPolicy(policy)
	importer := NewBTImporter(videos, policy, s)
	s.deps.ImportFile = func(ctx context.Context, taskID, fileID string) error {
		_, err := importer.Import(ctx, taskID, fileID)
		return err
	}
	view := btSubmitFirst(t, s)
	if err = s.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	row := btRemoteFirst(t, r)
	row.files[0].Progress = 1
	row.info.State = "stalledUP"
	path := filepath.Join(view.SavePath, filepath.FromSlash(view.Files[0].Path))
	os.MkdirAll(filepath.Dir(path), 0700)
	payload := []byte("1234")
	os.WriteFile(path, payload, 0600)
	unselected := filepath.Join(view.SavePath, filepath.FromSlash(view.Files[1].Path))
	os.WriteFile(unselected, []byte("1"), 0600)
	if err = s.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	ready, err := s.Get(ctx, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ready.ImportedCount != 1 || ready.Files[0].VideoID == nil || !ready.DownloadComplete {
		t.Fatalf("real import failed: %+v", ready)
	}
	var count int64
	database.DB.Model(&models.Video{}).Count(&count)
	if count != 1 {
		t.Fatal("unselected/partial file imported", count)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(payload, after) || row.info.State != "stalledUP" {
		t.Fatal("import changed payload or seeding")
	}
	id := *ready.Files[0].VideoID
	for _, change := range []string{"stale", "path", "scope"} {
		switch change {
		case "stale":
			database.DB.Model(&models.Video{}).Where("id = ?", id).Update("is_stale", true)
		case "path":
			database.DB.Model(&models.Video{}).Where("id = ?", id).Update("path", filepath.Join(connection.DownloadRoot, "different.mkv"))
		case "scope":
			database.DB.Model(&models.ScanDirectory{}).Where("path = ?", connection.DownloadRoot).Update("path", t.TempDir())
		}
		hidden, e := s.Get(ctx, view.ID)
		if e != nil || hidden.Files[0].VideoID != nil {
			t.Fatal("ineligible logical reference playable", change, e)
		}
		database.DB.Model(&models.Video{}).Where("id = ?", id).Updates(map[string]any{"path": path, "is_stale": false})
		if change == "scope" {
			database.DB.Model(&models.ScanDirectory{}).Where("1 = 1").Update("path", connection.DownloadRoot)
		}
	}
	if err = videos.DeleteVideo(id, false); err != nil {
		t.Fatal(err)
	}
	if err = s.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	database.DB.Model(&models.Video{}).Count(&count)
	if count != 0 {
		t.Fatal("poll resurrected user deletion")
	}
	removed, err := s.Get(ctx, view.ID)
	if err != nil || removed.Files[0].VideoID != nil {
		t.Fatal("deleted logical video reference still playable", err)
	}

	after, _ = os.ReadFile(path)
	if !bytes.Equal(payload, after) {
		t.Fatal("record deletion changed payload")
	}
}

func btScopedCachePath(t *testing.T, s *BTService, id, digest string) string {
	t.Helper()
	path, err := s.cachePath(context.Background(), id, digest)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
func btScopedCacheDir(t *testing.T, s *BTService, id string) string {
	t.Helper()
	return filepath.Dir(btScopedCachePath(t, s, id, strings.Repeat("0", 64)))
}
func btTaskCacheFile(t *testing.T, s *BTService, id string) string {
	t.Helper()
	task, _, err := btLoadTask(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return btScopedCachePath(t, s, id, task.ManifestDigest)
}

func TestBTTaskCacheCrashCleanupPreservesUncertainAndUnknownFiles(t *testing.T) {
	s, _ := newBTTaskTest(t)
	ctx := context.Background()
	view := btPreviewTest(t, s)
	database.DB.Model(&models.BTTask{}).Where("id = ?", view.ID).Updates(map[string]any{"stage": "uncertain", "unresolved_write": true})
	task, _, _ := btLoadTask(ctx, view.ID)
	orphanID := uuid.NewString()
	raw := []byte("orphan-cache")
	if err := s.writeCache(context.Background(), orphanID, raw); err != nil {
		t.Fatal(err)
	}
	orphan := btScopedCachePath(t, s, orphanID, btDigest(raw))
	temp := filepath.Join(btScopedCacheDir(t, s, view.ID), ".preview-123456789")
	unknown := filepath.Join(btScopedCacheDir(t, s, view.ID), "user-note.txt")
	for _, path := range []string{temp, unknown} {
		os.WriteFile(path, []byte("fixture"), 0600)
	}
	original := btScopedCachePath(t, s, view.ID, task.ManifestDigest)
	old := time.Now().Add(-25 * time.Hour)
	for _, path := range []string{orphan, temp, unknown, original} {
		os.Chtimes(path, old, old)
	}
	if err := s.ExpireDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{orphan, temp} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("crash cache retained")
		}
	}
	for _, path := range []string{unknown, original} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("protected/unknown cache removed", err)
		}
	}
	s.Quiesce(ctx)
}

func TestBTTaskRechecksOwnershipBetweenPriorityAndStartWrites(t *testing.T) {
	for _, boundary := range []int{0, 1} {
		t.Run(fmt.Sprint(boundary), func(t *testing.T) {
			s, r := newBTTaskTest(t)
			ctx := context.Background()
			task := btSubmitFirst(t, s)
			r.afterPriority = func(priority int, row *btFakeRemoteTask) {
				if priority == boundary {
					row.info.Tags = "external-owner"
				}
			}
			if err := s.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			current, _ := s.Get(ctx, task.ID)
			if current.Stage != "conflict" {
				t.Fatal("ownership change accepted", current.Stage)
			}
			want := "priority:0"
			if boundary == 1 {
				want += ",priority:1"
			}
			if strings.Join(r.actions, ",") != want {
				t.Fatal("continued mutating after external ownership change", r.actions)
			}
		})
	}
}

func TestBTTaskUnknownAddNeverReleasesBeforeLateArrival(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	task := btSubmitFirst(t, s)
	r.addErr = errors.New("lost add response")
	s.Poll(ctx)
	late := btRemoteFirst(t, r)
	delete(r.tasks, late.info.Hash)
	current, _ := s.Get(ctx, task.ID)
	if !current.UnresolvedWrite || current.PendingAction != "add" {
		t.Fatal("unknown add not persisted")
	}
	if _, err := s.Control(ctx, BTControlRequest{TaskID: task.ID, ExpectedRevision: current.Revision, Action: "release", Confirmed: true}); !errors.Is(err, ErrBTConflict) {
		t.Fatal("empty query washed unknown add", err)
	}
	current, err := s.Control(ctx, BTControlRequest{TaskID: task.ID, ExpectedRevision: current.Revision, Action: "abandon", Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Control(ctx, BTControlRequest{TaskID: task.ID, ExpectedRevision: current.Revision, Action: "release", Confirmed: true}); !errors.Is(err, ErrBTConflict) {
		t.Fatal("abandoned root released", err)
	}
	r.tasks[late.info.Hash] = late
	s.Poll(ctx)
	current, _ = s.Get(ctx, task.ID)
	if current.Stage != "abandoned" || !current.UnresolvedWrite || len(r.actions) != 0 {
		t.Fatal("late add was resumed or released")
	}
	path := filepath.Join(task.SavePath, filepath.FromSlash(task.Files[0].Path))
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte("x"), 0600)
	policy := NewBTPathPolicy(func(ctx context.Context) (*gorm.DB, error) { return database.DB.WithContext(ctx), nil })
	videos := &VideoService{}
	videos.SetBTPathPolicy(policy)
	if _, err = videos.AddVideo(path); !errors.Is(err, ErrBTPending) {
		t.Fatal("late partial payload escaped root protection", err)
	}
}
func TestBTTaskPriorityCancellationNeverReplays(t *testing.T) {
	for _, priority := range []int{0, 1} {
		t.Run(fmt.Sprint(priority), func(t *testing.T) {
			s, r := newBTTaskTest(t)
			task := btSubmitFirst(t, s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r.afterPriority = func(value int, row *btFakeRemoteTask) {
				if value == priority {
					cancel()
				}
			}
			_ = s.Poll(ctx)
			count := len(r.actions)
			row := btRemoteFirst(t, r)
			row.files[1].Priority = 7
			r.afterPriority = nil
			_ = s.Poll(context.Background())
			_ = s.Poll(context.Background())
			current, _ := s.Get(context.Background(), task.ID)
			if current.Stage != "uncertain" || !current.UnresolvedWrite || current.PendingAction == "" || len(r.actions) != count || row.files[1].Priority != 7 {
				t.Fatal("cancelled priority batch replayed", current, r.actions)
			}
		})
	}
}
func TestBTTaskTwoCoordinatorsOnlyOneSelectionWriter(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	other := NewBTService(s.dataDir, s.deps)
	if _, err := other.Search(ctx, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	view := btSubmitFirst(t, s)
	task, files, err := btLoadTask(ctx, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(task.CanonicalRoot, 0700)
	hash, err := r.QBAddStopped(ctx, btclient.QBAddOptions{Torrent: btTaskFixtureTorrent(), SavePath: task.CanonicalRoot, OwnerTag: task.OwnerTag})
	if err != nil {
		t.Fatal(err)
	}
	if err = database.DB.Model(&models.BTTask{}).Where("id = ?", task.ID).Updates(map[string]any{"stage": "selecting", "qb_hash": hash}).Error; err != nil {
		t.Fatal(err)
	}
	task, files, err = btLoadTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	begin := make(chan struct{})
	results := make(chan error, 2)
	for _, coordinator := range []*BTService{s, other} {
		go func(c *BTService) { copyTask := task; <-begin; results <- c.selectAndStart(ctx, &copyTask, files) }(coordinator)
	}
	close(begin)
	first, second := <-results, <-results
	for _, result := range []error{first, second} {
		if result != nil && !errors.Is(result, ErrBTTaskBusy) && !errors.Is(result, ErrBTConflict) {
			t.Fatal("unexpected competing writer outcome", result)
		}
	}
	expected := []string{"priority:0", "priority:1", "start"}
	if len(r.actions) == 0 || len(r.actions) > len(expected) {
		t.Fatal("missing or duplicated external write", r.actions)
	}
	for i, action := range r.actions {
		if action != expected[i] {
			t.Fatal("external writes are not an exclusive ordered prefix", r.actions)
		}
	}
	current, _, err := btLoadTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	switch current.Stage {
	case "active":
		if len(r.actions) != 3 || current.UnresolvedWrite || current.PendingAction != "" {
			t.Fatal("active without a complete acknowledged sequence", r.actions, current.Stage)
		}
	case "priority_select_ready", "start_ready":
		expectedCount := 1
		if current.Stage == "start_ready" {
			expectedCount = 2
		}
		if len(r.actions) != expectedCount || current.UnresolvedWrite {
			t.Fatal("ready phase does not describe acknowledged prefix", r.actions, current.Stage)
		}
		if err := s.Poll(ctx); err != nil {
			t.Fatal(err)
		}
		current, _, _ = btLoadTask(ctx, task.ID)
		if current.Stage != "active" || strings.Join(r.actions, ",") != "priority:0,priority:1,start" {
			t.Fatal("ready phase replayed an already-sent step", r.actions, current.Stage)
		}
	case "priority_zero_sent", "priority_select_sent", "start_sent":
		if !current.UnresolvedWrite || current.PendingAction != btActionForStage(current.Stage) {
			t.Fatal("sent phase lost pending operation")
		}
		count := len(r.actions)
		for i := 0; i < 2; i++ {
			if err := s.Poll(ctx); err != nil {
				t.Fatal(err)
			}
		}
		current, _, _ = btLoadTask(ctx, task.ID)
		if current.Stage != "uncertain" || !current.UnresolvedWrite || len(r.actions) != count {
			t.Fatal("unacknowledged sent step automatically replayed", r.actions, current.Stage)
		}
	default:
		t.Fatal("competing writers left an invalid persistent phase", current.Stage, r.actions)
	}
}
func TestBTTaskUnknownStopReconfirmsStopAndKeepsHistory(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	task := btSubmitFirst(t, s)
	s.Poll(ctx)
	task, _ = s.Get(ctx, task.ID)
	if _, err := s.Control(ctx, BTControlRequest{TaskID: task.ID, ExpectedRevision: task.Revision, Action: "stop"}); err != nil {
		t.Fatal(err)
	}
	r.startErr = errors.New("stop reply lost")
	s.Poll(ctx)
	r.startErr = nil
	current, _ := s.Get(ctx, task.ID)
	if current.PendingAction != "stop" || !current.UnresolvedWrite {
		t.Fatal("lost original command")
	}
	current, err := s.Control(ctx, BTControlRequest{TaskID: task.ID, ExpectedRevision: current.Revision, Action: "reconcile"})
	if err != nil || current.Stage != "uncertain" {
		t.Fatal("observation cleared stop uncertainty", err)
	}
	before := len(r.actions)
	if _, err = s.Control(ctx, BTControlRequest{TaskID: task.ID, ExpectedRevision: current.Revision, Action: "reconfirm_submit", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	s.Poll(ctx)
	current, _ = s.Get(ctx, task.ID)
	if current.Stage != "active" || !current.UnresolvedWrite || current.RemoteState != "stoppedDL" || len(r.actions) != before {
		t.Fatal("reconfirmation restarted instead of confirming stop", current, r.actions)
	}
	if _, err = s.Control(ctx, BTControlRequest{TaskID: task.ID, ExpectedRevision: current.Revision, Action: "release", Confirmed: true}); !errors.Is(err, ErrBTConflict) {
		t.Fatal("later success erased historical uncertainty", err)
	}
}
func TestBTTaskCancelledDraftReopensSameIntentWithFreshExpiry(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	task := btPreviewTest(t, s)
	original, _, _ := btLoadTask(ctx, task.ID)
	task, err := s.Control(ctx, BTControlRequest{TaskID: task.ID, ExpectedRevision: task.Revision, Action: "cancel_draft"})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	database.DB.Model(&models.BTTask{}).Where("id = ?", task.ID).Update("updated_at", old)
	var parentBefore models.BTConnection
	database.DB.First(&parentBefore, "id = ?", original.ConnectionID)
	reopened := btPreviewTest(t, s)
	record, _, _ := btLoadTask(ctx, task.ID)
	var parentAfter models.BTConnection
	database.DB.First(&parentAfter, "id = ?", original.ConnectionID)
	if reopened.ID != task.ID || reopened.SavePath != task.SavePath || reopened.Stage != "draft" || !record.CreatedAt.Equal(original.CreatedAt) || reopened.ExpiresAt == nil || reopened.ExpiresAt.Before(time.Now().Add(23*time.Hour)) || parentAfter.TaskEpoch != parentBefore.TaskEpoch+1 {
		t.Fatal("draft reopen changed identity or missed parent epoch", reopened)
	}
	if _, err = s.Submit(ctx, BTSubmitRequest{DraftID: reopened.ID, ExpectedRevision: reopened.Revision, SelectedFileIDs: []string{reopened.Files[0].ID}}); err != nil {
		t.Fatal(err)
	}
	s.Poll(ctx)
	if r.adds != 1 {
		t.Fatal("reopened draft was not downloadable")
	}
}
func TestBTTaskAbandonedConnectionCanRetireButCannotReactivate(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	task := btSubmitFirst(t, s)
	r.addErr = errors.New("unknown add")
	s.Poll(ctx)
	current, _ := s.Get(ctx, task.ID)
	if _, err := s.Control(ctx, BTControlRequest{TaskID: task.ID, ExpectedRevision: current.Revision, Action: "abandon", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	old, _, _ := btLoadTask(ctx, task.ID)
	config, _ := s.Connection().Get(ctx)
	next, err := s.Connection().Configure(ctx, BTConnectionRequest{ExpectedRevision: config.Revision, ProwlarrBase: config.ProwlarrBase, QBBase: "http://127.0.0.1:8082", DownloadRoot: config.DownloadRoot, Enabled: true, ProwlarrKey: BTSecretChange{Operation: "keep"}, QBPassword: BTSecretChange{Operation: "keep"}})
	if err != nil {
		t.Fatal(err)
	}
	if next.InstanceID == config.InstanceID {
		t.Fatal("abandoned instance not retired")
	}
	view, err := s.Get(ctx, task.ID)
	if err != nil || view.ReadOnlyReason != "connection_retired" || len(view.Actions) != 0 {
		t.Fatal("retired task offered live actions", view, err)
	}
	if err = s.reactivateTask(ctx, &old, map[string]any{"stage": "queued_add"}); !errors.Is(err, ErrBTConflict) {
		t.Fatal("retired parent admitted intent", err)
	}
	if r.adds != 1 || len(r.actions) != 0 {
		t.Fatal("retirement touched external task")
	}
}
func btConfigFromView(v BTConnectionView) BTConnectionRequest {
	return BTConnectionRequest{ExpectedRevision: v.Revision, ProwlarrBase: v.ProwlarrBase, QBBase: v.QBBase, QBUsername: v.QBUsername, DownloadRoot: v.DownloadRoot, Enabled: v.Enabled, ProwlarrKey: BTSecretChange{Operation: "keep"}, QBPassword: BTSecretChange{Operation: "keep"}}
}
func TestBTTaskFailedConfigurationDrainDoesNotBreakLifecycle(t *testing.T) {
	for _, lifecycle := range []string{"none", "quiesce", "stop"} {
		t.Run(lifecycle, func(t *testing.T) {
			s, r := newBTTaskTest(t)
			ctx := context.Background()
			btSubmitFirst(t, s)
			config, _ := s.Connection().Get(ctx)
			entered, release := make(chan struct{}), make(chan struct{})
			r.beforeAdd = func(context.Context) { close(entered); <-release }
			pollDone := make(chan error, 1)
			go func() { pollDone <- s.Poll(ctx) }()
			<-entered
			configureCtx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
			defer cancel()
			configureDone := make(chan error, 1)
			go func() {
				_, err := s.Connection().Configure(configureCtx, btConfigFromView(config))
				configureDone <- err
			}()
			deadline := time.Now().Add(time.Second)
			for {
				s.mu.Lock()
				closed := !s.accepting
				s.mu.Unlock()
				if closed {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("configure did not drain")
				}
				time.Sleep(time.Millisecond)
			}
			if lifecycle != "none" {
				stopCtx, end := context.WithTimeout(ctx, 10*time.Millisecond)
				if lifecycle == "stop" {
					_ = s.Stop(stopCtx)
				} else {
					_ = s.Quiesce(stopCtx)
				}
				end()
			}
			if err := <-configureDone; err == nil {
				t.Fatal("blocked drain unexpectedly configured")
			}
			close(release)
			<-pollDone
			fresh, err := s.Connection().Get(ctx)
			if err != nil || fresh.Revision != config.Revision {
				t.Fatal("failed configure changed connection", err)
			}
			_, err = s.Search(ctx, "fixture", nil)
			if lifecycle == "none" {
				if err != nil {
					t.Fatal("failed configure left old admission closed", err)
				}
			} else if !errors.Is(err, ErrBTMaintenance) {
				t.Fatal("configuration resume reopened maintenance", err)
			}
			if lifecycle != "none" {
				s.Resume()
				_, err = s.Search(ctx, "fixture", nil)
				if lifecycle == "stop" && !errors.Is(err, ErrBTMaintenance) {
					t.Fatal("resume reopened terminal stop")
				}
				if lifecycle == "quiesce" && err != nil {
					t.Fatal("explicit resume failed", err)
				}
			}
		})
	}
}

func TestBTTaskMetadataPreparationViewOffersSafeCancellation(t *testing.T) {
	for _, stage := range []string{"metadata_adding", "queued_metadata", "metadata_wait", "metadata_ready"} {
		t.Run(stage, func(t *testing.T) {
			s, _ := newBTTaskTest(t)
			ctx := context.Background()
			view := btPreviewTest(t, s)
			database.DB.Model(&models.BTTask{}).Where("id = ?", view.ID).Update("stage", stage)
			current, err := s.Get(ctx, view.ID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, action := range current.Actions {
				found = found || action == "cancel_draft"
			}
			if !found {
				t.Fatal("preparation view has no cancellation", current)
			}
			cancelled, err := s.Control(ctx, BTControlRequest{TaskID: view.ID, ExpectedRevision: current.Revision, Action: "cancel_draft"})
			if err != nil || cancelled.Stage != "cancel_pending" {
				t.Fatal("cancel could not be persisted", err)
			}
		})
	}
}

func btMetadataPreview(t *testing.T, s *BTService, r *btTaskFakeRemote) (BTTaskView, string) {
	t.Helper()
	data := btTaskFixtureTorrent()
	m, err := btmanifest.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	r.metadataData = data
	source := &btTaskFakeSearch{magnet: "magnet:?xt=urn:btih:" + m.InfoHashV1}
	s.deps.SearchFactory = func(*btclient.Client) BTSearchSource { return source }
	found, err := s.Search(context.Background(), "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	token := found.Resources[0].Token
	view, err := s.Preview(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return view, token
}
func TestBTTaskMetadataCachePointerSurvivesPreCommitCrash(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	view, _ := btMetadataPreview(t, s, r)
	s.Poll(ctx)
	before, _, err := btLoadTask(ctx, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldData, err := s.readCache(context.Background(), before)
	if err != nil {
		t.Fatal(err)
	}
	exported := btTaskFixtureTorrent()
	newDigest := btDigest(exported)
	// Exact crash boundary: a complete new cache exists, but no DB pointer CAS ran.
	if err = s.writeCache(context.Background(), view.ID, exported); err != nil {
		t.Fatal(err)
	}
	oldPath := btScopedCachePath(t, s, view.ID, before.ManifestDigest)
	newPath := btScopedCachePath(t, s, view.ID, newDigest)
	again, err := s.readCache(context.Background(), before)
	if err != nil || !bytes.Equal(again, oldData) {
		t.Fatal("new cache overwrote old DB pointer", err)
	}
	for _, path := range []string{oldPath, newPath} {
		info, e := os.Stat(path)
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatal("cache mode", e)
		}
	}
	old := time.Now().Add(-25 * time.Hour)
	os.Chtimes(oldPath, old, old)
	os.Chtimes(newPath, old, old)
	if err = s.expireOrphanCaches(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(newPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unreferenced crash version not collected")
	}
	if _, err = os.Stat(oldPath); err != nil {
		t.Fatal("current DB pointer collected", err)
	}
	if err = s.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	current, _, _ := btLoadTask(ctx, view.ID)
	if current.ManifestDigest != newDigest || current.Stage != "metadata_ready" {
		t.Fatal("metadata could not complete after crash")
	}
	if _, err = s.readCache(context.Background(), current); err != nil {
		t.Fatal("new committed pointer is dangling", err)
	}
	if _, err = os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("known old version retained after successful pointer CAS")
	}
}
func TestBTTaskUnknownMetadataReconfirmationNeverStartsDefaultSelection(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	view, _ := btMetadataPreview(t, s, r)
	r.addErr = errors.New("metadata add response lost")
	s.Poll(ctx)
	r.addErr = nil
	current, _ := s.Get(ctx, view.ID)
	if current.PendingAction != "metadata" || !current.UnresolvedWrite {
		t.Fatal("metadata operation lost")
	}
	if _, err := s.Control(ctx, BTControlRequest{TaskID: view.ID, ExpectedRevision: current.Revision, Action: "reconfirm_submit", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	s.Poll(ctx)
	s.Poll(ctx)
	ready, err := s.Get(ctx, view.ID)
	if err != nil || ready.Stage != "metadata_ready" || len(ready.Files) == 0 || len(r.actions) != 0 {
		t.Fatal("metadata retry started defaults", ready, err)
	}
	task, _, _ := btLoadTask(ctx, view.ID)
	if task.SelectionDigest != "" || task.PendingAction != "metadata" {
		t.Fatal("preparation fabricated confirmation")
	}
	magnet, err := s.readMetadataCache(context.Background(), task)
	if err != nil {
		t.Fatal("unresolved original magnet was discarded", err)
	}
	originalPath := btScopedCachePath(t, s, task.ID, btDigest(magnet))
	old := time.Now().Add(-25 * time.Hour)
	os.Chtimes(originalPath, old, old)
	if err = s.expireOrphanCaches(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(originalPath); err != nil {
		t.Fatal("GC deleted unresolved operation dependency")
	}
	// A subsequent restore/unknown state now has a full file list. Its pending
	// operation is still metadata, not the default Suggested files' download.
	database.DB.Model(&models.BTTask{}).Where("id = ?", task.ID).Update("stage", "uncertain")
	current, _ = s.Get(ctx, task.ID)
	if _, err = s.Control(ctx, BTControlRequest{TaskID: task.ID, ExpectedRevision: current.Revision, Action: "reconfirm_submit", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	s.Poll(ctx)
	s.Poll(ctx)
	ready, _ = s.Get(ctx, task.ID)
	if ready.Stage != "metadata_ready" || r.adds != 1 || len(r.actions) != 0 {
		t.Fatal("full metadata caused implicit payload start", ready, r.actions)
	}
	// Explicit abandonment clears versions; a new validated magnet preview can
	// restore only the same metadata intent without inventing torrent bytes.
	ready, err = s.Control(ctx, BTControlRequest{TaskID: task.ID, ExpectedRevision: ready.Revision, Action: "abandon", Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	found, err := s.Search(ctx, "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	ready, err = s.Preview(ctx, found.Resources[0].Token)
	if err != nil {
		t.Fatal("magnet re-preview could not restore original operation", err)
	}
	if _, err = s.Control(ctx, BTControlRequest{TaskID: task.ID, ExpectedRevision: ready.Revision, Action: "reconfirm_submit", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	s.Poll(ctx)
	s.Poll(ctx)
	ready, _ = s.Get(ctx, task.ID)
	if len(r.actions) != 0 {
		t.Fatal("metadata cache recovery started payload")
	}
	if _, err = s.Submit(ctx, BTSubmitRequest{DraftID: task.ID, ExpectedRevision: ready.Revision, SelectedFileIDs: []string{ready.Files[0].ID}}); err != nil {
		t.Fatal(err)
	}
	s.Poll(ctx)
	if r.adds != 1 || strings.Join(r.actions, ",") != "priority:0,priority:1,start" {
		t.Fatal("explicit submit did not start exact selection", r.actions)
	}
}
func TestBTTaskSuggestionsNeverGrantWriteOrImportPermission(t *testing.T) {
	for _, stage := range []string{"selecting", "start_ready", "active"} {
		t.Run(stage, func(t *testing.T) {
			s, r := newBTTaskTest(t)
			ctx := context.Background()
			view := btPreviewTest(t, s)
			task, files, _ := btLoadTask(ctx, view.ID)
			os.MkdirAll(task.CanonicalRoot, 0700)
			hash, err := r.QBAddStopped(ctx, btclient.QBAddOptions{Torrent: btTaskFixtureTorrent(), SavePath: task.CanonicalRoot, OwnerTag: task.OwnerTag})
			if err != nil {
				t.Fatal(err)
			}
			row := btRemoteFirst(t, r)
			for i := range row.files {
				row.files[i].Priority = 0
				for _, file := range files {
					if file.RelativePath == row.files[i].Name && file.Selected {
						row.files[i].Priority = 1
						row.files[i].Progress = 1
					}
				}
			}
			database.DB.Model(&models.BTTask{}).Where("id = ?", task.ID).Updates(map[string]any{"stage": stage, "qb_hash": hash, "remote_state": row.info.State})
			s.Poll(ctx)
			if len(r.actions) != 0 || len(r.importWrites) != 0 {
				t.Fatal("default selection was treated as user confirmation", r.actions, r.importWrites)
			}
		})
	}
}
func TestBTTaskRestoreReconfirmationIsReadOnlyOrReturnsToChoice(t *testing.T) {
	for _, state := range []string{"downloading", "stoppedDL"} {
		t.Run(state, func(t *testing.T) {
			s, r := newBTTaskTest(t)
			ctx := context.Background()
			view := btSubmitFirst(t, s)
			s.Poll(ctx)
			row := btRemoteFirst(t, r)
			row.info.State = state
			database.DB.Model(&models.BTTask{}).Where("id = ?", view.ID).Updates(map[string]any{"stage": "uncertain", "pending_action": "restore", "unresolved_write": true})
			current, _ := s.Get(ctx, view.ID)
			count := len(r.actions)
			restored, err := s.Control(ctx, BTControlRequest{TaskID: view.ID, ExpectedRevision: current.Revision, Action: "reconfirm_submit", Confirmed: true})
			if err != nil || restored.Stage != "active" || restored.RemoteState != state || !restored.UnresolvedWrite || len(r.actions) != count {
				t.Fatal("restore rewrote remote state", restored, err)
			}
		})
	}
	t.Run("unsubmitted_without_cache", func(t *testing.T) {
		s, r := newBTTaskTest(t)
		ctx := context.Background()
		view := btPreviewTest(t, s)
		s.removeTaskCaches(context.Background(), view.ID)
		database.DB.Model(&models.BTTask{}).Where("id = ?", view.ID).Updates(map[string]any{"stage": "uncertain", "pending_action": "restore", "unresolved_write": true})
		current, _ := s.Get(ctx, view.ID)
		if _, err := s.Control(ctx, BTControlRequest{TaskID: view.ID, ExpectedRevision: current.Revision, Action: "reconfirm_submit", Confirmed: true}); !errors.Is(err, ErrBTResultExpired) {
			t.Fatal("restore invented missing metadata", err)
		}
		prepared := btPreviewTest(t, s)
		if prepared.Stage != "draft" || r.adds != 0 || len(r.actions) != 0 {
			t.Fatal("re-search did not return to file choice", prepared)
		}
		task, _, _ := btLoadTask(ctx, view.ID)
		if task.SelectionDigest != "" || !task.UnresolvedWrite {
			t.Fatal("restore fabricated submit or cleared history")
		}
	})
}
func TestBTTaskRecoveryIgnoresFutureAndEqualCreatedAt(t *testing.T) {
	for _, mode := range []string{"restart", "resume"} {
		t.Run(mode, func(t *testing.T) {
			s, r := newBTTaskTest(t)
			ctx := context.Background()
			view := btSubmitFirst(t, s)
			future := time.Now().Add(48 * time.Hour)
			database.DB.Model(&models.BTTask{}).Where("id = ?", view.ID).Update("created_at", future)
			original, _, _ := btLoadTask(ctx, view.ID)
			copyTask := original
			copyTask.ID = uuid.NewString()
			copyTask.IntentKey = copyTask.ID
			copyTask.OwnerTag = copyTask.ID
			copyTask.InfoHashV1 = nil
			copyTask.InfoHashV2 = nil
			copyTask.RootKey = copyTask.ID
			copyTask.SaveRoot = filepath.Join(original.SaveRoot, copyTask.ID)
			copyTask.CanonicalRoot = filepath.Join(original.CanonicalRoot, copyTask.ID)
			if err := database.DB.Omit("Connection").Create(&copyTask).Error; err != nil {
				t.Fatal(err)
			}
			if mode == "restart" {
				s = NewBTService(s.dataDir, s.deps)
			} else {
				s.Quiesce(ctx)
				s.Resume()
			}
			if err := s.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{view.ID, copyTask.ID} {
				current, _ := s.Get(ctx, id)
				if current.Stage != "uncertain" || !current.UnresolvedWrite {
					t.Fatal("timestamp used as execution ownership", current)
				}
			}
			if r.adds != 0 || len(r.actions) != 0 {
				t.Fatal("future-dated command replayed")
			}
		})
	}
}
func TestBTTaskLocalAbandonDoesNotRequireCredentialsOrEnabledClient(t *testing.T) {
	for _, failure := range []string{"offline", "credentials", "disabled"} {
		t.Run(failure, func(t *testing.T) {
			s, r := newBTTaskTest(t)
			ctx := context.Background()
			view := btSubmitFirst(t, s)
			database.DB.Model(&models.BTTask{}).Where("id = ?", view.ID).Updates(map[string]any{"stage": "conflict", "pending_action": "add"})
			originalFactory := s.connection.credentialFactory
			switch failure {
			case "offline":
				r.profileErr = btclient.ErrUnavailable
			case "credentials":
				s.connection.credentialFactory = func(context.Context, *gorm.DB) (*BTCredentialManager, error) { return nil, ErrBTCredentials }
			case "disabled":
				database.DB.Model(&models.BTConnection{}).Where("slot = ?", "local").Update("enabled", false)
			}
			s.invalidate(0)
			current, _ := s.Get(ctx, view.ID)
			offered := false
			for _, action := range current.Actions {
				offered = offered || action == "abandon"
			}
			if !offered {
				t.Fatal("local exit hidden by remote failure")
			}
			abandoned, err := s.Control(ctx, BTControlRequest{TaskID: view.ID, ExpectedRevision: current.Revision, Action: "abandon", Confirmed: true})
			if err != nil || abandoned.Stage != "abandoned" {
				t.Fatal("local abandon depended on old service", err)
			}
			s.connection.credentialFactory = originalFactory
			config, err := s.Connection().Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			request := btConfigFromView(config)
			request.QBBase = "http://127.0.0.1:8082"
			request.Enabled = true
			if _, err = s.Connection().Configure(ctx, request); err != nil {
				t.Fatal("abandoned task prevented retirement", err)
			}
			if r.adds != 0 || len(r.actions) != 0 {
				t.Fatal("local exit touched remote")
			}
		})
	}
}

func TestBTTaskEveryWriteClaimRequiresCurrentEnabledConnection(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	view := btSubmitFirst(t, s)
	r.afterPriority = func(priority int, row *btFakeRemoteTask) {
		if priority == 0 {
			if err := database.DB.Model(&models.BTConnection{}).Where("slot = ?", "local").Updates(map[string]any{"enabled": false, "revision": gorm.Expr("revision + 1")}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.actions, ",") != "priority:0" {
		t.Fatal("disabled/revised connection admitted later writes", r.actions)
	}
	task, _, _ := btLoadTask(ctx, view.ID)
	if task.Stage != "priority_select_ready" {
		t.Fatal("acknowledged step was not preserved", task.Stage)
	}
}

func TestBTTaskObservationKeepsCommandConfirmationValid(t *testing.T) {
	t.Run("stop", func(t *testing.T) {
		s, _ := newBTTaskTest(t)
		ctx := context.Background()
		view := btSubmitFirst(t, s)
		s.Poll(ctx)
		before, _ := s.Get(ctx, view.ID)
		s.Poll(ctx)
		if _, err := s.Control(ctx, BTControlRequest{TaskID: view.ID, ExpectedRevision: before.Revision, Action: "stop"}); err != nil {
			t.Fatal("progress observation invalidated stop confirmation", err)
		}
	})
	t.Run("release", func(t *testing.T) {
		s, r := newBTTaskTest(t)
		ctx := context.Background()
		view := btSubmitFirst(t, s)
		s.Poll(ctx)
		row := btRemoteFirst(t, r)
		row.files[0].Progress = 1
		row.info.State = "stoppedUP"
		path := filepath.Join(view.SavePath, filepath.FromSlash(view.Files[0].Path))
		os.MkdirAll(filepath.Dir(path), 0700)
		os.WriteFile(path, []byte("1234"), 0600)
		s.Poll(ctx)
		before, _ := s.Get(ctx, view.ID)
		for i := 0; i < 3; i++ {
			s.Poll(ctx)
		}
		released, err := s.Control(ctx, BTControlRequest{TaskID: view.ID, ExpectedRevision: before.Revision, Action: "release", Confirmed: true})
		if err != nil || released.Stage != "released" {
			t.Fatal("completed polling invalidated release confirmation", err)
		}
	})
	for _, stage := range []string{"uncertain", "abandoned"} {
		t.Run(stage, func(t *testing.T) {
			s, r := newBTTaskTest(t)
			ctx := context.Background()
			view := btSubmitFirst(t, s)
			r.addErr = errors.New("lost response")
			s.Poll(ctx)
			before, _ := s.Get(ctx, view.ID)
			if stage == "abandoned" {
				var err error
				before, err = s.Control(ctx, BTControlRequest{TaskID: view.ID, ExpectedRevision: before.Revision, Action: "abandon", Confirmed: true})
				if err != nil {
					t.Fatal(err)
				}
			}
			s.Poll(ctx)
			after, _ := s.Get(ctx, view.ID)
			if before.Revision != after.Revision || after.Stage != stage {
				t.Fatal("read-only uncertain observation changed command version", before.Revision, after.Revision)
			}
		})
	}
}
func TestBTTaskTwoObserversCASWithoutChangingCommandRevision(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	view := btSubmitFirst(t, s)
	s.Poll(ctx)
	task, files, _ := btLoadTask(ctx, view.ID)
	r.importWrites = nil
	begin := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { copyTask := task; <-begin; results <- s.observeFiles(ctx, &copyTask, files) }()
	}
	close(begin)
	a, b := <-results, <-results
	if (a == nil) == (b == nil) {
		t.Fatal("observation CAS did not select one snapshot", a, b)
	}
	current, observed, _ := btLoadTask(ctx, view.ID)
	if current.Revision != task.Revision || current.ObservationEpoch != task.ObservationEpoch+1 || observed[0].EligibilityRevision != current.ObservationEpoch {
		t.Fatal("observation changed command version or reused eligibility", current.Revision, current.ObservationEpoch, observed[0].EligibilityRevision)
	}
}
func TestBTTaskNewObservationInvalidatesOldImporterEligibilityOnly(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	database.DB.Create(&models.Settings{})
	connection, _ := s.Connection().Get(ctx)
	database.DB.Create(&models.ScanDirectory{Path: connection.DownloadRoot})
	view := btSubmitFirst(t, s)
	s.Poll(ctx)
	row := btRemoteFirst(t, r)
	row.files[0].Progress = 1
	row.info.State = "stalledUP"
	path := filepath.Join(view.SavePath, filepath.FromSlash(view.Files[0].Path))
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte("1234"), 0600)
	s.Poll(ctx)
	before, files, _ := btLoadTask(ctx, view.ID)
	policy := NewBTPathPolicy(func(ctx context.Context) (*gorm.DB, error) { return database.DB.WithContext(ctx), nil })
	videos := &VideoService{}
	videos.SetBTPathPolicy(policy)
	calls := 0
	verifier := BTImportVerifyFunc(func(ctx context.Context, task models.BTTask, file models.BTFile) error {
		calls++
		if calls == 1 {
			newTask, newFiles, e := btLoadTask(ctx, task.ID)
			if e != nil {
				return e
			}
			if e = s.observeFiles(ctx, &newTask, newFiles); e != nil {
				return e
			}
		}
		return s.VerifyBTImport(ctx, task, file)
	})
	if _, err := NewBTImporter(videos, policy, verifier).Import(ctx, before.ID, files[0].ManifestFileID); err == nil {
		t.Fatal("old eligibility imported after newer snapshot")
	}
	after, current, _ := btLoadTask(ctx, before.ID)
	var count int64
	database.DB.Model(&models.Video{}).Count(&count)
	if after.Revision != before.Revision || current[0].EligibilityRevision == files[0].EligibilityRevision || count != 0 {
		t.Fatal("eligibility guard depended on command revision or published stale import", after.Revision, before.Revision, count)
	}
}

func TestBTTaskObservationEpochPreservesLegacyEligibilityAndRejectsOverflow(t *testing.T) {
	if next, err := btNextObservationEpoch(models.BTTask{ObservationEpoch: 2}, []models.BTFile{{EligibilityRevision: 70}}); err != nil || next != 71 {
		t.Fatal("legacy eligibility counter went backwards", next, err)
	}
	for _, test := range []struct {
		task  models.BTTask
		files []models.BTFile
	}{{task: models.BTTask{ObservationEpoch: ^uint64(0)}}, {files: []models.BTFile{{EligibilityRevision: ^uint64(0)}}}} {
		if next, err := btNextObservationEpoch(test.task, test.files); err == nil || next != 0 {
			t.Fatal("observation epoch wrapped into a stale grant", next, err)
		}
	}
}

type btDelayedObservationRemote struct {
	BTRemote
	first            atomic.Bool
	started, release chan struct{}
	missing          bool
}

func (r *btDelayedObservationRemote) QBTorrents(ctx context.Context, hash string) ([]btclient.QBTorrent, error) {
	if r.first.CompareAndSwap(true, false) {
		close(r.started)
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if r.missing {
			return nil, nil
		}
		return nil, btclient.ErrUnavailable
	}
	return r.BTRemote.QBTorrents(ctx, hash)
}
func TestBTTaskLateObservationFailureCannotReplaceNewSuccess(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			s, r := newBTTaskTest(t)
			ctx := context.Background()
			view := btSubmitFirst(t, s)
			s.Poll(ctx)
			s.Poll(ctx)
			old, files, _ := btLoadTask(ctx, view.ID)
			delayed := &btDelayedObservationRemote{BTRemote: r, started: make(chan struct{}), release: make(chan struct{}), missing: missing}
			delayed.first.Store(true)
			s.mu.Lock()
			s.remote = delayed
			s.mu.Unlock()
			failure := make(chan error, 1)
			go func() { copyTask := old; failure <- s.observeFiles(ctx, &copyTask, files) }()
			<-delayed.started
			newer, newFiles, _ := btLoadTask(ctx, view.ID)
			if err := s.observeFiles(ctx, &newer, newFiles); err != nil {
				t.Fatal(err)
			}
			close(delayed.release)
			if err := <-failure; !errors.Is(err, ErrBTConflict) {
				t.Fatal("late observation failure was not fenced", err)
			}
			current, observed, _ := btLoadTask(ctx, view.ID)
			if current.Stage != "active" || current.Revision != old.Revision || current.ObservationEpoch != newer.ObservationEpoch || observed[0].EligibilityRevision != newer.ObservationEpoch {
				t.Fatal("old failure replaced newer qualified success")
			}
		})
	}
}
func TestBTTaskCacheCancelAndReopenShareCrossInstanceGuard(t *testing.T) {
	s, _ := newBTTaskTest(t)
	ctx := context.Background()
	view := btPreviewTest(t, s)
	other := NewBTService(s.dataDir, s.deps)
	search, err := other.Search(ctx, "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	token := search.Resources[0].Token
	db := database.DB
	committed, allowCleanup := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer func() {
		select {
		case <-allowCleanup:
		default:
			close(allowCleanup)
		}
	}()
	s.deps.Changed = func(id string, revision uint64) {
		if id != view.ID {
			return
		}
		var task models.BTTask
		if db.First(&task, "id = ?", id).Error == nil && task.Stage == "cancelled" {
			once.Do(func() { close(committed); <-allowCleanup })
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Control(ctx, BTControlRequest{TaskID: view.ID, ExpectedRevision: view.Revision, Action: "cancel_draft"})
		done <- err
	}()
	select {
	case <-committed:
	case err := <-done:
		t.Fatal("cancel did not reach committed boundary", err)
	case <-time.After(3 * time.Second):
		t.Fatal("cancel boundary timed out")
	}
	var cancelled models.BTTask
	if err = db.First(&cancelled, "id = ?", view.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = other.Preview(ctx, token); !errors.Is(err, ErrBTTaskBusy) {
		t.Fatal("reopen crossed pending cleanup", err)
	}
	close(allowCleanup)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	reopened, err := other.Preview(ctx, token)
	if err != nil || reopened.Stage != "draft" {
		t.Fatal("reopen after cleanup failed", err)
	}
	if err = s.cleanupTaskCaches(ctx, cancelled); !errors.Is(err, ErrBTConflict) {
		t.Fatal("late cancelled cleanup was not revision fenced", err)
	}
	current, _, _ := btLoadTask(ctx, view.ID)
	if _, err = other.readCache(ctx, current); err != nil {
		t.Fatal("late cleanup deleted reopened current version", err)
	}
}
func TestBTTaskCacheGCAndMetadataPublishShareGuard(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	other := NewBTService(s.dataDir, s.deps)
	if _, err := other.Search(ctx, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	view, _ := btMetadataPreview(t, s, r)
	s.Poll(ctx)
	task, _, _ := btLoadTask(ctx, view.ID)
	exported := btTaskFixtureTorrent()
	if err := s.writeCache(ctx, view.ID, exported); err != nil {
		t.Fatal(err)
	}
	path := btScopedCachePath(t, s, view.ID, btDigest(exported))
	old := time.Now().Add(-25 * time.Hour)
	os.Chtimes(path, old, old)
	checked, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	first.Store(true)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	name := "test:bt_cache_gc_boundary"
	if err := database.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		selected := false
		for _, field := range tx.Statement.Selects {
			selected = selected || field == "manifest_digest"
		}
		if tx.Statement.Table == "bt_tasks" && selected && first.CompareAndSwap(true, false) {
			close(checked)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Callback().Query().Remove(name) })
	done := make(chan error, 1)
	go func() { done <- s.expireOrphanCaches(ctx) }()
	select {
	case <-checked:
	case err := <-done:
		t.Fatal("GC did not inspect references", err)
	case <-time.After(3 * time.Second):
		t.Fatal("GC reference boundary timed out")
	}
	if err := other.finishMetadata(ctx, &task); !errors.Is(err, ErrBTTaskBusy) {
		t.Fatal("pointer publisher crossed GC reference check", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := other.finishMetadata(ctx, &task); err != nil {
		t.Fatal("publisher could not restore collected orphan version", err)
	}
	current, _, _ := btLoadTask(ctx, view.ID)
	if _, err := other.readCache(ctx, current); err != nil {
		t.Fatal("GC left current pointer dangling", err)
	}
}
func TestBTTaskCacheDatabaseScopesKeepCopiedUUIDsIndependent(t *testing.T) {
	s, _ := newBTTaskTest(t)
	ctx := context.Background()
	view := btPreviewTest(t, s)
	dbA := database.DB
	taskA, _, _ := btLoadTask(ctx, view.ID)
	var connection models.BTConnection
	dbA.First(&connection, "id = ?", taskA.ConnectionID)
	dbB := dbtest.Open(t)
	if err := dbB.Create(&connection).Error; err != nil {
		t.Fatal(err)
	}
	taskB := taskA
	payloadB := []byte("different scoped cache")
	taskB.ManifestDigest = btDigest(payloadB)
	if err := dbB.Omit("Connection").Create(&taskB).Error; err != nil {
		t.Fatal(err)
	}
	pathA := btScopedCachePath(t, s, taskA.ID, taskA.ManifestDigest)
	database.DB = dbB
	if err := s.writeCache(ctx, taskB.ID, payloadB); err != nil {
		t.Fatal(err)
	}
	pathB := btScopedCachePath(t, s, taskB.ID, taskB.ManifestDigest)
	if pathA == pathB || filepath.Dir(filepath.Dir(pathA)) == filepath.Dir(filepath.Dir(pathB)) {
		t.Fatal("copied UUID reused another DB's cache scope")
	}
	database.DB = dbA
	if err := s.expireOrphanCaches(ctx); err != nil {
		t.Fatal(err)
	}
	scopeA := s.cacheSweepScope
	if _, err := s.Control(ctx, BTControlRequest{TaskID: taskA.ID, ExpectedRevision: taskA.Revision, Action: "cancel_draft"}); err != nil {
		t.Fatal(err)
	}
	database.DB = dbB
	if data, err := s.readCache(ctx, taskB); err != nil || !bytes.Equal(data, payloadB) {
		t.Fatal("another DB's cleanup removed current cache", err)
	}
	if err := s.expireOrphanCaches(ctx); err != nil {
		t.Fatal(err)
	}
	if s.cacheSweepScope == scopeA {
		t.Fatal("GC reused old scope cursor")
	}
	if _, err := os.Stat(pathB); err != nil {
		t.Fatal(err)
	}
	database.DB = dbA
	s.Quiesce(ctx)
}
func TestBTTaskCacheLeasePinsDatabaseAndRejectsMaintenanceBypass(t *testing.T) {
	s, _ := newBTTaskTest(t)
	ctx := context.Background()
	view := btPreviewTest(t, s)
	dbA := database.DB
	dbB := dbtest.Open(t)
	task, _, _ := btLoadTask(ctx, view.ID)
	var connection models.BTConnection
	dbA.First(&connection, "id = ?", task.ConnectionID)
	dbB.Create(&connection)
	copyTask := task
	dbB.Omit("Connection").Create(&copyTask)
	if err := s.withTaskCache(ctx, task.ID, func(scoped context.Context, db *gorm.DB, files *btCacheFiles) error {
		// Even a direct test-only global swap cannot redirect the leased callback.
		database.DB = dbB
		return btTaskTransaction(scoped, func(tx *gorm.DB) error {
			return tx.Model(&models.BTTask{}).Where("id = ?", task.ID).Update("title", "pinned-A").Error
		})
	}); err != nil {
		t.Fatal(err)
	}
	var a, b models.BTTask
	dbA.First(&a, "id = ?", task.ID)
	dbB.First(&b, "id = ?", task.ID)
	if a.Title != "pinned-A" || b.Title == "pinned-A" {
		t.Fatal("cache lease wrote a newly swapped DB")
	}
	database.DB = dbA
	// Production schema installation also installs fail-fast GORM maintenance guards.
	if err := database.ApplySchema(dbA); err != nil {
		t.Fatal(err)
	}
	entered, proceed := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.withTaskCache(ctx, task.ID, func(scoped context.Context, db *gorm.DB, _ *btCacheFiles) error {
			close(entered)
			<-proceed
			if err := btTaskTransaction(scoped, func(tx *gorm.DB) error {
				return tx.Model(&models.BTTask{}).Where("id = ?", task.ID).Update("title", "must-not-write").Error
			}); !errors.Is(err, database.ErrMaintenance) {
				return fmt.Errorf("lease bypassed maintenance: %w", err)
			}
			return nil
		})
	}()
	<-entered
	started := database.MaintenanceStarted()
	maintenance := make(chan func(), 1)
	released := false
	defer func() {
		select {
		case <-proceed:
		default:
			close(proceed)
		}
		if !released {
			select {
			case release := <-maintenance:
				release()
			case <-time.After(3 * time.Second):
				t.Error("maintenance lease did not finish")
			}
		}
	}()
	go func() { maintenance <- database.BeginMaintenance() }()
	<-started
	select {
	case release := <-maintenance:
		release()
		released = true
		t.Fatal("maintenance finished before cache lease")
	default:
	}
	close(proceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	release := <-maintenance
	release()
	released = true
	dbA.First(&a, "id = ?", task.ID)
	if a.Title == "must-not-write" {
		t.Fatal("cache operation bypassed maintenance fence")
	}
}
func TestBTTaskMismatchedSelectionDigestCannotWrite(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	view := btSubmitFirst(t, s)
	if err := database.DB.Model(&models.BTFile{}).Where("task_id = ? AND manifest_file_id = ?", view.ID, view.Files[1].ID).Update("selected", true).Error; err != nil {
		t.Fatal(err)
	}
	s.Poll(ctx)
	if r.adds != 0 || len(r.actions) != 0 {
		t.Fatal("nonempty but mismatched selection digest authorized writes")
	}
	current, _ := s.Get(ctx, view.ID)
	if current.ErrorCode != "selection_invalid" {
		t.Fatal("invalid selection binding was hidden")
	}
}

func TestBTTaskCacheLeaseCannotEscapeOrNestAnotherTask(t *testing.T) {
	s, _ := newBTTaskTest(t)
	ctx := context.Background()
	view := btPreviewTest(t, s)
	var escaped context.Context
	if err := s.withTaskCache(ctx, view.ID, func(scoped context.Context, _ *gorm.DB, _ *btCacheFiles) error {
		escaped = scoped
		if err := s.withTaskCache(scoped, uuid.NewString(), func(context.Context, *gorm.DB, *btCacheFiles) error { return nil }); !errors.Is(err, ErrBTConflict) {
			return errors.New("nested a different task lock")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.writeCache(escaped, view.ID, []byte("late")); !errors.Is(err, ErrBTConflict) {
		t.Fatal("escaped context reused an unlocked database scope", err)
	}
}

func TestBTTaskStaleSelectionClaimRejectedBeforeRemoteWrite(t *testing.T) {
	s, r := newBTTaskTest(t)
	ctx := context.Background()
	other := NewBTService(s.dataDir, s.deps)
	if _, err := other.Search(ctx, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	view := btSubmitFirst(t, s)
	task, files, _ := btLoadTask(ctx, view.ID)
	os.MkdirAll(task.CanonicalRoot, 0700)
	hash, err := r.QBAddStopped(ctx, btclient.QBAddOptions{Torrent: btTaskFixtureTorrent(), SavePath: task.CanonicalRoot, OwnerTag: task.OwnerTag})
	if err != nil {
		t.Fatal(err)
	}
	database.DB.Model(&models.BTTask{}).Where("id = ?", task.ID).Updates(map[string]any{"stage": "selecting", "qb_hash": hash})
	task, files, _ = btLoadTask(ctx, task.ID)
	claimed, continueWriter := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer func() {
		select {
		case <-continueWriter:
		default:
			close(continueWriter)
		}
	}()
	s.deps.Changed = func(id string, revision uint64) {
		if id != task.ID {
			return
		}
		var current models.BTTask
		if database.DB.First(&current, "id = ?", id).Error == nil && current.Stage == "priority_zero_sent" {
			once.Do(func() { close(claimed); <-continueWriter })
		}
	}
	winner := make(chan error, 1)
	go func() { copyTask := task; winner <- s.selectAndStart(ctx, &copyTask, files) }()
	select {
	case <-claimed:
	case err := <-winner:
		t.Fatal("claim owner did not pause after commit", err)
	case <-time.After(3 * time.Second):
		t.Fatal("claim boundary timed out")
	}
	// The OS guard is released by claimWrite before Changed. This actor reaches
	// the SQL CAS instead of being rejected merely because a file lock is busy.
	stale := task
	staleErr := other.selectAndStart(ctx, &stale, files)
	beforeResume := append([]string(nil), r.actions...)
	close(continueWriter)
	winnerErr := <-winner
	if !errors.Is(staleErr, ErrBTConflict) || len(beforeResume) != 0 {
		t.Fatal("stale selector crossed persisted claim", staleErr, beforeResume)
	}
	if winnerErr != nil || strings.Join(r.actions, ",") != "priority:0,priority:1,start" {
		t.Fatal("claim owner did not execute the only write sequence", winnerErr, r.actions)
	}
	current, _, _ := btLoadTask(ctx, task.ID)
	if current.Stage != "active" || current.UnresolvedWrite {
		t.Fatal("acknowledged owner left wrong state", current.Stage)
	}
}

type btBlockedPublicationStore struct {
	BTSecretStore
	entered, release chan struct{}
	once             sync.Once
}

func (s *btBlockedPublicationStore) Put(ref string, value []byte) error {
	s.once.Do(func() { close(s.entered); <-s.release })
	return s.BTSecretStore.Put(ref, value)
}
func TestBTTaskMaintenanceResumeCannotClearConfigurationPause(t *testing.T) {
	for _, outcome := range []string{"maintenance_aborted", "still_quiesced", "stopped"} {
		t.Run(outcome, func(t *testing.T) {
			s, _ := newBTTaskTest(t)
			ctx := context.Background()
			if _, err := s.Search(ctx, "fixture", nil); err != nil {
				t.Fatal(err)
			}
			view, err := s.Connection().Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			manager, err := s.connection.credentialFactory(ctx, database.DB)
			if err != nil {
				t.Fatal(err)
			}
			blocked := &btBlockedPublicationStore{BTSecretStore: manager.store, entered: make(chan struct{}), release: make(chan struct{})}
			s.connection.credentialFactory = func(context.Context, *gorm.DB) (*BTCredentialManager, error) {
				return NewBTCredentialManager(blocked, manager.locker), nil
			}
			request := btConfigFromView(view)
			request.ProwlarrKey = BTSecretChange{Operation: "replace", Value: "replacement-test-key"}
			done := make(chan error, 1)
			go func() { _, err := s.Connection().Configure(ctx, request); done <- err }()
			defer func() {
				select {
				case <-blocked.release:
				default:
					close(blocked.release)
				}
			}()
			select {
			case <-blocked.entered:
			case err := <-done:
				t.Fatal("configuration did not reach publication", err)
			case <-time.After(3 * time.Second):
				t.Fatal("configuration publication timed out")
			}
			if err = s.Quiesce(ctx); err != nil {
				t.Fatal(err)
			}
			if outcome == "stopped" {
				if err = s.Stop(ctx); err != nil {
					t.Fatal(err)
				}
			} else if outcome == "maintenance_aborted" {
				// Maintenance failed after Quiesce and before the DB fence. Its Resume is
				// not allowed to remove the still-running configuration's pause reason.
				s.Resume()
			}
			_, leave, enterErr := s.enter(ctx)
			if leave != nil {
				leave()
			}
			if !errors.Is(enterErr, ErrBTMaintenance) {
				t.Error("maintenance Resume reopened during secret publication", enterErr)
			}
			close(blocked.release)
			if err = <-done; err != nil {
				t.Fatal("configuration failed without a DB maintenance fence", err)
			}
			_, leave, enterErr = s.enter(ctx)
			if leave != nil {
				leave()
			}
			if outcome == "maintenance_aborted" {
				if enterErr != nil {
					t.Fatal("finished configuration left stale pause", enterErr)
				}
			} else {
				if !errors.Is(enterErr, ErrBTMaintenance) {
					t.Fatal("configuration completion removed maintenance/stop pause", enterErr)
				}
				s.Resume()
				_, leave, enterErr = s.enter(ctx)
				if leave != nil {
					leave()
				}
				if outcome == "stopped" && !errors.Is(enterErr, ErrBTMaintenance) {
					t.Fatal("explicit Resume reopened terminal stop")
				}
				if outcome == "still_quiesced" && enterErr != nil {
					t.Fatal("explicit maintenance Resume did not recover")
				}
			}
		})
	}
}
