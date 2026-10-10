package services

import (
	"context"
	"errors"
	"testing"
	"video-master/models"
)

// An end reached by seeking is not a natural end (TC-15/AC-09): the scrub to
// the end is refused without advancing, recording a view, or desynchronising
// the cycle. Playing again in the same cycle and reaching the end by playback
// then advances exactly once and records the view.
func TestQueueSeekToEndIsIgnoredThenPlayedEndAdvancesOnce(t *testing.T) {
	for _, landingPlaying := range []bool{false, true} {
		t.Run(map[bool]string{false: "paused", true: "playing"}[landingPlaying], func(t *testing.T) {
			s, v := queueRuntimeFixture(t, true)
			session := startQueueInline(t, s)
			seq := uint64(0)
			send := func(kind string, pos float64) error {
				seq++
				return s.ReportInline(context.Background(), queueEvent(session, seq, kind, pos))
			}
			must := func(kind string, pos float64) {
				t.Helper()
				if err := send(kind, pos); err != nil {
					t.Fatal(kind, err)
				}
			}
			must("loaded", 0)
			must("playing", 0)
			must("progress", 1)
			must("seeking", 1)
			must("seeked", 120)
			if landingPlaying {
				must("playing", 120) // element still unpaused when seeked fired
			}
			must("paused", 120)
			if err := send("ended", 120); !errors.Is(err, ErrQueueEndIgnored) {
				t.Fatalf("seek-to-end consumed as natural end: %v", err)
			}
			p := queueSnapshot(t, s)
			if p.Current.VideoID != v[0].ID || p.Session == nil || p.Session.Token != session.Token || p.Warning != "" {
				t.Fatalf("seek-to-end advanced or failed: %+v warning=%q", p.State, p.Warning)
			}
			mustNoteCount(t, &models.ViewingDiaryEntry{}, 0)
			// play() after ended restarts from 0 in the same view cycle.
			must("seeking", 0)
			must("seeked", 0)
			must("playing", 0)
			must("progress", 60)
			must("progress", 119)
			must("paused", 120)
			must("ended", 120)
			p = waitQueueSnapshot(t, s, func(p *QueueSnapshot) bool { return p.Session != nil && p.Session.Token != session.Token })
			if p.Current.VideoID != v[1].ID {
				t.Fatalf("played end did not advance exactly once: %+v", p.Current)
			}
			mustNoteCount(t, &models.ViewingDiaryEntry{}, 1)
		})
	}
}

// IINA reports the same facts: paused, seek past the end, eof. The eof is
// refused; unpausing continues the same cycle and a resumed playback that
// reaches eof counts once.
func TestQueueIINAShapedSeekPastEndThenResumedEndCountsOnce(t *testing.T) {
	s, v := queueRuntimeFixture(t, false)
	session := startQueueInline(t, s)
	for i, step := range []struct {
		kind string
		pos  float64
	}{{"loaded", 0}, {"playing", 0}, {"progress", 50}, {"paused", 50}, {"seeking", 50}, {"seeked", 119.9}} {
		// Frame-stepping while paused then moves time-pos to 119.96 without playback.
		mustQueueEvent(t, s, queueEvent(session, uint64(i+1), step.kind, step.pos))
	}
	if err := s.ReportInline(context.Background(), queueEvent(session, 7, "ended", 119.96)); !errors.Is(err, ErrQueueEndIgnored) {
		t.Fatalf("eof after paused seek consumed: %v", err)
	}
	mustQueueEvent(t, s, queueEvent(session, 8, "playing", 119.96))
	mustQueueEvent(t, s, queueEvent(session, 9, "seeking", 119.96))
	mustQueueEvent(t, s, queueEvent(session, 10, "seeked", 100))
	mustQueueEvent(t, s, queueEvent(session, 11, "playing", 100))
	mustQueueEvent(t, s, queueEvent(session, 12, "progress", 101))
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 0)
	mustQueueEvent(t, s, queueEvent(session, 13, "ended", 119.96))
	if p := queueSnapshot(t, s); p.State.Status != "ended" || p.Current.VideoID != v[0].ID {
		t.Fatalf("resumed eof not consumed once: %+v", p.State)
	}
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 1)
}

// An end the session refuses (still seeking, or never started) is reported back
// explicitly so a player keeps its current view cycle; it is not a failure and
// later facts of the same cycle keep being accepted.
func TestQueueIgnoredEndIsExplicitAndKeepsCycle(t *testing.T) {
	s, v := queueRuntimeFixture(t, false)
	session := startQueueInline(t, s)
	mustQueueEvent(t, s, queueEvent(session, 1, "loaded", 0))
	if err := s.ReportInline(context.Background(), queueEvent(session, 2, "ended", 120)); !errors.Is(err, ErrQueueEndIgnored) {
		t.Fatalf("end before start: %v", err)
	}
	mustQueueEvent(t, s, queueEvent(session, 3, "playing", 0))
	mustQueueEvent(t, s, queueEvent(session, 4, "seeking", 119.9))
	if err := s.ReportInline(context.Background(), queueEvent(session, 5, "ended", 120)); !errors.Is(err, ErrQueueEndIgnored) {
		t.Fatalf("end during seek: %v", err)
	}
	p := queueSnapshot(t, s)
	if p.State.Status != "playing" || p.Session == nil || p.Session.Token != session.Token || p.Warning != "" {
		t.Fatalf("ignored end changed the session: %+v warning=%q", p.State, p.Warning)
	}
	mustQueueEvent(t, s, queueEvent(session, 6, "seeked", 10))
	mustQueueEvent(t, s, queueEvent(session, 7, "playing", 10))
	mustQueueEvent(t, s, queueEvent(session, 8, "progress", 20))
	mustQueueEvent(t, s, queueEvent(session, 9, "ended", 120))
	p = queueSnapshot(t, s)
	if p.State.Status != "ended" || p.Current.VideoID != v[0].ID {
		t.Fatalf("genuine end after ignored one not consumed: %+v", p.State)
	}
	if err := s.ReportInline(context.Background(), queueEvent(session, 10, "ended", 120)); err != nil {
		t.Fatalf("duplicate consumed end must stay an idempotent no-op: %v", err)
	}
	replay := *session
	replay.ViewCycle++
	mustQueueEvent(t, s, queueEvent(&replay, 11, "playing", 0))
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 1)
}
