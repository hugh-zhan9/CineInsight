package perfdiag

import (
	"sync/atomic"
	"time"
)

// Start measures only while the independent local performance sink is enabled.
func Start() time.Time {
	if !Enabled() {
		return time.Time{}
	}
	return time.Now()
}

// Stage names are fixed source constants, never media labels or identifiers.
type Stage struct {
	Component string
	Name      string
	Slow      time.Duration
	Cooldown  time.Duration
	last      atomic.Int64
}

var epoch = time.Now()

// Record never receives paths, URLs, IDs, or errors. Slow=0 allows milestone
// sampling where the caller already bounds the number of records.
func (s *Stage) Record(start, end time.Time, failed bool) {
	if start.IsZero() || !Enabled() {
		return
	}
	elapsed := end.Sub(start)
	if elapsed < 0 || (!failed && elapsed < s.Slow) {
		return
	}
	if s.Cooldown > 0 {
		now := time.Since(epoch).Nanoseconds() + 1
		for {
			last := s.last.Load()
			if last != 0 && now-last < s.Cooldown.Nanoseconds() {
				return
			}
			if s.last.CompareAndSwap(last, now) {
				break
			}
		}
	}
	outcome := "ok"
	if failed {
		outcome = "error"
	}
	Printf("perf component=%s stage=%s duration_ms=%d status=%s", s.Component, s.Name, elapsed.Milliseconds(), outcome)
}

func (s *Stage) End(start time.Time, failed bool) {
	if !start.IsZero() {
		s.Record(start, time.Now(), failed)
	}
}
