package perfdiag

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func readDiagnosticLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func configureDiagnosticLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "perf.log")
	t.Cleanup(func() {
		if err := Close(); err != nil {
			t.Error(err)
		}
	})
	if err := Configure(path, true); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDedicatedSinkDefaultsOffAndDoesNotRedirectStandardLogger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "perf.log")
	var standard bytes.Buffer
	writer := log.Writer()
	log.SetOutput(&standard)
	t.Cleanup(func() { log.SetOutput(writer); _ = Close() })

	if Enabled() {
		t.Fatal("performance diagnostics enabled by default")
	}
	if err := Configure(path, false); err != nil {
		t.Fatal(err)
	}
	Printf("perf skipped")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disabled configuration created a performance log")
	}
	if err := Configure(path, true); err != nil {
		t.Fatal(err)
	}
	log.Print("operational")
	Printf("perf component=photo duration_ms=%d", 77)
	if err := Configure(path, true); err != nil {
		t.Fatal(err)
	}
	Printf("perf second")
	if err := Close(); err != nil {
		t.Fatal(err)
	}
	Printf("perf skipped after disable")
	diagnostic := readDiagnosticLog(t, path)
	if strings.Count(diagnostic, "\n") != 2 || !strings.Contains(diagnostic, "duration_ms=77") ||
		!strings.Contains(diagnostic, "perf second") || strings.Contains(diagnostic, "operational") ||
		!strings.Contains(standard.String(), "operational") || strings.Contains(standard.String(), "perf") {
		t.Fatal("standard and performance logs were not independent")
	}
	if Enabled() {
		t.Fatal("closed diagnostic sink is still enabled")
	}
}

func TestDedicatedSinkRotatesAndBoundsFilesDuringConcurrentWrites(t *testing.T) {
	path := configureDiagnosticLog(t)
	text := strings.Repeat("x", 3300)
	var wg sync.WaitGroup
	for index := 0; index < 8; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for line := 0; line < 130; line++ {
				Printf("perf sample=%d_%d value=%s", index, line, text)
			}
		}(index)
	}
	wg.Wait()
	Printf("perf giant=%s", strings.Repeat("q", perfRecordLimit+1))
	Printf("perf bad\nnewline")
	Printf("perf end")
	for _, name := range []string{path, path + ".1"} {
		info, err := os.Lstat(name)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > perfFileLimit {
			t.Fatalf("invalid diagnostic file size/mode: %d, %s", info.Size(), info.Mode())
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatal("diagnostic file is not private")
		}
		data := readDiagnosticLog(t, name)
		if strings.Contains(data, "perf giant") || strings.Contains(data, "newline") {
			t.Fatal("invalid record was written")
		}
		if !strings.HasSuffix(data, "\n") {
			t.Fatal("diagnostic file ends mid-record")
		}
	}
	if !strings.Contains(readDiagnosticLog(t, path), "perf end") {
		t.Fatal("newest sample is missing")
	}
	names, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(names) != 2 {
		t.Fatal("rotation should retain at most one backup")
	}
}

func TestDedicatedSinkBoundsExistingFilesAndRejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "perf.log")
	over := bytes.Repeat([]byte("z"), int(perfFileLimit)+1)
	for _, name := range []string{path, path + ".1"} {
		if err := os.WriteFile(name, over, 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = Close() })
	if err := Configure(path, true); err != nil {
		t.Fatal(err)
	}
	Printf("perf new")
	if err := Close(); err != nil {
		t.Fatal(err)
	}
	if got := readDiagnosticLog(t, path); !strings.Contains(got, "perf new") || len(got) > 100 {
		t.Fatal("oversized active log was not reset")
	}
	if _, err := os.Lstat(path + ".1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oversized older backup was not discarded")
	}
	target := filepath.Join(dir, "not-a-log")
	if err := os.WriteFile(target, []byte("leave untouched"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Join(dir, "other.log"), path + ".1"} {
		if err := os.Symlink(target, name); err != nil {
			if runtime.GOOS == "windows" {
				t.Skip("symlink unavailable")
			}
			t.Fatal(err)
		}
		configured := strings.TrimSuffix(name, ".1")
		if err := Configure(configured, true); err == nil || Enabled() {
			t.Fatalf("enabled a symlink log slot: %s", err)
		}
	}
	if got := readDiagnosticLog(t, target); got != "leave untouched" {
		t.Fatalf("symlink target changed: %q", got)
	}
}

type gatedMetric struct{ entered, done chan struct{} }

func (m gatedMetric) String() string {
	close(m.entered)
	<-m.done
	return "old"
}

func TestDedicatedSinkDoesNotLeakInFlightEntriesIntoReconfiguration(t *testing.T) {
	old := configureDiagnosticLog(t)
	next := filepath.Join(t.TempDir(), "perf.log")
	metric := gatedMetric{entered: make(chan struct{}), done: make(chan struct{})}
	written := make(chan struct{})
	go func() {
		defer close(written)
		Printf("perf value=%s", metric)
	}()
	<-metric.entered
	err := Configure(next, true)
	close(metric.done)
	<-written
	if err != nil {
		t.Fatal(err)
	}
	Printf("perf current=1")
	if readDiagnosticLog(t, old) != "" {
		t.Fatal("entry finished after the old sink closed")
	}
	got := readDiagnosticLog(t, next)
	if strings.Contains(got, "old") || !strings.Contains(got, "current=1") {
		t.Fatal("in-flight entry crossed diagnostic destinations")
	}
}
