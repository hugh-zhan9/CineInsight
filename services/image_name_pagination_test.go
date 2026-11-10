package services

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestImagePageNameDirectoryPagination(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageLibraryService()
	// Equal names can occur in imported metadata; IDs break ties. A blank
	// name remains a valid sort key and must survive the cursor round trip.
	var ids []uint
	for i, name := range []string{"z.jpg", "a.jpg", "a.jpg", ""} {
		image := models.Image{Name: name, Path: fmt.Sprintf("/photos/%d.jpg", i), Directory: "/photos", Format: "jpg"}
		if err := database.DB.Create(&image).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, image.ID)
	}
	for _, image := range []models.Image{
		{Name: "b.jpg", Path: "/photos/child/b.jpg", Directory: "/photos/child"},
		{Name: "c.jpg", Path: "/photos/c.jpg", Directory: "/photos", IsStale: true},
	} {
		if err := database.DB.Create(&image).Error; err != nil {
			t.Fatal(err)
		}
	}
	removed := models.Image{Name: "d.jpg", Path: "/photos/d.jpg", Directory: "/photos"}
	if err := database.DB.Create(&removed).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&removed).Error; err != nil {
		t.Fatal(err)
	}
	var got []uint
	var cursor *ImageCursor
	for i := 0; i < 5; i++ {
		page, err := svc.SearchImagePage(ImagePageRequest{Filter: ImageFilter{Directory: "/photos", SortMode: "name"}, Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, image := range page.Images {
			got = append(got, image.ID)
		}
		cursor = page.NextCursor
		if cursor == nil {
			break
		}
		encoded, err := json.Marshal(cursor)
		if err != nil {
			t.Fatal(err)
		}
		cursor = &ImageCursor{}
		if err := json.Unmarshal(encoded, cursor); err != nil {
			t.Fatal(err)
		}
	}
	if want := []uint{ids[3], ids[2], ids[1], ids[0]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	empty, err := svc.SearchImagePage(ImagePageRequest{Filter: ImageFilter{Directory: "/empty", SortMode: "name"}, Limit: 1})
	if err != nil || len(empty.Images) != 0 || empty.NextCursor != nil {
		t.Fatalf("empty %+v %v", empty, err)
	}
}
