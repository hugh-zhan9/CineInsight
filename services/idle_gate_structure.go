package services

import (
	"context"
	"time"
)

// FreshStructureDecision deliberately bypasses the shared 30-second cache.
// Callers supply a deadline and current settings; other task gates are unchanged.
func (g *IdleGate) FreshStructureDecision(ctx context.Context, settings IdleSchedulerSettings) (bool, string) {
	g.mu.Lock()
	probe, supported, now := g.probe, g.probeSupport, g.now
	g.mu.Unlock()
	if !supported || probe == nil {
		return false, "probe_unavailable"
	}
	sample, err := probe(ctx)
	if err != nil {
		return false, IdleWaitReasonProbeFailed
	}
	settings = normalizeIdleSchedulerSettings(settings)
	if sample.Idle < time.Duration(settings.ThresholdMinutes)*time.Minute {
		return false, IdleWaitReasonUserActive
	}
	if settings.RequireACPower && !sample.OnACPower {
		return false, IdleWaitReasonOnBattery
	}
	if !withinIdleWindow(now(), settings.WindowStart, settings.WindowEnd) {
		return false, IdleWaitReasonOutsideWindow
	}
	return true, ""
}
