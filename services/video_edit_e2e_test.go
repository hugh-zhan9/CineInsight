package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

// 真实 ffmpeg 的端到端夹具（缺 ffmpeg/ffprobe 时跳过）。画面用 testsrc2（随时间变化），
// 两条音轨是不同频率的正弦（jpn/eng），一条内嵌 srt 字幕（chi）。

func requireEditFFmpeg(t *testing.T) (string, string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg 不可用")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe 不可用")
	}
	return ffmpeg, ffprobe
}

type editFixture struct {
	width, height int
	seconds       float64
	gop           int
	silentAudio   bool
	noSubtitle    bool
	offsetSeconds float64
	negate        bool
	singleAudio   bool
	fonts         []string
}

func runFixtureCommand(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s 失败: %v\n%s", filepath.Base(name), err, out)
	}
	return string(out)
}

// makeEditFixture 生成一个 mkv 夹具。offsetSeconds>0 时画面取 testsrc2 从该秒开始的内容（模拟高清版截取）。
func makeEditFixture(t *testing.T, ffmpeg, path string, f editFixture) {
	t.Helper()
	gop := f.gop
	if gop == 0 {
		gop = 25
	}
	total := f.seconds + f.offsetSeconds
	duration := strconv.FormatFloat(f.seconds, 'f', 3, 64)
	args := []string{"-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=s=" + strconv.Itoa(f.width) + "x" + strconv.Itoa(f.height) + ":r=25:d=" + strconv.FormatFloat(total, 'f', 3, 64)}
	audio := []string{"sine=frequency=440:sample_rate=48000:duration=" + duration, "sine=frequency=880:sample_rate=48000:duration=" + duration}
	if f.silentAudio {
		audio = []string{"anullsrc=r=48000:cl=mono", "anullsrc=r=48000:cl=mono"}
	}
	if f.singleAudio {
		audio = audio[:1]
	}
	for _, source := range audio {
		args = append(args, "-f", "lavfi", "-t", duration, "-i", source)
	}
	maps := []string{"-map", "0:v", "-map", "1:a"}
	if !f.singleAudio {
		maps = append(maps, "-map", "2:a")
	}
	if !f.noSubtitle {
		subtitle := filepath.Join(t.TempDir(), "embedded.srt")
		writeEditTestFile(t, subtitle, "1\n00:00:00,200 --> 00:00:00,900\n内嵌字幕\n")
		args = append(args, "-i", subtitle)
		maps = append(maps, "-map", strconv.Itoa(len(audio)+1)+":s")
	}
	args = append(args, maps...)
	filters := []string{}
	if f.offsetSeconds > 0 {
		filters = append(filters, "trim=start="+strconv.FormatFloat(f.offsetSeconds, 'f', 3, 64), "setpts=PTS-STARTPTS")
	}
	if f.negate {
		filters = append(filters, "negate")
	}
	if len(filters) > 0 {
		args = append(args, "-vf", strings.Join(filters, ","))
	}
	args = append(args, "-t", duration, "-c:v", "libx264", "-preset", "ultrafast", "-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop),
		"-sc_threshold", "0", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ac", "2",
		"-metadata:s:a:0", "language=jpn")
	if !f.singleAudio {
		args = append(args, "-metadata:s:a:1", "language=eng")
	}
	if !f.noSubtitle {
		args = append(args, "-c:s", "srt", "-metadata:s:s:0", "language=chi")
	}
	for index, font := range f.fonts {
		args = append(args, "-attach", font, "-metadata:s:t:"+strconv.Itoa(index), "mimetype=application/x-truetype-font")
	}
	args = append(args, path)
	runFixtureCommand(t, ffmpeg, args...)
}

func writeEditTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sha256OfFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func insertEditVideo(t *testing.T, path string) models.Video {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: filepath.Base(path), Path: path, Directory: filepath.Dir(path), Size: info.Size()}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	return video
}

func newRealEditService(t *testing.T) *VideoEditService {
	t.Helper()
	setupVideoServiceTestDB(t)
	service := NewVideoEditService(NewMediaProbeService(), NewMediaWorkSlot(), t.TempDir())
	service.diskFree = func(string) (uint64, error) { return 1 << 40, nil }
	t.Cleanup(service.StopAndWait)
	return service
}

// queueAllAcknowledged 预检→确认全部警告→排队，并断言没有错误。
func queueAllAcknowledged(t *testing.T, service *VideoEditService, projectID uint) *EditQueueResult {
	t.Helper()
	ctx := context.Background()
	view, err := service.GetProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	pre, err := service.PreflightProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pre.Errors) > 0 {
		raw, _ := json.MarshalIndent(pre.Errors, "", " ")
		t.Fatalf("预检不应有错误: %s", raw)
	}
	keys := []string{}
	for _, warning := range pre.Warnings {
		keys = append(keys, warning.Key)
	}
	result, err := service.QueueProject(ctx, EditQueueRequest{ProjectID: projectID, ExpectedRevision: view.Revision, AcknowledgedWarnings: keys})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Queued {
		raw, _ := json.MarshalIndent(result, "", " ")
		t.Fatalf("应当排队成功: %s", raw)
	}
	return result
}

func waitEditProject(t *testing.T, service *VideoEditService, projectID uint, timeout time.Duration, statuses ...string) *EditProjectView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		view, err := service.GetProject(context.Background(), projectID)
		if err == nil && oneOf(view.Status, statuses...) {
			return view
		}
		if time.Now().After(deadline) {
			raw, _ := json.MarshalIndent(view, "", " ")
			t.Fatalf("项目没有在期限内进入 %v: %s", statuses, raw)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type probedOutput struct {
	durationMS int64
	width      int
	height     int
	audio      []string
	subtitles  int
}

func probeOutputFile(t *testing.T, ffprobe, path string) probedOutput {
	t.Helper()
	raw := runFixtureCommand(t, ffprobe, "-v", "error", "-show_streams", "-show_format", "-of", "json", path)
	probe, err := parseEditProbe([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	out := probedOutput{durationMS: probe.DurationMS, width: probe.Video.Width, height: probe.Video.Height, subtitles: len(probe.Subtitles)}
	for _, stream := range probe.Audio {
		out.audio = append(out.audio, stream.Language)
	}
	return out
}

// grayFrame 抽 at 秒处的一帧并缩成 32×32 灰度。
func grayFrame(t *testing.T, ffmpeg, path string, at float64) []byte {
	t.Helper()
	out, err := exec.Command(ffmpeg, "-v", "error", "-ss", strconv.FormatFloat(at, 'f', 3, 64), "-i", path, "-frames:v", "1",
		"-vf", "scale=32:32:flags=area,format=gray", "-f", "rawvideo", "pipe:1").Output()
	if err != nil || len(out) != 32*32 {
		t.Fatalf("抽帧失败 @%.3f: %v (%d 字节)", at, err, len(out))
	}
	return out
}

func meanAbsDiff(a, b []byte) float64 {
	total := 0
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		total += d
	}
	return float64(total) / float64(len(a))
}

var meanVolumePattern = regexp.MustCompile(`mean_volume:\s*(-?[0-9.]+|-inf) dB`)

// meanVolume 返回 [start, start+dur) 内第一条音轨的平均音量（dB）。
func meanVolume(t *testing.T, ffmpeg, path string, start, dur float64) float64 {
	t.Helper()
	out, _ := exec.Command(ffmpeg, "-v", "info", "-ss", strconv.FormatFloat(start, 'f', 3, 64), "-t", strconv.FormatFloat(dur, 'f', 3, 64),
		"-i", path, "-map", "0:a:0", "-af", "volumedetect", "-f", "null", "-").CombinedOutput()
	match := meanVolumePattern.FindStringSubmatch(string(out))
	if match == nil {
		t.Fatalf("读不到音量: %s", out)
	}
	if match[1] == "-inf" {
		return -200
	}
	value, _ := strconv.ParseFloat(match[1], 64)
	return value
}

func outputPathOf(t *testing.T, view *EditProjectView, seq int) (models.Video, string) {
	t.Helper()
	for _, item := range view.Items {
		if item.Seq == seq {
			if item.OutputVideoID == nil {
				t.Fatalf("第 %d 项没有成品: %+v", seq, item)
			}
			var video models.Video
			if err := database.DB.First(&video, *item.OutputVideoID).Error; err != nil {
				t.Fatal(err)
			}
			return video, video.Path
		}
	}
	t.Fatalf("没有第 %d 项", seq)
	return models.Video{}, ""
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}
