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

// 迁移必须把「哪些视频/图片被点过喜欢」这个信息保住。
//
// 老库里这个信息只存在标签关联里（短视频互动表里的 liked 是手机端状态，主片库
// 这一侧当时只有标签），直接删标签会丢掉它。
func TestMigrateShortFeedLikedTagPreservesLikesAndRemovesTheTag(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败: %v", err)
	}

	// 造一个老库形态：自动标签 + 关联 + 一条偏好分。
	likedTag := models.Tag{Name: "短视频喜欢", AutomaticKind: "short_feed_liked", Namespace: "自动", IsActive: true}
	userTag := models.Tag{Name: "夜景", IsActive: true}
	for _, tag := range []*models.Tag{&likedTag, &userTag} {
		if err := db.Create(tag).Error; err != nil {
			t.Fatalf("建标签失败: %v", err)
		}
	}
	liked := models.Video{Name: "liked.mp4", Path: "/lib/liked.mp4", Directory: "/lib"}
	plain := models.Video{Name: "plain.mp4", Path: "/lib/plain.mp4", Directory: "/lib"}
	for _, video := range []*models.Video{&liked, &plain} {
		if err := db.Create(video).Error; err != nil {
			t.Fatalf("建视频失败: %v", err)
		}
	}
	if err := db.Model(&liked).Association("Tags").Append(&likedTag, &userTag); err != nil {
		t.Fatalf("挂标签失败: %v", err)
	}
	likedImage := models.Image{Name: "a.jpg", Path: "/pic/a.jpg", Directory: "/pic"}
	if err := db.Create(&likedImage).Error; err != nil {
		t.Fatalf("建图片失败: %v", err)
	}
	if err := db.Model(&likedImage).Association("Tags").Append(&likedTag); err != nil {
		t.Fatalf("给图片挂标签失败: %v", err)
	}
	if err := db.Create(&models.ShortFeedTagPreference{TagID: likedTag.ID, Score: 3}).Error; err != nil {
		t.Fatalf("建偏好失败: %v", err)
	}

	// 再跑一次 ApplySchema，模拟带着老数据启动。
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	var migrated models.Video
	if err := db.First(&migrated, liked.ID).Error; err != nil || !migrated.IsLiked {
		t.Fatalf("标签关联应转成 is_liked: liked=%v err=%v", migrated.IsLiked, err)
	}
	var untouched models.Video
	if err := db.First(&untouched, plain.ID).Error; err != nil || untouched.IsLiked {
		t.Fatalf("没被点过喜欢的视频不应被标记: liked=%v", untouched.IsLiked)
	}
	var migratedImage models.Image
	if err := db.First(&migratedImage, likedImage.ID).Error; err != nil || !migratedImage.IsLiked {
		t.Fatalf("图片侧同样要转成 is_liked: liked=%v err=%v", migratedImage.IsLiked, err)
	}

	// 自动标签本身硬删掉，含其全部关联与偏好分。
	var remaining int64
	if err := db.Unscoped().Model(&models.Tag{}).Where("automatic_kind = ?", "short_feed_liked").Count(&remaining).Error; err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("自动标签应被硬删除，实际还剩 %d 条", remaining)
	}
	var preferences int64
	if err := db.Model(&models.ShortFeedTagPreference{}).Where("tag_id = ?", likedTag.ID).Count(&preferences).Error; err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	if preferences != 0 {
		t.Fatalf("投影标签的偏好分应一并清掉，实际 %d 条", preferences)
	}

	// 用户自己的标签与关联绝不能被牵连。
	var keptTag models.Tag
	if err := db.First(&keptTag, userTag.ID).Error; err != nil {
		t.Fatalf("用户标签不应被删除: %v", err)
	}
	var reloaded models.Video
	if err := db.Preload("Tags").First(&reloaded, liked.ID).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	if len(reloaded.Tags) != 1 || reloaded.Tags[0].ID != userTag.ID {
		t.Fatalf("只应剩下用户自己的标签: %+v", reloaded.Tags)
	}

	// 幂等：标签已不存在时第三次跑应空转。
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}
}
