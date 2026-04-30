package services

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"video-master/database"
	"video-master/models"
)

func createShortFeedVideo(t *testing.T, root string, name string, duration float64, stale bool, tags ...*models.Tag) models.Video {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("0123456789abcdef"), 0644); err != nil {
		t.Fatalf("写入视频文件失败: %v", err)
	}
	video := models.Video{
		Name:      name,
		Path:      path,
		Directory: root,
		Size:      16,
		Duration:  duration,
		IsStale:   stale,
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if len(tags) > 0 {
		if err := database.DB.Model(&video).Association("Tags").Append(tags); err != nil {
			t.Fatalf("绑定标签失败: %v", err)
		}
	}
	return video
}

func createShortFeedTag(t *testing.T, name string) models.Tag {
	t.Helper()
	tag := models.Tag{Name: name, Color: "#0d9488"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	return tag
}

func countShortFeedRows(t *testing.T, table string) int64 {
	t.Helper()
	var count int64
	if err := database.DB.Table(table).Count(&count).Error; err != nil {
		t.Fatalf("统计 %s 失败: %v", table, err)
	}
	return count
}

func TestShortFeedSelectionUsesCappedWeakRecommendation(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	likedTag := createShortFeedTag(t, "剧情")
	otherTag := createShortFeedTag(t, "旅行")
	likedVideo := createShortFeedVideo(t, root, "liked.mp4", 60, false, &likedTag)
	otherVideo := createShortFeedVideo(t, root, "other.mp4", 60, false, &otherTag)
	createShortFeedVideo(t, root, "long.mp4", 301, false, &likedTag)
	createShortFeedVideo(t, root, "zero.mp4", 0, false, &likedTag)
	createShortFeedVideo(t, root, "stale.mp4", 60, true, &likedTag)

	if err := database.DB.Create(&models.ShortFeedTagPreference{TagID: likedTag.ID, Score: 99}).Error; err != nil {
		t.Fatalf("创建偏好失败: %v", err)
	}

	var loadedLiked models.Video
	if err := database.DB.Preload("Tags").First(&loadedLiked, likedVideo.ID).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	if weight := shortFeedWeight(loadedLiked, map[uint]float64{likedTag.ID: 99}); weight != 1.5 {
		t.Fatalf("同标签权重应封顶为 1.5，实际 %.2f", weight)
	}

	svc := NewShortFeedService(&VideoService{})
	svc.randFloat64 = func() float64 { return 0.99 }
	next, err := svc.NextVideo(nil)
	if err != nil {
		t.Fatalf("获取下一个视频失败: %v", err)
	}
	if next.ID != otherVideo.ID {
		t.Fatalf("高随机区间仍应能选到非偏好视频，got=%d want=%d", next.ID, otherVideo.ID)
	}
}

func TestShortFeedLikeFavoriteDoNotPolluteCanonicalTags(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	tag := createShortFeedTag(t, "人物")
	video := createShortFeedVideo(t, root, "tagged.mp4", 80, false, &tag)
	beforeTags := countShortFeedRows(t, "tags")
	beforeVideoTags := countShortFeedRows(t, "video_tags")

	svc := NewShortFeedService(&VideoService{})
	if _, err := svc.SetLiked(video.ID, true); err != nil {
		t.Fatalf("设置喜欢失败: %v", err)
	}
	if _, err := svc.SetLiked(video.ID, true); err != nil {
		t.Fatalf("重复设置喜欢失败: %v", err)
	}
	if _, err := svc.SetFavorited(video.ID, true); err != nil {
		t.Fatalf("设置收藏失败: %v", err)
	}

	if got := countShortFeedRows(t, "tags"); got != beforeTags {
		t.Fatalf("like/favorite 不应改变 tags 数量，got=%d want=%d", got, beforeTags)
	}
	if got := countShortFeedRows(t, "video_tags"); got != beforeVideoTags {
		t.Fatalf("like/favorite 不应改变 video_tags 数量，got=%d want=%d", got, beforeVideoTags)
	}
	var preference models.ShortFeedTagPreference
	if err := database.DB.Where("tag_id = ?", tag.ID).First(&preference).Error; err != nil {
		t.Fatalf("应记录已有 tag 的偏好: %v", err)
	}
	if preference.Score != ShortFeedPreferenceStep {
		t.Fatalf("重复 liked=true 应保持 set-state 幂等，score=%.2f", preference.Score)
	}
}

func TestShortFeedPlaybackAndDeleteUseExistingSemantics(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	video := createShortFeedVideo(t, root, "play.mp4", 120, false)

	svc := NewShortFeedService(&VideoService{})
	if _, err := svc.RecordShortFeedPlayback(video.ID); err != nil {
		t.Fatalf("记录播放失败: %v", err)
	}
	var afterPlay models.Video
	if err := database.DB.First(&afterPlay, video.ID).Error; err != nil {
		t.Fatalf("读取播放后视频失败: %v", err)
	}
	if afterPlay.RandomPlayCount != 1 || afterPlay.LastPlayedAt == nil {
		t.Fatalf("播放统计未更新 random=%d last=%v", afterPlay.RandomPlayCount, afterPlay.LastPlayedAt)
	}

	if err := svc.DeleteVideo(video.ID); err != nil {
		t.Fatalf("删除视频失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, DefaultTrashDirName, "play.mp4")); err != nil {
		t.Fatalf("文件应移动到 trash: %v", err)
	}
	var deleted models.Video
	if err := database.DB.Unscoped().First(&deleted, video.ID).Error; err != nil {
		t.Fatalf("读取软删除视频失败: %v", err)
	}
	if !deleted.DeletedAt.IsValid() {
		t.Fatalf("应软删除数据库记录")
	}
}

func TestShortFeedHTTPGuardsAndRange(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	video := createShortFeedVideo(t, root, "range.mp4", 90, false)
	svc := NewShortFeedService(&VideoService{})
	server := NewShortFeedHTTPServer(svc, fstest.MapFS{
		"short.html": &fstest.MapFile{Data: []byte("<div>short</div>"), ModTime: time.Now()},
	}, ShortFeedHTTPServerConfig{BindAddress: "127.0.0.1", PortStart: 18088, PortEnd: 18088})
	handler := server.Handler()

	forbidden := httptest.NewRecorder()
	forbiddenReq := httptest.NewRequest(http.MethodGet, "/short-api/status", nil)
	forbiddenReq.RemoteAddr = "203.0.113.10:1234"
	handler.ServeHTTP(forbidden, forbiddenReq)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("公网来源应被拒绝，got=%d", forbidden.Code)
	}

	formWrite := httptest.NewRecorder()
	formReq := httptest.NewRequest(http.MethodPost, "/short-api/videos/1/like", strings.NewReader("liked=true"))
	formReq.RemoteAddr = "127.0.0.1:1234"
	formReq.Host = "127.0.0.1:18088"
	formReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(formWrite, formReq)
	if formWrite.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form mutation 应被拒绝，got=%d", formWrite.Code)
	}

	missingBody := httptest.NewRecorder()
	missingBodyReq := httptest.NewRequest(http.MethodPost, "/short-api/videos/"+strconvUint(video.ID)+"/like", nil)
	missingBodyReq.RemoteAddr = "127.0.0.1:1234"
	missingBodyReq.Host = "127.0.0.1:18088"
	missingBodyReq.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(missingBody, missingBodyReq)
	if missingBody.Code != http.StatusBadRequest {
		t.Fatalf("missing JSON body 应被拒绝，got=%d", missingBody.Code)
	}

	originMismatch := httptest.NewRecorder()
	originReq := httptest.NewRequest(http.MethodPost, "/short-api/videos/"+strconvUint(video.ID)+"/like", strings.NewReader(`{"liked":true}`))
	originReq.RemoteAddr = "127.0.0.1:1234"
	originReq.Host = "127.0.0.1:18088"
	originReq.Header.Set("Content-Type", "application/json")
	originReq.Header.Set("Origin", "http://example.test")
	handler.ServeHTTP(originMismatch, originReq)
	if originMismatch.Code != http.StatusForbidden {
		t.Fatalf("Origin mismatch 应被拒绝，got=%d", originMismatch.Code)
	}

	rangeResp := httptest.NewRecorder()
	rangeReq := httptest.NewRequest(http.MethodGet, "/short-media/"+strconvUint(video.ID), nil)
	rangeReq.RemoteAddr = "127.0.0.1:1234"
	rangeReq.Header.Set("Range", "bytes=0-3")
	handler.ServeHTTP(rangeResp, rangeReq)
	if rangeResp.Code != http.StatusPartialContent {
		t.Fatalf("Range 请求应返回 206，got=%d body=%s", rangeResp.Code, rangeResp.Body.String())
	}
	if body := rangeResp.Body.String(); body != "0123" {
		t.Fatalf("Range body 错误: %q", body)
	}
}

func TestShortFeedHTTPServerFallbackAndShutdown(t *testing.T) {
	setupVideoServiceTestDB(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占用端口失败: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	server := NewShortFeedHTTPServer(NewShortFeedService(&VideoService{}), fstest.MapFS{
		"short.html": &fstest.MapFile{Data: []byte("<div>short</div>"), ModTime: time.Now()},
	}, ShortFeedHTTPServerConfig{BindAddress: "127.0.0.1", PortStart: port, PortEnd: port + 1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.Start(ctx)
	status := server.Status()
	if !status.Running || !status.FallbackUsed || status.Port != port+1 {
		t.Fatalf("应使用 fallback 端口，status=%+v", status)
	}

	resp, err := http.Get(status.URL)
	if err != nil {
		t.Fatalf("fallback URL 应可访问: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("short app status=%d", resp.StatusCode)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := server.Stop(stopCtx); err != nil {
		t.Fatalf("停止服务失败: %v", err)
	}
	if _, err := http.Get(status.URL); err == nil {
		t.Fatalf("shutdown 后不应继续监听")
	}
}

func strconvUint(value uint) string {
	return strconv.FormatUint(uint64(value), 10)
}
