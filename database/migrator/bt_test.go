package migrator

import (
	"context"
	"strings"
	"testing"
	"video-master/internal/dbtest"

	"gorm.io/gorm"
	"video-master/models"
)

func seedBTRows(t *testing.T, db *gorm.DB) {
	t.Helper()
	connection := models.BTConnection{ID: "00000000-0000-0000-0000-000000000001", Slot: "local", Revision: 7, ProwlarrBase: "http://127.0.0.1:9696", QBBase: "http://127.0.0.1:8081", DownloadRoot: "/fixture/bt", Enabled: true}
	if err := db.Create(&connection).Error; err != nil {
		t.Fatal(err)
	}
	task := models.BTTask{ID: "00000000-0000-0000-0000-000000000002", ConnectionID: connection.ID, Revision: 3, AttemptGeneration: 2, Title: "资源标题快照", IndexerID: 42, IndexerName: "站点快照", IntentKey: "fixture:intent", OwnerTag: "fixture:owner", ManifestDigest: "manifest", SaveRoot: "/fixture/bt/task", CanonicalRoot: "/fixture/bt/task", RootKey: "root", Stage: "active", PendingAction: "start", UnresolvedWrite: true, ObservationEpoch: 31, SelectionDigest: "selection"}
	if err := db.Omit("Connection").Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	pathKey := "file"
	qbIndex := 7
	file := models.BTFile{TaskID: task.ID, ManifestFileID: strings.Repeat("a", 64), Position: 0, RelativePath: "movie.mkv", PathKey: &pathKey, QBFileIndex: &qbIndex, Selectable: true, Size: 100, Selected: true, Progress: 1, EligibilityRevision: 31, ImportState: "pending", PublicationState: "pending_new", DuplicateResult: `{"status":"none","video_id":9}`}
	if err := db.Omit("Task").Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	for index, letter := range []string{"b", "c"} {
		padding := models.BTFile{TaskID: task.ID, ManifestFileID: strings.Repeat(letter, 64), Position: index + 1, Padding: true, Size: 12, ImportState: "pending"}
		if err := db.Omit("Task").Create(&padding).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrateBTStringFileIdentityRoundTrip(t *testing.T) {
	source, middle, back := dbtest.Open(t), dbtest.Open(t), dbtest.Open(t)
	seedSource(t, source)
	for _, step := range []struct{ from, to *gorm.DB }{{source, middle}, {middle, back}} {
		if _, err := Migrate(context.Background(), Options{Source: step.from, Target: step.to, TargetBackend: backendOfTest()}); err != nil {
			t.Fatal(err)
		}
	}
	var task models.BTTask
	if err := back.First(&task, "id = ?", "00000000-0000-0000-0000-000000000002").Error; err != nil {
		t.Fatal(err)
	}
	if task.Title != "资源标题快照" || task.IndexerID != 42 || task.IndexerName != "站点快照" || task.PendingAction != "start" || !task.UnresolvedWrite || task.ObservationEpoch != 31 {
		t.Fatalf("task display snapshot lost: %+v", task)
	}
	var files []models.BTFile
	if err := back.Order("position ASC").Find(&files).Error; err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("expected payload and two padding rows: %+v", files)
	}
	for i, file := range files {
		want := strings.Repeat(string(rune('a'+i)), 64)
		if file.ManifestFileID != want || file.Position != i {
			t.Fatalf("identity/order changed: %+v", file)
		}
		if i == 0 {
			if file.PublicationState != "pending_new" || file.DuplicateResult != `{"status":"none","video_id":9}` || file.QBFileIndex == nil || *file.QBFileIndex != 7 || !file.Selectable || !file.Selected || file.Padding {
				t.Fatalf("payload identity mapped to qB index: %+v", file)
			}
		} else {
			if !file.Padding || file.PathKey != nil || file.QBFileIndex != nil || file.Selectable || file.Selected || file.RelativePath != "" {
				t.Fatalf("padding identity lost: %+v", file)
			}
		}
	}
}

func TestBTFilePublicationColumnUpgrade(t *testing.T) {
	db := dbtest.Open(t)
	seedSource(t, db)
	if err := db.Migrator().DropColumn(&models.BTFile{}, "publication_state"); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.BTFile{}); err != nil {
		t.Fatal(err)
	}
	var files []models.BTFile
	if err := db.Order("position ASC").Find(&files).Error; err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("upgrade lost BT files: %d", len(files))
	}
	for _, file := range files {
		if file.PublicationState != "" {
			t.Fatalf("legacy row gained fabricated notification: %+v", file)
		}
	}
}

func TestBTTaskWriteSafetyColumnUpgrade(t *testing.T) {
	db := dbtest.Open(t)
	// Construct the legacy schema before inserting dependent rows. Dropping a
	// referenced populated parent is not the production ADD COLUMN migration.
	for _, column := range []string{"pending_action", "unresolved_write", "observation_epoch"} {
		if err := db.Migrator().DropColumn(&models.BTTask{}, column); err != nil {
			t.Fatal(err)
		}
	}
	connection := models.BTConnection{ID: "legacy-connection", Slot: "local", Revision: 1}
	if err := db.Create(&connection).Error; err != nil {
		t.Fatal(err)
	}
	legacy := models.BTTask{ID: "legacy-task", ConnectionID: connection.ID, Revision: 2, IntentKey: "legacy-intent", OwnerTag: "legacy-owner", RootKey: "legacy-root", Stage: "active", Title: "legacy title"}
	if err := db.Omit("Connection", "PendingAction", "UnresolvedWrite", "ObservationEpoch").Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("f", 64)
	file := models.BTFile{TaskID: legacy.ID, ManifestFileID: key, PathKey: &key, RelativePath: "legacy.mkv", Size: 1, Selectable: true, Selected: true}
	if err := db.Omit("Task").Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.BTTask{}); err != nil {
		t.Fatal(err)
	}
	var task models.BTTask
	if err := db.First(&task, "id = ?", legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.PendingAction != "" || task.UnresolvedWrite || task.ObservationEpoch != 0 || task.Title != legacy.Title {
		t.Fatal("column upgrade changed legacy task")
	}
	var count int64
	if err := db.Model(&models.BTFile{}).Where("task_id = ?", legacy.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("upgrade lost child", err)
	}
}

func TestBTFileDuplicateResultColumnUpgrade(t *testing.T) {
	db := dbtest.Open(t)
	seedSource(t, db)
	if err := db.Migrator().DropColumn(&models.BTFile{}, "duplicate_result"); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.BTFile{}); err != nil {
		t.Fatal(err)
	}
	var rows []models.BTFile
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatal("upgrade lost files")
	}
	for _, row := range rows {
		if row.DuplicateResult != "" {
			t.Fatal("fabricated duplicate result")
		}
	}
}
