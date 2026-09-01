// 外部测试包：dbtest 依赖 database，database 的内部测试再依赖 dbtest 会形成导入环。
// ApplySchema 是导出的，放在这里刚好够用。
package database_test

import (
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// TestApplySchemaRunsCleanOnBothBackends 补上一层此前完全缺失的覆盖。
//
// 测试建库历来只调 AutoMigrate，绕开了 Init 里那一整串 ensure* 建索引/补约束的
// 语句。SQLite 后端上线时正是在这里炸的——ensureMediaDetailConstraints 里的
// Postgres DO 块在 SQLite 上是 near "DO": syntax error，而两个后端的全套测试都
// 没拦住，因为根本没跑到这条路径。
func TestApplySchemaRunsCleanOnBothBackends(t *testing.T) {
	db := dbtest.OpenRaw(t)

	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}

	// 默认设置必须落库：以前跑到一半失败时表都在、settings 却是空的，
	// 从外面看不出哪里断了。
	var settings int64
	if err := db.Model(&models.Settings{}).Count(&settings).Error; err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if settings != 1 {
		t.Fatalf("应初始化一条默认设置，实际 %d 条", settings)
	}

	// 幂等：老库上会再跑一遍，第二次不能报错、也不能重复插设置。
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重复建立 schema 应当幂等(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Model(&models.Settings{}).Count(&settings).Error; err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if settings != 1 {
		t.Fatalf("重复执行不应重复插入设置，实际 %d 条", settings)
	}
}

// 评分的半分制约束在 Postgres 上靠 ALTER TABLE 补，在 SQLite 上靠模型 tag 建表时
// 带上。两条路径不同，但结果必须一样——否则 SQLite 下这个约束就是形同虚设。
func TestPersonalRatingCheckConstraintHoldsOnBothBackends(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败: %v", err)
	}

	invalid := 8.3
	err := db.Create(&models.Video{
		Name: "bad.mp4", Path: "/tmp/bad-rating.mp4", Directory: "/tmp", PersonalRating: &invalid,
	}).Error
	if err == nil {
		t.Fatalf("非半分制评分应被数据库约束拒绝(%s)", dbtest.Backend())
	}

	valid := 8.5
	if err := db.Create(&models.Video{
		Name: "good.mp4", Path: "/tmp/good-rating.mp4", Directory: "/tmp", PersonalRating: &valid,
	}).Error; err != nil {
		t.Fatalf("半分制评分应被接受(%s): %v", dbtest.Backend(), err)
	}
}
