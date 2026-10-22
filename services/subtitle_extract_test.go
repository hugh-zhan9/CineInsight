package services

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func TestSubtitleExtractionRealTracksAndTargetConflict(t *testing.T) {
	setupVideoServiceTestDB(t)
	ffmpeg, err := findThumbnailFFmpeg()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source := filepath.Join(root, "input.srt")
	path := filepath.Join(root, "clip.mkv")
	if err = os.WriteFile(source, []byte("1\n00:00:00,100 --> 00:00:01,000\nhello\n你好\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ffmpeg, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "color=c=black:s=32x32:d=2", "-i", source, "-map", "0:v", "-map", "1:s", "-c:v", "libx264", "-c:s", "srt", "-y", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, output)
	}
	video := models.Video{Path: path, Name: "clip.mkv", Directory: root}
	if err = database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewSubtitleWorkbenchService(nil)
	svc.dataDir = t.TempDir()
	defer svc.QuiesceSubtitleExtraction()
	tracks, err := svc.ListExtractableSubtitleTracks(context.Background(), video.ID)
	if err != nil || len(tracks.Tracks) != 1 || tracks.Tracks[0].Index != 1 {
		t.Fatalf("tracks %+v %v", tracks, err)
	}
	request := SubtitleExtractRequest{VideoID: video.ID, SourceVersion: tracks.SourceVersion, TrackIndex: 1}
	preview, err := svc.PreviewSubtitleExtraction(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Entries) != 1 || preview.Entries[0].Text != "hello\n你好" {
		t.Fatal(preview)
	}
	target := filepath.Join(root, "clip.srt")
	original := []byte("1\n00:00:00,100 --> 00:00:01,000\nexternal edit\n")
	if err = os.WriteFile(target, original, 0600); err != nil {
		t.Fatal(err)
	}
	confirm := func(p *SubtitleExtractPreview) (*SubtitleSaveResult, error) {
		return svc.ConfirmSubtitleExtraction(context.Background(), SubtitleExtractConfirm{Token: p.Token, VideoID: p.VideoID, SourceVersion: p.SourceVersion, TrackIndex: p.TrackIndex, TargetFingerprint: p.TargetFingerprint})
	}
	result, err := confirm(preview)
	if err != nil || result.Status != SubtitleSaveStatusRejected {
		t.Fatalf("target conflict %+v %v", result, err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != string(original) {
		t.Fatal("overwrote external edit")
	}
	preview, err = svc.PreviewSubtitleExtraction(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	result, err = confirm(preview)
	if err != nil || result.Status != SubtitleSaveStatusSaved || result.BackupID == "" {
		t.Fatalf("save %+v %v", result, err)
	}
	if _, err = confirm(preview); err == nil {
		t.Fatal("reused preview")
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0600 {
		t.Fatal("lost permissions")
	}
	if supported, _ := subtitleTrackSupport("hdmv_pgs_subtitle"); supported {
		t.Fatal("picture track supported")
	}
	if supported, reason := subtitleTrackSupport("ass"); !supported || reason == "" {
		t.Fatal("missing ASS warning")
	}
	preview, err = svc.PreviewSubtitleExtraction(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	svc.CancelSubtitleExtraction(video.ID)
	if _, err = confirm(preview); err == nil {
		t.Fatal("cancelled preview usable")
	}
	preview, err = svc.PreviewSubtitleExtraction(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	artifact := svc.extraction.pending[preview.Token]
	if err := os.WriteFile(artifact.path, []byte("1\n00:00:00,100 --> 00:00:01,000\nunreviewed change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = confirm(preview); err == nil {
		t.Fatal("changed hidden preview published")
	}
	preview, err = svc.PreviewSubtitleExtraction(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	sourceInfo, _ := os.Stat(path)
	if err := os.Chtimes(path, sourceInfo.ModTime().Add(time.Second), sourceInfo.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = confirm(preview); err == nil {
		t.Fatal("changed source accepted")
	}

}
