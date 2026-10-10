package services

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func queueRuntimeFixture(t *testing.T, autoplay bool) (*PlaybackQueueService, []models.Video) {
	t.Helper()
	setupVideoServiceTestDB(t)
	videos := []models.Video{createProxyTestVideo(t, "one.mp4", 120), createProxyTestVideo(t, "two.mp4", 120), createProxyTestVideo(t, "three.mp4", 120)}
	proxy, _ := newProxyTestService(t)
	s := NewPlaybackQueueService(newProxyBackedVideoService(proxy), nil, nil)
	if err := s.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := s.Edit(context.Background(), QueueEdit{Action: "configure", ExpectedRevision: 1, Player: "inline", Autoplay: &autoplay}); err != nil {
		t.Fatal(err)
	}
	if err := s.Edit(context.Background(), QueueEdit{Action: "append_videos", ExpectedRevision: 2, VideoIDs: []uint{videos[0].ID, videos[1].ID, videos[2].ID}}); err != nil {
		t.Fatal(err)
	}
	return s, videos
}
func queueSnapshot(t *testing.T, s *PlaybackQueueService) *QueueSnapshot {
	t.Helper()
	p, err := s.Snapshot(context.Background(), QueueQuery{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func waitQueueSnapshot(t *testing.T, s *PlaybackQueueService, predicate func(*QueueSnapshot) bool) *QueueSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p := queueSnapshot(t, s)
		if predicate(p) {
			return p
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("queue condition did not become true")
	return nil
}
func startQueueInline(t *testing.T, s *PlaybackQueueService) *QueueSession {
	t.Helper()
	p := queueSnapshot(t, s)
	if err := s.Play(context.Background(), p.Items[0].ID, p.State.Revision); err != nil {
		t.Fatal(err)
	}
	return waitQueueSnapshot(t, s, func(p *QueueSnapshot) bool { return p.Session != nil }).Session
}
func queueEvent(session *QueueSession, seq uint64, kind string, pos float64) InlineQueueEvent {
	return InlineQueueEvent{Token: session.Token, VideoID: session.VideoID, SourceVersion: session.SourceVersion, ViewCycle: session.ViewCycle, Seq: seq, Kind: kind, Position: pos, Duration: 120}
}
func mustQueueEvent(t *testing.T, s *PlaybackQueueService, event InlineQueueEvent) {
	t.Helper()
	if err := s.ReportInline(context.Background(), event); err != nil {
		t.Fatal(event.Kind, err)
	}
}

func TestQueueManualNextAfterEndedAndRewatchFacts(t *testing.T) {
	s, v := queueRuntimeFixture(t, false)
	session := startQueueInline(t, s)
	mustQueueEvent(t, s, queueEvent(session, 1, "loaded", 0))
	mustQueueEvent(t, s, queueEvent(session, 2, "playing", 0))
	mustQueueEvent(t, s, queueEvent(session, 3, "ended", 120))
	mustQueueEvent(t, s, queueEvent(session, 4, "ended", 120))
	p := queueSnapshot(t, s)
	if p.State.Status != "ended" || p.Current.VideoID != v[0].ID {
		t.Fatalf("auto-off advanced: %+v", p.State)
	}
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 1)
	session.ViewCycle++
	mustQueueEvent(t, s, queueEvent(session, 5, "playing", 0))
	mustQueueEvent(t, s, queueEvent(session, 6, "ended", 120))
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 2)
	var video models.Video
	if err := database.DB.First(&video, v[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if video.PlayCount != 1 || !video.IsWatched {
		t.Fatalf("replay changed dispatch count or missed completion: %+v", video)
	}
	p = queueSnapshot(t, s)
	if err := s.Next(context.Background(), p.State.ActiveToken, p.State.Revision); err != nil {
		t.Fatal(err)
	}
	p = waitQueueSnapshot(t, s, func(p *QueueSnapshot) bool { return p.Session != nil && p.Session.Token != session.Token })
	if p.Current.VideoID != v[1].ID {
		t.Fatal("manual next after consumed EOF did not advance once")
	}
	if err := s.Next(context.Background(), session.Token, p.State.Revision); !errors.Is(err, ErrQueueChanged) {
		t.Fatalf("stale next: %v", err)
	}
}

func TestQueueNextAndNaturalEndAdvanceOnlyOnce(t *testing.T) {
	s, v := queueRuntimeFixture(t, true)
	session := startQueueInline(t, s)
	mustQueueEvent(t, s, queueEvent(session, 1, "loaded", 0))
	mustQueueEvent(t, s, queueEvent(session, 2, "playing", 0))
	p := queueSnapshot(t, s)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); results <- s.Next(context.Background(), session.Token, p.State.Revision) }()
	go func() {
		defer wg.Done()
		results <- s.ReportInline(context.Background(), queueEvent(session, 3, "ended", 120))
	}()
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil && !errors.Is(err, ErrQueueChanged) {
			t.Fatal(err)
		}
	}
	p = waitQueueSnapshot(t, s, func(p *QueueSnapshot) bool { return p.Session != nil && p.Session.Token != session.Token })
	if p.Current.VideoID != v[1].ID {
		t.Fatalf("double advance: %+v", p.Current)
	}
	_ = s.ReportInline(context.Background(), queueEvent(session, 4, "ended", 120))
	if queueSnapshot(t, s).Current.VideoID != v[1].ID {
		t.Fatal("old EOF advanced new session")
	}
}

func TestQueuePauseSeekStopAndErrorsNeverAdvance(t *testing.T) {
	s, v := queueRuntimeFixture(t, true)
	session := startQueueInline(t, s)
	mustQueueEvent(t, s, queueEvent(session, 1, "loaded", 0))
	mustQueueEvent(t, s, queueEvent(session, 2, "playing", 0))
	mustQueueEvent(t, s, queueEvent(session, 3, "seeking", 119))
	if err := s.ReportInline(context.Background(), queueEvent(session, 4, "ended", 120)); !errors.Is(err, ErrQueueEndIgnored) {
		t.Fatal("end during seek was not refused explicitly", err)
	}
	if queueSnapshot(t, s).Current.VideoID != v[0].ID {
		t.Fatal("seek advanced")
	}
	mustQueueEvent(t, s, queueEvent(session, 5, "paused", 119))
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 0)
	mustQueueEvent(t, s, queueEvent(session, 6, "error", 119))
	p := queueSnapshot(t, s)
	if p.State.Status != "failed" || p.Current.VideoID != v[0].ID {
		t.Fatalf("failure did not stay: %+v", p.State)
	}
	if err := s.ReportInline(context.Background(), queueEvent(session, 7, "playing", 0)); !errors.Is(err, ErrQueueChanged) {
		t.Fatal("failed session reactivated", err)
	}
	if err := s.Stop(context.Background(), session.Token); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background(), session.Token); err != nil {
		t.Fatal("stop not idempotent", err)
	}
	if _, _, _, err := s.Media(context.Background(), session.Token); !errors.Is(err, ErrQueueLeaseExpired) {
		t.Fatal(err)
	}
}

func TestQueueMediaTokenAndStopCloseReaders(t *testing.T) {
	s, _ := queueRuntimeFixture(t, false)
	session := startQueueInline(t, s)
	r, _, _, err := s.Media(context.Background(), session.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Media(context.Background(), "not-current"); !errors.Is(err, ErrQueueLeaseExpired) {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background(), session.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("body descriptor survived stop")
	}
	p := queueSnapshot(t, s)
	if p.Session != nil || p.State.Status != "stopped" || p.State.ActiveToken != "" {
		t.Fatalf("stop state: %+v", p)
	}
	if err := s.Play(context.Background(), p.Items[1].ID, p.State.Revision); err != nil {
		t.Fatal(err)
	}
	waitQueueSnapshot(t, s, func(p *QueueSnapshot) bool { return p.Session != nil })
	if err := s.Stop(context.Background(), session.Token); !errors.Is(err, ErrQueueChanged) {
		t.Fatal("old stop cancelled current", err)
	}
	latest := queueSnapshot(t, s)
	if latest.Session == nil || latest.Session.Token == session.Token {
		t.Fatal("old stop invalidated newer session")
	}
	if _, _, _, err := s.Media(context.Background(), latest.Session.Token); err != nil {
		t.Fatal("old stop closed newer descriptor", err)
	}
}

func TestQueueMissingNextStopsOnFailedItem(t *testing.T) {
	s, v := queueRuntimeFixture(t, true)
	session := startQueueInline(t, s)
	if err := database.DB.Delete(&v[1]).Error; err != nil {
		t.Fatal(err)
	}
	mustQueueEvent(t, s, queueEvent(session, 1, "loaded", 0))
	mustQueueEvent(t, s, queueEvent(session, 2, "playing", 0))
	mustQueueEvent(t, s, queueEvent(session, 3, "ended", 120))
	p := waitQueueSnapshot(t, s, func(p *QueueSnapshot) bool { return p.State.Status == "failed" })
	if p.Current.VideoID != v[1].ID || p.Current.SourceAvailable || p.Session != nil {
		t.Fatalf("failed next skipped or hid error: %+v", p)
	}
}

func TestQueueCloseRejectsLaterCallsAndPersistsInterrupted(t *testing.T) {
	s, _ := queueRuntimeFixture(t, false)
	session := startQueueInline(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(context.Background(), QueueQuery{}); !errors.Is(err, ErrPlaybackQuiesced) {
		t.Fatal(err)
	}
	if err := s.ReportInline(context.Background(), queueEvent(session, 1, "loaded", 0)); !errors.Is(err, ErrPlaybackQuiesced) {
		t.Fatal(err)
	}
	state, err := readQueueState(context.Background())
	if err != nil || state.Status != "interrupted" || state.ActiveToken != "" {
		t.Fatalf("shutdown state: %+v %v", state, err)
	}
}

func TestQueueRejectsWrongIdentityAndLateSequence(t *testing.T) {
	s, _ := queueRuntimeFixture(t, false)
	session := startQueueInline(t, s)
	mustQueueEvent(t, s, queueEvent(session, 1, "loaded", 0))
	mustQueueEvent(t, s, queueEvent(session, 2, "playing", 0))
	wrong := queueEvent(session, 3, "ended", 120)
	wrong.SourceVersion = "wrong"
	if err := s.ReportInline(context.Background(), wrong); !errors.Is(err, ErrQueueChanged) {
		t.Fatal(err)
	}
	mustQueueEvent(t, s, queueEvent(session, 4, "paused", 5))
	// A late or replayed seq (e.g. a remounted player restarting at 1) is
	// refused explicitly so the player can ask for a fresh play; it is not a
	// session failure.
	if err := s.ReportInline(context.Background(), queueEvent(session, 3, "ended", 120)); !errors.Is(err, ErrQueueStaleEvent) {
		t.Fatalf("late seq not refused explicitly: %v", err)
	}
	if p := queueSnapshot(t, s); p.State.Status != "paused" || p.Session == nil || p.Warning != "" {
		t.Fatalf("stale end changed state: %+v warning=%q", p.State, p.Warning)
	}
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 0)
}

func TestQueueStateWriteFailureStopsRuntimeWithoutRetry(t *testing.T) {
	s, _ := queueRuntimeFixture(t, false)
	session := startQueueInline(t, s)
	mustQueueEvent(t, s, queueEvent(session, 1, "loaded", 0))
	mustQueueEvent(t, s, queueEvent(session, 2, "playing", 0))
	name := "test:queue_state_write_failure"
	writes := 0
	database.DB.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "playback_queue_states" {
			writes++
			tx.AddError(errors.New("state write failed"))
		}
	})
	if err := s.ReportInline(context.Background(), queueEvent(session, 3, "paused", 5)); err == nil {
		t.Fatal("state error hidden")
	}
	database.DB.Callback().Update().Remove(name)
	if writes != 1 {
		t.Fatalf("state write retried %d times", writes)
	}
	p := queueSnapshot(t, s)
	if p.State.Status != "failed" || p.Warning == "" || p.Session != nil {
		t.Fatalf("failed runtime not exposed: %+v", p)
	}
}

func TestQueueStopStillStopsWhenDatabaseFails(t *testing.T) {
	for _, stage := range []string{"read", "write"} {
		t.Run(stage, func(t *testing.T) {
			s, _ := queueRuntimeFixture(t, false)
			session := startQueueInline(t, s)
			reader, _, _, err := s.Media(context.Background(), session.Token)
			if err != nil {
				t.Fatal(err)
			}
			name := "test:stop_database_failure"
			callback := func(tx *gorm.DB) {
				if tx.Statement.Table == "playback_queue_states" {
					tx.AddError(errors.New("database failed"))
				}
			}
			if stage == "read" {
				database.DB.Callback().Query().Before("gorm:query").Register(name, callback)
			} else {
				database.DB.Callback().Update().Before("gorm:update").Register(name, callback)
			}
			err = s.Stop(context.Background(), session.Token)
			if stage == "read" {
				database.DB.Callback().Query().Remove(name)
			} else {
				database.DB.Callback().Update().Remove(name)
			}
			if err == nil {
				t.Fatal("database failure hidden")
			}
			if _, err := io.ReadAll(reader); err == nil {
				t.Fatal("database failure left playback bytes readable")
			}
			p := queueSnapshot(t, s)
			if p.Session != nil || p.State.ActiveToken != "" || p.State.Status != "stopped" || p.Warning == "" {
				t.Fatalf("real stop missing from live state: %+v", p)
			}
			if err := s.Stop(context.Background(), session.Token); err != nil {
				t.Fatal("repeated stop was not idempotent", err)
			}
		})
	}
}
func TestQueueInvalidReplaceLeavesPlaybackAndRevisionAlone(t *testing.T) {
	s, _ := queueRuntimeFixture(t, false)
	session := startQueueInline(t, s)
	before := queueSnapshot(t, s)
	err := s.Edit(context.Background(), QueueEdit{Action: "replace_collection", CollectionID: 999, ExpectedRevision: before.State.Revision})
	if !errors.Is(err, ErrQueueMediaUnavailable) {
		t.Fatal(err)
	}
	after := queueSnapshot(t, s)
	if after.Session == nil || after.Session.Token != session.Token || after.State.Revision != before.State.Revision {
		t.Fatalf("rejected edit stopped playback: %+v", after.State)
	}
}
func TestQueueEditWriteFailureStopsWithoutSecondStateWrite(t *testing.T) {
	s, _ := queueRuntimeFixture(t, false)
	session := startQueueInline(t, s)
	before := queueSnapshot(t, s)
	name := "test:edit_commit_failure"
	writes := 0
	database.DB.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "playback_queue_states" {
			writes++
			tx.AddError(errors.New("state write failed"))
		}
	})
	err := s.Edit(context.Background(), QueueEdit{Action: "clear", ExpectedRevision: before.State.Revision})
	database.DB.Callback().Update().Remove(name)
	if err == nil || writes != 1 {
		t.Fatalf("commit writes=%d err=%v", writes, err)
	}
	after := queueSnapshot(t, s)
	if after.Session != nil || after.State.ActiveToken != "" || after.State.Status != "stopped" {
		t.Fatalf("stopped %s but returned %+v", session.Token, after.State)
	}
}
func TestQueueFailureStateScrubsSourcePaths(t *testing.T) {
	s, v := queueRuntimeFixture(t, false)
	missing := v[0].Path + "-missing"
	if err := database.DB.Model(&v[0]).Update("path", missing).Error; err != nil {
		t.Fatal(err)
	}
	p := queueSnapshot(t, s)
	if err := s.Play(context.Background(), p.Items[0].ID, p.State.Revision); err != nil {
		t.Fatal(err)
	}
	p = waitQueueSnapshot(t, s, func(p *QueueSnapshot) bool { return p.State.Status == "failed" })
	if strings.Contains(p.State.LastErrorMessage, v[0].Directory) || strings.Contains(p.Warning, v[0].Directory) {
		t.Fatalf("absolute path in queue error: %+v", p.State)
	}
}
func TestQueueSequenceIncreasesAcrossServiceReplacement(t *testing.T) {
	s, _ := queueRuntimeFixture(t, false)
	before := queueSnapshot(t, s).Sequence
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	next := NewPlaybackQueueService(s.video, nil, nil)
	if err := next.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if got := queueSnapshot(t, next).Sequence; got <= before {
		t.Fatalf("sequence reset: %d -> %d", before, got)
	}
}

func TestQueueOversizeCollectionDoesNotStopCurrent(t *testing.T) {
	s, _ := queueRuntimeFixture(t, false)
	session := startQueueInline(t, s)
	before := queueSnapshot(t, s)
	c := models.MediaCollection{Name: "oversize", NormalizedName: "oversize"}
	if err := database.DB.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	videos := make([]models.Video, playbackQueueCapacity+2)
	for i := range videos {
		videos[i] = models.Video{Name: "episode", Path: fmt.Sprintf("/queue-overflow/%d.mp4", i), Directory: "/queue-overflow"}
	}
	if err := database.DB.CreateInBatches(&videos, 100).Error; err != nil {
		t.Fatal(err)
	}
	members := make([]models.CollectionVideo, len(videos))
	for i, v := range videos {
		members[i] = models.CollectionVideo{CollectionID: c.ID, VideoID: v.ID, Position: i + 1}
	}
	if err := database.DB.CreateInBatches(&members, 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Edit(context.Background(), QueueEdit{Action: "replace_collection", CollectionID: c.ID, ExpectedRevision: before.State.Revision}); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	if err := s.Edit(context.Background(), QueueEdit{Action: "replace_collection", CollectionID: c.ID, StartVideoID: videos[0].ID, StartAfter: true, ExpectedRevision: before.State.Revision}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("anchor consumed overflow lookahead: %v", err)
	}
	p := queueSnapshot(t, s)
	if p.State.Revision != before.State.Revision || p.Session == nil || p.Session.Token != session.Token {
		t.Fatal("overflow replacement stopped playback")
	}
}

func TestQueueControlSequenceChangesForRepeatedIntents(t *testing.T) {
	s, _ := queueRuntimeFixture(t, false)
	session := startQueueInline(t, s)
	mustQueueEvent(t, s, queueEvent(session, 1, "loaded", 0))
	mustQueueEvent(t, s, queueEvent(session, 2, "playing", 0))
	mustQueueEvent(t, s, queueEvent(session, 3, "paused", 1))
	before := queueSnapshot(t, s).Session.ControlSequence
	if err := s.Control(context.Background(), session.Token, "resume"); err != nil {
		t.Fatal(err)
	}
	resumed := queueSnapshot(t, s)
	if resumed.Session.ControlSequence <= before || resumed.Session.DesiredPaused || resumed.State.Status != "paused" {
		t.Fatalf("same-value resume intent lost or falsely acknowledged: %+v", resumed)
	}
	mustQueueEvent(t, s, queueEvent(session, 4, "playing", 1))
	if err := s.Control(context.Background(), session.Token, "pause"); err != nil {
		t.Fatal(err)
	}
	paused := queueSnapshot(t, s).Session.ControlSequence
	mustQueueEvent(t, s, queueEvent(session, 5, "paused", 1))
	mustQueueEvent(t, s, queueEvent(session, 6, "playing", 1))
	if err := s.Control(context.Background(), session.Token, "pause"); err != nil {
		t.Fatal(err)
	}
	if latest := queueSnapshot(t, s); latest.Session.ControlSequence <= paused || !latest.Session.DesiredPaused {
		t.Fatal("native replay swallowed next external pause")
	}
}
