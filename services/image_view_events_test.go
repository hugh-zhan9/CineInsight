package services

import (
	"context"
	"errors"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

func recordImageViewAt(t *testing.T, imageID uint, source string, at time.Time) (bool, error) {
	t.Helper()
	var recorded bool
	err := database.Transaction(func(tx *gorm.DB) error {
		var err error
		recorded, err = recordImageViewTx(tx, imageID, source, at)
		return err
	})
	return recorded, err
}

func imageViewEventCount(t *testing.T, imageID uint) int64 {
	t.Helper()
	var count int64
	if err := database.DB.Model(&models.ImageViewEvent{}).Where("image_id = ?", imageID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

// 同一图片同一来源 30 分钟内只记一次（含边界：29:59 不记，整 30 分钟记），不同来源各自去重；
// view_count 与事件同步递增，last_viewed_at 只向前推进。
func TestRecordImageViewDedupesPerSourceWithinThirtyMinutes(t *testing.T) {
	setupVideoServiceTestDB(t)
	img := createShortFeedImage(t, t.TempDir(), "a.jpg", "jpg", false)
	t0 := time.Date(2026, 10, 11, 9, 0, 0, 0, time.Local)
	steps := []struct {
		source string
		at     time.Time
		want   bool
	}{
		{models.ImageViewSourceDesktopViewer, t0, true},
		{models.ImageViewSourceDesktopViewer, t0.Add(29*time.Minute + 59*time.Second), false},
		{models.ImageViewSourceMobileFeed, t0.Add(time.Minute), true},
		{models.ImageViewSourceDesktopViewer, t0.Add(30 * time.Minute), true},
		{models.ImageViewSourceDesktopViewer, t0.Add(45 * time.Minute), false},
	}
	for index, step := range steps {
		got, err := recordImageViewAt(t, img.ID, step.source, step.at)
		if err != nil || got != step.want {
			t.Fatalf("step %d: recorded=%v err=%v, want %v", index, got, err, step.want)
		}
	}
	if count := imageViewEventCount(t, img.ID); count != 3 {
		t.Fatalf("应只有 3 条事件，实际 %d", count)
	}
	var reloaded models.Image
	if err := database.DB.First(&reloaded, img.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.ViewCount != 3 || reloaded.LastViewedAt == nil || !reloaded.LastViewedAt.Equal(t0.Add(30*time.Minute)) {
		t.Fatalf("view_count / last_viewed_at 应与事件一致: %+v %v", reloaded.ViewCount, reloaded.LastViewedAt)
	}
}

// 软删除的图片不再记查看（回滚，不留半截）；已有事件保留；永久删除时级联清掉事件。
func TestRecordImageViewSoftDeleteKeepsEventsAndHardDeleteCascades(t *testing.T) {
	setupVideoServiceTestDB(t)
	img := createShortFeedImage(t, t.TempDir(), "b.jpg", "jpg", false)
	at := time.Now().Add(-time.Hour)
	if ok, err := recordImageViewAt(t, img.ID, models.ImageViewSourceDesktopViewer, at); err != nil || !ok {
		t.Fatalf("首次查看应记录: %v %v", ok, err)
	}
	if err := database.DB.Delete(&models.Image{}, img.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := recordImageViewAt(t, img.ID, models.ImageViewSourceMobileFeed, at.Add(time.Minute)); !errors.Is(err, errImageViewMissing) {
		t.Fatalf("软删图片应返回 errImageViewMissing，实际 %v", err)
	}
	if count := imageViewEventCount(t, img.ID); count != 1 {
		t.Fatalf("软删后事件应保留且没有新增，实际 %d", count)
	}
	if err := database.DB.Unscoped().Delete(&models.Image{}, img.ID).Error; err != nil {
		t.Fatal(err)
	}
	if count := imageViewEventCount(t, img.ID); count != 0 {
		t.Fatalf("永久删除应级联清除事件，实际 %d", count)
	}
}

// 桌面入口只接受 desktop_viewer；新记事件时发一次 recap-activity，去重命中时不发。
func TestRecordImageViewDesktopEntryNotifiesOnlyWhenRecorded(t *testing.T) {
	setupVideoServiceTestDB(t)
	img := createShortFeedImage(t, t.TempDir(), "c.jpg", "jpg", false)
	var activities []RecapActivity
	SetRecapActivityListener(func(activity RecapActivity) { activities = append(activities, activity) })
	t.Cleanup(func() { SetRecapActivityListener(nil) })
	service := NewImageService()
	if _, err := service.RecordImageView(context.Background(), img.ID, models.ImageViewSourceMobileFeed); err == nil {
		t.Fatal("桌面入口不应接受 mobile_feed")
	}
	first, err := service.RecordImageView(context.Background(), img.ID, models.ImageViewSourceDesktopViewer)
	if err != nil || !first.Recorded || first.ViewCount != 1 || first.LastViewedAt == nil {
		t.Fatalf("首次查看应记录: %+v %v", first, err)
	}
	second, err := service.RecordImageView(context.Background(), img.ID, models.ImageViewSourceDesktopViewer)
	if err != nil || second.Recorded || second.ViewCount != 1 {
		t.Fatalf("30 分钟内重复查看不应再记: %+v %v", second, err)
	}
	if len(activities) != 1 || activities[0].Kind != RecapActivityImage || activities[0].Source != models.ImageViewSourceDesktopViewer {
		t.Fatalf("应只通知一次图片活动: %+v", activities)
	}
	if _, err := service.RecordImageView(context.Background(), 999999, models.ImageViewSourceDesktopViewer); !errors.Is(err, errImageViewMissing) {
		t.Fatalf("不存在的图片应报 errImageViewMissing，实际 %v", err)
	}
}

// 手机端刷到图片：互动表浏览次数照旧每次 +1，查看事件与 images.view_count 按 30 分钟去重。
func TestShortFeedImageViewAppendsDedupedViewEvent(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	svc := NewShortFeedService(&VideoService{})
	svc.SetImageThumbnailService(NewImageThumbnailService(t.TempDir()))
	img := createShortFeedImage(t, root, "feed.jpg", "jpg", false)
	clock := time.Date(2026, 10, 11, 20, 0, 0, 0, time.Local)
	svc.now = func() time.Time { return clock }
	for _, offset := range []time.Duration{0, 10 * time.Minute, 31 * time.Minute} {
		clock = time.Date(2026, 10, 11, 20, 0, 0, 0, time.Local).Add(offset)
		if _, err := svc.RecordPlayback(imageRef(img.ID)); err != nil {
			t.Fatalf("手机端记录图片浏览失败: %v", err)
		}
	}
	var interaction models.ShortFeedImageInteraction
	if err := database.DB.Where("image_id = ?", img.ID).First(&interaction).Error; err != nil {
		t.Fatal(err)
	}
	if interaction.ViewCount != 3 {
		t.Fatalf("互动表浏览次数应保持每次递增，实际 %d", interaction.ViewCount)
	}
	var events []models.ImageViewEvent
	if err := database.DB.Where("image_id = ?", img.ID).Order("viewed_at ASC").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Source != models.ImageViewSourceMobileFeed {
		t.Fatalf("应记 2 条 mobile_feed 事件（第二次落在 30 分钟内）: %+v", events)
	}
	var reloaded models.Image
	if err := database.DB.First(&reloaded, img.ID).Error; err != nil || reloaded.ViewCount != 2 {
		t.Fatalf("images.view_count 应与事件一致: %d %v", reloaded.ViewCount, err)
	}
}
