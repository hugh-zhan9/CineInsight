package migrator

import (
	"context"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/gorm"
)

// seedSource 造一份覆盖面尽量宽的夹具：主表、关联表、隐式多对多、软删除行。
// 迁移器出问题时，最容易悄悄漏掉的就是后两类。
func seedSource(t *testing.T, db *gorm.DB) (videoIDs []uint, tagID uint) {
	t.Helper()

	tag := models.Tag{Name: "夜景", Color: "#0f8f82"}
	if err := db.Create(&tag).Error; err != nil {
		t.Fatalf("建标签失败: %v", err)
	}

	for index := 0; index < 3; index++ {
		video := models.Video{
			Name:      "v.mp4",
			Path:      "/lib/v" + string(rune('a'+index)) + ".mp4",
			Directory: "/lib",
			Size:      int64(index+1) * 100,
			PlayCount: index,
		}
		if err := db.Create(&video).Error; err != nil {
			t.Fatalf("建视频失败: %v", err)
		}
		videoIDs = append(videoIDs, video.ID)
	}

	// 隐式多对多：漏掉这张表，迁移后标签关联会全丢，而主表行数看起来完全正常。
	if err := db.Model(&models.Video{ID: videoIDs[0]}).Association("Tags").Append(&tag); err != nil {
		t.Fatalf("挂标签失败: %v", err)
	}

	// 软删除行：回收站与"否认后不得重复确认"都依赖它存在。
	if err := db.Delete(&models.Video{}, videoIDs[2]).Error; err != nil {
		t.Fatalf("软删除失败: %v", err)
	}
	if err := db.Create(&models.VideoTrashEntry{
		VideoID: videoIDs[2], VideoName: "v.mp4", OriginalPath: "/lib/vc.mp4",
		State: "deleted", CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("建回收站条目失败: %v", err)
	}

	if err := db.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2.0}).Error; err != nil {
		t.Fatalf("建设置失败: %v", err)
	}
	return videoIDs, tag.ID
}

func countUnscoped(t *testing.T, db *gorm.DB, model any) int64 {
	t.Helper()
	var count int64
	if err := db.Unscoped().Model(model).Count(&count).Error; err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	return count
}

func TestMigrateCopiesEverythingAndLeavesSourceUntouched(t *testing.T) {
	source := dbtest.Open(t)
	target := dbtest.Open(t)
	videoIDs, tagID := seedSource(t, source)

	sourceVideosBefore := countUnscoped(t, source, &models.Video{})
	var progressed []Progress
	result, err := Migrate(context.Background(), Options{
		Source: source, Target: target,
		TargetBackend: backendOfTest(),
		OnProgress:    func(p Progress) { progressed = append(progressed, p) },
	})
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	// 源库全程只读——这是"改回配置重启即可回滚"的前提。
	if got := countUnscoped(t, source, &models.Video{}); got != sourceVideosBefore {
		t.Fatalf("迁移不得改动源库: before=%d after=%d", sourceVideosBefore, got)
	}

	// 软删除行必须一起搬：3 条视频里有 1 条是软删除的。
	if got := countUnscoped(t, target, &models.Video{}); got != 3 {
		t.Fatalf("目标库视频数应为 3（含软删除），实际 %d", got)
	}
	var active int64
	if err := target.Model(&models.Video{}).Count(&active).Error; err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	if active != 2 {
		t.Fatalf("软删除状态应被保留，活跃数应为 2，实际 %d", active)
	}

	// 主键必须保留，否则关联表和回收站恢复路径全部错位。
	var migrated models.Video
	if err := target.Unscoped().First(&migrated, videoIDs[0]).Error; err != nil {
		t.Fatalf("主键未保留，按原 ID 查不到: %v", err)
	}

	// 隐式多对多。
	var linked models.Video
	if err := target.Preload("Tags").First(&linked, videoIDs[0]).Error; err != nil {
		t.Fatalf("读取迁移后的视频失败: %v", err)
	}
	if len(linked.Tags) != 1 || linked.Tags[0].ID != tagID {
		t.Fatalf("标签关联未迁移: %+v", linked.Tags)
	}

	if result.RowCounts["video_tags"] != 1 {
		t.Fatalf("关联表行数应为 1: %+v", result.RowCounts)
	}
	if len(progressed) != result.Tables {
		t.Fatalf("每张表都应上报一次进度: got=%d tables=%d", len(progressed), result.Tables)
	}
	if last := progressed[len(progressed)-1]; last.TableIndex != last.TableTotal {
		t.Fatalf("最后一次进度应到达总数: %+v", last)
	}
}

func TestMigrateRoundTripPreservesRowCountsAndKeys(t *testing.T) {
	source := dbtest.Open(t)
	middle := dbtest.Open(t)
	back := dbtest.Open(t)
	videoIDs, _ := seedSource(t, source)

	for _, step := range []struct{ from, to *gorm.DB }{{source, middle}, {middle, back}} {
		if _, err := Migrate(context.Background(), Options{
			Source: step.from, Target: step.to, TargetBackend: backendOfTest(),
		}); err != nil {
			t.Fatalf("迁移失败: %v", err)
		}
	}

	// 往返之后逐表比对行数与主键集合。
	for _, model := range models.AllModels() {
		want := countUnscoped(t, source, model)
		got := countUnscoped(t, back, model)
		if want != got {
			t.Fatalf("往返后行数不一致 %T: source=%d back=%d", model, want, got)
		}
	}
	var ids []uint
	if err := back.Unscoped().Model(&models.Video{}).Order("id ASC").Pluck("id", &ids).Error; err != nil {
		t.Fatalf("读取主键失败: %v", err)
	}
	if len(ids) != len(videoIDs) {
		t.Fatalf("往返后主键数量不一致: %v vs %v", ids, videoIDs)
	}
	for index, id := range ids {
		if id != videoIDs[index] {
			t.Fatalf("往返后主键变了: %v vs %v", ids, videoIDs)
		}
	}
}

func TestMigrateInsertsWorkAfterMigrationOnTargetBackend(t *testing.T) {
	source := dbtest.Open(t)
	target := dbtest.Open(t)
	seedSource(t, source)

	if _, err := Migrate(context.Background(), Options{
		Source: source, Target: target, TargetBackend: backendOfTest(),
	}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	// 这条是序列重置的验收：迁移带着显式 ID 插入，不推进 Postgres 的 BIGSERIAL。
	// 不重置的话，迁移之后新建的第一条记录就撞主键——而且报错发生在迁移结束很久
	// 之后，现场早没了。
	fresh := models.Video{Name: "new.mp4", Path: "/lib/new.mp4", Directory: "/lib"}
	if err := target.Create(&fresh).Error; err != nil {
		t.Fatalf("迁移后新建记录失败（Postgres 上多半是序列没重置）: %v", err)
	}
	if fresh.ID == 0 {
		t.Fatalf("新记录未分配主键")
	}
}

func TestPreflightRefusesNonEmptyAndHalfMigratedTargets(t *testing.T) {
	source := dbtest.Open(t)
	seedSource(t, source)

	t.Run("目标非空时拒绝", func(t *testing.T) {
		target := dbtest.Open(t)
		if err := target.Create(&models.Tag{Name: "占位"}).Error; err != nil {
			t.Fatalf("建行失败: %v", err)
		}
		err := Preflight(target)
		if err == nil {
			t.Fatalf("目标非空应被拒绝")
		}
		var notEmpty *ErrTargetNotEmpty
		if !asErrTargetNotEmpty(err, &notEmpty) {
			t.Fatalf("应返回 ErrTargetNotEmpty，实际 %v", err)
		}
		if _, err := Migrate(context.Background(), Options{
			Source: source, Target: target, TargetBackend: backendOfTest(),
		}); err == nil {
			t.Fatalf("Migrate 也应拒绝非空目标")
		}
	})

	t.Run("半迁移的目标必须先清空", func(t *testing.T) {
		target := dbtest.Open(t)
		if err := target.AutoMigrate(&migrationMarker{}); err != nil {
			t.Fatalf("建标记表失败: %v", err)
		}
		if err := target.Create(&migrationMarker{Completed: false}).Error; err != nil {
			t.Fatalf("写标记失败: %v", err)
		}
		if err := Preflight(target); err != ErrTargetHalfMigrated {
			t.Fatalf("半迁移目标应被拒绝，实际 %v", err)
		}
	})
}

func TestMigrateSkipsSemanticIndexStateSoTargetReportsRebuildNeeded(t *testing.T) {
	source := dbtest.Open(t)
	target := dbtest.Open(t)
	seedSource(t, source)

	if _, err := Migrate(context.Background(), Options{
		Source: source, Target: target, TargetBackend: backendOfTest(),
	}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	// 语义索引的元数据表不在 AllModels 里（由 PrepareSemanticVectorStorage 单独建），
	// 所以按 AllModels 驱动的复制天然不会搬它们。向量本身更是 pgvector 专有、
	// 跨不过来。这条测试把"不搬"钉成契约：一旦有人把这些表加进 AllModels，
	// 目标库就会声称索引存在却搜不出东西。
	for _, name := range []string{"video_semantic_indices", "video_semantic_vectors", "image_semantic_vectors"} {
		if target.Migrator().HasTable(name) {
			var count int64
			if err := target.Table(name).Count(&count).Error; err == nil && count > 0 {
				t.Fatalf("语义索引状态不应跨后端携带，%s 有 %d 行", name, count)
			}
		}
	}
}

func backendOfTest() database.Backend {
	if dbtest.IsPostgres() {
		return database.BackendPostgres
	}
	return database.BackendSQLite
}

func asErrTargetNotEmpty(err error, target **ErrTargetNotEmpty) bool {
	if casted, ok := err.(*ErrTargetNotEmpty); ok {
		*target = casted
		return true
	}
	return false
}
