package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// ---------------------------------------------------------------- 共享契约

type fakeWatchObserver struct{ calls []bool }

func (f *fakeWatchObserver) OnVideoWatchedChangedContext(_ context.Context, videoID uint, watched bool) {
	f.calls = append(f.calls, watched)
}

type fakeLinkedSetter struct{ last bool }

func (f *fakeLinkedSetter) SetVideoWatchedFromLink(videoID uint, watched bool) error {
	f.last = watched
	return nil
}

// 编译期断言：接口形状是下游切片依赖的契约，改签名会在这里直接编译失败。
var (
	_ WatchStateObserver     = (*fakeWatchObserver)(nil)
	_ LinkedVideoWatchSetter = (*fakeLinkedSetter)(nil)
)

func TestProductCompletenessSharedContracts(t *testing.T) {
	if personTagNamespace != "人物" {
		t.Fatalf("personTagNamespace 必须是「人物」，实际 %q", personTagNamespace)
	}
	sentinels := []error{
		ErrVideoBlockedByUserDelete, ErrTrashUnsupportedVolume, ErrTrashPermissionDenied, ErrTrashIdentityMismatch,
	}
	for i, left := range sentinels {
		if left == nil || left.Error() == "" {
			t.Fatalf("哨兵错误 %d 不能为空", i)
		}
		if !errors.Is(fmt.Errorf("包装: %w", left), left) {
			t.Fatalf("哨兵错误 %v 应能被 errors.Is 识别", left)
		}
		for j, right := range sentinels {
			if i != j && errors.Is(left, right) {
				t.Fatalf("哨兵错误 %v 与 %v 不该互相等价", left, right)
			}
		}
		// G-3：面向用户的文案不含绝对路径。
		if strings.Contains(left.Error(), "/") {
			t.Fatalf("哨兵错误文案不得含路径: %q", left.Error())
		}
	}
	// 失效原因与回收站模式的取值是数据契约，写错一个字符下游切片全部对不上。
	for got, want := range map[string]string{
		models.StaleReasonOfflineRoot: "offline_root", models.StaleReasonMissingFile: "missing_file",
		models.StaleReasonRemovedRoot: "removed_root", models.StaleReasonOutsideRoots: "outside_roots",
		models.StaleReasonPlayFailed: "play_failed", models.StaleReasonReadError: "read_error",
		models.StaleReasonWatcherMissing: "watcher_missing",
		models.TrashModeTrash:            "trash", models.TrashModeLegacyTrash: "legacy_trash",
		models.TrashModeRecordOnly: "record_only", models.TrashModeMissing: "missing",
		models.TrashStateFileGone: "file_gone",
	} {
		if got != want {
			t.Fatalf("常量取值应为 %q，实际 %q", want, got)
		}
	}
	// 登记表：image_cleanup 排在 cleanup 之后。
	keys := BackgroundTaskKeys()
	position := map[string]int{}
	for index, key := range keys {
		position[key] = index
	}
	cleanup, hasCleanup := position["cleanup"]
	imageCleanup, hasImageCleanup := position["image_cleanup"]
	if !hasCleanup || !hasImageCleanup || imageCleanup != cleanup+1 {
		t.Fatalf("image_cleanup 应紧跟在 cleanup 之后: cleanup=%d image_cleanup=%d keys=%v", cleanup, imageCleanup, keys)
	}
	registry := NewBackgroundTaskRegistry()
	registry.Begin(BackgroundTaskImageCleanup)
	if got := registry.Snapshot(); len(got) != 1 || got[0] != "image_cleanup" {
		t.Fatalf("image_cleanup 应能登记: %v", got)
	}
	registry.End(BackgroundTaskImageCleanup)
}

// ---------------------------------------------------------------- UpdateSettings

// 三个清理阈值进白名单：保存后读回；<=0 归一化成默认值 5 / 480 / 320。
func TestUpdateSettingsPersistsCleanupThresholds(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := &SettingsService{}

	if err := service.UpdateSettings(models.Settings{CleanupShortSeconds: 8, CleanupLowWidth: 640, CleanupLowHeight: 360}); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	saved, err := service.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if saved.CleanupShortSeconds != 8 || saved.CleanupLowWidth != 640 || saved.CleanupLowHeight != 360 {
		t.Fatalf("清理阈值未保存: %d/%d/%d", saved.CleanupShortSeconds, saved.CleanupLowWidth, saved.CleanupLowHeight)
	}

	if err := service.UpdateSettings(models.Settings{CleanupShortSeconds: 0, CleanupLowWidth: -1}); err != nil {
		t.Fatal(err)
	}
	saved, _ = service.GetSettings()
	if saved.CleanupShortSeconds != database.DefaultCleanupShortSeconds ||
		saved.CleanupLowWidth != database.DefaultCleanupLowWidth ||
		saved.CleanupLowHeight != database.DefaultCleanupLowHeight {
		t.Fatalf("<=0 应归一化为默认 5/480/320，实际 %d/%d/%d", saved.CleanupShortSeconds, saved.CleanupLowWidth, saved.CleanupLowHeight)
	}
}

// 通用设置保存用整行 Save：读到快照与写回之间，专用路径（SetShortFeedEnabled / SetShortFeedPIN /
// 收藏合并）写下的值不能被旧快照回滚。用一个 before-update 回调在 Save 执行前偷偷改这三列，
// 模拟「读快照之后别的路径写了」；Omit 不完整的话，Save 会把快照里的旧值写回。
func TestUpdateSettingsDoesNotWriteBackDedicatedColumns(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := &SettingsService{}
	stamp := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	fired := 0
	const hookName = "test:concurrent_dedicated_writer"
	err := database.DB.Callback().Update().Before("gorm:update").Register(hookName, func(tx *gorm.DB) {
		if tx.Statement.Table != "settings" || fired > 0 {
			return
		}
		fired++
		other := tx.Session(&gorm.Session{NewDB: true})
		if err := other.Exec("UPDATE settings SET short_feed_enabled = ?, short_feed_pin_hash = ?, favorites_unified_at = ?",
			true, "bcrypt-hash-written-by-dedicated-path", stamp).Error; err != nil {
			tx.AddError(err)
		}
	})
	if err != nil {
		t.Fatalf("注册测试回调失败: %v", err)
	}
	t.Cleanup(func() { _ = database.DB.Callback().Update().Remove(hookName) })

	// 通用保存尝试把这三列写成别的值：既不能靠入参改到它们，也不能被旧快照回滚。
	if err := service.UpdateSettings(models.Settings{
		ShortFeedEnabled: false, ShortFeedPINHash: "attacker-value", CleanupShortSeconds: 6,
	}); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	if fired != 1 {
		t.Fatalf("测试回调应恰好触发一次，实际 %d（Save 没走 update 回调？）", fired)
	}
	var stored models.Settings
	if err := database.DB.First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.ShortFeedEnabled {
		t.Fatal("short_feed_enabled 被通用保存回写成了旧值")
	}
	if stored.ShortFeedPINHash != "bcrypt-hash-written-by-dedicated-path" {
		t.Fatalf("short_feed_pin_hash 被通用保存回写: %q", stored.ShortFeedPINHash)
	}
	if stored.FavoritesUnifiedAt == nil || !stored.FavoritesUnifiedAt.Equal(stamp) {
		t.Fatalf("favorites_unified_at 被通用保存回写: %v", stored.FavoritesUnifiedAt)
	}
	if stored.CleanupShortSeconds != 6 {
		t.Fatalf("白名单里的列仍应正常保存，实际 %d", stored.CleanupShortSeconds)
	}
}

// ---------------------------------------------------------------- 收藏与点赞 setter

func TestSetVideoFavoriteMaintainsFavoritedAt(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := &VideoService{}
	video := models.Video{Name: "a.mp4", Path: "/lib/a.mp4", Directory: "/lib", Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}

	before := time.Now().Add(-time.Second)
	got, err := service.SetVideoFavorite(video.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsFavorite || got.FavoritedAt == nil || got.FavoritedAt.Before(before) {
		t.Fatalf("收藏应写入 favorited_at: %+v", got)
	}
	first := *got.FavoritedAt

	// 对已收藏的视频重复置 true：保留原时间，不把它顶到「最近收藏」最前面。
	time.Sleep(20 * time.Millisecond)
	again, err := service.SetVideoFavorite(video.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if again.FavoritedAt == nil || !again.FavoritedAt.Equal(first) {
		t.Fatalf("重复收藏不该刷新 favorited_at: first=%v again=%v", first, again.FavoritedAt)
	}

	cleared, err := service.SetVideoFavorite(video.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.IsFavorite || cleared.FavoritedAt != nil {
		t.Fatalf("取消收藏应清空 favorited_at: %+v", cleared)
	}

	if _, err := service.SetVideoFavorite(999999, true); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("不存在的视频应返回 ErrRecordNotFound，实际 %v", err)
	}
	if _, err := service.SetVideoFavorite(0, true); err == nil {
		t.Fatal("视频 ID 为 0 应报错")
	}
}

func TestSetVideoLikedIsIsomorphicToFavoriteAndLeavesFavoriteAlone(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := &VideoService{}
	tag := models.Tag{Name: "夜景", Color: "#0f8f82"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "a.mp4", Path: "/lib/a.mp4", Directory: "/lib", Size: 1}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&video).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetVideoFavorite(video.ID, true); err != nil {
		t.Fatal(err)
	}

	liked, err := service.SetVideoLiked(video.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !liked.IsLiked || !liked.IsFavorite || liked.FavoritedAt == nil {
		t.Fatalf("点赞不该影响收藏与收藏时间: %+v", liked)
	}
	if len(liked.Tags) != 1 || liked.Tags[0].ID != tag.ID {
		t.Fatalf("返回值应带标签（前端整行覆盖列表项）: %+v", liked.Tags)
	}
	unliked, err := service.SetVideoLiked(video.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if unliked.IsLiked || !unliked.IsFavorite {
		t.Fatalf("取消点赞只改 is_liked: %+v", unliked)
	}
	if _, err := service.SetVideoLiked(999999, true); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("不存在的视频应返回 ErrRecordNotFound，实际 %v", err)
	}
	if _, err := service.SetVideoLiked(0, true); err == nil {
		t.Fatal("视频 ID 为 0 应报错")
	}
}

func TestSetImageFavoriteAndLikedMaintainColumns(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := &ImageLibraryService{}
	image := models.Image{Name: "a.jpg", Path: "/img/a.jpg", Directory: "/img", Size: 1}
	if err := database.DB.Create(&image).Error; err != nil {
		t.Fatal(err)
	}

	favorite, err := service.SetImageFavorite(image.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !favorite.IsFavorite || favorite.FavoritedAt == nil {
		t.Fatalf("图片收藏应写入 favorited_at: %+v", favorite)
	}
	first := *favorite.FavoritedAt
	time.Sleep(20 * time.Millisecond)
	again, err := service.SetImageFavorite(image.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if again.FavoritedAt == nil || !again.FavoritedAt.Equal(first) {
		t.Fatalf("重复收藏不该刷新 favorited_at: %v vs %v", first, again.FavoritedAt)
	}

	liked, err := service.SetImageLiked(image.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !liked.IsLiked || !liked.IsFavorite || liked.FavoritedAt == nil {
		t.Fatalf("图片点赞不该影响收藏: %+v", liked)
	}
	if _, err := service.SetImageLiked(image.ID, false); err != nil {
		t.Fatal(err)
	}

	cleared, err := service.SetImageFavorite(image.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.IsFavorite || cleared.FavoritedAt != nil {
		t.Fatalf("取消收藏应清空 favorited_at: %+v", cleared)
	}
	if _, err := service.SetImageLiked(999999, true); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("不存在的图片应返回 ErrRecordNotFound，实际 %v", err)
	}
	if _, err := service.SetImageLiked(0, true); err == nil {
		t.Fatal("图片 ID 为 0 应报错")
	}
}
