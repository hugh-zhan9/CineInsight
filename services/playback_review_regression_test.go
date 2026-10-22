package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestReviewCancelWhileConfirmWaitingDoesNotWrite(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProxyTestVideo(t, "confirm.mp4", 10)
	target := filepath.Join(video.Directory, "confirm.srt")
	old := []byte("1\n00:00:00,000 --> 00:00:01,000\nold\n")
	fresh := []byte("1\n00:00:00,000 --> 00:00:01,000\nnew\n")
	if err := os.WriteFile(target, old, 0600); err != nil {
		t.Fatal(err)
	}
	_, targetFP, _, err := readSubtitleForEditing(target)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(video.Directory, ".pending.srt")
	if err := os.WriteFile(artifactPath, fresh, 0600); err != nil {
		t.Fatal(err)
	}
	_, artifactFP, _, err := readSubtitleForEditing(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(video.Path)
	source := videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano())
	preview := SubtitleExtractPreview{Token: "review-token", VideoID: video.ID, SourceVersion: source, TrackIndex: 1, TargetFingerprint: targetFP, ExpiresAt: time.Now().Add(time.Minute)}
	svc := NewSubtitleWorkbenchService(nil)
	svc.dataDir = t.TempDir()
	svc.extraction.pending = map[string]extractionArtifact{preview.Token: {preview: preview, path: artifactPath, fingerprint: artifactFP}}
	unlock := lockSubtitleFile(target)
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.ConfirmSubtitleExtraction(context.Background(), SubtitleExtractConfirm{Token: preview.Token, VideoID: video.ID, SourceVersion: source, TrackIndex: 1, TargetFingerprint: targetFP})
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		svc.extraction.mu.Lock()
		_, pending := svc.extraction.pending[preview.Token]
		svc.extraction.mu.Unlock()
		if !pending {
			break
		}
		time.Sleep(time.Millisecond)
	}
	svc.CancelSubtitleExtraction(video.ID)
	select {
	case <-done:
	case <-time.After(time.Second):
		unlock()
		<-done
		t.Fatal("cancelled confirmation waited for unrelated subtitle writer")
	}
	unlock()
	got, _ := os.ReadFile(target)
	if string(got) != string(old) {
		t.Fatal("cancelled extraction confirmation still overwrote SRT")
	}
}

type reviewBlockingControlledPlayer struct {
	controlledTestPlayer
	block   bool
	entered chan struct{}
	release chan struct{}
}

func (p *reviewBlockingControlledPlayer) Close() error {
	if p.block {
		close(p.entered)
		<-p.release
		p.block = false
	}
	return nil
}
func TestReviewControlledReservationUntilProcessClosed(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProxyTestVideo(t, "owned.mp4", 10)
	player := &reviewBlockingControlledPlayer{entered: make(chan struct{}), release: make(chan struct{})}
	svc := &VideoService{}
	svc.controlled = &controlledPlayback{video: svc, player: player, now: time.Now}
	if err := svc.startControlledPlayback(context.Background(), video.ID); err != nil {
		t.Fatal(err)
	}
	player.block = true
	done := make(chan struct{})
	go func() { defer close(done); svc.StopControlledPlayback() }()
	<-player.entered
	err := claimControlledQueue("review-queue")
	releaseControlledQueue("review-queue")
	close(player.release)
	<-done
	if err == nil {
		t.Fatal("queue admitted while ordinary controlled process close still pending")
	}
}
func TestReviewInvalidVideoFeedbackDoesNotSnoozeSurvivingGroup(t *testing.T) {
	setupVideoServiceTestDB(t)
	if err := database.DB.AutoMigrate(&models.RecommendationFeedback{}); err != nil {
		t.Fatal(err)
	}
	videos := []models.Video{{Name: "a", Path: "/a"}, {Name: "b", Path: "/b"}, {Name: "c", Path: "/c"}}
	for i := range videos {
		if err := database.DB.Create(&videos[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := &VideoService{}
	if _, err := svc.SetRecommendationFeedback(context.Background(), RecommendationFeedbackRequest{TargetKind: "work_video", TargetID: videos[0].ID}); err != nil {
		t.Fatal(err)
	}
	group := models.VideoVersionGroup{Title: "work", Revision: 1}
	if err := database.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	for i := range videos {
		if err := database.DB.Create(&models.VideoVersionMember{GroupID: group.ID, VideoID: videos[i].ID, Position: i + 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Delete(&videos[0]).Error; err != nil {
		t.Fatal(err)
	}
	items, err := svc.ListRecommendationFeedback(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Active {
		t.Fatalf("fixture feedback not inactive %+v", items)
	}
	pool, err := svc.collectRandomCandidatesFrom(database.DB, RandomPlayRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pool.rows) != 1 {
		t.Fatalf("inactive feedback excluded surviving work: candidates=%d", len(pool.rows))
	}
}

type reviewFailingControlledPlayer struct {
	controlledTestPlayer
	fail bool
}

func (p *reviewFailingControlledPlayer) Close() error {
	if p.fail {
		return errors.New("test process cleanup failed")
	}
	return nil
}
func TestControlledCloseFailureRetainsOwnershipAndRetry(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProxyTestVideo(t, "close-retry.mp4", 10)
	player := &reviewFailingControlledPlayer{}
	svc := &VideoService{}
	svc.controlled = &controlledPlayback{video: svc, player: player, now: time.Now}
	if err := svc.startControlledPlayback(context.Background(), video.ID); err != nil {
		t.Fatal(err)
	}
	run := svc.controlled.run
	player.fail = true
	if err := svc.StopControlledPlayback(); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	if svc.controlled.run != run || run.lease.file == nil {
		t.Fatal("failed cleanup discarded owned media")
	}
	if err := claimControlledQueue("blocked"); err == nil {
		releaseControlledQueue("blocked")
		t.Fatal("failed cleanup lost reservation")
	}
	if err := player.sink(InlineQueueEvent{Token: run.token, VideoID: video.ID, SourceVersion: run.lease.sourceVersion(), Seq: 1, ViewCycle: 1, Kind: "playing"}); err == nil {
		t.Fatal("stopping token accepted late fact")
	}
	if err := svc.startControlledPlayback(context.Background(), video.ID); err == nil {
		t.Fatal("replacement started over failed cleanup")
	}
	if err := claimControlledQueue("still-blocked"); err == nil {
		releaseControlledQueue("still-blocked")
		t.Fatal("failed replacement discarded reservation")
	}
	player.fail = false
	if err := svc.StopControlledPlayback(); err != nil {
		t.Fatal(err)
	}
	if svc.controlled.run != nil || run.lease.file != nil {
		t.Fatal("successful cleanup retained media")
	}
	if err := claimControlledQueue("released"); err != nil {
		t.Fatal(err)
	}
	releaseControlledQueue("released")
}
func TestControlledTransportRetainsProcessUntilCleanupSucceeds(t *testing.T) {
	directory := t.TempDir()
	blocker := filepath.Join(directory, "file")
	if err := os.WriteFile(blocker, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	close(done)
	process := &queueIINAProcess{done: done, workdir: filepath.Join(blocker, "child")}
	player := &queueIINAPlayer{process: process}
	if err := player.Close(); err == nil {
		t.Fatal("cleanup fixture should fail")
	}
	if player.process != process {
		t.Fatal("transport discarded process handle after cleanup error")
	}
	process.workdir = filepath.Join(directory, "owned")
	if err := os.Mkdir(process.workdir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := player.Close(); err != nil {
		t.Fatal(err)
	}
	if player.process != nil {
		t.Fatal("successful cleanup retained handle")
	}
}
func TestRecommendationInaccessibleVideoFeedbackInactiveEverywhere(t *testing.T) {
	for _, kind := range []string{"outside", "blacklisted"} {
		t.Run(kind, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			if err := database.DB.AutoMigrate(&models.RecommendationFeedback{}); err != nil {
				t.Fatal(err)
			}
			if err := database.DB.Create(&models.ScanDirectory{Path: "/scope"}).Error; err != nil {
				t.Fatal(err)
			}
			videos := []models.Video{{Name: "a", Path: "/scope/a", Directory: "/scope"}, {Name: "b", Path: "/scope/b", Directory: "/scope"}, {Name: "c", Path: "/scope/c", Directory: "/scope"}}
			for i := range videos {
				if err := database.DB.Create(&videos[i]).Error; err != nil {
					t.Fatal(err)
				}
			}
			svc := &VideoService{}
			if _, err := svc.SetRecommendationFeedback(context.Background(), RecommendationFeedbackRequest{TargetKind: "work_video", TargetID: videos[0].ID}); err != nil {
				t.Fatal(err)
			}
			group := models.VideoVersionGroup{Title: "group", Revision: 1}
			if err := database.DB.Create(&group).Error; err != nil {
				t.Fatal(err)
			}
			for i := range videos {
				if err := database.DB.Create(&models.VideoVersionMember{GroupID: group.ID, VideoID: videos[i].ID, Position: i + 1}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if kind == "outside" {
				database.DB.Model(&videos[0]).Updates(map[string]any{"path": "/outside/a", "directory": "/outside"})
			} else {
				database.DB.Model(&models.Settings{}).Where("id > 0").Update("scan_exclude_paths", "/scope/a")
			}
			items, err := svc.ListRecommendationFeedback(context.Background())
			if err != nil || len(items) != 1 || items[0].Active {
				t.Fatalf("inaccessible feedback active %+v %v", items, err)
			}
			pool, err := svc.collectRandomCandidatesFrom(database.DB, RandomPlayRequest{})
			if err != nil || len(pool.rows) != 1 {
				t.Fatalf("inaccessible feedback affected work %+v %v", pool, err)
			}
		})
	}
}

func TestSubtitleQuiesceCancelsConfirmationWaitingForWriter(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProxyTestVideo(t, "confirm.mp4", 10)
	target := filepath.Join(video.Directory, "confirm.srt")
	old := []byte("1\n00:00:00,000 --> 00:00:01,000\nold\n")
	fresh := []byte("1\n00:00:00,000 --> 00:00:01,000\nnew\n")
	if err := os.WriteFile(target, old, 0600); err != nil {
		t.Fatal(err)
	}
	_, targetFP, _, err := readSubtitleForEditing(target)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(video.Directory, ".pending.srt")
	if err := os.WriteFile(artifactPath, fresh, 0600); err != nil {
		t.Fatal(err)
	}
	_, artifactFP, _, err := readSubtitleForEditing(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(video.Path)
	source := videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano())
	preview := SubtitleExtractPreview{Token: "review-token", VideoID: video.ID, SourceVersion: source, TrackIndex: 1, TargetFingerprint: targetFP, ExpiresAt: time.Now().Add(time.Minute)}
	svc := NewSubtitleWorkbenchService(nil)
	svc.dataDir = t.TempDir()
	svc.extraction.pending = map[string]extractionArtifact{preview.Token: {preview: preview, path: artifactPath, fingerprint: artifactFP}}
	unlock := lockSubtitleFile(target)
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.ConfirmSubtitleExtraction(context.Background(), SubtitleExtractConfirm{Token: preview.Token, VideoID: video.ID, SourceVersion: source, TrackIndex: 1, TargetFingerprint: targetFP})
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		svc.extraction.mu.Lock()
		_, pending := svc.extraction.pending[preview.Token]
		svc.extraction.mu.Unlock()
		if !pending {
			break
		}
		time.Sleep(time.Millisecond)
	}
	quiesced := make(chan struct{})
	go func() { svc.QuiesceSubtitleExtraction(); close(quiesced) }()
	select {
	case <-quiesced:
	case <-time.After(time.Second):
		unlock()
		<-done
		t.Fatal("quiesce waited for unrelated writer")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		unlock()
		<-done
		t.Fatal("cancelled confirmation waited for unrelated subtitle writer")
	}
	unlock()
	got, _ := os.ReadFile(target)
	if string(got) != string(old) {
		t.Fatal("cancelled extraction confirmation still overwrote SRT")
	}
}

func reviewQueueWithOwnedPlayer(t *testing.T, player queueControlledPlayer) (*PlaybackQueueService, string) {
	t.Helper()
	svc, _ := queueRuntimeFixture(t, false)
	svc.player = player
	page := queueSnapshot(t, svc)
	if err := svc.Edit(context.Background(), QueueEdit{Action: "configure", ExpectedRevision: page.State.Revision, Player: "iina", Autoplay: queueBool(false)}); err != nil {
		t.Fatal(err)
	}
	page = queueSnapshot(t, svc)
	if err := svc.Play(context.Background(), page.Items[0].ID, page.State.Revision); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		svc.commands.Lock()
		ready := svc.run != nil && svc.run.lease != nil && svc.run.loaded
		token := ""
		if ready {
			token = svc.run.token
		}
		svc.commands.Unlock()
		if ready {
			return svc, token
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("queue not prepared")
	return nil, ""
}
func TestQueueReservationUntilOwnedProcessClosed(t *testing.T) {
	player := &reviewBlockingControlledPlayer{entered: make(chan struct{}), release: make(chan struct{})}
	svc, token := reviewQueueWithOwnedPlayer(t, player)
	player.block = true
	done := make(chan error, 1)
	go func() { done <- svc.Stop(context.Background(), token) }()
	<-player.entered
	err := claimControlledDesktop("blocked-desktop")
	releaseControlledDesktop("blocked-desktop")
	close(player.release)
	if stopErr := <-done; stopErr != nil {
		t.Fatal(stopErr)
	}
	if err == nil {
		t.Fatal("desktop entered while queue process Close was pending")
	}
	if err := claimControlledDesktop("after-close"); err != nil {
		t.Fatal(err)
	}
	releaseControlledDesktop("after-close")
}
func TestQueueCloseFailureRetainsOwnershipForExplicitRetry(t *testing.T) {
	for _, closeService := range []bool{false, true} {
		t.Run(fmt.Sprint(closeService), func(t *testing.T) {
			player := &reviewFailingControlledPlayer{}
			svc, token := reviewQueueWithOwnedPlayer(t, player)
			t.Cleanup(func() { player.fail = false })
			player.fail = true
			stop := func() error {
				if closeService {
					return svc.Close()
				}
				return svc.Stop(context.Background(), token)
			}
			if err := stop(); err == nil {
				t.Fatal("queue Close failure hidden")
			}
			if svc.run == nil || !svc.run.cleanupPending {
				t.Fatal("failed queue cleanup ownership lost")
			}
			if err := claimControlledDesktop("blocked-desktop"); err == nil {
				releaseControlledDesktop("blocked-desktop")
				t.Fatal("desktop entered after failed queue cleanup")
			}
			player.fail = false
			if err := stop(); err != nil {
				t.Fatal(err)
			}
			if svc.run != nil {
				t.Fatal("queue cleanup retry retained run")
			}
			if err := claimControlledDesktop("released-desktop"); err != nil {
				t.Fatal(err)
			}
			releaseControlledDesktop("released-desktop")
		})
	}
}
