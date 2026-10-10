package services

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// Explicit opt-in: opens only a dedicated owned IINA process and temporary
// synthetic clips. Normal go test never opens the user's graphical player.
func TestQueueIINARealConsecutiveFilesAndStop(t *testing.T) {
	if os.Getenv("CINEINSIGHT_TEST_IINA") != "1" {
		t.Skip("set CINEINSIGHT_TEST_IINA=1 for the owned IINA integration")
	}
	if _, ok := queueIINABinary(); !ok {
		t.Fatal("IINA not installed")
	}
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	s, videos := queueRuntimeFixture(t, true)
	player := NewQueueIINAPlayer()
	s.player = player
	for index, video := range videos[:2] {
		command := exec.Command(binary, "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=15", "-t", "5", "-c:v", "libx264", "-pix_fmt", "yuv420p", video.Path)
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture: %v %s", err, out)
		}
		duration := 0
		if index == 1 {
			duration = 99
		}
		if err := database.DB.Model(&video).Update("duration", duration).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := queueSnapshot(t, s)
	if err := s.Edit(context.Background(), QueueEdit{ExpectedRevision: p.State.Revision, Action: "remove", EntryID: p.Items[2].ID}); err != nil {
		t.Fatal(err)
	}
	p = queueSnapshot(t, s)
	if err := s.Edit(context.Background(), QueueEdit{ExpectedRevision: p.State.Revision, Action: "configure", Player: "iina", Autoplay: queueBool(true)}); err != nil {
		t.Fatal(err)
	}
	p = queueSnapshot(t, s)
	if err := s.Play(context.Background(), p.Items[0].ID, p.State.Revision); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(25 * time.Second)
	var firstPID int
	var firstToken string
	paused := false
	for time.Now().Before(deadline) {
		p = queueSnapshot(t, s)
		if p.State.Status == "failed" {
			t.Fatalf("IINA failed: %+v warning=%s", p.State, p.Warning)
		}
		if p.State.Status == "playing" && p.Current.VideoID == videos[0].ID && !paused {
			firstToken = p.State.ActiveToken
			player.mu.Lock()
			firstPID = player.process.cmd.Process.Pid
			player.mu.Unlock()

			// Real source is five seconds; library duration is unknown. Pause
			// after its half-duration threshold but before EOF proves the
			// adapter delivered actual metadata, not an eventual ended shortcut.
			until := time.Now().Add(5 * time.Second)
			for time.Now().Before(until) {
				player.mu.Lock()
				position := player.session.position
				player.mu.Unlock()
				if position >= 3 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := s.Control(context.Background(), firstToken, "pause"); err != nil {
				t.Fatal(err)
			}
			waitQueueSnapshot(t, s, func(p *QueueSnapshot) bool { return p.State.Status == "paused" })
			mustNoteCount(t, &models.ViewingDiaryEntry{}, 1)
			if err := s.Control(context.Background(), firstToken, "resume"); err != nil {
				t.Fatal(err)
			}
			paused = true
		}
		if p.State.Status == "ended" && p.Current.VideoID == videos[1].ID {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p.State.Status != "ended" || p.Current.VideoID != videos[1].ID || !paused {
		t.Fatalf("did not finish two clips: %+v warning=%s", p.State, p.Warning)
	}
	player.mu.Lock()
	process := player.process
	pid := process.cmd.Process.Pid
	player.mu.Unlock()
	if pid != firstPID || firstPID == 0 {
		t.Fatalf("did not reuse owned process: %d -> %d", firstPID, pid)
	}
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 2)
	for _, video := range videos[:2] {
		var got models.Video
		if err := database.DB.First(&got, video.ID).Error; err != nil {
			t.Fatal(err)
		}
		if got.PlayCount != 1 || !got.IsWatched {
			t.Fatalf("play facts: %+v", got)
		}
	}
	if err := s.Stop(context.Background(), p.State.ActiveToken); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.done:
	case <-time.After(5 * time.Second):
		t.Fatal("owned process remains after stop")
	}
	if _, err := os.Stat(process.workdir); !os.IsNotExist(err) {
		t.Fatalf("private IPC directory remains: %v", err)
	}
}

func TestQueueIINARealFailureDoesNotAdvance(t *testing.T) {
	if os.Getenv("CINEINSIGHT_TEST_IINA") != "1" {
		t.Skip("opt-in owned IINA test")
	}
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"external_stop", "ipc_disconnect", "source_removed"} {
		t.Run(scenario, func(t *testing.T) {
			s, videos := queueRuntimeFixture(t, true)
			player := NewQueueIINAPlayer()
			s.player = player
			cmd := exec.Command(binary, "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=15", "-t", "5", "-c:v", "libx264", "-pix_fmt", "yuv420p", videos[0].Path)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fixture: %v %s", err, out)
			}
			p := queueSnapshot(t, s)
			if err := s.Edit(context.Background(), QueueEdit{Action: "configure", Player: "iina", Autoplay: queueBool(true), ExpectedRevision: p.State.Revision}); err != nil {
				t.Fatal(err)
			}
			p = queueSnapshot(t, s)
			if err := s.Play(context.Background(), p.Items[0].ID, p.State.Revision); err != nil {
				t.Fatal(err)
			}
			p = waitQueueSnapshot(t, s, func(p *QueueSnapshot) bool { return p.State.Status == "playing" || p.State.Status == "failed" })
			if p.State.Status != "playing" {
				t.Fatal(p.State)
			}
			player.mu.Lock()
			process := player.process
			player.mu.Unlock()
			switch scenario {
			case "external_stop":
				_, _ = process.client.command(context.Background(), "stop")
			case "ipc_disconnect":
				_ = process.client.conn.Close()
			case "source_removed":
				if err := os.Rename(videos[0].Path, videos[0].Path+".moved"); err != nil {
					t.Fatal(err)
				}
				if err := s.Control(context.Background(), p.State.ActiveToken, "pause"); err == nil {
					t.Fatal("missing source accepted")
				}
			}
			p = waitQueueSnapshot(t, s, func(p *QueueSnapshot) bool { return p.State.Status == "failed" })
			if p.Current.VideoID != videos[0].ID {
				t.Fatal("failure advanced to another item")
			}
			if strings.Contains(p.State.LastErrorMessage, videos[0].Directory) {
				t.Fatal("failure exposed media path")
			}
			select {
			case <-process.done:
			case <-time.After(5 * time.Second):
				t.Fatal("owned process leaked after failure")
			}
			if _, err := os.Stat(process.workdir); !os.IsNotExist(err) {
				t.Fatalf("private directory remains: %v", err)
			}
		})
	}
}
