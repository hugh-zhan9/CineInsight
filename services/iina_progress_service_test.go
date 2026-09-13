package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

func writeIINAEntry(t *testing.T, dir, videoPath, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, iinaWatchLaterName(videoPath)), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 断点文件名就是路径原文的 MD5 大写十六进制——这是在真机上比对 IINA 现有记录
// 验证出来的口径，换成 file:// URL 或小写都对不上。
func TestIINAWatchLaterNameMatchesMpvRule(t *testing.T) {
	if got := iinaWatchLaterName("/Volumes/think plus/Ellieli-Collection/V/41.mp4"); got != "466BFA37A6F5CF75CD047203E5A36B8F" {
		t.Fatalf("断点文件名算错了: %s", got)
	}
}

func TestParseIINAStartSeconds(t *testing.T) {
	cases := []struct {
		content string
		want    float64
		ok      bool
	}{
		{"start=2374.748677\nvolume=95.179688\n", 2374.748677, true},
		{"start=388.960000\n", 388.96, true},
		{"# redirect entry\n", 0, false},
		{"volume=100.000000\n", 0, false},
		{"start=abc\n", 0, false},
	}
	for _, item := range cases {
		got, ok := parseIINAStartSeconds(item.content)
		if ok != item.ok || (ok && got != item.want) {
			t.Fatalf("解析 %q 得到 (%v,%v)，期望 (%v,%v)", item.content, got, ok, item.want, item.ok)
		}
	}
}

func TestIINAProgressSyncWritesPositionsForwardOnly(t *testing.T) {
	setupVideoServiceTestDB(t)
	home := t.TempDir()
	dir := filepath.Join(home, iinaWatchLaterRelativeDir)

	videos := []models.Video{
		{Name: "fresh.mp4", Path: "/media/fresh.mp4", Directory: "/media", Duration: 3600},
		{Name: "ahead.mp4", Path: "/media/ahead.mp4", Directory: "/media", Duration: 3600, WatchPositionSeconds: 1200},
		{Name: "untouched.mp4", Path: "/media/untouched.mp4", Directory: "/media", Duration: 3600},
		{Name: "overrun.mp4", Path: "/media/overrun.mp4", Directory: "/media", Duration: 100},
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatal(err)
	}
	writeIINAEntry(t, dir, "/media/fresh.mp4", "start=615.5\nvolume=100.0\n")
	// 应用内已经看到 1200 秒，IINA 里只到 300：不该把进度往回拽
	writeIINAEntry(t, dir, "/media/ahead.mp4", "start=300.0\n")
	// 没有 start 的条目直接跳过
	writeIINAEntry(t, dir, "/media/untouched.mp4", "# redirect entry\n")
	// 超过时长的断点要夹到时长
	writeIINAEntry(t, dir, "/media/overrun.mp4", "start=999.0\n")

	service := NewIINAProgressService(home)
	result, err := service.Sync()
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if result.Updated != 2 {
		t.Fatalf("应当只更新两条: %+v", result)
	}

	var refreshed []models.Video
	if err := database.DB.Order("id ASC").Find(&refreshed).Error; err != nil {
		t.Fatal(err)
	}
	if refreshed[0].WatchPositionSeconds != 615.5 {
		t.Fatalf("新进度没写进去: %v", refreshed[0].WatchPositionSeconds)
	}
	if refreshed[1].WatchPositionSeconds != 1200 {
		t.Fatalf("进度被往回拽了: %v", refreshed[1].WatchPositionSeconds)
	}
	if refreshed[2].WatchPositionSeconds != 0 {
		t.Fatalf("没有 start 的条目不该改动: %v", refreshed[2].WatchPositionSeconds)
	}
	// 2026-09-13 裁决：断点夹到片尾就是看完了，标已看并清掉断点（下次从头播）。
	if !refreshed[3].IsWatched || refreshed[3].WatchPositionSeconds != 0 {
		t.Fatalf("停在片尾的断点应当判为看完并清零: %+v", refreshed[3])
	}
	// 断点"消失"仍不据此判已看：可能是用户自己清了 IINA 的记录。
	for _, video := range refreshed[:3] {
		if video.IsWatched {
			t.Fatalf("看到中途不应当自动标记已看: %s", video.Name)
		}
	}
}

// 桌面「播放」按钮走的就是 IINA，这条路要是不判，用户看到的还是
// 「看到 00:28 / 00:28」却没标已看。
func TestIINAProgressSyncMarksWatchedAtEndAndSkipsWatchedVideos(t *testing.T) {
	setupVideoServiceTestDB(t)
	home := t.TempDir()
	dir := filepath.Join(home, iinaWatchLaterRelativeDir)

	videos := []models.Video{
		{Name: "ended.mp4", Path: "/media/ended.mp4", Directory: "/media", Duration: 28},
		{Name: "midway.mp4", Path: "/media/midway.mp4", Directory: "/media", Duration: 28},
		{Name: "already.mp4", Path: "/media/already.mp4", Directory: "/media", Duration: 28, IsWatched: true},
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatal(err)
	}
	writeIINAEntry(t, dir, "/media/ended.mp4", "start=27.6\n")
	writeIINAEntry(t, dir, "/media/midway.mp4", "start=14.0\n")
	// 已看的片子还留着一份陈旧断点：位置被清零之后，「只前进不后退」挡不住它。
	writeIINAEntry(t, dir, "/media/already.mp4", "start=14.0\n")

	result, err := NewIINAProgressService(home).Sync()
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if result.Updated != 2 || result.Skipped != 1 {
		t.Fatalf("已看的应当被跳过: %+v", result)
	}

	var refreshed []models.Video
	if err := database.DB.Order("id ASC").Find(&refreshed).Error; err != nil {
		t.Fatal(err)
	}
	if !refreshed[0].IsWatched || refreshed[0].WatchPositionSeconds != 0 || refreshed[0].WatchedAt == nil {
		t.Fatalf("停在片尾应判看完并清零: %+v", refreshed[0])
	}
	if refreshed[1].IsWatched || refreshed[1].WatchPositionSeconds != 14 {
		t.Fatalf("看到一半应当照旧记断点: %+v", refreshed[1])
	}
	if refreshed[2].WatchPositionSeconds != 0 {
		t.Fatalf("已看的不该被陈旧断点拉回「在看」: %+v", refreshed[2])
	}
	// 回报给前端的位置要和落库的一致，否则列表会先显示成还在看。
	for _, change := range result.Changes {
		if change.VideoID == refreshed[0].ID && change.WatchPositionSeconds != 0 {
			t.Fatalf("判为看完后回报的位置应当是 0: %+v", change)
		}
	}
}

func TestIINAProgressSyncFailsWhenIINAAbsent(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := NewIINAProgressService(t.TempDir())

	if service.Available() {
		t.Fatal("没有断点目录时不该报告可用")
	}
	if _, err := service.Sync(); err == nil {
		t.Fatal("找不到 IINA 时应当明确报错，而不是静默什么都不做")
	}
}

// 监听断点目录：IINA 退出播放时写文件，事件一到就该同步，不必等下次启动应用。
func TestIINAProgressWatchSyncsWhenIINAWritesAResumePoint(t *testing.T) {
	setupVideoServiceTestDB(t)
	home := t.TempDir()
	dir := filepath.Join(home, iinaWatchLaterRelativeDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "live.mp4", Path: "/media/live.mp4", Directory: "/media", Duration: 3600}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}

	service := NewIINAProgressService(home)
	synced := make(chan IINAProgressSyncResult, 1)
	service.SetOnSynced(func(result IINAProgressSyncResult) { synced <- result })
	if err := service.StartWatching(); err != nil {
		t.Fatalf("启动监听失败: %v", err)
	}
	defer service.StopWatching()

	writeIINAEntry(t, dir, "/media/live.mp4", "start=777.5\n")

	select {
	case result := <-synced:
		if result.Updated != 1 {
			t.Fatalf("应当更新一条: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("写入断点后没有触发同步")
	}

	var refreshed models.Video
	if err := database.DB.First(&refreshed, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refreshed.WatchPositionSeconds != 777.5 {
		t.Fatalf("进度没写进去: %v", refreshed.WatchPositionSeconds)
	}
}

func TestIINAProgressStopWatchingIsIdempotent(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, iinaWatchLaterRelativeDir), 0o755); err != nil {
		t.Fatal(err)
	}
	service := NewIINAProgressService(home)
	if err := service.StartWatching(); err != nil {
		t.Fatalf("启动监听失败: %v", err)
	}
	// 重复启动不该再开一个 watcher，重复停止也不该 panic
	if err := service.StartWatching(); err != nil {
		t.Fatalf("重复启动应当无害: %v", err)
	}
	service.StopWatching()
	service.StopWatching()
}

// 完成判定必须排在抗抖动守卫之前，否则两者同为 1 秒会形成死区：
// 既吞掉正常的完成，也让历史遗留行在 IINA 这条主力路径上永远不自愈
// ——而「不回填」的裁决正是建立在"再播一次自然就好"上的。
func TestIINAProgressSyncCompletionBeatsForwardOnlyGuard(t *testing.T) {
	setupVideoServiceTestDB(t)
	home := t.TempDir()
	dir := filepath.Join(home, iinaWatchLaterRelativeDir)

	videos := []models.Video{
		// 历史遗留行：位置已顶到片尾，但没标已看。seconds 被夹到 28，
		// 恒满足 28 <= 28+1，判定排在守卫之后就永远跳过。
		{Name: "legacy.mp4", Path: "/media/legacy.mp4", Directory: "/media", Duration: 28, WatchPositionSeconds: 28},
		// 死区：库里存 26.6，这次到 27.3，27.3 <= 27.6 会被守卫吞掉。
		{Name: "deadzone.mp4", Path: "/media/deadzone.mp4", Directory: "/media", Duration: 28, WatchPositionSeconds: 26.6},
		// 正常的往回拽仍要被守卫挡住。
		{Name: "backwards.mp4", Path: "/media/backwards.mp4", Directory: "/media", Duration: 3600, WatchPositionSeconds: 1200},
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatal(err)
	}
	writeIINAEntry(t, dir, "/media/legacy.mp4", "start=27.9\n")
	writeIINAEntry(t, dir, "/media/deadzone.mp4", "start=27.3\n")
	writeIINAEntry(t, dir, "/media/backwards.mp4", "start=300.0\n")

	if _, err := NewIINAProgressService(home).Sync(); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	var refreshed []models.Video
	if err := database.DB.Order("id ASC").Find(&refreshed).Error; err != nil {
		t.Fatal(err)
	}
	if !refreshed[0].IsWatched || refreshed[0].WatchPositionSeconds != 0 {
		t.Fatalf("历史遗留行应当在重播后自愈: %+v", refreshed[0])
	}
	if !refreshed[1].IsWatched || refreshed[1].WatchPositionSeconds != 0 {
		t.Fatalf("死区内的完成不该被抗抖动守卫吞掉: %+v", refreshed[1])
	}
	if refreshed[2].IsWatched || refreshed[2].WatchPositionSeconds != 1200 {
		t.Fatalf("往回拽的断点仍要被挡住: %+v", refreshed[2])
	}
}
