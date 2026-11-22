package main

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/internal/perfdiag"
)

func TestPerformanceLogRoutingAndBridgePrivacy(t *testing.T) {
	var system bytes.Buffer
	old := log.Writer()
	log.SetOutput(&system)
	t.Cleanup(func() { log.SetOutput(old); _ = perfdiag.Close() })
	path := filepath.Join(t.TempDir(), "perf.log")
	if err := perfdiag.Configure(path, true); err != nil {
		t.Fatal(err)
	}
	trace := startupPerfTrace{started: time.Now(), newApp: 2 * time.Millisecond, database: 6 * time.Millisecond, recovery: 4 * time.Millisecond}
	trace.flush() // startup has not finished: do not burn the once guard
	trace.toWindow = 12 * time.Millisecond
	trace.flush()
	trace.flush()
	perfLocal("photo_library", "next_page", time.Now(), nil)
	perfLocal("photo_library", "next_page", time.Now(), errors.New("private path and credential"))
	recordFrontendMediaPerf("photo_view_load_observed 300")
	for _, rejected := range []string{"photo_view_load_observed 401\nsecret", "evil_stage 500", "photo_view_load_observed -1", "poster_load_observed 900001", "poster_load_observed 20", "poster_load_observed +160"} {
		recordFrontendMediaPerf(rejected)
	}
	if err := perfdiag.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Count(text, "perf component=") != 3 || !strings.Contains(text, "database_ms=6 recovery_ms=4") ||
		!strings.Contains(text, "status=error") || !strings.Contains(text, "photo_view_load_observed duration_ms=300") || strings.Contains(text, "private path") || system.Len() != 0 {
		t.Fatalf("unexpected file %q; system output %q", text, system.String())
	}
}
