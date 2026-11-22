package perfdiag

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMediaDiagnosticsRespectIndependentSinkAndBoundSamples(t *testing.T) {
	writer := log.Writer()
	t.Cleanup(func() {
		_ = Close()
		log.SetOutput(writer)
	})
	stage := Stage{Component: "photo", Name: "test_read", Slow: 80 * time.Millisecond, Cooldown: time.Hour}
	log.SetOutput(io.Discard)
	if !Start().IsZero() {
		t.Fatal("disabled diagnostics still started media timing")
	}
	path := filepath.Join(t.TempDir(), "perf.log")
	if err := Configure(path, true); err != nil {
		t.Fatal(err)
	}
	start := Start()
	if start.IsZero() {
		t.Fatal("enabled diagnostics did not start media timing")
	}
	stage.Record(start, start.Add(time.Millisecond), false)
	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Fatalf("fast sample logged: %v, %v", info, err)
	}
	stage.Record(start, start.Add(95*time.Millisecond), false)
	stage.Record(start, start.Add(time.Second), true)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); strings.Count(got, "\n") != 1 ||
		!strings.Contains(got, "perf component=photo stage=test_read duration_ms=95 status=ok") {
		t.Fatalf("expected one sanitized sample, got %q", got)
	}
}
