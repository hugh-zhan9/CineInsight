package services

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

type fakePerceptualFrameRunner struct {
	frames [][]byte
	calls  int
}

type blockingPerceptualFrameRunner struct {
	started chan struct{}
	release chan struct{}
}

func (runner *blockingPerceptualFrameRunner) Frame(ctx context.Context, _ string, _ float64) ([]byte, error) {
	select {
	case <-runner.started:
	default:
		close(runner.started)
	}
	<-ctx.Done()
	<-runner.release
	return nil, ctx.Err()
}

func (runner *fakePerceptualFrameRunner) Frame(_ context.Context, _ string, _ float64) ([]byte, error) {
	frame := runner.frames[runner.calls%len(runner.frames)]
	runner.calls++
	return append([]byte(nil), frame...), nil
}

func gradientPerceptualFrame(reverse bool) []byte {
	frame := make([]byte, 72)
	for row := 0; row < 8; row++ {
		for column := 0; column < 9; column++ {
			value := column * 20
			if reverse {
				value = (8 - column) * 20
			}
			frame[row*9+column] = byte(value)
		}
	}
	return frame
}

func TestDifferenceHashAndDistance(t *testing.T) {
	increasing, err := perceptualDifferenceHash(gradientPerceptualFrame(false))
	if err != nil {
		t.Fatal(err)
	}
	decreasing, err := perceptualDifferenceHash(gradientPerceptualFrame(true))
	if err != nil {
		t.Fatal(err)
	}
	distance, err := perceptualHashDistance(increasing, decreasing)
	if err != nil {
		t.Fatal(err)
	}
	if distance != 64 {
		t.Fatalf("opposite gradients distance=%d", distance)
	}
	if !perceptualRowsMatch(
		models.VideoPerceptualHash{HashEarly: increasing, HashMiddle: increasing, HashLate: increasing},
		models.VideoPerceptualHash{HashEarly: increasing, HashMiddle: increasing, HashLate: increasing},
	) {
		t.Fatal("identical multi-frame hashes must match")
	}
}

func TestPerceptualHashRealTranscodeFixture(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source.avi")
	transcode := filepath.Join(root, "transcode.mp4")
	unrelated := filepath.Join(root, "unrelated.avi")
	run := func(args ...string) {
		t.Helper()
		command := exec.Command(ffmpeg, args...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %v: %v\n%s", args, err, output)
		}
	}
	run("-v", "error", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=8", "-t", "6", "-c:v", "mpeg4", "-q:v", "3", source)
	run("-v", "error", "-i", source, "-vf", "scale=320:180", "-c:v", "mpeg4", "-q:v", "8", transcode)
	run("-v", "error", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=8", "-t", "6", "-c:v", "mpeg4", "-q:v", "3", unrelated)

	runner := ffmpegPerceptualFrameRunner{}
	rowFor := func(path string) models.VideoPerceptualHash {
		t.Helper()
		hashes := make([]string, 0, 3)
		for _, second := range perceptualSampleSeconds(6) {
			frame, err := runner.Frame(context.Background(), path, second)
			if err != nil {
				t.Fatal(err)
			}
			hash, err := perceptualDifferenceHash(frame)
			if err != nil {
				t.Fatal(err)
			}
			hashes = append(hashes, hash)
		}
		return models.VideoPerceptualHash{HashEarly: hashes[0], HashMiddle: hashes[1], HashLate: hashes[2]}
	}
	sourceHash, transcodeHash, unrelatedHash := rowFor(source), rowFor(transcode), rowFor(unrelated)
	if !perceptualRowsMatch(sourceHash, transcodeHash) {
		t.Fatalf("real transcode did not match: source=%+v transcode=%+v", sourceHash, transcodeHash)
	}
	if perceptualRowsMatch(sourceHash, unrelatedHash) {
		t.Fatalf("unrelated fixture matched: source=%+v unrelated=%+v", sourceHash, unrelatedHash)
	}
}

func TestPerceptualHashRefreshPersistsAndSourceChangeInvalidates(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(path, []byte("first"), 0644); err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "movie.mp4", Path: path, Directory: root, Duration: 100}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	runner := &fakePerceptualFrameRunner{frames: [][]byte{gradientPerceptualFrame(false)}}
	service := NewPerceptualHashService()
	service.runner = runner
	if err := service.Refresh(context.Background(), video); err != nil {
		t.Fatal(err)
	}
	if runner.calls != 3 {
		t.Fatalf("frame calls=%d", runner.calls)
	}
	current, err := perceptualHashCurrent(video)
	if err != nil || !current {
		t.Fatalf("fresh hash current=%v err=%v", current, err)
	}
	if err := os.WriteFile(path, []byte("changed-source"), 0644); err != nil {
		t.Fatal(err)
	}
	current, err = perceptualHashCurrent(video)
	if err != nil || current {
		t.Fatalf("changed source current=%v err=%v", current, err)
	}
}

func TestPerceptualHashStopRejectsConcurrentStartAndDoesNotPersistCancellation(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(path, []byte("source"), 0644); err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "movie.mp4", Path: path, Directory: root, Duration: 100}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	runner := &blockingPerceptualFrameRunner{started: make(chan struct{}), release: make(chan struct{})}
	service := NewPerceptualHashService()
	service.runner = runner
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	stopped := make(chan struct{})
	go func() {
		service.StopAndWait()
		close(stopped)
	}()
	for service.Status().Running {
		service.mu.Lock()
		stopping := service.stopping
		service.mu.Unlock()
		if stopping {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := service.Start(context.Background()); err == nil {
		t.Fatal("start must be rejected while StopAndWait is active")
	}
	close(runner.release)
	<-stopped
	var count int64
	if err := database.DB.Model(&models.VideoPerceptualHash{}).Where("video_id = ?", video.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("cancelled extraction persisted %d failure rows", count)
	}
}

func TestCleanupIncludesOnlyCurrentNearDuplicateHashes(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	makeVideo := func(name, content string) models.Video {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		video := models.Video{Name: name, Path: path, Directory: root, Size: int64(len(content)), Duration: 12, Width: 1920, Height: 1080}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		return video
	}
	left := makeVideo("left.mp4", "left-content")
	right := makeVideo("right.mp4", "different-right-content")
	unrelated := makeVideo("unrelated.mp4", "unrelated")
	matchingHash := "0000000000000000"
	farHash := "ffffffffffffffff"
	for _, item := range []struct {
		video models.Video
		hash  string
	}{{left, matchingHash}, {right, "0000000000000001"}, {unrelated, farHash}} {
		info, err := os.Stat(item.video.Path)
		if err != nil {
			t.Fatal(err)
		}
		row := models.VideoPerceptualHash{
			VideoID: item.video.ID, SourceSize: info.Size(), SourceModTimeNS: info.ModTime().UnixNano(),
			HashEarly: item.hash, HashMiddle: item.hash, HashLate: item.hash, ComputedAt: time.Now(),
		}
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	result, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.NearDuplicateGroups) != 1 {
		t.Fatalf("near duplicate groups=%#v", result.NearDuplicateGroups)
	}
	members := append([]models.Video{result.NearDuplicateGroups[0].Original}, result.NearDuplicateGroups[0].Candidates...)
	ids := map[uint]bool{}
	for _, video := range members {
		ids[video.ID] = true
	}
	if !ids[left.ID] || !ids[right.ID] || ids[unrelated.ID] {
		t.Fatalf("near duplicate members=%#v", members)
	}
}

func TestCleanupNearDuplicateGroupsDoNotApplyTransitiveMatches(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	makeVideo := func(name, content, hash string) models.Video {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		video := models.Video{Name: name, Path: path, Directory: root, Size: int64(len(content)), Duration: 12, Width: 1920, Height: 1080}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		row := models.VideoPerceptualHash{
			VideoID: video.ID, SourceSize: info.Size(), SourceModTimeNS: info.ModTime().UnixNano(),
			HashEarly: hash, HashMiddle: hash, HashLate: hash, ComputedAt: time.Now(),
		}
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		return video
	}
	first := makeVideo("first.mp4", "a", "0000000000000000")
	middle := makeVideo("middle.mp4", "middle-source", "000000000000007f")
	last := makeVideo("last.mp4", "last-source-longer", "00000000007f007f")

	result, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.NearDuplicateGroups) != 1 {
		t.Fatalf("near duplicate groups=%#v", result.NearDuplicateGroups)
	}
	seenFirstMiddle := false
	for _, group := range result.NearDuplicateGroups {
		members := append([]models.Video{group.Original}, group.Candidates...)
		ids := make(map[uint]bool)
		for _, video := range members {
			ids[video.ID] = true
		}
		if ids[first.ID] && ids[last.ID] {
			t.Fatalf("transitive non-match was placed in one group: %#v", members)
		}
		seenFirstMiddle = seenFirstMiddle || ids[first.ID] && ids[middle.ID]
	}
	if !seenFirstMiddle {
		t.Fatalf("expected a directly matching pair, got %#v", result.NearDuplicateGroups)
	}
}
