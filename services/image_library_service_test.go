package services

import (
	"errors"
	"fmt"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

func mustCreateTestImage(t *testing.T, name string, size int64) *models.Image {
	t.Helper()
	image := &models.Image{
		Name:      name,
		Path:      "/tmp/image-library/" + name,
		Directory: "/tmp/image-library",
		Size:      size,
		Format:    "jpg",
	}
	if err := database.DB.Create(image).Error; err != nil {
		t.Fatalf("创建图片失败: %v", err)
	}
	return image
}

func mustCreateImageTag(t *testing.T, name string) *models.Tag {
	t.Helper()
	tag := &models.Tag{Name: name, Color: "#123456", IsActive: true}
	if err := database.DB.Create(tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	return tag
}

func imageTagLinkCount(t *testing.T, imageID, tagID uint) int64 {
	t.Helper()
	var count int64
	if err := database.DB.Table("image_tags").Where("image_id = ? AND tag_id = ?", imageID, tagID).Count(&count).Error; err != nil {
		t.Fatalf("统计图片标签关联失败: %v", err)
	}
	return count
}

func searchImageIDs(t *testing.T, filter ImageFilter) []uint {
	t.Helper()
	page, err := NewImageLibraryService().SearchImagePage(ImagePageRequest{Filter: filter, Limit: 200})
	if err != nil {
		t.Fatalf("照片页查询失败: %v", err)
	}
	ids := make([]uint, 0, len(page.Images))
	for _, image := range page.Images {
		ids = append(ids, image.ID)
	}
	return ids
}

func TestImagePageFiltersByTagFavoriteRatingSizeAndKeyword(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageLibraryService()
	alpha := mustCreateTestImage(t, "Alpha.JPG", 100)
	beta := mustCreateTestImage(t, "beta.png", 2048)
	gamma := mustCreateTestImage(t, "gamma.heic", 4096)
	travel := mustCreateImageTag(t, "旅行")
	family := mustCreateImageTag(t, "家庭")

	// TC-3: 打标后按标签筛选命中。
	if err := svc.AddTagToImage(alpha.ID, travel.ID); err != nil {
		t.Fatalf("打标失败: %v", err)
	}
	if err := svc.AddTagToImage(beta.ID, travel.ID); err != nil {
		t.Fatalf("打标失败: %v", err)
	}
	if err := svc.AddTagToImage(beta.ID, family.ID); err != nil {
		t.Fatalf("打标失败: %v", err)
	}
	if _, err := svc.SetImageFavorite(alpha.ID, true); err != nil {
		t.Fatalf("收藏失败: %v", err)
	}
	if _, err := svc.SetImageRating(alpha.ID, ratingPointer(8)); err != nil {
		t.Fatalf("评分失败: %v", err)
	}
	if _, err := svc.SetImageRating(beta.ID, ratingPointer(6.5)); err != nil {
		t.Fatalf("评分失败: %v", err)
	}

	if got := searchImageIDs(t, ImageFilter{TagIDs: []uint{travel.ID}}); len(got) != 2 {
		t.Fatalf("单标签筛选应命中 2 张: %v", got)
	}
	if got := searchImageIDs(t, ImageFilter{TagIDs: []uint{travel.ID, family.ID}}); len(got) != 1 || got[0] != beta.ID {
		t.Fatalf("多标签 AND 筛选应仅命中 beta: %v", got)
	}
	if got := searchImageIDs(t, ImageFilter{FavoriteOnly: true}); len(got) != 1 || got[0] != alpha.ID {
		t.Fatalf("收藏筛选应仅命中 alpha: %v", got)
	}
	if got := searchImageIDs(t, ImageFilter{MinRating: ratingPointer(7)}); len(got) != 1 || got[0] != alpha.ID {
		t.Fatalf("最低评分筛选应仅命中 alpha（NULL 不命中）: %v", got)
	}
	if got := searchImageIDs(t, ImageFilter{MaxRating: ratingPointer(7)}); len(got) != 1 || got[0] != beta.ID {
		t.Fatalf("最高评分筛选应仅命中 beta（NULL 不命中）: %v", got)
	}
	if got := searchImageIDs(t, ImageFilter{MinSize: 1024, MaxSize: 4096}); len(got) != 1 || got[0] != beta.ID {
		t.Fatalf("体积区间应仅命中 beta（上限开区间）: %v", got)
	}
	if got := searchImageIDs(t, ImageFilter{Keyword: "alpha"}); len(got) != 1 || got[0] != alpha.ID {
		t.Fatalf("关键词应大小写不敏感命中 alpha: %v", got)
	}
	if got := searchImageIDs(t, ImageFilter{Keyword: "%"}); len(got) != 0 {
		t.Fatalf("LIKE 通配符应被转义: %v", got)
	}
	_ = gamma

	// 去标后同一标签筛选不再命中。
	if err := svc.RemoveTagFromImage(beta.ID, family.ID); err != nil {
		t.Fatalf("去标失败: %v", err)
	}
	if got := searchImageIDs(t, ImageFilter{TagIDs: []uint{family.ID}}); len(got) != 0 {
		t.Fatalf("去标后不应再命中: %v", got)
	}
}

// AI 描述筛选：undescribed 要覆盖"从未排队""排队中""生成失败"三种没有描述的情况。
func TestImagePageFiltersByAIDescriptionState(t *testing.T) {
	setupVideoServiceTestDB(t)
	described := mustCreateTestImage(t, "described.jpg", 100)
	failed := mustCreateTestImage(t, "failed.jpg", 100)
	pending := mustCreateTestImage(t, "pending.jpg", 100)
	never := mustCreateTestImage(t, "never.jpg", 100)

	rows := []models.ImageAIDescription{
		{ImageID: described.ID, Status: "completed", Description: "海边日落"},
		{ImageID: failed.ID, Status: "failed", ErrorCode: "request_failed"},
		{ImageID: pending.ID, Status: "pending"},
	}
	if err := database.DB.Create(&rows).Error; err != nil {
		t.Fatalf("创建 AI 描述记录失败: %v", err)
	}

	if got := searchImageIDs(t, ImageFilter{AIDescriptionState: ImageAIDescriptionStateDescribed}); len(got) != 1 || got[0] != described.ID {
		t.Fatalf("described 应只命中已生成描述的图片，实际 %v", got)
	}

	undescribed := searchImageIDs(t, ImageFilter{AIDescriptionState: ImageAIDescriptionStateUndescribed})
	if len(undescribed) != 3 {
		t.Fatalf("undescribed 应命中失败/排队中/从未排队三张，实际 %v", undescribed)
	}
	for _, id := range undescribed {
		if id == described.ID {
			t.Fatalf("undescribed 不应包含已生成描述的图片: %v", undescribed)
		}
	}
	for _, want := range []uint{failed.ID, pending.ID, never.ID} {
		found := false
		for _, id := range undescribed {
			if id == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("undescribed 应包含图片 %d，实际 %v", want, undescribed)
		}
	}

	if got := searchImageIDs(t, ImageFilter{AIDescriptionState: ImageAIDescriptionStateFailed}); len(got) != 1 || got[0] != failed.ID {
		t.Fatalf("failed 应只命中生成失败的图片，实际 %v", got)
	}

	if got := searchImageIDs(t, ImageFilter{}); len(got) != 4 {
		t.Fatalf("不筛时应返回全部 4 张，实际 %v", got)
	}

	if _, err := NewImageLibraryService().SearchImagePage(ImagePageRequest{
		Filter: ImageFilter{AIDescriptionState: "bogus"}, Limit: 10,
	}); err == nil {
		t.Fatal("非法的 AI 描述筛选值应报错")
	}
}

func TestImagePageExcludesSoftDeletedRows(t *testing.T) {
	setupVideoServiceTestDB(t)
	kept := mustCreateTestImage(t, "kept.jpg", 10)
	removed := mustCreateTestImage(t, "removed.jpg", 10)
	if err := database.DB.Delete(&models.Image{}, removed.ID).Error; err != nil {
		t.Fatalf("软删除图片失败: %v", err)
	}
	if got := searchImageIDs(t, ImageFilter{}); len(got) != 1 || got[0] != kept.ID {
		t.Fatalf("软删除行不应出现在照片页: %v", got)
	}
}

func TestImagePageRecentCursorPaginationTieBreaksByID(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageLibraryService()
	ids := make([]uint, 0, 5)
	for _, name := range []string{"a.jpg", "b.jpg", "c.jpg", "d.jpg", "e.jpg"} {
		ids = append(ids, mustCreateTestImage(t, name, 10).ID)
	}
	// 全部行使用同一 created_at，翻页确定性只能靠 id 决胜列保证。
	fixed := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := database.DB.Model(&models.Image{}).Where("id IN ?", ids).Update("created_at", fixed).Error; err != nil {
		t.Fatalf("固定创建时间失败: %v", err)
	}

	collected := make([]uint, 0, len(ids))
	var cursor *ImageCursor
	pages := 0
	for {
		page, err := svc.SearchImagePage(ImagePageRequest{Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatalf("翻页失败: %v", err)
		}
		for _, image := range page.Images {
			collected = append(collected, image.ID)
		}
		pages++
		if page.NextCursor == nil {
			break
		}
		if page.NextCursor.SortMode != ImageSortRecent || page.NextCursor.CreatedAt == "" {
			t.Fatalf("recent 游标字段不完整: %+v", page.NextCursor)
		}
		cursor = page.NextCursor
		if pages > 10 {
			t.Fatal("翻页未收敛")
		}
	}
	if pages != 3 || len(collected) != 5 {
		t.Fatalf("应 3 页共 5 张: pages=%d collected=%v", pages, collected)
	}
	seen := make(map[uint]struct{}, len(collected))
	for index, id := range collected {
		if _, dup := seen[id]; dup {
			t.Fatalf("翻页出现重复图片: %v", collected)
		}
		seen[id] = struct{}{}
		if index > 0 && collected[index-1] <= id {
			t.Fatalf("同 created_at 应按 id DESC 决胜: %v", collected)
		}
	}
}

func TestImagePageSizeSortTieBreaksByID(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageLibraryService()
	small := mustCreateTestImage(t, "small.jpg", 10)
	twinA := mustCreateTestImage(t, "twin-a.jpg", 500)
	twinB := mustCreateTestImage(t, "twin-b.jpg", 500)

	page, err := svc.SearchImagePage(ImagePageRequest{Filter: ImageFilter{SortMode: ImageSortSize}, Limit: 2})
	if err != nil {
		t.Fatalf("体积排序查询失败: %v", err)
	}
	if len(page.Images) != 2 || page.Images[0].ID != twinB.ID || page.Images[1].ID != twinA.ID {
		t.Fatalf("同体积应按 id DESC 决胜: %+v", page.Images)
	}
	if page.NextCursor == nil || page.NextCursor.SortMode != ImageSortSize || page.NextCursor.Size != 500 {
		t.Fatalf("体积游标不完整: %+v", page.NextCursor)
	}
	page, err = svc.SearchImagePage(ImagePageRequest{Filter: ImageFilter{SortMode: ImageSortSize}, Cursor: page.NextCursor, Limit: 2})
	if err != nil {
		t.Fatalf("体积排序翻页失败: %v", err)
	}
	if len(page.Images) != 1 || page.Images[0].ID != small.ID || page.NextCursor != nil {
		t.Fatalf("末页应仅剩 small 且无游标: %+v", page.Images)
	}
}

func TestImagePageRatingSortPutsNullLastAcrossCursor(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageLibraryService()
	nullA := mustCreateTestImage(t, "null-a.jpg", 10)
	nullB := mustCreateTestImage(t, "null-b.jpg", 10)
	rated7 := mustCreateTestImage(t, "rated7.jpg", 10)
	rated9 := mustCreateTestImage(t, "rated9.jpg", 10)
	if _, err := svc.SetImageRating(rated7.ID, ratingPointer(7)); err != nil {
		t.Fatalf("评分失败: %v", err)
	}
	if _, err := svc.SetImageRating(rated9.ID, ratingPointer(9)); err != nil {
		t.Fatalf("评分失败: %v", err)
	}

	expected := []uint{rated9.ID, rated7.ID, nullB.ID, nullA.ID}
	collected := make([]uint, 0, len(expected))
	var cursor *ImageCursor
	for range [5]struct{}{} {
		page, err := svc.SearchImagePage(ImagePageRequest{Filter: ImageFilter{SortMode: ImageSortRating}, Cursor: cursor, Limit: 1})
		if err != nil {
			t.Fatalf("评分排序翻页失败: %v", err)
		}
		for _, image := range page.Images {
			collected = append(collected, image.ID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = page.NextCursor
	}
	if len(collected) != len(expected) {
		t.Fatalf("评分排序应返回全部 4 张: %v", collected)
	}
	for index := range expected {
		if collected[index] != expected[index] {
			t.Fatalf("评分排序应 DESC 且 NULL 排后（同为 NULL 按 id DESC）: got=%v want=%v", collected, expected)
		}
	}
}

// mustSetImageTaken 直接改写 taken_at/created_at，避免测试依赖 EXIF 夹具。
func mustSetImageTaken(t *testing.T, imageID uint, takenAt *time.Time, createdAt time.Time) {
	t.Helper()
	if err := database.DB.Model(&models.Image{}).Where("id = ?", imageID).
		Updates(map[string]interface{}{"taken_at": takenAt, "created_at": createdAt}).Error; err != nil {
		t.Fatalf("设置拍摄时间失败: %v", err)
	}
}

// TestImageExifTakenSortFallsBackToCreatedAtAcrossCursor 覆盖「拍摄时间」排序：
// 有 EXIF 用 taken_at，无 EXIF 回退 created_at，两者混排后仍按同一键 DESC + id 决胜。
func TestImageExifTakenSortFallsBackToCreatedAtAcrossCursor(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageLibraryService()
	newest := mustCreateTestImage(t, "newest.jpg", 10)
	middle := mustCreateTestImage(t, "middle.jpg", 10)
	oldest := mustCreateTestImage(t, "oldest.jpg", 10)
	tieLow := mustCreateTestImage(t, "tie-low.jpg", 10)
	tieHigh := mustCreateTestImage(t, "tie-high.jpg", 10)

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	taken := func(offset time.Duration) *time.Time { value := base.Add(offset); return &value }
	// newest/oldest 有 EXIF 时间；middle 没有，靠 created_at 落在两者之间。
	mustSetImageTaken(t, newest.ID, taken(72*time.Hour), base)
	mustSetImageTaken(t, middle.ID, nil, base.Add(48*time.Hour))
	mustSetImageTaken(t, oldest.ID, taken(24*time.Hour), base)
	// 两张排序键完全相同，只能靠 id DESC 决胜。
	mustSetImageTaken(t, tieLow.ID, taken(time.Hour), base)
	mustSetImageTaken(t, tieHigh.ID, taken(time.Hour), base)

	expected := []uint{newest.ID, middle.ID, oldest.ID, tieHigh.ID, tieLow.ID}
	collected := make([]uint, 0, len(expected))
	var cursor *ImageCursor
	for range [6]struct{}{} {
		page, err := svc.SearchImagePage(ImagePageRequest{
			Filter: ImageFilter{SortMode: ImageSortTaken}, Cursor: cursor, Limit: 2,
		})
		if err != nil {
			t.Fatalf("拍摄时间排序翻页失败: %v", err)
		}
		for _, image := range page.Images {
			collected = append(collected, image.ID)
		}
		if page.NextCursor == nil {
			break
		}
		if page.NextCursor.SortMode != ImageSortTaken || page.NextCursor.TakenAt == "" {
			t.Fatalf("taken 游标字段不完整: %+v", page.NextCursor)
		}
		cursor = page.NextCursor
	}
	if len(collected) != len(expected) {
		t.Fatalf("应返回全部 5 张: %v", collected)
	}
	for index := range expected {
		if collected[index] != expected[index] {
			t.Fatalf("拍摄时间排序不符: got=%v want=%v", collected, expected)
		}
	}
}

// TestImageExifTakenRangeFilterMatchesOnlyExifTimes 覆盖拍摄日期区间筛选：闭区间，
// 且无 taken_at 的图片不参与（不会被 created_at 回退带进来）。
func TestImageExifTakenRangeFilterMatchesOnlyExifTimes(t *testing.T) {
	setupVideoServiceTestDB(t)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	inside := mustCreateTestImage(t, "inside.jpg", 10)
	lowerEdge := mustCreateTestImage(t, "lower-edge.jpg", 10)
	upperEdge := mustCreateTestImage(t, "upper-edge.jpg", 10)
	outside := mustCreateTestImage(t, "outside.jpg", 10)
	noExif := mustCreateTestImage(t, "no-exif.jpg", 10)

	taken := func(offset time.Duration) *time.Time { value := base.Add(offset); return &value }
	mustSetImageTaken(t, inside.ID, taken(36*time.Hour), base)
	mustSetImageTaken(t, lowerEdge.ID, taken(24*time.Hour), base)
	mustSetImageTaken(t, upperEdge.ID, taken(48*time.Hour), base)
	mustSetImageTaken(t, outside.ID, taken(96*time.Hour), base)
	// created_at 落在区间内但没有 EXIF 时间，不应被筛出来。
	mustSetImageTaken(t, noExif.ID, nil, base.Add(30*time.Hour))

	after, before := base.Add(24*time.Hour), base.Add(48*time.Hour)
	ids := searchImageIDs(t, ImageFilter{TakenAfter: &after, TakenBefore: &before})
	matched := map[uint]bool{}
	for _, id := range ids {
		matched[id] = true
	}
	if len(ids) != 3 || !matched[inside.ID] || !matched[lowerEdge.ID] || !matched[upperEdge.ID] {
		t.Fatalf("拍摄日期区间应闭区间命中 3 张: %v", ids)
	}
	if matched[outside.ID] || matched[noExif.ID] {
		t.Fatalf("区间外与无 EXIF 时间的图片不应命中: %v", ids)
	}
}

func TestImagePageRejectsInvalidFilterAndCursor(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageLibraryService()

	if _, err := svc.SearchImagePage(ImagePageRequest{Filter: ImageFilter{SortMode: "unknown"}}); err == nil {
		t.Fatal("未知排序模式应被拒绝")
	}
	if _, err := svc.SearchImagePage(ImagePageRequest{Filter: ImageFilter{MinSize: -1}}); err == nil {
		t.Fatal("负数体积筛选应被拒绝")
	}
	if _, err := svc.SearchImagePage(ImagePageRequest{Filter: ImageFilter{MinSize: 10, MaxSize: 10}}); err == nil {
		t.Fatal("体积上限必须大于下限")
	}
	if _, err := svc.SearchImagePage(ImagePageRequest{Filter: ImageFilter{MinRating: ratingPointer(6.3)}}); err == nil {
		t.Fatal("非 0.5 步进评分筛选应被拒绝")
	}
	if _, err := svc.SearchImagePage(ImagePageRequest{Filter: ImageFilter{MinRating: ratingPointer(8), MaxRating: ratingPointer(7)}}); err == nil {
		t.Fatal("评分上限小于下限应被拒绝")
	}
	if _, err := svc.SearchImagePage(ImagePageRequest{Cursor: &ImageCursor{SortMode: ImageSortSize, ID: 1}}); err == nil {
		t.Fatal("游标排序模式与筛选不一致应被拒绝")
	}
	if _, err := svc.SearchImagePage(ImagePageRequest{Cursor: &ImageCursor{SortMode: ImageSortRecent, CreatedAt: "2026-08-01T12:00:00Z"}}); err == nil {
		t.Fatal("游标缺少 ID 应被拒绝")
	}
	if _, err := svc.SearchImagePage(ImagePageRequest{Cursor: &ImageCursor{SortMode: ImageSortRecent, CreatedAt: "not-a-time", ID: 1}}); err == nil {
		t.Fatal("非法时间游标应被拒绝")
	}
	if _, err := svc.SearchImagePage(ImagePageRequest{Cursor: &ImageCursor{SortMode: ImageSortRecent, CreatedAt: "2026-08-01T12:00:00Z", RatingIsNull: true, ID: 1}}); err == nil {
		t.Fatal("recent 游标不应包含评分字段")
	}
	if _, err := svc.SearchImagePage(ImagePageRequest{Filter: ImageFilter{SortMode: ImageSortRating}, Cursor: &ImageCursor{SortMode: ImageSortRating, ID: 1}}); err == nil {
		t.Fatal("评分游标缺少评分值应被拒绝")
	}
	if _, err := svc.SearchImagePage(ImagePageRequest{Filter: ImageFilter{SortMode: ImageSortRating}, Cursor: &ImageCursor{SortMode: ImageSortRating, RatingIsNull: true, Rating: ratingPointer(5), ID: 1}}); err == nil {
		t.Fatal("NULL 评分游标不能同时带评分值")
	}
	takenEarly := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	takenLate := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	if _, err := svc.SearchImagePage(ImagePageRequest{Filter: ImageFilter{TakenAfter: &takenLate, TakenBefore: &takenEarly}}); err == nil {
		t.Fatal("拍摄时间上限早于下限应被拒绝")
	}
	if _, err := svc.SearchImagePage(ImagePageRequest{Filter: ImageFilter{SortMode: ImageSortTaken}, Cursor: &ImageCursor{SortMode: ImageSortTaken, TakenAt: "not-a-time", ID: 1}}); err == nil {
		t.Fatal("非法拍摄时间游标应被拒绝")
	}
	if _, err := svc.SearchImagePage(ImagePageRequest{Filter: ImageFilter{SortMode: ImageSortTaken}, Cursor: &ImageCursor{SortMode: ImageSortTaken, TakenAt: "2026-08-01T12:00:00Z", RatingIsNull: true, ID: 1}}); err == nil {
		t.Fatal("taken 游标不应包含评分字段")
	}
}

// TestImageExifDefaultSortStaysRecent 钉住 D-013 的既有默认：不传排序模式仍是最近添加。
func TestImageExifDefaultSortStaysRecent(t *testing.T) {
	setupVideoServiceTestDB(t)
	normalized, err := normalizeImageFilter(ImageFilter{})
	if err != nil {
		t.Fatalf("归一化失败: %v", err)
	}
	if normalized.SortMode != ImageSortRecent {
		t.Fatalf("默认排序必须保持 recent, got %q", normalized.SortMode)
	}
}

func TestImageSetFavoriteTogglesAndRejectsMissing(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageLibraryService()
	image := mustCreateTestImage(t, "fav.jpg", 10)

	updated, err := svc.SetImageFavorite(image.ID, true)
	if err != nil || !updated.IsFavorite {
		t.Fatalf("收藏失败: %+v err=%v", updated, err)
	}
	updated, err = svc.SetImageFavorite(image.ID, false)
	if err != nil || updated.IsFavorite {
		t.Fatalf("取消收藏失败: %+v err=%v", updated, err)
	}
	if _, err := svc.SetImageFavorite(image.ID+1000, true); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("不存在的图片应返回未找到: %v", err)
	}
}

func TestImageSetRatingValidatesHalfStepsAndClears(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageLibraryService()
	image := mustCreateTestImage(t, "rate.jpg", 10)

	updated, err := svc.SetImageRating(image.ID, ratingPointer(7.5))
	if err != nil || updated.PersonalRating == nil || *updated.PersonalRating != 7.5 {
		t.Fatalf("0.5 步进评分应成功: %+v err=%v", updated, err)
	}
	for _, invalid := range []float64{7.3, -0.5, 10.5} {
		if _, err := svc.SetImageRating(image.ID, ratingPointer(invalid)); err == nil {
			t.Fatalf("评分 %v 应被拒绝", invalid)
		}
	}
	updated, err = svc.SetImageRating(image.ID, nil)
	if err != nil || updated.PersonalRating != nil {
		t.Fatalf("nil 应清空评分: %+v err=%v", updated, err)
	}
	if _, err := svc.SetImageRating(image.ID+1000, ratingPointer(5)); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("不存在的图片应返回未找到: %v", err)
	}
}

func TestImageAddTagIsIdempotentAndRejectsDeletedOrAutomaticTags(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageLibraryService()
	image := mustCreateTestImage(t, "tagged.jpg", 10)
	tag := mustCreateImageTag(t, "风景")

	if err := svc.AddTagToImage(image.ID, tag.ID); err != nil {
		t.Fatalf("首次打标失败: %v", err)
	}
	if err := svc.AddTagToImage(image.ID, tag.ID); err != nil {
		t.Fatalf("重复打标应幂等: %v", err)
	}
	if count := imageTagLinkCount(t, image.ID, tag.ID); count != 1 {
		t.Fatalf("重复打标不应产生重复关联: count=%d", count)
	}

	deleted := mustCreateImageTag(t, "已删除")
	if err := database.DB.Delete(&models.Tag{}, deleted.ID).Error; err != nil {
		t.Fatalf("软删除标签失败: %v", err)
	}
	if err := svc.AddTagToImage(image.ID, deleted.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("软删除标签打标应被拒绝: %v", err)
	}

	automatic := &models.Tag{Name: "自动短视频", Color: "#000000", IsActive: true, AutomaticKind: "short_video"}
	if err := database.DB.Create(automatic).Error; err != nil {
		t.Fatalf("创建自动标签失败: %v", err)
	}
	if err := svc.AddTagToImage(image.ID, automatic.ID); err == nil {
		t.Fatal("自动标签不应允许手动打标")
	}
	if err := svc.RemoveTagFromImage(image.ID, automatic.ID); err == nil {
		t.Fatal("自动标签不应允许手动去标")
	}

	if err := svc.RemoveTagFromImage(image.ID, tag.ID); err != nil {
		t.Fatalf("去标失败: %v", err)
	}
	if count := imageTagLinkCount(t, image.ID, tag.ID); count != 0 {
		t.Fatalf("去标后关联应清空: count=%d", count)
	}
}

func TestImageBatchTagOperationsRecordPerItemFailures(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageLibraryService()
	first := mustCreateTestImage(t, "batch-a.jpg", 10)
	second := mustCreateTestImage(t, "batch-b.jpg", 10)
	tag := mustCreateImageTag(t, "批量")
	missingID := second.ID + 1000

	result := svc.BatchAddTagToImages([]uint{first.ID, missingID, second.ID}, tag.ID)
	if result.Requested != 3 || result.Succeeded != 2 || result.Failed != 1 {
		t.Fatalf("批量打标计数错误: %+v", result)
	}
	if len(result.Errors) != 1 || result.Errors[0].ImageID != missingID {
		t.Fatalf("批量打标应逐项记录失败: %+v", result.Errors)
	}
	if imageTagLinkCount(t, first.ID, tag.ID) != 1 || imageTagLinkCount(t, second.ID, tag.ID) != 1 {
		t.Fatal("批量打标成功项应写入关联")
	}

	result = svc.BatchRemoveTagFromImages([]uint{first.ID, missingID}, tag.ID)
	if result.Requested != 2 || result.Succeeded != 1 || result.Failed != 1 {
		t.Fatalf("批量去标计数错误: %+v", result)
	}
	if imageTagLinkCount(t, first.ID, tag.ID) != 0 {
		t.Fatal("批量去标成功项应删除关联")
	}

	empty := svc.BatchAddTagToImages(nil, tag.ID)
	if empty.Requested != 0 || empty.Succeeded != 0 || empty.Failed != 0 || len(empty.Errors) != 0 {
		t.Fatalf("空批量应返回零结果: %+v", empty)
	}
}

func TestImageGetDetailIncludesTagsAndExistingAIDescription(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageLibraryService()
	described := mustCreateTestImage(t, "described.jpg", 10)
	plain := mustCreateTestImage(t, "plain.jpg", 10)
	tag := mustCreateImageTag(t, "详情")
	if err := svc.AddTagToImage(described.ID, tag.ID); err != nil {
		t.Fatalf("打标失败: %v", err)
	}
	description := models.ImageAIDescription{ImageID: described.ID, Status: "completed", Description: "山间日出，逆光剪影。"}
	if err := database.DB.Create(&description).Error; err != nil {
		t.Fatalf("创建 AI 描述失败: %v", err)
	}

	detail, err := svc.GetImageDetail(described.ID)
	if err != nil {
		t.Fatalf("读取详情失败: %v", err)
	}
	if detail.Image.ID != described.ID || len(detail.Image.Tags) != 1 || detail.Image.Tags[0].ID != tag.ID {
		t.Fatalf("详情应包含标签: %+v", detail.Image.Tags)
	}
	if detail.AIDescription != description.Description {
		t.Fatalf("详情应回读已有 AI 描述: %q", detail.AIDescription)
	}

	detail, err = svc.GetImageDetail(plain.ID)
	if err != nil {
		t.Fatalf("读取无描述详情失败: %v", err)
	}
	if detail.AIDescription != "" {
		t.Fatalf("无描述行时应为空串: %q", detail.AIDescription)
	}
	if _, err := svc.GetImageDetail(plain.ID + 1000); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("不存在的图片应返回未找到: %v", err)
	}
	if _, err := svc.GetImageDetail(0); err == nil {
		t.Fatal("ID 为 0 应被拒绝")
	}
}

// bucketKey 把年月桶压成 "YYYY-MM" 便于断言。
func bucketKey(bucket ImageTimelineBucket) string {
	return time.Date(bucket.Year, time.Month(bucket.Month), 1, 0, 0, 0, 0, time.Local).Format("2006-01")
}

// TestImageTimelineBucketsGroupByTakenKeyDescending 覆盖时间线分组计数（P-013/AC-16）：
// 口径与 taken 排序键一致（有 EXIF 用 taken_at，无 EXIF 回退 created_at），按年月倒序，
// 软删除行不计入。
func TestImageTimelineBucketsGroupByTakenKeyDescending(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageLibraryService()

	local := func(year int, month time.Month, day int) time.Time {
		return time.Date(year, month, day, 10, 30, 0, 0, time.Local)
	}
	takenAt := func(value time.Time) *time.Time { return &value }
	fallbackCreated := local(2025, time.December, 31)

	augA := mustCreateTestImage(t, "aug-a.jpg", 10)
	augB := mustCreateTestImage(t, "aug-b.jpg", 10)
	julA := mustCreateTestImage(t, "jul-a.jpg", 10)
	prevYear := mustCreateTestImage(t, "prev-year.jpg", 10)
	noExif := mustCreateTestImage(t, "no-exif.jpg", 10)
	removed := mustCreateTestImage(t, "removed.jpg", 10)

	mustSetImageTaken(t, augA.ID, takenAt(local(2026, time.August, 3)), fallbackCreated)
	mustSetImageTaken(t, augB.ID, takenAt(local(2026, time.August, 21)), fallbackCreated)
	mustSetImageTaken(t, julA.ID, takenAt(local(2026, time.July, 9)), fallbackCreated)
	mustSetImageTaken(t, prevYear.ID, takenAt(local(2025, time.August, 9)), fallbackCreated)
	// 无 EXIF：必须按 created_at 落到 2025-12，而不是被丢掉。
	mustSetImageTaken(t, noExif.ID, nil, fallbackCreated)
	mustSetImageTaken(t, removed.ID, takenAt(local(2026, time.August, 5)), fallbackCreated)
	if err := database.DB.Delete(&models.Image{}, removed.ID).Error; err != nil {
		t.Fatalf("软删除图片失败: %v", err)
	}

	buckets, err := svc.ListImageTimelineBuckets(ImageFilter{})
	if err != nil {
		t.Fatalf("时间线分组计数失败: %v", err)
	}

	gotOrder := make([]string, 0, len(buckets))
	gotCounts := make(map[string]int, len(buckets))
	for _, bucket := range buckets {
		gotOrder = append(gotOrder, bucketKey(bucket))
		gotCounts[bucketKey(bucket)] = bucket.Count
	}
	wantOrder := []string{"2026-08", "2026-07", "2025-12", "2025-08"}
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("分组桶数量不符: got=%v want=%v", gotOrder, wantOrder)
	}
	for index := range wantOrder {
		if gotOrder[index] != wantOrder[index] {
			t.Fatalf("分组桶应按年月倒序: got=%v want=%v", gotOrder, wantOrder)
		}
	}
	wantCounts := map[string]int{"2026-08": 2, "2026-07": 1, "2025-12": 1, "2025-08": 1}
	for key, want := range wantCounts {
		if gotCounts[key] != want {
			t.Fatalf("分组 %s 计数不符: got=%d want=%d (%v)", key, gotCounts[key], want, gotCounts)
		}
	}
}

// TestImageTimelineBucketsMatchTakenSortPaging 钉住「分组头计数」与「时间线排序翻页结果」
// 同源：逐页拉完 taken 排序的全部图片再自行归并，应与分组 API 逐桶相等。
func TestImageTimelineBucketsMatchTakenSortPaging(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageLibraryService()

	base := time.Date(2026, 6, 15, 8, 0, 0, 0, time.Local)
	for index := 0; index < 7; index++ {
		image := mustCreateTestImage(t, fmt.Sprintf("paged-%d.jpg", index), 10)
		taken := base.AddDate(0, -index, 0)
		if index%3 == 0 {
			// 每三张留一张没有 EXIF 时间，靠 created_at 参与分组。
			mustSetImageTaken(t, image.ID, nil, taken)
			continue
		}
		mustSetImageTaken(t, image.ID, &taken, base.AddDate(-5, 0, 0))
	}

	paged := make(map[string]int)
	var cursor *ImageCursor
	for range [10]struct{}{} {
		page, err := svc.SearchImagePage(ImagePageRequest{
			Filter: ImageFilter{SortMode: ImageSortTaken}, Cursor: cursor, Limit: 2,
		})
		if err != nil {
			t.Fatalf("时间线翻页失败: %v", err)
		}
		for _, image := range page.Images {
			key := imageTakenSortKey(image).In(time.Local)
			paged[key.Format("2006-01")]++
		}
		if page.NextCursor == nil {
			break
		}
		cursor = page.NextCursor
	}

	buckets, err := svc.ListImageTimelineBuckets(ImageFilter{SortMode: ImageSortTaken})
	if err != nil {
		t.Fatalf("时间线分组计数失败: %v", err)
	}
	if len(buckets) != len(paged) {
		t.Fatalf("分组桶数应与翻页归并一致: buckets=%v paged=%v", buckets, paged)
	}
	total := 0
	for _, bucket := range buckets {
		total += bucket.Count
		if paged[bucketKey(bucket)] != bucket.Count {
			t.Fatalf("分组 %s 计数与翻页归并不一致: bucket=%d paged=%d", bucketKey(bucket), bucket.Count, paged[bucketKey(bucket)])
		}
	}
	if total != 7 {
		t.Fatalf("分组计数总和应等于图片总数: got=%d", total)
	}
}

// TestImageTimelineBucketsApplyFiltersAndValidate 覆盖筛选复用与非法筛选拒绝：
// 分组计数必须与照片页当前筛选同口径，否则分组头会显示筛不出来的张数。
func TestImageTimelineBucketsApplyFiltersAndValidate(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageLibraryService()

	local := func(month time.Month, day int) time.Time {
		return time.Date(2026, month, day, 9, 0, 0, 0, time.Local)
	}
	takenAt := func(value time.Time) *time.Time { return &value }

	starred := mustCreateTestImage(t, "starred.jpg", 10)
	plain := mustCreateTestImage(t, "plain.jpg", 10)
	tagged := mustCreateTestImage(t, "tagged.jpg", 10)
	travel := mustCreateImageTag(t, "旅行")

	mustSetImageTaken(t, starred.ID, takenAt(local(time.May, 2)), local(time.January, 1))
	mustSetImageTaken(t, plain.ID, takenAt(local(time.May, 3)), local(time.January, 1))
	mustSetImageTaken(t, tagged.ID, takenAt(local(time.April, 4)), local(time.January, 1))
	if _, err := svc.SetImageFavorite(starred.ID, true); err != nil {
		t.Fatalf("收藏失败: %v", err)
	}
	if err := svc.AddTagToImage(tagged.ID, travel.ID); err != nil {
		t.Fatalf("打标失败: %v", err)
	}

	favorites, err := svc.ListImageTimelineBuckets(ImageFilter{FavoriteOnly: true})
	if err != nil {
		t.Fatalf("收藏筛选分组失败: %v", err)
	}
	if len(favorites) != 1 || bucketKey(favorites[0]) != "2026-05" || favorites[0].Count != 1 {
		t.Fatalf("仅收藏筛选下应只剩 2026-05 的 1 张: %+v", favorites)
	}

	byTag, err := svc.ListImageTimelineBuckets(ImageFilter{TagIDs: []uint{travel.ID}})
	if err != nil {
		t.Fatalf("标签筛选分组失败: %v", err)
	}
	if len(byTag) != 1 || bucketKey(byTag[0]) != "2026-04" || byTag[0].Count != 1 {
		t.Fatalf("标签筛选下应只剩 2026-04 的 1 张: %+v", byTag)
	}

	byKeyword, err := svc.ListImageTimelineBuckets(ImageFilter{Keyword: "plain"})
	if err != nil {
		t.Fatalf("关键词筛选分组失败: %v", err)
	}
	if len(byKeyword) != 1 || byKeyword[0].Count != 1 {
		t.Fatalf("关键词筛选下应只剩 1 张: %+v", byKeyword)
	}

	empty, err := svc.ListImageTimelineBuckets(ImageFilter{Keyword: "没有这个文件"})
	if err != nil {
		t.Fatalf("空结果分组失败: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("无命中时应返回空桶列表: %+v", empty)
	}

	if _, err := svc.ListImageTimelineBuckets(ImageFilter{SortMode: "unknown"}); err == nil {
		t.Fatal("未知排序模式应被拒绝")
	}
	if _, err := svc.ListImageTimelineBuckets(ImageFilter{MinRating: ratingPointer(6.3)}); err == nil {
		t.Fatal("非 0.5 步进评分筛选应被拒绝")
	}
}
