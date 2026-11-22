package main

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
	"time"
)

func TestStartupPerfTraceWritesOnceOnly(t *testing.T) {
	var out bytes.Buffer
	old := log.Writer()
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(old) })
	trace := startupPerfTrace{started: time.Now(), newApp: 2 * time.Millisecond, database: 6 * time.Millisecond, recovery: 4 * time.Millisecond, toWindow: 12 * time.Millisecond}
	trace.flush()
	trace.flush()
	if text := out.String(); strings.Count(text, "perf component=startup") != 1 || !strings.Contains(text, "database_ms=6 recovery_ms=4") {
		t.Fatalf("unexpected trace: %s", text)
	}
}

func TestPerfLocalDoesNotLogInputErrorText(t *testing.T) {
	var out bytes.Buffer
	old := log.Writer()
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(old) })
	perfLocal("photo_library", "next_page", time.Now(), nil)
	if out.Len() != 0 {
		t.Fatal("fast background request was logged")
	}
	perfLocal("photo_library", "next_page", time.Now(), errors.New("private path and credential"))
	if text := out.String(); !strings.Contains(text, "status=error") || strings.Contains(text, "private path") {
		t.Fatalf("unexpected trace: %s", text)
	}
}
