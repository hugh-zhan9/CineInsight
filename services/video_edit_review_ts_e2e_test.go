package services

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"video-master/models"
)

// 评审 2：mpegts 的 start_time 非零（这里 1.4 秒），-read_intervals 与 pts_time 是流的绝对时间戳，
// 而 ffmpeg -ss（在 -i 之前）相对 start_time。预检报告的实际切点必须就是成品真正用到的切点，
// 旁挂字幕按它平移。
func TestVideoEditFastCutHonoursStartTime(t *testing.T) {
	ffmpeg, ffprobe := requireEditFFmpeg(t)
	service := newRealEditService(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "直播录像.ts")
	makeEditFixture(t, ffmpeg, source, editFixture{width: 320, height: 180, seconds: 12, gop: 50, noSubtitle: true})
	start, err := strconv.ParseFloat(strings.TrimSpace(runFixtureCommand(t, ffprobe, "-v", "error", "-show_entries", "format=start_time", "-of", "csv=p=0", source)), 64)
	if err != nil || start < 0.5 {
		t.Skipf("夹具 start_time 不是非零（%v %v），无法复现", start, err)
	}
	// 真值：相对 start_time 不晚于 6.3 秒的最后一个关键帧。
	want := int64(-1)
	for _, line := range strings.Fields(runFixtureCommand(t, ffprobe, "-v", "error", "-select_streams", "v:0", "-skip_frame", "nokey",
		"-show_entries", "frame=pts_time", "-of", "csv=p=0", source)) {
		if value, err := strconv.ParseFloat(strings.Trim(line, ","), 64); err == nil && value-start <= 6.3 {
			want = int64(math.Ceil((value - start) * 1000))
		}
	}
	writeEditTestFile(t, filepath.Join(dir, "直播录像.srt"), "1\n00:00:05,000 --> 00:00:05,500\n删去\n\n2\n00:00:06,500 --> 00:00:07,000\n保留\n")
	video := insertEditVideo(t, source)
	view, err := service.CreateProject(context.Background(), EditProjectCreateRequest{Kind: models.VideoEditKindTrimIntro, VideoIDs: []uint{video.ID}})
	if err != nil {
		t.Fatal(err)
	}
	view = setTrimRecipe(t, service, view, models.VideoEditModeFast,
		EditTrimItem{VideoID: video.ID, RemoveStartMS: 2000, RemoveEndMS: 6300, Origin: EditOriginManual, Confirmed: true})
	pre, err := service.PreflightProject(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pre.Fast.CutPoints) != 1 || pre.Fast.CutPoints[0].ActualMS-want > 1 || want-pre.Fast.CutPoints[0].ActualMS > 1 {
		t.Fatalf("预检报告的实际切点应为相对 start_time 的 %d ms: %+v", want, pre.Fast)
	}
	actual := pre.Fast.CutPoints[0].ActualMS
	queueAllAcknowledged(t, service, view.ID)
	done := waitEditProject(t, service, view.ID, 3*time.Minute, models.VideoEditStatusCompleted, models.VideoEditStatusFailed)
	if done.Status != models.VideoEditStatusCompleted {
		t.Fatalf("应完成: %+v", done.Items)
	}
	_, output := outputPathOf(t, done, 1)
	// 按解码顺序的帧序号比较（mpegts 上按时间 -ss 抽帧本身就不准）：成品里第一个不早于 2 秒的帧必须紧接在
	// 2 秒处（切点之后不能有空档），且就是原片里实际切点处的那个关键帧。
	outPTS, srcPTS := videoFramePTS(t, ffprobe, output, 0), videoFramePTS(t, ffprobe, source, start)
	kOut, kSrc := firstFrameAtOrAfter(outPTS, 2.0-0.001), firstFrameAtOrAfter(srcPTS, float64(actual)/1000-0.001)
	if kOut < 0 || kSrc < 52 || outPTS[kOut] > 2.0+0.045 {
		t.Fatalf("切点后的画面应紧接在 2 秒处: kOut=%d kSrc=%d pts=%v", kOut, kSrc, outPTS[max(kOut, 0)])
	}
	got := grayFrameAtIndex(t, ffmpeg, output, kOut+2)
	if used, removed := meanAbsDiff(got, grayFrameAtIndex(t, ffmpeg, source, kSrc+2)), meanAbsDiff(got, grayFrameAtIndex(t, ffmpeg, source, kSrc-48)); used > 2 || used >= removed {
		t.Fatalf("成品切点后的画面应来自原片 %d ms 处（差 %.2f / 对照 %.2f）", actual, used, removed)
	}
	srt := readFileString(t, strings.TrimSuffix(output, filepath.Ext(output))+".srt")
	if line := formatEditSRTTime(2000+6500-actual) + " --> " + formatEditSRTTime(2000+7000-actual) + "\n保留"; !strings.Contains(srt, line) || strings.Contains(srt, "删去") {
		t.Fatalf("旁挂字幕应按真实切点平移（期望 %q）:\n%s", line, srt)
	}
}

// videoFramePTS 列出第一条视频流每帧的显示时间（减去容器 start_time，与 -ss 同一时基）。
func videoFramePTS(t *testing.T, ffprobe, path string, start float64) []float64 {
	t.Helper()
	values := []float64{}
	for _, line := range strings.Fields(runFixtureCommand(t, ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "frame=pts_time", "-of", "csv=p=0", path)) {
		if value, err := strconv.ParseFloat(strings.Trim(line, ","), 64); err == nil {
			values = append(values, value-start)
		}
	}
	return values
}

func firstFrameAtOrAfter(pts []float64, at float64) int {
	for index, value := range pts {
		if value >= at {
			return index
		}
	}
	return -1
}

// grayFrameAtIndex 按解码顺序取第 n 帧（不定位，从头解码）并缩成 32×32 灰度。
func grayFrameAtIndex(t *testing.T, ffmpeg, path string, n int) []byte {
	t.Helper()
	out, err := exec.Command(ffmpeg, "-v", "error", "-i", path, "-vf", fmt.Sprintf("select=eq(n\\,%d),scale=32:32:flags=area,format=gray", n),
		"-frames:v", "1", "-f", "rawvideo", "pipe:1").Output()
	if err != nil || len(out) != 32*32 {
		t.Fatalf("取第 %d 帧失败: %v (%d 字节)", n, err, len(out))
	}
	return out
}
