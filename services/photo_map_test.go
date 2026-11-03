package services

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func photoMapFixture(t *testing.T, name string, lat, lon *float64, day string) *models.Image {
	t.Helper()
	image := mustCreateTestImage(t, name, 100)
	updates := map[string]any{"gps_latitude": lat, "gps_longitude": lon}
	if day != "" {
		at, err := time.ParseInLocation("2006-01-02", day, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		updates["taken_at"] = at
	}
	if err := database.DB.Model(image).Updates(updates).Error; err != nil {
		t.Fatal(err)
	}
	return image
}
func TestPhotoMapDatelineCountsAndBoundClusterPaging(t *testing.T) {
	setupImageServiceTestDB(t)
	lat, edge, other := 10.0, 180.0, 0.0
	negative := -180.0
	pole := 89.0
	a := photoMapFixture(t, "east.jpg", &lat, &edge, "2026-11-02")
	b := photoMapFixture(t, "west.jpg", &lat, &negative, "2026-11-02")
	photoMapFixture(t, "outside.jpg", &lat, &other, "2026-11-02")
	photoMapFixture(t, "missing.jpg", nil, nil, "")
	photoMapFixture(t, "polar.jpg", &pole, &edge, "2026-11-02")
	photoMapFixture(t, "undated.jpg", &other, &edge, "")
	service := NewImageLibraryService()
	ctx := context.Background()
	request := PhotoMapRequest{Bounds: PhotoBounds{West: 170, South: -90, East: -170, North: 90}, Zoom: 3}
	result, err := service.QueryPhotoMap(ctx, request)
	if err != nil || result.MatchedCount != 3 || result.MissingGPSCount != 1 || result.UndatedCount != 2 || result.UnprojectableCount != 1 {
		t.Fatalf("map %+v %v", result, err)
	}
	var key string
	var count int64
	for _, cluster := range result.Clusters {
		count += cluster.Count
		if cluster.Count == 2 {
			key = cluster.Key
		}
	}
	if count != 3 || key == "" {
		t.Fatalf("dateline duplication or lost cell %+v", result)
	}
	page, err := service.ListPhotoMapCluster(ctx, PhotoMapPageRequest{Query: request, CellKey: key, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != b.ID || page.NextCursor == "" {
		t.Fatalf("page %+v %v", page, err)
	}
	next, err := service.ListPhotoMapCluster(ctx, PhotoMapPageRequest{Query: request, CellKey: key, Cursor: page.NextCursor, Limit: 1})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != a.ID || next.NextCursor != "" {
		t.Fatalf("next %+v %v", next, err)
	}
	changed := request
	changed.Zoom++
	if _, err := service.ListPhotoMapCluster(ctx, PhotoMapPageRequest{Query: changed, CellKey: key, Cursor: page.NextCursor}); !errors.Is(err, ErrPhotoCursorInvalid) {
		t.Fatalf("changed scope cursor %v", err)
	}
	if _, err := service.ListPhotoMapCluster(ctx, PhotoMapPageRequest{Query: request, CellKey: key + "x"}); !errors.Is(err, ErrPhotoCursorInvalid) {
		t.Fatalf("forged cell %v", err)
	}
}
func TestPhotoMapCoarsensWithoutDroppingPhotos(t *testing.T) {
	setupImageServiceTestDB(t)
	rows := []models.Image{}
	for i := 0; i < 1201; i++ {
		lat := float64(i%81) - 40
		lon := float64(i)/1201*359 - 179.5
		rows = append(rows, models.Image{Name: fmt.Sprintf("%d.jpg", i), Path: fmt.Sprintf("/map/%d.jpg", i), Directory: "/map", GPSLatitude: &lat, GPSLongitude: &lon})
	}
	if err := database.DB.CreateInBatches(rows, 200).Error; err != nil {
		t.Fatal(err)
	}
	result, err := NewImageLibraryService().QueryPhotoMap(context.Background(), PhotoMapRequest{Bounds: PhotoBounds{West: -180, South: -90, East: 180, North: 90}, Zoom: 19})
	if err != nil {
		t.Fatal(err)
	}
	total := int64(0)
	for _, cluster := range result.Clusters {
		total += cluster.Count
	}
	if len(result.Clusters) > 1000 || result.GridLevel >= 20 || total != 1201 || result.MatchedCount != 1201 {
		t.Fatalf("coarsening dropped photos %+v", result)
	}
}
func TestPhotoRecapDatesNeverUseCreationTimeAndMissingPeopleRemainInvalid(t *testing.T) {
	setupImageServiceTestDB(t)
	photoMapFixture(t, "undated.jpg", nil, nil, "")
	dated := photoMapFixture(t, "dated-no-gps.jpg", nil, nil, "2026-11-02")
	photoMapFixture(t, "outside-date.jpg", nil, nil, "2026-11-03")
	service := NewImageLibraryService()
	ctx := context.Background()
	filter := PhotoSelection{DateFrom: "2026-11-02", DateTo: "2026-11-02"}
	page, err := service.ListPhotoRecapImages(ctx, PhotoRecapImagesRequest{Selection: filter})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != dated.ID || page.Items[0].TakenDate != "2026-11-02" {
		t.Fatalf("invented dates %+v %v", page, err)
	}
	person := models.Person{DisplayName: "同行人"}
	if err := database.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	filter.PersonIDs = []uint{person.ID}
	saved, err := service.SavePhotoRecap(ctx, PhotoRecapInput{Name: "旅行", Selection: filter})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&person).Error; err != nil {
		t.Fatal(err)
	}
	listed, err := service.ListPhotoRecaps(ctx, PhotoRecapQuery{})
	if err != nil || len(listed.Items) != 1 || len(listed.Items[0].InvalidPersonIDs) != 1 {
		t.Fatalf("missing person not exposed %+v %v", listed, err)
	}
	if _, err := service.ListPhotoRecapImages(ctx, PhotoRecapImagesRequest{Selection: filter}); !errors.Is(err, ErrPhotoFilterInvalid) {
		t.Fatalf("deleted person expanded scope %v", err)
	}
	if err := service.DeletePhotoRecap(ctx, saved.Recap.ID, saved.Recap.Revision); err != nil {
		t.Fatal(err)
	}
	mustNoteCount(t, &models.Image{}, 3)
}

func TestPhotoMapPeopleANDAndRecapRevision(t *testing.T) {
	setupImageServiceTestDB(t)
	lat, lon := 0.0, 0.0
	both := photoMapFixture(t, "both.jpg", &lat, &lon, "2026-11-02")
	one := photoMapFixture(t, "one.jpg", &lat, &lon, "2026-11-02")
	people := []models.Person{{DisplayName: "甲"}, {DisplayName: "乙"}}
	if err := database.DB.Create(&people).Error; err != nil {
		t.Fatal(err)
	}
	for _, link := range []models.ImagePerson{{ImageID: both.ID, PersonID: people[0].ID}, {ImageID: both.ID, PersonID: people[1].ID}, {ImageID: one.ID, PersonID: people[0].ID}} {
		if err := database.DB.Create(&link).Error; err != nil {
			t.Fatal(err)
		}
	}
	selection := PhotoSelection{PersonIDs: []uint{people[0].ID, people[1].ID}, DateFrom: "2026-11-02", DateTo: "2026-11-02"}
	service := NewImageLibraryService()
	ctx := context.Background()
	points, err := service.QueryPhotoMap(ctx, PhotoMapRequest{Bounds: PhotoBounds{West: -180, South: -90, East: 180, North: 90}, Zoom: 0, PhotoSelection: selection})
	if err != nil || points.MatchedCount != 1 || points.Clusters[0].SampleImageID != both.ID {
		t.Fatalf("people OR instead of AND: %+v %v", points, err)
	}
	timeline, err := service.ListPhotoRecapImages(ctx, PhotoRecapImagesRequest{Selection: selection})
	if err != nil || len(timeline.Items) != 1 || timeline.Items[0].ID != both.ID {
		t.Fatalf("recap scope %+v %v", timeline, err)
	}
	first, err := service.SavePhotoRecap(ctx, PhotoRecapInput{Name: "同名回顾", Selection: selection})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SavePhotoRecap(ctx, PhotoRecapInput{Name: "同名回顾", Selection: selection}); err != nil {
		t.Fatal("same names should be allowed")
	}
	updated, err := service.SavePhotoRecap(ctx, PhotoRecapInput{ID: first.Recap.ID, ExpectedRevision: first.Recap.Revision, Name: "更新", Selection: selection})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SavePhotoRecap(ctx, PhotoRecapInput{ID: first.Recap.ID, ExpectedRevision: first.Recap.Revision, Name: "旧覆盖", Selection: selection}); !errors.Is(err, ErrPhotoRecapConflict) {
		t.Fatalf("stale update %v", err)
	}
	if err := service.DeletePhotoRecap(ctx, first.Recap.ID, first.Recap.Revision); !errors.Is(err, ErrPhotoRecapConflict) {
		t.Fatalf("stale delete %v", err)
	}
	if err := service.DeletePhotoRecap(ctx, updated.Recap.ID, updated.Recap.Revision); err != nil {
		t.Fatal(err)
	}
	mustNoteCount(t, &models.Image{}, 2)
	mustNoteCount(t, &models.ImageViewEvent{}, 0)
}
