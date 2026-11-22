package perfdiag

import (
	"bytes"
	"io"
	"log"
	"strings"
	"testing"
	"time"
)

func TestMediaDiagnosticsRespectLoggerAndBoundSamples(t *testing.T) {
	writer := log.Writer()
	t.Cleanup(func() { log.SetOutput(writer) })
	stage := Stage{Component: "photo", Name: "test_read", Slow: 80 * time.Millisecond, Cooldown: time.Hour}
	log.SetOutput(io.Discard)
	if !Start().IsZero() {
		t.Fatal("disabled logging still started media timing")
	}
	logs := &bytes.Buffer{}
	log.SetOutput(logs)
	start := Start()
	if start.IsZero() {
		t.Fatal("enabled logging did not start media timing")
	}
	stage.Record(start, start.Add(time.Millisecond), false)
	if logs.Len() != 0 {
		t.Fatalf("fast sample logged: %s", logs)
	}
	stage.Record(start, start.Add(95*time.Millisecond), false)
	stage.Record(start, start.Add(time.Second), true)
	if got := logs.String(); strings.Count(got, "\n") != 1 ||
		!strings.Contains(got, "perf component=photo stage=test_read duration_ms=95 status=ok") {
		t.Fatalf("expected one sanitized sample, got %q", got)
	}
}
