package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// fakeDownloadHasher 替身：把预设的指纹写进 video_perceptual_hashes，记下被调用的视频。
type fakeDownloadHasher struct {
	hashes map[uint][3]string
	calls  []uint
	err    error
}

func (f *fakeDownloadHasher) Refresh(_ context.Context, video models.Video) error {
	f.calls = append(f.calls, video.ID)
	if f.err != nil {
		return f.err
	}
	return writeDownloadTestHash(video, f.hashes[video.ID])
}

func writeDownloadTestHash(video models.Video, hash [3]string) error {
	info, err := os.Stat(video.Path)
	if err != nil {
		return err
	}
	row := models.VideoPerceptualHash{VideoID: video.ID, SourceSize: info.Size(), SourceModTimeNS: info.ModTime().UnixNano(),
		HashEarly: hash[0], HashMiddle: hash[1], HashLate: hash[2], ComputedAt: time.Now()}
	return database.DB.Save(&row).Error
}

func downloadTestVideo(t *testing.T, dir, name string, content []byte, width, height int, duration float64) models.Video {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: name, Path: path, Directory: dir, Size: int64(len(content)), Width: width, Height: height, Duration: duration}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	return video
}

var (
	downloadHashA = [3]string{"0f0f0f0f0f0f0f0f", "00ff00ff00ff00ff", "f0f0f0f0f0f0f0f0"}
	downloadHashB = [3]string{"ffffffffffffffff", "0000000000000000", "aaaaaaaaaaaaaaaa"}
)

// 相同文件：大小一致且三段采样哈希一致；大小相同但内容不同的不算；对方文件读不到的跳过。
func TestDownloadDuplicateExactUsesSizeAndSampledHash(t *testing.T) {
	setupVideoServiceTestDB(t)
	library, downloads := t.TempDir(), t.TempDir()
	same := []byte("the very same movie bytes")
	other := []byte("a different movie, same n")
	kept := downloadTestVideo(t, library, "kept.mp4", same, 1920, 1080, 600)
	downloadTestVideo(t, library, "other.mp4", other, 1920, 1080, 600)
	offline := downloadTestVideo(t, library, "offline.mp4", same, 1920, 1080, 600)
	if err := os.Remove(offline.Path); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(downloads, "new.mp4")
	if err := os.WriteFile(newPath, same, 0o644); err != nil {
		t.Fatal(err)
	}
	hasher := &fakeDownloadHasher{}
	result, err := (&DownloadDuplicateChecker{PerceptualHash: hasher}).Check(context.Background(), newPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != DownloadDuplicateFound || len(result.Matches) != 1 || result.Matches[0] != (DownloadDuplicateMatch{VideoID: kept.ID, Kind: DownloadDuplicateExact, Relation: DownloadRelationSame}) {
		t.Fatalf("应只找到内容相同、文件在的那一份: %+v", result)
	}
	if len(hasher.calls) != 0 {
		t.Fatalf("没入库的下载只做精确比对，不算指纹: %v", hasher.calls)
	}

	// 文件已经不在：跳过。
	result, err = (&DownloadDuplicateChecker{}).Check(context.Background(), filepath.Join(downloads, "gone.mp4"), 0)
	if err != nil || result.Status != DownloadDuplicateSkipped {
		t.Fatalf("产物不在应为 skipped: %+v %v", result, err)
	}
}

// 其他版本：新片指纹与片库里指纹相近、时长可比的片；判过「不是同一部」的不再提示；
// 关系按像素数标出更清楚 / 更模糊；没有可用指纹的可见视频计入 unhashed。
func TestDownloadDuplicateVersionsUseFingerprintsAndDismissals(t *testing.T) {
	setupVideoServiceTestDB(t)
	library, downloads := t.TempDir(), t.TempDir()
	fresh := downloadTestVideo(t, downloads, "new-720p.mp4", []byte("new download content!"), 1280, 720, 6000)
	hd := downloadTestVideo(t, library, "hd-1080p.mp4", []byte("library 1080p version...."), 1920, 1080, 6010)
	sd := downloadTestVideo(t, library, "sd-480p.mp4", []byte("library 480p"), 854, 480, 5995)
	dismissed := downloadTestVideo(t, library, "dismissed.mp4", []byte("dismissed version bytes"), 1920, 1080, 6000)
	otherFilm := downloadTestVideo(t, library, "other-film.mp4", []byte("other film"), 1920, 1080, 6000)
	tooLong := downloadTestVideo(t, library, "director-cut.mp4", []byte("longer cut"), 1920, 1080, 7200)
	downloadTestVideo(t, library, "unhashed.mp4", []byte("never hashed"), 1920, 1080, 6000)
	for _, video := range []models.Video{hd, sd, dismissed, tooLong} {
		if err := writeDownloadTestHash(video, downloadHashA); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeDownloadTestHash(otherFilm, downloadHashB); err != nil {
		t.Fatal(err)
	}
	if err := DismissNearDuplicateGroup([]uint{fresh.ID, dismissed.ID}); err != nil {
		t.Fatal(err)
	}
	hasher := &fakeDownloadHasher{hashes: map[uint][3]string{fresh.ID: downloadHashA}}
	result, err := (&DownloadDuplicateChecker{PerceptualHash: hasher}).Check(context.Background(), fresh.Path, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hasher.calls) != 1 || hasher.calls[0] != fresh.ID {
		t.Fatalf("应只给新片算一次指纹: %v", hasher.calls)
	}
	got := map[uint]string{}
	for _, match := range result.Matches {
		if match.Kind != DownloadDuplicateVersion {
			t.Fatalf("都应是其他版本: %+v", match)
		}
		got[match.VideoID] = match.Relation
	}
	want := map[uint]string{hd.ID: DownloadRelationHigher, sd.ID: DownloadRelationLower}
	if result.Status != DownloadDuplicateFound || len(got) != len(want) || got[hd.ID] != want[hd.ID] || got[sd.ID] != want[sd.ID] {
		t.Fatalf("应找到 1080p（更清楚）与 480p（更模糊），排除已忽略、别的片与时长差太多的: %+v", result)
	}
	if result.UnhashedVideos != 1 {
		t.Fatalf("还没算指纹的可见视频应计 1 部，实际 %d", result.UnhashedVideos)
	}

	// 片库里的旧指纹对不上文件此刻的大小与修改时间（文件换过）：不算。
	if err := os.WriteFile(hd.Path, []byte("replaced bytes, same len.."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(hd.Path, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	result, err = (&DownloadDuplicateChecker{PerceptualHash: hasher}).Check(context.Background(), fresh.Path, fresh.ID)
	if err != nil || len(result.Matches) != 1 || result.Matches[0].VideoID != sd.ID {
		t.Fatalf("文件换过的那份不应再算: %+v %v", result, err)
	}

}

// 「其他版本」那一步失败（例如纯音轨文件算不出视频指纹）：已经找到的相同文件照样报告，另记一句没做完的原因
// （2026-10-12 审查 I-4）；结论记下查的是哪一份（片库记录与文件指纹）。
func TestDownloadDuplicateVersionFailureKeepsExactMatches(t *testing.T) {
	setupVideoServiceTestDB(t)
	library, downloads := t.TempDir(), t.TempDir()
	same := []byte("identical bytes")
	kept := downloadTestVideo(t, library, "kept.mp4", same, 1920, 1080, 60)
	fresh := downloadTestVideo(t, downloads, "new.mp4", same, 1920, 1080, 60)
	result, err := (&DownloadDuplicateChecker{PerceptualHash: &fakeDownloadHasher{err: errors.New("/secret/path: ffmpeg 失败")}}).Check(context.Background(), fresh.Path, fresh.ID)
	if err != nil {
		t.Fatalf("其他版本失败不应让整次查重失败: %v", err)
	}
	if result.Status != DownloadDuplicateFound || len(result.Matches) != 1 || result.Matches[0].VideoID != kept.ID || result.VersionError != "计算视频指纹失败" {
		t.Fatalf("相同文件应保留并说明其他版本没做完（不带路径）: %+v", result)
	}
	info, _ := os.Stat(fresh.Path)
	if result.VideoID != fresh.ID || result.SourceSize != info.Size() || result.SourceModTimeNS != info.ModTime().UnixNano() {
		t.Fatalf("应记下查的是哪一份: %+v", result)
	}
}

// 读取视图时按 ID 现查对方：已删除的丢掉；全丢光时状态改回 none。
func TestResolveDownloadDuplicateViewsDropsGoneVideos(t *testing.T) {
	setupVideoServiceTestDB(t)
	dir := t.TempDir()
	kept := downloadTestVideo(t, dir, "kept.mp4", []byte("kept"), 1920, 1080, 60)
	gone := downloadTestVideo(t, dir, "gone.mp4", []byte("gone"), 1280, 720, 60)
	if err := database.DB.Model(&models.Video{}).Where("id = ?", kept.ID).Update("display_title", "保留的片").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&models.Video{}, gone.ID).Error; err != nil {
		t.Fatal(err)
	}
	tasks := []BrowserDownloadTask{
		{ID: "a", duplicateResult: encodeDownloadDuplicateResult(DownloadDuplicateResult{Status: DownloadDuplicateFound, Matches: []DownloadDuplicateMatch{
			{VideoID: kept.ID, Kind: DownloadDuplicateExact, Relation: DownloadRelationSame},
			{VideoID: gone.ID, Kind: DownloadDuplicateVersion, Relation: DownloadRelationLower},
		}})},
		{ID: "b", duplicateResult: encodeDownloadDuplicateResult(DownloadDuplicateResult{Status: DownloadDuplicateFound, Matches: []DownloadDuplicateMatch{
			{VideoID: gone.ID, Kind: DownloadDuplicateExact, Relation: DownloadRelationSame},
		}})},
		{ID: "c"},
	}
	resolveDownloadDuplicateViews(database.DB, tasks)
	first := tasks[0].Duplicate
	if first == nil || first.Status != DownloadDuplicateFound || len(first.Matches) != 1 || first.Matches[0].Title != "保留的片" || first.Matches[0].Width != 1920 || first.Matches[0].Size != 4 {
		t.Fatalf("应只剩保留的那部并带上标题、分辨率与大小: %+v", first)
	}
	if tasks[1].Duplicate == nil || tasks[1].Duplicate.Status != DownloadDuplicateNone || len(tasks[1].Duplicate.Matches) != 0 {
		t.Fatalf("对方都已删除时应显示为没有重复: %+v", tasks[1].Duplicate)
	}
	if tasks[2].Duplicate != nil {
		t.Fatalf("还没查过的任务不应有结果: %+v", tasks[2].Duplicate)
	}
}

func TestDownloadRelation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		w, h  int
		size  int64
		ow    int
		oh    int
		osize int64
		want  string
	}{
		{"对方更清楚", 1280, 720, 100, 1920, 1080, 100, DownloadRelationHigher},
		{"对方更模糊", 1920, 1080, 100, 1280, 720, 100, DownloadRelationLower},
		{"同分辨率对方更大", 1920, 1080, 100, 1920, 1080, 200, DownloadRelationLarger},
		{"同分辨率对方更小", 1920, 1080, 200, 1920, 1080, 100, DownloadRelationSmaller},
		{"几乎一样", 1920, 1080, 100, 1920, 1080, 103, DownloadRelationSimilar},
		{"分辨率未知按大小", 0, 0, 100, 0, 0, 300, DownloadRelationLarger},
	} {
		if got := downloadRelation(tc.w, tc.h, tc.size, tc.ow, tc.oh, tc.osize); got != tc.want {
			t.Fatalf("%s: got %s want %s", tc.name, got, tc.want)
		}
	}
}
