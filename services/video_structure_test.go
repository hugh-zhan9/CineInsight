package services

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func grayStructure(value byte) []byte {
	data := make([]byte, structureFrameBytes)
	for i := range data {
		data[i] = value
	}
	return data
}
func structureTestFixture(t *testing.T) (*VideoStructureService, models.Video) {
	t.Helper()
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "fixture.mp4")
	if err := os.WriteFile(path, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "fixture", Path: path, Directory: root, Duration: 240}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	service := NewVideoStructureService(nil, NewMediaWorkSlot())
	service.runtimeAvailable = func() bool { return true }
	service.extract = func(_ context.Context, _ string, start, end int64) ([]structureFrame, error) {
		var frames []structureFrame
		for pts := start; pts < end; pts += 500 {
			frames = append(frames, structureFrame{PTS: pts, Gray: grayStructure(100)})
		}
		return frames, nil
	}
	if err := service.Observe(context.Background(), video.ID); err != nil {
		t.Fatal(err)
	}
	return service, video
}
func structureState(t *testing.T, id uint) models.VideoStructureState {
	t.Helper()
	var state models.VideoStructureState
	if err := database.DB.Where("video_id = ?", id).First(&state).Error; err != nil {
		t.Fatal(err)
	}
	return state
}
func TestVideoStructureCheckpointRecoverAndPublish(t *testing.T) {
	service, video := structureTestFixture(t)
	ctx := context.Background()
	state := structureState(t, video.ID)
	if err := service.ProcessWindow(ctx, state.ID); err != nil {
		t.Fatal(err)
	}
	checkpoint := structureState(t, video.ID)
	if checkpoint.CheckpointMS != 120000 || len(checkpoint.PreviousSample) != structureFrameBytes {
		t.Fatalf("checkpoint: %+v", checkpoint)
	}
	view, err := service.Get(ctx, video.ID)
	if err != nil || len(view.Segments) != 0 {
		t.Fatalf("partial result exposed: %+v %v", view, err)
	}
	if err := database.DB.Model(&checkpoint).Updates(map[string]interface{}{"status": "waiting_idle", "run_token": "retired"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	recovered := structureState(t, video.ID)
	if recovered.Status != "pending" || recovered.RunToken != "" || recovered.CheckpointMS != 120000 || recovered.Revision <= checkpoint.Revision {
		t.Fatalf("recovery: %+v", recovered)
	}
	for i := 0; i < 20 && structureState(t, video.ID).Status != "ready"; i++ {
		if err := service.ProcessWindow(ctx, state.ID); err != nil {
			t.Fatal(err)
		}
	}
	view, err = service.Get(ctx, video.ID)
	if err != nil || view.SourceStatus != "ready" || len(view.Segments) != 2 || view.Segments[0].StartMS != 0 || view.Segments[1].EndMS != 240000 {
		t.Fatalf("published: %+v %v", view, err)
	}
}
func TestVideoStructureABARejectsOldWindow(t *testing.T) {
	service, video := structureTestFixture(t)
	ctx := context.Background()
	initial := structureState(t, video.ID)
	info, _ := os.Stat(video.Path)
	normal := service.extract
	service.extract = func(ctx context.Context, path string, start, end int64) ([]structureFrame, error) {
		frames, err := normal(ctx, path, start, end)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte("change"), 0600); err != nil {
			return nil, err
		}
		if err := os.Chtimes(path, info.ModTime().Add(time.Second), info.ModTime().Add(time.Second)); err != nil {
			return nil, err
		}
		if err := service.Observe(ctx, video.ID); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte("source"), 0600); err != nil {
			return nil, err
		}
		if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
			return nil, err
		}
		if err := service.Observe(ctx, video.ID); err != nil {
			return nil, err
		}
		// A new run may be claimed after source invalidation while the retired
		// extractor still returns; the status alone is no longer sufficient.
		if _, err := service.claim(ctx, initial.ID); err != nil {
			return nil, err
		}
		return frames, nil
	}
	if err := service.ProcessWindow(ctx, initial.ID); !errors.Is(err, ErrStructureObsolete) {
		t.Fatalf("expected obsolete: %v", err)
	}
	state := structureState(t, video.ID)
	if state.Generation != initial.Generation+2 || state.CheckpointMS != 0 || state.PublishedGeneration != nil {
		t.Fatalf("old result survived ABA: %+v", state)
	}
	var count int64
	database.DB.Model(&models.VideoStructureSegment{}).Where("state_id = ?", state.ID).Count(&count)
	if count != 0 {
		t.Fatal("losing transaction kept staged rows")
	}
}
func TestVideoStructureCancellationPreservesCommittedWindow(t *testing.T) {
	service, video := structureTestFixture(t)
	state := structureState(t, video.ID)
	ctx := context.Background()
	if err := service.ProcessWindow(ctx, state.ID); err != nil {
		t.Fatal(err)
	}
	service.extract = func(context.Context, string, int64, int64) ([]structureFrame, error) { return nil, context.Canceled }
	if err := service.ProcessWindow(ctx, state.ID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	state = structureState(t, video.ID)
	if state.Status != "waiting_idle" || state.CheckpointMS != 120000 || state.RunToken != "" {
		t.Fatalf("cancel: %+v", state)
	}
}
func TestVideoStructureAlgorithmShortChangeAndMaximum(t *testing.T) {
	state := models.VideoStructureState{DurationMS: 12000}
	result, err := analyzeStructureWindow(state, []structureFrame{{PTS: 0, Gray: grayStructure(0)}, {PTS: 6000, Gray: grayStructure(255)}}, 12000)
	if err != nil || len(result.Segments) != 2 || result.Segments[0].EndMS != 6000 || result.Segments[0].Basis != "change" {
		t.Fatalf("split: %+v %v", result, err)
	}
	state.DurationMS = 4000
	result, err = analyzeStructureWindow(state, []structureFrame{{PTS: 0, Gray: grayStructure(100)}}, 4000)
	if err != nil || len(result.Segments) != 1 || result.Segments[0].EndMS != 4000 {
		t.Fatal("short source")
	}
}
func TestVideoStructureRealFramePTS(t *testing.T) {
	binary, err := findThumbnailFFmpeg()
	if err != nil {
		t.Skip("FFmpeg is unavailable")
	}
	path := filepath.Join(t.TempDir(), "pts.mp4")
	cmd := exec.Command(binary, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=12", "-t", "4", "-c:v", "mpeg4", "-threads", "2", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	frames, err := extractStructureFrames(context.Background(), path, 1500, 3500)
	if err != nil || len(frames) == 0 {
		t.Fatalf("extract: %v frames=%d", err, len(frames))
	}
	for _, frame := range frames {
		if frame.PTS < 1500 || frame.PTS >= 3500 || len(frame.Gray) != structureFrameBytes {
			t.Fatalf("bad actual timestamp: %d", frame.PTS)
		}
	}
}

func TestVideoStructureMergesShortTail(t *testing.T) {
	state := models.VideoStructureState{DurationMS: 12000}
	result, err := analyzeStructureWindow(state, []structureFrame{{PTS: 0, Gray: grayStructure(0)}, {PTS: 11000, Gray: grayStructure(255)}}, 12000)
	if err != nil || len(result.Segments) != 1 || result.Segments[0].EndMS != 12000 {
		t.Fatalf("short tail: %+v %v", result, err)
	}
}
func TestVideoStructureMissingRuntimeRecoversWithoutRetryLoop(t *testing.T) {
	service, video := structureTestFixture(t)
	available := false
	service.runtimeAvailable = func() bool { return available }
	state := structureState(t, video.ID)
	if err := service.ProcessWindow(context.Background(), state.ID); !errors.Is(err, ErrStructureRuntimeUnavailable) {
		t.Fatal(err)
	}
	state = structureState(t, video.ID)
	if state.Status != "unavailable" {
		t.Fatalf("wrong missing dependency state: %+v", state)
	}
	if err := service.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if structureState(t, video.ID).Status != "unavailable" {
		t.Fatal("missing dependency retried")
	}
	available = true
	if err := service.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if structureState(t, video.ID).Status != "pending" {
		t.Fatal("restored dependency not queued")
	}
}
func TestVideoStructureSparseFramesUseKnownPTS(t *testing.T) {
	service, video := structureTestFixture(t)
	database.DB.Model(&video).Update("duration", 12)
	service.extract = func(_ context.Context, _ string, start, end int64) ([]structureFrame, error) {
		if start == 0 {
			return []structureFrame{{PTS: 0, Gray: grayStructure(100)}}, nil
		}
		return nil, nil
	}
	state := structureState(t, video.ID)
	for i := 0; i < 8 && structureState(t, video.ID).Status != "ready"; i++ {
		if err := service.ProcessWindow(context.Background(), state.ID); err != nil {
			t.Fatal(err)
		}
	}
	view, err := service.Get(context.Background(), video.ID)
	if err != nil || len(view.Segments) != 1 || view.Segments[0].RepresentativeMS != 0 {
		t.Fatalf("sparse source: %+v %v", view, err)
	}
}

func TestVideoStructureRealSparseHeldFrame(t *testing.T) {
	binary, err := findThumbnailFFmpeg()
	if err != nil {
		t.Skip("FFmpeg unavailable")
	}
	service, video := structureTestFixture(t)
	command := exec.Command(binary, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=blue:size=160x90:rate=1/10", "-frames:v", "1", "-c:v", "mpeg4", "-fps_mode", "passthrough", "-threads", "2", video.Path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("held-frame fixture: %v %s", err, output)
	}
	service.probe = NewMediaProbeService()
	service.extract = extractStructureFrames
	if err := service.Observe(context.Background(), video.ID); err != nil {
		t.Fatal(err)
	}
	state := structureState(t, video.ID)
	for i := 0; i < 8 && structureState(t, video.ID).Status != "ready"; i++ {
		if err := service.ProcessWindow(context.Background(), state.ID); err != nil {
			t.Fatal(err)
		}
	}
	view, err := service.Get(context.Background(), video.ID)
	if err != nil || view.Status != "ready" || len(view.Segments) != 1 || view.Segments[0].RepresentativeMS != 0 {
		t.Fatalf("held-frame result: %+v %v", view, err)
	}
}

// Moving a same-size udta atom ahead of trak makes the real demuxer encounter the
// cover first without touching any absolute media chunk offsets.
func structureCoverFirstMP4(t *testing.T, data []byte) []byte {
	t.Helper()
	for offset := 0; offset+8 <= len(data); {
		size := int(binary.BigEndian.Uint32(data[offset:]))
		if size < 8 || offset+size > len(data) {
			t.Fatal("invalid fixture atom")
		}
		if string(data[offset+4:offset+8]) == "moov" {
			payload := data[offset+8 : offset+size]
			var cover, rest []byte
			for cursor := 0; cursor+8 <= len(payload); {
				length := int(binary.BigEndian.Uint32(payload[cursor:]))
				if length < 8 || cursor+length > len(payload) {
					t.Fatal("invalid moov fixture")
				}
				chunk := payload[cursor : cursor+length]
				if string(chunk[4:8]) == "udta" {
					cover = append(cover, chunk...)
				} else {
					rest = append(rest, chunk...)
				}
				cursor += length
			}
			if len(cover) == 0 {
				t.Fatal("fixture did not contain cover metadata")
			}
			copy(data[offset+8:offset+size], append(cover, rest...))
			return data
		}
		offset += size
	}
	t.Fatal("fixture missing moov")
	return nil
}
func TestVideoStructureIgnoresFirstAttachedPicture(t *testing.T) {
	ffmpeg, err := findThumbnailFFmpeg()
	if err != nil {
		t.Skip("FFmpeg unavailable")
	}
	ffprobe, err := findFFProbeBinary()
	if err != nil {
		t.Skip("FFprobe unavailable")
	}
	root := t.TempDir()
	base := filepath.Join(root, "base.mp4")
	cover := filepath.Join(root, "cover.jpg")
	path := filepath.Join(root, "attached.mp4")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(ffmpeg, append([]string{"-hide_banner", "-loglevel", "error"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture: %v %s", err, out)
		}
	}
	run("-f", "lavfi", "-i", "testsrc2=size=160x90:rate=12", "-t", "3", "-c:v", "mpeg4", "-threads", "2", base)
	run("-f", "lavfi", "-i", "color=red:size=160x90", "-frames:v", "1", "-threads", "2", cover)
	run("-i", base, "-i", cover, "-map", "1:v", "-map", "0:v", "-c", "copy", "-disposition:v:0", "attached_pic", path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, structureCoverFirstMP4(t, data), 0600); err != nil {
		t.Fatal(err)
	}
	metadata, err := exec.Command(ffprobe, "-v", "error", "-show_streams", "-of", "json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Streams []struct {
			Disposition struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(metadata, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Streams) < 2 || payload.Streams[0].Disposition.AttachedPic != 1 {
		t.Fatalf("cover is not first: %s", metadata)
	}
	frames, err := extractStructureFrames(context.Background(), path, 0, 2500)
	if err != nil || len(frames) < 3 {
		t.Fatalf("selected cover instead of video: frames=%d err=%v", len(frames), err)
	}
}

func TestVideoStructureUsesEmbeddedBoundaryWithoutInventingTitle(t *testing.T) {
	state := models.VideoStructureState{DurationMS: 20000}
	result, err := analyzeStructureWindow(state, []structureFrame{{PTS: 0, Gray: grayStructure(100)}, {PTS: 8000, Gray: grayStructure(100)}}, 20000, []int64{7900})
	if err != nil || len(result.Segments) != 2 || result.Segments[0].EndMS != 8000 || result.Segments[0].Basis != "embedded" {
		t.Fatalf("embedded reference: %+v %v", result, err)
	}
}

func TestVideoStructureRecoverCrossesBatchBoundary(t *testing.T) {
	service, _ := structureTestFixture(t)
	var videos []models.Video
	for i := 0; i < 205; i++ {
		videos = append(videos, models.Video{Name: "changed", Path: fmt.Sprintf("/structure-recovery-%d.mp4", i)})
	}
	if err := database.DB.CreateInBatches(&videos, 100).Error; err != nil {
		t.Fatal(err)
	}
	published := uint64(1)
	var states []models.VideoStructureState
	for _, video := range videos {
		states = append(states, models.VideoStructureState{VideoID: video.ID, AlgorithmVersion: structureAlgorithmVersion, Revision: 1, Generation: 2, Status: "source_changed", PublishedGeneration: &published, CheckpointMS: 1000, PreviousSample: grayStructure(2)})
	}
	if err := database.DB.CreateInBatches(&states, 100).Error; err != nil {
		t.Fatal(err)
	}
	var segments []models.VideoStructureSegment
	for _, state := range states {
		for gen := uint64(1); gen <= 2; gen++ {
			segments = append(segments, models.VideoStructureSegment{StateID: state.ID, Generation: gen, Position: 0, StartMS: 0, EndMS: 5000, RepresentativeMS: 1000, Basis: "interval"})
		}
	}
	if err := database.DB.CreateInBatches(&segments, 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, original := range []models.VideoStructureState{states[0], states[199], states[204]} {
		got := structureState(t, original.VideoID)
		if got.Generation != 3 || got.Status != "pending" || got.CheckpointMS != 0 || len(got.PreviousSample) != 0 {
			t.Fatalf("incomplete recovery: %+v", got)
		}
	}
	var count int64
	if err := database.DB.Model(&models.VideoStructureSegment{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 205 {
		t.Fatalf("publication or staging boundary lost: %d", count)
	}
}

func TestVideoStructureGradientAndFlashStayBounded(t *testing.T) {
	var gradient, flash []structureFrame
	for at := int64(0); at < 12000; at += 500 {
		gradient = append(gradient, structureFrame{PTS: at, Gray: grayStructure(byte(at * 255 / 12000))})
		value := byte(0)
		if at == 6000 {
			value = 255
		}
		flash = append(flash, structureFrame{PTS: at, Gray: grayStructure(value)})
	}
	state := models.VideoStructureState{DurationMS: 12000}
	result, err := analyzeStructureWindow(state, gradient, 12000)
	if err != nil || len(result.Segments) != 1 || result.Segments[0].Basis != "interval" {
		t.Fatalf("gradient invented cuts: %+v %v", result, err)
	}
	result, err = analyzeStructureWindow(state, flash, 12000)
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range result.Segments {
		if segment.EndMS-segment.StartMS < 5000 {
			t.Fatalf("flash produced a tiny segment: %+v", segment)
		}
	}
	if _, black := structureFrameQuality(grayStructure(0)); !black {
		t.Fatal("black frame not recognized")
	}
	if _, black := structureFrameQuality(grayStructure(100)); black {
		t.Fatal("uniform gray mistaken for black")
	}
}
func TestVideoStructureCompactsToFiveHundredSegments(t *testing.T) {
	service, video := structureTestFixture(t)
	state := structureState(t, video.ID)
	const duration = int64(600 * 5000)
	if err := database.DB.Model(&state).Updates(map[string]any{"duration_ms": duration, "checkpoint_ms": duration}).Error; err != nil {
		t.Fatal(err)
	}
	var segments []models.VideoStructureSegment
	for i := 0; i < 600; i++ {
		start := int64(i) * 5000
		segments = append(segments, models.VideoStructureSegment{StateID: state.ID, Generation: state.Generation, Position: i, StartMS: start, EndMS: start + 5000, RepresentativeMS: start, Basis: "change", Score: float64(i%7) / 10})
	}
	if err := database.DB.CreateInBatches(&segments, 100).Error; err != nil {
		t.Fatal(err)
	}
	claimed, err := service.claim(context.Background(), state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.compact(context.Background(), &claimed); err != nil {
		t.Fatal(err)
	}
	var got []models.VideoStructureSegment
	if err = database.DB.Where("state_id = ? AND generation = ?", state.ID, state.Generation).Order("position").Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 500 {
		t.Fatalf("public segment budget: %d", len(got))
	}
	for i := range got {
		got[i].RepresentativeReady = true
	}
	if !validStructureSegments(got, duration) {
		t.Fatal("compaction lost coverage or representative bounds")
	}
}
func TestVideoStructureRealVariableFrameRateUsesSourcePTS(t *testing.T) {
	binary, err := findThumbnailFFmpeg()
	if err != nil {
		t.Skip("FFmpeg unavailable")
	}
	probe, err := findFFProbeBinary()
	if err != nil {
		t.Skip("FFprobe unavailable")
	}
	path := filepath.Join(t.TempDir(), "variable.mp4")
	command := exec.Command(binary, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=12", "-t", "4", "-vf", "select='if(lt(t,2),not(mod(n,2)),not(mod(n,5)))'", "-fps_mode", "vfr", "-c:v", "mpeg4", "-threads", "2", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("VFR fixture: %v %s", err, output)
	}
	output, err := exec.Command(probe, "-v", "error", "-select_streams", "v:0", "-show_entries", "frame=best_effort_timestamp_time", "-of", "json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Frames []struct {
			Time string `json:"best_effort_timestamp_time"`
		} `json:"frames"`
	}
	if err = json.Unmarshal(output, &metadata); err != nil {
		t.Fatal(err)
	}
	known := map[int64]bool{}
	for _, frame := range metadata.Frames {
		seconds, err := strconv.ParseFloat(frame.Time, 64)
		if err != nil {
			t.Fatal(err)
		}
		known[int64(math.Round(seconds*1000))] = true
	}
	frames, err := extractStructureFrames(context.Background(), path, 1500, 4000)
	if err != nil || len(frames) < 3 {
		t.Fatalf("VFR extraction: %v %d", err, len(frames))
	}
	varied := false
	for i, frame := range frames {
		if !known[frame.PTS] {
			t.Fatalf("invented source PTS %d", frame.PTS)
		}
		if i > 1 && frames[i].PTS-frames[i-1].PTS != frames[1].PTS-frames[0].PTS {
			varied = true
		}
	}
	if !varied {
		t.Fatal("fixture did not exercise variable source PTS")
	}
}

func TestVideoStructurePublishesBlackSource(t *testing.T) {
	service, video := structureTestFixture(t)
	if err := database.DB.Model(&video).Update("duration", 12).Error; err != nil {
		t.Fatal(err)
	}
	service.extract = func(_ context.Context, _ string, start, end int64) ([]structureFrame, error) {
		var frames []structureFrame
		for at := start; at < end; at += 500 {
			frames = append(frames, structureFrame{PTS: at, Gray: grayStructure(0)})
		}
		return frames, nil
	}
	state := structureState(t, video.ID)
	for i := 0; i < 8 && structureState(t, video.ID).Status != "ready"; i++ {
		if err := service.ProcessWindow(context.Background(), state.ID); err != nil {
			t.Fatal(err)
		}
	}
	view, err := service.Get(context.Background(), video.ID)
	if err != nil || view.Status != "ready" || len(view.Segments) != 1 || view.Segments[0].RepresentativeMS != 6000 {
		t.Fatalf("black source not represented honestly: %+v %v", view, err)
	}
}
func TestVideoStructureLongUniformTimelineHasBoundedCoverage(t *testing.T) {
	duration := int64(26 * time.Hour / time.Millisecond)
	state := models.VideoStructureState{DurationMS: duration}
	var segments []models.VideoStructureSegment
	for start := int64(0); start < duration; start += structureWindowMS {
		end := start + structureWindowMS
		if end > duration {
			end = duration
		}
		result, err := analyzeStructureWindow(state, []structureFrame{{PTS: start, Gray: grayStructure(100)}}, end)
		if err != nil {
			t.Fatal(err)
		}
		segments = append(segments, result.Segments...)
		state.CheckpointMS = end
		state.PendingStartMS = result.PendingStartMS
		state.PendingFramePTS = result.PendingFramePTS
		state.PreviousSamplePTS = result.PreviousSamplePTS
		state.PreviousSample = result.PreviousSample
	}
	for i := range segments {
		segments[i].Position = i
		segments[i].RepresentativeReady = true
		if segments[i].Basis != "interval" {
			t.Fatal("uniform source presented as a detected scene")
		}
	}
	if !validStructureSegments(segments, duration) {
		t.Fatalf("long timeline coverage/budget invalid: %d", len(segments))
	}
}
