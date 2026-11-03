package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"log"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
)

func resolvedQuery(draft *LibraryQueryDraft) SearchParsedLibraryQueryRequest {
	request := SearchParsedLibraryQueryRequest{DraftID: draft.DraftID, Limit: 50, ResolvedConditions: []LibraryQueryResolution{}, AcknowledgedRemovals: []string{}}
	for _, condition := range draft.Conditions {
		request.ResolvedConditions = append(request.ResolvedConditions, LibraryQueryResolution{ConditionID: condition.ID, EntityID: condition.EntityID})
	}
	return request
}
func TestParsedLibraryQueryCompoundMatchesSharedFiltersAndSavedView(t *testing.T) {
	setupVideoServiceTestDB(t)
	tag := models.Tag{Name: "科幻"}
	person := models.Person{DisplayName: "张三"}
	for _, row := range []any{&tag, &person} {
		if err := database.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	ids := []uint{}
	for i := 0; i < 4; i++ {
		video := models.Video{Name: fmt.Sprintf("%d.mp4", i), Path: fmt.Sprintf("/query/%d.mp4", i), Directory: "/query", Duration: 7200, IsFavorite: true, IsWatched: i == 1}
		if i == 2 {
			video.Duration = 7201
		}
		if i == 3 {
			video.Duration = 0
		}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, video.ID)
		if err := database.DB.Model(&video).Association("Tags").Append(&tag); err != nil {
			t.Fatal(err)
		}
		if err := database.DB.Create(&models.VideoPerson{VideoID: video.ID, PersonID: person.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	videos := &VideoService{}
	service := NewLibraryQueryService(videos, nil)
	draft, err := service.Parse(context.Background(), ParseLibraryQueryRequest{RequestID: uuid.NewString(), Text: "未看 收藏 120分钟以内 人物 张三 标签 科幻", Mode: "local"})
	if err != nil || draft.Status != "ready" {
		t.Fatalf("%+v %v", draft, err)
	}
	result, err := service.Search(context.Background(), resolvedQuery(draft))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Page.Videos) != 1 || result.Page.Videos[0].ID != ids[0] || result.Filter.MaxDurationSeconds != 7200 || result.Filter.WatchState != "unwatched" || !result.Filter.FavoritesOnly {
		t.Fatalf("compound %+v", result)
	}
	view, err := videos.SaveLibraryView(SavedLibraryViewInput{Name: "compound query", LibraryFilter: result.Filter})
	if err != nil {
		t.Fatal(err)
	}
	if view.PersonIDsJSON != fmt.Sprintf("[%d]", person.ID) || view.TagIDsJSON != fmt.Sprintf("[%d]", tag.ID) || view.MaxDurationSeconds != 7200 || !view.FavoritesOnly || view.WatchState != "unwatched" {
		t.Fatal(view)
	}
}
func TestParsedLibraryQueryRequiresAmbiguityRoleAndUnsupportedDecisions(t *testing.T) {
	setupVideoServiceTestDB(t)
	first, second := models.Person{DisplayName: "同名"}, models.Person{DisplayName: "同名"}
	for _, person := range []*models.Person{&first, &second} {
		if err := database.DB.Create(person).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewLibraryQueryService(&VideoService{}, nil)
	draft, err := service.Parse(context.Background(), ParseLibraryQueryRequest{RequestID: uuid.NewString(), Text: "导演 同名 或 收藏", Mode: "local"})
	if err != nil || draft.Status != "needs_input" || len(draft.Unresolved) != 1 || len(draft.Unresolved[0].Candidates) != 2 || len(draft.Unsupported) == 0 {
		t.Fatalf("%+v %v", draft, err)
	}
	request := resolvedQuery(draft)
	if _, err := service.Search(context.Background(), request); err == nil {
		t.Fatal("unresolved executed")
	}
	for _, issue := range draft.Unsupported {
		request.AcknowledgedRemovals = append(request.AcknowledgedRemovals, issue.ID)
	}
	for i, condition := range draft.Conditions {
		if condition.Kind == "person" {
			request.ResolvedConditions[i].EntityID = first.ID
		}
	}
	if _, err := service.Search(context.Background(), request); err == nil {
		t.Fatal("role silently converted")
	}
	for i, condition := range draft.Conditions {
		if condition.Kind == "person" {
			request.ResolvedConditions[i].AcceptPersonRelation = true
		}
	}
	if _, err := service.Search(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&first).Update("display_name", "changed").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Search(context.Background(), request); !errors.Is(err, ErrLibraryQueryExpired) {
		t.Fatalf("stale entity accepted: %v", err)
	}
}
func TestParsedLibraryQueryDoesNotAcceptInjectedConditionsOrOldCursor(t *testing.T) {
	setupVideoServiceTestDB(t)
	for i := 0; i < 3; i++ {
		if err := database.DB.Create(&models.Video{Name: fmt.Sprintf("%d.mp4", i), Path: fmt.Sprintf("/query/%d.mp4", i), Directory: "/query"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewLibraryQueryService(&VideoService{}, nil)
	owner, cancel := context.WithCancel(context.Background())
	defer cancel()
	draft, err := service.Parse(owner, ParseLibraryQueryRequest{RequestID: uuid.NewString(), Text: "未看", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	request := resolvedQuery(draft)
	request.Limit = 1
	page, err := service.Search(owner, request)
	if err != nil || page.NextCursor == nil {
		t.Fatalf("%+v %v", page, err)
	}
	request.Cursor = page.NextCursor
	request.Cursor.Signature = "forged"
	if _, err := service.Search(owner, request); err == nil {
		t.Fatal("accepted forged cursor")
	}
	request.Cursor = nil
	request.ResolvedConditions[0].EntityID = 42
	if _, err := service.Search(owner, request); err == nil {
		t.Fatal("accepted irrelevant ID")
	}
	request.ResolvedConditions[0].EntityID = 0
	request.AcknowledgedRemovals = []string{"unknown"}
	if _, err := service.Search(owner, request); err == nil {
		t.Fatal("accepted unknown removal")
	}
	cancel()
	request = resolvedQuery(draft)
	if _, err := service.Search(context.Background(), request); !errors.Is(err, ErrLibraryQueryExpired) {
		t.Fatal(err)
	}
}

func TestParsedLibraryQueryCannotPickEntityCreatedAfterDraft(t *testing.T) {
	setupVideoServiceTestDB(t)
	first := models.Person{DisplayName: "同名"}
	if err := database.DB.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	service := NewLibraryQueryService(&VideoService{}, nil)
	draft, err := service.Parse(context.Background(), ParseLibraryQueryRequest{RequestID: uuid.NewString(), Text: "人物 同名", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	second := models.Person{DisplayName: "同名"}
	if err := database.DB.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	request := resolvedQuery(draft)
	request.ResolvedConditions[0].EntityID = second.ID
	if _, err := service.Search(context.Background(), request); !errors.Is(err, ErrLibraryQueryInvalid) {
		t.Fatalf("accepted unreviewed entity: %v", err)
	}
}

func TestParsedLibraryQueryDoesNotLogEntityTextEvenWithSQLLogging(t *testing.T) {
	setupVideoServiceTestDB(t)
	person := models.Person{DisplayName: "私密名称"}
	if err := database.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	previous := database.DB
	database.DB = database.DB.Session(&gorm.Session{Logger: logger.New(log.New(&output, "", 0), logger.Config{LogLevel: logger.Info})})
	defer func() { database.DB = previous }()
	service := NewLibraryQueryService(&VideoService{}, nil)
	if _, err := service.Parse(context.Background(), ParseLibraryQueryRequest{RequestID: uuid.NewString(), Text: "人物 私密名称", Mode: "local"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "私密名称") {
		t.Fatal("entity text reached SQL log")
	}
}
