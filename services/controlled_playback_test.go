package services

import (
	"context"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

type controlledTestPlayer struct {
	sink   func(InlineQueueEvent) error
	closed int
}

func (*controlledTestPlayer) Available() (bool, string) { return true, "" }
func (p *controlledTestPlayer) Load(_ context.Context, _ *queueMediaLease, _ string, sink func(InlineQueueEvent) error) error {
	p.sink = sink
	return nil
}
func (*controlledTestPlayer) Control(context.Context, string) error { return nil }
func (*controlledTestPlayer) Stop(context.Context) error            { return nil }
func (p *controlledTestPlayer) Close() error                        { p.closed++; return nil }
func TestControlledPlaybackOwnsFactsReplayAndQuiescence(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProxyTestVideo(t, "controlled.mp4", 3)
	svc := &VideoService{}
	player := &controlledTestPlayer{}
	now := time.Now()
	owner := &controlledPlayback{video: svc, player: player, now: func() time.Time { return now }}
	svc.controlled = owner
	if err := svc.startControlledPlayback(context.Background(), video.ID); err != nil {
		t.Fatal(err)
	}
	defer svc.StopControlledPlayback()
	r := owner.run
	seq := uint64(0)
	send := func(kind string, pos float64, cycle uint64) error {
		seq++
		return player.sink(InlineQueueEvent{Token: r.token, VideoID: video.ID, SourceVersion: r.lease.sourceVersion(), Seq: seq, ViewCycle: cycle, Kind: kind, Position: pos, Duration: 3})
	}
	if err := claimControlledQueue("other"); err == nil {
		releaseControlledQueue("other")
		t.Fatal("queue stole desktop ownership")
	}
	if err := send("playing", 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := send("seeking", 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := send("seeked", 3, 1); err != nil {
		t.Fatal(err)
	}
	if err := send("ended", 3, 1); err == nil {
		t.Fatal("seek became natural end")
	}
	if err := send("seeking", 3, 1); err != nil {
		t.Fatal(err)
	}
	if err := send("seeked", 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := send("playing", 0, 1); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err := send("progress", 1, 1); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if err := send("ended", 3, 1); err != nil {
		t.Fatal(err)
	}
	if err := send("ended", 3, 1); err != nil {
		t.Fatal(err)
	}
	if err := send("playing", 0, 2); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Second)
	if err := send("ended", 3, 2); err != nil {
		t.Fatal(err)
	}
	var events, diary, queue int64
	database.DB.Model(&models.PlayEvent{}).Where("source = ?", PlayEventSourceDesktopControlledView).Count(&events)
	database.DB.Model(&models.ViewingDiaryEntry{}).Count(&diary)
	database.DB.Model(&models.PlaybackQueueState{}).Count(&queue)
	if events != 2 || diary != 2 || queue != 0 {
		t.Fatalf("facts events=%d diary=%d queue=%d", events, diary, queue)
	}
	release, err := svc.QuiesceControlledPlayback()
	if err != nil {
		t.Fatal(err)
	}
	if err := send("progress", 2, 2); err == nil {
		t.Fatal("late callback admitted")
	}
	if err := svc.startControlledPlayback(context.Background(), video.ID); err == nil {
		t.Fatal("quiesced admission")
	}
	release()
	if err := claimControlledQueue("queue"); err != nil {
		t.Fatal(err)
	}
	if err := svc.startControlledPlayback(context.Background(), video.ID); err == nil {
		t.Fatal("desktop stole queue")
	}
	releaseControlledQueue("queue")
}
