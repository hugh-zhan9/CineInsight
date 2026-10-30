package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"video-master/models"
)

func TestVideoEditCropAnalysisProvidesCanvasWithoutPreflight(t *testing.T) {
	for _, tc := range []struct{ status, sample string }{
		{"detected", "crop=100:180:110:0"},
		{"no_borders", "crop=320:180:0:0"},
		{"undetected", ""},
	} {
		t.Run(tc.status, func(t *testing.T) {
			s, media := newFakeEditService(t)
			v := media.addFakeSource(t, t.TempDir(), "a.mkv", 4000)
			view := mustEditProject(t, s, models.VideoEditKindCrop, v.ID)
			original := s.runOutput
			s.runOutput = func(ctx context.Context, name string, args []string) (string, string, error) {
				if strings.Contains(strings.Join(args, " "), "cropdetect") {
					return "", tc.sample, nil
				}
				return original(ctx, name, args)
			}
			_, err := s.AnalyzeProject(context.Background(), EditAnalyzeRequest{ProjectID: view.ID, ExpectedRevision: view.Revision})
			if err != nil {
				t.Fatal(err)
			}
			got := waitEditProject(t, s, view.ID, 5*time.Second, models.VideoEditStatusDraft)
			raw, err := json.Marshal(got.Analysis)
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Crops []map[string]any `json:"crops"`
			}
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Crops) != 1 {
				t.Fatalf("analysis: %s", raw)
			}
			size, modified, err := editFileFingerprint(v.Path)
			if err != nil {
				t.Fatal(err)
			}
			crop := result.Crops[0]
			if crop["status"] != tc.status || crop["display_width"] != float64(320) || crop["display_height"] != float64(180) || crop["duration_ms"] != float64(4000) || crop["source_version"] != editCropSourceVersion(size, modified) {
				t.Fatalf("missing canvas for manual editing: %s", raw)
			}
		})
	}
}

func TestVideoEditCropPreflight(t *testing.T) {
	s, media := newFakeEditService(t)
	v := media.addFakeSource(t, t.TempDir(), "portrait.mkv", 4000)
	view := mustEditProject(t, s, models.VideoEditKindCrop, v.ID)
	pre, err := s.PreflightProject(context.Background(), view.ID)
	if err != nil || pre.Ready || pre.Sources[0].DisplayWidth != 320 {
		t.Fatalf("empty crop: %+v %v", pre, err)
	}
	version := pre.Sources[0].SourceVersion
	tests := []struct {
		name          string
		rect          *EditCropRect
		confirmed     bool
		version, code string
	}{
		{"unconfirmed", &EditCropRect{X: 110, Width: 100, Height: 180}, false, version, "unconfirmed"},
		{"negative", &EditCropRect{X: -2, Width: 100, Height: 180}, true, version, "crop_invalid"},
		{"odd", &EditCropRect{X: 111, Width: 100, Height: 180}, true, version, "crop_invalid"},
		{"zero", &EditCropRect{Width: 0, Height: 180}, true, version, "crop_invalid"},
		{"outside", &EditCropRect{X: 222, Width: 100, Height: 180}, true, version, "crop_invalid"},
		{"huge", &EditCropRect{X: int(^uint(0)>>1) - 1, Width: 100, Height: 180}, true, version, "crop_invalid"},
		{"stale", &EditCropRect{X: 110, Width: 100, Height: 180}, true, "old", "source_changed"},
		{"valid", &EditCropRect{X: 110, Width: 100, Height: 180}, true, version, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			view = mustUpdateRecipe(t, s, view, func(r *EditRecipe) {
				r.Crop.Items[0] = EditCropItem{VideoID: v.ID, Rect: tc.rect, SourceVersion: tc.version, Confirmed: tc.confirmed}
			})
			p, err := s.PreflightProject(context.Background(), view.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.code != "" {
				if p.Ready || !editIssueCodes(p.Errors)[tc.code] {
					t.Fatalf("want %s: %+v", tc.code, p)
				}
				return
			}
			if !p.Ready || len(p.Outputs) != 1 || p.Outputs[0].Width != 100 || p.Outputs[0].Height != 180 || p.Fast.Available {
				t.Fatalf("crop plan: %+v", p)
			}
		})
	}
	_, err = s.UpdateRecipe(context.Background(), EditRecipeUpdateRequest{ProjectID: view.ID, ExpectedRevision: view.Revision, Recipe: view.Recipe, Mode: models.VideoEditModeFast})
	if err != nil {
		t.Fatal(err)
	}
	fast, _ := s.PreflightProject(context.Background(), view.ID)
	if fast.Ready || !editIssueCodes(fast.Errors)["fast_unavailable"] {
		t.Fatalf("crop accepted fast export: %+v", fast)
	}
	current, _ := s.GetProject(context.Background(), view.ID)
	view, err = s.UpdateRecipe(context.Background(), EditRecipeUpdateRequest{ProjectID: view.ID, ExpectedRevision: current.Revision, Recipe: current.Recipe, Mode: models.VideoEditModePrecise})
	if err != nil {
		t.Fatal(err)
	}
	pauseEditWorker(s)
	result := queueAllAcknowledged(t, s, view.ID)
	if !result.Queued {
		t.Fatal("not queued")
	}
	items, _ := loadEditItems(context.Background(), view.ID)
	if !strings.Contains(items[0].PlanJSON, `"crop":{"x":110`) {
		t.Fatal(items[0].PlanJSON)
	}
}

func TestVideoEditCropRangePlan(t *testing.T) {
	s, media := newFakeEditService(t)
	v := media.addFakeSource(t, t.TempDir(), "range.mkv", 4000)
	view := mustEditProject(t, s, models.VideoEditKindCrop, v.ID)
	pre, _ := s.PreflightProject(context.Background(), view.ID)
	project, _ := loadEditProject(context.Background(), view.ID)
	for _, tc := range []struct {
		name, interval  string
		start, duration int64
		invalid         bool
	}{
		{"legacy", "null", 0, 4000, false},
		{"selection", `{"start_ms":1000,"end_ms":3000}`, 1000, 2000, false},
		{"one_frame", `{"start_ms":1000,"end_ms":1040}`, 1000, 40, false},
		{"empty", `{"start_ms":1000,"end_ms":1000}`, 0, 0, true},
		{"outside", `{"start_ms":0,"end_ms":4001}`, 0, 0, true},
		{"negative", `{"start_ms":-1,"end_ms":1000}`, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := view.Recipe
			r.Crop = &EditCropRecipe{Items: []EditCropItem{{}}}
			raw := fmt.Sprintf(`{"video_id":%d,"rect":{"x":0,"y":0,"width":320,"height":180},"source_version":%q,"confirmed":true,"range":%s}`, v.ID, pre.Sources[0].SourceVersion, tc.interval)
			if err := json.Unmarshal([]byte(raw), &r.Crop.Items[0]); err != nil {
				t.Fatal(err)
			}
			p, plans, err := s.preflight(context.Background(), project, r, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.invalid {
				if p.Ready {
					t.Fatalf("invalid range accepted: %s", raw)
				}
				return
			}
			if !p.Ready || len(plans) != 1 {
				t.Fatalf("range preflight: %+v", p)
			}
			segment := plans[0].Segments[0]
			if segment.InMS != tc.start || segment.SidecarInMS != tc.start || segment.DurMS != tc.duration || plans[0].DurationMS != tc.duration {
				t.Fatalf("range lost: %+v", plans[0])
			}
		})
	}
}

func TestVideoEditCropTailWithoutFrameRejected(t *testing.T) {
	ffmpeg, _ := requireEditFFmpeg(t)
	s := newRealEditService(t)
	path := filepath.Join(t.TempDir(), "tail.mp4")
	runFixtureCommand(t, ffmpeg, "-v", "error", "-threads", "2", "-filter_threads", "1", "-f", "lavfi", "-i", "testsrc2=s=160x90:r=25:d=2", "-c:v", "libx264", "-threads", "2", path)
	v := insertEditVideo(t, path)
	view := mustEditProject(t, s, models.VideoEditKindCrop, v.ID)
	pre, _ := s.PreflightProject(context.Background(), view.ID)
	view = mustUpdateRecipe(t, s, view, func(r *EditRecipe) {
		r.Crop.Items[0] = EditCropItem{VideoID: v.ID, Rect: &EditCropRect{Width: 160, Height: 90}, SourceVersion: pre.Sources[0].SourceVersion, Confirmed: true, Range: &EditCropRange{StartMS: 1999, EndMS: 2000}}
	})
	pre, err := s.PreflightProject(context.Background(), view.ID)
	if err != nil || pre.Ready || !editIssueCodes(pre.Errors)["range_invalid"] {
		t.Fatalf("empty tail accepted: %+v %v", pre, err)
	}
	view = mustUpdateRecipe(t, s, view, func(r *EditRecipe) { r.Crop.Items[0].Range.StartMS = 1960 })
	pre, err = s.PreflightProject(context.Background(), view.ID)
	if err != nil || !pre.Ready {
		t.Fatalf("real last frame rejected: %+v %v", pre, err)
	}
	queueAllAcknowledged(t, s, view.ID)
	done := waitEditProject(t, s, view.ID, 30*time.Second, models.VideoEditStatusCompleted, models.VideoEditStatusFailed)
	if done.Status != models.VideoEditStatusCompleted {
		t.Fatalf("last frame export failed: %+v", done)
	}
	_, out := outputPathOf(t, done, 1)
	if p, err := s.probeEditSource(context.Background(), out); err != nil || p.Video == nil || p.DurationMS < 30 || p.DurationMS > 100 {
		t.Fatalf("last frame output: %+v %v", p, err)
	}
}

func TestVideoEditCropSparseRangeKeepsDistantFrame(t *testing.T) {
	ffmpeg, _ := requireEditFFmpeg(t)
	s := newRealEditService(t)
	path := filepath.Join(t.TempDir(), "sparse.mkv")
	runFixtureCommand(t, ffmpeg, "-v", "error", "-threads", "2", "-filter_threads", "1", "-f", "lavfi", "-i", "testsrc2=s=160x90:r=1:d=3", "-vf", "setpts=20*PTS", "-fps_mode", "vfr", "-c:v", "libx264", "-threads", "2", path)
	v := insertEditVideo(t, path)
	view := mustEditProject(t, s, models.VideoEditKindCrop, v.ID)
	pre, _ := s.PreflightProject(context.Background(), view.ID)
	view = mustUpdateRecipe(t, s, view, func(r *EditRecipe) {
		r.Crop.Items[0] = EditCropItem{VideoID: v.ID, Rect: &EditCropRect{Width: 160, Height: 90}, SourceVersion: pre.Sources[0].SourceVersion, Confirmed: true, Range: &EditCropRange{StartMS: 5000, EndMS: 40000}}
	})
	pre, err := s.PreflightProject(context.Background(), view.ID)
	if err != nil || !pre.Ready {
		t.Fatalf("distant real frame was rejected: %+v %v", pre, err)
	}
}

func TestVideoEditCropStructureAndGeometry(t *testing.T) {
	for _, n := range []int{0, 1, 200, 201} {
		ids := make([]uint, n)
		for i := range ids {
			ids[i] = uint(i + 1)
		}
		r, err := defaultEditRecipe(models.VideoEditKindCrop, ids)
		if (err == nil) != (n >= 1 && n <= 200) {
			t.Fatalf("count %d: %v", n, err)
		}
		if err == nil && len(editRecipeVideoIDs(r)) != n {
			t.Fatal("missing ids")
		}
	}
	if _, err := defaultEditRecipe(models.VideoEditKindCrop, []uint{1, 1}); err == nil {
		t.Fatal("duplicate accepted")
	}
	for _, tc := range []struct {
		sar      string
		rotation float64
		w, h     int
	}{
		{"1:1", 0, 320, 180}, {"2:1", 0, 640, 180}, {"2:1", 90, 180, 640}, {"1:1", -90, 180, 320},
	} {
		w, h := editDisplaySize(&editStream{Width: 320, Height: 180, SampleAspectRatio: tc.sar, Rotation: tc.rotation})
		if w != tc.w || h != tc.h {
			t.Fatalf("display %v: %dx%d", tc, w, h)
		}
	}
	r := editCropUnion([]EditCropRect{{X: 100, Y: 20, Width: 100, Height: 100}, {X: 90, Y: 10, Width: 120, Height: 120}})
	if *r != (EditCropRect{X: 90, Y: 10, Width: 120, Height: 120}) {
		t.Fatalf("union: %+v", r)
	}
	if parseEditCropSample("crop=0:0:320:180 crop=100:100:900:0", 320, 180) != nil {
		t.Fatal("invalid sample accepted")
	}
}

func TestVideoEditCropAnalysisFailureAndCancellation(t *testing.T) {
	for _, cancelRun := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRun), func(t *testing.T) {
			s, media := newFakeEditService(t)
			v := media.addFakeSource(t, t.TempDir(), "a.mkv", 4000)
			view := mustEditProject(t, s, models.VideoEditKindCrop, v.ID)
			original := s.runOutput
			started := make(chan struct{}, 1)
			s.runOutput = func(ctx context.Context, name string, args []string) (string, string, error) {
				if !strings.Contains(strings.Join(args, " "), "cropdetect") {
					return original(ctx, name, args)
				}
				started <- struct{}{}
				if cancelRun {
					<-ctx.Done()
					return "", "", ctx.Err()
				}
				return "", "", errors.New("decode failed")
			}
			_, err := s.AnalyzeProject(context.Background(), EditAnalyzeRequest{ProjectID: view.ID, ExpectedRevision: view.Revision})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("not started")
			}
			if cancelRun {
				if err := s.CancelAnalysis(context.Background(), view.ID); err != nil {
					t.Fatal(err)
				}
			}
			got := waitEditProject(t, s, view.ID, 5*time.Second, models.VideoEditStatusDraft)
			if got.Recipe.Crop.Items[0].Rect != nil || got.Revision != view.Revision {
				t.Fatalf("failed analysis changed recipe: %+v", got)
			}
			want := "failed"
			if cancelRun {
				want = "cancelled"
			}
			if got.Analysis.Status != want {
				t.Fatalf("status %+v", got.Analysis)
			}
		})
	}
}

func TestVideoEditCropAnalysisKeepsConfirmedAndDarkFrames(t *testing.T) {
	s, media := newFakeEditService(t)
	dir := t.TempDir()
	a := media.addFakeSource(t, dir, "a.mkv", 4000)
	b := media.addFakeSource(t, dir, "b.mkv", 4000)
	view := mustEditProject(t, s, models.VideoEditKindCrop, a.ID, b.ID)
	pre, _ := s.PreflightProject(context.Background(), view.ID)
	view = mustUpdateRecipe(t, s, view, func(r *EditRecipe) {
		r.Crop.Items[0] = EditCropItem{VideoID: a.ID, Rect: &EditCropRect{X: 100, Width: 120, Height: 180}, SourceVersion: pre.Sources[0].SourceVersion, Confirmed: true}
	})
	original := s.runOutput
	s.runOutput = func(ctx context.Context, name string, args []string) (string, string, error) {
		if strings.Contains(strings.Join(args, " "), "cropdetect") {
			return "", "crop=0:0:320:180", nil
		}
		return original(ctx, name, args)
	}
	_, err := s.AnalyzeProject(context.Background(), EditAnalyzeRequest{ProjectID: view.ID, ExpectedRevision: view.Revision})
	if err != nil {
		t.Fatal(err)
	}
	got := waitEditProject(t, s, view.ID, 5*time.Second, models.VideoEditStatusDraft)
	if !got.Recipe.Crop.Items[0].Confirmed || got.Recipe.Crop.Items[1].Rect != nil || got.Analysis.Crops[1].Status != "undetected" {
		t.Fatalf("dark/confirmed: %+v", got)
	}
}

func TestVideoEditCropEndToEnd(t *testing.T) {
	ffmpeg, ffprobe := requireEditFFmpeg(t)
	for _, variant := range []string{"plain", "sar", "rotated", "selection"} {
		t.Run(variant, func(t *testing.T) {
			s := newRealEditService(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "borders.mp4")
			filter := "pad=320:180:112:10:black"
			if variant == "sar" {
				filter += ",setsar=2/1"
			}
			runFixtureCommand(t, ffmpeg, "-v", "error", "-threads", "2", "-filter_threads", "1", "-f", "lavfi", "-i", "color=c=green:s=96x160:r=25:d=2", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2", "-vf", filter, "-c:v", "libx264", "-threads", "2", "-c:a", "aac", "-shortest", path)
			if variant == "rotated" {
				tmp := filepath.Join(dir, "rotated.mp4")
				runFixtureCommand(t, ffmpeg, "-v", "error", "-display_rotation:v:0", "90", "-i", path, "-c", "copy", tmp)
				if err := os.Rename(tmp, path); err != nil {
					t.Fatal(err)
				}
			}
			sourceHash := sha256OfFile(t, path)
			writeEditTestFile(t, strings.TrimSuffix(path, ".mp4")+".srt", "1\n00:00:00,200 --> 00:00:01,200\n字幕\n")
			v := insertEditVideo(t, path)
			view := mustEditProject(t, s, models.VideoEditKindCrop, v.ID)
			_, err := s.AnalyzeProject(context.Background(), EditAnalyzeRequest{ProjectID: view.ID, ExpectedRevision: view.Revision})
			if err != nil {
				t.Fatal(err)
			}
			got := waitEditProject(t, s, view.ID, 30*time.Second, models.VideoEditStatusDraft)
			if got.Analysis == nil || got.Analysis.Status != "completed" {
				t.Fatalf("analysis: %+v", got.Analysis)
			}
			expected := EditCropRect{X: 112, Y: 10, Width: 96, Height: 160}
			if variant == "sar" {
				expected = EditCropRect{X: 224, Y: 10, Width: 192, Height: 160}
			}
			if variant == "rotated" {
				expected = EditCropRect{X: 10, Y: 112, Width: 160, Height: 96}
			}
			if got.Recipe.Crop.Items[0].Rect == nil || *got.Recipe.Crop.Items[0].Rect != expected {
				t.Fatalf("detection want %+v got %+v", expected, got.Recipe.Crop.Items[0].Rect)
			}
			got = mustUpdateRecipe(t, s, got, func(r *EditRecipe) {
				r.Crop.Items[0].Confirmed = true
				if variant == "selection" {
					r.Crop.Items[0].Range = &EditCropRange{StartMS: 500, EndMS: 1500}
				}
			})
			queueAllAcknowledged(t, s, got.ID)
			done := waitEditProject(t, s, got.ID, 30*time.Second, models.VideoEditStatusCompleted, models.VideoEditStatusFailed)
			if done.Status != models.VideoEditStatusCompleted {
				t.Fatalf("export: %+v", done)
			}
			_, out := outputPathOf(t, done, 1)
			_ = probeOutputFile(t, ffprobe, out)
			p, err := s.probeEditSource(context.Background(), out)
			wantDuration := int64(2000)
			if variant == "selection" {
				wantDuration = 1000
			}
			if err != nil || p.Video.Width != expected.Width || p.Video.Height != expected.Height || len(p.Audio) != 1 || p.DurationMS < wantDuration-100 || p.DurationMS > wantDuration+200 {
				t.Fatalf("output: %+v %v", p, err)
			}
			if sha256OfFile(t, path) != sourceHash {
				t.Fatal("source modified")
			}
			if !strings.Contains(readFileString(t, strings.TrimSuffix(out, filepath.Ext(out))+".srt"), "字幕") {
				t.Fatal("SRT lost")
			}
			if variant == "selection" && !strings.Contains(readFileString(t, strings.TrimSuffix(out, filepath.Ext(out))+".srt"), "00:00:00,000 --> 00:00:00,700") {
				t.Fatal("selection SRT was not clipped and shifted")
			}
			frame := grayFrame(t, ffmpeg, out, 0.5)
			for _, pixel := range frame {
				if pixel < 30 {
					t.Fatal("black border retained")
				}
			}
		})
	}
}

func TestVideoEditCropContinueSkipsCompletedSource(t *testing.T) {
	s, media := newFakeEditService(t)
	dir := t.TempDir()
	a, b := media.addFakeSource(t, dir, "a.mkv", 4000), media.addFakeSource(t, dir, "b.mkv", 4000)
	view := mustEditProject(t, s, models.VideoEditKindCrop, a.ID, b.ID)
	pre, _ := s.PreflightProject(context.Background(), view.ID)
	view = mustUpdateRecipe(t, s, view, func(r *EditRecipe) {
		for i := range r.Crop.Items {
			r.Crop.Items[i].Rect = &EditCropRect{X: 100, Width: 120, Height: 180}
			r.Crop.Items[i].Confirmed = true
			r.Crop.Items[i].SourceVersion = pre.Sources[i].SourceVersion
		}
	})
	if err := os.Remove(a.Path); err != nil {
		t.Fatal(err)
	}
	project, _ := loadEditProject(context.Background(), view.ID)
	p, plans, err := s.preflight(context.Background(), project, view.Recipe, map[int]bool{1: true})
	if err != nil || !p.Ready || len(plans) != 1 || plans[0].Seq != 2 || len(p.Sources) != 1 || p.Sources[0].VideoID != b.ID {
		t.Fatalf("continue completed source: %+v %+v %v", p, plans, err)
	}
}
