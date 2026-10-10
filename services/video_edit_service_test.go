package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 服务层单测用进程内假 ffmpeg/ffprobe：来源探测返回登记的 JSON；片段命令写出文件并累计 -t 时长；
// 拼接命令把「成品探测结果」写进 final.part，校验阶段读回它。

type fakeEditMedia struct {
	mu         sync.Mutex
	probes     map[string]string
	segmentMS  map[string]int64
	block      chan struct{}
	started    chan struct{}
	failSource string
	runs       int
}

func fakeEditProbeJSON(width, height int, durationMS int64, languages []string, subtitles int) string {
	streams := []map[string]any{{"index": 0, "codec_type": "video", "codec_name": "h264", "profile": "High", "width": width, "height": height,
		"pix_fmt": "yuv420p", "r_frame_rate": "25/1", "avg_frame_rate": "25/1", "time_base": "1/1000", "bit_rate": "4000000"}}
	for i, language := range languages {
		streams = append(streams, map[string]any{"index": 1 + i, "codec_type": "audio", "codec_name": "aac", "sample_rate": "48000",
			"channels": 2, "channel_layout": "stereo", "tags": map[string]any{"language": language}})
	}
	for i := 0; i < subtitles; i++ {
		streams = append(streams, map[string]any{"index": 1 + len(languages) + i, "codec_type": "subtitle", "codec_name": "subrip",
			"tags": map[string]any{"language": "chi"}})
	}
	raw, _ := json.Marshal(map[string]any{"streams": streams,
		"format": map[string]any{"format_name": "matroska,webm", "duration": strconv.FormatFloat(float64(durationMS)/1000, 'f', 3, 64)}})
	return string(raw)
}

func (f *fakeEditMedia) output(ctx context.Context, name string, args []string) (string, string, error) {
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-skip_frame") {
		return "0.000000,\n", "", nil
	}
	path := args[len(args)-1]
	if strings.HasSuffix(path, "final.part") {
		raw, err := os.ReadFile(path)
		return string(raw), "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if raw, ok := f.probes[path]; ok {
		return raw, "", nil
	}
	return "", "No such file", errors.New("ffprobe: exit status 1")
}

func (f *fakeEditMedia) progress(ctx context.Context, name string, args []string, onProgress func(int64)) (string, error) {
	f.mu.Lock()
	f.runs++
	block, started, failSource := f.block, f.started, f.failSource
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return "killed", ctx.Err()
		}
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if failSource != "" && strings.Contains(strings.Join(args, "\x00"), failSource) {
		return "Error while encoding " + failSource, errors.New("ffmpeg: exit status 1")
	}
	out := args[len(args)-1]
	workdir := filepath.Dir(out)
	if strings.HasSuffix(out, "final.part") {
		f.mu.Lock()
		total := f.segmentMS[workdir]
		f.mu.Unlock()
		audio := strings.Count(strings.Join(args, " "), "-map 0:a:")
		subs := strings.Count(strings.Join(args, " "), "-map 0:s:")
		langs := make([]string, audio)
		return "", os.WriteFile(out, []byte(fakeEditProbeJSON(320, 180, total, langs, subs)), 0o644)
	}
	duration := int64(0)
	for i := len(args) - 1; i > 0; i-- {
		if args[i-1] == "-t" {
			seconds, _ := strconv.ParseFloat(args[i], 64)
			duration = int64(seconds*1000 + 0.5)
			break
		}
	}
	f.mu.Lock()
	f.segmentMS[workdir] += duration
	f.mu.Unlock()
	if onProgress != nil {
		onProgress(duration)
	}
	return "", os.WriteFile(out, []byte("segment"), 0o644)
}

func newFakeEditService(t *testing.T) (*VideoEditService, *fakeEditMedia) {
	t.Helper()
	setupVideoServiceTestDB(t)
	media := &fakeEditMedia{probes: map[string]string{}, segmentMS: map[string]int64{}}
	service := NewVideoEditService(nil, NewMediaWorkSlot(), t.TempDir())
	service.runOutput = media.output
	service.runProgress = media.progress
	service.findFFmpeg = func() (string, error) { return "ffmpeg", nil }
	service.findFFprobe = func() (string, error) { return "ffprobe", nil }
	service.listEncoders = func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{"h264_videotoolbox": true, "hevc_videotoolbox": true, "libx264": true, "libx265": true, "aac": true}, nil
	}
	service.diskFree = func(string) (uint64, error) { return 1 << 40, nil }
	t.Cleanup(service.StopAndWait)
	return service, media
}

// addFakeSource 建一个来源文件与库记录，并登记它的探测结果（默认 320×180、jpn+eng 两条音轨、1 条字幕）。
func (f *fakeEditMedia) addFakeSource(t *testing.T, dir, name string, durationMS int64) models.Video {
	t.Helper()
	path := filepath.Join(dir, name)
	writeEditTestFile(t, path, "source:"+name)
	f.mu.Lock()
	f.probes[path] = fakeEditProbeJSON(320, 180, durationMS, []string{"jpn", "eng"}, 1)
	f.mu.Unlock()
	return insertEditVideo(t, path)
}

func (f *fakeEditMedia) setProbe(path, raw string) {
	f.mu.Lock()
	f.probes[path] = raw
	f.mu.Unlock()
}

func mustEditProject(t *testing.T, service *VideoEditService, kind string, ids ...uint) *EditProjectView {
	t.Helper()
	view, err := service.CreateProject(context.Background(), EditProjectCreateRequest{Kind: kind, VideoIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func mustUpdateRecipe(t *testing.T, service *VideoEditService, view *EditProjectView, mutate func(*EditRecipe)) *EditProjectView {
	t.Helper()
	recipe := view.Recipe
	mutate(&recipe)
	updated, err := service.UpdateRecipe(context.Background(), EditRecipeUpdateRequest{ProjectID: view.ID, ExpectedRevision: view.Revision, Recipe: recipe})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

// pauseEditWorker 让 worker 不领新项（与维护停机同一开关），用来观察排队状态。
func pauseEditWorker(service *VideoEditService) {
	service.mu.Lock()
	service.stopping = true
	service.mu.Unlock()
}

func editErrorCodeOf(err error) string {
	return VideoEditErrorCode(err)
}

func loadEditItemsForTest(t *testing.T, projectID uint) []models.VideoEditItem {
	t.Helper()
	var items []models.VideoEditItem
	if err := database.DB.Where("project_id = ?", projectID).Order("seq").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	return items
}

func waitForEditCondition(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("等待超时: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

var _ = fmt.Sprintf

type gormDB = gorm.DB

var errTestPublish = errors.New("模拟入库失败")

func containsAbsolutePath(text string) bool {
	return strings.Contains(text, string(os.PathSeparator)+"var"+string(os.PathSeparator)) ||
		strings.Contains(text, "/private/") || strings.Contains(text, "/Users/") || strings.Contains(text, "/tmp/")
}

func strconvFormatUint(value uint64) string { return strconv.FormatUint(value, 10) }
