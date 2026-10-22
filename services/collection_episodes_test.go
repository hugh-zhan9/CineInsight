package services

import (
	"errors"
	"fmt"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestCollectionEpisodeReportAndAtomicCorrections(t *testing.T) {
	setupVideoServiceTestDB(t)
	if err := database.DB.AutoMigrate(&models.CollectionEpisodeAssignment{}, &models.CollectionSeasonExpectation{}); err != nil {
		t.Fatal(err)
	}
	svc := NewCollectionService(t.TempDir())
	collection, err := svc.CreateCollection("Series", "")
	if err != nil {
		t.Fatal(err)
	}
	ids := []uint{}
	for _, n := range []int{1, 2, 4} {
		v := models.Video{Name: fmt.Sprintf("Series.S01E%02d.mkv", n), Path: fmt.Sprintf("/series/%d.mkv", n), Directory: "/series", IsStale: n == 2}
		if err := database.DB.Create(&v).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, v.ID)
	}
	if err := svc.AddCollectionVideos(collection.ID, ids); err != nil {
		t.Fatal(err)
	}
	report, err := svc.GetEpisodeReport(collection.ID, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Seasons) != 1 || report.Seasons[0].MissingRanges[0] != (EpisodeMissingRange{3, 3}) || report.Seasons[0].Offline != 1 || report.Total != 3 || report.NextCursor == 0 {
		t.Fatalf("wrong report %+v", report)
	}
	if err := svc.SetSeasonExpectation(collection.ID, 1, 6, 0); err != nil {
		t.Fatal(err)
	}
	report, err = svc.GetEpisodeReport(collection.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Seasons[0].MissingRanges) != 2 || report.Seasons[0].MissingRanges[1] != (EpisodeMissingRange{5, 6}) {
		t.Fatalf("tail missing: %+v", report.Seasons)
	}
	season, episode := 1, 3
	if err := svc.SetEpisodeAssignments(collection.ID, []CollectionEpisodeEdit{{VideoID: ids[2], Season: &season, Episode: &episode}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetEpisodeAssignments(collection.ID, []CollectionEpisodeEdit{{VideoID: ids[0], Excluded: true}, {VideoID: ids[2], Excluded: true, ExpectedRevision: 0}}); !errors.Is(err, ErrCollectionEpisodeConflict) {
		t.Fatalf("conflict: %v", err)
	}
	var n int64
	database.DB.Model(&models.CollectionEpisodeAssignment{}).Where("video_id = ?", ids[0]).Count(&n)
	if n != 0 {
		t.Fatal("partial batch committed")
	}
	if err := svc.RemoveCollectionVideo(collection.ID, ids[2]); err != nil {
		t.Fatal(err)
	}
	database.DB.Model(&models.CollectionEpisodeAssignment{}).Where("video_id = ?", ids[2]).Count(&n)
	if n != 0 {
		t.Fatal("member override survived removal")
	}
	invalid := models.CollectionEpisodeAssignment{CollectionID: collection.ID, VideoID: ids[2], Revision: 1}
	if err := database.DB.Create(&invalid).Error; err == nil {
		t.Fatal("composite FK accepted nonmember")
	}
	if err := svc.DeleteCollection(collection.ID); err != nil {
		t.Fatal(err)
	}
	database.DB.Model(&models.CollectionSeasonExpectation{}).Where("collection_id = ?", collection.ID).Count(&n)
	if n != 0 {
		t.Fatal("expectations survived collection deletion")
	}
}
func TestCollectionEpisodeParserRejectsMultiEpisode(t *testing.T) {
	for _, name := range []string{"Series.S01E01E02.mkv", "Series.S01E01-E03.mkv", "Series.第1-3集.mp4"} {
		if p := parseCollectionEpisode(models.Video{Name: name}); p.OK {
			t.Fatalf("multi episode guessed: %s", name)
		}
	}
}
