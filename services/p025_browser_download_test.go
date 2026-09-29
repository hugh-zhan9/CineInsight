package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// P-025：浏览器下载任务的落库、清洗、重启中断、同会话重试与三个入库动作。

func withBrowserDownloadStore(service *BrowserDownloadService) *BrowserDownloadService {
	service.SetStore(func() *gorm.DB { return database.DB })
	return service
}

func loadBrowserDownloadRows(t *testing.T) []models.BrowserDownloadTask {
	t.Helper()
	var rows []models.BrowserDownloadTask
	if err := database.DB.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("读取下载任务表失败: %v", err)
	}
	return rows
}

func mustLoadBrowserDownloadRow(t *testing.T, taskUID string) models.BrowserDownloadTask {
	t.Helper()
	var row models.BrowserDownloadTask
	if err := database.DB.Where("task_uid = ?", taskUID).First(&row).Error; err != nil {
		t.Fatalf("读取下载任务 %s 失败: %v", taskUID, err)
	}
	return row
}

// 验收用例（详细设计 §5.4）：ffmpeg stderr 带签名 query 与 Cookie: 行，表里查不到签名串与 Cookie 值。
func TestBrowserDownloadPersistedErrorIsSanitizedMEDIA10(t *testing.T) {
	setupVideoServiceTestDB(t)
	dir := t.TempDir()
	const signature = "SIGNATUREabc123XYZ"
	const cookieValue = "sessCOOKIEvalue42"
	const bearer = "BEARERtoken777"
	binary, _ := writeFakeFFmpeg(t, `echo "[https @ 0x1] HTTP error 403 Forbidden" 1>&2
echo "https://user:pw@cdn.example.com/hls/seg-001.ts?sig=`+signature+`&expires=1700000000#frag: Server returned 403 Forbidden" 1>&2
echo "Cookie: session=`+cookieValue+`; theme=dark" 1>&2
echo "Authorization: Bearer `+bearer+`" 1>&2
echo "X-Amz-Signature: `+signature+`" 1>&2
echo "Failed to open `+dir+`/leak/out.mp4.part" 1>&2
exit 1
`)
	service := withBrowserDownloadStore(newTestDownloadService(t, dir, binary, nil))

	task, err := service.Enqueue(BrowserDownloadRequest{
		URL:     "https://cdn.example.com/hls/index.m3u8?sig=" + signature + "&token=abcdefgh1234#part",
		Kind:    "hls",
		Title:   "带签名的流",
		Referer: "https://page.example.com/watch?v=123456789",
		Cookie:  "session=" + cookieValue + "; theme=dark",
	})
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	failed := waitForState(t, service, task.ID, browserDownloadStateFailed)
	service.Wait()

	rows := loadBrowserDownloadRows(t)
	if len(rows) != 1 {
		t.Fatalf("应当恰好落一行，实际 %d", len(rows))
	}
	row := rows[0]
	for _, field := range []string{row.TaskUID, row.DisplayURL, row.FileName, row.Directory, row.Status, row.Error} {
		for _, secret := range []string{signature, cookieValue, bearer, "abcdefgh1234", "sig=", "token=", "user:pw", "#frag", "#part", "Cookie", "Authorization", "X-Amz"} {
			if strings.Contains(field, secret) {
				t.Fatalf("表里出现了敏感串 %q：%q", secret, field)
			}
		}
	}
	if row.DisplayURL != "https://cdn.example.com/hls/index.m3u8" {
		t.Fatalf("display_url 只应含 scheme+host+path，实际 %q", row.DisplayURL)
	}
	if row.Status != browserDownloadStateFailed || row.FinishedAt == nil {
		t.Fatalf("终态应当写 failed 与 finished_at: %+v", row)
	}
	if !strings.Contains(row.Error, "403") || !strings.Contains(row.Error, "https://cdn.example.com/hls/seg-001.ts") {
		t.Fatalf("清洗后仍应保留可读的原因与去掉 query 的地址，实际 %q", row.Error)
	}
	if strings.Contains(row.Error, dir) {
		t.Fatalf("error 里不该出现本机路径：%q", row.Error)
	}
	if len([]rune(row.Error)) > browserDownloadErrorMaxRunes {
		t.Fatalf("error 超过 %d 字符：%d", browserDownloadErrorMaxRunes, len([]rune(row.Error)))
	}
	// 内存与表是同一份清洗结果：重启前后界面看到的原因一致。
	if failed.Error != row.Error {
		t.Fatalf("内存与表里的错误应当一致：memory=%q row=%q", failed.Error, row.Error)
	}
}

func TestSanitizeBrowserDownloadErrorRulesMEDIA10(t *testing.T) {
	long := strings.Repeat("长", 700)
	cases := []struct {
		name    string
		input   string
		secrets []string
		want    []string
		absent  []string
	}{
		{
			name:   "URL 去掉 query、fragment、路径参数与 userinfo",
			input:  "open https://u:p@cdn.test/a/b.m3u8;jsessionid=JSESSION99?sig=S1#f failed",
			want:   []string{"https://cdn.test/a/b.m3u8", "failed"},
			absent: []string{"u:p@", "JSESSION99", "sig=", "S1", "#f"},
		},
		{
			name:   "请求头样式的行整行删除",
			input:  "line one\nCookie: a=1\nset-cookie: b=2\r\nAuthorization: Basic Zm9v\nX-Signed-Token: t\nline two",
			want:   []string{"line one", "line two"},
			absent: []string{"a=1", "b=2", "Zm9v", "Signed-Token"},
		},
		{
			name:   "绝对路径擦成 <path>，URL 不被当成路径",
			input:  "cannot write /Users/me/Downloads/x.mp4.part: Permission denied; see https://cdn.test/p/q.ts",
			want:   []string{"<path>: Permission denied", "https://cdn.test/p/q.ts"},
			absent: []string{"/Users/me"},
		},
		{
			name:    "已知敏感值逐字擦除",
			input:   "bad cookie value SECRETVALUE9 rejected",
			secrets: []string{"SECRETVALUE9"},
			absent:  []string{"SECRETVALUE9"},
			want:    []string{"<redacted>"},
		},
		{
			name:   "NUL 与非法 UTF-8 去掉（PG text 存不下）",
			input:  "bad\x00byte\xff here",
			want:   []string{"badbyte here"},
			absent: []string{"\x00", "\xff"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := sanitizeBrowserDownloadError(testCase.input, testCase.secrets)
			for _, want := range testCase.want {
				if !strings.Contains(got, want) {
					t.Fatalf("结果应含 %q，实际 %q", want, got)
				}
			}
			for _, absent := range testCase.absent {
				if strings.Contains(got, absent) {
					t.Fatalf("结果不应含 %q，实际 %q", absent, got)
				}
			}
		})
	}
	if got := sanitizeBrowserDownloadError(long, nil); len([]rune(got)) != browserDownloadErrorMaxRunes {
		t.Fatalf("应当截到 %d 字符，实际 %d", browserDownloadErrorMaxRunes, len([]rune(got)))
	}
}

// 启动时非终态行一律改为 interrupted；终态行与本次会话内存里的任务不动。
func TestBrowserDownloadStartupMarksUnfinishedRowsInterruptedMEDIA10(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Now()
	for uid, status := range map[string]string{
		"old-queued": browserDownloadStateQueued, "old-running": browserDownloadStateRun,
		"old-importing": browserDownloadStateImport, "old-done": browserDownloadStateDone,
		"old-failed": browserDownloadStateFailed, "live-queued": browserDownloadStateQueued,
	} {
		row := models.BrowserDownloadTask{TaskUID: uid, Status: status, DisplayURL: "https://cdn/x", CreatedAt: now, UpdatedAt: now}
		if err := database.DB.Create(&row).Error; err != nil {
			t.Fatalf("写入夹具失败: %v", err)
		}
	}
	service := withBrowserDownloadStore(NewBrowserDownloadService(BrowserDownloadDeps{}))
	service.entries["live-queued"] = &browserDownloadEntry{task: BrowserDownloadTask{ID: "live-queued", State: browserDownloadStateQueued}}

	marked, err := service.MarkInterruptedOnStartup()
	if err != nil {
		t.Fatalf("标记中断失败: %v", err)
	}
	if marked != 3 {
		t.Fatalf("应当标记 3 行，实际 %d", marked)
	}
	for uid, want := range map[string]string{
		"old-queued": browserDownloadStateInterrupted, "old-running": browserDownloadStateInterrupted,
		"old-importing": browserDownloadStateInterrupted, "old-done": browserDownloadStateDone,
		"old-failed": browserDownloadStateFailed, "live-queued": browserDownloadStateQueued,
	} {
		row := mustLoadBrowserDownloadRow(t, uid)
		if row.Status != want {
			t.Fatalf("%s 应为 %s，实际 %s", uid, want, row.Status)
		}
		if want == browserDownloadStateInterrupted && row.FinishedAt == nil {
			t.Fatalf("%s 置为 interrupted 时应写 finished_at", uid)
		}
	}
	// 重复调用无害。
	if again, err := service.MarkInterruptedOnStartup(); err != nil || again != 0 {
		t.Fatalf("重复标记应当无事可做: marked=%d err=%v", again, err)
	}
}

// 应用退出（生命周期上下文结束）打断的下载记为 interrupted，不是用户取消。
func TestBrowserDownloadShutdownRecordsInterruptedMEDIA10(t *testing.T) {
	setupVideoServiceTestDB(t)
	dir := t.TempDir()
	binary, _ := writeFakeFFmpeg(t, "sleep 5\nexit 0\n")
	service := withBrowserDownloadStore(NewBrowserDownloadService(BrowserDownloadDeps{
		Settings: func() (BrowserDownloadSettings, error) {
			return BrowserDownloadSettings{Directory: dir, Concurrency: 1}, nil
		},
		FFmpegPath: func() (string, error) { return binary, nil },
	}))
	lifecycle, stop := context.WithCancel(context.Background())
	service.Start(lifecycle)
	task, err := service.Enqueue(BrowserDownloadRequest{URL: "https://cdn/a.m3u8", Kind: "hls", Title: "退出时在跑"})
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	waitForState(t, service, task.ID, browserDownloadStateRun)
	stop()
	waitForState(t, service, task.ID, browserDownloadStateInterrupted)
	service.Wait()
	if row := mustLoadBrowserDownloadRow(t, task.ID); row.Status != browserDownloadStateInterrupted {
		t.Fatalf("表里应记 interrupted，实际 %s", row.Status)
	}
}

// 重启后内存清空：历史从表里带出来，但不能重试（请求头不在了）。
func TestBrowserDownloadHistorySurvivesRestartButNeedsBrowserMEDIA10(t *testing.T) {
	setupVideoServiceTestDB(t)
	dir := t.TempDir()
	binary, _ := writeFakeFFmpeg(t, "echo boom 1>&2\nexit 1\n")
	first := withBrowserDownloadStore(newTestDownloadService(t, dir, binary, nil))
	task, err := first.Enqueue(BrowserDownloadRequest{URL: "https://cdn/a.m3u8?k=secretkey99", Kind: "hls", Title: "重启前失败", Cookie: "c=cookievalue99"})
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	waitForState(t, first, task.ID, browserDownloadStateFailed)
	first.Wait()

	restarted := withBrowserDownloadStore(newTestDownloadService(t, dir, binary, nil))
	tasks := restarted.ListDownloadTasks()
	if len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Fatalf("重启后应从表里带出历史任务: %+v", tasks)
	}
	history := tasks[0]
	if history.State != browserDownloadStateFailed || history.Retryable || history.URL != "https://cdn/a.m3u8" {
		t.Fatalf("历史任务的形态不对: %+v", history)
	}
	if history.FinishedAt == 0 || !strings.Contains(history.Error, "boom") {
		t.Fatalf("历史任务应带完成时间与原因: %+v", history)
	}
	result, err := restarted.RetryDownload(task.ID)
	if err != nil || result.Code != BrowserDownloadCodeRetryRequiresBrowser {
		t.Fatalf("重启后重试应返回 retry_requires_browser: %+v err=%v", result, err)
	}
}

// 同一会话内失败的下载可以重试：沿用原任务，请求头仍在内存里。
func TestBrowserDownloadRetryInSameSessionMEDIA09(t *testing.T) {
	setupVideoServiceTestDB(t)
	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "attempted")
	binary, argsLog := writeFakeFFmpeg(t, `for arg in "$@"; do out="$arg"; done
if [ ! -f "`+marker+`" ]; then
  : > "`+marker+`"
  echo "first attempt fails" 1>&2
  exit 1
fi
printf 'media' > "$out"
exit 0
`)
	service := withBrowserDownloadStore(newTestDownloadService(t, dir, binary, nil))
	task, err := service.Enqueue(BrowserDownloadRequest{URL: "https://cdn/a.m3u8", Kind: "hls", Title: "重试", Referer: "https://page/r"})
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	failed := waitForState(t, service, task.ID, browserDownloadStateFailed)
	if !failed.Retryable {
		t.Fatalf("同会话的失败任务应可重试: %+v", failed)
	}
	if listed := service.ListDownloadTasks(); len(listed) != 1 || !listed[0].Retryable {
		t.Fatalf("列表里的失败任务应标 retryable: %+v", listed)
	}

	result, err := service.RetryDownload(task.ID)
	if err != nil || result.Code != BrowserDownloadCodeOK || result.Task == nil || result.Task.ID != task.ID {
		t.Fatalf("重试应当受理: %+v err=%v", result, err)
	}
	done := waitForState(t, service, task.ID, browserDownloadStateDone)
	service.Wait()
	if done.Error != "" || done.Retryable {
		t.Fatalf("重试成功后不该残留错误或可重试标记: %+v", done)
	}
	if logged, _ := os.ReadFile(argsLog); !strings.Contains(string(logged), "Referer: https://page/r") {
		t.Fatalf("重试必须带上原来的请求头: %s", logged)
	}
	row := mustLoadBrowserDownloadRow(t, task.ID)
	if row.Status != browserDownloadStateDone || row.Error != "" || row.FileName != done.Filename {
		t.Fatalf("表里同一行应改为完成且清掉错误: %+v", row)
	}

	// 只有失败或已取消的任务能重试。
	if again, _ := service.RetryDownload(task.ID); again.Code != BrowserDownloadCodeNotRetryable {
		t.Fatalf("已完成的任务不该能重试: %+v", again)
	}
}

// 文件已保存但下载目录不在扫描范围：给出入库状态；把目录加进扫描范围后「重新入库」可以把它收进片库，
// 重启后的历史任务同样按当前状态现算入库状态。「打开所在目录」定位到文件本身。
func TestBrowserDownloadImportActionsMEDIA09(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	downloadDir := filepath.Join(root, "downloads")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binary, _ := writeFakeFFmpeg(t, fakeFFmpegSuccess)
	directories := &DirectoryService{}
	importer := BrowserDownloadImporterFromScan(&VideoService{}, directories.GetAllDirectories)
	var revealed []string
	newService := func() *BrowserDownloadService {
		service := withBrowserDownloadStore(NewBrowserDownloadService(BrowserDownloadDeps{
			Settings: func() (BrowserDownloadSettings, error) {
				return BrowserDownloadSettings{Directory: downloadDir, Concurrency: 1}, nil
			},
			ImportDirectory: importer,
			FFmpegPath:      func() (string, error) { return binary, nil },
			Reveal:          func(path string) error { revealed = append(revealed, path); return nil },
		}))
		service.Start(context.Background())
		return service
	}
	service := newService()
	task, err := service.Enqueue(BrowserDownloadRequest{URL: "https://cdn/a.m3u8", Kind: "hls", Title: "未入库"})
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	done := waitForState(t, service, task.ID, browserDownloadStateDone)
	service.Wait()
	if done.ImportStatus != BrowserDownloadImportNotInScanRoots || done.ImportError != ErrBrowserDownloadNotInScanRoots.Error() {
		t.Fatalf("不在扫描范围时应标 not_in_scan_roots 且只给一句原因: %+v", done)
	}
	if strings.Contains(done.ImportError, "没有入库") || strings.Contains(done.ImportError, downloadDir) {
		t.Fatalf("原因里不该重复「没有入库」或带路径: %q", done.ImportError)
	}

	// 目录还没加：重新入库如实报 not_in_scan_roots。
	if result, err := service.ReimportDownload(task.ID); err != nil || result.Code != BrowserDownloadImportNotInScanRoots {
		t.Fatalf("目录不在扫描范围时重新入库应报 not_in_scan_roots: %+v err=%v", result, err)
	}
	directory, check, err := service.FinishedDownloadDirectory(task.ID)
	if err != nil || check.Code != BrowserDownloadCodeOK || directory != downloadDir {
		t.Fatalf("应当取到下载目录: dir=%q check=%+v err=%v", directory, check, err)
	}

	// 重启后的历史任务：按当前扫描目录现算，仍是 not_in_scan_roots，三个动作都可用。
	restarted := newService()
	history := restarted.ListDownloadTasks()
	if len(history) != 1 || history[0].ImportStatus != BrowserDownloadImportNotInScanRoots {
		t.Fatalf("重启后历史任务应现算出 not_in_scan_roots: %+v", history)
	}

	if _, err := directories.AddDirectory(downloadDir, ""); err != nil {
		t.Fatalf("加入扫描目录失败: %v", err)
	}
	result, err := restarted.ReimportDownload(task.ID)
	if err != nil || result.Code != BrowserDownloadCodeOK || result.Task == nil || result.Task.ImportStatus != BrowserDownloadImportImported || result.Task.VideoID == 0 {
		t.Fatalf("加入扫描目录后重新入库应成功: %+v err=%v", result, err)
	}
	var video models.Video
	if err := database.DB.Where("path = ?", done.OutputPath).First(&video).Error; err != nil {
		t.Fatalf("文件应当进了片库: %v", err)
	}
	listed := restarted.ListDownloadTasks()
	if len(listed) != 1 || listed[0].ImportStatus != BrowserDownloadImportImported || listed[0].VideoID != video.ID {
		t.Fatalf("入库后历史任务应显示已入库: %+v", listed)
	}

	reveal, err := restarted.RevealDownload(task.ID)
	if err != nil || reveal.Code != BrowserDownloadCodeOK || len(revealed) != 1 || revealed[0] != done.OutputPath {
		t.Fatalf("打开所在目录应定位到下载的文件: %+v revealed=%v err=%v", reveal, revealed, err)
	}

	// 文件被删掉之后三个动作如实报 file_missing；未知任务报 task_not_found。
	if err := os.Remove(done.OutputPath); err != nil {
		t.Fatal(err)
	}
	if result, _ := restarted.RevealDownload(task.ID); result.Code != BrowserDownloadCodeFileMissing {
		t.Fatalf("文件不在时应报 file_missing: %+v", result)
	}
	if result, _ := restarted.ReimportDownload("nope"); result.Code != BrowserDownloadCodeTaskNotFound {
		t.Fatalf("未知任务应报 task_not_found: %+v", result)
	}
}

// 失败任务的入库动作报 not_finished，不会拿一个不存在的产物去扫描。
func TestBrowserDownloadImportActionsRequireFinishedTaskMEDIA09(t *testing.T) {
	setupVideoServiceTestDB(t)
	dir := t.TempDir()
	binary, _ := writeFakeFFmpeg(t, "exit 1\n")
	imports := 0
	service := withBrowserDownloadStore(newTestDownloadService(t, dir, binary, func(string, string) (uint, error) {
		imports++
		return 0, errors.New("不该被调用")
	}))
	task, err := service.Enqueue(BrowserDownloadRequest{URL: "https://cdn/a.m3u8", Kind: "hls", Title: "失败"})
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, service, task.ID, browserDownloadStateFailed)
	service.Wait()
	for name, action := range map[string]func(string) (BrowserDownloadActionResult, error){
		"reimport": service.ReimportDownload, "reveal": service.RevealDownload,
	} {
		if result, err := action(task.ID); err != nil || result.Code != BrowserDownloadCodeNotFinished {
			t.Fatalf("%s 对失败任务应报 not_finished: %+v err=%v", name, result, err)
		}
	}
	if imports != 0 {
		t.Fatalf("失败任务不该触发入库: %d", imports)
	}
}

// ===== P-025 复审：URL 清洗绕过、推送快照、完成丢请求规格、表行裁剪 =====

// I-1 / M-1：userinfo 先于路径参数去掉（按最后一个 @），path 上才截 ? # ;；URL 前面紧贴下划线
// 或字母数字时也要认得出来。
func TestBrowserDownloadStripURLUserinfoBeforePathParamsMEDIA10(t *testing.T) {
	cases := map[string]string{
		"https://u:p;w@host/x":                                   "https://host/x",
		"https://alice:pa55;word@cdn.example.com/a/b":            "https://cdn.example.com/a/b",
		"https://a@b:c;d@cdn.example.com/p;jsessionid=J?sig=S#f": "https://cdn.example.com/p",
		"https://u:p@host?sig=SECRET":                            "https://host",
		"https://u:p@host#frag":                                  "https://host",
		"https://host/a@b/c?x=1":                                 "https://host/a@b/c",
	}
	for input, want := range cases {
		if got := browserDownloadDisplayURL(input); got != want {
			t.Fatalf("display_url(%q) = %q，应为 %q", input, got, want)
		}
	}
	got := sanitizeBrowserDownloadError(
		"open _https://bob:hunter22;x@cdn.example.com/seg-1.ts?sig=SIGunderscore9 failed\n"+
			"retry 9https://carol:secret77@cdn.example.com/seg-2.ts#FRAGMENT7 failed",
		nil)
	for _, absent := range []string{"bob", "hunter22", "SIGunderscore9", "sig=", "carol", "secret77", "FRAGMENT7", "@"} {
		if strings.Contains(got, absent) {
			t.Fatalf("清洗结果不应含 %q：%q", absent, got)
		}
	}
	for _, want := range []string{"cdn.example.com/seg-1.ts", "cdn.example.com/seg-2.ts", "failed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("清洗结果应保留 %q：%q", want, got)
		}
	}
}

// I-1 验收：请求地址带 userinfo（密码含 ;）与签名，ffmpeg 报错里还有 _https:// 形态的分片地址与
// 单独打印的凭证。表里查不到用户名、密码、签名。
func TestBrowserDownloadPersistedRowHidesUserinfoAndSignaturesMEDIA10(t *testing.T) {
	setupVideoServiceTestDB(t)
	dir := t.TempDir()
	const user = "alicexyz"
	const password = "pa55;word"
	const signature = "SIGNATURE7788"
	const segmentSig = "SEGSIG445566"
	binary, _ := writeFakeFFmpeg(t, `echo "[https @ 0x1] HTTP error 401 Unauthorized" 1>&2
echo "_https://`+user+`:`+password+`@cdn.example.com/hls/seg-001.ts?sig=`+segmentSig+`: Server returned 401" 1>&2
echo "login `+user+` rejected (password `+password+`)" 1>&2
exit 1
`)
	service := withBrowserDownloadStore(newTestDownloadService(t, dir, binary, nil))
	task, err := service.Enqueue(BrowserDownloadRequest{
		URL:   "https://" + user + ":" + password + "@cdn.example.com/hls/index.m3u8?sig=" + signature,
		Kind:  "hls",
		Title: "带凭证的流",
	})
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	waitForState(t, service, task.ID, browserDownloadStateFailed)
	service.Wait()

	row := mustLoadBrowserDownloadRow(t, task.ID)
	if row.DisplayURL != "https://cdn.example.com/hls/index.m3u8" {
		t.Fatalf("display_url 只应含 scheme+host+path，实际 %q", row.DisplayURL)
	}
	for _, field := range []string{row.DisplayURL, row.FileName, row.Directory, row.Error} {
		for _, secret := range []string{user, password, "pa55", signature, segmentSig, "sig="} {
			if strings.Contains(field, secret) {
				t.Fatalf("表里出现了敏感串 %q：%q", secret, field)
			}
		}
	}
	if !strings.Contains(row.Error, "https://cdn.example.com/hls/seg-001.ts") || !strings.Contains(row.Error, "401") {
		t.Fatalf("清洗后仍应保留可读的原因与去掉 userinfo / query 的地址：%q", row.Error)
	}
}

// M-2：推送事件用历史快照拼列表，快照在锁外刷新——store（查库）从不在持有 emitMu 时被调用；
// 纯进度更新不查库；快照只在终态、显式列举时刷新。
func TestBrowserDownloadEventsUseHistorySnapshotOutsideEmitLockMEDIA10(t *testing.T) {
	setupVideoServiceTestDB(t)
	now := time.Now()
	if err := database.DB.Create(&models.BrowserDownloadTask{TaskUID: "history-1", Status: browserDownloadStateDone, CreatedAt: now.Add(-time.Hour), UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewBrowserDownloadService(BrowserDownloadDeps{})
	var storeCalls, underEmitLock int
	service.SetStore(func() *gorm.DB {
		storeCalls++
		if service.emitMu.TryLock() {
			service.emitMu.Unlock()
		} else {
			underEmitLock++
		}
		return database.DB
	})
	var emitted [][]BrowserDownloadTask
	service.SetEventEmitter(func(tasks []BrowserDownloadTask) { emitted = append(emitted, tasks) })
	clock := now
	service.deps.Now = func() time.Time { return clock }
	service.entries["live-1"] = &browserDownloadEntry{task: BrowserDownloadTask{ID: "live-1", State: browserDownloadStateRun, CreatedAt: now.UnixMilli()}}

	service.update("live-1", func(task *BrowserDownloadTask) { task.ProcessedSeconds = 1 })
	if len(emitted) != 1 || len(emitted[0]) != 2 {
		t.Fatalf("第一次推送应带上内存任务与表里的历史: %+v", emitted)
	}
	afterFirst := storeCalls
	for i := 2; i <= 5; i++ {
		clock = clock.Add(time.Second) // 越过 500ms 节流，每次都真推送
		seconds := float64(i)
		service.update("live-1", func(task *BrowserDownloadTask) { task.ProcessedSeconds = seconds })
	}
	if len(emitted) != 5 {
		t.Fatalf("越过节流后每次进度都应推送: %d", len(emitted))
	}
	if storeCalls != afterFirst {
		t.Fatalf("纯进度推送不该查库: 之前 %d 次，之后 %d 次", afterFirst, storeCalls)
	}

	// 快照只在显式刷新时更新：表里新插一行，进度推送看不到；列举一次之后看得到。
	if err := database.DB.Create(&models.BrowserDownloadTask{TaskUID: "history-2", Status: browserDownloadStateFailed, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	service.update("live-1", func(task *BrowserDownloadTask) { task.ProcessedSeconds = 9 })
	if got := len(emitted[len(emitted)-1]); got != 2 {
		t.Fatalf("快照未刷新前推送不应看到新行: %d", got)
	}
	if listed := service.ListDownloadTasks(); len(listed) != 3 {
		t.Fatalf("显式列举应现读历史: %+v", listed)
	}
	clock = clock.Add(time.Second)
	service.update("live-1", func(task *BrowserDownloadTask) { task.ProcessedSeconds = 10 })
	if got := len(emitted[len(emitted)-1]); got != 3 {
		t.Fatalf("列举刷新快照之后推送应带上新行: %d", got)
	}
	// 终态同样刷新快照（在推送之前、锁外）。
	service.update("live-1", func(task *BrowserDownloadTask) { task.State = browserDownloadStateFailed })
	if underEmitLock != 0 {
		t.Fatalf("store 不该在持有 emitMu 时被调用，实际 %d 次", underEmitLock)
	}
}

// M-4：完成的任务从内存丢掉请求规格（带 Cookie）；重试如实报「不可重试」而不是「请回浏览器」。
// 失败的任务保留规格以便同会话重试（TestBrowserDownloadRetryInSameSessionMEDIA09）。
func TestBrowserDownloadDoneDropsRequestSpecMEDIA10(t *testing.T) {
	setupVideoServiceTestDB(t)
	dir := t.TempDir()
	binary, _ := writeFakeFFmpeg(t, fakeFFmpegSuccess)
	service := withBrowserDownloadStore(newTestDownloadService(t, dir, binary, nil))
	task, err := service.Enqueue(BrowserDownloadRequest{URL: "https://cdn/a.m3u8", Kind: "hls", Title: "完成", Cookie: "session=cookievalue99"})
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, service, task.ID, browserDownloadStateDone)
	service.Wait()
	service.mu.Lock()
	request := service.entries[task.ID].request
	service.mu.Unlock()
	if request != nil {
		t.Fatalf("完成的任务不该再握着请求规格（含 Cookie）: %+v", request)
	}
	if result, err := service.RetryDownload(task.ID); err != nil || result.Code != BrowserDownloadCodeNotRetryable {
		t.Fatalf("完成的任务重试应报 not_retryable: %+v err=%v", result, err)
	}
}

// MEDIA-10（B-m-3）：userinfo 里的短凭证（admin:admin）不受「<6 不擦」门槛限制，报错里单独打印出来的
// 用户名与密码同样擦掉；Cookie / query 的短值照旧不擦。
func TestBrowserDownloadShortUserinfoCredentialsAreRedactedMEDIA10(t *testing.T) {
	setupVideoServiceTestDB(t)
	dir := t.TempDir()
	binary, _ := writeFakeFFmpeg(t, `echo "[https @ 0x1] HTTP error 401 Unauthorized" 1>&2
echo "login admin rejected (password admin), retry=true" 1>&2
exit 1
`)
	service := withBrowserDownloadStore(newTestDownloadService(t, dir, binary, nil))
	task, err := service.Enqueue(BrowserDownloadRequest{
		URL:    "https://admin:admin@host.example.com/hls/index.m3u8?sig=SIGNATURE9911&v=true",
		Kind:   "hls",
		Title:  "短凭证",
		Cookie: "theme=dark",
	})
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	failed := waitForState(t, service, task.ID, browserDownloadStateFailed)
	service.Wait()
	row := mustLoadBrowserDownloadRow(t, task.ID)
	for _, text := range []string{failed.Error, row.Error, row.DisplayURL} {
		for _, secret := range []string{"admin", "SIGNATURE9911"} {
			if strings.Contains(text, secret) {
				t.Fatalf("清洗结果不应含 %q：%q", secret, text)
			}
		}
	}
	if !strings.Contains(row.Error, "401") || !strings.Contains(row.Error, "retry=true") {
		t.Fatalf("短的非凭证字样照旧保留，原因仍可读：%q", row.Error)
	}
	secrets := browserDownloadSecrets(&browserDownloadNormalized{URL: "https://a:b@host/x?k=v", Headers: []string{"Cookie: c=d"}})
	for _, want := range []string{"a", "b"} {
		found := false
		for _, secret := range secrets {
			found = found || secret == want
		}
		if !found {
			t.Fatalf("单字符的用户名与密码也要擦: %v", secrets)
		}
	}
	for _, secret := range secrets {
		if secret == "v" || secret == "d" || secret == "k=v" {
			t.Fatalf("query 与 Cookie 的短值不该进待擦除表: %v", secrets)
		}
	}
}

// MEDIA-10（B-m-3）：任务完成后内存里的地址换成显示用地址——ListTasks、推送事件与合并列表都不再带出
// 签名 query 与 userinfo，与表里的 display_url 一致。
func TestBrowserDownloadDoneTaskListsDisplayURLOnlyMEDIA10(t *testing.T) {
	setupVideoServiceTestDB(t)
	dir := t.TempDir()
	binary, _ := writeFakeFFmpeg(t, fakeFFmpegSuccess)
	service := withBrowserDownloadStore(newTestDownloadService(t, dir, binary, nil))
	var emittedMu sync.Mutex
	var lastEmitted []BrowserDownloadTask
	service.SetEventEmitter(func(tasks []BrowserDownloadTask) {
		emittedMu.Lock()
		lastEmitted = tasks
		emittedMu.Unlock()
	})
	const display = "https://cdn.example.com/hls/index.m3u8"
	task, err := service.Enqueue(BrowserDownloadRequest{
		URL:   "https://admin:admin@cdn.example.com/hls/index.m3u8?sig=SIGNATURE4242#frag",
		Kind:  "hls",
		Title: "完成后的地址",
	})
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	done := waitForState(t, service, task.ID, browserDownloadStateDone)
	service.Wait()
	check := func(source string, tasks []BrowserDownloadTask) {
		t.Helper()
		found := false
		for _, item := range tasks {
			if item.ID != task.ID {
				continue
			}
			found = true
			if item.URL != display {
				t.Fatalf("%s 里的地址应为显示用地址 %q，实际 %q", source, display, item.URL)
			}
		}
		if !found {
			t.Fatalf("%s 里没有这个任务", source)
		}
	}
	if done.URL != display {
		t.Fatalf("完成时的快照地址应为显示用地址: %q", done.URL)
	}
	check("ListTasks", service.ListTasks())
	check("ListDownloadTasks", service.ListDownloadTasks())
	emittedMu.Lock()
	emitted := lastEmitted
	emittedMu.Unlock()
	check("推送事件", emitted)
	if row := mustLoadBrowserDownloadRow(t, task.ID); row.DisplayURL != display {
		t.Fatalf("表里的 display_url 不对: %q", row.DisplayURL)
	}
}

// M-8：写入终态后表里只留最近 500 条终态行（按 created_at），非终态行不裁。
func TestBrowserDownloadTableKeepsLatestTerminalRowsMEDIA10(t *testing.T) {
	setupVideoServiceTestDB(t)
	base := time.Now().Add(-24 * time.Hour)
	rows := make([]models.BrowserDownloadTask, 0, 510)
	for i := 0; i < 505; i++ {
		stamp := base.Add(time.Duration(i) * time.Second)
		rows = append(rows, models.BrowserDownloadTask{TaskUID: fmt.Sprintf("old-%03d", i), Status: browserDownloadStateDone, CreatedAt: stamp, UpdatedAt: stamp})
	}
	for i := 0; i < 3; i++ {
		rows = append(rows, models.BrowserDownloadTask{TaskUID: fmt.Sprintf("queued-%d", i), Status: browserDownloadStateQueued, CreatedAt: base.Add(-time.Hour), UpdatedAt: base})
	}
	if err := database.DB.CreateInBatches(&rows, 100).Error; err != nil {
		t.Fatal(err)
	}
	service := withBrowserDownloadStore(NewBrowserDownloadService(BrowserDownloadDeps{}))
	now := time.Now()
	service.entries["latest"] = &browserDownloadEntry{task: BrowserDownloadTask{
		ID: "latest", State: browserDownloadStateFailed, CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(), FinishedAt: now.UnixMilli(),
	}}
	service.persist("latest")

	var terminal, queued int64
	database.DB.Model(&models.BrowserDownloadTask{}).Where("status <> ?", browserDownloadStateQueued).Count(&terminal)
	database.DB.Model(&models.BrowserDownloadTask{}).Where("status = ?", browserDownloadStateQueued).Count(&queued)
	if terminal != browserDownloadPersistedTerminalLimit || queued != 3 {
		t.Fatalf("应留 %d 条终态行与全部非终态行: terminal=%d queued=%d", browserDownloadPersistedTerminalLimit, terminal, queued)
	}
	mustLoadBrowserDownloadRow(t, "latest")
	for _, gone := range []string{"old-000", "old-005"} {
		var count int64
		database.DB.Model(&models.BrowserDownloadTask{}).Where("task_uid = ?", gone).Count(&count)
		if count != 0 {
			t.Fatalf("最早的终态行 %s 应被裁掉", gone)
		}
	}
	mustLoadBrowserDownloadRow(t, "old-006")
}
