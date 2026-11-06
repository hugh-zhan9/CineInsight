package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestResolveVisiblePlaybackSource(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewVideoService(nil)
	for _, tc := range []struct {
		name                    string
		outside, stale, missing bool
	}{{name: "visible"}, {name: "outside", outside: true}, {name: "stale", stale: true}, {name: "missing", missing: true}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := root
			if tc.outside {
				dir = t.TempDir()
			}
			path := filepath.Join(dir, tc.name+".avi")
			if !tc.missing {
				if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			row := models.Video{Name: tc.name, Path: path, Directory: dir, IsStale: tc.stale}
			if err := database.DB.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			source, err := service.ResolveVisibleVideoForPlayback(context.Background(), row.ID)
			if tc.outside || tc.stale || tc.missing {
				if err == nil {
					t.Fatal("ineligible source accepted")
				}
				return
			}
			if err != nil || source.VideoID != row.ID || source.SourceVersion == "" {
				t.Fatalf("source=%+v error=%v", source, err)
			}
		})
	}
}
