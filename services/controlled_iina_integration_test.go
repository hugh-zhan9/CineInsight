package services

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestControlledIINARealEndReplayAndClose(t *testing.T) {
	if os.Getenv("CINEINSIGHT_TEST_IINA") != "1" {
		t.Skip("set CINEINSIGHT_TEST_IINA=1 for owned synthetic IINA test")
	}
	setupVideoServiceTestDB(t)
	video := createProxyTestVideo(t, "controlled-real.mp4", 3)
	binary, err := findThumbnailFFmpeg()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(binary, "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=15", "-t", "3", "-c:v", "libx264", "-pix_fmt", "yuv420p", video.Path).CombinedOutput(); err != nil {
		t.Fatalf("fixture %v %s", err, output)
	}
	if err := database.DB.Model(&models.Settings{}).Where("id > 0").Update("desktop_player", "iina_controlled").Error; err != nil {
		t.Fatal(err)
	}
	svc := &VideoService{}
	defer svc.StopControlledPlayback()
	result, err := svc.PlayVideo(video.ID)
	if err != nil || !result.DispatchSucceeded {
		t.Fatalf("dispatch %+v %v", result, err)
	}
	wait := func(status string) {
		t.Helper()
		end := time.Now().Add(15 * time.Second)
		for time.Now().Before(end) {
			state := svc.GetControlledPlaybackState()
			if state.Status == status {
				return
			}
			if state.Status == "failed" {
				t.Fatal(state)
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("expected %s got %+v", status, svc.GetControlledPlaybackState())
	}
	wait("playing")
	player := svc.controlledPlayer().player.(*queueIINAPlayer)
	if err := player.Control(context.Background(), "pause"); err != nil {
		t.Fatal(err)
	}
	wait("paused")
	var count int64
	database.DB.Model(&models.PlayEvent{}).Where("source = ?", PlayEventSourceDesktopControlledView).Count(&count)
	if count != 0 {
		t.Fatal("early pause counted completion")
	}
	if err := player.Control(context.Background(), "resume"); err != nil {
		t.Fatal(err)
	}
	wait("ended")
	player.mu.Lock()
	process := player.process
	player.mu.Unlock()
	// mpv native replay from the same window must create a new viewing cycle.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := process.client.command(ctx, "seek", 0, "absolute"); err != nil {
		t.Fatal(err)
	}
	if err := player.Control(context.Background(), "resume"); err != nil {
		t.Fatal(err)
	}
	wait("playing")
	wait("ended")
	database.DB.Model(&models.PlayEvent{}).Where("source = ?", PlayEventSourceDesktopControlledView).Count(&count)
	if count != 2 {
		t.Fatalf("replay effective events=%d", count)
	}
	var got models.Video
	database.DB.First(&got, video.ID)
	if got.PlayCount != 1 || !got.IsWatched {
		t.Fatalf("dispatch/replay facts %+v", got)
	}
	var diary int64
	database.DB.Model(&models.ViewingDiaryEntry{}).Count(&diary)
	if diary != 2 {
		t.Fatalf("diary=%d", diary)
	}
	// A subsequent ordinary dispatch retires the previous owned process and starts a fresh token.
	second, err := svc.PlayVideo(video.ID)
	if err != nil || !second.DispatchSucceeded {
		t.Fatalf("second dispatch %+v %v", second, err)
	}
	select {
	case <-process.done:
	case <-time.After(5 * time.Second):
		t.Fatal("previous process survived replacement")
	}
	wait("ended")
	player.mu.Lock()
	process = player.process
	player.mu.Unlock()
	database.DB.First(&got, video.ID)
	if got.PlayCount != 2 {
		t.Fatalf("second dispatch count %d", got.PlayCount)
	}
	if err := svc.StopControlledPlayback(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.done:
	case <-time.After(5 * time.Second):
		t.Fatal("owned IINA process leaked")
	}
}
