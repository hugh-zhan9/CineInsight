package services

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// seedReadyProxy 直接放一份 ready 代理（文件 + 表行），跳过编码。
// 返回产物路径。lastUsed 决定它在 LRU 里的位置。
func seedReadyProxy(t *testing.T, service *PlaybackProxyService, video models.Video, size int64, lastUsed time.Time) string {
	t.Helper()
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatalf("读取源文件失败: %v", err)
	}
	fingerprint := playbackProxyFingerprintOf(info)
	if err := os.MkdirAll(service.Dir(), 0o755); err != nil {
		t.Fatalf("创建代理目录失败: %v", err)
	}
	path := service.proxyPath(video.ID, fingerprint)
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), int(size)), 0o600); err != nil {
		t.Fatalf("写入代理产物失败: %v", err)
	}
	row := models.VideoPlaybackProxy{
		VideoID:         video.ID,
		SourceSize:      fingerprint.size,
		SourceModTimeNS: fingerprint.modTimeNS,
		Strategy:        models.PlaybackProxyStrategyRemux,
		Status:          models.PlaybackProxyStatusReady,
		OutputSize:      size,
		LastUsedAt:      lastUsed,
	}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatalf("写入代理记录失败: %v", err)
	}
	return path
}

func newProxyBackedVideoService(service *PlaybackProxyService) *VideoService {
	videoService := &VideoService{}
	videoService.SetPlaybackProxyService(service)
	return videoService
}

// ===== D-002 指纹失效 =====

// 源指纹变了：命中前的校验把文件与表行一起删掉，本次请求退回"无代理"行为。
func TestPlaybackProxyInvalidatedWhenSourceFingerprintChanges(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	videoService := newProxyBackedVideoService(service)
	video := createProxyTestVideo(t, "stale.mkv", 10)
	proxyPath := seedReadyProxy(t, service, video, 2048, time.Now())

	// 先确认有代理时确实走内嵌。
	session, err := videoService.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	if session.Mode != "inline" || session.Proxy == nil {
		t.Fatalf("有代理时应当内嵌: %#v", session)
	}

	// 源被替换：指纹对不上。
	if err := os.WriteFile(video.Path, []byte("new-content"), 0o600); err != nil {
		t.Fatalf("替换源文件失败: %v", err)
	}
	mustSetFileModTime(t, video.Path, time.Now().Add(3*time.Hour))

	session, err = videoService.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	if session.Mode != "external-preview" || session.Proxy != nil {
		t.Fatalf("指纹失效后应退回外部预览: %#v", session)
	}
	if _, err := os.Stat(proxyPath); !os.IsNotExist(err) {
		t.Fatalf("失效代理文件应被删除: %v", err)
	}
	row, err := loadProxyRow(video.ID)
	if err != nil {
		t.Fatalf("读取代理记录失败: %v", err)
	}
	if row != nil {
		t.Fatalf("失效代理记录应被删除: %#v", row)
	}
	// 源文件本身一个字节都不许动。
	content, err := os.ReadFile(video.Path)
	if err != nil || string(content) != "new-content" {
		t.Fatalf("源文件不该被碰: %q err=%v", content, err)
	}
}

// 产物被外部删掉（回退旧版本、手工清理）：表行是孤儿，命中时一起收掉。
func TestPlaybackProxyDropsRowWhenFileMissing(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	videoService := newProxyBackedVideoService(service)
	video := createProxyTestVideo(t, "orphan.mkv", 10)
	proxyPath := seedReadyProxy(t, service, video, 1024, time.Now())
	if err := os.Remove(proxyPath); err != nil {
		t.Fatalf("删除代理产物失败: %v", err)
	}

	session, err := videoService.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	if session.Mode != "external-preview" {
		t.Fatalf("产物缺失应退回外部预览: %#v", session)
	}
	row, err := loadProxyRow(video.ID)
	if err != nil {
		t.Fatalf("读取代理记录失败: %v", err)
	}
	if row != nil {
		t.Fatalf("孤儿代理记录应被删除: %#v", row)
	}
}

// failed 行不算有效代理：预览不换源。
func TestPlaybackProxyFailedRowIsNotConsumed(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	videoService := newProxyBackedVideoService(service)
	video := createProxyTestVideo(t, "failedrow.mkv", 10)
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatalf("读取源文件失败: %v", err)
	}
	fingerprint := playbackProxyFingerprintOf(info)
	row := models.VideoPlaybackProxy{
		VideoID: video.ID, SourceSize: fingerprint.size, SourceModTimeNS: fingerprint.modTimeNS,
		Strategy: models.PlaybackProxyStrategyTranscode, Status: models.PlaybackProxyStatusFailed,
		LastUsedAt: time.Now(), LastError: "encode failed",
	}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatalf("写入失败记录失败: %v", err)
	}

	session, err := videoService.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	if session.Mode != "external-preview" || session.Proxy != nil {
		t.Fatalf("failed 行不该被当成代理: %#v", session)
	}
}

// ===== D-002 删除级联 =====

// 软删除（进回收站）也要连带删代理：回收站里的视频不需要代理。
func TestPlaybackProxyDeletedWithSoftDeletedVideo(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	videoService := newProxyBackedVideoService(service)
	video := createProxyTestVideo(t, "softdel.mkv", 10)
	proxyPath := seedReadyProxy(t, service, video, 1024, time.Now())

	if err := videoService.DeleteVideo(video.ID, false); err != nil {
		t.Fatalf("软删除视频失败: %v", err)
	}
	if _, err := os.Stat(proxyPath); !os.IsNotExist(err) {
		t.Fatalf("软删除后代理文件应被删除: %v", err)
	}
	row, err := loadProxyRow(video.ID)
	if err != nil {
		t.Fatalf("读取代理记录失败: %v", err)
	}
	if row != nil {
		t.Fatalf("软删除后代理记录应被删除: %#v", row)
	}
	// 视频本身仍在回收站里（软删除），源文件也还在。
	var trashed models.Video
	if err := database.DB.Unscoped().First(&trashed, video.ID).Error; err != nil {
		t.Fatalf("回收站里应当还有这条视频: %v", err)
	}
	if !trashed.DeletedAt.IsValid() {
		t.Fatalf("视频应处于软删除状态")
	}
	if _, err := os.Stat(video.Path); err != nil {
		t.Fatalf("源文件不该被碰: %v", err)
	}
}

// 永久删除：数据库层的 CASCADE 兜底删行；文件在软删除那一步就已经删掉了。
func TestPlaybackProxyRowCascadesOnPermanentDelete(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	video := createProxyTestVideo(t, "harddel.mkv", 10)
	seedReadyProxy(t, service, video, 1024, time.Now())

	if err := database.DB.Unscoped().Delete(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("永久删除视频失败: %v", err)
	}
	var count int64
	if err := database.DB.Model(&models.VideoPlaybackProxy{}).Where("video_id = ?", video.ID).Count(&count).Error; err != nil {
		t.Fatalf("统计代理记录失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("永久删除后代理记录应被级联删除: %d", count)
	}
}

// 按视频删除只删这一个视频的代理，别人的一份不动。
func TestDeletePlaybackProxyOnlyTouchesOneVideo(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	target := createProxyTestVideo(t, "target.mkv", 10)
	other := createProxyTestVideo(t, "other.mkv", 10)
	targetPath := seedReadyProxy(t, service, target, 1024, time.Now())
	otherPath := seedReadyProxy(t, service, other, 1024, time.Now())

	if err := service.DeletePlaybackProxy(target.ID); err != nil {
		t.Fatalf("删除代理失败: %v", err)
	}
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Fatalf("目标代理应被删除: %v", err)
	}
	if _, err := os.Stat(otherPath); err != nil {
		t.Fatalf("其他视频的代理不该被碰: %v", err)
	}
	// 没有代理时删除是空操作，不报错。
	if err := service.DeletePlaybackProxy(target.ID); err != nil {
		t.Fatalf("重复删除应当空转: %v", err)
	}
}

// 清空全部：表清空、目录里的产物删净，源文件一个不碰。
func TestClearPlaybackProxiesEmptiesDirectoryAndTable(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	first := createProxyTestVideo(t, "c1.mkv", 10)
	second := createProxyTestVideo(t, "c2.mkv", 10)
	seedReadyProxy(t, service, first, 1024, time.Now())
	seedReadyProxy(t, service, second, 2048, time.Now())

	usage, err := service.ClearPlaybackProxies()
	if err != nil {
		t.Fatalf("清空代理失败: %v", err)
	}
	if usage.Count != 0 || usage.TotalBytes != 0 {
		t.Fatalf("清空后占用应为零: %#v", usage)
	}
	entries, err := os.ReadDir(service.Dir())
	if err != nil {
		t.Fatalf("读取代理目录失败: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Fatalf("清空后目录不该有文件: %s", entry.Name())
		}
	}
	var count int64
	if err := database.DB.Model(&models.VideoPlaybackProxy{}).Count(&count).Error; err != nil {
		t.Fatalf("统计代理记录失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("清空后表应为空: %d", count)
	}
	for _, video := range []models.Video{first, second} {
		if _, err := os.Stat(video.Path); err != nil {
			t.Fatalf("源文件不该被碰: %v", err)
		}
	}
}

// ===== D-005 LRU =====

// 调低上限之后的下一次整理：按 last_used_at 从旧到新淘汰，跳过 60 秒内用过的那份。
func TestPlaybackProxyLRUEvictsOldestAndSkipsRecentlyUsed(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	now := time.Now()
	service.now = func() time.Time { return now }

	oldest := createProxyTestVideo(t, "oldest.mkv", 10)
	middle := createProxyTestVideo(t, "middle.mkv", 10)
	fresh := createProxyTestVideo(t, "fresh.mkv", 10)
	oldestPath := seedReadyProxy(t, service, oldest, 1000, now.Add(-3*time.Hour))
	middlePath := seedReadyProxy(t, service, middle, 1000, now.Add(-2*time.Hour))
	// 10 秒前刚用过：落在 60 秒保护窗口里，哪怕它最"该"被淘汰也不动。
	freshPath := seedReadyProxy(t, service, fresh, 1000, now.Add(-10*time.Second))

	// 上限压到 1000：只留得下一份，但保护窗口里的那份不能删。
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("proxy_cache_limit_bytes", 1000).Error; err != nil {
		t.Fatalf("设置上限失败: %v", err)
	}

	usage, err := service.EnforcePlaybackProxyLimit()
	if err != nil {
		t.Fatalf("整理失败: %v", err)
	}
	if _, err := os.Stat(oldestPath); !os.IsNotExist(err) {
		t.Fatalf("最早使用的代理应被淘汰: %v", err)
	}
	if _, err := os.Stat(middlePath); !os.IsNotExist(err) {
		t.Fatalf("次早使用的代理应被淘汰: %v", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Fatalf("60 秒内用过的代理不该被淘汰: %v", err)
	}
	if usage.Count != 1 || usage.TotalBytes != 1000 {
		t.Fatalf("整理后应只剩一份: %#v", usage)
	}
	// 源文件全都不许动。
	for _, video := range []models.Video{oldest, middle, fresh} {
		if _, err := os.Stat(video.Path); err != nil {
			t.Fatalf("淘汰不该触碰源文件: %v", err)
		}
	}
}

// 保护窗口把所有候选都挡住时就停手，不为了达标去删刚用过的那份。
func TestPlaybackProxyLRUStopsWhenAllCandidatesProtected(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	now := time.Now()
	service.now = func() time.Time { return now }
	video := createProxyTestVideo(t, "protected.mkv", 10)
	path := seedReadyProxy(t, service, video, 5000, now.Add(-5*time.Second))
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("proxy_cache_limit_bytes", 1).Error; err != nil {
		t.Fatalf("设置上限失败: %v", err)
	}

	usage, err := service.EnforcePlaybackProxyLimit()
	if err != nil {
		t.Fatalf("整理失败: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("保护窗口内的代理不该被淘汰: %v", err)
	}
	if usage.TotalBytes != 5000 {
		t.Fatalf("占用应保持不变: %#v", usage)
	}
}

// 上限 0 = 不限：多少都不淘汰。
func TestPlaybackProxyLRUDoesNothingWhenUnlimited(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	video := createProxyTestVideo(t, "unlimited.mkv", 10)
	path := seedReadyProxy(t, service, video, 9999, time.Now().Add(-time.Hour))
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("proxy_cache_limit_bytes", 0).Error; err != nil {
		t.Fatalf("设置不限失败: %v", err)
	}

	usage, err := service.EnforcePlaybackProxyLimit()
	if err != nil {
		t.Fatalf("整理失败: %v", err)
	}
	if usage.LimitBytes != 0 {
		t.Fatalf("上限应回报为 0（不限）: %#v", usage)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("不限时不该淘汰: %v", err)
	}
}

// 成功写入之后自动整理一次：新产物落地即触发淘汰（D-005）。
func TestPlaybackProxyEnforcesLimitAfterSuccessfulWrite(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	stub.payload = bytes.Repeat([]byte("n"), 500)
	old := createProxyTestVideo(t, "toevict.mkv", 10)
	oldPath := seedReadyProxy(t, service, old, 4000, time.Now().Add(-2*time.Hour))
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("proxy_cache_limit_bytes", 1000).Error; err != nil {
		t.Fatalf("设置上限失败: %v", err)
	}

	fresh := createProxyTestVideo(t, "brandnew.mkv", 10)
	writeProxySnapshot(t, fresh, "h264", "aac", 0, 0)
	if result := runProxyOnce(t, service, fresh.ID); result.Code != PlaybackProxyCodeCreated {
		t.Fatalf("应当生成成功: %#v", result)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("写入成功后应淘汰最早使用的代理: %v", err)
	}
	usage, err := service.GetPlaybackProxyUsage()
	if err != nil {
		t.Fatalf("统计占用失败: %v", err)
	}
	if usage.Count != 1 || usage.TotalBytes != 500 {
		t.Fatalf("整理后应只剩新产物: %#v", usage)
	}
}

// last_used_at 节流：60 秒内的重复访问不再 UPDATE，过了窗口才写。
func TestPlaybackProxyLastUsedThrottle(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	videoService := newProxyBackedVideoService(service)
	base := time.Now()
	current := base
	service.now = func() time.Time { return current }

	video := createProxyTestVideo(t, "throttled.mkv", 10)
	seedReadyProxy(t, service, video, 1024, base.Add(-time.Hour))

	if _, err := videoService.GetPreviewSession(video.ID); err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	first := mustLoadProxyRow(t, video.ID).LastUsedAt
	if first.Before(base.Add(-time.Minute)) {
		t.Fatalf("首次访问应当刷新 last_used_at: %v", first)
	}

	// 30 秒后再访问一次：还在节流窗口里，值不动。
	current = base.Add(30 * time.Second)
	if _, err := videoService.GetPreviewSession(video.ID); err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	second := mustLoadProxyRow(t, video.ID).LastUsedAt
	if !second.Equal(first) {
		t.Fatalf("60 秒内不该重复写 last_used_at: %v -> %v", first, second)
	}

	// 过了 60 秒窗口再访问：这次要写进去。
	current = base.Add(90 * time.Second)
	if _, err := videoService.GetPreviewSession(video.ID); err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	third := mustLoadProxyRow(t, video.ID).LastUsedAt
	if !third.After(second) {
		t.Fatalf("超过节流窗口应当刷新 last_used_at: %v -> %v", second, third)
	}
}

// ===== D-004 消费与不变行为 =====

// 有代理时预览走内嵌，locator 仍是 /preview/media/{id}，字节由 ResolvePreviewMedia 换源。
func TestPreviewSessionUsesProxyWithUnchangedLocator(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	videoService := newProxyBackedVideoService(service)
	video := createProxyTestVideo(t, "proxied.mkv", 10)
	proxyPath := seedReadyProxy(t, service, video, 3072, time.Now())

	session, err := videoService.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	if session.Mode != "inline" {
		t.Fatalf("有代理时应当内嵌: %#v", session)
	}
	if session.InlineSource == nil || session.InlineSource.LocatorValue != previewMediaPath(video.ID) {
		t.Fatalf("locator 形态不该变: %#v", session.InlineSource)
	}
	if session.InlineSource.MIME != "video/mp4" {
		t.Fatalf("代理 MIME 应为 video/mp4: %s", session.InlineSource.MIME)
	}
	if session.Proxy == nil || session.Proxy.Strategy != models.PlaybackProxyStrategyRemux || session.Proxy.Size != 3072 {
		t.Fatalf("会话应带代理标注: %#v", session.Proxy)
	}

	media, err := videoService.ResolvePreviewMedia(video.ID)
	if err != nil {
		t.Fatalf("解析预览字节失败: %v", err)
	}
	if media.Path != proxyPath {
		t.Fatalf("应当解析到代理路径: %s", media.Path)
	}
	if media.MIME != "video/mp4" {
		t.Fatalf("代理 MIME 应为 video/mp4: %s", media.MIME)
	}
	if media.DisplayName != video.Name {
		t.Fatalf("显示名仍是源文件名: %s", media.DisplayName)
	}
}

// 白名单命中的视频（mp4）根本不查代理：既有路径逐字节不变。
func TestPreviewSessionForWhitelistedSourceIgnoresProxy(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	videoService := newProxyBackedVideoService(service)
	video := createProxyTestVideo(t, "native.mp4", 10)
	seedReadyProxy(t, service, video, 4096, time.Now())

	session, err := videoService.GetPreviewSession(video.ID)
	if err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	if session.Mode != "inline" || session.Proxy != nil {
		t.Fatalf("白名单命中时不该标代理: %#v", session)
	}
	if session.InlineSource.MIME != "video/mp4" {
		t.Fatalf("MIME 应来自白名单: %s", session.InlineSource.MIME)
	}
	media, err := videoService.ResolvePreviewMedia(video.ID)
	if err != nil {
		t.Fatalf("解析预览字节失败: %v", err)
	}
	if media.Path != video.Path {
		t.Fatalf("白名单命中时应下发源文件: %s", media.Path)
	}
}

// 正式播放永远打开源文件：代理只服务预览与手机端（D-004）。
func TestPlayVideoNeverOpensProxy(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	videoService := newProxyBackedVideoService(service)
	video := createProxyTestVideo(t, "formal.mkv", 10)
	seedReadyProxy(t, service, video, 1024, time.Now())

	original := openWithDefaultFn
	t.Cleanup(func() { openWithDefaultFn = original })
	opened := make([]string, 0, 1)
	openWithDefaultFn = func(path string, isDir bool) error {
		opened = append(opened, path)
		return nil
	}

	if _, err := videoService.PlayVideo(video.ID); err != nil {
		t.Fatalf("正式播放失败: %v", err)
	}
	if len(opened) != 1 {
		t.Fatalf("应当打开一次: %v", opened)
	}
	if opened[0] != video.Path {
		t.Fatalf("正式播放应打开源文件: %s", opened[0])
	}
	if strings.Contains(opened[0], playbackProxyDirName) {
		t.Fatalf("正式播放路径不该出现 proxies: %s", opened[0])
	}
}

// 内嵌白名单一个字都没改（4.1.5 不变行为）：这张表一改就会外溢到手机端。
func TestInlinePreviewMIMEsUnchanged(t *testing.T) {
	want := map[string]string{
		".mp4":  "video/mp4",
		".m4v":  "video/x-m4v",
		".webm": "video/webm",
		".ogv":  "video/ogg",
		".ogg":  "video/ogg",
	}
	if !reflect.DeepEqual(inlinePreviewMIMEs, want) {
		t.Fatalf("inlinePreviewMIMEs 被改动了\n实际: %#v\n期望: %#v", inlinePreviewMIMEs, want)
	}
}

// 手机端：有有效代理的视频入选候选池，媒体字节返回代理产物。
func TestShortFeedIncludesProxiedVideoAndServesProxyBytes(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	videoService := newProxyBackedVideoService(service)
	feed := NewShortFeedService(videoService)

	video := createProxyTestVideo(t, "mobile.mkv", 10)
	// 没有代理时不入选。
	candidates, _, err := feed.collectCandidates()
	if err != nil {
		t.Fatalf("收集候选失败: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("没有代理的 mkv 不该入选: %#v", candidates)
	}
	// 没有代理时不回落到源字节：mkv 发过去手机也放不出来（#7）。
	if _, err := feed.ResolveMedia(ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: video.ID}); !errors.Is(err, ErrShortFeedNoEligibleVideos) {
		t.Fatalf("没有代理时应当明确报没有可播内容: %v", err)
	}

	proxyPath := seedReadyProxy(t, service, video, 2048, time.Now())
	feed.invalidateCandidates()
	candidates, _, err = feed.collectCandidates()
	if err != nil {
		t.Fatalf("收集候选失败: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ref.ID != video.ID {
		t.Fatalf("有代理的 mkv 应当入选: %#v", candidates)
	}

	media, err := feed.ResolveMedia(ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: video.ID})
	if err != nil {
		t.Fatalf("解析手机端字节失败: %v", err)
	}
	if media.Path != proxyPath {
		t.Fatalf("应当下发代理字节: %s", media.Path)
	}
	if media.MIME != "video/mp4" {
		t.Fatalf("代理 MIME 应为 video/mp4: %s", media.MIME)
	}

	dto, err := feed.videoDTO(&video, "", "")
	if err != nil {
		t.Fatalf("构造条目失败: %v", err)
	}
	if dto.MediaURL == "" || dto.MediaMIME != "video/mp4" {
		t.Fatalf("有代理的条目应当给出媒体地址: %#v", dto)
	}
	if dto.ReasonCode != "" {
		t.Fatalf("有代理时不该报不支持内嵌: %#v", dto)
	}
}

// 手机端命中代理前也要校验指纹：失效就把它删掉，并明确报"没有可播内容"，
// 绝不把不可播的源字节发给手机（#7）。
func TestShortFeedResolveMediaRefusesSourceWhenProxyInvalid(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	videoService := newProxyBackedVideoService(service)
	feed := NewShortFeedService(videoService)
	video := createProxyTestVideo(t, "mobilestale.mkv", 10)
	proxyPath := seedReadyProxy(t, service, video, 2048, time.Now())

	if err := os.WriteFile(video.Path, []byte("changed"), 0o600); err != nil {
		t.Fatalf("替换源文件失败: %v", err)
	}
	mustSetFileModTime(t, video.Path, time.Now().Add(4*time.Hour))

	// 代理失效之后不回落到源字节，而是明确报没有可播内容（#7）；
	// 同一次调用顺带把失效的文件与表行清掉。
	if _, err := feed.ResolveMedia(ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: video.ID}); !errors.Is(err, ErrShortFeedNoEligibleVideos) {
		t.Fatalf("代理失效后应当明确报没有可播内容，而不是发源文件: %v", err)
	}
	if _, err := os.Stat(proxyPath); !os.IsNotExist(err) {
		t.Fatalf("失效代理应被删除: %v", err)
	}
	row, err := loadProxyRow(video.ID)
	if err != nil {
		t.Fatalf("读取代理记录失败: %v", err)
	}
	if row != nil {
		t.Fatalf("失效代理记录应被删除: %#v", row)
	}
	// 白名单命中的视频不受影响：照旧下发源字节。
	native := createProxyTestVideo(t, "native.mp4", 10)
	media, err := feed.ResolveMedia(ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: native.ID})
	if err != nil {
		t.Fatalf("白名单命中时应当照旧下发源字节: %v", err)
	}
	if media.Path != native.Path || media.MIME != "video/mp4" {
		t.Fatalf("白名单路径不该变: %#v", media)
	}
}

// 代理绝不写进用户的媒体目录，只落在应用数据目录的 proxies/ 下（D-001）。
func TestPlaybackProxyStaysInsideDataDirectory(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	video := createProxyTestVideo(t, "isolated.mkv", 10)
	writeProxySnapshot(t, video, "h264", "aac", 0, 0)

	if result := runProxyOnce(t, service, video.ID); result.Code != PlaybackProxyCodeCreated {
		t.Fatalf("应当生成成功: %#v", result)
	}
	row := mustLoadProxyRow(t, video.ID)
	path := service.proxyPathForRow(row)
	if !strings.HasPrefix(path, service.Dir()+string(os.PathSeparator)) {
		t.Fatalf("产物应在代理目录内: %s", path)
	}
	// 源目录里除了源文件之外什么都没多。
	entries, err := os.ReadDir(filepath.Dir(video.Path))
	if err != nil {
		t.Fatalf("读取源目录失败: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != video.Name {
		t.Fatalf("源目录不该被写入: %v", entries)
	}
	// 代理不进 videos 表：库里视频条数不变。
	var count int64
	if err := database.DB.Model(&models.Video{}).Count(&count).Error; err != nil {
		t.Fatalf("统计视频失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("代理不该入库为视频记录: videos=%d", count)
	}
}

// GetPlaybackProxy 只读回元数据，不做指纹校验也不刷新 last_used_at。
func TestGetPlaybackProxyIsReadOnly(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	video := createProxyTestVideo(t, "readonly.mkv", 10)
	stamped := time.Now().Add(-2 * time.Hour)
	seedReadyProxy(t, service, video, 1024, stamped)

	view, err := service.GetPlaybackProxy(video.ID)
	if err != nil {
		t.Fatalf("读取代理视图失败: %v", err)
	}
	if view == nil || view.Status != models.PlaybackProxyStatusReady || view.OutputSize != 1024 {
		t.Fatalf("代理视图不符: %#v", view)
	}
	if got := mustLoadProxyRow(t, video.ID).LastUsedAt; got.After(stamped.Add(time.Second)) {
		t.Fatalf("只读查询不该刷新 last_used_at: %v", got)
	}

	other := createProxyTestVideo(t, "noproxy.mkv", 10)
	missing, err := service.GetPlaybackProxy(other.ID)
	if err != nil {
		t.Fatalf("读取不存在的代理应当空转: %v", err)
	}
	if missing != nil {
		t.Fatalf("没有代理时应返回 nil: %#v", missing)
	}
}

// 按当前筛选批量生成：筛选在后端解析成 ID 列表。
func TestBatchCreatePlaybackProxiesForFilter(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, stub := newProxyTestService(t)
	matching := createProxyTestVideo(t, "filtered.mkv", 10)
	writeProxySnapshot(t, matching, "h264", "aac", 0, 0)

	if _, err := service.BatchCreatePlaybackProxiesForFilter(context.Background(), LibraryFilter{Keyword: "filtered"}); err != nil {
		t.Fatalf("按筛选批量入队失败: %v", err)
	}
	status := waitForProxyIdle(t, service)
	if status.Total != 1 || len(status.Results) != 1 || status.Results[0].VideoID != matching.ID {
		t.Fatalf("只有命中筛选的视频该入队: %#v", status)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("ffmpeg 应当只跑一次: %d", len(stub.calls))
	}

	// 一条都不命中时不启动任务。
	fresh, freshStub := newProxyTestService(t)
	if _, err := fresh.BatchCreatePlaybackProxiesForFilter(context.Background(), LibraryFilter{Keyword: "不存在的关键词"}); err != nil {
		t.Fatalf("按筛选批量入队失败: %v", err)
	}
	if got := fresh.Status(); got.Total != 0 || got.Running {
		t.Fatalf("零命中时不该有任务: %#v", got)
	}
	if len(freshStub.calls) != 0 {
		t.Fatalf("不该调 ffmpeg: %#v", freshStub.calls)
	}
}

// 扫描对账要报出"本次新增了哪几条"（D-006）：自动代理只对这一批下手，
// 光有 Added 计数说不出是哪几条，代理候选就无从筛选。
func TestScanSyncReportsAddedVideoIDsForProxyCandidates(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	root := t.TempDir()
	inlinePath := filepath.Join(root, "native.mp4")
	proxyPath := filepath.Join(root, "container.mkv")
	mustCreateFile(t, inlinePath)
	mustCreateFile(t, proxyPath)
	// 扫描会跳过刚刚改动过的文件（避免收进正在复制的半个文件），把 mtime 拨旧。
	settled := time.Now().Add(-10 * time.Minute)
	mustSetFileModTime(t, inlinePath, settled)
	mustSetFileModTime(t, proxyPath, settled)
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("video_extensions", ".mp4,.mkv").Error; err != nil {
		t.Fatalf("放开扫描扩展名失败: %v", err)
	}

	videoService := newProxyBackedVideoService(service)
	result := videoService.SyncScanDirectories([]models.ScanDirectory{{Path: root, Alias: "root"}})
	if result.Added != 2 {
		t.Fatalf("应当新增两条: %+v", result)
	}
	if len(result.AddedVideoIDs) != 2 {
		t.Fatalf("新增 ID 应当逐条报出: %+v", result.AddedVideoIDs)
	}

	// 自动路径从这一批里只挑内嵌白名单不命中的那条。
	candidates, err := filterPlaybackProxyCandidates(result.AddedVideoIDs)
	if err != nil {
		t.Fatalf("筛选代理候选失败: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("只有 mkv 该成为代理候选: %v", candidates)
	}
	var candidate models.Video
	if err := database.DB.First(&candidate, candidates[0]).Error; err != nil {
		t.Fatalf("读取候选视频失败: %v", err)
	}
	if candidate.Path != proxyPath {
		t.Fatalf("候选应当是那条 mkv: %s", candidate.Path)
	}

	// 第二次扫描没有新增，候选集为空——自动模式不会去重建被淘汰的老代理。
	second := videoService.SyncScanDirectories([]models.ScanDirectory{{Path: root, Alias: "root"}})
	if second.Added != 0 || len(second.AddedVideoIDs) != 0 {
		t.Fatalf("没有新文件时不该报新增: %+v", second)
	}
}

// 保护窗口必须严格大于节流窗口，否则「正在播的代理」会被淘汰：
// T0 刷新一次 → 59 秒时再访问被节流（表里仍是 T0）→ 61 秒时整理，
// 两个窗口等长的话 T0 恰好落在 cutoff 之外，那一份就被删了。
func TestPlaybackProxyRecentlyPlayedSurvivesEvictionDespiteThrottle(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	videoService := newProxyBackedVideoService(service)
	base := time.Now()
	current := base
	service.now = func() time.Time { return current }

	playing := createProxyTestVideo(t, "playing.mkv", 10)
	idle := createProxyTestVideo(t, "idle.mkv", 10)
	playingPath := seedReadyProxy(t, service, playing, 1000, base.Add(-time.Hour))
	idlePath := seedReadyProxy(t, service, idle, 1000, base.Add(-2*time.Hour))
	// 上限压到 500：淘汰掉最旧那份之后仍然超限，整理必须继续考虑"正在播"的这份，
	// 保护窗口是不是真的把它挡住了才检得出来。
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("proxy_cache_limit_bytes", 500).Error; err != nil {
		t.Fatalf("设置上限失败: %v", err)
	}

	// T0：开始播放，last_used_at 刷成 T0。
	if _, err := videoService.GetPreviewSession(playing.ID); err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	if got := mustLoadProxyRow(t, playing.ID).LastUsedAt; !got.Equal(base) {
		t.Fatalf("T0 应当刷成当前时间: %v", got)
	}

	// T0+59s：仍在播（又访问一次），落在节流窗口里，表里还是 T0。
	current = base.Add(59 * time.Second)
	if _, err := videoService.GetPreviewSession(playing.ID); err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	if got := mustLoadProxyRow(t, playing.ID).LastUsedAt; !got.Equal(base) {
		t.Fatalf("59 秒内应当被节流，表里仍是 T0: %v", got)
	}

	// T0+61s：整理。表里的值已经陈旧 61 秒，但保护窗口是 120 秒，救得住。
	current = base.Add(61 * time.Second)
	if _, err := service.EnforcePlaybackProxyLimit(); err != nil {
		t.Fatalf("整理失败: %v", err)
	}
	if _, err := os.Stat(playingPath); err != nil {
		t.Fatalf("正在播的代理不该被淘汰: %v", err)
	}
	if _, err := os.Stat(idlePath); !os.IsNotExist(err) {
		t.Fatalf("真正最久没用过的那份应当被淘汰: %v", err)
	}
	// 保护窗口把仅剩的候选挡住了，占用因此仍然超限——这是有意的：
	// 宁可暂时超限，也不在播放中途删掉正在读的那个文件。
	usage, err := service.GetPlaybackProxyUsage()
	if err != nil {
		t.Fatalf("统计占用失败: %v", err)
	}
	if usage.Count != 1 || usage.TotalBytes != 1000 {
		t.Fatalf("整理后应当只剩正在播的那份: %#v", usage)
	}
}

// 表里的 last_used_at 老过保护窗口时绕过节流强制刷新：
// 否则一直在播但每次都被节流的代理会在表里越来越旧，最终被误判为最久没用过。
func TestPlaybackProxyTouchBypassesThrottleWhenRowIsStale(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	videoService := newProxyBackedVideoService(service)
	base := time.Now()
	current := base
	service.now = func() time.Time { return current }

	video := createProxyTestVideo(t, "longplay.mkv", 10)
	seedReadyProxy(t, service, video, 1024, base.Add(-time.Hour))

	if _, err := videoService.GetPreviewSession(video.ID); err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	first := mustLoadProxyRow(t, video.ID).LastUsedAt

	// 把表里的值单独改旧（模拟"内存节流记录还在、表却已经陈旧"），
	// 再在节流窗口内访问：这一次必须强刷。
	staleStamp := base.Add(-playbackProxyEvictionGrace - time.Minute)
	if err := database.DB.Model(&models.VideoPlaybackProxy{}).
		Where("video_id = ?", video.ID).Update("last_used_at", staleStamp).Error; err != nil {
		t.Fatalf("改旧 last_used_at 失败: %v", err)
	}
	current = base.Add(10 * time.Second)
	if _, err := videoService.GetPreviewSession(video.ID); err != nil {
		t.Fatalf("读取预览会话失败: %v", err)
	}
	refreshed := mustLoadProxyRow(t, video.ID).LastUsedAt
	if !refreshed.After(first) {
		t.Fatalf("陈旧行应当绕过节流强制刷新: %v -> %v", first, refreshed)
	}
}

// 「清空全部代理」只删符合命名规则的文件：数据目录可能被用户加成扫描根，
// 或者手工放过东西进来，认不出来的一律跳过并报数。
func TestClearPlaybackProxiesSkipsForeignFiles(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	video := createProxyTestVideo(t, "c1.mkv", 10)
	proxyPath := seedReadyProxy(t, service, video, 1024, time.Now())

	foreign := filepath.Join(service.Dir(), "用户自己放的.mp4")
	if err := os.WriteFile(foreign, []byte("not ours"), 0o600); err != nil {
		t.Fatalf("放一个陌生文件失败: %v", err)
	}
	notes := filepath.Join(service.Dir(), "readme.txt")
	if err := os.WriteFile(notes, []byte("hello"), 0o600); err != nil {
		t.Fatalf("放一个陌生文件失败: %v", err)
	}

	usage, err := service.ClearPlaybackProxies()
	if err != nil {
		t.Fatalf("清空代理失败: %v", err)
	}
	if _, err := os.Stat(proxyPath); !os.IsNotExist(err) {
		t.Fatalf("代理文件应当被删除: %v", err)
	}
	for _, path := range []string{foreign, notes} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("陌生文件不该被删: %s err=%v", filepath.Base(path), err)
		}
	}
	if usage.ForeignFiles != 2 {
		t.Fatalf("陌生文件应当被报数: %#v", usage)
	}
	if usage.Count != 0 || usage.TotalBytes != 0 || usage.OrphanCount != 0 {
		t.Fatalf("清空后代理占用应为零: %#v", usage)
	}
}

// 只统计表行会漏掉「文件在、表里没行」的孤儿产物，它们照样占磁盘。
// 孤儿单列一档（不混进 Count），「清空全部」会一并删掉。
func TestPlaybackProxyUsageReportsOrphanFiles(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	kept := createProxyTestVideo(t, "kept.mkv", 10)
	seedReadyProxy(t, service, kept, 1000, time.Now())

	// 孤儿：名字符合命名规则，但表里没有对应行（回退旧版本、表被清过都会这样）。
	orphan := filepath.Join(service.Dir(), "999-0123456789abcdef.mp4")
	if err := os.WriteFile(orphan, bytes.Repeat([]byte("o"), 700), 0o600); err != nil {
		t.Fatalf("造孤儿文件失败: %v", err)
	}

	usage, err := service.GetPlaybackProxyUsage()
	if err != nil {
		t.Fatalf("统计占用失败: %v", err)
	}
	if usage.Count != 1 || usage.TotalBytes != 1000 {
		t.Fatalf("有行的那份应当照常统计: %#v", usage)
	}
	if usage.OrphanCount != 1 || usage.OrphanBytes != 700 {
		t.Fatalf("孤儿文件应当被单列统计: %#v", usage)
	}

	if _, err := service.ClearPlaybackProxies(); err != nil {
		t.Fatalf("清空代理失败: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("清空应当把孤儿文件一起删掉: %v", err)
	}
}

// 应用数据目录没解析出来时（appdata.Resolve 失败），所有会删东西的入口直接拒绝——
// 绝不退到相对路径 `proxies/` 去动进程工作目录下的文件。
func TestPlaybackProxyRefusesDeletionWhenDataDirUnavailable(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := NewPlaybackProxyService("", NewMediaProbeService())
	t.Cleanup(service.StopAndWait)

	if got := service.Dir(); got != "" {
		t.Fatalf("目录无效时 Dir() 应为空串: %q", got)
	}
	if _, err := service.ClearPlaybackProxies(); !errors.Is(err, ErrPlaybackProxyDirUnavailable) {
		t.Fatalf("清空应当明确拒绝: %v", err)
	}
	if _, err := service.EnforcePlaybackProxyLimit(); !errors.Is(err, ErrPlaybackProxyDirUnavailable) {
		t.Fatalf("整理应当明确拒绝: %v", err)
	}
	if err := service.DeletePlaybackProxy(1); !errors.Is(err, ErrPlaybackProxyDirUnavailable) {
		t.Fatalf("按视频删除应当明确拒绝: %v", err)
	}

	// 消费路径按"没有代理"处理，不猜路径。
	video := createProxyTestVideo(t, "nodir.mkv", 10)
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatalf("读取源文件失败: %v", err)
	}
	if proxy := service.resolveValidProxy(video.ID, playbackProxyFingerprintOf(info), true); proxy != nil {
		t.Fatalf("目录无效时不该解析出代理: %#v", proxy)
	}
	// 删除级联静默跳过，不能把删除操作本身弄挂。
	service.DeleteForVideo(video.ID)

	// 生成任务落在 encode_failed 上，且工作目录里不该冒出 proxies/。
	writeProxySnapshot(t, video, "h264", "aac", 0, 0)
	if result := runProxyOnce(t, service, video.ID); result.Code != PlaybackProxyCodeEncodeFailed {
		t.Fatalf("目录无效时生成应当失败: %#v", result)
	}
	if _, err := os.Stat("proxies"); !os.IsNotExist(err) {
		t.Fatalf("绝不能在相对路径上建代理目录: %v", err)
	}
}

// 抽签阶段只看「有没有 ready 行」，抽中之后必须补校验指纹：
// 代理刚失效的话这条视频本来就不该入选，放过去手机端只会拿到播不了的源字节。
func TestShortFeedSkipsVideoWhoseProxyWentStale(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	videoService := newProxyBackedVideoService(service)
	feed := NewShortFeedService(videoService)

	video := createProxyTestVideo(t, "stalefeed.mkv", 10)
	proxyPath := seedReadyProxy(t, service, video, 2048, time.Now())

	// 有效代理：正常入选并抽得出来。
	if _, err := feed.NextItem(nil); err != nil {
		t.Fatalf("有代理时应当抽得出来: %v", err)
	}

	// 源被替换：表里还是 ready 行，但指纹已经对不上了。
	if err := os.WriteFile(video.Path, []byte("replaced"), 0o600); err != nil {
		t.Fatalf("替换源文件失败: %v", err)
	}
	mustSetFileModTime(t, video.Path, time.Now().Add(5*time.Hour))

	item, err := feed.NextItem(nil)
	if err == nil {
		// 唯一候选失效之后只可能回退成"存在但不可内联"的提示条目，
		// 绝不能给出一个带媒体地址的条目。
		if item.MediaURL != "" {
			t.Fatalf("失效代理不该再给出媒体地址: %#v", item)
		}
		if item.ReasonCode != "inline_not_supported" {
			t.Fatalf("应当明确报不可内联: %#v", item)
		}
	} else if !errors.Is(err, ErrShortFeedNoEligibleVideos) {
		t.Fatalf("失效之后应当没有可播候选: %v", err)
	}
	if _, err := os.Stat(proxyPath); !os.IsNotExist(err) {
		t.Fatalf("抽中时的校验应当把失效代理清掉: %v", err)
	}
	row, err := loadProxyRow(video.ID)
	if err != nil {
		t.Fatalf("读取代理记录失败: %v", err)
	}
	if row != nil {
		t.Fatalf("失效代理记录应当被清掉: %#v", row)
	}
}

// 直接打 ResolveMedia 的口径表（#7）：白名单命中发源文件，非白名单只在有
// 有效代理时才发字节，其余一律报"没有可播内容"（路由层会收敛成 404）。
//
// 为什么不能回落到源文件：抽签阶段只看"有没有 ready 代理行"，指纹校验落在
// 下发这一刻。回落的话，代理刚失效的那条视频会把几十兆 mkv 发给手机，
// 而手机只能显示一个放不出来的黑框。
func TestShortFeedResolveMediaPlayabilityMatrix(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	videoService := newProxyBackedVideoService(service)
	feed := NewShortFeedService(videoService)

	// ① 白名单命中：照旧发源文件。
	native := createProxyTestVideo(t, "native.mp4", 10)
	media, err := feed.ResolveMedia(ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: native.ID})
	if err != nil {
		t.Fatalf("白名单命中应当发源文件: %v", err)
	}
	if media.Path != native.Path || media.MIME != "video/mp4" {
		t.Fatalf("白名单路径不该变: %#v", media)
	}

	// ② 非白名单、从来没有过代理：报没有可播内容，不发源文件。
	never := createProxyTestVideo(t, "never.mkv", 10)
	if _, err := feed.ResolveMedia(ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: never.ID}); !errors.Is(err, ErrShortFeedNoEligibleVideos) {
		t.Fatalf("非白名单且无代理应当报没有可播内容: %v", err)
	}

	// ③ 非白名单、有有效代理：发代理字节。
	proxied := createProxyTestVideo(t, "proxied.mkv", 10)
	proxyPath := seedReadyProxy(t, service, proxied, 2048, time.Now())
	media, err = feed.ResolveMedia(ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: proxied.ID})
	if err != nil {
		t.Fatalf("有有效代理应当发代理字节: %v", err)
	}
	if media.Path != proxyPath || media.MIME != "video/mp4" {
		t.Fatalf("应当发代理产物: %#v", media)
	}

	// ④ 非白名单、代理行还在但产物被外部删了：同样报没有可播内容，并收掉孤儿行。
	if err := os.Remove(proxyPath); err != nil {
		t.Fatalf("删除代理产物失败: %v", err)
	}
	if _, err := feed.ResolveMedia(ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: proxied.ID}); !errors.Is(err, ErrShortFeedNoEligibleVideos) {
		t.Fatalf("产物缺失应当报没有可播内容: %v", err)
	}
	row, err := loadProxyRow(proxied.ID)
	if err != nil {
		t.Fatalf("读取代理记录失败: %v", err)
	}
	if row != nil {
		t.Fatalf("孤儿代理记录应当被收掉: %#v", row)
	}

	// ⑤ 非白名单、只有 failed 行：不算有代理。
	failed := createProxyTestVideo(t, "failedrow2.mkv", 10)
	info, err := os.Stat(failed.Path)
	if err != nil {
		t.Fatalf("读取源文件失败: %v", err)
	}
	fingerprint := playbackProxyFingerprintOf(info)
	if err := database.DB.Create(&models.VideoPlaybackProxy{
		VideoID: failed.ID, SourceSize: fingerprint.size, SourceModTimeNS: fingerprint.modTimeNS,
		Strategy: models.PlaybackProxyStrategyTranscode, Status: models.PlaybackProxyStatusFailed,
		LastUsedAt: time.Now(), LastError: "boom",
	}).Error; err != nil {
		t.Fatalf("写入失败记录失败: %v", err)
	}
	if _, err := feed.ResolveMedia(ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: failed.ID}); !errors.Is(err, ErrShortFeedNoEligibleVideos) {
		t.Fatalf("failed 行不算有代理，应当报没有可播内容: %v", err)
	}
}

// 抽签阶段只看"有没有 ready 行"，抽中之后必须补校验指纹——这一条把
// candidateFileExists 里那一步单独钉住：代理失效的视频不得被抽出来当可播条目。
func TestShortFeedCandidateCheckRejectsStaleProxyAtDrawTime(t *testing.T) {
	setupVideoServiceTestDB(t)
	service, _ := newProxyTestService(t)
	videoService := newProxyBackedVideoService(service)
	feed := NewShortFeedService(videoService)

	video := createProxyTestVideo(t, "drawstale.mkv", 10)
	proxyPath := seedReadyProxy(t, service, video, 2048, time.Now())

	// 直接构造一个候选，绕过缓存，只测抽中之后的这一步校验。
	candidate := shortFeedCandidate{
		ref:   ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: video.ID},
		video: &video,
	}
	if !feed.candidateFileExists(candidate) {
		t.Fatalf("代理有效时该条候选应当通得过")
	}

	if err := os.WriteFile(video.Path, []byte("replaced"), 0o600); err != nil {
		t.Fatalf("替换源文件失败: %v", err)
	}
	mustSetFileModTime(t, video.Path, time.Now().Add(6*time.Hour))

	if feed.candidateFileExists(candidate) {
		t.Fatalf("代理失效之后该条候选必须被剔除")
	}
	if _, err := os.Stat(proxyPath); !os.IsNotExist(err) {
		t.Fatalf("这一步校验应当顺手把失效代理删掉: %v", err)
	}
}
