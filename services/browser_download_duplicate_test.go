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

// downloadTestTask 造一个「已完成且已入库」的下载：文件真实存在，片库里有同路径的记录。
func downloadTestTask(t *testing.T, dir, id, name string, content []byte) (BrowserDownloadTask, models.Video) {
	t.Helper()
	video := downloadTestVideo(t, dir, name, content, 1280, 720, 60)
	now := time.Now().UnixMilli()
	return BrowserDownloadTask{ID: id, Title: name, Filename: name, Directory: dir, OutputPath: video.Path, State: browserDownloadStateDone,
		VideoID: video.ID, ImportStatus: BrowserDownloadImportImported, CreatedAt: now, UpdatedAt: now}, video
}

// fakeExactChecker 像真查重一样记下查的是哪一份（片库记录与文件指纹），结论是「与 match 相同」。
func fakeExactChecker(match uint, calls *[]string) func(context.Context, string, uint) (DownloadDuplicateResult, error) {
	return func(_ context.Context, outputPath string, videoID uint) (DownloadDuplicateResult, error) {
		*calls = append(*calls, outputPath)
		info, err := os.Stat(outputPath)
		if err != nil {
			return DownloadDuplicateResult{}, errors.New("读取失败")
		}
		return DownloadDuplicateResult{Status: DownloadDuplicateFound, CheckedAt: time.Now(), VideoID: videoID,
			SourceSize: info.Size(), SourceModTimeNS: info.ModTime().UnixNano(),
			Matches: []DownloadDuplicateMatch{{VideoID: match, Kind: DownloadDuplicateExact, Relation: DownloadRelationSame}}}, nil
	}
}

// 完成后排队查重、结论写回内存并落库、查出重复时回调并立刻推送（审查 I-3）；查重失败只记 failed，
// 下载仍是完成；重启后历史里仍能看到结论。
func TestBrowserDownloadDuplicateCheckRunsAfterDoneAndPersists(t *testing.T) {
	setupVideoServiceTestDB(t)
	library, downloads := t.TempDir(), t.TempDir()
	kept := downloadTestVideo(t, library, "kept.mp4", []byte("kept"), 1920, 1080, 60)
	var calls []string
	var found []BrowserDownloadTask
	service := withBrowserDownloadStore(NewBrowserDownloadService(BrowserDownloadDeps{
		CheckDuplicates: fakeExactChecker(kept.ID, &calls),
		DuplicateFound:  func(task BrowserDownloadTask) { found = append(found, task) },
	}))
	var pushed []BrowserDownloadTask
	service.SetEventEmitter(func(tasks []BrowserDownloadTask) { pushed = tasks })
	task, _ := downloadTestTask(t, downloads, "t1", "new.mp4", []byte("kept"))
	service.entries["t1"] = &browserDownloadEntry{task: task}
	broken := BrowserDownloadTask{ID: "t2", Filename: "broken.mp4", Directory: downloads, OutputPath: filepath.Join(downloads, "broken.mp4"), State: browserDownloadStateDone, CreatedAt: task.CreatedAt}
	service.entries["t2"] = &browserDownloadEntry{task: broken}
	service.entries["t3"] = &browserDownloadEntry{task: BrowserDownloadTask{ID: "t3", State: browserDownloadStateRun, OutputPath: "/x/running.mp4"}}
	service.enqueueDuplicateCheck("t1")
	service.enqueueDuplicateCheck("t2")
	service.enqueueDuplicateCheck("t3")
	service.Wait()

	if len(calls) != 2 {
		t.Fatalf("只查已完成的任务: %v", calls)
	}
	got, ok, _, _ := service.findTask("t1")
	if !ok || got.State != browserDownloadStateDone || got.Duplicate == nil || got.Duplicate.Status != DownloadDuplicateFound || got.Duplicate.Matches[0].Title != "kept.mp4" {
		t.Fatalf("结论应写回任务并带上对方标题: %+v", got)
	}
	if len(found) != 1 || found[0].ID != "t1" {
		t.Fatalf("查出重复应回调一次: %+v", found)
	}
	var pushedT1 *BrowserDownloadTask
	for index := range pushed {
		if pushed[index].ID == "t1" {
			pushedT1 = &pushed[index]
		}
	}
	if pushedT1 == nil || pushedT1.Duplicate == nil || pushedT1.Duplicate.Status != DownloadDuplicateFound {
		t.Fatalf("结论应立刻推送给已打开的下载页，不被节流吞掉: %+v", pushedT1)
	}
	if result, ok := decodeDownloadDuplicateResult(mustLoadBrowserDownloadRow(t, "t1").DuplicateResult); !ok || result.Matches[0].VideoID != kept.ID {
		t.Fatal("结论应落库")
	}
	failed, _, _, _ := service.findTask("t2")
	if failed.State != browserDownloadStateDone || failed.Duplicate == nil || failed.Duplicate.Status != DownloadDuplicateFailed || failed.Duplicate.Error == "" {
		t.Fatalf("查重失败只记 failed，下载仍是完成: %+v", failed)
	}

	restarted := withBrowserDownloadStore(NewBrowserDownloadService(BrowserDownloadDeps{}))
	var history *BrowserDownloadTask
	for _, item := range restarted.ListDownloadTasks() {
		if item.ID == "t1" {
			item := item
			history = &item
		}
	}
	if history == nil || history.Duplicate == nil || history.Duplicate.Status != DownloadDuplicateFound || len(history.Duplicate.Matches) != 1 {
		t.Fatalf("重启后历史应保留查重结论: %+v", history)
	}
}

// 列表时按此刻的片库现算（内存里的任务也一样，审查 I-1）：对方被删后不再列出；这次下载自己被删后整条不显示。
func TestBrowserDownloadDuplicateViewsAreResolvedAtListTime(t *testing.T) {
	setupVideoServiceTestDB(t)
	library, downloads := t.TempDir(), t.TempDir()
	kept := downloadTestVideo(t, library, "kept.mp4", []byte("kept"), 1920, 1080, 60)
	var calls []string
	service := withBrowserDownloadStore(NewBrowserDownloadService(BrowserDownloadDeps{CheckDuplicates: fakeExactChecker(kept.ID, &calls)}))
	task, self := downloadTestTask(t, downloads, "t1", "new.mp4", []byte("kept"))
	service.entries["t1"] = &browserDownloadEntry{task: task}
	service.enqueueDuplicateCheck("t1")
	service.Wait()
	find := func() *BrowserDownloadTask {
		for _, item := range service.ListDownloadTasks() {
			if item.ID == "t1" {
				item := item
				return &item
			}
		}
		return nil
	}
	if listed := find(); listed == nil || listed.Duplicate == nil || len(listed.Duplicate.Matches) != 1 {
		t.Fatalf("先应列出匹配: %+v", listed)
	}
	if err := database.DB.Delete(&models.Video{}, kept.ID).Error; err != nil {
		t.Fatal(err)
	}
	if listed := find(); listed.Duplicate == nil || listed.Duplicate.Status != DownloadDuplicateNone || len(listed.Duplicate.Matches) != 0 {
		t.Fatalf("对方被删后不应再列出: %+v", listed.Duplicate)
	}
	if err := database.DB.Delete(&models.Video{}, self.ID).Error; err != nil {
		t.Fatal(err)
	}
	if listed := find(); listed.Duplicate != nil {
		t.Fatalf("这次下载自己被删后整条提示不显示: %+v", listed.Duplicate)
	}
}

// 历史任务按路径找「这次下载」：同名路径后来换成了别的文件，旧结论作废——不显示、不能据此写忽略（审查 I-2）。
func TestBrowserDownloadDuplicateIgnoresStaleIdentity(t *testing.T) {
	setupVideoServiceTestDB(t)
	library, downloads := t.TempDir(), t.TempDir()
	version := downloadTestVideo(t, library, "version.mp4", []byte("version!"), 1920, 1080, 60)
	var pairs [][2]uint
	service := withBrowserDownloadStore(NewBrowserDownloadService(BrowserDownloadDeps{
		DismissDuplicatePair: func(videoID, otherID uint) error { pairs = append(pairs, [2]uint{videoID, otherID}); return nil },
	}))
	task, self := downloadTestTask(t, downloads, "h1", "movie.mp4", []byte("original download"))
	info, _ := os.Stat(task.OutputPath)
	result := DownloadDuplicateResult{Status: DownloadDuplicateFound, VideoID: self.ID, SourceSize: info.Size(), SourceModTimeNS: info.ModTime().UnixNano(),
		Matches: []DownloadDuplicateMatch{{VideoID: version.ID, Kind: DownloadDuplicateVersion, Relation: DownloadRelationHigher}}}
	now := time.Now()
	if err := database.DB.Create(&models.BrowserDownloadTask{TaskUID: "h1", FileName: "movie.mp4", Directory: downloads, Status: browserDownloadStateDone,
		DuplicateResult: encodeDownloadDuplicateResult(result), CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	// 原来那份被删了，同一路径上来了另一部片（另一条片库记录）。
	if err := database.DB.Delete(&models.Video{}, self.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(task.OutputPath, []byte("a different film at the same path"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := models.Video{Name: "movie.mp4", Path: task.OutputPath, Directory: downloads, Size: 33}
	if err := database.DB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	for _, item := range service.ListDownloadTasks() {
		if item.ID == "h1" && item.Duplicate != nil {
			t.Fatalf("旧结论不应套到同路径的新文件上: %+v", item.Duplicate)
		}
	}
	got, err := service.DismissDownloadDuplicate("h1", version.ID)
	if err != nil || got.Code != BrowserDownloadCodeDuplicateNotFound || len(pairs) != 0 {
		t.Fatalf("过期的提示不能据此写忽略: %+v pairs=%v err=%v", got, pairs, err)
	}
}

// 「不是同一部」：其他版本写进近似重复忽略；相同文件只从提示里拿掉；「清除」任何时候都不写忽略；
// 返回的任务带着按路径现算的片库记录（审查 Minor 2）。
func TestBrowserDownloadDismissAndClearDuplicate(t *testing.T) {
	setupVideoServiceTestDB(t)
	library, downloads := t.TempDir(), t.TempDir()
	exact := downloadTestVideo(t, library, "exact.mp4", []byte("exact"), 1920, 1080, 60)
	version := downloadTestVideo(t, library, "version.mp4", []byte("version!"), 1920, 1080, 60)
	grouped := downloadTestVideo(t, library, "grouped.mp4", []byte("grouped!!"), 1920, 1080, 60)
	var pairs [][2]uint
	service := withBrowserDownloadStore(NewBrowserDownloadService(BrowserDownloadDeps{
		DismissDuplicatePair: func(videoID, otherID uint) error { pairs = append(pairs, [2]uint{videoID, otherID}); return nil },
	}))
	task, self := downloadTestTask(t, downloads, "m1", "new.mp4", []byte("new download"))
	result := DownloadDuplicateResult{Status: DownloadDuplicateFound, VideoID: self.ID, Matches: []DownloadDuplicateMatch{
		{VideoID: exact.ID, Kind: DownloadDuplicateExact, Relation: DownloadRelationSame},
		{VideoID: version.ID, Kind: DownloadDuplicateVersion, Relation: DownloadRelationHigher},
		{VideoID: grouped.ID, Kind: DownloadDuplicateVersion, Relation: DownloadRelationLower},
	}}
	task.duplicateResult = encodeDownloadDuplicateResult(result)
	service.entries["m1"] = &browserDownloadEntry{task: task}

	got, err := service.DismissDownloadDuplicate("m1", exact.ID)
	if err != nil || got.Code != BrowserDownloadCodeOK || len(pairs) != 0 || len(got.Task.Duplicate.Matches) != 2 {
		t.Fatalf("相同文件只从提示里拿掉、不写忽略: %+v pairs=%v err=%v", got, pairs, err)
	}
	got, err = service.ClearDownloadDuplicate("m1", grouped.ID)
	if err != nil || got.Code != BrowserDownloadCodeOK || len(pairs) != 0 || len(got.Task.Duplicate.Matches) != 1 {
		t.Fatalf("清除不写忽略: %+v pairs=%v err=%v", got, pairs, err)
	}
	got, err = service.DismissDownloadDuplicate("m1", version.ID)
	if err != nil || got.Code != BrowserDownloadCodeOK || len(pairs) != 1 || pairs[0] != [2]uint{self.ID, version.ID} || got.Task.Duplicate.Status != DownloadDuplicateNone {
		t.Fatalf("其他版本应写忽略、拿光后状态为 none: %+v pairs=%v err=%v", got, pairs, err)
	}
	if again, _ := service.DismissDownloadDuplicate("m1", version.ID); again.Code != BrowserDownloadCodeDuplicateNotFound {
		t.Fatalf("已处理过的再点应提示没有: %+v", again)
	}

	// 已入库的历史任务（不在内存里）：片库记录按路径现算，照样写忽略，返回的任务也带着记录。
	historyTask, historySelf := downloadTestTask(t, downloads, "h2", "imported.mp4", []byte("imported download"))
	historyResult := DownloadDuplicateResult{Status: DownloadDuplicateFound, VideoID: historySelf.ID, Matches: []DownloadDuplicateMatch{{VideoID: version.ID, Kind: DownloadDuplicateVersion}}}
	now := time.Now()
	if err := database.DB.Create(&models.BrowserDownloadTask{TaskUID: "h2", FileName: historyTask.Filename, Directory: downloads, Status: browserDownloadStateDone,
		DuplicateResult: encodeDownloadDuplicateResult(historyResult), CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	got, err = service.DismissDownloadDuplicate("h2", version.ID)
	if err != nil || got.Code != BrowserDownloadCodeOK || len(pairs) != 2 || pairs[1] != [2]uint{historySelf.ID, version.ID} || got.Task.VideoID != historySelf.ID {
		t.Fatalf("已入库的历史任务应写忽略并带回片库记录: %+v pairs=%v err=%v", got, pairs, err)
	}
	if stored, ok := decodeDownloadDuplicateResult(mustLoadBrowserDownloadRow(t, "h2").DuplicateResult); !ok || len(stored.Matches) != 0 {
		t.Fatal("历史任务应直接改表")
	}
	if missing, _ := service.DismissDownloadDuplicate("nope", exact.ID); missing.Code != BrowserDownloadCodeTaskNotFound {
		t.Fatalf("不存在的任务: %+v", missing)
	}
}

// 退出时取消查重：卡在查重里的任务不会让 Wait 一直等，结论也不写（审查 I-5）；停止之后不再接受新的查重。
func TestBrowserDownloadStopDuplicateChecksCancelsQueue(t *testing.T) {
	setupVideoServiceTestDB(t)
	started := make(chan struct{}, 1)
	service := withBrowserDownloadStore(NewBrowserDownloadService(BrowserDownloadDeps{
		CheckDuplicates: func(ctx context.Context, _ string, _ uint) (DownloadDuplicateResult, error) {
			started <- struct{}{}
			<-ctx.Done()
			return DownloadDuplicateResult{}, ctx.Err()
		},
	}))
	task, _ := downloadTestTask(t, t.TempDir(), "t1", "a.mp4", []byte("a"))
	service.entries["t1"] = &browserDownloadEntry{task: task}
	service.enqueueDuplicateCheck("t1")
	<-started
	done := make(chan struct{})
	go func() {
		service.StopDuplicateChecks()
		service.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("停止查重后 Wait 应很快返回")
	}
	if got, _, _, _ := service.findTask("t1"); got.duplicateResult != "" || got.Duplicate != nil {
		t.Fatalf("取消的查重不写结论: %q", got.duplicateResult)
	}
	service.enqueueDuplicateCheck("t1")
	service.Wait()
}

// 端到端：一次真正走完下载流水线（替身 ffmpeg）的任务，落到「完成」之后才查重，查到的是最终落盘的文件
// 与入库得到的视频 ID；重新入库之后再查一次（审查 Minor 1）。
func TestBrowserDownloadPipelineQueuesDuplicateCheckAfterDone(t *testing.T) {
	dir := t.TempDir()
	binary, _ := writeFakeFFmpeg(t, fakeFFmpegSuccess)
	var checked []string
	var checkedIDs []uint
	var stateAtCheck string
	var service *BrowserDownloadService
	service = NewBrowserDownloadService(BrowserDownloadDeps{
		Settings: func() (BrowserDownloadSettings, error) {
			return BrowserDownloadSettings{Directory: dir, Concurrency: 1}, nil
		},
		ImportDirectory: func(string, string) (uint, error) { return 42, nil },
		FFmpegPath:      func() (string, error) { return binary, nil },
		CheckDuplicates: func(_ context.Context, outputPath string, videoID uint) (DownloadDuplicateResult, error) {
			for _, task := range service.ListTasks() {
				stateAtCheck = task.State
			}
			checked = append(checked, outputPath)
			checkedIDs = append(checkedIDs, videoID)
			return DownloadDuplicateResult{Status: DownloadDuplicateNone, Matches: []DownloadDuplicateMatch{}, VideoID: videoID}, nil
		},
	})
	service.Start(context.Background())
	task, err := service.Enqueue(BrowserDownloadRequest{URL: "https://cdn/a.m3u8", Kind: "hls", Title: "片子"})
	if err != nil {
		t.Fatal(err)
	}
	done := waitForState(t, service, task.ID, browserDownloadStateDone)
	service.Wait()
	if len(checked) != 1 || checked[0] != done.OutputPath || checkedIDs[0] != 42 || stateAtCheck != browserDownloadStateDone {
		t.Fatalf("应在完成后查一次最终落盘的文件: checked=%v ids=%v state=%s output=%s", checked, checkedIDs, stateAtCheck, done.OutputPath)
	}
	if result, err := service.ReimportDownload(task.ID); err != nil || result.Code != BrowserDownloadCodeOK {
		t.Fatalf("重新入库: %+v %v", result, err)
	}
	service.Wait()
	if len(checked) != 2 {
		t.Fatalf("重新入库后应再查一次: %v", checked)
	}
}

// 文件没变、但片库记录换了（例如被删后重新扫描入库成了新记录）：旧结论同样作废。
func TestBrowserDownloadDuplicateStaleWhenLibraryRecordChanges(t *testing.T) {
	setupVideoServiceTestDB(t)
	library, downloads := t.TempDir(), t.TempDir()
	version := downloadTestVideo(t, library, "version.mp4", []byte("version!"), 1920, 1080, 60)
	var pairs [][2]uint
	service := withBrowserDownloadStore(NewBrowserDownloadService(BrowserDownloadDeps{
		DismissDuplicatePair: func(videoID, otherID uint) error { pairs = append(pairs, [2]uint{videoID, otherID}); return nil },
	}))
	task, self := downloadTestTask(t, downloads, "h1", "movie.mp4", []byte("same file all along"))
	info, _ := os.Stat(task.OutputPath)
	result := DownloadDuplicateResult{Status: DownloadDuplicateFound, VideoID: self.ID, SourceSize: info.Size(), SourceModTimeNS: info.ModTime().UnixNano(),
		Matches: []DownloadDuplicateMatch{{VideoID: version.ID, Kind: DownloadDuplicateVersion}}}
	now := time.Now()
	if err := database.DB.Create(&models.BrowserDownloadTask{TaskUID: "h1", FileName: "movie.mp4", Directory: downloads, Status: browserDownloadStateDone,
		DuplicateResult: encodeDownloadDuplicateResult(result), CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&models.Video{}, self.ID).Error; err != nil {
		t.Fatal(err)
	}
	reimported := models.Video{Name: "movie.mp4", Path: task.OutputPath, Directory: downloads, Size: info.Size()}
	if err := database.DB.Create(&reimported).Error; err != nil {
		t.Fatal(err)
	}
	for _, item := range service.ListDownloadTasks() {
		if item.ID == "h1" && item.Duplicate != nil {
			t.Fatalf("片库记录换了，旧结论不应再显示: %+v", item.Duplicate)
		}
	}
	if got, _ := service.DismissDownloadDuplicate("h1", version.ID); got.Code != BrowserDownloadCodeDuplicateNotFound || len(pairs) != 0 {
		t.Fatalf("片库记录换了，不能据旧结论写忽略: %+v pairs=%v", got, pairs)
	}
}

// 片库记录没变、但文件原地换了内容（大小或修改时间变了）：旧结论也作废，不能据此删除或写忽略。
func TestBrowserDownloadDuplicateStaleWhenFileReplacedInPlace(t *testing.T) {
	setupVideoServiceTestDB(t)
	library, downloads := t.TempDir(), t.TempDir()
	version := downloadTestVideo(t, library, "version.mp4", []byte("version!"), 1920, 1080, 60)
	var pairs [][2]uint
	service := withBrowserDownloadStore(NewBrowserDownloadService(BrowserDownloadDeps{
		DismissDuplicatePair: func(videoID, otherID uint) error { pairs = append(pairs, [2]uint{videoID, otherID}); return nil },
	}))
	task, self := downloadTestTask(t, downloads, "m1", "movie.mp4", []byte("checked content"))
	info, _ := os.Stat(task.OutputPath)
	task.duplicateResult = encodeDownloadDuplicateResult(DownloadDuplicateResult{Status: DownloadDuplicateFound, VideoID: self.ID,
		SourceSize: info.Size(), SourceModTimeNS: info.ModTime().UnixNano(),
		Matches: []DownloadDuplicateMatch{{VideoID: version.ID, Kind: DownloadDuplicateVersion}}})
	service.entries["m1"] = &browserDownloadEntry{task: task}
	if err := os.WriteFile(task.OutputPath, []byte("re-encoded, different content"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, item := range service.ListDownloadTasks() {
		if item.ID == "m1" && item.Duplicate != nil {
			t.Fatalf("文件换过，旧结论不应再显示: %+v", item.Duplicate)
		}
	}
	if got, _ := service.DismissDownloadDuplicate("m1", version.ID); got.Code != BrowserDownloadCodeDuplicateNotFound || len(pairs) != 0 {
		t.Fatalf("文件换过，不能据旧结论写忽略: %+v pairs=%v", got, pairs)
	}
}
