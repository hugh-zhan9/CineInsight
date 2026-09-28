package services

import (
	"errors"
	"testing"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

func conversionFixture(t *testing.T) (models.Tag, []models.Video, []models.Image) {
	t.Helper()
	setupVideoServiceTestDB(t)
	tag := models.Tag{Name: "张三", Namespace: "人物"}
	videos := []models.Video{{Name: "a.mp4", Path: "/conversion/a.mp4"}, {Name: "b.mp4", Path: "/conversion/b.mp4"}}
	images := []models.Image{{Name: "a.jpg", Path: "/conversion/a.jpg"}, {Name: "b.jpg", Path: "/conversion/b.jpg"}}
	for _, value := range []any{&tag, &videos, &images} {
		if err := database.DB.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, video := range videos {
		if err := database.DB.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?)", video.ID, tag.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, image := range images {
		if err := database.DB.Exec("INSERT INTO image_tags(image_id, tag_id) VALUES (?, ?)", image.ID, tag.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Restorable media must not lose their person relationship in a conversion.
	if err := database.DB.Delete(&videos[1]).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&images[1]).Error; err != nil {
		t.Fatal(err)
	}
	return tag, videos, images
}

func assertConversionCount(t *testing.T, table, where string, args []any, want int64) {
	t.Helper()
	var count int64
	if err := database.DB.Table(table).Where(where, args...).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("%s count=%d, want %d", table, count, want)
	}
}

func TestConvertTagToPersonTransfersBothMediaAndInvalidatesPendingCandidates(t *testing.T) {
	tag, videos, images := conversionFixture(t)
	candidate := models.AITagCandidate{VideoID: videos[0].ID, MatchedTagID: &tag.ID, SuggestedName: tag.Name, Confidence: "high", Status: models.AITagCandidateStatusPending}
	imageCandidate := models.ImageAITagCandidate{ImageID: images[0].ID, MatchedTagID: &tag.ID, SuggestedName: tag.Name, Confidence: "high", Status: models.AITagCandidateStatusPending}
	for _, row := range []any{&candidate, &imageCandidate} {
		if err := database.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := &TagService{}
	preview, err := svc.PreviewTagPersonConversion(tag.ID)
	if err != nil || preview.VideoCount != 2 || preview.ImageCount != 2 || len(preview.People) != 0 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	assertConversionCount(t, "people", "1=1", nil, 0)
	request := TagPersonConversionRequest{TagID: tag.ID, TagName: tag.Name, CreateNew: true}
	result, err := svc.ConvertTagToPerson(request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Person.DisplayName != tag.Name || result.VideoCount != 2 || result.ImageCount != 2 {
		t.Fatalf("result=%+v", result)
	}
	for _, table := range []string{"video_people", "image_people"} {
		assertConversionCount(t, table, "person_id = ?", []any{result.Person.ID}, 2)
	}
	for _, table := range []string{"video_tags", "image_tags"} {
		assertConversionCount(t, table, "tag_id = ?", []any{tag.ID}, 0)
	}
	for _, table := range []string{"ai_tag_candidates", "image_ai_tag_candidates"} {
		assertConversionCount(t, table, "matched_tag_id = ? AND status = ?", []any{tag.ID, models.AITagCandidateStatusSuperseded}, 1)
	}
	assertConversionCount(t, "tags", "id = ? AND deleted_at IS NOT NULL", []any{tag.ID}, 1)
	assertConversionCount(t, "videos", "1=1", nil, 2)
	assertConversionCount(t, "images", "1=1", nil, 2)
	if _, err := svc.ConvertTagToPerson(request); err == nil {
		t.Fatal("repeated conversion must reject a removed tag")
	}
	assertConversionCount(t, "people", "1=1", nil, 1)
}

func TestConvertTagToPersonRequiresExplicitIdentityAndDeduplicates(t *testing.T) {
	tag, videos, images := conversionFixture(t)
	people := []models.Person{{DisplayName: tag.Name}, {DisplayName: tag.Name, OriginalName: "另一位"}}
	if err := database.DB.Create(&people).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{&models.VideoPerson{VideoID: videos[0].ID, PersonID: people[1].ID}, &models.ImagePerson{ImageID: images[0].ID, PersonID: people[1].ID}} {
		if err := database.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := &TagService{}
	preview, err := svc.PreviewTagPersonConversion(tag.ID)
	if err != nil || len(preview.People) != 2 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if _, err := svc.ConvertTagToPerson(TagPersonConversionRequest{TagID: tag.ID, TagName: tag.Name}); err == nil {
		t.Fatal("must not guess the target person")
	}
	result, err := svc.ConvertTagToPerson(TagPersonConversionRequest{TagID: tag.ID, TagName: tag.Name, TargetPersonID: people[1].ID})
	if err != nil || result.Person.ID != people[1].ID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertConversionCount(t, "people", "1=1", nil, 2)
	for _, table := range []string{"video_people", "image_people"} {
		assertConversionCount(t, table, "person_id = ?", []any{people[1].ID}, 2)
		assertConversionCount(t, table, "person_id = ?", []any{people[0].ID}, 0)
	}
}

func TestConvertTagToPersonRollsBackWhenRemovingTagFails(t *testing.T) {
	tag, _, _ := conversionFixture(t)
	const callback = "test:reject_conversion_delete"
	if err := database.DB.Callback().Delete().Before("gorm:delete").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "tags" {
			tx.AddError(errors.New("injected tag delete failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Callback().Delete().Remove(callback) })
	_, err := (&TagService{}).ConvertTagToPerson(TagPersonConversionRequest{TagID: tag.ID, TagName: tag.Name, CreateNew: true})
	if err == nil {
		t.Fatal("expected rollback")
	}
	assertConversionCount(t, "tags", "id = ? AND deleted_at IS NULL", []any{tag.ID}, 1)
	for _, table := range []string{"video_tags", "image_tags"} {
		assertConversionCount(t, table, "tag_id = ?", []any{tag.ID}, 2)
	}
	for _, table := range []string{"people", "video_people", "image_people"} {
		assertConversionCount(t, table, "1=1", nil, 0)
	}
}

func TestConvertTagToPersonRejectsStaleOrInvalidInput(t *testing.T) {
	tag, _, _ := conversionFixture(t)
	svc := &TagService{}
	for _, request := range []TagPersonConversionRequest{
		{TagID: 0, TagName: tag.Name, CreateNew: true},
		{TagID: tag.ID, TagName: "旧名称", CreateNew: true},
		{TagID: tag.ID, TagName: tag.Name, TargetPersonID: 99999},
		{TagID: tag.ID, TagName: tag.Name, TargetPersonID: 99999, CreateNew: true},
	} {
		if _, err := svc.ConvertTagToPerson(request); err == nil {
			t.Fatalf("accepted invalid request %+v", request)
		}
	}
	if err := database.DB.Model(&tag).Update("automatic_kind", "short_video").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PreviewTagPersonConversion(tag.ID); err == nil {
		t.Fatal("automatic tag must not be convertible")
	}
	if _, err := svc.ConvertTagToPerson(TagPersonConversionRequest{TagID: tag.ID, TagName: tag.Name, CreateNew: true}); err == nil {
		t.Fatal("automatic tag must not be convertible")
	}
	assertConversionCount(t, "people", "1=1", nil, 0)
	assertConversionCount(t, "video_tags", "tag_id = ?", []any{tag.ID}, 2)
}

func TestConvertTagToPersonRejectsNonPersonCategoryAndChangedCategory(t *testing.T) {
	tag, _, _ := conversionFixture(t)
	svc := &TagService{}
	preview, err := svc.PreviewTagPersonConversion(tag.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Even a previously valid preview cannot authorize conversion after the
	// saved tag moves out of the person category.
	request := TagPersonConversionRequest{TagID: tag.ID, TagName: preview.TagName, CreateNew: true}
	for _, category := range []string{"", "场景", "人物关系"} {
		if err := database.DB.Model(&tag).Update("namespace", category).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := svc.PreviewTagPersonConversion(tag.ID); err == nil {
			t.Fatalf("preview accepted category %q", category)
		}
		if _, err := svc.ConvertTagToPerson(request); err == nil {
			t.Fatalf("conversion accepted category %q", category)
		}
	}
	assertConversionCount(t, "tags", "id = ? AND deleted_at IS NULL", []any{tag.ID}, 1)
	assertConversionCount(t, "people", "1=1", nil, 0)
	for _, table := range []string{"video_tags", "image_tags"} {
		assertConversionCount(t, table, "tag_id = ?", []any{tag.ID}, 2)
	}
	for _, table := range []string{"video_people", "image_people"} {
		assertConversionCount(t, table, "1=1", nil, 0)
	}
}
