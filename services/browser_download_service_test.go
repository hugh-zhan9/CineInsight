package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"video-master/models"
)

// writeFakeFFmpeg 造一个假的 ffmpeg：吐几行进度、把最后一个参数当输出路径写出去。
// 参数原样落到一个文件里，测试据此断言真实的命令行长什么样。
func writeFakeFFmpeg(t *testing.T, script string) (binary string, argsLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("假 ffmpeg 用的是 sh 脚本，Windows 上跳过")
	}
	dir := t.TempDir()
	binary = filepath.Join(dir, "fake-ffmpeg")
	argsLog = filepath.Join(dir, "args.log")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsLog + "\n" + script
	if err := os.WriteFile(binary, []byte(body), 0o755); err != nil {
		t.Fatalf("写假 ffmpeg 失败: %v", err)
	}
	return binary, argsLog
}

const fakeFFmpegSuccess = `for arg in "$@"; do out="$arg"; done
echo "out_time_ms=1500000"
echo "total_size=2048"
echo "progress=end"
printf 'fake-media' > "$out"
exit 0
`

func newTestDownloadService(t *testing.T, dir string, binary string, importFn func(string, string) (uint, error)) *BrowserDownloadService {
	t.Helper()
	service := NewBrowserDownloadService(BrowserDownloadDeps{
		Settings: func() (BrowserDownloadSettings, error) {
			return BrowserDownloadSettings{Directory: dir, Concurrency: 2}, nil
		},
		ImportDirectory: importFn,
		FFmpegPath:      func() (string, error) { return binary, nil },
	})
	service.Start(context.Background())
	return service
}

func waitForState(t *testing.T, service *BrowserDownloadService, id string, want string) BrowserDownloadTask {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last BrowserDownloadTask
	for time.Now().Before(deadline) {
		for _, task := range service.ListTasks() {
			if task.ID == id {
				last = task
				if task.State == want {
					return task
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等不到任务 %s 变成 %s，最后停在 %s（错误：%s）", id, want, last.State, last.Error)
	return last
}

func TestBrowserDownloadRejectsNonHTTPSchemes(t *testing.T) {
	// ffmpeg 认得 file、concat、pipe 等一堆协议。放任协议等于把"读本机任意文件"
	// 的能力交给任何能往桥接发请求的东西。
	for _, rawURL := range []string{
		"file:///etc/passwd",
		"concat:/etc/passwd|/etc/hosts",
		"pipe:0",
		"ftp://host/x.mp4",
		"data:text/plain,hello",
		"/etc/passwd",
		"",
	} {
		_, err := normalizeBrowserDownloadRequest(BrowserDownloadRequest{URL: rawURL, Kind: "hls"})
		if !errors.Is(err, ErrBrowserDownloadInvalidRequest) {
			t.Fatalf("地址 %q 应当被拒，实际 err=%v", rawURL, err)
		}
	}
}

func TestBrowserDownloadRejectsHeaderInjection(t *testing.T) {
	// -headers 是一整段 CRLF 分隔的文本，值里带换行就能多塞任意请求头。
	for name, request := range map[string]BrowserDownloadRequest{
		"referer 换行": {URL: "https://a/b.m3u8", Referer: "https://a/\r\nX-Evil: 1"},
		"cookie 换行":  {URL: "https://a/b.m3u8", Cookie: "a=b\nX-Evil: 1"},
		"origin 换行":  {URL: "https://a/b.m3u8", Origin: "https://a\rX: 1"},
		"UA 换行":      {URL: "https://a/b.m3u8", UserAgent: "UA\r\nX: 1"},
	} {
		if _, err := normalizeBrowserDownloadRequest(request); !errors.Is(err, ErrBrowserDownloadInvalidRequest) {
			t.Fatalf("%s 应当被拒，实际 err=%v", name, err)
		}
	}
}

func TestBrowserDownloadRejectsUnknownKind(t *testing.T) {
	if _, err := normalizeBrowserDownloadRequest(BrowserDownloadRequest{URL: "https://a/b", Kind: "torrent"}); !errors.Is(err, ErrBrowserDownloadInvalidRequest) {
		t.Fatalf("未知类型应当被拒，实际 err=%v", err)
	}
	normalized, err := normalizeBrowserDownloadRequest(BrowserDownloadRequest{URL: "https://a/b.mkv", Kind: "file"})
	if err != nil {
		t.Fatalf("直链应当被接受：%v", err)
	}
	if normalized.OutputExtension != "mkv" {
		t.Fatalf("直链应当保留原扩展名，实际 %q", normalized.OutputExtension)
	}
}

func TestBrowserDownloadArgsAreSafe(t *testing.T) {
	normalized, err := normalizeBrowserDownloadRequest(BrowserDownloadRequest{
		URL:       "https://cdn/a.m3u8?token=x&y=1",
		Kind:      "hls",
		Referer:   "https://page/x",
		UserAgent: "UA/1",
	})
	if err != nil {
		t.Fatalf("归一化失败：%v", err)
	}
	args := buildBrowserDownloadArgs(normalized, "/tmp/out.mp4.part")

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-protocol_whitelist") {
		t.Fatal("必须显式限定协议白名单")
	}
	// 地址必须原样作为一个独立参数传递，不做任何拼接
	found := false
	for index, arg := range args {
		if arg == "-i" && index+1 < len(args) {
			if args[index+1] != normalized.URL {
				t.Fatalf("-i 的参数被改过：%q", args[index+1])
			}
			found = true
		}
	}
	if !found {
		t.Fatal("参数里没有 -i")
	}
	if args[len(args)-1] != "/tmp/out.mp4.part" {
		t.Fatalf("输出路径应当是最后一个参数，实际 %q", args[len(args)-1])
	}
}

func TestBrowserDownloadReservePathNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "某剧 第一集_1080p.mp4")
	if err := os.WriteFile(existing, []byte("原有内容"), 0o644); err != nil {
		t.Fatalf("准备已有文件失败：%v", err)
	}

	output, part, err := reserveBrowserDownloadPath(dir, "某剧 第一集", "1080p", "mp4")
	if err != nil {
		t.Fatalf("取文件名失败：%v", err)
	}
	if output == existing {
		t.Fatal("不该覆盖已经存在的文件")
	}
	if filepath.Base(output) != "某剧 第一集_1080p (2).mp4" {
		t.Fatalf("重名应当追加序号，实际 %q", filepath.Base(output))
	}
	if part != output+".part" {
		t.Fatalf("临时文件名不对：%q", part)
	}
	// 原文件内容没被动过
	content, _ := os.ReadFile(existing)
	if string(content) != "原有内容" {
		t.Fatal("已有文件的内容被改了")
	}
}

func TestBrowserDownloadReserveDoesNotCreateFinalFile(t *testing.T) {
	// 占位的必须是 .part。早先版本会先建一个空的最终文件来占名，而下载目录通常
	// 就是扫描目录：并发的另一个任务下载完成时会触发扫描，把这个 0 字节的 .mp4
	// 当成一条视频记录扫进片库；应用被强制退出时它还会永久留在那里。
	dir := t.TempDir()
	outputPath, partPath, err := reserveBrowserDownloadPath(dir, "占名测试", "", "mp4")
	if err != nil {
		t.Fatalf("取文件名失败：%v", err)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("下载期间不该存在最终文件：%s", outputPath)
	}
	if _, err := os.Stat(partPath); err != nil {
		t.Fatalf(".part 应当已经建好用来占名：%v", err)
	}

	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), browserDownloadPartSuffix) {
			t.Fatalf("下载目录里出现了非 .part 的占位文件：%s", entry.Name())
		}
	}
}

func TestBrowserDownloadFinalizeNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	outputPath, partPath, err := reserveBrowserDownloadPath(dir, "改名测试", "", "mp4")
	if err != nil {
		t.Fatalf("取文件名失败：%v", err)
	}
	if err := os.WriteFile(partPath, []byte("下载内容"), 0o644); err != nil {
		t.Fatalf("造 .part 失败：%v", err)
	}
	// 下载期间别的东西占了这个名字：改名必须让路，不能盖掉它
	if err := os.WriteFile(outputPath, []byte("别人的文件"), 0o644); err != nil {
		t.Fatalf("造抢占文件失败：%v", err)
	}

	finalPath, err := finalizeBrowserDownloadPath(partPath, outputPath)
	if err != nil {
		t.Fatalf("改名失败：%v", err)
	}
	if finalPath == outputPath {
		t.Fatal("不该覆盖下载期间出现的同名文件")
	}
	if content, _ := os.ReadFile(outputPath); string(content) != "别人的文件" {
		t.Fatal("别人的文件被改写了")
	}
	if content, _ := os.ReadFile(finalPath); string(content) != "下载内容" {
		t.Fatalf("产物内容不对：%s", finalPath)
	}
	if _, err := os.Stat(partPath); !os.IsNotExist(err) {
		t.Fatal(".part 改名后不该还在")
	}
}

func TestBrowserDownloadFilenameStaysInsideDirectory(t *testing.T) {
	dir := t.TempDir()
	// 标题里带路径分隔符与上跳段：都必须被清洗掉，结果只能落在下载目录里。
	output, _, err := reserveBrowserDownloadPath(dir, "../../../etc/passwd", "", "mp4")
	if err != nil {
		t.Fatalf("取文件名失败：%v", err)
	}
	if filepath.Dir(output) != filepath.Clean(dir) {
		t.Fatalf("文件逃出了下载目录：%q", output)
	}
	if strings.Contains(filepath.Base(output), "/") || strings.Contains(filepath.Base(output), "..") {
		t.Fatalf("文件名没清干净：%q", filepath.Base(output))
	}
}

func TestBrowserDownloadRejectsMissingDirectory(t *testing.T) {
	if _, _, err := reserveBrowserDownloadPath("", "标题", "", "mp4"); !errors.Is(err, ErrBrowserDownloadDirectoryUnset) {
		t.Fatalf("空目录应当报未设置，实际 %v", err)
	}
	if _, _, err := reserveBrowserDownloadPath(filepath.Join(t.TempDir(), "不存在"), "标题", "", "mp4"); err == nil {
		t.Fatal("不存在的目录应当报错")
	}
}

func TestBrowserDownloadEnqueueRequiresDirectory(t *testing.T) {
	service := NewBrowserDownloadService(BrowserDownloadDeps{
		Settings: func() (BrowserDownloadSettings, error) { return BrowserDownloadSettings{Directory: ""}, nil },
	})
	_, err := service.Enqueue(BrowserDownloadRequest{URL: "https://a/b.m3u8", Kind: "hls"})
	if !errors.Is(err, ErrBrowserDownloadDirectoryUnset) {
		t.Fatalf("没设下载目录时应当明确拒绝，实际 %v", err)
	}
}

func TestBrowserDownloadEndToEnd(t *testing.T) {
	dir := t.TempDir()
	binary, argsLog := writeFakeFFmpeg(t, fakeFFmpegSuccess)

	imported := make(chan string, 1)
	service := newTestDownloadService(t, dir, binary, func(directory, outputPath string) (uint, error) {
		imported <- directory
		return 1, nil
	})

	task, err := service.Enqueue(BrowserDownloadRequest{
		URL:          "https://cdn/a.m3u8",
		Kind:         "hls",
		Title:        "某剧 第一集",
		VariantLabel: "1080p",
		Referer:      "https://page/x",
		UserAgent:    "UA/1",
	})
	if err != nil {
		t.Fatalf("入队失败：%v", err)
	}

	done := waitForState(t, service, task.ID, browserDownloadStateDone)
	if done.Error != "" {
		t.Fatalf("任务不该有错误：%s", done.Error)
	}
	if done.Filename != "某剧 第一集_1080p.mp4" {
		t.Fatalf("文件名不对：%q", done.Filename)
	}
	if done.ProcessedSeconds != 1.5 {
		t.Fatalf("进度没解析出来：%v", done.ProcessedSeconds)
	}
	if done.BytesWritten != 2048 {
		t.Fatalf("字节数没解析出来：%v", done.BytesWritten)
	}

	// 产物落在下载目录里，且 .part 已经改名
	content, err := os.ReadFile(filepath.Join(dir, done.Filename))
	if err != nil || string(content) != "fake-media" {
		t.Fatalf("产物不对：err=%v content=%q", err, string(content))
	}
	if _, err := os.Stat(filepath.Join(dir, done.Filename+".part")); !os.IsNotExist(err) {
		t.Fatal(".part 临时文件没有清理掉")
	}

	select {
	case directory := <-imported:
		if directory != dir {
			t.Fatalf("入库扫描的目录不对：%q", directory)
		}
	case <-time.After(time.Second):
		t.Fatal("下载完成后没有触发入库")
	}

	// 命令行里请求头确实带上了
	logged, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(logged), "Referer: https://page/x") {
		t.Fatalf("Referer 没有传给 ffmpeg：%s", string(logged))
	}
	if !strings.Contains(string(logged), "UA/1") {
		t.Fatalf("User-Agent 没有传给 ffmpeg：%s", string(logged))
	}
}

func TestBrowserDownloadFailureKeepsNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	binary, _ := writeFakeFFmpeg(t, `for arg in "$@"; do out="$arg"; done
printf 'half' > "$out"
echo "取不到分片" 1>&2
exit 1
`)
	service := newTestDownloadService(t, dir, binary, nil)

	task, err := service.Enqueue(BrowserDownloadRequest{URL: "https://cdn/a.m3u8", Kind: "hls", Title: "失败的片子"})
	if err != nil {
		t.Fatalf("入队失败：%v", err)
	}
	failed := waitForState(t, service, task.ID, browserDownloadStateFailed)
	if !strings.Contains(failed.Error, "取不到分片") {
		t.Fatalf("失败原因应当带上 ffmpeg 的输出，实际 %q", failed.Error)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("失败后不该在下载目录留下任何文件，实际留下 %v", names)
	}
}

func TestBrowserDownloadImportFailureIsNotDownloadFailure(t *testing.T) {
	dir := t.TempDir()
	binary, _ := writeFakeFFmpeg(t, fakeFFmpegSuccess)
	service := newTestDownloadService(t, dir, binary, func(string, string) (uint, error) {
		return 0, errors.New("下载目录不在片库扫描目录里")
	})

	task, err := service.Enqueue(BrowserDownloadRequest{URL: "https://cdn/a.m3u8", Kind: "hls", Title: "入库失败"})
	if err != nil {
		t.Fatalf("入队失败：%v", err)
	}
	done := waitForState(t, service, task.ID, browserDownloadStateDone)
	if done.Error != "" {
		t.Fatalf("文件已经在盘上，不该报成下载失败：%q", done.Error)
	}
	if !strings.Contains(done.ImportError, "扫描目录") {
		t.Fatalf("入库失败应当单独记下来，实际 %q", done.ImportError)
	}
	if _, err := os.Stat(filepath.Join(dir, done.Filename)); err != nil {
		t.Fatalf("入库失败不该动已下载的文件：%v", err)
	}
}

func TestBrowserDownloadCancelQueuedTask(t *testing.T) {
	dir := t.TempDir()
	// 并发设为 1 并让第一个任务卡住，第二个就会一直排队。
	binary, _ := writeFakeFFmpeg(t, "sleep 5\nexit 0\n")
	service := NewBrowserDownloadService(BrowserDownloadDeps{
		Settings: func() (BrowserDownloadSettings, error) {
			return BrowserDownloadSettings{Directory: dir, Concurrency: 1}, nil
		},
		FFmpegPath: func() (string, error) { return binary, nil },
	})
	service.Start(context.Background())

	first, err := service.Enqueue(BrowserDownloadRequest{URL: "https://cdn/a.m3u8", Kind: "hls", Title: "第一个"})
	if err != nil {
		t.Fatalf("入队失败：%v", err)
	}
	second, err := service.Enqueue(BrowserDownloadRequest{URL: "https://cdn/b.m3u8", Kind: "hls", Title: "第二个"})
	if err != nil {
		t.Fatalf("入队失败：%v", err)
	}
	waitForState(t, service, first.ID, browserDownloadStateRun)

	if err := service.CancelTask(second.ID); err != nil {
		t.Fatalf("取消排队任务失败：%v", err)
	}
	canceled := waitForState(t, service, second.ID, browserDownloadStateCancel)
	if canceled.State != browserDownloadStateCancel {
		t.Fatalf("排队任务应当直接进取消态，实际 %s", canceled.State)
	}

	if err := service.CancelTask(first.ID); err != nil {
		t.Fatalf("取消运行中任务失败：%v", err)
	}
	waitForState(t, service, first.ID, browserDownloadStateCancel)
	service.Wait()

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("取消后不该留下文件，实际 %d 个", len(entries))
	}
}

func TestBrowserDownloadCancelUnknownTask(t *testing.T) {
	service := NewBrowserDownloadService(BrowserDownloadDeps{
		Settings: func() (BrowserDownloadSettings, error) { return BrowserDownloadSettings{Directory: "/tmp"}, nil },
	})
	if err := service.CancelTask("nope"); err == nil {
		t.Fatal("取消不存在的任务应当报错")
	}
}

func TestBrowserDownloadConcurrencyIsBounded(t *testing.T) {
	if got := NormalizeBrowserDownloadConcurrency(0); got != 2 {
		t.Fatalf("非正并发应当取默认 2，实际 %d", got)
	}
	if got := NormalizeBrowserDownloadConcurrency(-3); got != 2 {
		t.Fatalf("负数并发应当取默认 2，实际 %d", got)
	}
	if got := NormalizeBrowserDownloadConcurrency(99); got != 4 {
		t.Fatalf("并发应当被收到 4，实际 %d", got)
	}
	if got := NormalizeBrowserDownloadConcurrency(3); got != 3 {
		t.Fatalf("合法并发应当原样保留，实际 %d", got)
	}
}

func TestBrowserDownloadQueueFullIsRejected(t *testing.T) {
	dir := t.TempDir()
	binary, _ := writeFakeFFmpeg(t, "sleep 5\nexit 0\n")
	service := NewBrowserDownloadService(BrowserDownloadDeps{
		Settings: func() (BrowserDownloadSettings, error) {
			return BrowserDownloadSettings{Directory: dir, Concurrency: 1}, nil
		},
		FFmpegPath: func() (string, error) { return binary, nil },
	})
	service.Start(context.Background())
	t.Cleanup(func() {
		for _, task := range service.ListTasks() {
			_ = service.CancelTask(task.ID)
		}
		service.Wait()
	})

	var lastErr error
	for index := 0; index < browserDownloadMaxQueue+5; index++ {
		_, lastErr = service.Enqueue(BrowserDownloadRequest{
			URL:   fmt.Sprintf("https://cdn/%d.m3u8", index),
			Kind:  "hls",
			Title: fmt.Sprintf("任务 %d", index),
		})
		if lastErr != nil {
			break
		}
	}
	if !errors.Is(lastErr, ErrBrowserDownloadQueueFull) {
		t.Fatalf("队列满了应当明确拒绝，实际 %v", lastErr)
	}
}

func TestBrowserDownloadImporterRequiresScanDirectory(t *testing.T) {
	// 下载目录不在扫描目录里时，如实说清楚"文件保存了但没入库"，
	// 不自作主张把它加进扫描目录。
	importer := BrowserDownloadImporterFromScan(&VideoService{}, func() ([]models.ScanDirectory, error) {
		return []models.ScanDirectory{{Path: "/library/videos"}}, nil
	})
	_, err := importer("/downloads", "/downloads/a.mp4")
	if err == nil || !strings.Contains(err.Error(), "没有入库") {
		t.Fatalf("应当说明没有入库，实际 %v", err)
	}
}

func TestBrowserDownloadArgsSpecifyOutputFormat(t *testing.T) {
	// 输出文件名以 .part 结尾，ffmpeg 无法从扩展名推断格式，必须显式 -f。
	// 少了它 ffmpeg 会直接拒绝：Unable to choose an output format。
	for extension, muxer := range map[string]string{"mp4": "mp4", "mkv": "matroska", "ts": "mpegts"} {
		normalized, err := normalizeBrowserDownloadRequest(BrowserDownloadRequest{
			URL: "https://cdn/a." + extension, Kind: "file",
		})
		if err != nil {
			t.Fatalf("%s 归一化失败: %v", extension, err)
		}
		if normalized.OutputFormat != muxer {
			t.Fatalf("%s 的封装器应当是 %s，实际 %s", extension, muxer, normalized.OutputFormat)
		}
		args := buildBrowserDownloadArgs(normalized, "/tmp/out."+extension+".part")
		found := false
		for index, arg := range args {
			if arg == "-f" && index+1 < len(args) && args[index+1] == muxer {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s 的命令行里没有 -f %s：%v", extension, muxer, args)
		}
	}
	// HLS 一律输出 mp4
	hls, err := normalizeBrowserDownloadRequest(BrowserDownloadRequest{URL: "https://cdn/a.m3u8", Kind: "hls"})
	if err != nil || hls.OutputFormat != "mp4" {
		t.Fatalf("HLS 应当输出 mp4：format=%s err=%v", hls.OutputFormat, err)
	}
}

func TestBrowserDownloadRejectsUnknownContainer(t *testing.T) {
	// 认不出封装器时明确拒绝，而不是让 ffmpeg 抛一句用户看不懂的错。
	_, err := normalizeBrowserDownloadRequest(BrowserDownloadRequest{URL: "https://cdn/a.weirdext", Kind: "file"})
	if !errors.Is(err, ErrBrowserDownloadInvalidRequest) {
		t.Fatalf("不支持的容器应当被拒，实际 err=%v", err)
	}
}

// 用真的 ffmpeg 跑一遍。
//
// 这条是补课：之前只用假 ffmpeg 脚本测，那个脚本什么输出路径都收，于是
// "输出名以 .part 结尾导致 ffmpeg 认不出封装格式"这个真实缺陷一路漏到用户那里。
func TestBrowserDownloadWithRealFFmpeg(t *testing.T) {
	binary, err := findBrowserDownloadFFmpeg()
	if err != nil {
		t.Skipf("没装 ffmpeg，跳过真机集成测试: %v", err)
	}

	// 先用 ffmpeg 造一个极小的 mp4 当作"网上的视频"
	sourceDir := t.TempDir()
	sourcePath := filepath.Join(sourceDir, "source.mp4")
	gen := exec.Command(binary, "-nostdin", "-v", "error",
		"-f", "lavfi", "-i", "testsrc=size=64x64:rate=5:duration=1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-y", sourcePath)
	if output, genErr := gen.CombinedOutput(); genErr != nil {
		t.Skipf("造测试视频失败（ffmpeg 可能缺 libx264）: %v %s", genErr, string(output))
	}

	server := httptest.NewServer(http.FileServer(http.Dir(sourceDir)))
	defer server.Close()

	downloadDir := t.TempDir()
	imported := make(chan string, 1)
	service := NewBrowserDownloadService(BrowserDownloadDeps{
		Settings: func() (BrowserDownloadSettings, error) {
			return BrowserDownloadSettings{Directory: downloadDir, Concurrency: 1}, nil
		},
		ImportDirectory: func(directory, outputPath string) (uint, error) { imported <- directory; return 1, nil },
	})
	service.Start(context.Background())

	task, err := service.Enqueue(BrowserDownloadRequest{
		URL:   server.URL + "/source.mp4",
		Kind:  "file",
		Title: "真机集成测试",
	})
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}

	done := waitForState(t, service, task.ID, browserDownloadStateDone)
	if done.Error != "" {
		t.Fatalf("真 ffmpeg 下载不该失败: %s", done.Error)
	}
	info, err := os.Stat(filepath.Join(downloadDir, done.Filename))
	if err != nil {
		t.Fatalf("产物不存在: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("产物是空文件")
	}
	if filepath.Ext(done.Filename) != ".mp4" {
		t.Fatalf("产物扩展名应当是 .mp4，实际 %s", done.Filename)
	}
	if _, err := os.Stat(filepath.Join(downloadDir, done.Filename+browserDownloadPartSuffix)); !os.IsNotExist(err) {
		t.Fatal(".part 没有清理掉")
	}
}

// 入库之后要把片库记录的 ID 带回任务上：界面靠它取缩略图，
// 几个任务并排时光看文件名分不清谁是谁。
func TestBrowserDownloadCarriesImportedVideoID(t *testing.T) {
	dir := t.TempDir()
	binary, _ := writeFakeFFmpeg(t, fakeFFmpegSuccess)
	service := newTestDownloadService(t, dir, binary, func(directory, outputPath string) (uint, error) {
		return 42, nil
	})
	task, err := service.Enqueue(BrowserDownloadRequest{URL: "https://cdn/a.m3u8", Kind: "hls", Title: "带缩略图"})
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	done := waitForState(t, service, task.ID, browserDownloadStateDone)
	if done.VideoID != 42 {
		t.Fatalf("完成后应当带上入库的视频 ID，实际 %d", done.VideoID)
	}
}
