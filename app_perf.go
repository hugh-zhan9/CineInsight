package main

import (
	"log"
	"sync"
	"time"
)

// Startup is measured before app.log becomes available. Emit only when the
// already-existing local logging setting opens the log file.
type startupPerfTrace struct {
	once                                 sync.Once
	started                              time.Time
	newApp, database, recovery, toWindow time.Duration
}

func (p *startupPerfTrace) flush() {
	if p.started.IsZero() {
		return
	}
	p.once.Do(func() {
		log.Printf("perf component=startup stage=before_window duration_ms=%d new_app_ms=%d database_ms=%d recovery_ms=%d", p.toWindow.Milliseconds(), p.newApp.Milliseconds(), p.database.Milliseconds(), p.recovery.Milliseconds())
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
	log.Printf("perf component=%s stage=%s duration_ms=%d status=%s", component, stage, elapsed.Milliseconds(), status)
}
