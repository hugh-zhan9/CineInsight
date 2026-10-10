package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/gorm"
)

func TestReviewSearchMatchesAllEvidenceFieldsAndUnicode(t *testing.T) {
	setupVideoServiceTestDB(t)
	existing := models.Tag{Name: "仅有关联标签的线索"}
	matched := models.Tag{Name: "仅有匹配标签的线索"}
	if err := database.DB.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&matched).Error; err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "ΟΣ-İ-电影.mp4", Path: "/独特目录/name.mp4", Tags: []models.Tag{existing}}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	candidate := models.AITagCandidate{VideoID: video.ID, SuggestedName: "建议名称", MatchedTagID: &matched.ID, Confidence: "high", Status: "pending", Reasoning: "原因里有 100%_\\ 字面符号，以及 Straße"}
	if err := database.DB.Create(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	svc := &AITaggingService{}
	for _, word := range []string{"电影", "独特目录", "关联标签", "匹配标签", "建议名称", "100%_\\", "ος", "i\u0307", "\ufeff 电影 \ufeff"} {
		page, err := svc.SearchCandidatePage(context.Background(), ReviewSearchRequest{Keyword: word, Limit: 1})
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != candidate.ID || page.NextID != 0 {
			t.Fatalf("%q: %+v %v", word, page, err)
		}
	}
	for _, word := range []string{"不存在", "STRASSE", "mp4建议", "100XYZ"} {
		page, err := svc.SearchCandidatePage(context.Background(), ReviewSearchRequest{Keyword: word})
		if err != nil || len(page.Items) != 0 || page.NextID != 0 {
			t.Fatalf("%q: %+v %v", word, page, err)
		}
	}
	if err := database.DB.Delete(&existing).Error; err != nil {
		t.Fatal(err)
	}
	page, err := svc.SearchCandidatePage(context.Background(), ReviewSearchRequest{Keyword: "关联标签"})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("deleted tag still matched: %+v %v", page, err)
	}
	if err := database.DB.Delete(&matched).Error; err != nil {
		t.Fatal(err)
	}
	page, err = svc.SearchCandidatePage(context.Background(), ReviewSearchRequest{Keyword: "匹配标签"})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("deleted matched tag still matched: %+v %v", page, err)
	}
}

func TestReviewSearchFindsUnloadedRowsAndDoesNotSkipCursorMatches(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "file", Path: "/cursor-file"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	var expected []uint
	rows := make([]models.AITagCandidate, 610)
	for i := range rows {
		reason := "其他"
		if i == 0 || i == 255 || i == 257 || i == 609 {
			reason = "目标"
		}
		rows[i] = models.AITagCandidate{VideoID: video.ID, SuggestedName: "建议", Confidence: "high", Status: "pending", Reasoning: reason}
	}
	if err := database.DB.CreateInBatches(&rows, 100).Error; err != nil {
		t.Fatal(err)
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Reasoning == "目标" {
			expected = append(expected, rows[i].ID)
		}
	}
	svc := &AITaggingService{}
	first, err := svc.SearchCandidatePage(context.Background(), ReviewSearchRequest{Keyword: "目标", Limit: 2})
	if err != nil || len(first.Items) != 2 || first.Items[0].ID != expected[0] || first.Items[1].ID != expected[1] || first.NextID != expected[1] {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := svc.SearchCandidatePage(context.Background(), ReviewSearchRequest{Keyword: "目标", Limit: 2, CursorID: first.NextID})
	if err != nil || len(second.Items) != 2 || second.Items[0].ID != expected[2] || second.Items[1].ID != expected[3] || second.NextID != 0 {
		t.Fatalf("second: %+v %v", second, err)
	}
	page, err := svc.SearchCandidatePage(context.Background(), ReviewSearchRequest{Keyword: "不命中"})
	if err != nil || len(page.Items) != 0 || page.NextID != 0 {
		t.Fatalf("empty: %+v %v", page, err)
	}
}

func TestReviewSearchCombinesFiltersAndKeepsDeletedMediaHistory(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := models.Tag{Name: "test"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	videos := []models.Video{{Name: "one", Path: "/one"}, {Name: "two", Path: "/two"}}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatal(err)
	}
	rows := []models.AITagCandidate{
		{VideoID: videos[0].ID, MatchedTagID: &tag.ID, SuggestedName: "hit", Confidence: "HIGH", Status: "pending"},
		{VideoID: videos[0].ID, MatchedTagID: &tag.ID, SuggestedName: "hit", Confidence: "medium", Status: "pending"},
		{VideoID: videos[1].ID, MatchedTagID: &tag.ID, SuggestedName: "hit", Confidence: "high", Status: "pending"},
		{VideoID: videos[0].ID, MatchedTagID: &tag.ID, SuggestedName: "hit", Confidence: "high", Status: "rejected"},
	}
	if err := database.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&videos[0]).Error; err != nil {
		t.Fatal(err)
	}
	page, err := (&AITaggingService{}).SearchCandidatePage(context.Background(), ReviewSearchRequest{Keyword: "HIT", MediaID: videos[0].ID, TagID: tag.ID, Confidence: "high"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != rows[0].ID || !page.Items[0].VideoDeleted {
		t.Fatalf("%+v %v", page, err)
	}
	for _, request := range []ReviewSearchRequest{{Status: "anything"}, {Confidence: "anything"}} {
		if _, err := (&AITaggingService{}).SearchCandidatePage(context.Background(), request); err == nil {
			t.Fatal("invalid filter silently broadened the query")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&AITaggingService{}).SearchCandidatePage(ctx, ReviewSearchRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestReviewSearchImageUsesSameFieldsAndTagIdentity(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := models.Tag{Name: "图片已有标签"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	image := models.Image{Name: "portrait.jpg", Path: "/images/portrait.jpg", Tags: []models.Tag{tag}}
	if err := database.DB.Create(&image).Error; err != nil {
		t.Fatal(err)
	}
	row := models.ImageAITagCandidate{ImageID: image.ID, MatchedTagID: &tag.ID, SuggestedName: "建议", Confidence: "high", Status: "pending", Reasoning: "光线"}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	svc := &ImageAITaggingService{db: database.DB}
	for _, keyword := range []string{"PORTRAIT", "/images/", "图片已有标签", "光线"} {
		page, err := svc.SearchCandidatePage(context.Background(), ReviewSearchRequest{Keyword: keyword, MediaID: image.ID, TagID: tag.ID, Confidence: "high"})
		if err != nil || len(page.Items) != 1 || page.Items[0].ImageID != image.ID {
			t.Fatalf("%s: %+v %v", keyword, page, err)
		}
	}
}

func TestReviewSearchPagesLargeMatchingTagVocabulary(t *testing.T) {
	setupVideoServiceTestDB(t)
	tags := make([]models.Tag, 530)
	for i := range tags {
		tags[i] = models.Tag{Name: fmt.Sprintf("词表-%04d", i)}
	}
	if err := database.DB.CreateInBatches(&tags, 100).Error; err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "v", Path: "/large-tag-vocabulary", Tags: []models.Tag{tags[len(tags)-1]}}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	row := models.AITagCandidate{VideoID: video.ID, SuggestedName: "建议", Confidence: "high", Status: "pending"}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	page, err := (&AITaggingService{}).SearchCandidatePage(context.Background(), ReviewSearchRequest{Keyword: "词表"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != row.ID {
		t.Fatalf("%+v %v", page, err)
	}
}

func TestReviewCursorIndexesExistAndAvoidSQLiteTemporarySort(t *testing.T) {
	setupVideoServiceTestDB(t)
	for _, pair := range []struct {
		model        any
		table, index string
	}{{&models.AITagCandidate{}, "ai_tag_candidates", "idx_ai_tag_candidates_status_id"}, {&models.ImageAITagCandidate{}, "image_ai_tag_candidates", "idx_image_ai_tag_candidates_status_id"}} {
		if !database.DB.Migrator().HasIndex(pair.model, pair.index) {
			t.Fatalf("missing %s", pair.index)
		}
		if dbtest.IsPostgres() {
			continue
		}
		var plan []struct{ Detail string }
		if err := database.DB.Raw("EXPLAIN QUERY PLAN SELECT * FROM "+pair.table+" WHERE status=? AND id<? ORDER BY id DESC LIMIT 50", "pending", 100).Scan(&plan).Error; err != nil {
			t.Fatal(err)
		}
		text := fmt.Sprint(plan)
		if !strings.Contains(text, pair.index) || strings.Contains(text, "TEMP B-TREE") {
			t.Fatalf("%s: %s", pair.table, text)
		}
	}
}

func TestReviewSearchNeverReturnsMoreThanRequestedAndHydratesOnePage(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "common", Path: "/common"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	rows := make([]models.AITagCandidate, 400)
	for i := range rows {
		rows[i] = models.AITagCandidate{VideoID: video.ID, SuggestedName: "common", Confidence: "high", Status: "pending", SourceSummary: strings.Repeat("evidence", 100)}
	}
	if err := database.DB.CreateInBatches(&rows, 100).Error; err != nil {
		t.Fatal(err)
	}
	fullRows := int64(0)
	if err := database.DB.Callback().Query().After("gorm:query").Register("review-hydration-count", func(tx *gorm.DB) {
		if tx.Statement.Table == "ai_tag_candidates" {
			fullRows += tx.RowsAffected
		}
	}); err != nil {
		t.Fatal(err)
	}
	page, err := (&AITaggingService{}).SearchCandidatePage(context.Background(), ReviewSearchRequest{Keyword: "common", Limit: 7})
	if err != nil || len(page.Items) != 7 || page.NextID == 0 || fullRows != 7 {
		t.Fatalf("page=%+v rows=%d err=%v", page, fullRows, err)
	}
}
