package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// proxyFFmpegStub 冒充 ffmpeg：记下每次的参数数组，并按需要在输出路径上落一个文件。
// 参数数组是设计 D-003 的可验证面，所以这里逐项断言而不是只看"跑成功了没有"。
type proxyFFmpegStub struct {
	calls    [][]string
	payload  []byte
	duration float64
	// beforeWrite 在写输出之前调用，用来模拟"源文件在编码期间被替换"。
	beforeWrite func(args []string)
	// err 非 nil 时直接失败，不写输出。
	err error
	// stderr 是失败时返回的 stderr 尾部。
	stderr string
	// skipWrite 为真时成功返回但不写输出（模拟产物不可读）。
	skipWrite bool
}

func (s *proxyFFmpegStub) run(ctx context.Context, args []string) (string, error) {
	s.calls = append(s.calls, append([]string(nil), args...))
	if s.beforeWrite != nil {
		s.beforeWrite(args)
	}
	// 真实实现走 exec.CommandContext：ctx 一取消子进程就被杀，ffmpeg 以非零码退出。
	// stub 照同一语义返回错误，取消用例才落在真实分支上而不是"照样跑完"。
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("signal: killed: %w", err)
	}
	if s.err != nil {
		return s.stderr, s.err
	}
	if s.skipWrite {
		return "", nil
	}
	output := args[len(args)-1]
	payload := s.payload
	if payload == nil {
		payload = bytes.Repeat([]byte("p"), 1024)
	}
	if err := os.WriteFile(output, payload, 0o600); err != nil {
		return "", err
	}
	return "", nil
}

func (s *proxyFFmpegStub) lastCall() []string {
	if len(s.calls) == 0 {
		return nil
	}
	return s.calls[len(s.calls)-1]
}

// newProxyTestService 造一个不碰真 ffmpeg 的代理服务。probe 默认不该被调用：
// 需要走探测路径的测试自己替换它。
func newProxyTestService(t *testing.T) (*PlaybackProxyService, *proxyFFmpegStub) {
	t.Helper()
	probe := newMediaProbeServiceWithRunner(func(ctx context.Context, path string) ([]byte, string, error) {
		return nil, "", errors.New("stub probe: ffprobe 不该在这条路径上被调用")
	})
	service := NewPlaybackProxyService(t.TempDir(), probe)
	stub := &proxyFFmpegStub{duration: 10}
	service.runFFmpeg = stub.run
	service.probeDuration = func(ctx context.Context, path string) (float64, error) {
		return stub.duration, nil
	}
	t.Cleanup(service.StopAndWait)
	return service, stub
}

func createProxyTestVideo(t *testing.T, name string, duration float64) models.Video {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	mustCreateFile(t, path)
	video := models.Video{
		Name:      name,
		Path:      path,
		Directory: filepath.Dir(path),
		Size:      1,
		Duration:  duration,
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建代理测试视频失败: %v", err)
	}
	return video
}

// writeProxySnapshot 写一份"最后成功"的技术快照：指纹取当前文件，
// 这样 loadPlaybackProxySnapshot 认它新鲜。audioCodec 为空表示无音频流。
func writeProxySnapshot(t *testing.T, video models.Video, videoCodec, audioCodec string, videoBitRate, totalBitRate int64) {
	t.Helper()
	fingerprint, err := mediaProbeStat(video.Path)
	if err != nil {
		t.Fatalf("读取快照指纹失败: %v", err)
	}
	now := time.Now()
	metadata := models.VideoTechnicalMetadata{
		VideoID:                   video.ID,
		FormatName:                "matroska",
		SuccessfulSourceSize:      &fingerprint.size,
		SuccessfulSourceModTimeNS: &fingerprint.modTimeNS,
		ProbedAt:                  &now,
	}
	if totalBitRate > 0 {
		metadata.TotalBitRate = &totalBitRate
	}
	if err := database.DB.Create(&metadata).Error; err != nil {
		t.Fatalf("写入技术快照失败: %v", err)
	}
	videoStream := models.MediaStream{VideoID: video.ID, StreamIndex: 0, StreamType: "video", CodecName: videoCodec}
	if videoBitRate > 0 {
		videoStream.BitRate = &videoBitRate
	}
	if err := database.DB.Create(&videoStream).Error; err != nil {
		t.Fatalf("写入视频流失败: %v", err)
	}
	if audioCodec == "" {
		return
	}
	audioStream := models.MediaStream{VideoID: video.ID, StreamIndex: 1, StreamType: "audio", CodecName: audioCodec}
	if err := database.DB.Create(&audioStream).Error; err != nil {
		t.Fatalf("写入音频流失败: %v", err)
	}
}

func waitForProxyIdle(t *testing.T, service *PlaybackProxyService) PlaybackProxyStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status := service.Status()
		if !status.Running {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待代理任务结束超时: %#v", service.Status())
	return PlaybackProxyStatus{}
}

func runProxyOnce(t *testing.T, service *PlaybackProxyService, videoID uint) PlaybackProxyItemResult {
	t.Helper()
	if _, err := service.CreatePlaybackProxy(context.Background(), videoID); err != nil {
		t.Fatalf("入队代理任务失败: %v", err)
	}
	status := waitForProxyIdle(t, service)
	if len(status.Results) != 1 {
		t.Fatalf("应当只有一项结果: %#v", status.Results)
	}
	return status.Results[0]
}

func mustLoadProxyRow(t *testing.T, videoID uint) models.VideoPlaybackProxy {
	t.Helper()
	row, err := loadProxyRow(videoID)
	if err != nil {
		t.Fatalf("读取代理记录失败: %v", err)
	}
	if row == nil {
		t.Fatalf("代理记录缺失 video_id=%d", videoID)
	}
	return *row
}

func proxyTempFiles(t *testing.T, service *PlaybackProxyService) []string {
	t.Helper()
	entries, err := os.ReadDir(service.tempDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("读取临时目录失败: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// ===== D-003 策略与参数数组 =====

// H.264 + AAC 走 remux：参数数组逐项钉住，产物从临时目录 rename 到最终路径。
func TestPlaybackProxyRemuxArgsAndPublish(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "remux.mkv", 10)
	writeProxySnapshot(t, video, "h264", "aac", 3_000_000, 3_200_000)

	result := runProxyOnce(t, service, video.ID)
	if result.Code != PlaybackProxyCodeCreated {
		t.Fatalf("应当生成成功: %#v", result)
	}
	if result.Strategy != models.PlaybackProxyStrategyRemux {
		t.Fatalf("H.264 + AAC 应当 remux: %s", result.Strategy)
	}

	args := stub.lastCall()
	if len(args) == 0 {
		t.Fatalf("ffmpeg 没有被调用")
	}
	tempPath := args[len(args)-1]
	if filepath.Dir(tempPath) != service.tempDir() {
		t.Fatalf("输出应先落到临时目录: %s", tempPath)
	}
	want := []string{
		"-v", "error", "-y", "-i", video.Path,
		"-map", "0:0", "-map", "0:1",
		"-c", "copy",
		"-movflags", "+faststart",
		tempPath,
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("remux 参数数组不符\n实际: %#v\n期望: %#v", args, want)
	}

	// 临时文件已被 rename 走，临时目录里不该有残留。
	if names := proxyTempFiles(t, service); len(names) != 0 {
		t.Fatalf("临时目录应为空: %v", names)
	}
	row := mustLoadProxyRow(t, video.ID)
	if row.Status != models.PlaybackProxyStatusReady {
		t.Fatalf("代理状态应为 ready: %s", row.Status)
	}
	finalPath := service.proxyPathForRow(row)
	info, err := os.Stat(finalPath)
	if err != nil {
		t.Fatalf("最终产物缺失: %v", err)
	}
	if row.OutputSize != info.Size() {
		t.Fatalf("产物大小应记进表里: row=%d file=%d", row.OutputSize, info.Size())
	}
	if !strings.HasPrefix(filepath.Base(finalPath), fmt.Sprintf("%d-", video.ID)) {
		t.Fatalf("文件名应以 videoID 开头: %s", filepath.Base(finalPath))
	}
	if filepath.Base(service.Dir()) != "proxies" {
		t.Fatalf("代理目录名应为 proxies: %s", service.Dir())
	}
}

// HEVC 仍走 remux，但要显式打 hvc1 标签，否则 WKWebView 不认。
func TestPlaybackProxyHEVCRemuxTagsHvc1(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "hevc.mkv", 10)
	writeProxySnapshot(t, video, "hevc", "aac", 0, 0)

	result := runProxyOnce(t, service, video.ID)
	if result.Code != PlaybackProxyCodeCreated || result.Strategy != models.PlaybackProxyStrategyRemux {
		t.Fatalf("HEVC + AAC 应当 remux 成功: %#v", result)
	}
	args := stub.lastCall()
	want := []string{
		"-v", "error", "-y", "-i", video.Path,
		"-map", "0:0", "-map", "0:1",
		"-c", "copy",
		"-tag:v", "hvc1",
		"-movflags", "+faststart",
		args[len(args)-1],
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("HEVC remux 参数数组不符\n实际: %#v\n期望: %#v", args, want)
	}
}

// 无音频流也走 remux，且仍带 -map 0:a:0?（可选映射，没有音频不报错）。
func TestPlaybackProxyRemuxWithoutAudio(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "silent.mkv", 10)
	writeProxySnapshot(t, video, "h264", "", 0, 0)

	result := runProxyOnce(t, service, video.ID)
	if result.Strategy != models.PlaybackProxyStrategyRemux {
		t.Fatalf("无音频的 H.264 应当 remux: %#v", result)
	}
	// 无音频：只映射视频那一条，不留空的音频映射。
	if !containsPair(stub.lastCall(), "-map", "0:0") {
		t.Fatalf("应当映射主视频流: %#v", stub.lastCall())
	}
	for _, arg := range stub.lastCall() {
		if strings.HasPrefix(arg, "0:1") {
			t.Fatalf("没有音频流时不该出现音频映射: %#v", stub.lastCall())
		}
	}
}

// 表驱动：策略判定表。remux 的门槛是"首视频流 h264/hevc 且首音频流 aac/mp3 或无音频"。
func TestPlaybackProxyStrategyTable(t *testing.T) {
	cases := []struct {
		name     string
		snapshot playbackProxySnapshot
		want     string
	}{
		{"h264+aac", playbackProxySnapshot{HasVideo: true, VideoCodec: "h264", HasAudio: true, AudioCodec: "aac"}, models.PlaybackProxyStrategyRemux},
		{"h264+mp3", playbackProxySnapshot{HasVideo: true, VideoCodec: "h264", HasAudio: true, AudioCodec: "mp3"}, models.PlaybackProxyStrategyRemux},
		{"hevc+aac", playbackProxySnapshot{HasVideo: true, VideoCodec: "hevc", HasAudio: true, AudioCodec: "aac"}, models.PlaybackProxyStrategyRemux},
		{"h264 无音频", playbackProxySnapshot{HasVideo: true, VideoCodec: "h264"}, models.PlaybackProxyStrategyRemux},
		{"h264+ac3", playbackProxySnapshot{HasVideo: true, VideoCodec: "h264", HasAudio: true, AudioCodec: "ac3"}, models.PlaybackProxyStrategyTranscode},
		{"h264+flac", playbackProxySnapshot{HasVideo: true, VideoCodec: "h264", HasAudio: true, AudioCodec: "flac"}, models.PlaybackProxyStrategyTranscode},
		{"mpeg4+mp3", playbackProxySnapshot{HasVideo: true, VideoCodec: "mpeg4", HasAudio: true, AudioCodec: "mp3"}, models.PlaybackProxyStrategyTranscode},
		{"vp9 无音频", playbackProxySnapshot{HasVideo: true, VideoCodec: "vp9"}, models.PlaybackProxyStrategyTranscode},
		{"没有视频流", playbackProxySnapshot{HasAudio: true, AudioCodec: "aac"}, models.PlaybackProxyStrategyTranscode},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			plan := planPlaybackProxy(testCase.snapshot, "/src.mkv", "/tmp/out.mp4")
			if plan.Strategy != testCase.want {
				t.Fatalf("策略应为 %s，实际 %s", testCase.want, plan.Strategy)
			}
		})
	}
}

// 转码参数数组与码率上限：源码率高于 8 Mbps 时按 8 Mbps 封顶，且永不放大。
func TestPlaybackProxyTranscodeArgsCapBitrate(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "transcode.avi", 10)
	writeProxySnapshot(t, video, "mpeg4", "ac3", 20_000_000, 21_000_000)

	result := runProxyOnce(t, service, video.ID)
	if result.Code != PlaybackProxyCodeCreated || result.Strategy != models.PlaybackProxyStrategyTranscode {
		t.Fatalf("mpeg4 + ac3 应当转码成功: %#v", result)
	}
	args := stub.lastCall()
	want := []string{
		"-v", "error", "-y", "-i", video.Path,
		"-map", "0:0", "-map", "0:1",
		"-c:v", "h264_videotoolbox",
		"-b:v", "8000000",
		"-vf", playbackProxyScaleFilter,
		"-c:a", "aac",
		"-b:a", "160k",
		"-movflags", "+faststart",
		args[len(args)-1],
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("转码参数数组不符\n实际: %#v\n期望: %#v", args, want)
	}
}

// 源码率低于上限时照抄源码率：代理不该比源还大。
func TestPlaybackProxyTranscodeKeepsLowSourceBitrate(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "lowrate.avi", 10)
	writeProxySnapshot(t, video, "mpeg4", "ac3", 1_500_000, 1_600_000)

	if result := runProxyOnce(t, service, video.ID); result.Code != PlaybackProxyCodeCreated {
		t.Fatalf("应当转码成功: %#v", result)
	}
	if !containsPair(stub.lastCall(), "-b:v", "1500000") {
		t.Fatalf("应当沿用源码率: %#v", stub.lastCall())
	}
}

// 视频流没有码率时退到容器总码率；两者都没有就用上限。
func TestPlaybackProxyTargetBitrateFallbacks(t *testing.T) {
	if got := playbackProxyTargetBitrate(playbackProxySnapshot{TotalBitRate: 2_000_000}); got != 2_000_000 {
		t.Fatalf("应退到容器总码率: %d", got)
	}
	if got := playbackProxyTargetBitrate(playbackProxySnapshot{}); got != playbackProxyMaxVideoBitrate {
		t.Fatalf("码率未知时应用上限: %d", got)
	}
}

// ===== 流程步骤 6：编码期间源被替换 =====

// 编码完成后再核对一次指纹（步骤 6）：对不上就丢弃产物，不落最终路径。
func TestPlaybackProxyDiscardsOutputWhenSourceChanged(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "changing.mkv", 10)
	writeProxySnapshot(t, video, "h264", "aac", 0, 0)

	stub.beforeWrite = func([]string) {
		// 模拟源在编码期间被替换：内容与 mtime 都变了。
		if err := os.WriteFile(video.Path, []byte("replaced-source"), 0o600); err != nil {
			t.Fatalf("替换源文件失败: %v", err)
		}
		mustSetFileModTime(t, video.Path, time.Now().Add(2*time.Hour))
	}

	result := runProxyOnce(t, service, video.ID)
	if result.Code != PlaybackProxyCodeSourceChanged {
		t.Fatalf("应当报 source_changed: %#v", result)
	}
	if names := proxyTempFiles(t, service); len(names) != 0 {
		t.Fatalf("临时文件应被清理: %v", names)
	}
	entries, err := os.ReadDir(service.Dir())
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("读取代理目录失败: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Fatalf("代理目录不该有产物: %s", entry.Name())
		}
	}
	row := mustLoadProxyRow(t, video.ID)
	if row.Status != models.PlaybackProxyStatusFailed {
		t.Fatalf("应当留一行 failed 记录: %#v", row)
	}
}

// ===== 失败路径 =====

// 磁盘满：报 disk_full、清理临时文件，且**不触发淘汰**——淘汰只在成功写入之后跑。
func TestPlaybackProxyDiskFullDoesNotEvict(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)

	// 先放一份别人的代理，并把上限压到 1 字节：一旦误触发淘汰，它就会消失。
	victim := createProxyTestVideo(t, "victim.mkv", 10)
	victimPath := seedReadyProxy(t, service, victim, 4096, time.Now().Add(-time.Hour))
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("proxy_cache_limit_bytes", 1).Error; err != nil {
		t.Fatalf("设置上限失败: %v", err)
	}

	video := createProxyTestVideo(t, "full.mkv", 10)
	writeProxySnapshot(t, video, "h264", "aac", 0, 0)
	stub.err = fmt.Errorf("ffmpeg failed: %w", syscall.ENOSPC)

	if _, err := service.CreatePlaybackProxy(context.Background(), video.ID); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	status := waitForProxyIdle(t, service)
	if len(status.Results) != 1 || status.Results[0].Code != PlaybackProxyCodeDiskFull {
		t.Fatalf("应当报 disk_full: %#v", status.Results)
	}
	if status.Failed != 1 {
		t.Fatalf("失败计数应为 1: %#v", status)
	}
	if names := proxyTempFiles(t, service); len(names) != 0 {
		t.Fatalf("临时文件应被清理: %v", names)
	}
	if _, err := os.Stat(victimPath); err != nil {
		t.Fatalf("磁盘满不该触发淘汰，别人的代理应当还在: %v", err)
	}
}

// ffmpeg 以 stderr 报磁盘满（退出码里看不出来）也要认成 disk_full。
func TestPlaybackProxyDiskFullFromStderr(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "full2.mkv", 10)
	writeProxySnapshot(t, video, "h264", "aac", 0, 0)
	stub.err = errors.New("exit status 1")
	stub.stderr = "av_interleaved_write_frame(): No space left on device"

	if result := runProxyOnce(t, service, video.ID); result.Code != PlaybackProxyCodeDiskFull {
		t.Fatalf("stderr 里的 ENOSPC 也应认成 disk_full: %#v", result)
	}
}

// 一般编码失败：报 encode_failed 并清掉半个产物。
func TestPlaybackProxyEncodeFailureCleansTempFile(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "broken.mkv", 10)
	writeProxySnapshot(t, video, "h264", "aac", 0, 0)
	stub.beforeWrite = func(args []string) {
		// 编码器写了一半就死了：临时文件已经存在。
		if err := os.WriteFile(args[len(args)-1], []byte("half"), 0o600); err != nil {
			t.Fatalf("写半个产物失败: %v", err)
		}
	}
	stub.err = errors.New("exit status 1")
	stub.stderr = "Invalid data found when processing input"

	result := runProxyOnce(t, service, video.ID)
	if result.Code != PlaybackProxyCodeEncodeFailed {
		t.Fatalf("应当报 encode_failed: %#v", result)
	}
	if !strings.Contains(result.Message, "Invalid data") {
		t.Fatalf("错误摘要应含 stderr 尾部: %s", result.Message)
	}
	if names := proxyTempFiles(t, service); len(names) != 0 {
		t.Fatalf("临时文件应被清理: %v", names)
	}
	row := mustLoadProxyRow(t, video.ID)
	if row.Status != models.PlaybackProxyStatusFailed || row.LastError == "" {
		t.Fatalf("失败记录应带原因: %#v", row)
	}
}

// 产物时长与源相差超过 2%（步骤 7）：不落位，算编码失败。
func TestPlaybackProxyRejectsOutputWithWrongDuration(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "shortened.mkv", 100)
	writeProxySnapshot(t, video, "h264", "aac", 0, 0)
	stub.duration = 50

	result := runProxyOnce(t, service, video.ID)
	if result.Code != PlaybackProxyCodeEncodeFailed {
		t.Fatalf("时长对不上应当拒绝落位: %#v", result)
	}
	if names := proxyTempFiles(t, service); len(names) != 0 {
		t.Fatalf("临时文件应被清理: %v", names)
	}
}

// 快照缺失 → 先探测一次；探测失败就是 probe_failed。
func TestPlaybackProxyProbeFailedWhenSnapshotUnavailable(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	video := createProxyTestVideo(t, "unprobed.mkv", 10)

	probeCalls := 0
	service.probe = newMediaProbeServiceWithRunner(func(ctx context.Context, path string) ([]byte, string, error) {
		probeCalls++
		return nil, "moov atom not found", errors.New("exit status 1")
	})

	result := runProxyOnce(t, service, video.ID)
	if result.Code != PlaybackProxyCodeProbeFailed {
		t.Fatalf("应当报 probe_failed: %#v", result)
	}
	if probeCalls != 1 {
		t.Fatalf("快照缺失时应当只探测一次: %d", probeCalls)
	}
}

// 快照缺失但探测成功：继续走完策略判定并生成代理。
func TestPlaybackProxyProbesOnceThenEncodes(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "toprobe.mkv", 10)

	probeCalls := 0
	service.probe = newMediaProbeServiceWithRunner(func(ctx context.Context, path string) ([]byte, string, error) {
		probeCalls++
		payload := `{"format":{"format_name":"matroska","duration":"10.0","bit_rate":"3000000"},` +
			`"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080},` +
			`{"index":1,"codec_type":"audio","codec_name":"aac"}]}`
		return []byte(payload), "", nil
	})

	result := runProxyOnce(t, service, video.ID)
	if result.Code != PlaybackProxyCodeCreated || result.Strategy != models.PlaybackProxyStrategyRemux {
		t.Fatalf("探测成功后应当 remux: %#v", result)
	}
	if probeCalls != 1 {
		t.Fatalf("应当只探测一次: %d", probeCalls)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("ffmpeg 应当只跑一次: %d", len(stub.calls))
	}
}

// 源文件不在：报 file_missing 并标 is_stale（沿用既有语义）。
func TestPlaybackProxyFileMissingMarksStale(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	video := createProxyTestVideo(t, "gone.mkv", 10)
	if err := os.Remove(video.Path); err != nil {
		t.Fatalf("删除源文件失败: %v", err)
	}

	result := runProxyOnce(t, service, video.ID)
	if result.Code != PlaybackProxyCodeFileMissing {
		t.Fatalf("应当报 file_missing: %#v", result)
	}
	var reloaded models.Video
	if err := database.DB.First(&reloaded, video.ID).Error; err != nil {
		t.Fatalf("重读视频失败: %v", err)
	}
	if !reloaded.IsStale {
		t.Fatalf("源文件缺失应标 is_stale")
	}
}

// ===== 幂等 =====

// 已有有效代理：报 already_exists，不再调 ffmpeg。
func TestPlaybackProxyAlreadyExists(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "exists.mkv", 10)
	writeProxySnapshot(t, video, "h264", "aac", 0, 0)
	seedReadyProxy(t, service, video, 2048, time.Now())

	result := runProxyOnce(t, service, video.ID)
	if result.Code != PlaybackProxyCodeAlreadyExists {
		t.Fatalf("应当报 already_exists: %#v", result)
	}
	if len(stub.calls) != 0 {
		t.Fatalf("已有代理时不该调 ffmpeg: %#v", stub.calls)
	}
}

// 批量里的重复 ID 去重：同一个视频只处理一次。
func TestPlaybackProxyBatchDeduplicates(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "dup.mkv", 10)
	writeProxySnapshot(t, video, "h264", "aac", 0, 0)

	if _, err := service.BatchCreatePlaybackProxies(context.Background(), []uint{video.ID, video.ID, video.ID}); err != nil {
		t.Fatalf("批量入队失败: %v", err)
	}
	status := waitForProxyIdle(t, service)
	if status.Total != 1 || len(status.Results) != 1 {
		t.Fatalf("重复 ID 应当去重: %#v", status)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("ffmpeg 应当只跑一次: %d", len(stub.calls))
	}
}

// ===== 单 worker、槽位与取消 =====

// 每一项处理前抢 MediaWorkSlot（D-007）：槽被别人占着时代理任务等着。
func TestPlaybackProxyAcquiresMediaWorkSlot(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "slotted.mkv", 10)
	writeProxySnapshot(t, video, "h264", "aac", 0, 0)

	slot := NewMediaWorkSlot()
	service.SetMediaWorkSlot(slot)
	if err := slot.Acquire(context.Background()); err != nil {
		t.Fatalf("占住槽位失败: %v", err)
	}

	if _, err := service.CreatePlaybackProxy(context.Background(), video.ID); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if len(stub.calls) != 0 {
		t.Fatalf("槽位被占时不该开始编码: %#v", stub.calls)
	}
	slot.Release()
	status := waitForProxyIdle(t, service)
	if status.Succeeded != 1 {
		t.Fatalf("释放槽位后应当完成: %#v", status)
	}
}

// 取消：队列清空，后面的项一项都不做。
func TestPlaybackProxyCancelDrainsQueue(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	first := createProxyTestVideo(t, "first.mkv", 10)
	second := createProxyTestVideo(t, "second.mkv", 10)
	writeProxySnapshot(t, first, "h264", "aac", 0, 0)
	writeProxySnapshot(t, second, "h264", "aac", 0, 0)

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	stub.beforeWrite = func([]string) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
	}

	if _, err := service.BatchCreatePlaybackProxies(context.Background(), []uint{first.ID, second.ID}); err != nil {
		t.Fatalf("批量入队失败: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("第一项没有开始")
	}
	if err := service.CancelPlaybackProxyTask(); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	close(release)
	status := waitForProxyIdle(t, service)
	if !status.Cancelled {
		t.Fatalf("状态应为已取消: %#v", status)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("取消后不该继续第二项: %d", len(stub.calls))
	}
	if err := service.CancelPlaybackProxyTask(); !errors.Is(err, ErrPlaybackProxyNotRunning) {
		t.Fatalf("空闲时取消应当报未运行: %v", err)
	}
}

// 队列里已有同一个视频：第二次入队报 in_progress，不重复处理。
func TestPlaybackProxyInProgressIsNotRequeued(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "busy.mkv", 10)
	writeProxySnapshot(t, video, "h264", "aac", 0, 0)

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	stub.beforeWrite = func([]string) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
	}
	if _, err := service.CreatePlaybackProxy(context.Background(), video.ID); err != nil {
		t.Fatalf("首次入队失败: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("第一项没有开始")
	}
	if _, err := service.CreatePlaybackProxy(context.Background(), video.ID); err != nil {
		t.Fatalf("二次入队失败: %v", err)
	}
	close(release)
	status := waitForProxyIdle(t, service)
	codes := make([]string, 0, len(status.Results))
	for _, item := range status.Results {
		codes = append(codes, item.Code)
	}
	if !containsString(codes, PlaybackProxyCodeInProgress) {
		t.Fatalf("重复触发应报 in_progress: %#v", status.Results)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("同一视频不该被编码两次: %d", len(stub.calls))
	}
}

// 后台任务登记表：跑的时候 proxy 在册，跑完清空（defer End 无漂移）。
func TestPlaybackProxyRegistersBackgroundTask(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	registry := NewBackgroundTaskRegistry()
	service.SetBackgroundTaskRegistry(registry)
	video := createProxyTestVideo(t, "registered.mkv", 10)
	writeProxySnapshot(t, video, "h264", "aac", 0, 0)

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	stub.beforeWrite = func([]string) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
	}
	if _, err := service.CreatePlaybackProxy(context.Background(), video.ID); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("任务没有开始")
	}
	if running := registry.Snapshot(); !containsString(running, string(BackgroundTaskProxy)) {
		t.Fatalf("运行中应登记 proxy: %v", running)
	}
	close(release)
	waitForProxyIdle(t, service)
	if running := registry.Snapshot(); containsString(running, string(BackgroundTaskProxy)) {
		t.Fatalf("跑完应当注销 proxy: %v", running)
	}
}

// 一轮跑完发一条桌面通知，文案只有成功/失败条数：不含路径，也不含失败原因。
func TestPlaybackProxyNotifiesOnceOnCompletion(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	notifier := &stubDesktopNotifier{}
	service.SetDesktopNotifier(notifier)
	video := createProxyTestVideo(t, "notified.mkv", 10)
	writeProxySnapshot(t, video, "h264", "aac", 0, 0)

	runProxyOnce(t, service, video.ID)
	messages := notifier.Notifications()
	if len(messages) != 1 {
		t.Fatalf("应当只发一条通知: %#v", messages)
	}
	if !strings.Contains(messages[0].Body, "成功 1") || !strings.Contains(messages[0].Body, "失败 0") {
		t.Fatalf("通知正文应含成功/失败数: %s", messages[0].Body)
	}
	if strings.Contains(messages[0].Body, "/") || strings.Contains(messages[0].Title, "/") {
		t.Fatalf("通知不该出现路径: %s / %s", messages[0].Title, messages[0].Body)
	}
}

// ===== 自动路径候选筛选（D-006）=====

// 自动模式只挑内嵌白名单不命中的视频；mp4 这类本来就能内嵌的一律不做代理。
func TestPlaybackProxyAutoCandidatesSkipInlineWhitelist(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	inline := createProxyTestVideo(t, "inline.mp4", 10)
	needsProxy := createProxyTestVideo(t, "needs.mkv", 10)
	writeProxySnapshot(t, needsProxy, "h264", "aac", 0, 0)

	if _, err := service.EnqueueAutoCandidates(context.Background(), []uint{inline.ID, needsProxy.ID}); err != nil {
		t.Fatalf("自动入队失败: %v", err)
	}
	status := waitForProxyIdle(t, service)
	if status.Total != 1 || len(status.Results) != 1 || status.Results[0].VideoID != needsProxy.ID {
		t.Fatalf("只有非白名单视频该入队: %#v", status)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("ffmpeg 应当只跑一次: %d", len(stub.calls))
	}

	// 全是白名单命中时一项都不入队，也不启动 worker。
	fresh, freshStub := newProxyTestService(t)
	if _, err := fresh.EnqueueAutoCandidates(context.Background(), []uint{inline.ID}); err != nil {
		t.Fatalf("自动入队失败: %v", err)
	}
	if got := fresh.Status(); got.Total != 0 || got.Running {
		t.Fatalf("白名单命中时不该有任务: %#v", got)
	}
	if len(freshStub.calls) != 0 {
		t.Fatalf("不该调 ffmpeg: %#v", freshStub.calls)
	}
}

// ===== 上限归一化 =====

func TestNormalizeProxyCacheLimitBytes(t *testing.T) {
	if got := NormalizeProxyCacheLimitBytes(0); got != 0 {
		t.Fatalf("0 是「不限」，应当原样保留: %d", got)
	}
	if got := NormalizeProxyCacheLimitBytes(-1); got != database.DefaultProxyCacheLimitBytes {
		t.Fatalf("负数应归一化为默认值: %d", got)
	}
	if got := NormalizeProxyCacheLimitBytes(1234); got != 1234 {
		t.Fatalf("正数应原样保留: %d", got)
	}
}

func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// 取空与翻 Running 之间入队的那一项不能被丢掉：入队方看到 Running=true 不会
// 另起 worker，收尾方如果不在同一把锁里重新看一眼队列，这一项就永远没人处理。
func TestPlaybackProxyDoesNotDropItemEnqueuedWhileDraining(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	first := createProxyTestVideo(t, "drain-first.mkv", 10)
	second := createProxyTestVideo(t, "drain-second.mkv", 10)
	writeProxySnapshot(t, first, "h264", "aac", 0, 0)
	writeProxySnapshot(t, second, "h264", "aac", 0, 0)

	var once sync.Once
	service.onQueueDrained = func() {
		// 精确落在窗口里：队列刚被取空，Running 还没翻过来。
		once.Do(func() {
			if _, err := service.CreatePlaybackProxy(context.Background(), second.ID); err != nil {
				t.Errorf("窗口内入队失败: %v", err)
			}
		})
	}

	if _, err := service.CreatePlaybackProxy(context.Background(), first.ID); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	status := waitForProxyIdle(t, service)
	if status.Total != 2 || status.Succeeded != 2 {
		t.Fatalf("窗口内入队的那一项不该被丢掉: %#v", status)
	}
	if len(stub.calls) != 2 {
		t.Fatalf("两项都应当真的编码过: %d", len(stub.calls))
	}
	if status.Queued != 0 {
		t.Fatalf("收尾后队列应为空: %#v", status)
	}
}

// 取消之后 worker 还没退出这段时间里不接新活：那一轮的 ctx 已经死了，
// 塞进去会被清空吞掉。明确报"正在停止"让用户重试一次。
func TestPlaybackProxyRefusesEnqueueWhileStopping(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	first := createProxyTestVideo(t, "stopping-first.mkv", 10)
	second := createProxyTestVideo(t, "stopping-second.mkv", 10)
	writeProxySnapshot(t, first, "h264", "aac", 0, 0)
	writeProxySnapshot(t, second, "h264", "aac", 0, 0)

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	stub.beforeWrite = func([]string) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
	}
	if _, err := service.CreatePlaybackProxy(context.Background(), first.ID); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("第一项没有开始")
	}
	if err := service.CancelPlaybackProxyTask(); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	if _, err := service.CreatePlaybackProxy(context.Background(), second.ID); !errors.Is(err, ErrPlaybackProxyStopping) {
		t.Fatalf("停止中入队应当明确报错: %v", err)
	}
	close(release)
	waitForProxyIdle(t, service)

	// worker 退出之后 stopping 清掉，照常接活。
	if _, err := service.CreatePlaybackProxy(context.Background(), second.ID); err != nil {
		t.Fatalf("退出之后应当能重新入队: %v", err)
	}
	status := waitForProxyIdle(t, service)
	if status.Succeeded != 1 || status.Total != 1 {
		t.Fatalf("重新入队应当作为新的一轮: %#v", status)
	}
}

// 转码的缩放必须约束**长边**：只限宽的话竖屏 2160×3840 会产出 1920×3413，
// 那比 1080p 高得多（AC-03 要的是不高于 1080p）。表达式在滤镜期按 iw/ih 分支，
// 因此横竖两种源共用同一串参数——真实输出尺寸由集成测试钉住。
func TestPlaybackProxyTranscodeScaleClampsLongEdge(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "portrait.avi", 10)
	writeProxySnapshot(t, video, "mpeg4", "ac3", 0, 0)

	if result := runProxyOnce(t, service, video.ID); result.Code != PlaybackProxyCodeCreated {
		t.Fatalf("应当转码成功: %#v", result)
	}
	want := "scale='if(gt(iw,ih),min(1920,iw),-2)':'if(gt(iw,ih),-2,min(1920,ih))'"
	if !containsPair(stub.lastCall(), "-vf", want) {
		t.Fatalf("缩放表达式应当按长边约束\n实际: %#v\n期望 -vf %s", stub.lastCall(), want)
	}
	if containsPair(stub.lastCall(), "-vf", "scale='min(1920,iw)':-2") {
		t.Fatalf("只限宽的旧表达式不该再出现: %#v", stub.lastCall())
	}
}

// 封面图也是一条 video 流，`-map 0:v:0` 有可能选中它而不是正片：
// 映射一律用快照认定的主视频轨的绝对流序号。
func TestPlaybackProxySkipsAttachedPictureStream(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "withcover.mkv", 10)

	fingerprint, err := mediaProbeStat(video.Path)
	if err != nil {
		t.Fatalf("读取快照指纹失败: %v", err)
	}
	now := time.Now()
	if err := database.DB.Create(&models.VideoTechnicalMetadata{
		VideoID: video.ID, SuccessfulSourceSize: &fingerprint.size,
		SuccessfulSourceModTimeNS: &fingerprint.modTimeNS, ProbedAt: &now,
	}).Error; err != nil {
		t.Fatalf("写入技术快照失败: %v", err)
	}
	// 流 0 是封面图，流 1 才是正片，流 2 是音频。
	streams := []models.MediaStream{
		{VideoID: video.ID, StreamIndex: 0, StreamType: "video", CodecName: "mjpeg", IsAttachedPic: true},
		{VideoID: video.ID, StreamIndex: 1, StreamType: "video", CodecName: "h264"},
		{VideoID: video.ID, StreamIndex: 2, StreamType: "audio", CodecName: "aac"},
	}
	for i := range streams {
		if err := database.DB.Create(&streams[i]).Error; err != nil {
			t.Fatalf("写入流失败: %v", err)
		}
	}

	result := runProxyOnce(t, service, video.ID)
	if result.Code != PlaybackProxyCodeCreated || result.Strategy != models.PlaybackProxyStrategyRemux {
		t.Fatalf("正片是 H.264 + AAC，应当 remux: %#v", result)
	}
	args := stub.lastCall()
	if !containsPair(args, "-map", "0:1") || !containsPair(args, "-map", "0:2") {
		t.Fatalf("应当映射正片与音频的绝对序号: %#v", args)
	}
	if containsPair(args, "-map", "0:0") {
		t.Fatalf("不该映射封面图流: %#v", args)
	}
}

// ffmpeg 报错里的绝对路径不能跟着进库、也不能送到前端：
// last_error 长期留在 video_playback_proxies 里，逐项结果还会经事件推给界面。
func TestPlaybackProxyFailureMessageScrubsAbsolutePaths(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	video := createProxyTestVideo(t, "leaky.mkv", 10)
	writeProxySnapshot(t, video, "h264", "aac", 0, 0)
	stub.err = errors.New("exit status 1")
	stub.stderr = "/Users/someone/Movies/私人 影片.mkv: Invalid data found when processing input"

	result := runProxyOnce(t, service, video.ID)
	if result.Code != PlaybackProxyCodeEncodeFailed {
		t.Fatalf("应当报 encode_failed: %#v", result)
	}
	if strings.Contains(result.Message, "/Users/") || strings.Contains(result.Message, "Movies") {
		t.Fatalf("逐项结果不该带绝对路径: %s", result.Message)
	}
	if !strings.Contains(result.Message, "<path>") {
		t.Fatalf("路径应当被擦成 <path>: %s", result.Message)
	}
	if !strings.Contains(result.Message, "Invalid data found") {
		t.Fatalf("stderr 尾部的排障信息应当保留: %s", result.Message)
	}
	row := mustLoadProxyRow(t, video.ID)
	if strings.Contains(row.LastError, "/Users/") {
		t.Fatalf("写进表里的 last_error 不该带绝对路径: %s", row.LastError)
	}
}

func TestScrubPlaybackProxyPaths(t *testing.T) {
	cases := map[string]string{
		"":                                     "",
		"no paths here":                        "no paths here",
		"/a/b/c.mkv: boom":                     "<path>: boom",
		"open '/x/y z.mkv' failed":             "open '<path> z.mkv' failed",
		"a /p/q and /r/s end":                  "a <path> and <path> end",
		"Error while opening /Volumes/d/e.mov": "Error while opening <path>",
	}
	for input, want := range cases {
		if got := scrubPlaybackProxyPaths(input); got != want {
			t.Fatalf("scrubPlaybackProxyPaths(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// 取消的项也要有逐项结果，否则 Processed 永远追不上 Total。
func TestPlaybackProxyCancelRecordsCancelledResults(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	first := createProxyTestVideo(t, "cancel-first.mkv", 10)
	second := createProxyTestVideo(t, "cancel-second.mkv", 10)
	third := createProxyTestVideo(t, "cancel-third.mkv", 10)
	for _, video := range []models.Video{first, second, third} {
		writeProxySnapshot(t, video, "h264", "aac", 0, 0)
	}

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	stub.beforeWrite = func([]string) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
	}
	if _, err := service.BatchCreatePlaybackProxies(context.Background(), []uint{first.ID, second.ID, third.ID}); err != nil {
		t.Fatalf("批量入队失败: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("第一项没有开始")
	}
	if err := service.CancelPlaybackProxyTask(); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	close(release)
	status := waitForProxyIdle(t, service)

	if status.Processed != status.Total {
		t.Fatalf("取消之后 Processed 应当追上 Total: %#v", status)
	}
	cancelled := 0
	for _, item := range status.Results {
		if item.Code == PlaybackProxyCodeCancelled {
			cancelled++
		}
	}
	// 队列里没轮到的两条 + 正在跑被取消的那一条。
	if cancelled != 3 {
		t.Fatalf("三项都应当记成 cancelled: %#v", status.Results)
	}
	if status.Failed != 0 {
		t.Fatalf("取消不是失败: %#v", status)
	}
	if status.Skipped != 3 {
		t.Fatalf("取消应当计入 Skipped: %#v", status)
	}
}
