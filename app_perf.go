package main

import (
	"strconv"
	"strings"
	"sync"
	"time"
	"video-master/internal/perfdiag"
)

// Startup is measured before local log settings are loaded. Emit once in
// the independent performance sink, after the trace is complete.
type startupPerfTrace struct {
	once                                 sync.Once
	started                              time.Time
	newApp, database, recovery, toWindow time.Duration
}

func (p *startupPerfTrace) flush() {
	if p.started.IsZero() || p.toWindow == 0 || !perfdiag.Enabled() {
		return
	}
	p.once.Do(func() {
		perfdiag.Printf("perf component=startup stage=before_window duration_ms=%d new_app_ms=%d database_ms=%d recovery_ms=%d", p.toWindow.Milliseconds(), p.newApp.Milliseconds(), p.database.Milliseconds(), p.recovery.Milliseconds())
	})
}

// These call sites pass fixed component/stage literals. Never log filters,
// source identifiers or error text here.
func perfLocal(component, stage string, started time.Time, err error) {
	elapsed := time.Since(started)
	if elapsed < 150*time.Millisecond && err == nil && stage != "first_page" {
		return
	}
	status := "ok"
	if err != nil {
		status = "error"
	}
	perfdiag.Printf("perf component=%s stage=%s duration_ms=%d status=%s", component, stage, elapsed.Milliseconds(), status)
}

var frontendMediaPerfStages = map[string]*perfdiag.Stage{
	"photo_thumbnail_load_observed": {Component: "webview_media", Name: "photo_thumbnail_load_observed", Cooldown: 5 * time.Second},
	"photo_view_load_observed":      {Component: "webview_media", Name: "photo_view_load_observed", Cooldown: 5 * time.Second},
	"poster_load_observed":          {Component: "webview_media", Name: "poster_load_observed", Cooldown: 5 * time.Second},
	"artwork_load_observed":         {Component: "webview_media", Name: "artwork_load_observed", Cooldown: 5 * time.Second},
}

// The existing bridge is used as transport only. Reject arbitrary payloads,
// including URLs, paths, newlines and identities; no fallback to system logs.
func recordFrontendMediaPerf(input string) {
	if !perfdiag.Enabled() {
		return
	}
	stage, value, found := strings.Cut(input, " ")
	if !found || len(value) == 0 || len(value) > 6 {
		return
	}
	metric := frontendMediaPerfStages[stage]
	if metric == nil {
		return
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return
		}
	}
	ms, err := strconv.Atoi(value)
	if err != nil || ms < 150 || ms > 900000 {
		return
	}
	end := time.Now()
	metric.Record(end.Add(-time.Duration(ms)*time.Millisecond), end, false)
}
