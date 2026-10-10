package services

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"video-master/models"
)

// 真实分析（不替换 readHashes/grayReader/detectIntros/alignSegments）：夹具与 editalign 的
// ffmpeg_fixture_test 同法——64×36 上 7 个沿互不公约正弦轨迹运动的明暗高斯斑（geq）再放大。
// testsrc2 之类的标准源在 9×8 dHash 尺度上几乎处处重复，锚点会被正确地判为有歧义。

const analysisFixtureFPS = 25

type analysisSpan struct {
	fromMS, toMS int64
	bars         bool
}

func analysisBlobExpression() string {
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

func runAnalysisFFmpeg(t *testing.T, ffmpeg string, args ...string) {
	t.Helper()
	full := append([]string{"-v", "error", "-nostdin", "-y"}, args...)
	if out, err := exec.Command(ffmpeg, full...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// makeAnalysisMaster 生成 seconds 秒、640×360、25fps、每秒一个关键帧的母带。
func makeAnalysisMaster(t *testing.T, ffmpeg, dir string, seconds int) string {
	t.Helper()
	out := filepath.Join(dir, "master.mkv")
	src := fmt.Sprintf("nullsrc=s=64x36:r=%d,format=gray,geq=lum='%s',scale=640:360:flags=bicubic,format=yuv420p",
		analysisFixtureFPS, analysisBlobExpression())
	runAnalysisFFmpeg(t, ffmpeg, "-f", "lavfi", "-i", src, "-t", fmt.Sprint(seconds),
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "14", "-g", "25", out)
	return out
}

// makeAnalysisCut 把母带上的若干段（或静止彩条）按顺序拼接并缩放到 size。
func makeAnalysisCut(t *testing.T, ffmpeg, dir, master, name, size string, spans []analysisSpan) string {
	t.Helper()
	out := filepath.Join(dir, name+".mp4")
	args := []string{"-i", master}
	graph, labels, inputs := []string{}, "", 1
	for i, s := range spans {
		label := fmt.Sprintf("[p%d]", i)
		if s.bars {
			args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("smptehdbars=s=640x360:r=%d:d=%.3f", analysisFixtureFPS, float64(s.toMS-s.fromMS)/1000))
			graph = append(graph, fmt.Sprintf("[%d:v]format=yuv420p,setsar=1%s", inputs, label))
			inputs++
		} else {
			graph = append(graph, fmt.Sprintf("[0:v]trim=start=%.3f:end=%.3f,setpts=PTS-STARTPTS,setsar=1%s",
				float64(s.fromMS)/1000, float64(s.toMS)/1000, label))
		}
		labels += label
	}
	graph = append(graph, fmt.Sprintf("%sconcat=n=%d:v=1:a=0,scale=%s:flags=bicubic,format=yuv420p[out]", labels, len(spans), size))
	args = append(args, "-filter_complex", strings.Join(graph, ";"), "-map", "[out]",
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "20", "-g", "25", out)
	runAnalysisFFmpeg(t, ffmpeg, args...)
	return out
}

func requireWithin(t *testing.T, label string, got, want, tolerance int64) {
	t.Helper()
	if diff := got - want; diff > tolerance || diff < -tolerance {
		t.Errorf("%s = %d, want %d ± %d", label, got, want, tolerance)
	}
}

// waitEditAnalysis 等分析回到 draft，并要求这一轮是 completed（失败时把原因打出来）。
func waitEditAnalysis(t *testing.T, service *VideoEditService, projectID uint) *EditProjectView {
	t.Helper()
	view := waitEditProject(t, service, projectID, 45*time.Second, models.VideoEditStatusDraft)
	if view.Analysis == nil || view.Analysis.Status != "completed" {
		t.Fatalf("分析应完成: %+v", view.Analysis)
	}
	return view
}

// Q1：四集共享 12 秒片头（冷开场长度各不相同、分辨率不同）。前三集未确认 → 识别为 detected、
// 区间与真值相差 ≤100 ms、confirmed=false；第四集事先确认的手填区间保持原样。
func TestVideoEditRealIntroAnalysis(t *testing.T) {
	ffmpeg, _ := requireEditFFmpeg(t)
	service := newRealEditService(t)
	dir := t.TempDir()
	master := makeAnalysisMaster(t, ffmpeg, dir, 100)
	intro := analysisSpan{fromMS: 0, toMS: 12000}
	episodes := []struct {
		size       string
		spans      []analysisSpan
		introStart int64
	}{
		{"640:360", []analysisSpan{{fromMS: 20000, toMS: 28000}, intro, {fromMS: 28000, toMS: 40000}}, 8000},
		{"320:180", []analysisSpan{{fromMS: 40000, toMS: 43480}, intro, {fromMS: 43480, toMS: 58000}}, 3480},
		{"480:270", []analysisSpan{{fromMS: 58000, toMS: 71200}, intro, {fromMS: 71200, toMS: 80000}}, 13200},
		{"640:360", []analysisSpan{{fromMS: 80000, toMS: 85000}, intro, {fromMS: 85000, toMS: 95000}}, 5000},
	}
	ids := []uint{}
	for i, episode := range episodes {
		path := makeAnalysisCut(t, ffmpeg, dir, master, fmt.Sprintf("第%d集", i+1), episode.size, episode.spans)
		ids = append(ids, insertEditVideo(t, path).ID)
	}
	view := mustEditProject(t, service, models.VideoEditKindTrimIntro, ids...)
	manual := EditTrimItem{VideoID: ids[3], RemoveStartMS: 1000, RemoveEndMS: 2000, Origin: EditOriginManual, Confirmed: true}
	view = mustUpdateRecipe(t, service, view, func(recipe *EditRecipe) { recipe.TrimIntro.Items[3] = manual })
	if _, err := service.AnalyzeProject(context.Background(), EditAnalyzeRequest{ProjectID: view.ID, ExpectedRevision: view.Revision}); err != nil {
		t.Fatal(err)
	}
	done := waitEditAnalysis(t, service, view.ID)
	items := done.Recipe.TrimIntro.Items
	for i := 0; i < 3; i++ {
		item := items[i]
		t.Logf("episode %d: %+v (truth %d–%d)", i+1, item, episodes[i].introStart, episodes[i].introStart+12000)
		if item.DetectStatus != EditDetectDetected || item.Origin != EditOriginDetected || item.Confirmed || item.Confidence <= 0 {
			t.Fatalf("第 %d 集应识别为未确认的 detected: %+v", i+1, item)
		}
		requireWithin(t, fmt.Sprintf("ep%d remove_start", i+1), item.RemoveStartMS, episodes[i].introStart, 100)
		requireWithin(t, fmt.Sprintf("ep%d remove_end", i+1), item.RemoveEndMS, episodes[i].introStart+12000, 100)
	}
	if items[3] != manual {
		t.Fatalf("已确认项不能被分析覆盖: %+v", items[3])
	}
	if done.Revision != view.Revision+1 || len(done.Analysis.Intros) != 4 || done.Analysis.Intros[3].Applied {
		t.Fatalf("分析结果记录不对: revision=%d analysis=%+v", done.Revision, done.Analysis)
	}
}

// Q2/Q7：320×180 长版 vs 640×360 高清版；高清版删去长版 [12s,18s)、在 22s 处插入 6 秒长版没有的内容，
// 两侧都带 4 秒静止彩条。建议段：三段、区间与真值相差 ≤100 ms、长版/高清时长相等、confirmed=false。
func TestVideoEditRealAlignmentAnalysis(t *testing.T) {
	ffmpeg, _ := requireEditFFmpeg(t)
	service := newRealEditService(t)
	dir := t.TempDir()
	master := makeAnalysisMaster(t, ffmpeg, dir, 100)
	bars := analysisSpan{fromMS: 0, toMS: 4000, bars: true}
	longPath := makeAnalysisCut(t, ffmpeg, dir, master, "长版", "320:180",
		[]analysisSpan{{fromMS: 0, toMS: 30000}, bars, {fromMS: 30000, toMS: 40000}})
	hdPath := makeAnalysisCut(t, ffmpeg, dir, master, "高清", "640:360",
		[]analysisSpan{{fromMS: 0, toMS: 12000}, {fromMS: 18000, toMS: 28000}, {fromMS: 90000, toMS: 96000},
			{fromMS: 28000, toMS: 30000}, bars, {fromMS: 30000, toMS: 40000}})
	long, hd := insertEditVideo(t, longPath), insertEditVideo(t, hdPath)
	view := mustEditProject(t, service, models.VideoEditKindHDReplace, long.ID, hd.ID)
	if _, err := service.AnalyzeProject(context.Background(), EditAnalyzeRequest{ProjectID: view.ID, ExpectedRevision: view.Revision}); err != nil {
		t.Fatal(err)
	}
	done := waitEditAnalysis(t, service, view.ID)
	segments := done.Recipe.HDReplace.Segments
	want := []struct{ longStart, longEnd, hdStart, hdEnd int64 }{
		{0, 12000, 0, 12000}, {18000, 28000, 12000, 22000}, {28000, 44000, 28000, 44000},
	}
	for _, segment := range segments {
		t.Logf("segment %+v", segment)
	}
	if len(segments) != len(want) {
		t.Fatalf("应建议 %d 段，实际 %d: %+v", len(want), len(segments), segments)
	}
	for i, w := range want {
		s := segments[i]
		requireWithin(t, fmt.Sprintf("seg%d long_start", i), s.LongStartMS, w.longStart, 100)
		requireWithin(t, fmt.Sprintf("seg%d long_end", i), s.LongEndMS, w.longEnd, 100)
		requireWithin(t, fmt.Sprintf("seg%d hd_start", i), s.HDStartMS, w.hdStart, 100)
		requireWithin(t, fmt.Sprintf("seg%d hd_end", i), s.HDEndMS, w.hdEnd, 100)
		if s.LongEndMS-s.LongStartMS != s.HDEndMS-s.HDStartMS || s.Confirmed || s.Origin != EditOriginDetected ||
			s.AudioSource != EditAudioSourceLong || s.Status != "matched" {
			t.Errorf("第 %d 段应为等长、未确认、默认长版音频的 matched 段: %+v", i, s)
		}
	}
}
