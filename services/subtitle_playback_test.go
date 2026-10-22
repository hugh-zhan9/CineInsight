package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestPlaybackSubtitleProjectionAndIdentity(t *testing.T) {
	setupVideoServiceTestDB(t)
	path := filepath.Join(t.TempDir(), "clip.mp4")
	mustCreateFile(t, path)
	video := models.Video{Path: path, Name: "clip.mp4", Directory: filepath.Dir(path)}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	request := PlaybackSubtitleRequest{VideoID: video.ID, SourceVersion: videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano())}
	svc := NewSubtitleWorkbenchService(nil)
	result, err := svc.GetPlaybackSubtitle(context.Background(), request)
	if err != nil || result.Status != "absent" {
		t.Fatalf("absent: %+v %v", result, err)
	}
	subtitle := filepath.Join(filepath.Dir(path), "clip.srt")
	for _, test := range []struct {
		name, text string
		wantError  bool
	}{
		{"bilingual", "1\n00:00:01,000 --> 00:00:02,000\nHello\n你好\n", false},
		{"empty", "", true}, {"invalid", "garbage", true}, {"encoding", string([]byte{0xff, 0xfe}), true},
		{"range", "1\n00:00:02,000 --> 00:00:01,000\nbad\n", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(subtitle, []byte(test.text), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := svc.GetPlaybackSubtitle(context.Background(), request)
			if (err != nil) != test.wantError {
				t.Fatalf("got %+v %v", got, err)
			}
			if !test.wantError && (got.Cues[0].Text != "Hello\n你好" || got.Cues[0].StartMS != 1000) {
				t.Fatal(got)
			}
		})
	}
	if err := os.WriteFile(subtitle, []byte("1\n00:00:01,000 --> 00:00:02,000\nvalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	request.SourceVersion = "old"
	if _, err := svc.GetPlaybackSubtitle(context.Background(), request); err == nil {
		t.Fatal("stale source accepted")
	}
}

func TestPlaybackSubtitleRejectsQueuePreparingFailedAndWrongToken(t *testing.T) {
	queue := NewPlaybackQueueService(&VideoService{}, nil, nil)
	workbench := NewSubtitleWorkbenchService(nil)
	request := PlaybackSubtitleRequest{VideoID: 1, QueueToken: "token", SourceVersion: "source"}
	queue.run = &playbackQueueRun{token: "token", player: "inline"}
	if _, err := queue.GetPlaybackSubtitle(context.Background(), workbench, request); err == nil {
		t.Fatal("preparing queue accepted")
	}
	queue.run.lease = &queueMediaLease{video: models.Video{ID: 1}}
	queue.run.failed = true
	if _, err := queue.GetPlaybackSubtitle(context.Background(), workbench, request); err == nil {
		t.Fatal("failed queue accepted")
	}
	queue.run.failed = false
	request.QueueToken = "old"
	if _, err := queue.GetPlaybackSubtitle(context.Background(), workbench, request); err == nil {
		t.Fatal("stale queue accepted")
	}
	if _, err := workbench.GetPlaybackSubtitle(context.Background(), PlaybackSubtitleRequest{}); err == nil {
		t.Fatal("zero video accepted")
	}
}
