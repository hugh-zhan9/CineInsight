package services

import (
	"context"
	"errors"
	"fmt"
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

// 排除集与媒体路由都改成带类型的键：图片 ID 与视频 ID 各自从 1 开始，
// 裸 ID 会让两种媒体互相顶掉。未知类型必须拒绝而不是回退成视频。
func TestShortFeedTypedRefParsing(t *testing.T) {
	if ref, ok := ParseShortFeedMediaRef("video:12"); !ok || ref.Kind != ShortFeedMediaVideo || ref.ID != 12 {
		t.Fatalf("video:12 解析失败: %+v ok=%v", ref, ok)
	}
	if ref, ok := ParseShortFeedMediaRef(" image:7 "); !ok || ref.Kind != ShortFeedMediaImage || ref.ID != 7 {
		t.Fatalf("image:7 解析失败: %+v ok=%v", ref, ok)
	}
	for _, bad := range []string{"", "12", "audio:1", "video:0", "video:abc", "video:", ":1"} {
		if ref, ok := ParseShortFeedMediaRef(bad); ok {
			t.Fatalf("%q 不应被接受，实际 %+v", bad, ref)
		}
	}
	if got := (ShortFeedMediaRef{Kind: ShortFeedMediaImage, ID: 3}).Key(); got != "image:3" {
		t.Fatalf("Key() 应为 image:3，实际 %q", got)
	}

	refs := parseShortFeedExcludeRefs("video:1, image:2 ,audio:3,,video:0,video:4")
	want := []ShortFeedMediaRef{
		{Kind: ShortFeedMediaVideo, ID: 1},
		{Kind: ShortFeedMediaImage, ID: 2},
		{Kind: ShortFeedMediaVideo, ID: 4},
	}
	if len(refs) != len(want) {
		t.Fatalf("排除集应丢弃无法识别的条目，期望 %d 条，实际 %+v", len(want), refs)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("排除集第 %d 条应为 %+v，实际 %+v", i, want[i], refs[i])
		}
	}

	if ref, action, ok := parseShortFeedItemAction("/short-api/items/video/9/like"); !ok || action != "like" || ref.ID != 9 {
		t.Fatalf("动作路径解析失败: %+v %q ok=%v", ref, action, ok)
	}
	for _, bad := range []string{"/short-api/items/9/like", "/short-api/items/audio/9/like", "/short-api/items/video/0/like", "/short-api/items/video/9"} {
		if _, _, ok := parseShortFeedItemAction(bad); ok {
			t.Fatalf("%q 不应被接受", bad)
		}
	}
	if ref, ok := parseShortFeedMediaPath("/short-media/image/5"); !ok || ref.Kind != ShortFeedMediaImage || ref.ID != 5 {
		t.Fatalf("媒体路径解析失败: %+v ok=%v", ref, ok)
	}
	for _, bad := range []string{"/short-media/5", "/short-media/audio/5", "/short-media/video/0"} {
		if _, ok := parseShortFeedMediaPath(bad); ok {
			t.Fatalf("%q 不应被接受", bad)
		}
	}
}

func imageRef(id uint) ShortFeedMediaRef {
	return ShortFeedMediaRef{Kind: ShortFeedMediaImage, ID: id}
}

// createShortFeedImage 写一张真实字节的文件并建库记录。内容不需要是合法图像：
// jpg 走 ffmpeg 解码分支，大图直出原文件，不经过解码。
func createShortFeedImage(t *testing.T, root string, name string, format string, stale bool) models.Image {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("0123456789abcdef"), 0644); err != nil {
		t.Fatalf("写入图片文件失败: %v", err)
	}
	img := models.Image{
		Name:      name,
		Path:      path,
		Directory: root,
		Size:      16,
		Format:    format,
		IsStale:   stale,
	}
	if err := database.DB.Create(&img).Error; err != nil {
		t.Fatalf("创建图片失败: %v", err)
	}
	return img
}

// 图片没有时长，视频那条门槛不适用；判据是"未失效 + 有可用解码器"。
// 不可解码的格式必须在入选阶段就被挡掉，而不是入选后再报"无法播放"。
func TestShortFeedImageEligibilityAndMediaRange(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := NewShortFeedService(&VideoService{})
	svc.SetImageThumbnailService(NewImageThumbnailService(t.TempDir()))

	ok := createShortFeedImage(t, root, "ok.jpg", "jpg", false)
	staleImage := createShortFeedImage(t, root, "stale.jpg", "jpg", true)
	unsupported := createShortFeedImage(t, root, "weird.xyz", "xyz", false)

	eligible, err := svc.loadEligibleImages(nil)
	if err != nil {
		t.Fatalf("读取可入选图片失败: %v", err)
	}
	if len(eligible) != 1 || eligible[0].ID != ok.ID {
		ids := make([]uint, 0, len(eligible))
		for _, img := range eligible {
			ids = append(ids, img.ID)
		}
		t.Fatalf("只有可解码且未失效的图片应入选，期望 [%d]，实际 %v", ok.ID, ids)
	}
	if svc.shortFeedImageEligible(staleImage) {
		t.Fatal("失效图片不应入选")
	}
	if svc.shortFeedImageEligible(unsupported) {
		t.Fatal("无可用解码器的图片不应入选")
	}

	// 未注入解码服务时图片一律不入选，不能悄悄退化成"当作视频处理"。
	bare := NewShortFeedService(&VideoService{})
	if bare.shortFeedImageEligible(ok) {
		t.Fatal("未注入图片解码服务时不应入选任何图片")
	}

	server := NewShortFeedHTTPServer(svc, fstest.MapFS{"short.html": &fstest.MapFile{Data: []byte("<html></html>")}}, ShortFeedHTTPServerConfig{})
	handler := server.Handler()

	rangeReq := httptest.NewRequest(http.MethodGet, "/short-media/image/"+strconvUint(ok.ID), nil)
	rangeReq.RemoteAddr = "127.0.0.1:5321"
	rangeReq.Header.Set("Range", "bytes=0-3")
	rangeRec := httptest.NewRecorder()
	handler.ServeHTTP(rangeRec, rangeReq)
	if rangeRec.Code != http.StatusPartialContent {
		t.Fatalf("图片应支持 Range 请求，实际 %d", rangeRec.Code)
	}
	if body := rangeRec.Body.String(); body != "0123" {
		t.Fatalf("Range 响应体应为 0123，实际 %q", body)
	}
	if cache := rangeRec.Header().Get("Cache-Control"); cache == "" {
		t.Fatal("图片下发应带 Cache-Control")
	}

	// 不可解码的图片不给下发，且不泄露内部原因。
	badReq := httptest.NewRequest(http.MethodGet, "/short-media/image/"+strconvUint(unsupported.ID), nil)
	badReq.RemoteAddr = "127.0.0.1:5321"
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusNotFound {
		t.Fatalf("不可解码的图片应返回 404，实际 %d", badRec.Code)
	}

	// 源文件消失后要标记 stale，与视频侧一致。
	if err := os.Remove(ok.Path); err != nil {
		t.Fatalf("删除图片文件失败: %v", err)
	}
	missingReq := httptest.NewRequest(http.MethodGet, "/short-media/image/"+strconvUint(ok.ID), nil)
	missingReq.RemoteAddr = "127.0.0.1:5321"
	missingRec := httptest.NewRecorder()
	handler.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("源文件缺失应返回 404，实际 %d", missingRec.Code)
	}
	var reloaded models.Image
	if err := database.DB.First(&reloaded, ok.ID).Error; err != nil {
		t.Fatalf("重新读取图片失败: %v", err)
	}
	if !reloaded.IsStale {
		t.Fatal("源文件缺失的图片应被标记为 stale")
	}
}

// 图片的喜欢/收藏/删除与回写投影：喜欢完全拥有那个自动标签（含反向清理），
// 收藏每次动作只投影一次，绝不覆盖用户随后在桌面端的手动取消。
func TestShortFeedImageInteractionsProjectIntoImageLibrary(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := NewShortFeedService(&VideoService{})
	svc.SetImageThumbnailService(NewImageThumbnailService(t.TempDir()))

	img := createShortFeedImage(t, root, "photo.jpg", "jpg", false)
	ref := imageRef(img.ID)

	if _, err := svc.SetLiked(ref, true); err != nil {
		t.Fatalf("图片喜欢失败: %v", err)
	}
	// 幂等：重复喜欢不应重复插标签。
	if _, err := svc.SetLiked(ref, true); err != nil {
		t.Fatalf("图片重复喜欢失败: %v", err)
	}
	tagID := shortFeedLikedTagID(t)
	if got := imageTagLinkCount(t, img.ID, tagID); got != 1 {
		t.Fatalf("喜欢应投影为 1 条图片标签关联，实际 %d", got)
	}

	if _, err := svc.SetFavorited(ref, true); err != nil {
		t.Fatalf("图片收藏失败: %v", err)
	}
	var reloaded models.Image
	if err := database.DB.First(&reloaded, img.ID).Error; err != nil {
		t.Fatalf("读取图片失败: %v", err)
	}
	if !reloaded.IsFavorite {
		t.Fatal("手机端收藏应回写图片库的 is_favorite")
	}

	// 用户在桌面端手动取消收藏后，再次同步不得把它改回去。
	if err := database.DB.Model(&models.Image{}).Where("id = ?", img.ID).Update("is_favorite", false).Error; err != nil {
		t.Fatalf("手动取消收藏失败: %v", err)
	}
	if _, err := svc.SyncFeedback(); err != nil {
		t.Fatalf("再次同步失败: %v", err)
	}
	if err := database.DB.First(&reloaded, img.ID).Error; err != nil {
		t.Fatalf("读取图片失败: %v", err)
	}
	if reloaded.IsFavorite {
		t.Fatal("同一次手机端收藏动作只投影一次，不应覆盖桌面端的手动取消")
	}

	// 取消喜欢要反向清理投影出去的标签。
	if _, err := svc.SetLiked(ref, false); err != nil {
		t.Fatalf("取消图片喜欢失败: %v", err)
	}
	if got := imageTagLinkCount(t, img.ID, tagID); got != 0 {
		t.Fatalf("取消喜欢后标签投影应被回收，实际 %d", got)
	}

	// 浏览计数走图片自己的并行表，不碰视频表。
	if _, err := svc.RecordPlayback(ref); err != nil {
		t.Fatalf("记录图片浏览失败: %v", err)
	}
	var imageInteraction models.ShortFeedImageInteraction
	if err := database.DB.Where("image_id = ?", img.ID).First(&imageInteraction).Error; err != nil {
		t.Fatalf("读取图片互动失败: %v", err)
	}
	if imageInteraction.ViewCount != 1 {
		t.Fatalf("图片浏览次数应为 1，实际 %d", imageInteraction.ViewCount)
	}
	var videoInteractions int64
	if err := database.DB.Model(&models.ShortFeedInteraction{}).Count(&videoInteractions).Error; err != nil {
		t.Fatalf("统计视频互动失败: %v", err)
	}
	if videoInteractions != 0 {
		t.Fatalf("图片互动不应写进视频互动表，实际 %d 行", videoInteractions)
	}

	// 删除走图片回收站：记录软删除且留有可恢复条目。
	if err := svc.DeleteItem(ref); err != nil {
		t.Fatalf("删除图片失败: %v", err)
	}
	var alive int64
	if err := database.DB.Model(&models.Image{}).Where("id = ?", img.ID).Count(&alive).Error; err != nil {
		t.Fatalf("统计图片失败: %v", err)
	}
	if alive != 0 {
		t.Fatal("删除后图片不应仍是活跃记录")
	}
	var trashed int64
	if err := database.DB.Model(&models.ImageTrashEntry{}).Where("image_id = ?", img.ID).Count(&trashed).Error; err != nil {
		t.Fatalf("统计图片回收站失败: %v", err)
	}
	if trashed != 1 {
		t.Fatalf("删除应留下 1 条图片回收站记录，实际 %d", trashed)
	}
}

// 投影总开关关闭时既不新增也不清理既有投影，图片侧与视频侧一致。
func TestShortFeedImageSyncDisabledLeavesProjectionAlone(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := NewShortFeedService(&VideoService{})
	svc.SetImageThumbnailService(NewImageThumbnailService(t.TempDir()))
	img := createShortFeedImage(t, root, "photo.jpg", "jpg", false)
	ref := imageRef(img.ID)

	if _, err := svc.SetLiked(ref, true); err != nil {
		t.Fatalf("图片喜欢失败: %v", err)
	}
	tagID := shortFeedLikedTagID(t)
	if got := imageTagLinkCount(t, img.ID, tagID); got != 1 {
		t.Fatalf("投影应存在，实际 %d", got)
	}

	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("short_feed_feedback_sync_enabled", false).Error; err != nil {
		t.Fatalf("关闭投影开关失败: %v", err)
	}
	if _, err := svc.SetLiked(ref, false); err != nil {
		t.Fatalf("取消图片喜欢失败: %v", err)
	}
	if got := imageTagLinkCount(t, img.ID, tagID); got != 1 {
		t.Fatalf("关闭开关后不应清理既有投影，实际 %d", got)
	}
}

func shortFeedLikedTagID(t *testing.T) uint {
	t.Helper()
	var tag models.Tag
	if err := database.DB.Where("name = ?", ShortFeedLikedTagName).First(&tag).Error; err != nil {
		t.Fatalf("读取自动标签失败: %v", err)
	}
	return tag.ID
}

// 混编流：视频与图片进同一个候选池，排除集能同时排掉两种媒体。
func TestShortFeedMixedFeedDrawsBothMediaKinds(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := NewShortFeedService(&VideoService{})
	svc.SetImageThumbnailService(NewImageThumbnailService(t.TempDir()))

	video := createShortFeedVideo(t, root, "clip.mp4", 30, false)
	img := createShortFeedImage(t, root, "photo.jpg", "jpg", false)

	// 抽满两端：randFloat64 分别落在第一个与最后一个候选上。
	svc.randFloat64 = func() float64 { return 0 }
	first, err := svc.NextItem(nil)
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	svc.randFloat64 = func() float64 { return 0.999 }
	last, err := svc.NextItem(nil)
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	kinds := map[ShortFeedMediaKind]bool{first.MediaKind: true, last.MediaKind: true}
	if !kinds[ShortFeedMediaVideo] || !kinds[ShortFeedMediaImage] {
		t.Fatalf("混编流应能抽到两种媒体，实际 %v / %v", first.MediaKind, last.MediaKind)
	}

	// 排除视频后只能抽到图片，反之亦然。
	onlyImage, err := svc.NextItem([]ShortFeedMediaRef{videoRef(video.ID)})
	if err != nil {
		t.Fatalf("排除视频后抽取失败: %v", err)
	}
	if onlyImage.MediaKind != ShortFeedMediaImage || onlyImage.ID != img.ID {
		t.Fatalf("排除视频后应只抽到图片，实际 %+v", onlyImage)
	}
	onlyVideo, err := svc.NextItem([]ShortFeedMediaRef{imageRef(img.ID)})
	if err != nil {
		t.Fatalf("排除图片后抽取失败: %v", err)
	}
	if onlyVideo.MediaKind != ShortFeedMediaVideo || onlyVideo.ID != video.ID {
		t.Fatalf("排除图片后应只抽到视频，实际 %+v", onlyVideo)
	}
	if onlyImage.MediaURL != "/short-media/image/"+strconvUint(img.ID) {
		t.Fatalf("图片媒体地址应带类型，实际 %q", onlyImage.MediaURL)
	}

	// 排除集盖住全部候选时回退允许重复，但不能连着重复最后那一条。
	repeated, err := svc.NextItem([]ShortFeedMediaRef{imageRef(img.ID), videoRef(video.ID)})
	if err != nil {
		t.Fatalf("排除集盖满时不应报错: %v", err)
	}
	if repeated.Ref() == (ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: video.ID}) {
		t.Fatalf("回退时不应连着重复最近一条，实际 %+v", repeated)
	}

	// 完全没有候选才报耗尽。
	empty := NewShortFeedService(&VideoService{})
	setupVideoServiceTestDB(t)
	if _, err := empty.NextItem(nil); !errors.Is(err, ErrShortFeedNoEligibleVideos) {
		t.Fatalf("没有任何候选时应报耗尽，实际 %v", err)
	}
}

// 性能回归：以前每次抽取都会把整库每个文件 stat 一遍，外置盘上几千次系统调用
// 会让每一次划动都等好几秒。现在只 stat 被选中的那一条。
func TestShortFeedNextItemStatsOnlyTheSelectedFile(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := NewShortFeedService(&VideoService{})
	svc.SetImageThumbnailService(NewImageThumbnailService(t.TempDir()))

	const n = 40
	for i := 0; i < n; i++ {
		createShortFeedVideo(t, root, fmt.Sprintf("clip-%d.mp4", i), 30, false)
		createShortFeedImage(t, root, fmt.Sprintf("photo-%d.jpg", i), "jpg", false)
	}

	statCount := 0
	svc.statFile = func(path string) (os.FileInfo, error) {
		statCount++
		return os.Stat(path)
	}

	if _, err := svc.NextItem(nil); err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	if statCount != 1 {
		t.Fatalf("一次抽取应只 stat 被选中的那一条，实际 %d 次（候选共 %d 条）", statCount, n*2)
	}

	// 候选快照命中时也不该重新扫库。
	statCount = 0
	if _, err := svc.NextItem(nil); err != nil {
		t.Fatalf("第二次抽取失败: %v", err)
	}
	if statCount != 1 {
		t.Fatalf("第二次抽取同样只应 stat 一条，实际 %d 次", statCount)
	}
}

// 选中项的文件不在了：标记 stale 并另选一条，而不是把这次划动报成失败。
func TestShortFeedNextItemRetriesWhenSelectedFileIsMissing(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := NewShortFeedService(&VideoService{})

	missing := createShortFeedVideo(t, root, "gone.mp4", 30, false)
	alive := createShortFeedVideo(t, root, "alive.mp4", 30, false)
	if err := os.Remove(missing.Path); err != nil {
		t.Fatalf("删除文件失败: %v", err)
	}
	// 先抽到已缺失的那条。
	svc.randFloat64 = func() float64 { return 0 }

	item, err := svc.NextItem(nil)
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	if item.ID != alive.ID {
		t.Fatalf("应跳过缺失文件另选一条，实际选中 %d", item.ID)
	}
	var reloaded models.Video
	if err := database.DB.First(&reloaded, missing.ID).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	if !reloaded.IsStale {
		t.Fatal("缺失文件的视频应被标记为 stale")
	}
}

// 手机端不该被塞一张几十 MB 的原图：超过阈值改发降采样后的 JPEG。
func TestShortFeedLargeImageIsNotServedAsOriginal(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := NewShortFeedService(&VideoService{})
	svc.SetImageThumbnailService(NewImageThumbnailService(t.TempDir()))

	small := createShortFeedImage(t, root, "small.jpg", "jpg", false)
	media, err := svc.ResolveMedia(imageRef(small.ID))
	if err != nil {
		t.Fatalf("解析小图失败: %v", err)
	}
	if media.Path != small.Path {
		t.Fatalf("小图应直出原文件，实际 %q", media.Path)
	}

	// 造一张超过直出阈值的"大图"。转码在测试环境多半不可用，
	// 此时必须退回原文件而不是报错——宁可慢也要能看。
	bigPath := filepath.Join(root, "big.jpg")
	if err := os.WriteFile(bigPath, make([]byte, shortFeedInlineImageMaxBytes+1024), 0644); err != nil {
		t.Fatalf("写入大图失败: %v", err)
	}
	big := models.Image{Name: "big.jpg", Path: bigPath, Directory: root, Size: shortFeedInlineImageMaxBytes + 1024, Format: "jpg"}
	if err := database.DB.Create(&big).Error; err != nil {
		t.Fatalf("创建大图记录失败: %v", err)
	}
	if _, err := svc.ResolveMedia(imageRef(big.ID)); err != nil {
		t.Fatalf("大图解析不应失败（转码不可用时应退回原文件）: %v", err)
	}
}

// 收藏页把两种媒体都带出来。
func TestShortFeedFavoritesIncludeImages(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := NewShortFeedService(&VideoService{})
	svc.SetImageThumbnailService(NewImageThumbnailService(t.TempDir()))

	video := createShortFeedVideo(t, root, "clip.mp4", 30, false)
	img := createShortFeedImage(t, root, "photo.jpg", "jpg", false)
	if _, err := svc.SetFavorited(videoRef(video.ID), true); err != nil {
		t.Fatalf("收藏视频失败: %v", err)
	}
	if _, err := svc.SetFavorited(imageRef(img.ID), true); err != nil {
		t.Fatalf("收藏图片失败: %v", err)
	}

	items, err := svc.FavoriteItems()
	if err != nil {
		t.Fatalf("读取收藏失败: %v", err)
	}
	seen := map[ShortFeedMediaKind]bool{}
	for _, item := range items {
		seen[item.MediaKind] = true
	}
	if !seen[ShortFeedMediaVideo] || !seen[ShortFeedMediaImage] {
		t.Fatalf("收藏页应同时含视频与图片，实际 %+v", items)
	}
}

func videoRef(id uint) ShortFeedMediaRef {
	return ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: id}
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
	next, err := svc.NextItem(nil)
	if err != nil {
		t.Fatalf("获取下一个视频失败: %v", err)
	}
	if next.ID != otherVideo.ID {
		t.Fatalf("高随机区间仍应能选到非偏好视频，got=%d want=%d", next.ID, otherVideo.ID)
	}
}

func TestShortFeedUsesConfiguredMaxDuration(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	inRange := createShortFeedVideo(t, root, "eight-minutes.mp4", 8*60, false)
	createShortFeedVideo(t, root, "eleven-minutes.mp4", 11*60, false)

	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").
		Update("short_feed_max_duration_minutes", 10).Error; err != nil {
		t.Fatalf("更新短视频时长设置失败: %v", err)
	}

	svc := NewShortFeedService(&VideoService{})
	next, err := svc.NextItem(nil)
	if err != nil {
		t.Fatalf("获取下一个视频失败: %v", err)
	}
	if next.ID != inRange.ID {
		t.Fatalf("应只选中配置范围内的视频，got=%d want=%d", next.ID, inRange.ID)
	}
}

func TestShortFeedSkipsMissingFilesBeforeReturningNextVideo(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	missing := createShortFeedVideo(t, root, "missing.mp4", 60, false)
	existing := createShortFeedVideo(t, root, "existing.mp4", 70, false)
	if err := os.Remove(missing.Path); err != nil {
		t.Fatalf("删除测试视频失败: %v", err)
	}

	svc := NewShortFeedService(&VideoService{})
	next, err := svc.NextItem(nil)
	if err != nil {
		t.Fatalf("获取下一个视频失败: %v", err)
	}
	if next.ID != existing.ID {
		t.Fatalf("应跳过缺失文件并返回存在的视频，got=%d want=%d", next.ID, existing.ID)
	}
	// 标记 stale 只在缺失项被抽中时发生——不再每次划动都把整库巡检一遍，
	// 那正是外置盘上卡顿的来源。被抽中时的 stale 标记见
	// TestShortFeedNextItemRetriesWhenSelectedFileIsMissing。
}

func TestShortFeedFeedbackSyncIsIdempotent(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	tag := createShortFeedTag(t, "人物")
	video := createShortFeedVideo(t, root, "tagged.mp4", 80, false, &tag)
	beforeTags := countShortFeedRows(t, "tags")
	beforeVideoTags := countShortFeedRows(t, "video_tags")

	svc := NewShortFeedService(&VideoService{})
	if _, err := svc.SetLiked(videoRef(video.ID), true); err != nil {
		t.Fatalf("设置喜欢失败: %v", err)
	}
	if _, err := svc.SetLiked(videoRef(video.ID), true); err != nil {
		t.Fatalf("重复设置喜欢失败: %v", err)
	}
	if _, err := svc.SetFavorited(videoRef(video.ID), true); err != nil {
		t.Fatalf("设置收藏失败: %v", err)
	}

	if got := countShortFeedRows(t, "tags"); got != beforeTags+1 {
		t.Fatalf("喜欢应创建一个自动标签，got=%d want=%d", got, beforeTags+1)
	}
	if got := countShortFeedRows(t, "video_tags"); got != beforeVideoTags+1 {
		t.Fatalf("喜欢应只增加一个自动标签关联，got=%d want=%d", got, beforeVideoTags+1)
	}
	var likedTag models.Tag
	if err := database.DB.Where("automatic_kind = ?", shortFeedLikedTagKind).First(&likedTag).Error; err != nil {
		t.Fatalf("读取喜欢自动标签失败: %v", err)
	}
	if likedTag.Name != ShortFeedLikedTagName {
		t.Fatalf("喜欢自动标签名称=%q", likedTag.Name)
	}
	var reloaded models.Video
	if err := database.DB.First(&reloaded, video.ID).Error; err != nil || !reloaded.IsFavorite {
		t.Fatalf("手机收藏应同步到主片库: favorite=%v err=%v", reloaded.IsFavorite, err)
	}
	var preference models.ShortFeedTagPreference
	if err := database.DB.Where("tag_id = ?", tag.ID).First(&preference).Error; err != nil {
		t.Fatalf("应记录已有 tag 的偏好: %v", err)
	}
	if preference.Score != ShortFeedPreferenceStep {
		t.Fatalf("重复 liked=true 应保持 set-state 幂等，score=%.2f", preference.Score)
	}

	if _, err := svc.SetLiked(videoRef(video.ID), false); err != nil {
		t.Fatalf("取消喜欢失败: %v", err)
	}
	if got := countShortFeedRows(t, "tags"); got != beforeTags+1 {
		t.Fatalf("取消喜欢不应删除自动标签，got=%d", got)
	}
	if got := countShortFeedRows(t, "video_tags"); got != beforeVideoTags {
		t.Fatalf("取消喜欢应移除自动标签关联，got=%d want=%d", got, beforeVideoTags)
	}
	if err := database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Update("is_favorite", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncFeedback(); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.First(&reloaded, video.ID).Error; err != nil || reloaded.IsFavorite {
		t.Fatalf("主库手动取消收藏不应被后续同步覆盖: favorite=%v err=%v", reloaded.IsFavorite, err)
	}

	if _, err := svc.SetFavorited(videoRef(video.ID), false); err != nil {
		t.Fatalf("手机取消收藏失败: %v", err)
	}
	if err := database.DB.First(&reloaded, video.ID).Error; err != nil || reloaded.IsFavorite {
		t.Fatalf("手机取消收藏不应改变主库状态: favorite=%v err=%v", reloaded.IsFavorite, err)
	}
	if _, err := svc.SetFavorited(videoRef(video.ID), true); err != nil {
		t.Fatalf("手机重新收藏失败: %v", err)
	}
	if err := database.DB.First(&reloaded, video.ID).Error; err != nil || !reloaded.IsFavorite {
		t.Fatalf("手机端新的收藏动作应重新投影到主库: favorite=%v err=%v", reloaded.IsFavorite, err)
	}
}

func TestShortFeedFeedbackSyncDisabledDoesNotRemoveExistingProjection(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	video := createShortFeedVideo(t, root, "disabled.mp4", 80, false)
	svc := NewShortFeedService(&VideoService{})
	if _, err := svc.SetLiked(videoRef(video.ID), true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetFavorited(videoRef(video.ID), true); err != nil {
		t.Fatal(err)
	}
	beforeVideoTags := countShortFeedRows(t, "video_tags")
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").Update("short_feed_feedback_sync_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetLiked(videoRef(video.ID), false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetFavorited(videoRef(video.ID), false); err != nil {
		t.Fatal(err)
	}
	if got := countShortFeedRows(t, "video_tags"); got != beforeVideoTags {
		t.Fatalf("关闭同步不得删除既有标签关联，got=%d want=%d", got, beforeVideoTags)
	}
	var reloaded models.Video
	if err := database.DB.First(&reloaded, video.ID).Error; err != nil || !reloaded.IsFavorite {
		t.Fatalf("关闭同步不得清除既有主片库收藏: favorite=%v err=%v", reloaded.IsFavorite, err)
	}
}

func TestShortFeedPlaybackAndDeleteUseExistingSemantics(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	video := createShortFeedVideo(t, root, "play.mp4", 120, false)

	svc := NewShortFeedService(&VideoService{})
	if _, err := svc.RecordPlayback(videoRef(video.ID)); err != nil {
		t.Fatalf("记录播放失败: %v", err)
	}
	var afterPlay models.Video
	if err := database.DB.First(&afterPlay, video.ID).Error; err != nil {
		t.Fatalf("读取播放后视频失败: %v", err)
	}
	if afterPlay.RandomPlayCount != 1 || afterPlay.LastPlayedAt == nil {
		t.Fatalf("播放统计未更新 random=%d last=%v", afterPlay.RandomPlayCount, afterPlay.LastPlayedAt)
	}

	if err := svc.DeleteItem(videoRef(video.ID)); err != nil {
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
	formReq := httptest.NewRequest(http.MethodPost, "/short-api/items/video/1/like", strings.NewReader("liked=true"))
	formReq.RemoteAddr = "127.0.0.1:1234"
	formReq.Host = "127.0.0.1:18088"
	formReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(formWrite, formReq)
	if formWrite.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form mutation 应被拒绝，got=%d", formWrite.Code)
	}

	missingBody := httptest.NewRecorder()
	missingBodyReq := httptest.NewRequest(http.MethodPost, "/short-api/items/video/"+strconvUint(video.ID)+"/like", nil)
	missingBodyReq.RemoteAddr = "127.0.0.1:1234"
	missingBodyReq.Host = "127.0.0.1:18088"
	missingBodyReq.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(missingBody, missingBodyReq)
	if missingBody.Code != http.StatusBadRequest {
		t.Fatalf("missing JSON body 应被拒绝，got=%d", missingBody.Code)
	}

	originMismatch := httptest.NewRecorder()
	originReq := httptest.NewRequest(http.MethodPost, "/short-api/items/video/"+strconvUint(video.ID)+"/like", strings.NewReader(`{"liked":true}`))
	originReq.RemoteAddr = "127.0.0.1:1234"
	originReq.Host = "127.0.0.1:18088"
	originReq.Header.Set("Content-Type", "application/json")
	originReq.Header.Set("Origin", "http://example.test")
	handler.ServeHTTP(originMismatch, originReq)
	if originMismatch.Code != http.StatusForbidden {
		t.Fatalf("Origin mismatch 应被拒绝，got=%d", originMismatch.Code)
	}

	rangeResp := httptest.NewRecorder()
	rangeReq := httptest.NewRequest(http.MethodGet, "/short-media/video/"+strconvUint(video.ID), nil)
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
