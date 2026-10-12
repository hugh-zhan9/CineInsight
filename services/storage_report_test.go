package services

import (
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

const gib = int64(1) << 30

func TestStorageReportEmptyLibraryUsesDefaults(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	service := NewStorageReportService()
	service.now = func() time.Time { return now }
	report, err := service.GetStorageReport(StorageReportRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Videos != (StorageTotals{}) || report.Images != (StorageTotals{}) || report.Probe != (StorageProbeCoverage{}) {
		t.Fatalf("空库应全为 0：%#v %#v %#v", report.Videos, report.Images, report.Probe)
	}
	if report.UnwatchedCriteria.MinBytes != 2*gib || report.UnwatchedCriteria.Months != 12 || !report.UnwatchedCriteria.Cutoff.Equal(now.AddDate(-1, 0, 0)) {
		t.Fatalf("默认门槛不对：%#v", report.UnwatchedCriteria)
	}
	if report.Unwatched.Items == nil || len(report.Unwatched.Items) != 0 || report.Transcode.Items == nil || report.Unwatched.HasMore {
		t.Fatalf("空清单应是空数组：%#v %#v", report.Unwatched, report.Transcode)
	}
}

func TestStorageReportAggregatesCodecsUnwatchedAndTranscode(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	ago := func(years, days int) *time.Time { at := now.AddDate(-years, 0, -days); return &at }
	video := func(name string, size int64, duration float64, height int, lastPlayed *time.Time, added time.Time, stale bool) models.Video {
		t.Helper()
		row := models.Video{Name: name, Path: "/lib/" + name, Directory: "/lib", Size: size, Duration: duration,
			Width: height * 16 / 9, Height: height, LastPlayedAt: lastPlayed, CreatedAt: added}
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if stale {
			if err := database.DB.Model(&models.Video{}).Where("id = ?", row.ID).
				Updates(map[string]interface{}{"is_stale": true, "stale_reason": models.StaleReasonOfflineRoot}).Error; err != nil {
				t.Fatal(err)
			}
		}
		return row
	}
	probed := map[uint]bool{}
	probe := func(videoID uint) {
		t.Helper()
		if probed[videoID] {
			return
		}
		probed[videoID] = true
		at := now.AddDate(0, -1, 0)
		if err := database.DB.Create(&models.VideoTechnicalMetadata{VideoID: videoID, ProbedAt: &at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	stream := func(videoID uint, index int, codec string, attachedPic bool) {
		t.Helper()
		probe(videoID)
		if err := database.DB.Create(&models.MediaStream{VideoID: videoID, StreamIndex: index, StreamType: "video", CodecName: codec, IsAttachedPic: attachedPic}).Error; err != nil {
			t.Fatal(err)
		}
	}
	playEvent := func(videoID uint, at time.Time) {
		t.Helper()
		if err := database.DB.Create(&models.PlayEvent{VideoID: videoID, PlayedAt: at, Source: "inline_view"}).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 1080p H.264 4 GiB / 2 小时（约 4.8 Mbps）：两年没播 → 久未观看；码率已低于 HEVC 常用码率 → 不进转码。
	lowRate := video("low-rate.mkv", 4*gib, 7200, 1080, ago(2, 0), *ago(3, 0), false)
	stream(lowRate.ID, 0, "h264", false)
	// 1080p H.264 20 GiB / 2 小时：昨天刚播 → 不算久未观看；转 HEVC 估一半 → 可省 10 GiB。
	// 0 号流是封面图（mjpeg），主视频流取 1 号。
	remux := video("remux.mkv", 20*gib, 7200, 1080, ago(0, 1), *ago(3, 0), false)
	stream(remux.ID, 0, "mjpeg", true)
	stream(remux.ID, 1, "h264", false)
	// 4K HEVC 30 GiB：入库三年多从没播 → 久未观看；HEVC 不进转码。
	never := video("never.mkv", 30*gib, 7200, 2160, nil, *ago(3, 1), false)
	stream(never.ID, 0, "hevc", false)
	// 720p H.264 500 MB / 30 分钟：低于码率下限，不进转码；也没到 2 GiB。
	small := video("small.mp4", 500<<20, 1800, 720, nil, *ago(3, 0), false)
	stream(small.ID, 0, "h264", false)
	// 还没探测的 3 GiB：上个月才入库，不算久未观看；编码记为「未探测」。
	video("unprobed.avi", 3*gib, 0, 0, nil, *ago(0, 30), false)
	// 失效（硬盘拔掉）的 50 GiB：哪里都不计。
	gone := video("gone.mkv", 50*gib, 7200, 1080, nil, *ago(5, 0), true)
	stream(gone.ID, 0, "h264", false)
	// 三年前入库、视频表上没有桌面播放记录，但上周在内嵌播放器里看过（只记在播放账本里）→ 不算久未观看。
	inline := video("inline.mkv", 3*gib, 7200, 1080, nil, *ago(3, 0), false)
	stream(inline.ID, 0, "hevc", false)
	playEvent(inline.ID, now.AddDate(0, 0, -7))
	// 上个月在 Jellyfin 客户端里看过（只更新观看进度时间）→ 不算久未观看。
	progress := video("progress.mkv", 3*gib, 7200, 1080, nil, *ago(3, 0), false)
	stream(progress.ID, 0, "hevc", false)
	if err := database.DB.Model(&models.Video{}).Where("id = ?", progress.ID).Update("watch_progress_updated_at", now.AddDate(0, -1, 0)).Error; err != nil {
		t.Fatal(err)
	}
	// 两年前在内嵌播放器看过 → 久未观看，最后观看时间取账本里那一次。
	oldEvent := video("old-event.mkv", 3*gib, 7200, 1080, nil, *ago(3, 0), false)
	stream(oldEvent.ID, 0, "hevc", false)
	oldWatch := now.AddDate(-2, 0, -10)
	playEvent(oldEvent.ID, oldWatch)
	// 探测成功但没有视频流（纯音频）：算已探测，编码记「无视频流」。
	audio := video("audio.mka", 3*gib, 7200, 0, nil, *ago(0, 30), false)
	probe(audio.ID)

	images := []models.Image{
		{Name: "a.jpg", Path: "/pics/a.jpg", Directory: "/pics", Size: 100},
		{Name: "b.jpg", Path: "/pics/b.jpg", Directory: "/pics", Size: 200},
		{Name: "c.jpg", Path: "/pics/c.jpg", Directory: "/pics", Size: 400, IsStale: true},
	}
	if err := database.DB.Create(&images).Error; err != nil {
		t.Fatal(err)
	}

	service := NewStorageReportService()
	service.now = func() time.Time { return now }
	report, err := service.GetStorageReport(StorageReportRequest{})
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := 4*gib + 20*gib + 30*gib + (500 << 20) + 3*gib + 4*3*gib
	if report.Videos.Count != 9 || report.Videos.Bytes != wantBytes {
		t.Fatalf("视频总量应排除失效项：%#v", report.Videos)
	}
	if report.Images.Count != 2 || report.Images.Bytes != 300 {
		t.Fatalf("图片总量应排除失效项：%#v", report.Images)
	}
	if report.Probe.Probed != 8 || report.Probe.Total != 9 {
		t.Fatalf("探测覆盖率：%#v", report.Probe)
	}
	codecs := map[string]LibraryStatsBucket{}
	for _, bucket := range report.ByCodec {
		codecs[bucket.Label] = bucket
	}
	if len(codecs) != 4 || codecs["H.264"].Count != 3 || codecs["HEVC"].Count != 4 || codecs["未探测"].Bytes != 3*gib || codecs["无视频流"].Count != 1 {
		t.Fatalf("编码分布（封面图流不算主视频流）：%#v", report.ByCodec)
	}
	if report.ByCodec[0].Label != "HEVC" {
		t.Fatalf("编码分布按占用从大到小：%#v", report.ByCodec)
	}

	if report.Unwatched.Count != 3 || report.Unwatched.Bytes != 37*gib {
		t.Fatalf("久未观看（内嵌、Jellyfin 近期看过的不算）：%#v", report.Unwatched)
	}
	got := []uint{report.Unwatched.Items[0].ID, report.Unwatched.Items[1].ID, report.Unwatched.Items[2].ID}
	if got[0] != never.ID || got[1] != lowRate.ID || got[2] != oldEvent.ID {
		t.Fatalf("久未观看按大小从大到小：%v", got)
	}
	if last := report.Unwatched.Items[2].LastWatchedAt; last == nil || !last.Equal(oldWatch) {
		t.Fatalf("最后观看时间应取播放账本里那一次：%v", last)
	}
	if report.Unwatched.Items[0].LastWatchedAt != nil {
		t.Fatalf("从没看过的没有最后观看时间：%v", report.Unwatched.Items[0].LastWatchedAt)
	}
	if report.Transcode.Count != 1 || report.Transcode.Items[0].ID != remux.ID {
		t.Fatalf("转码清单只应有 remux：%#v", report.Transcode)
	}
	item := report.Transcode.Items[0]
	if item.EstimatedSize != 10*gib || item.EstimatedSavings != 10*gib || report.Transcode.Savings != 10*gib || item.Codec != "H.264" {
		t.Fatalf("转码估算：%#v", item)
	}
	if want := 20 * gib * 8 / 7200; item.BitRate != want {
		t.Fatalf("码率按大小与时长算：%d", item.BitRate)
	}

	// 门槛可调，清单可截断。
	report, err = service.GetStorageReport(StorageReportRequest{UnwatchedMonths: 36, UnwatchedMinBytes: 5 * gib, UnwatchedLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if report.Unwatched.Count != 1 || report.Unwatched.Items[0].ID != never.ID || report.Unwatched.HasMore {
		t.Fatalf("36 个月、≥5 GiB 只剩 never：%#v", report.Unwatched)
	}
	report, err = service.GetStorageReport(StorageReportRequest{UnwatchedLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Unwatched.Items) != 1 || !report.Unwatched.HasMore || report.Unwatched.Count != 3 {
		t.Fatalf("截断后计数仍按全部：%#v", report.Unwatched)
	}
	if len(report.Transcode.Items) != 1 || report.Transcode.HasMore {
		t.Fatalf("两个清单各自截断，转码清单不受影响：%#v", report.Transcode)
	}
}

func TestStorageTranscodeEstimateFloorsAtTargetBitRate(t *testing.T) {
	// 1080p 2 小时的下限是 6 Mbps × 7200 s / 8 = 5.4e9 字节：比一半大时按下限估。
	estimate, ok := storageTranscodeEstimate("h264", 8_000_000_000, 7200, 1080)
	if !ok || estimate != 5_400_000_000 {
		t.Fatalf("应按码率下限估：%d %v", estimate, ok)
	}
	for _, tc := range []struct {
		name     string
		codec    string
		size     int64
		duration float64
		height   int
	}{
		{"已是 HEVC", "hevc", 20 * gib, 7200, 1080},
		{"剪辑中间格式", "prores", 20 * gib, 7200, 1080},
		{"时长未知", "h264", 20 * gib, 0, 1080},
		{"分辨率未知", "h264", 20 * gib, 7200, 0},
		{"省不到 100 MB", "h264", 150 << 20, 60, 1080},
	} {
		if _, ok := storageTranscodeEstimate(tc.codec, tc.size, tc.duration, tc.height); ok {
			t.Fatalf("%s 不应进转码清单", tc.name)
		}
	}
}

// 老数据可能有视频流记录却没写 probed_at：有流记录的一定探测成功过，不该被算成「没有技术信息」。
func TestStorageReportCountsStreamsWithoutProbedAtAsProbed(t *testing.T) {
	setupVideoServiceTestDB(t)
	row := models.Video{Name: "legacy.mkv", Path: "/lib/legacy.mkv", Directory: "/lib", Size: gib, Duration: 600, Height: 720}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.MediaStream{VideoID: row.ID, StreamIndex: 0, StreamType: "video", CodecName: "h264"}).Error; err != nil {
		t.Fatal(err)
	}
	report, err := NewStorageReportService().GetStorageReport(StorageReportRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Probe.Probed != 1 || report.Probe.Total != 1 || len(report.ByCodec) != 1 || report.ByCodec[0].Label != "H.264" {
		t.Fatalf("有流记录的算已探测：%#v %#v", report.Probe, report.ByCodec)
	}
}
