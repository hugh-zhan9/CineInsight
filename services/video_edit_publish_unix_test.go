//go:build darwin || linux

package services

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"gorm.io/gorm"
	"video-master/models"
)

func TestEditRenameUnsupportedFilesystem(t *testing.T) {
	for _, cause := range []error{unix.ENOTSUP, unix.ENOSYS, unix.EACCES, unix.EIO, unix.EXDEV} {
		t.Run(cause.Error(), func(t *testing.T) {
			dir := t.TempDir()
			source, target := filepath.Join(dir, "staged"), filepath.Join(dir, "output")
			writeEditTestFile(t, source, "complete output")
			err := editRenameNoReplaceWith(source, target, func(string, string) error {
				return &os.LinkError{Op: "rename", Old: source, New: target, Err: cause}
			})
			if cause == unix.ENOTSUP || cause == unix.ENOSYS {
				if err != nil {
					t.Fatal(err)
				}
				if got := readFileString(t, target); got != "complete output" {
					t.Fatal(got)
				}
				if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("source not removed: %v", err)
				}
			} else {
				if !errors.Is(err, cause) {
					t.Fatalf("lost original error: %v", err)
				}
				if got := readFileString(t, source); got != "complete output" {
					t.Fatal(got)
				}
				if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unexpected target: %v", err)
				}
			}
		})
	}
}

func TestEditCopyFallbackNeverOverwrites(t *testing.T) {
	for _, kind := range []string{"file", "directory", "symlink", "dangling_symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			source, target, other := filepath.Join(dir, "staged"), filepath.Join(dir, "output"), filepath.Join(dir, "other")
			writeEditTestFile(t, source, "complete output")
			writeEditTestFile(t, other, "someone else")
			switch kind {
			case "file":
				writeEditTestFile(t, target, "someone else")
			case "directory":
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(other, target); err != nil {
					t.Fatal(err)
				}
			case "dangling_symlink":
				if err := os.Symlink(filepath.Join(dir, "missing"), target); err != nil {
					t.Fatal(err)
				}
			}
			err := editRenameNoReplaceWith(source, target, func(string, string) error { return unix.ENOTSUP })
			if !errors.Is(err, os.ErrExist) {
				t.Fatalf("expected collision: %v", err)
			}
			if _, err := os.Lstat(target); err != nil {
				t.Fatal(err)
			}
			if got := readFileString(t, other); got != "someone else" {
				t.Fatal(got)
			}
			if kind == "file" && readFileString(t, target) != "someone else" {
				t.Fatal("overwritten")
			}
			if got := readFileString(t, source); got != "complete output" {
				t.Fatal(got)
			}
		})
	}
}

func TestEditCopyFallbackFailureRetainsSource(t *testing.T) {
	for _, phase := range []string{"create_target", "remove_source"} {
		t.Run(phase, func(t *testing.T) {
			if phase == "remove_source" && os.Geteuid() == 0 {
				t.Skip("requires filesystem permission enforcement")
			}
			sourceDir, outputDir := t.TempDir(), t.TempDir()
			source, target := filepath.Join(sourceDir, "staged"), filepath.Join(outputDir, "output")
			writeEditTestFile(t, source, "complete output")
			if phase == "create_target" {
				target = filepath.Join(outputDir, "missing", "output")
			} else {
				if err := os.Chmod(sourceDir, 0500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(sourceDir, 0700) })
			}
			err := editRenameNoReplaceWith(source, target, func(string, string) error { return unix.ENOTSUP })
			if err == nil {
				t.Fatal("expected filesystem error")
			}
			if readFileString(t, source) != "complete output" {
				t.Fatal("source lost")
			}
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed target retained: %v", err)
			}
		})
	}
}

func TestVideoEditPublishCopyFallback(t *testing.T) {
	for _, scenario := range []string{"success", "race", "transaction_failure", "slow_copy"} {
		t.Run(scenario, func(t *testing.T) {
			s, media := newFakeEditService(t)
			s.renamePublishFile = func(source, target string) error {
				err := editRenameNoReplaceWith(source, target, func(string, string) error { return unix.ENOTSUP })
				if scenario == "slow_copy" {
					time.Sleep(3 * s.publishTimeout)
				}
				return err
			}
			if scenario == "slow_copy" {
				s.publishTimeout = 100 * time.Millisecond
			}
			if scenario == "transaction_failure" {
				s.publishTxHook = func(*gorm.DB) error { return errors.New("injected failure") }
			}
			raced := ""
			if scenario == "race" {
				s.beforePublishRename = func(target string) {
					if raced == "" {
						raced = target
						writeEditTestFile(t, target, "someone else")
					}
				}
			}
			view, originals := queuedMerge(t, s, media)
			done := waitEditProject(t, s, view.ID, 10*time.Second, models.VideoEditStatusCompleted, models.VideoEditStatusFailed)
			if scenario == "transaction_failure" {
				if !strings.Contains(done.Items[0].ErrorMessage, "injected failure") {
					t.Fatalf("wrong failure: %+v", done.Items[0])
				}
				if done.Items[0].ErrorCode != "publish_failed" || done.Items[0].OutputVideoID != nil {
					t.Fatalf("%+v", done.Items[0])
				}
				target := filepath.Join(filepath.Dir(originals[0].Path), "a (合并).mkv")
				if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("target not rolled back: %v", err)
				}
			} else {
				if done.Status != models.VideoEditStatusCompleted {
					t.Fatalf("%+v", done.Items[0])
				}
				_, target := outputPathOf(t, done, 1)
				if scenario == "race" && (target == raced || readFileString(t, raced) != "someone else") {
					t.Fatal("collision overwritten")
				}
			}
			if readFileString(t, originals[0].Path) != "source:a.mkv" {
				t.Fatal("source changed")
			}
		})
	}
}
