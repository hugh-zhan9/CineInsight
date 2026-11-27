package services

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func cropLumaFixture(width, height int, content EditCropRect, bar, picture byte) string {
	pixels := make([]byte, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			pixels[y*width+x] = bar
			if x >= content.X && x < content.X+content.Width && y >= content.Y && y < content.Y+content.Height {
				pixels[y*width+x] = picture
			}
		}
	}
	return string(pixels)
}

func cropNeutralSample(luma string) string {
	return luma + strings.Repeat("\x80", len(luma)/2)
}

func TestEditCropLumaLiftedBlackAsymmetricAndNoise(t *testing.T) {
	const width, height = 320, 180
	actual := EditCropRect{X: 29, Y: 15, Width: 270, Height: 154}
	data := []byte(cropLumaFixture(width, height, actual, 16, 72))
	// An isolated hot pixel should not erase the left bar suggestion.
	data[height/2*width+7] = 220
	obs := editCropWindowObs(cropNeutralSample(string(data)), width, height)
	if obs == nil {
		t.Fatal("lifted black borders discarded")
	}
	for side, inset := range [4]int{29, 21, 15, 11} {
		if got := obs[side]; got.kind != edgeBorder || got.inset != inset {
			t.Fatalf("side %d: want border of %dpx, got %+v", side, inset, got)
		}
	}
	status, got := editCropResolve(slices.Repeat([]editCropWindowObservation{*obs}, 4), width, height, width, height)
	if want := (EditCropRect{X: 28, Y: 14, Width: 272, Height: 156}); status != "detected" || got == nil || *got != want {
		t.Fatalf("outward even rounding: want %+v, got %s %+v", want, status, got)
	}
}

func TestEditCropLumaConservativeMixedFrames(t *testing.T) {
	const width, height = 320, 180
	full := EditCropRect{Width: width, Height: height}
	border := EditCropRect{Y: 20, Width: width, Height: 140}
	fade := cropNeutralSample(cropLumaFixture(width, height, full, 0, 23))
	letterbox := cropNeutralSample(cropLumaFixture(width, height, border, 5, 85))
	thicker := cropNeutralSample(cropLumaFixture(width, height, EditCropRect{Y: 24, Width: width, Height: 132}, 0, 100))
	openTop := cropNeutralSample(cropLumaFixture(width, height, EditCropRect{Width: width, Height: 160}, 5, 85))
	if r := editCropWindowObs(fade, width, height); r != nil {
		t.Fatalf("dark shot is not evidence: %+v", r)
	}
	want := editCropSideObs{kind: edgeBorder, inset: 20, barMean: 5, stepBrightPct: 100}
	for _, raw := range []string{fade + letterbox, thicker + letterbox, letterbox + thicker} {
		obs := editCropWindowObs(raw, width, height)
		if obs == nil || obs[editCropTop] != want || obs[editCropBottom] != want {
			t.Fatalf("want smallest border evidence on both sides, got %+v", obs)
		}
	}
	for _, raw := range []string{letterbox + openTop, openTop + letterbox} {
		obs := editCropWindowObs(raw, width, height)
		if obs == nil || obs[editCropTop].kind != edgeOpen || obs[editCropBottom] != want {
			t.Fatalf("open content must veto only the affected side: %+v", obs)
		}
	}
}

func TestEditCropSample4KOutwardTolerance(t *testing.T) {
	w, h := editCropSampleSize(3840, 2160)
	if w != 1920 || h != 1080 {
		t.Fatalf("sample [%d %d]", w, h)
	}
	raw := cropNeutralSample(cropLumaFixture(w, h, EditCropRect{Y: 60, Width: w, Height: 960}, 0, 100))
	obs := editCropWindowObs(raw, w, h)
	if obs == nil {
		t.Fatal("no sample")
	}
	status, r := editCropResolve(slices.Repeat([]editCropWindowObservation{*obs}, 4), w, h, 3840, 2160)
	if status != "detected" || r == nil || *r != (EditCropRect{Y: 118, Width: 3840, Height: 1924}) {
		t.Fatalf("downsample edge needs a conservative buffer: %s %+v", status, r)
	}
}

func TestEditCropLumaHairlineAndCornerDetail(t *testing.T) {
	const width, height = 320, 180
	thin := EditCropRect{Y: 1, Width: width, Height: height - 1}
	if r := editCropWindowObs(cropNeutralSample(cropLumaFixture(width, height, thin, 0, 100)), width, height); r == nil || r[editCropTop].kind != edgeOpen {
		t.Fatalf("one-pixel hairline cannot be safely trimmed on the two-pixel export grid: %+v", r)
	}
	bar := EditCropRect{Y: 20, Width: width, Height: height - 20}
	body := []byte(cropLumaFixture(width, height, bar, 0, 100))
	for y := 0; y < 12; y++ {
		for x := 0; x < 18; x++ {
			body[y*width+x] = 140
		}
	}
	if r := editCropWindowObs(cropNeutralSample(string(body)), width, height); r == nil || r[editCropTop].kind != edgeOpen {
		t.Fatalf("real corner detail should veto crop even if the rest of the edge is black: %+v", r)
	}
	onlyCorner := cropLumaFixture(width, height, EditCropRect{Width: 18, Height: 12}, 10, 140)
	open := editCropSideObs{kind: edgeOpen}
	if r := editCropWindowObs(cropNeutralSample(onlyCorner), width, height); r == nil || *r != (editCropWindowObservation{editCropLeft: open, editCropTop: open}) {
		t.Fatalf("a dark frame with clearly visible corner content must veto only the detailed edges: %+v", r)
	}
	leftEdgeLamp := cropLumaFixture(width, height, EditCropRect{X: 0, Y: 40, Width: 10, Height: 20}, 0, 160)
	if r := editCropWindowObs(cropNeutralSample(leftEdgeLamp), width, height); r == nil || *r != (editCropWindowObservation{editCropLeft: open}) {
		t.Fatalf("left-edge detail in a dark shot must not veto top/bottom letterbox: %+v", r)
	}
}

func TestEditCropChromaPreservesDarkBlueEdge(t *testing.T) {
	const width, height = 320, 180
	body := EditCropRect{Y: 20, Width: width, Height: height - 20}
	data := []byte(cropNeutralSample(cropLumaFixture(width, height, body, 12, 80)))
	for y := 0; y < 10; y++ {
		for x := 0; x < width/2; x++ {
			data[width*height+y*(width/2)+x] = 175
		}
	}
	if r := editCropWindowObs(string(data), width, height); r == nil || r[editCropTop].kind != edgeOpen {
		t.Fatalf("dark blue pixels are real content, not black padding: %+v", r)
	}
}

func TestEditCropResolveStaticScenes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
		content       EditCropRect
		bar           byte
		status        string
	}{
		{"letterbox", 320, 180, EditCropRect{Y: 20, Width: 320, Height: 140}, 0, "detected"},
		{"pillarbox", 320, 180, EditCropRect{X: 40, Width: 240, Height: 180}, 0, "detected"},
		{"portrait-in-landscape", 1920, 1080, EditCropRect{X: 656, Width: 608, Height: 1080}, 0, "detected"},
		{"single-20px-at-1080p", 1920, 1080, EditCropRect{Y: 20, Width: 1920, Height: 1060}, 0, "no_borders"},
		{"single-thick-shadow", 320, 180, EditCropRect{Y: 24, Width: 320, Height: 156}, 14, "no_borders"},
		{"less-than-3-percent", 1920, 1080, EditCropRect{Y: 16, Width: 1920, Height: 1048}, 0, "no_borders"},
		{"less-than-20-percent-remaining", 320, 180, EditCropRect{X: 144, Width: 32, Height: 180}, 0, "undetected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := cropNeutralSample(cropLumaFixture(tc.width, tc.height, tc.content, tc.bar, 100))
			obs := editCropWindowObs(raw, tc.width, tc.height)
			if obs == nil {
				t.Fatal("no sample")
			}
			status, r := editCropResolve(slices.Repeat([]editCropWindowObservation{*obs}, 4), tc.width, tc.height, tc.width, tc.height)
			want := tc.content
			if tc.status == "no_borders" {
				want = EditCropRect{Width: tc.width, Height: tc.height}
			}
			if status != tc.status || status == "undetected" && r != nil || status != "undetected" && (r == nil || *r != want) {
				t.Fatalf("want %s %+v, got %s %+v", tc.status, want, status, r)
			}
		})
	}
}

func TestEditCropResolveThinBorders(t *testing.T) {
	for _, full := range []EditCropRect{{Width: 320, Height: 180}, {Width: 1920, Height: 1080}} {
		for inset := 2; inset <= 6; inset++ {
			t.Run(fmt.Sprintf("%dp/%dpx", full.Height, inset), func(t *testing.T) {
				w, h := full.Width, full.Height
				content := EditCropRect{X: inset, Y: inset, Width: w - 2*inset, Height: h - 2*inset}
				raw := cropNeutralSample(cropLumaFixture(w, h, content, 0, 100))
				obs := editCropWindowObs(raw, w, h)
				if obs == nil {
					t.Fatal("no sample")
				}
				status, r := editCropResolve(slices.Repeat([]editCropWindowObservation{*obs}, 4), w, h, w, h)
				if status != "no_borders" || r == nil || *r != full {
					t.Fatalf("thin borders must be retained: %s %+v", status, r)
				}
			})
		}
	}
}

func TestEditCropResolveNightScenes(t *testing.T) {
	const width, height = 320, 320
	full := EditCropRect{Width: width, Height: height}
	night := make([]byte, width*height)
	gradient := make([]byte, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			night[y*width+x] = byte(5 + (x+y)%10)
			if x >= 90 && x < 230 && y >= 35 && y < 48 {
				night[y*width+x] = 100
			}
			gradient[y*width+x] = byte(min(y, height-1-y, 20) * 6)
		}
	}
	if obs := editCropWindowObs(cropNeutralSample(string(night)), width, height); obs != nil {
		t.Fatalf("night shot with a small bright object is not evidence: %+v", obs)
	}
	obs := editCropWindowObs(cropNeutralSample(string(gradient)), width, height)
	if obs == nil || obs[editCropTop].kind != edgeAmbiguous || obs[editCropBottom].kind != edgeAmbiguous {
		t.Fatalf("dark gradient is not a flat border: %+v", obs)
	}
	windows := make([]editCropWindowObservation, 0, 4)
	for _, inset := range []int{120, 40, 82, 18} {
		raw := cropNeutralSample(cropLumaFixture(width, height, EditCropRect{Y: inset, Width: width, Height: height - 2*inset}, 14, 100))
		obs := editCropWindowObs(raw, width, height)
		if obs == nil || obs[editCropTop].kind != edgeBorder || obs[editCropBottom].kind != edgeBorder {
			t.Fatalf("want plausible but inconsistent shadow evidence at %dpx: %+v", inset, obs)
		}
		windows = append(windows, *obs)
	}
	for _, windows := range [][]editCropWindowObservation{windows, slices.Repeat([]editCropWindowObservation{*obs}, 4)} {
		status, r := editCropResolve(windows, width, height, width, height)
		if status != "no_borders" || r == nil || *r != full {
			t.Fatalf("dark scene must retain full canvas: %s %+v", status, r)
		}
	}
}

func TestEditCropResolveUnpairedConsensus(t *testing.T) {
	const width, height = 320, 180
	full := EditCropRect{Width: width, Height: height}
	crop := EditCropRect{Y: 20, Width: width, Height: 160}
	raw := cropNeutralSample(cropLumaFixture(width, height, crop, 0, 100))
	obs := editCropWindowObs(raw, width, height)
	if obs == nil {
		t.Fatal("no sample")
	}
	windows := make([]editCropWindowObservation, 11)
	for i := 0; i < 9; i++ {
		windows[i] = *obs
	}
	// Include the upper edge of the 2px tolerance in the winning cluster.
	windows[8][editCropTop].inset += 2
	status, r := editCropResolve(windows, width, height, width, height)
	if status != "detected" || r == nil || *r != crop {
		t.Fatalf("nine consistent windows must retain a thick, black unpaired border: %s %+v", status, r)
	}
	windows[8][editCropTop].inset++
	status, r = editCropResolve(windows, width, height, width, height)
	if status != "no_borders" || r == nil || *r != full {
		t.Fatalf("eight consistent windows do not justify unpaired crop: %s %+v", status, r)
	}
	status, r = editCropResolve(windows[:3], width, height, width, height)
	if status != "undetected" || r != nil {
		t.Fatalf("three windows are not enough evidence: %s %+v", status, r)
	}
}

func TestEditCropLumaFFmpeg(t *testing.T) {
	ffmpeg, _ := requireEditFFmpeg(t)
	for _, tc := range []struct {
		name, luminance, cb, status string
		want                        *EditCropRect
	}{
		{"fixed", "if(between(X,28,299)*between(Y,14,167),120,30)", "128", "detected", &EditCropRect{X: 28, Y: 14, Width: 272, Height: 154}},
		// Only the 50% sample window sees a full picture.
		{"changing", "if(between(T,2.45,2.70),120,if(between(X,28,299)*between(Y,14,167),120,30))", "128", "no_borders", &EditCropRect{Width: 320, Height: 180}},
		{"dark-blue", "if(gte(Y,20),120,28)", "if(lt(Y,10),175,128)", "no_borders", &EditCropRect{Width: 320, Height: 180}},
		{"night", "if(between(Y,26,32)*between(X,90,229),100,19+mod(X+Y,9))", "128", "undetected", nil},
		{"4px-borders", "if(between(Y,4,175),120,16)", "128", "no_borders", &EditCropRect{Width: 320, Height: 180}},
		{"single-shadow", "if(gte(Y,24),120,28)", "128", "no_borders", &EditCropRect{Width: 320, Height: 180}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newRealEditService(t)
			path := filepath.Join(t.TempDir(), "raised-blacks.mkv")
			runFixtureCommand(t, ffmpeg, "-v", "error", "-threads", "2", "-filter_threads", "1", "-f", "lavfi", "-i", "color=black:s=320x180:r=12:d=5",
				"-vf", "geq=lum='"+tc.luminance+"':cb='"+tc.cb+"':cr=128", "-c:v", "libx264", "-threads", "2", "-crf", "18", path)
			probe, err := s.probeEditSource(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			size, mtime, err := editFileFingerprint(path)
			if err != nil {
				t.Fatal(err)
			}
			sc := &editSourceCtx{path: path, probe: probe, size: size, modTimeNS: mtime}
			got, err := s.detectCropSource(context.Background(), ffmpeg, sc, nil)
			if err != nil || got.Status != tc.status || got.Applied != (tc.want != nil) ||
				(tc.want == nil) != (got.Rect == nil) || tc.want != nil && *got.Rect != *tc.want {
				t.Fatalf("want %s %+v, got %+v (rect %+v), err %v", tc.status, tc.want, got, got.Rect, err)
			}
		})
	}
}

func BenchmarkEditCropSample1080p(b *testing.B) {
	const width, height = 1920, 1080
	sample := cropNeutralSample(cropLumaFixture(width, height, EditCropRect{Y: 130, Width: width, Height: 820}, 16, 100))
	b.ReportAllocs()
	b.SetBytes(int64(len(sample)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		editCropWindowObs(sample, width, height)
	}
}
