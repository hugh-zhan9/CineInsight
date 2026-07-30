package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

const multiStreamFFProbeFixture = `{
  "streams": [
    {
      "index": 0,
      "codec_name": "mjpeg",
      "codec_long_name": "Motion JPEG",
      "codec_type": "video",
      "width": 600,
      "height": 900,
      "disposition": {"attached_pic": 1}
    },
    {
      "index": 1,
      "codec_name": "hevc",
      "codec_long_name": "H.265 / HEVC",
      "profile": "Main 10",
      "codec_type": "video",
      "width": 3840,
      "height": 2160,
      "pix_fmt": "yuv420p10le",
      "bits_per_raw_sample": "10",
      "color_range": "tv",
      "color_space": "bt2020nc",
      "color_transfer": "smpte2084",
      "color_primaries": "bt2020",
      "avg_frame_rate": "24000/1001",
      "r_frame_rate": "24000/1001",
      "bit_rate": "15000000",
      "disposition": {"default": 1, "attached_pic": 0}
    },
    {
      "index": 2,
      "codec_name": "aac",
      "codec_long_name": "AAC",
      "profile": "LC",
      "codec_type": "audio",
      "sample_rate": "48000",
      "channels": 6,
      "channel_layout": "5.1",
      "bit_rate": "384000",
      "tags": {"language": "jpn", "title": "Japanese"},
      "disposition": {"default": 1}
    },
    {
      "index": 3,
      "codec_name": "opus",
      "codec_type": "audio",
      "sample_rate": "not-a-number",
      "tags": {},
      "disposition": {}
    },
    {
      "index": 4,
      "codec_name": "subrip",
      "codec_long_name": "SubRip subtitle",
      "codec_type": "subtitle",
      "tags": {"language": "eng", "title": "English SDH"},
      "disposition": {"default": 0}
    },
    {"index": 5, "codec_type": "data", "codec_name": "bin_data"}
  ],
  "format": {
    "format_name": "matroska,webm",
    "format_long_name": "Matroska / WebM",
    "duration": "125.500000",
    "bit_rate": "16000000"
  }
}`

func createProbeTestVideo(t *testing.T) models.Video {
	t.Helper()
	path := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(path, []byte("local-video"), 0600); err != nil {
		t.Fatalf("创建探测测试文件失败: %v", err)
	}
	modTime := time.Unix(1_700_000_000, 123_000_000)
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("设置探测测试文件时间失败: %v", err)
	}
	video := models.Video{
		Name:       filepath.Base(path),
		Path:       path,
		Directory:  filepath.Dir(path),
		Size:       1,
		Duration:   1,
		Resolution: "1x1",
		Width:      1,
		Height:     1,
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建探测测试视频失败: %v", err)
	}
	return video
}

func TestMediaProbeRefreshPersistsMultiStreamSnapshot(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProbeTestVideo(t)
	runnerCalls := 0
	svc := newMediaProbeServiceWithRunner(func(ctx context.Context, path string) ([]byte, string, error) {
		runnerCalls++
		if path != video.Path {
			t.Fatalf("runner path=%q want=%q", path, video.Path)
		}
		return []byte(multiStreamFFProbeFixture), "", nil
	})

	if err := svc.Refresh(context.Background(), video.ID); err != nil {
		t.Fatalf("刷新技术快照失败: %v", err)
	}
	if runnerCalls != 1 {
		t.Fatalf("ffprobe 调用次数=%d want=1", runnerCalls)
	}
	var metadata models.VideoTechnicalMetadata
	if err := database.DB.First(&metadata, "video_id = ?", video.ID).Error; err != nil {
		t.Fatalf("读取技术快照失败: %v", err)
	}
	if metadata.FormatName != "matroska,webm" || metadata.FormatLongName != "Matroska / WebM" {
		t.Fatalf("容器信息错误: %#v", metadata)
	}
	if metadata.TotalBitRate == nil || *metadata.TotalBitRate != 16_000_000 {
		t.Fatalf("总码率错误: %#v", metadata.TotalBitRate)
	}
	if metadata.SuccessfulSourceSize == nil || *metadata.SuccessfulSourceSize != int64(len("local-video")) {
		t.Fatalf("成功快照文件大小错误: %#v", metadata.SuccessfulSourceSize)
	}
	if metadata.ProbedAt == nil || metadata.LastAttemptAt == nil || metadata.LastError != "" {
		t.Fatalf("成功状态字段错误: %#v", metadata)
	}

	var streams []models.MediaStream
	if err := database.DB.Where("video_id = ?", video.ID).Order("stream_index ASC").Find(&streams).Error; err != nil {
		t.Fatalf("读取媒体流失败: %v", err)
	}
	if len(streams) != 5 {
		t.Fatalf("应保存 video/audio/subtitle 五条受支持流，实际=%d %#v", len(streams), streams)
	}
	if !streams[0].IsAttachedPic || streams[0].IsHDR != nil {
		t.Fatalf("封面视频流不应猜测 HDR: %#v", streams[0])
	}
	mainVideo := streams[1]
	if mainVideo.StreamType != "video" || mainVideo.Width == nil || *mainVideo.Width != 3840 || mainVideo.Height == nil || *mainVideo.Height != 2160 {
		t.Fatalf("主视频流尺寸错误: %#v", mainVideo)
	}
	if mainVideo.IsHDR == nil || !*mainVideo.IsHDR || mainVideo.BitsPerRawSample == nil || *mainVideo.BitsPerRawSample != 10 {
		t.Fatalf("明确 PQ/位深未正确保存: %#v", mainVideo)
	}
	audio := streams[2]
	if audio.Language != "jpn" || audio.Title != "Japanese" || !audio.IsDefault || audio.SampleRate == nil || *audio.SampleRate != 48000 || audio.Channels == nil || *audio.Channels != 6 {
		t.Fatalf("音轨信息错误: %#v", audio)
	}
	if streams[3].SampleRate != nil {
		t.Fatalf("非法采样率应保存 NULL: %#v", streams[3].SampleRate)
	}
	if streams[4].StreamType != "subtitle" || streams[4].Language != "eng" || streams[4].Title != "English SDH" {
		t.Fatalf("字幕轨信息错误: %#v", streams[4])
	}

	var refreshed models.Video
	if err := database.DB.First(&refreshed, video.ID).Error; err != nil {
		t.Fatalf("读取视频基础元数据失败: %v", err)
	}
	if refreshed.Size != int64(len("local-video")) || refreshed.Duration != 125.5 || refreshed.Resolution != "3840x2160" || refreshed.Width != 3840 || refreshed.Height != 2160 {
		t.Fatalf("视频基础元数据未同步: %#v", refreshed)
	}
}

func TestMediaProbeRefreshSerializesSameVideo(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProbeTestVideo(t)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	svc := newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) {
		current := inFlight.Add(1)
		for {
			previous := maxInFlight.Load()
			if current <= previous || maxInFlight.CompareAndSwap(previous, current) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		inFlight.Add(-1)
		return []byte(multiStreamFFProbeFixture), "", nil
	})
	errorsCh := make(chan error, 2)
	go func() { errorsCh <- svc.Refresh(context.Background(), video.ID) }()
	<-entered
	go func() { errorsCh <- svc.Refresh(context.Background(), video.ID) }()
	overlapped := false
	select {
	case <-entered:
		overlapped = true
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for index := 0; index < 2; index++ {
		if err := <-errorsCh; err != nil {
			t.Fatalf("串行探测失败: %v", err)
		}
	}
	if maxInFlight.Load() != 1 {
		t.Fatalf("同一视频最大并发探测数=%d want=1", maxInFlight.Load())
	}
	if overlapped {
		t.Fatal("同一视频的第二次探测不应与第一次同时进入 runner")
	}
}

func TestMediaProbeFailurePreservesLastSuccessfulSnapshot(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProbeTestVideo(t)
	svc := newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) {
		return []byte(multiStreamFFProbeFixture), "", nil
	})
	if err := svc.Refresh(context.Background(), video.ID); err != nil {
		t.Fatalf("建立初始快照失败: %v", err)
	}

	var before models.VideoTechnicalMetadata
	if err := database.DB.First(&before, "video_id = ?", video.ID).Error; err != nil {
		t.Fatalf("读取初始快照失败: %v", err)
	}
	svc.runner = func(context.Context, string) ([]byte, string, error) {
		return nil, "decoder failed with local file", errors.New("exit status 1")
	}
	if err := svc.Refresh(context.Background(), video.ID); err == nil {
		t.Fatal("ffprobe 失败应返回错误")
	}

	var after models.VideoTechnicalMetadata
	if err := database.DB.First(&after, "video_id = ?", video.ID).Error; err != nil {
		t.Fatalf("读取失败后的快照失败: %v", err)
	}
	if after.ProbedAt == nil || before.ProbedAt == nil || !after.ProbedAt.Equal(*before.ProbedAt) || after.FormatName != before.FormatName {
		t.Fatalf("失败覆盖了最后成功快照: before=%#v after=%#v", before, after)
	}
	if after.LastError == "" || after.LastAttemptAt == nil || !after.LastAttemptAt.After(*before.LastAttemptAt) {
		t.Fatalf("失败尝试状态未更新: before=%#v after=%#v", before, after)
	}
	var streamCount int64
	if err := database.DB.Model(&models.MediaStream{}).Where("video_id = ?", video.ID).Count(&streamCount).Error; err != nil {
		t.Fatalf("统计旧流失败: %v", err)
	}
	if streamCount != 5 {
		t.Fatalf("失败后旧流被改动: count=%d", streamCount)
	}
}

func TestMediaProbeSuccessClearsBaseFieldsMissingFromNewSnapshot(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProbeTestVideo(t)
	svc := newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) {
		return []byte(multiStreamFFProbeFixture), "", nil
	})
	if err := svc.Refresh(context.Background(), video.ID); err != nil {
		t.Fatalf("建立完整技术快照失败: %v", err)
	}
	svc.runner = func(context.Context, string) ([]byte, string, error) {
		return []byte(`{"streams":[{"index":2,"codec_type":"audio","codec_name":"aac"}],"format":{"format_name":"matroska"}}`), "", nil
	}
	if err := svc.Refresh(context.Background(), video.ID); err != nil {
		t.Fatalf("刷新缺字段技术快照失败: %v", err)
	}
	var refreshed models.Video
	if err := database.DB.First(&refreshed, video.ID).Error; err != nil {
		t.Fatalf("读取缺字段刷新后视频失败: %v", err)
	}
	if refreshed.Duration != 0 || refreshed.Resolution != "" || refreshed.Width != 0 || refreshed.Height != 0 {
		t.Fatalf("新快照缺失的基础字段应清空旧值: %#v", refreshed)
	}
	var streams []models.MediaStream
	if err := database.DB.Where("video_id = ?", video.ID).Find(&streams).Error; err != nil {
		t.Fatalf("读取缺字段刷新后的流失败: %v", err)
	}
	if len(streams) != 1 || streams[0].StreamType != "audio" {
		t.Fatalf("新快照应完整替换旧媒体流: %#v", streams)
	}
}

func TestMediaProbeDiscardsResultWhenSourceChangesDuringProbe(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProbeTestVideo(t)
	svc := newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) {
		if err := os.WriteFile(video.Path, []byte("local-video-changed"), 0600); err != nil {
			t.Fatalf("修改探测中源文件失败: %v", err)
		}
		return []byte(multiStreamFFProbeFixture), "", nil
	})

	err := svc.Refresh(context.Background(), video.ID)
	if !errors.Is(err, ErrMediaProbeSourceChanged) {
		t.Fatalf("源文件变化错误=%v want=%v", err, ErrMediaProbeSourceChanged)
	}
	var metadata models.VideoTechnicalMetadata
	if err := database.DB.First(&metadata, "video_id = ?", video.ID).Error; err != nil {
		t.Fatalf("读取失败尝试状态失败: %v", err)
	}
	if metadata.ProbedAt != nil || metadata.SuccessfulSourceSize != nil || metadata.LastError == "" {
		t.Fatalf("源变化不应产生成功快照: %#v", metadata)
	}
	var streamCount int64
	if err := database.DB.Model(&models.MediaStream{}).Where("video_id = ?", video.ID).Count(&streamCount).Error; err != nil {
		t.Fatalf("统计流失败: %v", err)
	}
	if streamCount != 0 {
		t.Fatalf("源变化不应保存流: count=%d", streamCount)
	}
}

func TestMediaProbeCancellationIsRecordedWithoutSnapshot(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProbeTestVideo(t)
	svc := newMediaProbeServiceWithRunner(func(ctx context.Context, _ string) ([]byte, string, error) {
		<-ctx.Done()
		return nil, "", ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := svc.Refresh(ctx, video.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误=%v want=context.Canceled", err)
	}
	var metadata models.VideoTechnicalMetadata
	if err := database.DB.First(&metadata, "video_id = ?", video.ID).Error; err != nil {
		t.Fatalf("读取取消尝试状态失败: %v", err)
	}
	if metadata.ProbedAt != nil || metadata.LastError != "cancelled" {
		t.Fatalf("取消状态错误: %#v", metadata)
	}
}

func TestMediaProbeReportsFailureStatePersistenceError(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := createProbeTestVideo(t)
	svc := newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) {
		sqlDB, err := database.DB.DB()
		if err != nil {
			t.Fatalf("获取底层测试数据库失败: %v", err)
		}
		if err := sqlDB.Close(); err != nil {
			t.Fatalf("关闭底层测试数据库失败: %v", err)
		}
		return nil, "decoder failed", errors.New("exit status 1")
	})
	err := svc.Refresh(context.Background(), video.ID)
	if err == nil || !strings.Contains(err.Error(), "ffprobe failed") || !strings.Contains(err.Error(), "persist media probe failure state") {
		t.Fatalf("应同时返回探测与失败状态持久化错误: %v", err)
	}
}

func TestVideoImportPersistsTechnicalSnapshotWithoutBlockingOnProbeFailure(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	successPath := filepath.Join(root, "success.mkv")
	failurePath := filepath.Join(root, "failure.mkv")
	if err := os.WriteFile(successPath, []byte("success-video"), 0600); err != nil {
		t.Fatalf("创建成功导入文件失败: %v", err)
	}
	if err := os.WriteFile(failurePath, []byte("failure-video"), 0600); err != nil {
		t.Fatalf("创建失败导入文件失败: %v", err)
	}
	probe := newMediaProbeServiceWithRunner(func(_ context.Context, path string) ([]byte, string, error) {
		if path == failurePath {
			return nil, "unsupported", errors.New("exit status 1")
		}
		return []byte(multiStreamFFProbeFixture), "", nil
	})
	svc := NewVideoService(probe)
	success, err := svc.AddVideo(successPath)
	if err != nil {
		t.Fatalf("成功技术探测的导入失败: %v", err)
	}
	if success.Duration != 125.5 || success.Width != 3840 {
		t.Fatalf("导入未返回详细探测后的基础字段: %#v", success)
	}
	var metadata models.VideoTechnicalMetadata
	if err := database.DB.First(&metadata, "video_id = ?", success.ID).Error; err != nil || metadata.ProbedAt == nil {
		t.Fatalf("导入未保存技术快照: %#v err=%v", metadata, err)
	}
	var automaticTagRelations int64
	if err := database.DB.Table("video_tags").
		Joins("JOIN tags ON tags.id = video_tags.tag_id").
		Where("video_tags.video_id = ? AND tags.automatic_kind = ?", success.ID, shortVideoAutomaticTagKind).
		Count(&automaticTagRelations).Error; err != nil {
		t.Fatalf("查询导入后的短视频自动标签关系失败: %v", err)
	}
	if automaticTagRelations != 1 {
		t.Fatalf("导入后的短视频自动标签关系=%d want=1", automaticTagRelations)
	}

	failure, err := svc.AddVideo(failurePath)
	if err != nil {
		t.Fatalf("技术探测失败不应阻止导入: %v", err)
	}
	if failure.ID == 0 {
		t.Fatalf("探测失败仍应返回已导入视频: %#v", failure)
	}
	metadata = models.VideoTechnicalMetadata{}
	if err := database.DB.First(&metadata, "video_id = ?", failure.ID).Error; err != nil {
		t.Fatalf("探测失败应保存尝试状态: %v", err)
	}
	if metadata.ProbedAt != nil || metadata.LastError == "" {
		t.Fatalf("失败导入技术状态错误: %#v", metadata)
	}
}

func TestSyncDoesNotImplicitlyBackfillLegacyCompleteMetadata(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "legacy.mp4")
	if err := os.WriteFile(path, []byte("legacy-video"), 0600); err != nil {
		t.Fatalf("创建旧视频失败: %v", err)
	}
	mustSetFileModTime(t, path, time.Now().Add(-10*time.Minute))
	legacy := models.Video{Name: "legacy.mp4", Path: path, Directory: root, Size: int64(len("legacy-video")), Duration: 60, Resolution: "1920x1080", Width: 1920, Height: 1080}
	if err := database.DB.Create(&legacy).Error; err != nil {
		t.Fatalf("创建旧视频记录失败: %v", err)
	}
	var calls int
	svc := NewVideoService(newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) {
		calls++
		return []byte(multiStreamFFProbeFixture), "", nil
	}))
	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if len(result.Errors) != 0 {
		t.Fatalf("旧片库同步失败: %#v", result.Errors)
	}
	if calls != 0 || result.MetadataRefreshed != 0 {
		t.Fatalf("普通扫描不得隐式全量补旧快照: calls=%d result=%#v", calls, result)
	}
}

func TestSyncRefreshesExistingTechnicalSnapshotWhenFileChanges(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "changed.mp4")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatalf("创建变化视频失败: %v", err)
	}
	mustSetFileModTime(t, path, time.Now().Add(-20*time.Minute))
	video := models.Video{Name: "changed.mp4", Path: path, Directory: root, Size: int64(len("before")), Duration: 60, Resolution: "1920x1080", Width: 1920, Height: 1080}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建变化视频记录失败: %v", err)
	}
	oldSize := video.Size
	oldMTime := int64(1)
	probedAt := time.Now().Add(-time.Hour)
	if err := database.DB.Create(&models.VideoTechnicalMetadata{
		VideoID: video.ID, SuccessfulSourceSize: &oldSize, SuccessfulSourceModTimeNS: &oldMTime, ProbedAt: &probedAt,
	}).Error; err != nil {
		t.Fatalf("创建旧技术快照失败: %v", err)
	}
	if err := os.WriteFile(path, []byte("after-file-change"), 0600); err != nil {
		t.Fatalf("修改视频文件失败: %v", err)
	}
	mustSetFileModTime(t, path, time.Now().Add(-10*time.Minute))
	var calls int
	svc := NewVideoService(newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) {
		calls++
		return []byte(multiStreamFFProbeFixture), "", nil
	}))
	result := svc.SyncScanDirectories([]models.ScanDirectory{{Path: root}})
	if calls != 1 || result.MetadataRefreshed != 1 || len(result.Errors) != 0 {
		t.Fatalf("文件变化应刷新技术快照: calls=%d result=%#v", calls, result)
	}
}
