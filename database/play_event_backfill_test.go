// 外部测试包：与 schema_ext_test.go 同理，dbtest 依赖 database，
// database 的内部测试再依赖 dbtest 会形成导入环。
package database_test

import (
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// 老库升级时把 videos.last_played_at 补成一条 legacy 事件，重复启动不累积，
// 从没播过的视频不回填。两个后端都要跑：回填 SQL 是两端共用的一条语句。
func TestBackfillLegacyPlayEventsIsIdempotentAndSkipsNeverPlayedVideos(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}

	playedAt := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	played := models.Video{
		Name: "played.mp4", Path: "/library/played.mp4", Directory: "/library",
		PlayCount: 3, LastPlayedAt: &playedAt,
	}
	neverPlayed := models.Video{
		Name: "never.mp4", Path: "/library/never.mp4", Directory: "/library",
	}
	if err := db.Create(&played).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := db.Create(&neverPlayed).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	// 升级路径：老库启动时再跑一次 ApplySchema。
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重跑 schema 失败: %v", err)
	}

	var events []models.PlayEvent
	if err := db.Order("id ASC").Find(&events).Error; err != nil {
		t.Fatalf("读取播放事件失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("应只回填一条事件，实际 %d 条: %+v", len(events), events)
	}
	if events[0].VideoID != played.ID || events[0].Source != models.PlayEventSourceLegacy {
		t.Fatalf("回填内容错误: %+v", events[0])
	}
	// play_count=3 也只回填一条：库里没有另外两次的时间点，编出来的日期会让热力图说谎。
	if diff := events[0].PlayedAt.Sub(playedAt); diff > time.Second || diff < -time.Second {
		t.Fatalf("回填时间应取 last_played_at，偏差 %v", diff)
	}

	// 幂等：再跑两次都不能新增。
	for round := 0; round < 2; round++ {
		if err := database.ApplySchema(db); err != nil {
			t.Fatalf("第 %d 次重跑 schema 失败: %v", round+2, err)
		}
	}
	var total int64
	if err := db.Model(&models.PlayEvent{}).Count(&total).Error; err != nil {
		t.Fatalf("统计播放事件失败: %v", err)
	}
	if total != 1 {
		t.Fatalf("重跑不应新增回填事件，实际 %d 条", total)
	}

	// 之后产生的真实播放事件不会被回填逻辑当成缺口再补一条 legacy。
	if err := db.Create(&models.PlayEvent{
		VideoID: played.ID, PlayedAt: time.Now(), Source: models.PlayEventSourceDesktopPlay,
	}).Error; err != nil {
		t.Fatalf("写入播放事件失败: %v", err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重跑 schema 失败: %v", err)
	}
	var legacyCount int64
	if err := db.Model(&models.PlayEvent{}).
		Where("source = ?", models.PlayEventSourceLegacy).Count(&legacyCount).Error; err != nil {
		t.Fatalf("统计 legacy 事件失败: %v", err)
	}
	if legacyCount != 1 {
		t.Fatalf("legacy 事件应保持 1 条，实际 %d 条", legacyCount)
	}
}

// 永久删除视频时账本行随之消失（D-008 的 ON DELETE CASCADE）；
// 软删除只是标记，事件必须留着。
func TestPlayEventsCascadeOnPermanentVideoDeleteAndSurviveSoftDelete(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	video := models.Video{Name: "cascade.mp4", Path: "/library/cascade.mp4", Directory: "/library"}
	if err := db.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := db.Create(&models.PlayEvent{
		VideoID: video.ID, PlayedAt: time.Now(), Source: models.PlayEventSourceDesktopPlay,
	}).Error; err != nil {
		t.Fatalf("写入播放事件失败: %v", err)
	}

	if err := db.Delete(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("软删除视频失败: %v", err)
	}
	var afterSoftDelete int64
	if err := db.Model(&models.PlayEvent{}).Count(&afterSoftDelete).Error; err != nil {
		t.Fatalf("统计播放事件失败: %v", err)
	}
	if afterSoftDelete != 1 {
		t.Fatalf("软删除不应带走事件，实际 %d 条", afterSoftDelete)
	}

	if err := db.Unscoped().Delete(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("永久删除视频失败: %v", err)
	}
	var afterHardDelete int64
	if err := db.Model(&models.PlayEvent{}).Count(&afterHardDelete).Error; err != nil {
		t.Fatalf("统计播放事件失败: %v", err)
	}
	if afterHardDelete != 0 {
		t.Fatalf("永久删除应级联删事件，实际 %d 条", afterHardDelete)
	}
}
