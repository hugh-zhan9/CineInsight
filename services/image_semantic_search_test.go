package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

func TestImageSemanticSearchQueryEmbeddingIsStableAcrossPages(t *testing.T) {
	service := NewImageSemanticIndexService(nil, database.SemanticVectorCapability{Available: true, Backend: "test"}, nil)
	calls := 0
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) {
			calls++
			return []float64{0.2, 0.8}, nil
		})
	}
	config := SemanticIndexConfig{Model: "embed-v1"}
	first, err := service.embedSearchQuery(context.Background(), config, "雨后的城市夜景")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.embedSearchQuery(context.Background(), config, "雨后的城市夜景")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(first) != 2 || len(second) != 2 {
		t.Fatalf("embedding calls=%d first=%v second=%v", calls, first, second)
	}
}

func TestImageSemanticSearchPageNormalization(t *testing.T) {
	cases := []struct {
		name                 string
		offset, limit        int
		wantOffset, wantWant int
	}{
		{"defaults", -5, 0, 0, 20},
		{"capped", 10, 500, 10, 100},
		{"passthrough", 3, 50, 3, 50},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			offset, limit := normalizeImageSemanticPage(testCase.offset, testCase.limit)
			if offset != testCase.wantOffset || limit != testCase.wantWant {
				t.Fatalf("normalize(%d, %d) = (%d, %d)", testCase.offset, testCase.limit, offset, limit)
			}
		})
	}
}

func TestImageSemanticSearchTrimProbesHasMore(t *testing.T) {
	rows := []imageSemanticDistanceRow{{ImageID: 1}, {ImageID: 2}, {ImageID: 3}, {ImageID: 4}}
	trimmed, hasMore := trimImageSemanticRows(rows, 3)
	if !hasMore || len(trimmed) != 3 || trimmed[2].ImageID != 3 {
		t.Fatalf("probe page trimmed=%+v hasMore=%t", trimmed, hasMore)
	}
	trimmed, hasMore = trimImageSemanticRows(rows[:3], 3)
	if hasMore || len(trimmed) != 3 {
		t.Fatalf("exact page trimmed=%+v hasMore=%t", trimmed, hasMore)
	}
	trimmed, hasMore = trimImageSemanticRows(nil, 3)
	if hasMore || len(trimmed) != 0 {
		t.Fatalf("empty page trimmed=%+v hasMore=%t", trimmed, hasMore)
	}
}

func TestImageSemanticScoreClampsToRange(t *testing.T) {
	cases := []struct {
		distance float64
		want     float64
	}{
		{0, 1},
		{0.25, 0.75},
		{1, 0},
		{2, -1},
		{3, -1},
		{-0.5, 1},
	}
	for _, testCase := range cases {
		if got := imageSemanticScore(testCase.distance); got != testCase.want {
			t.Errorf("imageSemanticScore(%v) = %v, want %v", testCase.distance, got, testCase.want)
		}
	}
}

func imageSemanticSQLVarsContain(vars []interface{}, want int64) bool {
	for _, value := range vars {
		switch typed := value.(type) {
		case int:
			if int64(typed) == want {
				return true
			}
		case int64:
			if typed == want {
				return true
			}
		case uint:
			if int64(typed) == want {
				return true
			}
		}
	}
	return false
}

func TestImageSemanticSearchDistanceQueryAppliesFiltersAndNeverTouchesVideoTables(t *testing.T) {
	capability := setupImageSemanticIndexTestDB(t)
	service := NewImageSemanticIndexService(database.DB, capability, imageSemanticTestProvider("embed-v1"))
	profile := models.SemanticIndexProfile{ID: 1, ActiveModel: "embed-v1", Dimension: 3, Generation: 2}
	minRating, maxRating := 6.0, 9.5
	filter := ImageSemanticFilter{
		TagIDs:       []uint{2, 1, 2},
		FavoriteOnly: true,
		MinRating:    &minRating,
		MaxRating:    &maxRating,
		MinSize:      100,
		MaxSize:      5000,
	}
	var rows []imageSemanticDistanceRow
	stmt := service.imageSemanticDistanceQuery(context.Background(), filter, profile, "[0.1,0.2,0.3]", 20, 20).
		Session(&gorm.Session{DryRun: true}).Scan(&rows).Statement
	sql := stmt.SQL.String()

	for _, expected := range []string{
		"image_semantic_vectors.embedding <=> CAST(? AS vector)",
		"JOIN image_semantic_vectors ON image_semantic_vectors.image_id = images.id",
		"image_semantic_vectors.model_identifier = ?",
		"image_semantic_vectors.dimension = ?",
		"image_semantic_vectors.generation = ?",
		"images.deleted_at IS NULL",
		"images.is_favorite",
		"images.size >= ?",
		"images.size < ?",
		"images.personal_rating >= ?",
		"images.personal_rating <= ?",
		"image_tags",
		"COUNT(DISTINCT tag_id) = ?",
		"ORDER BY distance ASC, images.id ASC",
	} {
		if !strings.Contains(sql, expected) {
			t.Errorf("distance SQL missing %q: %s", expected, sql)
		}
	}
	// 断言边界（AC-6）：图片语义检索绝不查询 videos 或 video_semantic_vectors。
	if strings.Contains(strings.ToLower(sql), "video") {
		t.Errorf("distance SQL must never reference video tables: %s", sql)
	}
	// gorm 的 sqlite 方言将 LIMIT/OFFSET 内联进 SQL；其他方言可能走占位符。
	if !strings.Contains(sql, "LIMIT 21") && !imageSemanticSQLVarsContain(stmt.Vars, 21) {
		t.Errorf("limit+1 probe missing: sql=%s vars=%v", sql, stmt.Vars)
	}
	if !strings.Contains(sql, "OFFSET 20") && !imageSemanticSQLVarsContain(stmt.Vars, 20) {
		t.Errorf("offset missing: sql=%s vars=%v", sql, stmt.Vars)
	}
}

// TestImageSemanticSearchFilterSelectsExpectedRows 在 sqlite 上实际执行筛选子句，
// 验证 filter 组合的行选择语义（距离表达式是 pgvector 专有，此处单独验证 WHERE 部分）。
func TestImageSemanticSearchFilterSelectsExpectedRows(t *testing.T) {
	capability := setupImageSemanticIndexTestDB(t)
	service := NewImageSemanticIndexService(database.DB, capability, imageSemanticTestProvider("embed-v1"))
	tag := models.Tag{Name: "夜景", IsActive: true}
	other := models.Tag{Name: "人像", IsActive: true}
	for _, item := range []*models.Tag{&tag, &other} {
		if err := database.DB.Create(item).Error; err != nil {
			t.Fatalf("create tag: %v", err)
		}
	}
	rating := func(value float64) *float64 { return &value }
	seed := []models.Image{
		{Name: "match.jpg", Path: "/library/match.jpg", Size: 2000, IsFavorite: true, PersonalRating: rating(8), Tags: []models.Tag{tag}},
		{Name: "not-favorite.jpg", Path: "/library/not-favorite.jpg", Size: 2000, PersonalRating: rating(8), Tags: []models.Tag{tag}},
		{Name: "too-small.jpg", Path: "/library/too-small.jpg", Size: 10, IsFavorite: true, PersonalRating: rating(8), Tags: []models.Tag{tag}},
		{Name: "too-big.jpg", Path: "/library/too-big.jpg", Size: 90000, IsFavorite: true, PersonalRating: rating(8), Tags: []models.Tag{tag}},
		{Name: "low-rating.jpg", Path: "/library/low-rating.jpg", Size: 2000, IsFavorite: true, PersonalRating: rating(3), Tags: []models.Tag{tag}},
		{Name: "high-rating.jpg", Path: "/library/high-rating.jpg", Size: 2000, IsFavorite: true, PersonalRating: rating(10), Tags: []models.Tag{tag}},
		{Name: "wrong-tag.jpg", Path: "/library/wrong-tag.jpg", Size: 2000, IsFavorite: true, PersonalRating: rating(8), Tags: []models.Tag{other}},
		{Name: "soft-deleted.jpg", Path: "/library/soft-deleted.jpg", Size: 2000, IsFavorite: true, PersonalRating: rating(8), Tags: []models.Tag{tag}},
	}
	for index := range seed {
		if err := database.DB.Create(&seed[index]).Error; err != nil {
			t.Fatalf("create image: %v", err)
		}
	}
	if err := database.DB.Delete(&models.Image{}, seed[len(seed)-1].ID).Error; err != nil {
		t.Fatalf("soft delete image: %v", err)
	}

	filter := ImageSemanticFilter{
		TagIDs: []uint{tag.ID}, FavoriteOnly: true,
		MinRating: rating(6), MaxRating: rating(9.5), MinSize: 100, MaxSize: 5000,
	}
	query := service.applyImageSemanticFilter(
		database.DB.Table("images").Select("images.id").Where("images.deleted_at IS NULL"), filter)
	var ids []uint
	if err := query.Order("images.id ASC").Scan(&ids).Error; err != nil {
		t.Fatalf("apply filter: %v", err)
	}
	if len(ids) != 1 || ids[0] != seed[0].ID {
		t.Fatalf("filter selected %v, want only %d", ids, seed[0].ID)
	}

	// 空 filter 只受软删除约束。
	query = service.applyImageSemanticFilter(
		database.DB.Table("images").Select("images.id").Where("images.deleted_at IS NULL"), ImageSemanticFilter{})
	if err := query.Scan(&ids).Error; err != nil {
		t.Fatalf("apply empty filter: %v", err)
	}
	if len(ids) != len(seed)-1 {
		t.Fatalf("empty filter selected %d rows, want %d", len(ids), len(seed)-1)
	}
}

func TestImageSemanticSearchPageExcludesSoftDeletedImagesAndScoresHits(t *testing.T) {
	capability := setupImageSemanticIndexTestDB(t)
	tag := models.Tag{Name: "夜景", IsActive: true}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("create tag: %v", err)
	}
	kept := createImageSemanticTestImage(t, "kept.jpg", "保留的图片", tag)
	removed := createImageSemanticTestImage(t, "removed.jpg", "被软删的图片")
	profile := models.SemanticIndexProfile{ID: 1, ActiveModel: "embed-v1", Dimension: 2, Generation: 1}
	if err := database.DB.Create(&profile).Error; err != nil {
		t.Fatalf("create profile: %v", err)
	}
	for _, imageID := range []uint{kept.ID, removed.ID} {
		if err := database.DB.Create(&models.ImageSemanticIndex{
			ImageID: imageID, ModelIdentifier: "embed-v1", Dimension: 2, Generation: 1,
			ContentFingerprint: "fp", IndexedAt: time.Now(),
		}).Error; err != nil {
			t.Fatalf("create semantic row: %v", err)
		}
	}
	if err := database.DB.Delete(&models.Image{}, removed.ID).Error; err != nil {
		t.Fatalf("soft delete image: %v", err)
	}

	service := NewImageSemanticIndexService(database.DB, capability, imageSemanticTestProvider("embed-v1"))
	rows := []imageSemanticDistanceRow{
		{ImageID: kept.ID, Distance: 0.25},
		{ImageID: removed.ID, Distance: 0.5},
	}
	page, err := service.buildImageSemanticSearchPage(context.Background(), rows, profile, true)
	if err != nil {
		t.Fatalf("build page: %v", err)
	}
	if len(page.Hits) != 1 || page.Hits[0].Image.ID != kept.ID {
		t.Fatalf("soft-deleted image leaked into hits: %+v", page.Hits)
	}
	if page.Hits[0].Score != 0.75 {
		t.Fatalf("score = %v, want 0.75", page.Hits[0].Score)
	}
	if len(page.Hits[0].Image.Tags) != 1 || page.Hits[0].Image.Tags[0].Name != "夜景" {
		t.Fatalf("tags not preloaded: %+v", page.Hits[0].Image.Tags)
	}
	if !page.HasMore {
		t.Fatalf("hasMore must survive short pages: %+v", page)
	}
	if page.Coverage.Total != 1 || page.Coverage.Indexed != 1 {
		t.Fatalf("coverage must exclude soft-deleted images: %+v", page.Coverage)
	}
}

func TestImageSemanticSearchRejectsStaleOrRebuildingProfile(t *testing.T) {
	setupImageSemanticIndexTestDB(t)
	// sqlite 上伪装 postgres 能力，仅验证 activeSearchProfile 的拒绝路径，不触发 SQL。
	capability := database.SemanticVectorCapability{Available: true, Backend: "postgres"}
	service := NewImageSemanticIndexService(database.DB, capability, imageSemanticTestProvider("embed-v1"))
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) {
			t.Error("embedding must not be called when search is rejected")
			return nil, errors.New("unreachable")
		})
	}
	profile := models.SemanticIndexProfile{ID: 1, ActiveModel: "embed-v1", Dimension: 2, Generation: 1, NeedsRebuild: true}
	if err := database.DB.Create(&profile).Error; err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := service.SearchImagesSemantic(context.Background(), ImageSemanticSearchRequest{Query: "夜景"}); !errors.Is(err, ErrImageSemanticIndexRebuildRequired) {
		t.Fatalf("needs_rebuild search error = %v", err)
	}
	if err := database.DB.Model(&models.SemanticIndexProfile{}).Where("id = ?", 1).
		Updates(map[string]any{"needs_rebuild": false, "active_model": "other-model"}).Error; err != nil {
		t.Fatalf("update profile: %v", err)
	}
	if _, err := service.SearchImagesSemantic(context.Background(), ImageSemanticSearchRequest{Query: "夜景"}); !errors.Is(err, ErrImageSemanticIndexRebuildRequired) {
		t.Fatalf("model mismatch search error = %v", err)
	}
	if _, err := service.SearchImagesSemantic(context.Background(), ImageSemanticSearchRequest{Query: "   "}); err == nil {
		t.Fatal("blank query must be rejected")
	}
}
