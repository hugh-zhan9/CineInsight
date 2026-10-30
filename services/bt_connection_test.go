package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func newBTConnectionTest(t *testing.T) (*BTConnectionService, *btMemorySecrets) {
	t.Helper()
	before := database.DB
	database.DB = dbtest.Open(t)
	t.Cleanup(func() { database.DB = before })
	store := btMemorySecrets{}
	service := NewBTConnectionService(func(context.Context, *gorm.DB) (*BTCredentialManager, error) {
		return NewBTCredentialManager(store), nil
	}, BTConnectionHooks{Drain: func(context.Context) error { return nil }, Invalidate: func(uint64) {}, Resume: func() {}})
	return service, &store
}
func btConnectionRequest(t *testing.T) BTConnectionRequest {
	return BTConnectionRequest{ProwlarrBase: "http://127.0.0.1:9696", QBBase: "http://127.0.0.1:8081", DownloadRoot: t.TempDir(), Enabled: true, ProwlarrKey: BTSecretChange{Operation: "replace", Value: "secret-key"}}
}
func TestBTConnectionConfigureAndRetire(t *testing.T) {
	service, store := newBTConnectionTest(t)
	ctx := context.Background()
	request := btConnectionRequest(t)
	reads := 0
	service.hooks.Drain = func(ctx context.Context) error { _, err := service.Get(ctx); reads++; return err }
	service.hooks.Resume = func() {
		if _, err := service.Get(ctx); err != nil {
			t.Error(err)
		}
	}
	first, err := service.Configure(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !first.ProwlarrKeyConfigured || first.QBPasswordConfigured || first.Revision != 1 || reads != 1 {
		t.Fatalf("%+v", first)
	}
	raw, _ := json.Marshal(first)
	if strings.Contains(string(raw), "secret-key") {
		t.Fatal("secret leaked")
	}
	request.ExpectedRevision = first.Revision
	request.ProwlarrKey = BTSecretChange{Operation: "keep"}
	request.QBBase = "http://127.0.0.1:8082"
	next, err := service.Configure(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.InstanceID == next.InstanceID || next.Revision != 2 || !next.ProwlarrKeyConfigured {
		t.Fatalf("%+v", next)
	}
	var old models.BTConnection
	if err := database.DB.First(&old, "id = ?", first.InstanceID).Error; err != nil {
		t.Fatal(err)
	}
	if old.Slot == "local" || old.CredentialRef != "" || old.Enabled {
		t.Fatalf("retired row: %+v", old)
	}
	if len(*store) != 1 {
		t.Fatal("old secret retained")
	}
	if _, err = service.Configure(ctx, request); !errors.Is(err, ErrBTConflict) {
		t.Fatal(err)
	}
}
func TestBTConnectionActiveIdentityAndDynamicDB(t *testing.T) {
	service, _ := newBTConnectionTest(t)
	ctx := context.Background()
	request := btConnectionRequest(t)
	first, err := service.Configure(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	task := models.BTTask{ID: "task", ConnectionID: first.InstanceID, Revision: 1, IntentKey: "intent", OwnerTag: "tag", CanonicalRoot: "root", RootKey: "key", Stage: "active"}
	if err := database.DB.Omit("Connection").Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	request.ExpectedRevision = first.Revision
	request.QBBase = "http://127.0.0.1:8082"
	if _, err := service.Configure(ctx, request); !errors.Is(err, ErrBTConnectionActive) {
		t.Fatalf("active task identity changed: %v", err)
	}
	current, _ := service.Get(ctx)
	if current.InstanceID != first.InstanceID || current.Revision != 1 {
		t.Fatal("failed save changed config")
	}
	database.DB = dbtest.Open(t)
	fresh, err := service.Get(ctx)
	if err != nil || fresh.InstanceID != "" {
		t.Fatalf("cached old DB: %+v %v", fresh, err)
	}
}
func TestBTConnectionRequiresLifecycleHooks(t *testing.T) {
	service, _ := newBTConnectionTest(t)
	service.hooks.Drain = nil
	if _, err := service.Configure(context.Background(), btConnectionRequest(t)); !errors.Is(err, ErrBTConnection) {
		t.Fatal(err)
	}
}
func TestBTConnectionUniquenessAndFK(t *testing.T) {
	service, _ := newBTConnectionTest(t)
	v, err := service.Configure(context.Background(), btConnectionRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	db := database.DB
	a := models.BTTask{ID: "a", ConnectionID: v.InstanceID, IntentKey: "same", OwnerTag: "owner-a", RootKey: "root-a"}
	if err := db.Omit("Connection").Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	b := a
	b.ID = "b"
	b.OwnerTag = "owner-b"
	b.RootKey = "root-b"
	if err := db.Omit("Connection").Create(&b).Error; err == nil {
		t.Fatal("duplicate intent")
	}
	pathKey := "path"
	file := models.BTFile{TaskID: "missing", ManifestFileID: strings.Repeat("1", 64), RelativePath: "a", PathKey: &pathKey}
	if err := db.Omit("Task").Create(&file).Error; err == nil {
		t.Fatal("orphan file")
	}
}

func TestBTConnectionMissingCredentialCanReconfigure(t *testing.T) {
	service, store := newBTConnectionTest(t)
	ctx := context.Background()
	r := btConnectionRequest(t)
	v, err := service.Configure(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	for key := range *store {
		delete(*store, key)
	}
	missing, err := service.Get(ctx)
	if err != nil || missing.CredentialStatus != "unavailable" || missing.Revision != v.Revision || missing.QBBase != r.QBBase {
		t.Fatalf("lost recovery metadata: %+v %v", missing, err)
	}
	if _, err := service.Clients(ctx); err == nil {
		t.Fatal("missing secret accepted for clients")
	}
	r.ExpectedRevision = v.Revision
	r.ProwlarrKey = BTSecretChange{Operation: "replace", Value: "new-key"}
	r.QBPassword = BTSecretChange{Operation: "clear"}
	restored, err := service.Configure(ctx, r)
	if err != nil || restored.CredentialStatus != "configured" {
		t.Fatalf("reconnect: %+v %v", restored, err)
	}
}

func TestBTTaskHashAliasesUniqueAndNull(t *testing.T) {
	service, _ := newBTConnectionTest(t)
	v, err := service.Configure(context.Background(), btConnectionRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	db := database.DB
	makeTask := func(id string) models.BTTask {
		return models.BTTask{ID: id, ConnectionID: v.InstanceID, IntentKey: id, OwnerTag: id, RootKey: id}
	}
	a, b := makeTask("null-a"), makeTask("null-b")
	for _, row := range []*models.BTTask{&a, &b} {
		if err := db.Omit("Connection").Create(row).Error; err != nil {
			t.Fatal("multiple unknown hashes should coexist", err)
		}
	}
	v1, v2 := strings.Repeat("a", 40), strings.Repeat("b", 64)
	known := makeTask("hybrid")
	known.InfoHashV1 = &v1
	known.InfoHashV2 = &v2
	if err := db.Omit("Connection").Create(&known).Error; err != nil {
		t.Fatal(err)
	}
	for _, which := range []string{"v1", "v2"} {
		row := makeTask(which)
		if which == "v1" {
			row.InfoHashV1 = &v1
		} else {
			row.InfoHashV2 = &v2
		}
		if err := db.Omit("Connection").Create(&row).Error; err == nil {
			t.Fatalf("duplicate %s alias accepted", which)
		}
	}
	empty := ""
	row := makeTask("empty")
	row.InfoHashV1 = &empty
	if err := db.Omit("Connection").Create(&row).Error; err == nil {
		t.Fatal("empty string stored instead of NULL")
	}
}

func TestBTConnectionTaskEpochFencesRetirement(t *testing.T) {
	service, _ := newBTConnectionTest(t)
	ctx := context.Background()
	r := btConnectionRequest(t)
	v, err := service.Configure(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	db := database.DB
	if err := ClaimBTTaskEpoch(db, v.InstanceID, v.Revision, 0); !errors.Is(err, ErrBTConnection) {
		t.Fatal("nontransaction claim accepted", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return ClaimBTTaskEpoch(tx, v.InstanceID, v.Revision, 0) }); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return ClaimBTTaskEpoch(tx, v.InstanceID, v.Revision, 0) }); !errors.Is(err, ErrBTConflict) {
		t.Fatal("stale epoch admitted", err)
	}
	service.hooks.Drain = func(context.Context) error {
		return db.Transaction(func(tx *gorm.DB) error { return ClaimBTTaskEpoch(tx, v.InstanceID, v.Revision, 1) })
	}
	r.ExpectedRevision = v.Revision
	r.QBBase = "http://127.0.0.1:8888"
	if _, err := service.Configure(ctx, r); !errors.Is(err, ErrBTConflict) {
		t.Fatal("retirement ignored concurrent epoch change", err)
	}
	var row models.BTConnection
	db.First(&row, "id = ?", v.InstanceID)
	if row.Slot != "local" || row.TaskEpoch != 2 {
		t.Fatalf("lost parent: %+v", row)
	}
	rollback := errors.New("rollback")
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := ClaimBTTaskEpoch(tx, v.InstanceID, v.Revision, 2); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	db.First(&row, "id = ?", v.InstanceID)
	if row.TaskEpoch != 2 {
		t.Fatal("epoch was not rolled back")
	}
}

func TestBTConnectionTestIsReadOnlyAndPasswordless(t *testing.T) {
	service, _ := newBTConnectionTest(t)
	calls := []string{}
	unauthorized := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if unauthorized {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("secret upstream message"))
			return
		}
		switch r.URL.Path {
		case "/api/v1/system/status":
			w.Write([]byte(`{"version":"3.2.1.99","appData":"must-not-leak"}`))
		case "/api/v2/app/buildInfo":
			w.Write([]byte(`{"libtorrent":"2.0.13.0"}`))
		case "/api/v1/indexer":
			w.Write([]byte("[]"))
		case "/api/v2/app/version":
			w.Write([]byte("v5.2.3"))
		case "/api/v2/app/webapiVersion":
			w.Write([]byte("2.15.1"))
		default:
			t.Errorf("unexpected remote action: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	request := btConnectionRequest(t)
	request.ProwlarrBase = server.URL
	request.QBBase = server.URL
	if _, err := service.Configure(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	result, err := service.Test(context.Background())
	if err != nil || result.QBStatus != "ok" || result.ProwlarrStatus != "ok" || result.ProwlarrVersion != "3.2.1.99" || result.QBVersion != "v5.2.3" || result.QBAPIVersion != "2.15.1" || result.MetadataPreviewStatus != "supported" {
		t.Fatalf("%+v %v", result, err)
	}
	unauthorized = true
	rejected, err := service.Test(context.Background())
	if err != nil || rejected.ProwlarrStatus != "auth_failed" || rejected.QBStatus != "auth_failed" {
		t.Fatalf("authentication result lost: %+v %v", rejected, err)
	}
	for _, call := range calls {
		if !strings.HasPrefix(call, "GET ") {
			t.Fatal("readonly test sent write", call)
		}
	}
	var tasks int64
	database.DB.Model(&models.BTTask{}).Count(&tasks)
	if tasks != 0 {
		t.Fatal("test created task")
	}
}

func TestBTFilePaddingDoesNotClaimPayloadPath(t *testing.T) {
	service, _ := newBTConnectionTest(t)
	v, err := service.Configure(context.Background(), btConnectionRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	task := models.BTTask{ID: "padding-task", ConnectionID: v.InstanceID, IntentKey: "padding", OwnerTag: "padding", RootKey: "padding"}
	db := database.DB
	if err := db.Omit("Connection").Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		file := models.BTFile{TaskID: task.ID, ManifestFileID: strings.Repeat(string(rune('a'+i)), 64), Position: i, Padding: true, RelativePath: ""}
		if err := db.Omit("Task").Create(&file).Error; err != nil {
			t.Fatal("padding collided", err)
		}
	}
	key := "payload"
	file := models.BTFile{TaskID: task.ID, ManifestFileID: strings.Repeat("c", 64), Position: 2, RelativePath: "movie.mkv", PathKey: &key, Selectable: true}
	if err := db.Omit("Task").Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	file.ManifestFileID = strings.Repeat("d", 64)
	if err := db.Omit("Task").Create(&file).Error; err == nil {
		t.Fatal("duplicate payload path accepted")
	}
}

func TestBTConnectionRejectsRestoreDuringDrain(t *testing.T) {
	for _, swap := range []bool{false, true} {
		t.Run(fmt.Sprint(swap), func(t *testing.T) {
			service, store := newBTConnectionTest(t)
			ctx := context.Background()
			request := btConnectionRequest(t)
			initial, err := service.Configure(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			original := database.DB
			var originalRow models.BTConnection
			if err := original.First(&originalRow, "id = ?", initial.InstanceID).Error; err != nil {
				t.Fatal(err)
			}
			restored := original
			if swap {
				restored = dbtest.Open(t)
				if err := restored.Create(&originalRow).Error; err != nil {
					t.Fatal(err)
				}
			}
			service.hooks.Drain = func(context.Context) error {
				release := database.BeginMaintenance()
				database.DB = restored
				release()
				return nil
			}
			request.ExpectedRevision = initial.Revision
			request.ProwlarrKey = BTSecretChange{Operation: "replace", Value: "replacement-secret"}
			if _, err := service.Configure(ctx, request); !errors.Is(err, ErrBTConflict) {
				t.Fatalf("pre-restore request crossed generation: %v", err)
			}
			var got models.BTConnection
			if err := restored.First(&got, "id = ?", initial.InstanceID).Error; err != nil {
				t.Fatal(err)
			}
			if got.Revision != originalRow.Revision || got.CredentialRef != originalRow.CredentialRef {
				t.Fatalf("restored snapshot mutated: %+v", got)
			}
			if _, ok := (*store)[originalRow.CredentialRef]; !ok {
				t.Fatal("restored credential deleted")
			}
			if len(*store) != 1 {
				t.Fatal("rejected pre-restore request wrote a secret")
			}
		})
	}
}

type btBlockingDeleteStore struct {
	btMemorySecrets
	blockedRef string
	entered    chan struct{}
	proceed    chan struct{}
}

func (s *btBlockingDeleteStore) Delete(ref string) error {
	if ref == s.blockedRef {
		close(s.entered)
		<-s.proceed
	}
	return s.btMemorySecrets.Delete(ref)
}
func TestBTConnectionMaintenanceWaitsForCredentialCleanup(t *testing.T) {
	service, _ := newBTConnectionTest(t)
	store := &btBlockingDeleteStore{btMemorySecrets: btMemorySecrets{}, entered: make(chan struct{}), proceed: make(chan struct{})}
	service.credentialFactory = func(context.Context, *gorm.DB) (*BTCredentialManager, error) {
		return NewBTCredentialManager(store), nil
	}
	request := btConnectionRequest(t)
	initial, err := service.Configure(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var row models.BTConnection
	database.DB.First(&row, "id = ?", initial.InstanceID)
	store.blockedRef = row.CredentialRef
	request.ExpectedRevision = initial.Revision
	request.ProwlarrKey = BTSecretChange{Operation: "replace", Value: "replacement"}
	done := make(chan error, 1)
	go func() { _, err := service.Configure(context.Background(), request); done <- err }()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup not reached")
	}
	maintenanceStarted := database.MaintenanceStarted()
	entered := make(chan func(), 1)
	go func() { entered <- database.BeginMaintenance() }()
	<-maintenanceStarted
	early := false
	select {
	case release := <-entered:
		early = true
		release()
	case <-time.After(30 * time.Millisecond):
	}
	close(store.proceed)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("configuration cleanup deadlocked")
	}
	if !early {
		select {
		case release := <-entered:
			release()
		case <-time.After(5 * time.Second):
			t.Fatal("maintenance never acquired")
		}
	}
	if early {
		t.Fatal("maintenance crossed the Keychain deletion gap")
	}
}

func TestBTConnectionScopesFollowLeasedDatabase(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("OS namespace lock is unsupported on this platform")
	}
	service, _ := newBTConnectionTest(t)
	stores := map[string]btMemorySecrets{}
	dir := t.TempDir()
	service.credentialFactory = func(ctx context.Context, db *gorm.DB) (*BTCredentialManager, error) {
		scope, err := BTCredentialScope(ctx, db)
		if err != nil {
			return nil, err
		}
		store := stores[scope]
		if store == nil {
			store = btMemorySecrets{}
			stores[scope] = store
		}
		return NewBTCredentialManager(store, BTFileCredentialLock{Path: filepath.Join(dir, scope+".lock")}), nil
	}
	r := btConnectionRequest(t)
	initial, err := service.Configure(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	var copied models.BTConnection
	if err := database.DB.First(&copied, "id = ?", initial.InstanceID).Error; err != nil {
		t.Fatal(err)
	}
	oldDB := database.DB
	restored := dbtest.Open(t)
	if err := restored.Create(&copied).Error; err != nil {
		t.Fatal(err)
	}
	release := database.BeginMaintenance()
	database.DB = restored
	release()
	unavailable, err := service.Get(context.Background())
	if err != nil || unavailable.CredentialStatus != "unavailable" || unavailable.Revision != initial.Revision {
		t.Fatalf("new physical DB reused old secrets: %+v %v", unavailable, err)
	}
	r.ExpectedRevision = initial.Revision
	r.ProwlarrKey = BTSecretChange{Operation: "replace", Value: "restored-key"}
	r.QBPassword = BTSecretChange{Operation: "clear"}
	if _, err := service.Configure(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	release = database.BeginMaintenance()
	database.DB = oldDB
	release()
	previous, err := service.Get(context.Background())
	if err != nil || !previous.ProwlarrKeyConfigured || previous.Revision != initial.Revision {
		t.Fatalf("new scope cleanup altered old DB credentials: %+v %v", previous, err)
	}
}
func TestBTConnectionGetShowsCredentialBusy(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("OS namespace lock is unsupported on this platform")
	}
	service, _ := newBTConnectionTest(t)
	store := btMemorySecrets{}
	lock := BTFileCredentialLock{Path: filepath.Join(t.TempDir(), "busy.lock")}
	service.credentialFactory = func(context.Context, *gorm.DB) (*BTCredentialManager, error) {
		return NewBTCredentialManager(store, lock), nil
	}
	r := btConnectionRequest(t)
	saved, err := service.Configure(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lock.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	busy, err := service.Get(context.Background())
	if err != nil || busy.CredentialStatus != "busy" || busy.Revision != saved.Revision || busy.QBBase != saved.QBBase {
		t.Fatalf("busy credentials lost configuration: %+v %v", busy, err)
	}
}

func TestBTConnectionActiveTasksAllowAccountRotation(t *testing.T) {
	service, store := newBTConnectionTest(t)
	ctx := context.Background()
	request := btConnectionRequest(t)
	first, err := service.Configure(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	task := models.BTTask{ID: "owned-task", ConnectionID: first.InstanceID, Revision: 7, IntentKey: "owned-intent", OwnerTag: "owned-tag", CanonicalRoot: "owned-root", RootKey: "owned-key", Stage: "active"}
	if err = database.DB.Omit("Connection").Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	drained, invalidated, resumed := 0, 0, 0
	service.hooks = BTConnectionHooks{Drain: func(context.Context) error { drained++; return nil }, Invalidate: func(uint64) { invalidated++ }, Resume: func() { resumed++ }}
	request.ExpectedRevision = first.Revision
	request.ProwlarrKey = BTSecretChange{Operation: "keep"}
	request.QBUsername = "new-account"
	request.QBPassword = BTSecretChange{Operation: "replace", Value: "new-password"}
	updated, err := service.Configure(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if updated.InstanceID != first.InstanceID || updated.Revision != first.Revision+1 || updated.QBUsername != "new-account" || !updated.QBPasswordConfigured {
		t.Fatalf("account rotation replaced identity: %+v", updated)
	}
	if drained != 1 || invalidated != 1 || resumed != 1 || len(*store) != 1 {
		t.Fatal("rotation skipped lifecycle or reference cleanup")
	}
	var retained models.BTTask
	if err = database.DB.First(&retained, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if retained.ConnectionID != task.ConnectionID || retained.Revision != task.Revision || retained.OwnerTag != task.OwnerTag {
		t.Fatal("rotation changed task ownership")
	}
	request.ExpectedRevision = updated.Revision
	request.QBBase = "http://127.0.0.1:8082"
	if _, err = service.Configure(ctx, request); !errors.Is(err, ErrBTConnectionActive) {
		t.Fatal("endpoint change bypassed active protection", err)
	}
}
