package services

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestScanDirectoryProgressBeforeCompletionAndSameFiltering(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	excluded := filepath.Join(root, "excluded")
	if err := database.DB.Model(&models.Settings{}).Where("id > 0").Update("scan_exclude_paths", excluded).Error; err != nil {
		t.Fatal(err)
	}
	// More than one read batch, plus every special exclusion used by the scanner.
	for i := 0; i < 270; i++ {
		path := filepath.Join(root, fmt.Sprintf("video-%03d.mp4", i))
		mustCreateFile(t, path)
		mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
	}
	for _, name := range []string{"child/inside.mp4", "excluded/no.mp4", ".hidden/no.mp4", ".hidden.mp4", "trash/no.mp4", "partial.temp.mp4", "notes.txt"} {
		path := filepath.Join(root, name)
		mustCreateFile(t, path)
		mustSetFileModTime(t, path, time.Now().Add(-time.Hour))
	}
	mustCreateFile(t, filepath.Join(root, "recent.mp4"))
	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	svc := &VideoService{}
	want, err := svc.ScanDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	var snapshots []DirectoryScanProgress
	got, err := svc.ScanDirectoryWithProgress(root, func(p DirectoryScanProgress) {
		snapshots = append(snapshots, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) || len(got) != 271 {
		t.Fatalf("changed discovery/filtering: got %d, want %d", len(got), len(want))
	}
	if snapshots[0].Phase != "checking" || snapshots[0].Found != 0 {
		t.Fatalf("missing pre-I/O progress: %+v", snapshots[0])
	}
	foundBeforeChild := false
	for i, p := range snapshots {
		if i > 0 && (p.Found < snapshots[i-1].Found || p.Visited < snapshots[i-1].Visited) {
			t.Fatalf("progress went backwards: %+v", snapshots)
		}
		if p.CurrentPath == filepath.Join(root, "child") && p.Found == 270 {
			foundBeforeChild = true
		}
	}
	if !foundBeforeChild || snapshots[len(snapshots)-1].Found != len(got) {
		t.Fatalf("did not report discoveries before reading next directory: %+v", snapshots)
	}
}

func TestScanDirectoryProgressReadFailureDoesNotReturnPartialSuccess(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	child := filepath.Join(root, "unavailable")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatal(err)
	}
	// Simulate a share disappearing after stat, before opening a descendant.
	removed := false
	files, err := (&VideoService{}).ScanDirectoryWithProgress(root, func(p DirectoryScanProgress) {
		if !removed && p.Phase == "reading" && p.CurrentPath == child {
			if err := os.Remove(child); err != nil {
				t.Fatal(err)
			}
			removed = true
		}
	})
	if err == nil || files != nil {
		t.Fatalf("unreadable subtree must not be reconciled as an empty directory: files=%v err=%v", files, err)
	}
}
