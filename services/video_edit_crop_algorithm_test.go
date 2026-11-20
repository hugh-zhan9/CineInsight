package services

import (
	"context"
	"path/filepath"
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
	r := editCropWindow(cropNeutralSample(string(data)), width, height)
	if r == nil || *r != actual {
		t.Fatalf("want measured edge %+v, got %+v", actual, r)
	}
	if got, want := editCropMapToDisplay(*r, width, height, width, height), (EditCropRect{X: 28, Y: 14, Width: 272, Height: 156}); got != want {
		t.Fatalf("outward even rounding: want %+v, got %+v", want, got)
	}
}

func TestEditCropLumaConservativeMixedFrames(t *testing.T) {
	const width, height = 320, 180
	full := EditCropRect{Width: width, Height: height}
	border := EditCropRect{Y: 20, Width: width, Height: 140}
	fade := cropLumaFixture(width, height, full, 0, 28)
	letterbox := cropLumaFixture(width, height, border, 5, 85)
	open := cropLumaFixture(width, height, full, 5, 85)
	if r := editCropWindow(cropNeutralSample(fade), width, height); r != nil {
		t.Fatalf("dark shot is not evidence: %+v", r)
	}
	if r := editCropWindow(cropNeutralSample(fade)+cropNeutralSample(letterbox), width, height); r == nil || *r != border {
		t.Fatalf("fade should not narrow/invalidate stable crop: %+v", r)
	}
	if r := editCropWindow(cropNeutralSample(letterbox)+cropNeutralSample(open), width, height); r == nil || *r != full {
		t.Fatalf("real full-bleed frame must retain the full picture: %+v", r)
	}
	// Near-black margins with a low-contrast transition look like actual
	// content, not padding, and should retain the affected side.
	gradient := []byte(open)
	for y := 0; y < 28; y++ {
		for x := 0; x < width; x++ {
			gradient[y*width+x] = byte(y * 2)
		}
	}
	if r := editCropWindow(cropNeutralSample(string(gradient)), width, height); r == nil || *r != full {
		t.Fatalf("low-contrast shadow shouldn't be cropped: %+v", r)
	}
}

func TestEditCropSample4KOutwardTolerance(t *testing.T) {
	w, h := editCropSampleSize(3840, 2160)
	if w != 1920 || h != 1080 {
		t.Fatalf("sample [%d %d]", w, h)
	}
	r := editCropMapToDisplay(EditCropRect{Y: 60, Width: w, Height: 960}, w, h, 3840, 2160)
	if r != (EditCropRect{Y: 118, Width: 3840, Height: 1924}) {
		t.Fatalf("downsample edge needs a conservative buffer: %+v", r)
	}
}

func TestEditCropLumaHairlineAndCornerDetail(t *testing.T) {
	const width, height = 320, 180
	full := EditCropRect{Width: width, Height: height}
	thin := EditCropRect{Y: 1, Width: width, Height: height - 1}
	if r := editCropWindow(cropNeutralSample(cropLumaFixture(width, height, thin, 0, 100)), width, height); r == nil || *r != full {
		t.Fatalf("one-pixel hairline cannot be safely trimmed on the two-pixel export grid: %+v", r)
	}
	bar := EditCropRect{Y: 20, Width: width, Height: height - 20}
	body := []byte(cropLumaFixture(width, height, bar, 0, 100))
	for y := 0; y < 12; y++ {
		for x := 0; x < 18; x++ {
			body[y*width+x] = 140
		}
	}
	if r := editCropWindow(cropNeutralSample(string(body)), width, height); r == nil || *r != full {
		t.Fatalf("real corner detail should veto crop even if the rest of the edge is black: %+v", r)
	}
	onlyCorner := cropLumaFixture(width, height, EditCropRect{Width: 18, Height: 12}, 10, 140)
	if r := editCropWindow(cropNeutralSample(onlyCorner), width, height); r == nil || *r != full {
		t.Fatalf("a dark frame with clearly visible corner content must not be discarded: %+v", r)
	}
}

func TestEditCropChromaPreservesDarkBlueEdge(t *testing.T) {
	const width, height = 320, 180
	full := EditCropRect{Width: width, Height: height}
	body := EditCropRect{Y: 20, Width: width, Height: height - 20}
	data := []byte(cropNeutralSample(cropLumaFixture(width, height, body, 12, 80)))
	for y := 0; y < 10; y++ {
		for x := 0; x < width/2; x++ {
			data[width*height+y*(width/2)+x] = 175
		}
	}
	if r := editCropWindow(string(data), width, height); r == nil || *r != full {
		t.Fatalf("dark blue pixels are real content, not black padding: %+v", r)
	}
}

func TestEditCropLumaFFmpegLiftedBlackAndOpenFrame(t *testing.T) {
	ffmpeg, _ := requireEditFFmpeg(t)
	for _, tc := range []struct {
		name, luminance, cb, status string
		want                        EditCropRect
	}{
		{"fixed", "if(between(X,28,299)*between(Y,14,167),120,30)", "128", "detected", EditCropRect{X: 28, Y: 14, Width: 272, Height: 154}},
		// A clearly full-bleed frame in the fourth sampled window means a
		// single fixed crop must keep the image used by that scene.
		{"changing", "if(between(T,2,3),120,if(between(X,28,299)*between(Y,14,167),120,30))", "128", "no_borders", EditCropRect{Width: 320, Height: 180}},
		{"dark-blue", "if(gte(Y,20),120,28)", "if(lt(Y,10),175,128)", "no_borders", EditCropRect{Width: 320, Height: 180}},
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
			if err != nil || got.Status != tc.status || got.Rect == nil || *got.Rect != tc.want {
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
		editCropWindow(sample, width, height)
	}
}
