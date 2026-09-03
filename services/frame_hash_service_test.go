package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// ascendingGrayscaleFrame 每行都是从左到右递增：dHash 的每一位都是"左不亮于右" → 0。
func ascendingGrayscaleFrame() []byte {
	frame := make([]byte, frameHashGrayscaleBytes)
	for row := 0; row < 8; row++ {
		for column := 0; column < 9; column++ {
			frame[row*9+column] = byte(column * 10)
		}
	}
	return frame
}

// descendingGrayscaleFrame 每行都是从左到右递减：每一位都是 1。
func descendingGrayscaleFrame() []byte {
	frame := make([]byte, frameHashGrayscaleBytes)
	for row := 0; row < 8; row++ {
		for column := 0; column < 9; column++ {
			frame[row*9+column] = byte((8 - column) * 10)
		}
	}
	return frame
}

// topRowDescendingFrame 只有第 0 行递减：高 8 位为 1，其余为 0。
// 这一帧钉住位序——行优先、第 0 行在高位。
func topRowDescendingFrame() []byte {
	frame := ascendingGrayscaleFrame()
	for column := 0; column < 9; column++ {
		frame[column] = byte((8 - column) * 10)
	}
	return frame
}

func stubFrameHashRunner(frames ...[]byte) frameHashRawFrameRunner {
	return func(_ context.Context, _ string, consume func([]byte)) error {
		for _, frame := range frames {
			consume(frame)
		}
		return nil
	}
}

func newTestFrameHashService(t *testing.T, runner frameHashRawFrameRunner) *FrameHashService {
	t.Helper()
	service := NewFrameHashService(nil)
	service.rawFrames = runner
	return service
}

func seedFrameHashVideo(t *testing.T, name string, payload []byte) models.Video {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("写入视频文件失败: %v", err)
	}
	video := models.Video{Name: name, Path: path, Directory: root, Duration: 120}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	return video
}

func waitForFrameHashStatus(t *testing.T, service *FrameHashService, accept func(FrameHashStatus) bool) FrameHashStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var status FrameHashStatus
	for time.Now().Before(deadline) {
		status = service.Status()
		if accept(status) {
			return status
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待帧哈希状态超时，实际 %+v", status)
	return status
}

func frameHashSequenceRow(t *testing.T, videoID uint) models.VideoFrameHashSequence {
	t.Helper()
	var row models.VideoFrameHashSequence
	if err := database.DB.First(&row, "video_id = ?", videoID).Error; err != nil {
		t.Fatalf("读取帧哈希序列失败(%s): %v", dbtest.Backend(), err)
	}
	return row
}

// dHash 的位序必须写死在测试里：序列一旦落库，改位序就等于让全库的序列失效。
func TestFrameHashFromGrayscalePinsBitOrder(t *testing.T) {
	if hash, err := frameHashFromGrayscale(ascendingGrayscaleFrame()); err != nil || hash != 0 {
		t.Fatalf("每行递增应得全 0：hash=%#x err=%v", hash, err)
	}
	if hash, err := frameHashFromGrayscale(descendingGrayscaleFrame()); err != nil || hash != 0xFFFFFFFFFFFFFFFF {
		t.Fatalf("每行递减应得全 1：hash=%#x err=%v", hash, err)
	}
	if hash, err := frameHashFromGrayscale(topRowDescendingFrame()); err != nil || hash != 0xFF00000000000000 {
		t.Fatalf("只有第 0 行递减应得高 8 位为 1：hash=%#x err=%v", hash, err)
	}
	if _, err := frameHashFromGrayscale(make([]byte, 71)); err == nil {
		t.Fatal("字节数不对时必须报错，不能算出一个凑数的哈希")
	}
}

// 真实 ffmpeg 的输出是一条连续字节流，切帧这一步自己要站得住：
// 72 字节一帧，尾部残缺说明输出被截断，宁可报错也不许把半帧算成一帧。
func TestReadFrameHashRawFramesSplitsFramesAndRejectsTruncation(t *testing.T) {
	stream := bytes.Join([][]byte{ascendingGrayscaleFrame(), descendingGrayscaleFrame(), topRowDescendingFrame()}, nil)
	var hashes []uint64
	if err := readFrameHashRawFrames(bytes.NewReader(stream), func(frame []byte) {
		hash, err := frameHashFromGrayscale(frame)
		if err != nil {
			t.Fatalf("算哈希失败: %v", err)
		}
		hashes = append(hashes, hash)
	}); err != nil {
		t.Fatalf("读取完整帧流不该报错: %v", err)
	}
	want := []uint64{0, 0xFFFFFFFFFFFFFFFF, 0xFF00000000000000}
	if len(hashes) != len(want) {
		t.Fatalf("应切出 %d 帧，实际 %d 帧", len(want), len(hashes))
	}
	for index := range want {
		if hashes[index] != want[index] {
			t.Fatalf("第 %d 帧不符：%#x != %#x", index, hashes[index], want[index])
		}
	}

	truncated := append(append([]byte(nil), stream[:frameHashGrayscaleBytes]...), 1, 2, 3)
	err := readFrameHashRawFrames(bytes.NewReader(truncated), func([]byte) {})
	if err == nil {
		t.Fatal("残缺的尾帧必须报错")
	}
}

// 回填一趟：序列、帧数、采样间隔、源指纹都要落库，状态也要对上。
func TestFrameHashBackfillStoresSequence(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := seedFrameHashVideo(t, "one.mp4", []byte("frame-hash-one"))
	service := newTestFrameHashService(t, stubFrameHashRunner(
		ascendingGrayscaleFrame(), descendingGrayscaleFrame(), topRowDescendingFrame(),
	))

	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("启动帧哈希回填失败: %v", err)
	}
	defer service.StopAndWait()
	status := waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return status.Completed && !status.Running })
	if status.Total != 1 || status.Succeeded != 1 || status.Failed != 0 || status.Skipped != 0 {
		t.Fatalf("一个候选应成功一项: %+v", status)
	}

	row := frameHashSequenceRow(t, video.ID)
	if row.IntervalMS != 2000 {
		t.Fatalf("采样间隔应写入 2000(%s)，实际 %d", dbtest.Backend(), row.IntervalMS)
	}
	if row.FrameCount != 3 {
		t.Fatalf("帧数应为 3，实际 %d", row.FrameCount)
	}
	if row.LastError != "" {
		t.Fatalf("成功的行不该带 last_error: %q", row.LastError)
	}
	hashes := decodeFrameHashes(row.Hashes)
	want := []uint64{0, 0xFFFFFFFFFFFFFFFF, 0xFF00000000000000}
	if len(hashes) != len(want) {
		t.Fatalf("读回帧数不一致: %d != %d", len(hashes), len(want))
	}
	for index := range want {
		if hashes[index] != want[index] {
			t.Fatalf("第 %d 帧不符：%#x != %#x", index, hashes[index], want[index])
		}
	}
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatalf("读取文件信息失败: %v", err)
	}
	if row.SourceSize != info.Size() || row.SourceModTimeNS != info.ModTime().UnixNano() {
		t.Fatalf("源指纹应与文件一致: row=(%d,%d) file=(%d,%d)",
			row.SourceSize, row.SourceModTimeNS, info.Size(), info.ModTime().UnixNano())
	}
}

// 指纹跳过与失效重算：可续跑靠的就是这一条。
func TestFrameHashBackfillSkipsFreshAndRecomputesAfterSourceChange(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := seedFrameHashVideo(t, "two.mp4", []byte("frame-hash-two"))
	service := newTestFrameHashService(t, stubFrameHashRunner(ascendingGrayscaleFrame(), descendingGrayscaleFrame()))

	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("首轮启动失败: %v", err)
	}
	waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return status.Completed && !status.Running })
	service.StopAndWait()

	// 第二轮：指纹没变，候选集合应当是空的。
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("第二轮启动失败: %v", err)
	}
	status := waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return status.Completed && !status.Running })
	service.StopAndWait()
	if status.Total != 0 || status.Succeeded != 0 {
		t.Fatalf("指纹未变时不该有候选: %+v", status)
	}

	// 源文件变了（重编码）：这一行失效，必须重算。
	if err := os.WriteFile(video.Path, []byte("frame-hash-two-re-encoded"), 0o644); err != nil {
		t.Fatalf("改写视频文件失败: %v", err)
	}
	service.rawFrames = stubFrameHashRunner(topRowDescendingFrame())
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("第三轮启动失败: %v", err)
	}
	status = waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return status.Completed && !status.Running })
	service.StopAndWait()
	if status.Total != 1 || status.Succeeded != 1 {
		t.Fatalf("源文件变更后应重算: %+v", status)
	}
	row := frameHashSequenceRow(t, video.ID)
	if row.FrameCount != 1 {
		t.Fatalf("重算后帧数应为 1，实际 %d", row.FrameCount)
	}
	if hashes := decodeFrameHashes(row.Hashes); len(hashes) != 1 || hashes[0] != 0xFF00000000000000 {
		t.Fatalf("重算后序列应被整行替换，实际 %#v", hashes)
	}
}

// 抽帧失败要留下可读的原因，并且下一轮还会再试——不能留一行空序列让匹配阶段
// 以为"这个视频已经算过了"。
func TestFrameHashBackfillRecordsFailureAndRetriesNextRun(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := seedFrameHashVideo(t, "broken.mp4", []byte("frame-hash-broken"))
	service := newTestFrameHashService(t, func(context.Context, string, func([]byte)) error {
		return errors.New("ffmpeg 抽帧失败: exit status 1")
	})

	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	status := waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return status.Completed && !status.Running })
	service.StopAndWait()
	if status.Failed != 1 || len(status.Failures) != 1 || status.Failures[0].VideoID != video.ID {
		t.Fatalf("失败应记在状态里: %+v", status)
	}
	row := frameHashSequenceRow(t, video.ID)
	if row.LastError == "" || row.FrameCount != 0 {
		t.Fatalf("失败行应带 last_error 且帧数为 0(%s): %+v", dbtest.Backend(), row)
	}

	// 带 last_error 的行不算新鲜：下一轮回填要再试一次。
	service.rawFrames = stubFrameHashRunner(ascendingGrayscaleFrame())
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("第二轮启动失败: %v", err)
	}
	status = waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return status.Completed && !status.Running })
	service.StopAndWait()
	if status.Total != 1 || status.Succeeded != 1 {
		t.Fatalf("失败过的视频下一轮应重试: %+v", status)
	}
	if row := frameHashSequenceRow(t, video.ID); row.LastError != "" || row.FrameCount != 1 {
		t.Fatalf("重试成功后应清掉 last_error: %+v", row)
	}
}

// last_error 是要落库、被备份带走、随迁移搬走的持久数据，不许留绝对路径。
func TestRedactFrameHashErrorPathsStripsAbsolutePaths(t *testing.T) {
	for _, testCase := range []struct{ name, in, want string }{
		{"ffmpeg 报错带输入路径", "ffmpeg 抽帧失败: exit status 1: /Users/me/lib/a.mp4: Invalid data found", "ffmpeg 抽帧失败: exit status 1: <path> Invalid data found"},
		// 引号被排除在 token 之外，所以引号里的路径抹得更干净：闭合引号与冒号都留着。
		{"引号里的路径", "Cannot open '/Volumes/disk/影片/a.mkv': No such file", "Cannot open '<path>': No such file"},
		{"两个路径", "copy /a/b.mp4 /c/d.mp4 failed", "copy <path> <path> failed"},
		{"行首就是路径", "/Users/me/a.mp4: moov atom not found", "<path> moov atom not found"},
		{"滤镜里的斜杠不动", "Error initializing filter 'fps' with args 1000/2000", "Error initializing filter 'fps' with args 1000/2000"},
		{"相对路径不动", "open bin/ffmpeg: permission denied", "open bin/ffmpeg: permission denied"},
		{"没有路径", "ffmpeg 没有抽出任何帧", "ffmpeg 没有抽出任何帧"},
	} {
		if got := redactFrameHashErrorPaths(testCase.in); got != testCase.want {
			t.Fatalf("%s：\n got=%q\nwant=%q", testCase.name, got, testCase.want)
		}
	}
}

// 落库那一步真的抹了：直接断言表里的值，而不是只测纯函数。
func TestFrameHashFailureRowDoesNotPersistAbsolutePaths(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := seedFrameHashVideo(t, "leaky.mp4", []byte("frame-hash-leaky"))
	service := newTestFrameHashService(t, func(_ context.Context, path string, _ func([]byte)) error {
		// 真实 runner 的错误里就带着这个路径（ffmpeg 把它打在 stderr 上）。
		return fmt.Errorf("ffmpeg 抽帧失败: exit status 1: %s: Invalid data found", path)
	})

	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return status.Completed && !status.Running })
	service.StopAndWait()

	row := frameHashSequenceRow(t, video.ID)
	if row.LastError == "" {
		t.Fatal("失败行应带 last_error")
	}
	if strings.Contains(row.LastError, video.Path) || strings.Contains(row.LastError, "/") {
		t.Fatalf("落库的 last_error 不该含绝对路径: %q", row.LastError)
	}
	if !strings.Contains(row.LastError, "<path>") || !strings.Contains(row.LastError, "Invalid data found") {
		t.Fatalf("路径应换成 <path> 且保留其余原因: %q", row.LastError)
	}
}

// 一帧都没抽出来算失败，不算成功的空序列。
func TestFrameHashBackfillTreatsEmptyOutputAsFailure(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := seedFrameHashVideo(t, "empty.mp4", []byte("frame-hash-empty"))
	service := newTestFrameHashService(t, stubFrameHashRunner())

	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	status := waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return status.Completed && !status.Running })
	service.StopAndWait()
	if status.Failed != 1 || status.Succeeded != 0 {
		t.Fatalf("空输出应记为失败: %+v", status)
	}
	if row := frameHashSequenceRow(t, video.ID); row.FrameCount != 0 || row.LastError == "" {
		t.Fatalf("空输出行应带 last_error: %+v", row)
	}
}

// 每处理一项之前要取重媒体槽（D-007）：槽被别人占着时一项都不许开工。
func TestFrameHashBackfillAcquiresMediaWorkSlot(t *testing.T) {
	setupVideoServiceTestDB(t)
	seedFrameHashVideo(t, "slot.mp4", []byte("frame-hash-slot"))
	slot := NewMediaWorkSlot()
	if err := slot.Acquire(context.Background()); err != nil {
		t.Fatalf("测试自己先占住槽位失败: %v", err)
	}
	service := NewFrameHashService(slot)
	service.rawFrames = stubFrameHashRunner(ascendingGrayscaleFrame())

	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer service.StopAndWait()
	// 候选已经排好（Total=1），但槽位在别人手里，一项都不该处理。
	waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return !status.Preparing && status.Total == 1 })
	time.Sleep(30 * time.Millisecond)
	if status := service.Status(); status.Processed != 0 {
		t.Fatalf("槽位被占时不该处理任何一项: %+v", status)
	}

	slot.Release()
	status := waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return status.Completed && !status.Running })
	if status.Succeeded != 1 {
		t.Fatalf("槽位释放后应跑完: %+v", status)
	}
	// 槽位必须归还：拿了不还，下一个重任务永远等不到。
	if err := slot.Acquire(context.Background()); err != nil {
		t.Fatalf("跑完之后槽位应当是空的: %v", err)
	}
	slot.Release()
}

// 登记表配对（D-014）：跑起来登记 frame_hash，收尾必须清空。
func TestFrameHashBackfillPairsBackgroundTaskRegistry(t *testing.T) {
	setupVideoServiceTestDB(t)
	seedFrameHashVideo(t, "registry.mp4", []byte("frame-hash-registry"))
	registry := NewBackgroundTaskRegistry()
	service := newTestFrameHashService(t, stubFrameHashRunner(ascendingGrayscaleFrame()))
	service.SetBackgroundTaskRegistry(registry)

	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer service.StopAndWait()
	if tasks := registry.Snapshot(); len(tasks) != 1 || tasks[0] != "frame_hash" {
		t.Fatalf("运行中的任务应登记为 frame_hash: %v", tasks)
	}
	waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return status.Completed && !status.Running })
	service.StopAndWait()
	if tasks := registry.Snapshot(); len(tasks) != 0 {
		t.Fatalf("任务结束后登记表应清空: %v", tasks)
	}
}

// 项间检查点（AC-21）：自动路径在用户活跃时停在下一项之前，
// 「忽略空闲立即运行」之后一路跑完，bypass 豁免的是这一轮而不是一项。
func TestFrameHashPauseHookWaitsThenRunsAfterBypass(t *testing.T) {
	setupVideoServiceTestDB(t)
	seedFrameHashVideo(t, "gate-one.mp4", []byte("frame-hash-gate-one"))
	seedFrameHashVideo(t, "gate-two.mp4", []byte("frame-hash-gate-two"))

	gate := busyGate()
	registry := NewBackgroundTaskRegistry()
	registry.SetOnChange(gate.SyncRunningTasks)
	service := newTestFrameHashService(t, stubFrameHashRunner(ascendingGrayscaleFrame()))
	service.SetBackgroundTaskRegistry(registry)

	if _, err := service.StartWithPauseHook(context.Background(), gate.PauseHook(string(BackgroundTaskFrameHash))); err != nil {
		t.Fatalf("自动启动失败: %v", err)
	}
	defer service.StopAndWait()

	status := waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return status.Gate.WaitingIdle })
	if status.Gate.Reason != IdleWaitReasonUserActive {
		t.Fatalf("用户活跃时状态应为 waiting_idle/user_active: %+v", status.Gate)
	}
	if status.Processed != 0 {
		t.Fatalf("等待空闲期间不该处理任何一项: %+v", status)
	}

	if err := gate.RunGatedTaskNow(string(BackgroundTaskFrameHash)); err != nil {
		t.Fatalf("立即运行失败: %v", err)
	}
	status = waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return status.Completed && !status.Running })
	if status.Succeeded != 2 {
		t.Fatalf("两个视频都该处理完（bypass 只豁免一项？）: %+v", status)
	}
	if status.Gate.WaitingIdle {
		t.Fatalf("收尾后应清掉门状态: %+v", status.Gate)
	}
}

// 停在检查点上的任务仍然可以取消：Cancel 立刻生效，不用等空闲。
func TestFrameHashCancelWhileWaitingIdle(t *testing.T) {
	setupVideoServiceTestDB(t)
	seedFrameHashVideo(t, "cancel.mp4", []byte("frame-hash-cancel"))

	gate := busyGate()
	service := newTestFrameHashService(t, stubFrameHashRunner(ascendingGrayscaleFrame()))
	if _, err := service.StartWithPauseHook(context.Background(), gate.PauseHook(string(BackgroundTaskFrameHash))); err != nil {
		t.Fatalf("自动启动失败: %v", err)
	}
	defer service.StopAndWait()
	waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return status.Gate.WaitingIdle })

	if err := service.Cancel(); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	status := waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return !status.Running })
	if !status.Cancelled || status.Completed {
		t.Fatalf("等待空闲期间取消应立刻收尾为已取消: %+v", status)
	}
	if status.Processed != 0 {
		t.Fatalf("取消时不该处理任何一项: %+v", status)
	}
	if waiting := gate.GetIdleSchedulerStatus().Waiting; len(waiting) != 0 {
		t.Fatalf("取消后不该留下等待项: %+v", waiting)
	}
	// 没在跑的时候取消要说清楚，不能假装成功。
	if err := service.Cancel(); !errors.Is(err, ErrFrameHashNotRunning) {
		t.Fatalf("未运行时取消应返回 ErrFrameHashNotRunning，实际 %v", err)
	}
}

// 显式启动摘掉钩子：停在检查点上的 worker 要被立刻放出来继续跑。
func TestFrameHashExplicitStartReleasesParkedWorker(t *testing.T) {
	setupVideoServiceTestDB(t)
	seedFrameHashVideo(t, "release-one.mp4", []byte("frame-hash-release-one"))
	seedFrameHashVideo(t, "release-two.mp4", []byte("frame-hash-release-two"))

	gate := busyGate()
	service := newTestFrameHashService(t, stubFrameHashRunner(ascendingGrayscaleFrame()))
	if _, err := service.StartWithPauseHook(context.Background(), gate.PauseHook(string(BackgroundTaskFrameHash))); err != nil {
		t.Fatalf("自动启动失败: %v", err)
	}
	defer service.StopAndWait()
	waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return status.Gate.WaitingIdle })

	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("显式启动失败: %v", err)
	}
	status := waitForFrameHashStatus(t, service, func(status FrameHashStatus) bool { return status.Completed && !status.Running })
	if status.Succeeded != 2 {
		t.Fatalf("摘钩子后应一路跑完: %+v", status)
	}
	if waiting := gate.GetIdleSchedulerStatus().Waiting; len(waiting) != 0 {
		t.Fatalf("放行后不该留下等待项: %+v", waiting)
	}
}

// 帧哈希的自动开关必须真的存得下来（两个后端都要成立）。
func TestUpdateSettingsPersistsAutoFrameHashSequence(t *testing.T) {
	database.DB = dbtest.Open(t)
	if err := database.DB.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2}).Error; err != nil {
		t.Fatalf("创建设置失败(%s): %v", dbtest.Backend(), err)
	}
	service := &SettingsService{}

	if err := service.UpdateSettings(models.Settings{VideoExtensions: ".mp4", PlayWeight: 2, AutoFrameHashSequence: true}); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	saved, err := service.GetSettings()
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if !saved.AutoFrameHashSequence {
		t.Fatalf("自动帧哈希开关应被保存为开(%s)", dbtest.Backend())
	}

	if err := service.UpdateSettings(models.Settings{VideoExtensions: ".mp4", PlayWeight: 2}); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	saved, err = service.GetSettings()
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if saved.AutoFrameHashSequence {
		t.Fatalf("自动帧哈希开关应被保存为关(%s)", dbtest.Backend())
	}
}
