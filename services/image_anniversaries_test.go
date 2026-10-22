package services

import (
	"fmt"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestImageAnniversariesCalendarAndCursor(t *testing.T) {
	setupVideoServiceTestDB(t)
	dates := []string{"2020-02-29", "2024-02-29", "2024-02-29", "2024-02-28", "2028-02-29"}
	for i, d := range dates {
		at, _ := time.ParseInLocation("2006-01-02", d, time.Local)
		im := models.Image{Name: fmt.Sprint(i), Path: fmt.Sprintf("/anniversary/%d.jpg", i), TakenAt: &at}
		if err := database.DB.Create(&im).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := NewImageLibraryService()
	p, err := svc.ListImageAnniversaries(ImageAnniversaryRequest{Date: "2028-02-29", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 3 || len(p.Items) != 1 || p.NextCursor == nil {
		t.Fatalf("wrong leap day page: %+v", p)
	}
	ids := map[uint]bool{p.Items[0].ID: true}
	for p.NextCursor != nil {
		p, err = svc.ListImageAnniversaries(ImageAnniversaryRequest{Date: "2028-02-29", Limit: 1, Cursor: p.NextCursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range p.Items {
			if ids[im.ID] {
				t.Fatal("duplicate page")
			}
			ids[im.ID] = true
		}
	}
	if len(ids) != 3 {
		t.Fatalf("missing rows %v", ids)
	}
	if _, err = svc.ListImageAnniversaries(ImageAnniversaryRequest{Date: "2025-02-29"}); err == nil {
		t.Fatal("invalid leap day accepted")
	}
	p, err = svc.ListImageAnniversaries(ImageAnniversaryRequest{Date: "2028-02-29", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ListImageAnniversaries(ImageAnniversaryRequest{Date: "2028-02-28", Cursor: p.NextCursor}); err == nil {
		t.Fatal("changed filter accepted")
	}
	p, err = svc.ListImageAnniversaries(ImageAnniversaryRequest{Date: "2028-02-28"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 1 {
		t.Fatal("leap date leaked into Feb 28")
	}
}

func TestImageAnniversariesManyYearsRemainBounded(t *testing.T) {
	setupVideoServiceTestDB(t)
	for year := 1500; year < 1905; year++ {
		at := time.Date(year, 1, 1, 12, 0, 0, 0, time.Local)
		row := models.Image{Name: "old.jpg", Path: fmt.Sprintf("/many/%d.jpg", year), TakenAt: &at}
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	page, err := NewImageLibraryService().ListImageAnniversaries(ImageAnniversaryRequest{Date: "9999-01-01", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 405 || len(page.Items) != 2 || page.Items[0].TakenAt.Year() != 1904 {
		t.Fatalf("many-year query %+v", page)
	}
}
