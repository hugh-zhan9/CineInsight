package services

import (
	"context"
	"testing"

	"video-master/database"
	"video-master/models"
)

func TestVideoEditDuplicateCopiesRecipeIntoFreshDraft(t *testing.T) {
	service, media := newFakeEditService(t)
	dir := t.TempDir()
	first := media.addFakeSource(t, dir, "a.mkv", 60000)
	second := media.addFakeSource(t, dir, "b.mkv", 60000)
	view := mustEditProject(t, service, models.VideoEditKindTrimIntro, first.ID, second.ID)
	view = mustUpdateRecipe(t, service, view, func(recipe *EditRecipe) {
		recipe.TrimIntro.Items[0].RemoveStartMS = 1000
		recipe.TrimIntro.Items[0].RemoveEndMS = 9000
		recipe.TrimIntro.Items[0].Confirmed = true
	})
	// 原项目置为失败、带分析与已确认警告：副本只继承配方，不继承这些执行态。
	if err := database.DB.Model(&models.VideoEditProject{}).Where("id = ?", view.ID).
		Updates(map[string]any{"status": models.VideoEditStatusFailed, "analysis_json": `{"kind":"trim_intro"}`, "acknowledged_json": `["upscale"]`}).Error; err != nil {
		t.Fatal(err)
	}

	copied, err := service.DuplicateProject(context.Background(), view.ID)
	if err != nil {
		t.Fatalf("复制失败: %v", err)
	}
	if copied.ID == view.ID || copied.Status != models.VideoEditStatusDraft || copied.Revision != 1 || copied.Kind != view.Kind {
		t.Fatalf("副本应是新的草稿: %+v", copied)
	}
	if copied.Analysis != nil || len(copied.AcknowledgedWarnings) != 0 || len(copied.Items) != 0 {
		t.Fatalf("副本不应带分析、已确认警告或执行项: %+v", copied)
	}
	item := copied.Recipe.TrimIntro.Items[0]
	if item.RemoveStartMS != 1000 || item.RemoveEndMS != 9000 || !item.Confirmed || len(copied.Recipe.TrimIntro.Items) != 2 {
		t.Fatalf("副本配方应与原项目一致: %+v", copied.Recipe.TrimIntro)
	}
	original, err := service.GetProject(context.Background(), view.ID)
	if err != nil || original.Status != models.VideoEditStatusFailed {
		t.Fatalf("原项目不应被改动: %+v err=%v", original, err)
	}
	if _, err := service.UpdateRecipe(context.Background(), EditRecipeUpdateRequest{ProjectID: copied.ID, ExpectedRevision: copied.Revision, Recipe: copied.Recipe}); err != nil {
		t.Fatalf("副本应可编辑: %v", err)
	}
	if _, err := service.DuplicateProject(context.Background(), 999999); editErrorCodeOf(err) != "edit_project_not_found" {
		t.Fatalf("不存在的项目应报 edit_project_not_found: %v", err)
	}
}
