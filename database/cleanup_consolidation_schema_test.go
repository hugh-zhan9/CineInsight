package database_test

import (
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestCleanupConsolidationSchema(t *testing.T) {
	db := dbtest.OpenRaw(t)
	for i := 0; i < 2; i++ {
		if err := database.ApplySchema(db); err != nil {
			t.Fatal(err)
		}
	}
	assertMovieChartIndex(t, db, "idx_cleanup_consolidation_preview", true, "preview_id")
	assertMovieChartIndex(t, db, "idx_cleanup_consolidation_active", true, "active_slot")
	slot := "video_cleanup"
	makeTask := func(preview string, active *string) models.CleanupConsolidationTask {
		return models.CleanupConsolidationTask{PreviewID: preview, ActiveSlot: active, OwnerScope: "test-owner", Status: "running"}
	}
	first := makeTask("first", &slot)
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := makeTask("first", nil)
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("相同确认令牌应只创建一个任务")
	}
	concurrent := makeTask("second", &slot)
	if err := db.Create(&concurrent).Error; err == nil {
		t.Fatal("不能同时创建两个活动整理任务")
	}
	if err := db.Model(&first).Update("active_slot", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&concurrent).Error; err != nil {
		t.Fatal(err)
	}
	terminal := makeTask("third", nil)
	terminal.Status = "completed"
	if err := db.Create(&terminal).Error; err != nil {
		t.Fatal("多个终态 NULL 活动槽应共存:", err)
	}
	var restored models.CleanupConsolidationTask
	if err := db.First(&restored, first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if restored.Version != 1 || restored.ActiveSlot != nil {
		t.Fatalf("恢复证据未完整保存: %+v", restored)
	}
}

func TestCleanupConsolidationPlanAndItemSchema(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	assertMovieChartIndex(t, db, "idx_cleanup_consolidation_plan_task", true, "task_id")
	assertMovieChartIndex(t, db, "idx_cleanup_consolidation_item_position", true, "task_id", "position")
	task := models.CleanupConsolidationTask{PreviewID: "children", OwnerScope: "owner", Status: "running"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	plan := models.CleanupConsolidationTaskPlan{TaskID: task.ID, PlanJSON: `{"schema_version":1,"proof":"plan"}`}
	item := models.CleanupConsolidationTaskItem{TaskID: task.ID, Position: 0, JournalJSON: `{"schema_version":1,"proof":"item"}`}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	duplicatePlan := plan
	duplicatePlan.ID = 0
	if err := db.Create(&duplicatePlan).Error; err == nil {
		t.Fatal("duplicate task plan accepted")
	}
	duplicateItem := item
	duplicateItem.ID = 0
	if err := db.Create(&duplicateItem).Error; err == nil {
		t.Fatal("duplicate position accepted")
	}
	next := item
	next.ID = 0
	next.Position = 1
	if err := db.Create(&next).Error; err != nil {
		t.Fatal(err)
	}
	var restoredPlan models.CleanupConsolidationTaskPlan
	var restoredItem models.CleanupConsolidationTaskItem
	if err := db.First(&restoredPlan, plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&restoredItem, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if restoredPlan.PlanJSON != plan.PlanJSON || restoredItem.JournalJSON != item.JournalJSON || restoredItem.Position != 0 {
		t.Fatal("snapshot round trip changed")
	}
	if err := db.Delete(&task).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&restoredPlan, plan.ID).Error; err != nil {
		t.Fatal("plan evidence cascaded", err)
	}
	if err := db.First(&restoredItem, item.ID).Error; err != nil {
		t.Fatal("item evidence cascaded", err)
	}
}
