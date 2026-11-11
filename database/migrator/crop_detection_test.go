package migrator

import (
	"context"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestMigrateCropDetectionRoundTrip(t *testing.T) {
	source, middle, back := sequenceSQLite(t), dbtest.Open(t), sequenceSQLite(t)
	v := models.Video{Name: "crop.mp4", Path: "/crop.mp4"}
	if e := source.Create(&v).Error; e != nil {
		t.Fatal(e)
	}
	state := models.VideoCropDetectionState{VideoID: v.ID, SourceVersion: "100:200", AlgorithmVersion: 1, Revision: 7, Status: "no_borders", ResultJSON: "{}", CheckedAt: time.Now()}
	if e := source.Create(&state).Error; e != nil {
		t.Fatal(e)
	}
	if _, e := Migrate(context.Background(), Options{Source: source, Target: middle, TargetBackend: database.Backend(middle.Dialector.Name())}); e != nil {
		t.Fatal(e)
	}
	if _, e := Migrate(context.Background(), Options{Source: middle, Target: back, TargetBackend: database.Backend(back.Dialector.Name())}); e != nil {
		t.Fatal(e)
	}
	var got models.VideoCropDetectionState
	if e := back.First(&got, "video_id = ?", v.ID).Error; e != nil || got.Revision != 7 || got.SourceVersion != state.SourceVersion {
		t.Fatal(got, e)
	}
}
