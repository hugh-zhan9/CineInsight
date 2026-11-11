//go:build darwin || linux

package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func replacementProbeFixture(t *testing.T) (string, string, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	from, to := filepath.Join(dir, "original.dat"), filepath.Join(dir, "destination.dat")
	if err = os.WriteFile(from, []byte("original must remain untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "user-keep"), []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir, from, to
}
func assertReplacementProbeClean(t *testing.T, dir, from, to string) {
	t.Helper()
	raw, err := os.ReadFile(from)
	if err != nil || string(raw) != "original must remain untouched" {
		t.Fatal("original changed", err)
	}
	if _, err = os.Lstat(to); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("business destination created", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("probe paths leaked: %+v", entries)
	}
	keep, err := os.ReadFile(filepath.Join(dir, "user-keep"))
	if err != nil || string(keep) != "unrelated" {
		t.Fatal("unrelated user file changed")
	}
}
func TestEditReplacementEINVALRequiresUnsupportedSameVolumeProbe(t *testing.T) {
	for _, probeErr := range []error{syscall.EINVAL, syscall.ENOTSUP} {
		t.Run(probeErr.Error(), func(t *testing.T) {
			dir, from, to := replacementProbeFixture(t)
			calls := 0
			s := &VideoEditService{replacementRename: func(a, b string) error {
				calls++
				if a == from && b == to {
					return syscall.EINVAL
				}
				if !strings.HasPrefix(filepath.Base(filepath.Dir(a)), ".cineinsight-rename-probe-") || filepath.Dir(filepath.Dir(a)) != dir || filepath.Dir(a) != filepath.Dir(b) {
					t.Fatal("probe not in actual destination volume", a, b)
				}
				return probeErr
			}}
			err := s.renameReplacementChecked(from, to)
			if !replacementRenameUnsupported(err) || !errors.Is(err, syscall.EINVAL) || calls != 2 {
				t.Fatalf("missing confirmed unsupported result: %v calls=%d", err, calls)
			}
			assertReplacementProbeClean(t, dir, from, to)
		})
	}
}
func TestEditReplacementEINVALProbeSuccessKeepsOriginalError(t *testing.T) {
	dir, from, to := replacementProbeFixture(t)
	calls := 0
	s := &VideoEditService{replacementRename: func(a, b string) error {
		calls++
		if a == from {
			return syscall.EINVAL
		}
		return os.Rename(a, b)
	}}
	err := s.renameReplacementChecked(from, to)
	if !errors.Is(err, syscall.EINVAL) || replacementRenameUnsupported(err) || calls != 2 {
		t.Fatal("successful probe was misclassified", err, calls)
	}
	assertReplacementProbeClean(t, dir, from, to)
}
func TestEditReplacementEINVALProbePermissionAndIOAreNotCapabilities(t *testing.T) {
	for _, probeErr := range []error{syscall.EACCES, syscall.EPERM, syscall.EIO} {
		t.Run(probeErr.Error(), func(t *testing.T) {
			dir, from, to := replacementProbeFixture(t)
			s := &VideoEditService{replacementRename: func(a, b string) error {
				if a == from {
					return syscall.EINVAL
				}
				return probeErr
			}}
			err := s.renameReplacementChecked(from, to)
			if !errors.Is(err, syscall.EINVAL) || replacementRenameUnsupported(err) {
				t.Fatal("probe failure swallowed as unsupported", err)
			}
			assertReplacementProbeClean(t, dir, from, to)
		})
	}
}
func TestEditReplacementOrdinaryErrorsDoNotProbe(t *testing.T) {
	for _, original := range []error{syscall.EIO, syscall.EACCES, syscall.ENOENT} {
		t.Run(original.Error(), func(t *testing.T) {
			dir, from, to := replacementProbeFixture(t)
			calls := 0
			s := &VideoEditService{replacementRename: func(string, string) error { calls++; return original }}
			if err := s.renameReplacementChecked(from, to); err != original || calls != 1 {
				t.Fatal("ordinary error changed", err, calls)
			}
			assertReplacementProbeClean(t, dir, from, to)
		})
	}
}
func TestEditReplacementEINVALWithOccupiedDestinationDoesNotProbe(t *testing.T) {
	dir, from, to := replacementProbeFixture(t)
	if err := os.WriteFile(to, []byte("user target"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s := &VideoEditService{replacementRename: func(string, string) error { calls++; return syscall.EINVAL }}
	err := s.renameReplacementChecked(from, to)
	if !errors.Is(err, syscall.EINVAL) || replacementRenameUnsupported(err) || calls != 1 {
		t.Fatal("invalid real arguments triggered probe", err, calls)
	}
	raw, _ := os.ReadFile(to)
	if string(raw) != "user target" {
		t.Fatal("user target overwritten")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Fatal("unexpected paths created", entries)
	}
}
func TestEditReplacementProbeCleanupPreservesForeignFiles(t *testing.T) {
	_, from, to := replacementProbeFixture(t)
	foreign := ""
	s := &VideoEditService{replacementRename: func(a, b string) error {
		if a == from {
			return syscall.EINVAL
		}
		foreign = b
		if err := os.WriteFile(b, []byte("foreign file"), 0600); err != nil {
			t.Fatal(err)
		}
		return syscall.EINVAL
	}}
	err := s.renameReplacementChecked(from, to)
	if !errors.Is(err, syscall.EINVAL) || replacementRenameUnsupported(err) {
		t.Fatal("foreign probe file was accepted", err)
	}
	raw, e := os.ReadFile(foreign)
	if e != nil || string(raw) != "foreign file" {
		t.Fatal("cleanup removed a foreign file", e)
	}
	if _, e = os.Lstat(filepath.Join(filepath.Dir(foreign), "source")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("owned source was not cleaned", e)
	}
	original, _ := os.ReadFile(from)
	if string(original) != "original must remain untouched" {
		t.Fatal("original changed")
	}
}

func TestEditReplacementProbeCleanupUnsupportedIsNotRenameEvidence(t *testing.T) {
	err := &replacementRenameProbeError{original: syscall.EINVAL, detail: syscall.ENOTSUP}
	if !errors.Is(err, syscall.EINVAL) || replacementRenameUnsupported(err) {
		t.Fatal("cleanup unsupported error became a rename capability", err)
	}
}

func TestEditReplacementConfirmedEINVALUsesCopyPublication(t *testing.T) {
	s, item, plan, staged := replacementFixture(t)
	newSHA, err := replacementSHA(staged)
	if err != nil {
		t.Fatal(err)
	}
	s.replacementRename = func(string, string) error { return syscall.EINVAL }
	if err = s.publishEditReplacement(context.Background(), item, plan, staged); err != nil {
		t.Fatal(err)
	}
	if !replacementHasSHA(plan.Sources[0].Path, newSHA) {
		t.Fatal("confirmed EINVAL did not publish complete copy")
	}
	current, err := loadEditItemByID(context.Background(), item.ID)
	if err != nil || current.Status != "completed" || current.OutputVideoID == nil || *current.OutputVideoID != plan.Sources[0].VideoID {
		t.Fatal(current, err)
	}
	dirs, err := filepath.Glob(filepath.Join(plan.OutputDir, ".cineinsight-rename-probe-*"))
	if err != nil || len(dirs) != 0 {
		t.Fatal("probe directories remain", dirs, err)
	}
}
func TestEditReplacementUnconfirmedEINVALLeavesOriginalUntouched(t *testing.T) {
	for _, mode := range []string{"probe_supported", "probe_io"} {
		t.Run(mode, func(t *testing.T) {
			s, item, plan, staged := replacementFixture(t)
			s.replacementRename = func(from, to string) error {
				if strings.HasPrefix(filepath.Base(filepath.Dir(from)), ".cineinsight-rename-probe-") {
					if mode == "probe_supported" {
						return os.Rename(from, to)
					}
					return syscall.EIO
				}
				return syscall.EINVAL
			}
			err := s.publishEditReplacement(context.Background(), item, plan, staged)
			if !errors.Is(err, syscall.EINVAL) || replacementRenameUnsupported(err) {
				t.Fatal("unconfirmed error lost or swallowed", err)
			}
			if !replacementHasSHA(plan.Sources[0].Path, plan.Sources[0].SHA256) {
				t.Fatal("unconfirmed EINVAL changed original")
			}
			dirs, e := filepath.Glob(filepath.Join(plan.OutputDir, ".cineinsight-rename-probe-*"))
			if e != nil || len(dirs) != 0 {
				t.Fatal("probe directories remain", dirs, e)
			}
		})
	}
}
