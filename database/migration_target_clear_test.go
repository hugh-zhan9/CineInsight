package database_test

import (
	"context"
	"errors"
	"testing"
	"video-master/database"
	"video-master/database/migrator"
	"video-master/internal/dbtest"
	"video-master/models"
)

// APP02：清空迁移目标库（D-PC55 的 PostgreSQL 分支，SQLite 上同样可跑）。
// 走真实入口：dbtest 建库、migrator.Migrate 造出半迁移状态、DropApplicationTables 清空，
// 再用 migrator.Preflight / Migrate 证明可以重试；设 CINEINSIGHT_TEST_PG_DSN 即在 PG 上复用。

func migrationTargetBackend() database.Backend {
	if dbtest.IsPostgres() {
		return database.BackendPostgres
	}
	return database.BackendSQLite
}

func TestAPP02DropApplicationTablesClearsHalfMigratedTargetAndAllowsRetry(t *testing.T) {
	source := dbtest.Open(t)
	tag := models.Tag{Name: "清空前", Color: "#0f8f82"}
	if err := source.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	video := models.Video{Name: "clear.mp4", Path: "/lib/clear.mp4", Directory: "/lib"}
	if err := source.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := source.Model(&video).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	backend := migrationTargetBackend()

	target := dbtest.OpenRaw(t)
	if err := target.Exec("CREATE TABLE user_notes (id BIGINT PRIMARY KEY, body TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	if err := target.Exec("INSERT INTO user_notes (id, body) VALUES (1, 'keep me')").Error; err != nil {
		t.Fatal(err)
	}
	// 半迁移：标记（未完成）与全部表已建、数据未搬——迁移中途失败留下的形态。
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := migrator.Migrate(canceled, migrator.Options{Source: source, Target: target, TargetBackend: backend}); !errors.Is(err, context.Canceled) {
		t.Fatalf("构造半迁移目标库失败: %v", err)
	}
	// 中途失败前已搬过去的几行（含隐式关联表）：清空若漏掉关联表，重试时会撞主键。
	partialTag := models.Tag{ID: tag.ID, Name: tag.Name, Color: tag.Color}
	if err := target.Create(&partialTag).Error; err != nil {
		t.Fatal(err)
	}
	partialVideo := models.Video{ID: video.ID, Name: video.Name, Path: video.Path, Directory: video.Directory}
	if err := target.Omit("Tags").Create(&partialVideo).Error; err != nil {
		t.Fatal(err)
	}
	if err := target.Exec("INSERT INTO video_tags (video_id, tag_id) VALUES (?, ?)", video.ID, tag.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrator.Preflight(target); !errors.Is(err, migrator.ErrTargetHalfMigrated) {
		t.Fatalf("前置：应处于半迁移状态: %v", err)
	}

	listed, err := database.ApplicationTables(target)
	if err != nil {
		t.Fatal(err)
	}
	expected := make([]string, 0, len(listed))
	for _, table := range listed {
		if target.Migrator().HasTable(table) {
			expected = append(expected, table)
		}
	}
	dropped, err := database.DropApplicationTables(target)
	if err != nil {
		t.Fatalf("清空目标库失败: %v", err)
	}
	if len(dropped) != len(expected) {
		t.Fatalf("半迁移目标库里的本应用表应全部删除: dropped=%d expected=%d %v", len(dropped), len(expected), dropped)
	}
	for _, model := range models.AllModels() {
		if target.Migrator().HasTable(model) {
			t.Fatalf("AllModels 的表 %T 应已删除", model)
		}
	}
	for _, table := range []string{"video_tags", "image_tags", "cineinsight_migration_marker"} {
		if target.Migrator().HasTable(table) {
			t.Fatalf("%s 应已删除", table)
		}
	}
	var kept int64
	if err := target.Table("user_notes").Count(&kept).Error; err != nil || kept != 1 {
		t.Fatalf("本应用之外的表与数据必须原样保留: count=%d err=%v", kept, err)
	}

	// 可以重试：预检通过、迁移成功，关联行只有源库那一条。
	if err := migrator.Preflight(target); err != nil {
		t.Fatalf("清空后预检应通过: %v", err)
	}
	if _, err := migrator.Migrate(context.Background(), migrator.Options{Source: source, Target: target, TargetBackend: backend}); err != nil {
		t.Fatalf("清空后重试迁移应成功: %v", err)
	}
	var videos, links int64
	if err := target.Model(&models.Video{}).Count(&videos).Error; err != nil || videos != 1 {
		t.Fatalf("重试后视频数不对: %d err=%v", videos, err)
	}
	if err := target.Table("video_tags").Count(&links).Error; err != nil || links != 1 {
		t.Fatalf("重试后标签关联数不对: %d err=%v", links, err)
	}

	// 重复清空：只删存在的表，结果可重复执行。
	if _, err := database.DropApplicationTables(target); err != nil {
		t.Fatalf("清空已迁移完成的目标库失败: %v", err)
	}
	again, err := database.DropApplicationTables(target)
	if err != nil || len(again) != 0 {
		t.Fatalf("再次清空应无事可做: %v err=%v", again, err)
	}
}

// APP02：DropApplicationTables 不带 CASCADE。PG 上若有别的对象依赖本应用的表，整个事务回滚，
// 一张都不删。SQLite 的 DROP TABLE 不检查这类依赖，本用例只在 PG 上有意义。
func TestAPP02DropApplicationTablesRollsBackWhenForeignObjectsDependOnThem(t *testing.T) {
	if !dbtest.IsPostgres() {
		t.Skip("依赖对象阻止删表是 PostgreSQL 的语义")
	}
	target := dbtest.Open(t)
	if err := target.Exec("CREATE TABLE user_refs (video_id BIGINT REFERENCES videos(id))").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := database.DropApplicationTables(target); err == nil {
		t.Fatal("有外部对象依赖时应失败")
	}
	for _, model := range models.AllModels() {
		if !target.Migrator().HasTable(model) {
			t.Fatalf("失败时应整体回滚，%T 不应被删除", model)
		}
	}
	if !target.Migrator().HasTable("video_tags") || !target.Migrator().HasTable("user_refs") {
		t.Fatal("失败时关联表与外部表都应原样保留")
	}
}

// APP02：防呆——活动库句柄本身不能被清空。
func TestAPP02DropApplicationTablesRefusesActiveDatabase(t *testing.T) {
	db := dbtest.Open(t)
	previous := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	if _, err := database.DropApplicationTables(db); err == nil {
		t.Fatal("活动库不应被清空")
	}
	if !db.Migrator().HasTable(&models.Video{}) {
		t.Fatal("活动库的表不应被删除")
	}
}

// APP02：目标库若曾经作为活动库建过语义索引，语义向量表的外键指向主表。它们是本应用
// 自己建的表，必须一并删除，否则主表删不掉、「清空后重试」无法闭环。
func TestAPP02DropApplicationTablesAlsoDropsSemanticVectorTables(t *testing.T) {
	target := dbtest.Open(t)
	video := models.Video{Name: "vector.mp4", Path: "/lib/vector.mp4", Directory: "/lib"}
	if err := target.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		"CREATE TABLE video_semantic_vectors (video_id BIGINT NOT NULL REFERENCES videos(id), model_identifier TEXT NOT NULL)",
		"CREATE TABLE image_semantic_vectors (image_id BIGINT NOT NULL REFERENCES images(id), model_identifier TEXT NOT NULL)",
	} {
		if err := target.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := target.Exec("INSERT INTO video_semantic_vectors (video_id, model_identifier) VALUES (?, 'm')", video.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := database.DropApplicationTables(target); err != nil {
		t.Fatalf("含语义向量表的目标库应能清空: %v", err)
	}
	for _, table := range []string{"video_semantic_vectors", "image_semantic_vectors", "videos", "images"} {
		if target.Migrator().HasTable(table) {
			t.Fatalf("%s 应已删除", table)
		}
	}
}
