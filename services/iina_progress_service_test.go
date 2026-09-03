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
	if refreshed[3].WatchPositionSeconds != 100 {
		t.Fatalf("超过时长的断点应当夹到时长: %v", refreshed[3].WatchPositionSeconds)
	}
	// 同步不碰"已看"：断点消失既可能是看完，也可能是用户清了记录，不替他判定
	for _, video := range refreshed {
		if video.IsWatched {
			t.Fatalf("同步不应当自动标记已看: %s", video.Name)
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
