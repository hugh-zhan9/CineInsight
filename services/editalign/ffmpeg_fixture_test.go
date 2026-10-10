package editalign

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 真实 ffmpeg 夹具：用 geq 在 64×36 上画 7 个沿互不公约的正弦轨迹运动的明暗高斯斑，
// 再放大到 640×360。标准 lavfi 源（testsrc2、mandelbrot、life…）在 9×8 dHash 尺度上
// 几乎全是重复画面，锚点会被正确地判为有歧义；这种内容处处有信息量且不周期重复。
const fixtureFPS = 25

func requireFFmpeg(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	return path
}

func blobExpression() string {
	params := [][5]float64{
		{0.53, 0.31, 1.0, 2.0, 1}, {0.71, 0.47, 2.5, 0.3, -1}, {0.29, 0.83, 4.1, 1.7, 1},
		{0.97, 0.37, 0.7, 5.2, -1}, {0.43, 0.61, 3.3, 2.9, 1}, {0.67, 0.23, 5.9, 4.4, -1}, {0.19, 0.89, 1.9, 3.8, 1},
	}
	terms := make([]string, 0, len(params))
	for _, p := range params {
		terms = append(terms, fmt.Sprintf("%+.0f*95*exp(-(pow(X-(32+34*sin(%.2f*T+%.1f)),2)+pow(Y-(18+20*sin(%.2f*T+%.1f)),2))/90)",
			p[4], p[0], p[2], p[1], p[3]))
	}
	return "128+" + strings.Join(terms, "+")
}

func runFFmpeg(t *testing.T, ffmpeg string, args ...string) {
	t.Helper()
	full := append([]string{"-v", "error", "-nostdin", "-y"}, args...)
	if out, err := exec.Command(ffmpeg, full...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// makeMaster 生成 seconds 秒的母带（640×360、25fps、每秒一个关键帧）。
func makeMaster(t *testing.T, ffmpeg, dir string, seconds int) string {
	out := filepath.Join(dir, "master.mkv")
	src := fmt.Sprintf("nullsrc=s=64x36:r=%d,format=gray,geq=lum='%s',scale=640:360:flags=bicubic,format=yuv420p", fixtureFPS, blobExpression())
	runFFmpeg(t, ffmpeg, "-f", "lavfi", "-i", src, "-t", fmt.Sprint(seconds),
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "14", "-g", "25", out)
	return out
}

// span 是母带上的一段 [fromMS, toMS)；bars=true 表示一段静止的 SMPTE 彩条（长度 toMS-fromMS）。
type span struct {
	fromMS, toMS int64
	bars         bool
}

// makeCut 把若干段按顺序拼接并缩放到 size，返回文件路径与总时长。
func makeCut(t *testing.T, ffmpeg, dir, name, size string, spans []span) (string, int64) {
	out := filepath.Join(dir, name+".mp4")
	master := filepath.Join(dir, "master.mkv")
	args := []string{"-i", master}
	var graph []string
	var labels string
	total, inputs := int64(0), 1
	for i, s := range spans {
		label := fmt.Sprintf("[p%d]", i)
		if s.bars {
			args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("smptehdbars=s=640x360:r=%d:d=%.3f", fixtureFPS, float64(s.toMS-s.fromMS)/1000))
			graph = append(graph, fmt.Sprintf("[%d:v]format=yuv420p,setsar=1%s", inputs, label))
			inputs++
		} else {
			graph = append(graph, fmt.Sprintf("[0:v]trim=start=%.3f:end=%.3f,setpts=PTS-STARTPTS,setsar=1%s", float64(s.fromMS)/1000, float64(s.toMS)/1000, label))
		}
		labels += label
		total += s.toMS - s.fromMS
	}
	graph = append(graph, fmt.Sprintf("%sconcat=n=%d:v=1:a=0,scale=%s:flags=bicubic,format=yuv420p[out]", labels, len(spans), size))
	args = append(args, "-filter_complex", strings.Join(graph, ";"), "-map", "[out]",
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "20", "-g", "25", out)
	runFFmpeg(t, ffmpeg, args...)
	return out, total
}

func ffmpegSource(t *testing.T, ffmpeg, path string, id uint, duration int64) Source {
	t.Helper()
	seq, err := ReadHashSequence(context.Background(), ffmpeg, path, 0, 0, 250)
	if err != nil {
		t.Fatal(err)
	}
	return Source{ID: id, DurationMS: duration, Hashes: seq, Frames: NewFFmpegGrayReader(ffmpeg, path, 32)}
}

func TestFFmpegFixtures(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	dir := t.TempDir()
	master := makeMaster(t, ffmpeg, dir, 150)
	ctx := context.Background()
	const frame = 1000 / fixtureFPS

	t.Run("readers", func(t *testing.T) {
		full, err := ReadHashSequence(ctx, ffmpeg, master, 0, 20000, 250)
		if err != nil || full.StartMS != 0 || full.StepMS != 250 || len(full.Hashes) != 80 {
			t.Fatalf("full: %d hashes, %+v, err=%v", len(full.Hashes), full.StepMS, err)
		}
		part, err := ReadHashSequence(ctx, ffmpeg, master, 10000, 5000, 250)
		if err != nil || part.StartMS != 10000 || len(part.Hashes) != 20 {
			t.Fatalf("part: %d hashes start %d err=%v", len(part.Hashes), part.StartMS, err)
		}
		for i, hash := range part.Hashes {
			if d := hamming(hash, full.Hashes[40+i]); d > 2 {
				t.Errorf("part[%d] differs from full[%d] by %d bits", i, 40+i, d)
			}
		}
		frames, err := NewFFmpegGrayReader(ffmpeg, master, 32)(ctx, 2000, 1000)
		if err != nil || len(frames) != fixtureFPS {
			t.Fatalf("gray frames: %d err=%v", len(frames), err)
		}
		for k, f := range frames {
			if f.PTSMS != 2000+int64(k*frame) || len(f.Pixels) != 32*32 {
				t.Fatalf("frame %d: pts %d len %d", k, f.PTSMS, len(f.Pixels))
			}
		}
	})
	t.Run("reader errors", func(t *testing.T) { testFFmpegReaderErrors(t, ffmpeg, dir, master) })
	t.Run("ntsc pts", func(t *testing.T) { testFFmpegNTSCPTS(t, ffmpeg, dir) })
	t.Run("intro", func(t *testing.T) { testFFmpegIntro(t, ffmpeg, dir) })
	t.Run("align", func(t *testing.T) { testFFmpegAlign(t, ffmpeg, dir) })
}

func testFFmpegReaderErrors(t *testing.T, ffmpeg, dir, master string) {
	ctx := context.Background()
	missing := filepath.Join(dir, "no such dir", "missing.mp4")
	_, err := ReadHashSequence(ctx, ffmpeg, missing, 0, 0, 250)
	if err == nil || strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), "missing.mp4") {
		t.Fatalf("missing file error must exist and hide the path: %v", err)
	}
	if _, err := NewFFmpegGrayReader(ffmpeg, missing, 32)(ctx, 0, 1000); err == nil || strings.Contains(err.Error(), dir) {
		t.Fatalf("gray reader missing file error: %v", err)
	}
	if _, err := NewFFmpegGrayReader(ffmpeg, master, 32)(ctx, 0, maxGrayWindowMS+1); err == nil {
		t.Fatal("expected window bound error")
	}
	if _, err := ReadHashSequence(ctx, ffmpeg, master, 0, 0, 0); err == nil {
		t.Fatal("expected step error")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ReadHashSequence(cancelled, ffmpeg, master, 0, 0, 250); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
}

// 23.976fps 的帧时间不是整毫秒：PTS 必须是逐帧真实时间（四舍五入到毫秒），不是按固定间隔推算。
func testFFmpegNTSCPTS(t *testing.T, ffmpeg, dir string) {
	clip := filepath.Join(dir, "ntsc.mp4")
	runFFmpeg(t, ffmpeg, "-f", "lavfi", "-i", "testsrc2=s=160x90:r=24000/1001", "-t", "6", "-c:v", "libx264", "-preset", "ultrafast", clip)
	frames, err := NewFFmpegGrayReader(ffmpeg, clip, 16)(context.Background(), 3000, 1000)
	if err != nil || len(frames) < 23 || len(frames) > 25 {
		t.Fatalf("frames=%d err=%v", len(frames), err)
	}
	for _, f := range frames {
		k := (f.PTSMS*24000 + 500500) / 1001000 // 最近的帧号
		want := (k*1001000 + 12000) / 24000
		if d := f.PTSMS - want; d < -1 || d > 1 || f.PTSMS < 3000 || f.PTSMS >= 4000 {
			t.Fatalf("pts %d is not a 23.976fps frame time (nearest %d)", f.PTSMS, want)
		}
	}
}

// 三集不同分辨率，各有不同长度的冷开场，后接同一段 12 秒片头。
func testFFmpegIntro(t *testing.T, ffmpeg, dir string) {
	intro := span{fromMS: 0, toMS: 12000}
	episodes := []struct {
		size       string
		spans      []span
		introStart int64
	}{
		{"640:360", []span{{fromMS: 20000, toMS: 40000}, intro, {fromMS: 40000, toMS: 60000}}, 20000},
		{"320:180", []span{{fromMS: 60000, toMS: 67480}, intro, {fromMS: 67480, toMS: 95000}}, 7480},
		{"480:270", []span{{fromMS: 95000, toMS: 128200}, intro, {fromMS: 128200, toMS: 140000}}, 33200},
	}
	var sources []Source
	for i, ep := range episodes {
		path, duration := makeCut(t, ffmpeg, dir, fmt.Sprintf("ep%d", i+1), ep.size, ep.spans)
		sources = append(sources, ffmpegSource(t, ffmpeg, path, uint(i+1), duration))
	}
	results, err := DetectIntros(context.Background(), sources, IntroOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i, result := range results {
		t.Logf("episode %d: %+v (truth %d–%d)", i+1, result, episodes[i].introStart, episodes[i].introStart+12000)
		if result.Status != IntroDetected {
			t.Fatalf("episode %d not detected: %+v", i+1, result)
		}
		within(t, "intro start", result.StartMS, episodes[i].introStart, 1000/fixtureFPS)
		within(t, "intro end", result.EndMS, episodes[i].introStart+12000, 1000/fixtureFPS)
	}
}

// 长版 320×180；HD 640×360 删去长版 [20s,30s)，在 32s 处插入 6 秒长版没有的内容；
// 两侧都带一段 4 秒静止彩条（锚点作废，靠外扩与合并跨过）。
func testFFmpegAlign(t *testing.T, ffmpeg, dir string) {
	bars := span{fromMS: 0, toMS: 4000, bars: true}
	longPath, longDuration := makeCut(t, ffmpeg, dir, "long", "320:180",
		[]span{{fromMS: 0, toMS: 44000}, bars, {fromMS: 44000, toMS: 54000}})
	hdPath, hdDuration := makeCut(t, ffmpeg, dir, "hd", "640:360",
		[]span{{fromMS: 0, toMS: 20000}, {fromMS: 30000, toMS: 42000}, {fromMS: 100000, toMS: 106000},
			{fromMS: 42000, toMS: 44000}, bars, {fromMS: 44000, toMS: 54000}})
	long := ffmpegSource(t, ffmpeg, longPath, 1, longDuration)
	hd := ffmpegSource(t, ffmpeg, hdPath, 2, hdDuration)
	got, err := AlignSegments(context.Background(), long, hd, AlignOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []wantSegment{{0, 20000, 0, ""}, {20000, 32000, 30000, ""}, {38000, 54000, 42000, ""}}
	for _, segment := range got {
		t.Logf("segment %+v", segment)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d segments, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		within(t, "HDStart", g.HDStartMS, w.hdStart, 1000/fixtureFPS)
		within(t, "HDEnd", g.HDEndMS, w.hdEnd, 1000/fixtureFPS)
		within(t, "LongStart", g.LongStartMS, w.longStart, 1000/fixtureFPS)
		if g.Status != SegmentMatched || g.LongEndMS-g.LongStartMS != g.HDEndMS-g.HDStartMS {
			t.Errorf("segment %d: %+v", i, g)
		}
	}
}
