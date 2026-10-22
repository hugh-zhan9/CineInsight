package migrator

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"reflect"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

type functionalPolishRows struct {
	Feedback       []models.RecommendationFeedback
	Run            models.TaskFailureRun
	Item           models.TaskFailureItem
	Assignments    []models.CollectionEpisodeAssignment
	Expectation    models.CollectionSeasonExpectation
	AI             models.AITaggingState
	Technical      models.VideoTechnicalMetadata
	Proxy          models.VideoPlaybackProxy
	Event          models.PlayEvent
	Diary          models.ViewingDiaryEntry
	DeletedMediaID uint
}

func seedFunctionalPolishRows(t *testing.T, db *gorm.DB, videoIDs []uint) functionalPolishRows {
	t.Helper()
	at := time.Date(2026, 10, 22, 7, 8, 9, 123456000, time.UTC)
	finished := at.Add(time.Minute)
	expires := at.Add(7 * 24 * time.Hour)
	collection := models.MediaCollection{Name: "Migration episodes", NormalizedName: "migration episodes"}
	if err := db.Create(&collection).Error; err != nil {
		t.Fatal(err)
	}
	for i, id := range videoIDs[:2] {
		if err := db.Create(&models.CollectionVideo{CollectionID: collection.ID, VideoID: id, Position: i + 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	gone := models.Video{Name: "removed.mp4", Path: "/polish/removed.mp4", Directory: "/polish"}
	if err := db.Create(&gone).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Unscoped().Delete(&gone).Error; err != nil {
		t.Fatal(err)
	}
	season, episode := 0, 2
	rows := functionalPolishRows{
		Feedback:       []models.RecommendationFeedback{{TargetKind: "work_video", TargetID: gone.ID, Effect: "snooze", ExpiresAt: &expires, Revision: 7, CreatedAt: at, UpdatedAt: finished}, {TargetKind: "tag", TargetID: 1, Effect: "downweight", Revision: 3, CreatedAt: at, UpdatedAt: finished}},
		Run:            models.TaskFailureRun{TaskKey: "technical", StartedAt: at, FinishedAt: &finished, State: "interrupted", RecordsComplete: false},
		Assignments:    []models.CollectionEpisodeAssignment{{CollectionID: collection.ID, VideoID: videoIDs[0], Season: &season, Episode: &episode, Excluded: false, Revision: 8}, {CollectionID: collection.ID, VideoID: videoIDs[1], Excluded: true, Revision: 2}},
		Expectation:    models.CollectionSeasonExpectation{CollectionID: collection.ID, Season: 0, EpisodeCount: 6, Revision: 9},
		AI:             models.AITaggingState{VideoID: videoIDs[0], Status: models.AITaggingStateStatusPending, FailureRetrySourceVersion: strings.Repeat("a", 64), SkipReason: "manual_retry", CreatedAt: at, UpdatedAt: at},
		Technical:      models.VideoTechnicalMetadata{VideoID: videoIDs[0], LastError: "prior failure", FailureRetryClaim: "frozen-technical", CreatedAt: at, UpdatedAt: at},
		Proxy:          models.VideoPlaybackProxy{VideoID: videoIDs[0], Status: models.PlaybackProxyStatusFailed, Strategy: models.PlaybackProxyStrategyRemux, SourceSize: 123, SourceModTimeNS: 123456789, LastUsedAt: at, LastError: "prior proxy failure", FailureRetryClaim: "frozen-proxy", CreatedAt: at, UpdatedAt: at},
		Event:          models.PlayEvent{VideoID: videoIDs[0], Source: "desktop_controlled_view", PlayedAt: at},
		Diary:          models.ViewingDiaryEntry{VideoID: &videoIDs[0], Title: "controlled view", WatchedOn: "2026-10-22", RecordedAt: &at, Origin: models.DiaryOriginAutomatic, Source: "desktop_controlled_view", Revision: 4, CreatedAt: at, UpdatedAt: at},
		DeletedMediaID: gone.ID,
	}
	for i := range rows.Feedback {
		if err := db.Create(&rows.Feedback[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&rows.Run).Error; err != nil {
		t.Fatal(err)
	}
	rows.Item = models.TaskFailureItem{RunID: rows.Run.ID, MediaKind: "video", MediaID: gone.ID, AttemptKey: "retained-deleted-attempt", SourceVersion: "source-before-delete", StateVersion: "failure-before-delete", ErrorCode: "probe_failed", ErrorSummary: "删除媒体后仍保留这次失败", FailedAt: at}
	for _, row := range []any{&rows.Item, &rows.Assignments[0], &rows.Assignments[1], &rows.Expectation, &rows.Event, &rows.Diary} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []any{&rows.AI, &rows.Technical, &rows.Proxy} {
		err := db.Where("video_id = ?", videoIDs[0]).First(row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = db.Create(row).Error
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&models.Settings{}).Where("id > 0").Update("desktop_player", "iina_controlled").Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func assertFunctionalPolishRows(t *testing.T, db *gorm.DB, want functionalPolishRows) {
	t.Helper()
	assert := func(expected any, where string, args ...any) {
		dest := reflect.New(reflect.TypeOf(expected))
		if err := db.Unscoped().Where(where, args...).First(dest.Interface()).Error; err != nil {
			t.Fatal(err)
		}
		assertPolishValue(t, reflect.ValueOf(expected), dest.Elem())
	}
	for _, row := range want.Feedback {
		assert(row, "id = ?", row.ID)
	}
	assert(want.Run, "id = ?", want.Run.ID)
	assert(want.Item, "id = ?", want.Item.ID)
	for _, row := range want.Assignments {
		assert(row, "collection_id = ? AND video_id = ?", row.CollectionID, row.VideoID)
	}
	assert(want.Expectation, "collection_id = ? AND season = ?", want.Expectation.CollectionID, want.Expectation.Season)
	assert(want.AI, "video_id = ?", want.AI.VideoID)
	assert(want.Technical, "video_id = ?", want.Technical.VideoID)
	assert(want.Proxy, "video_id = ?", want.Proxy.VideoID)
	var ai models.AITaggingState
	var technical models.VideoTechnicalMetadata
	var proxy models.VideoPlaybackProxy
	if err := db.Where("video_id = ?", want.AI.VideoID).First(&ai).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("video_id = ?", want.Technical.VideoID).First(&technical).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("video_id = ?", want.Proxy.VideoID).First(&proxy).Error; err != nil {
		t.Fatal(err)
	}
	if ai.FailureRetrySourceVersion != want.AI.FailureRetrySourceVersion || technical.FailureRetryClaim != want.Technical.FailureRetryClaim || proxy.FailureRetryClaim != want.Proxy.FailureRetryClaim {
		t.Fatal("private retry authorization columns changed")
	}
	assert(want.Event, "id = ?", want.Event.ID)
	assert(want.Diary, "id = ?", want.Diary.ID)
	var deleted int64
	if err := db.Unscoped().Model(&models.Video{}).Where("id = ?", want.DeletedMediaID).Count(&deleted).Error; err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatal("logical failure reference recreated deleted media")
	}
	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatal(err)
	}
	if settings.DesktopPlayer != "iina_controlled" {
		t.Fatal("desktop player preference lost")
	}
}

// Compare stored values, treating backend timestamp locations as representations
// of the same instant. GORM association placeholders are not persisted columns.
func assertPolishValue(t *testing.T, want, got reflect.Value) {
	t.Helper()
	if want.Type() == reflect.TypeOf(time.Time{}) {
		if !want.Interface().(time.Time).Equal(got.Interface().(time.Time)) {
			t.Fatalf("time changed: %v != %v", want.Interface(), got.Interface())
		}
		return
	}
	switch want.Kind() {
	case reflect.Ptr:
		if want.IsNil() != got.IsNil() {
			t.Fatal("NULL changed")
		}
		if !want.IsNil() {
			assertPolishValue(t, want.Elem(), got.Elem())
		}
	case reflect.Struct:
		for i := 0; i < want.NumField(); i++ {
			field := want.Type().Field(i)
			if !field.IsExported() || field.Tag.Get("json") == "-" {
				continue
			}
			assertPolishValue(t, want.Field(i), got.Field(i))
		}
	default:
		if !reflect.DeepEqual(want.Interface(), got.Interface()) {
			t.Fatalf("persisted value changed: %v != %v", want.Interface(), got.Interface())
		}
	}
}

func TestMigrateFunctionalPolishActualRoundTrip(t *testing.T) {
	source, middle, back := sequenceSQLite(t), dbtest.Open(t), sequenceSQLite(t)
	videos := []models.Video{{Name: "one.mp4", Path: "/polish/one.mp4"}, {Name: "two.mp4", Path: "/polish/two.mp4"}}
	for i := range videos {
		if err := source.Create(&videos[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := source.Create(&models.Tag{Name: "migration feedback tag"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := source.Create(&models.Settings{}).Error; err != nil {
		t.Fatal(err)
	}
	want := seedFunctionalPolishRows(t, source, []uint{videos[0].ID, videos[1].ID})
	for _, step := range []struct{ from, to *gorm.DB }{{source, middle}, {middle, back}} {
		result, err := Migrate(context.Background(), Options{Source: step.from, Target: step.to, TargetBackend: database.Backend(step.to.Dialector.Name())})
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"recommendation_feedbacks", "task_failure_runs", "task_failure_items", "collection_episode_assignments", "collection_season_expectations"} {
			if result.RowCounts[table] < 1 {
				t.Fatalf("table %s copied no actual rows", table)
			}
		}
		assertFunctionalPolishRows(t, step.to, want)
	}
	assertFunctionalPolishRows(t, source, want)
	inserted := models.Video{Name: "new.mp4", Path: "/polish/new.mp4"}
	if err := back.Create(&inserted).Error; err != nil {
		t.Fatal(err)
	}
	if inserted.ID <= want.DeletedMediaID {
		t.Fatalf("deleted logical target reused: %d", inserted.ID)
	}
	t.Logf("actual row round trip: sqlite -> %s -> sqlite", middle.Dialector.Name())
}
