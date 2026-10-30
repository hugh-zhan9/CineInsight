package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"video-master/models"
)

func TestVideoEditNavigateFrame(t *testing.T) {
	s, media := newFakeEditService(t)
	v := media.addFakeSource(t, t.TempDir(), "vfr.mkv", 4000)
	view := mustEditProject(t, s, models.VideoEditKindCrop, v.ID)
	pre, _ := s.PreflightProject(context.Background(), view.ID)
	request := EditFrameRequest{ProjectID: view.ID, VideoID: v.ID, SourceVersion: pre.Sources[0].SourceVersion}
	original := s.runOutput
	s.runOutput = func(ctx context.Context, name string, args []string) (string, string, error) {
		if strings.Contains(strings.Join(args, " "), "frame=best_effort_timestamp_time") {
			return "0.000000\n0.040000\n0.120500\n1.500000\n3.960000\n", "", nil
		}
		return original(ctx, name, args)
	}
	for _, tc := range []struct {
		at        int64
		direction int
		want      int64
	}{
		{40, 1, 120}, {120, -1, 40}, {50, 0, 120}, {0, -1, 0}, {3960, 1, 3960}, {1999, -1, 1500},
	} {
		request.AtMS, request.Direction = tc.at, tc.direction
		got, err := s.NavigateFrame(context.Background(), request)
		if err != nil || got.AtMS != tc.want {
			t.Fatalf("%+v => %+v %v", tc, got, err)
		}
	}
	request.VideoID++
	if _, err := s.NavigateFrame(context.Background(), request); err == nil {
		t.Fatal("foreign source accepted")
	}
	request.VideoID = v.ID
	request.SourceVersion = "stale"
	if _, err := s.NavigateFrame(context.Background(), request); err == nil || !strings.HasPrefix(err.Error(), "source_changed:") {
		t.Fatalf("stale source: %v", err)
	}
	request.SourceVersion = pre.Sources[0].SourceVersion
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.NavigateFrame(ctx, request); err == nil {
		t.Fatal("cancelled request accepted")
	}
	s.runOutput = func(ctx context.Context, name string, args []string) (string, string, error) {
		if strings.Contains(strings.Join(args, " "), "frame=best_effort_timestamp_time") {
			return "", "", errors.New("timeout")
		}
		return original(ctx, name, args)
	}
	if _, err := s.NavigateFrame(context.Background(), request); err == nil || !strings.HasPrefix(err.Error(), "frame_unavailable:") {
		t.Fatalf("failed read: %v", err)
	}
}

func TestVideoEditNavigateFrameWindowIsNotMediaEnd(t *testing.T) {
	s, media := newFakeEditService(t)
	v := media.addFakeSource(t, t.TempDir(), "sparse.mkv", 60000)
	view := mustEditProject(t, s, models.VideoEditKindCrop, v.ID)
	pre, _ := s.PreflightProject(context.Background(), view.ID)
	original := s.runOutput
	raw := "20.000000\n"
	s.runOutput = func(ctx context.Context, name string, args []string) (string, string, error) {
		if strings.Contains(strings.Join(args, " "), "frame=best_effort_timestamp_time") {
			return raw, "", nil
		}
		return original(ctx, name, args)
	}
	for _, direction := range []int{-1, 1} {
		_, err := s.NavigateFrame(context.Background(), EditFrameRequest{ProjectID: view.ID, VideoID: v.ID, SourceVersion: pre.Sources[0].SourceVersion, AtMS: 20000, Direction: direction})
		if err == nil || !strings.HasPrefix(err.Error(), "frame_unavailable:") {
			t.Fatalf("window edge reported as media edge: %v", err)
		}
	}
	raw = "0.000000\n"
	_, err := s.NavigateFrame(context.Background(), EditFrameRequest{ProjectID: view.ID, VideoID: v.ID, SourceVersion: pre.Sources[0].SourceVersion, AtMS: 0, Direction: 1})
	if err == nil {
		t.Fatal("sparse next frame silently clamped")
	}
}

func TestVideoEditNavigateFrameChecksAfterRead(t *testing.T) {
	s, media := newFakeEditService(t)
	v := media.addFakeSource(t, t.TempDir(), "change.mkv", 4000)
	view := mustEditProject(t, s, models.VideoEditKindCrop, v.ID)
	pre, _ := s.PreflightProject(context.Background(), view.ID)
	original := s.runOutput
	s.runOutput = func(ctx context.Context, name string, args []string) (string, string, error) {
		if strings.Contains(strings.Join(args, " "), "frame=best_effort_timestamp_time") {
			if err := os.WriteFile(v.Path, []byte("modified while probing"), 0600); err != nil {
				t.Fatal(err)
			}
			return "0\n0.04\n", "", nil
		}
		return original(ctx, name, args)
	}
	_, err := s.NavigateFrame(context.Background(), EditFrameRequest{ProjectID: view.ID, VideoID: v.ID, SourceVersion: pre.Sources[0].SourceVersion, Direction: 1})
	if err == nil || !strings.HasPrefix(err.Error(), "source_changed:") {
		t.Fatalf("source change ignored: %v", err)
	}
}

func TestVideoEditNavigateRealVFRFrames(t *testing.T) {
	ffmpeg, _ := requireEditFFmpeg(t)
	s := newRealEditService(t)
	path := filepath.Join(t.TempDir(), "vfr.mkv")
	runFixtureCommand(t, ffmpeg, "-v", "error", "-threads", "2", "-filter_threads", "1", "-f", "lavfi", "-i", "testsrc2=s=160x90:r=25:d=2", "-vf", "select='eq(n,0)+eq(n,1)+eq(n,4)+eq(n,10)+eq(n,30)'", "-fps_mode", "vfr", "-c:v", "libx264", "-threads", "2", path)
	v := insertEditVideo(t, path)
	view := mustEditProject(t, s, models.VideoEditKindCrop, v.ID)
	pre, err := s.PreflightProject(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := EditFrameRequest{ProjectID: view.ID, VideoID: v.ID, SourceVersion: pre.Sources[0].SourceVersion, AtMS: 40, Direction: 1}
	got, err := s.NavigateFrame(context.Background(), req)
	if err != nil || got.AtMS != 160 {
		t.Fatalf("actual VFR next: %+v %v", got, err)
	}
	req.AtMS = 160
	req.Direction = -1
	got, err = s.NavigateFrame(context.Background(), req)
	if err != nil || got.AtMS != 40 {
		t.Fatalf("actual VFR previous: %+v %v", got, err)
	}
}
