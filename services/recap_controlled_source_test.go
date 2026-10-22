package services

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestControlledSourceRemainsDistinctInStatsRecapAndSerialization(t *testing.T) {
	f := newRecapFixture(t)
	video := f.video("controlled-source.mp4")
	at := recapAt(2026, time.October, 3, 20, 0)
	f.play(video.ID, at, PlayEventSourceDesktopControlledView)
	stats := NewLibraryStatsService()
	stats.now = func() time.Time { return at.Add(time.Hour) }
	summary, err := stats.GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if summary.PlaysBySource[PlayEventSourceDesktopControlledView] != 1 {
		t.Fatal(summary.PlaysBySource)
	}
	result := f.recap(at.Add(time.Hour), RecapRequest{Period: RecapPeriodMonth, Year: 2026, Month: 10})
	if len(result.Habits.Sources) != 1 || result.Habits.Sources[0].Source != PlayEventSourceDesktopControlledView {
		t.Fatal(result.Habits.Sources)
	}
	encoded, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(encoded), PlayEventSourceDesktopControlledView) {
		t.Fatalf("serialized source lost: %v", err)
	}
	if _, err := (&VideoService{}).RecordViewEvent(video.ID, PlayEventSourceDesktopControlledView, "forged"); err == nil {
		t.Fatal("public frontend record endpoint accepted controlled source")
	}
}
