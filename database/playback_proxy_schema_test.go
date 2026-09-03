// 外部测试包，与 image_people_schema_test.go 同因：dbtest 依赖 database，
// database 的内部测试再依赖 dbtest 会形成导入环。
package database_test

import (
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// video_playback_proxies 的形态（D-001、D-005）：video_id 唯一、外键级联、
// last_used_at 有索引。两个后端都必须建得出来，且重复 ApplySchema 幂等。
func TestPlaybackProxyTableShapeOnBothBackends(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重复建立 schema 应当幂等(%s): %v", dbtest.Backend(), err)
	}
	if !db.Migrator().HasTable(&models.VideoPlaybackProxy{}) {
		t.Fatalf("video_playback_proxies 表缺失(%s)", dbtest.Backend())
	}
	if !db.Migrator().HasIndex(&models.VideoPlaybackProxy{}, "idx_video_playback_proxies_video") {
		t.Fatalf("video_id 唯一索引缺失(%s)", dbtest.Backend())
	}
	if !db.Migrator().HasIndex(&models.VideoPlaybackProxy{}, "idx_video_playback_proxies_last_used") {
		t.Fatalf("last_used_at 索引缺失(%s)", dbtest.Backend())
	}

	video := models.Video{Name: "proxy.mkv", Path: "/tmp/proxy-schema.mkv", Directory: "/tmp", Size: 1, Duration: 1}
	if err := db.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	row := models.VideoPlaybackProxy{
		VideoID: video.ID, SourceSize: 1, SourceModTimeNS: 2,
		Strategy: models.PlaybackProxyStrategyRemux, Status: models.PlaybackProxyStatusReady,
		OutputSize: 100, LastUsedAt: video.CreatedAt,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("写入代理记录失败: %v", err)
	}
	// video_id 唯一：同一个视频只能有一份代理元数据。
	duplicate := models.VideoPlaybackProxy{
		VideoID: video.ID, SourceSize: 3, SourceModTimeNS: 4,
		Strategy: models.PlaybackProxyStrategyTranscode, Status: models.PlaybackProxyStatusReady,
		LastUsedAt: video.CreatedAt,
	}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatalf("同一视频的第二份代理记录应被唯一索引拒绝(%s)", dbtest.Backend())
	}
	// 外键：不存在的视频不得建代理记录。
	orphan := models.VideoPlaybackProxy{
		VideoID: video.ID + 9999, SourceSize: 1, SourceModTimeNS: 1,
		Strategy: models.PlaybackProxyStrategyRemux, Status: models.PlaybackProxyStatusReady,
		LastUsedAt: video.CreatedAt,
	}
	if err := db.Create(&orphan).Error; err == nil {
		t.Fatalf("不存在的视频应被外键拒绝(%s)", dbtest.Backend())
	}

	// 软删除保留行：级联删除由服务层显式做（要连文件一起删），
	// 数据库层的 CASCADE 只在视频被物理删除时兜底。
	if err := db.Delete(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("软删除视频失败: %v", err)
	}
	var count int64
	if err := db.Model(&models.VideoPlaybackProxy{}).Where("video_id = ?", video.ID).Count(&count).Error; err != nil {
		t.Fatalf("统计代理记录失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("软删除视频不该由数据库层删掉代理记录: count=%d", count)
	}
	if err := db.Unscoped().Delete(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("永久删除视频失败: %v", err)
	}
	if err := db.Model(&models.VideoPlaybackProxy{}).Where("video_id = ?", video.ID).Count(&count).Error; err != nil {
		t.Fatalf("统计代理记录失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("永久删除视频后代理记录应被级联删除: count=%d", count)
	}
}

// 新装库：代理上限默认 50 GiB（D-005）。
func TestProxyCacheLimitDefaultForFreshDatabase(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatalf("读取默认设置失败: %v", err)
	}
	if settings.ProxyCacheLimitBytes != database.DefaultProxyCacheLimitBytes {
		t.Fatalf("新库代理上限应为 %d，实际 %d(%s)",
			database.DefaultProxyCacheLimitBytes, settings.ProxyCacheLimitBytes, dbtest.Backend())
	}
	// 自动开关默认关（零值即默认，不需要迁移）。
	if settings.AutoCompatibilityProxy {
		t.Fatalf("新库的自动代理开关应默认关闭(%s)", dbtest.Backend())
	}
}

// 老库升级：表已存在、列是这一轮才建出来的，必须显式刷成 50 GiB。
// 漏掉这一步，老用户升级后上限是 0（不限），代理目录会无声长到撑满磁盘。
func TestProxyCacheLimitMigratedForOldDatabase(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Migrator().DropColumn(&models.Settings{}, "proxy_cache_limit_bytes"); err != nil {
		t.Fatalf("模拟老库删除列失败(%s): %v", dbtest.Backend(), err)
	}
	if db.Migrator().HasColumn(&models.Settings{}, "proxy_cache_limit_bytes") {
		t.Fatalf("模拟老库失败：列仍然存在(%s)", dbtest.Backend())
	}
	recycleConnections(t, db)

	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("老库升级失败(%s): %v", dbtest.Backend(), err)
	}
	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatalf("读取升级后的设置失败: %v", err)
	}
	if settings.ProxyCacheLimitBytes != database.DefaultProxyCacheLimitBytes {
		t.Fatalf("老库升级后代理上限应为 %d，实际 %d(%s)",
			database.DefaultProxyCacheLimitBytes, settings.ProxyCacheLimitBytes, dbtest.Backend())
	}
}

// 用户把上限设成 0（不限）之后重启：列已存在，迁移必须空转。
func TestProxyCacheLimitMigrationDoesNotOverrideUserChoice(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Model(&models.Settings{}).Where("1 = 1").Update("proxy_cache_limit_bytes", 0).Error; err != nil {
		t.Fatalf("把上限设成不限失败: %v", err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重跑 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if settings.ProxyCacheLimitBytes != 0 {
		t.Fatalf("重启不该覆盖用户设的「不限」: %d(%s)", settings.ProxyCacheLimitBytes, dbtest.Backend())
	}
}

// 双向迁移器逐行 Unscoped().Create 复制：默认非零的数值列一旦带上 gorm default
// 标签，GORM 会把 0 当成"零值即未设置"跳过，用户设的「不限」在迁移之后自己翻回
// 50 GiB。这一条把"0 存得下来"钉住（设计 V1.0.11）。
func TestProxyCacheLimitZeroSurvivesRowCopy(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Where("1 = 1").Delete(&models.Settings{}).Error; err != nil {
		t.Fatalf("清空默认设置失败: %v", err)
	}

	source := models.Settings{ID: 1, VideoExtensions: ".mp4", ProxyCacheLimitBytes: 0}
	if err := db.Unscoped().Create(&source).Error; err != nil {
		t.Fatalf("复制设置行失败(%s): %v", dbtest.Backend(), err)
	}

	var copied models.Settings
	if err := db.First(&copied).Error; err != nil {
		t.Fatalf("读取复制结果失败: %v", err)
	}
	if copied.ProxyCacheLimitBytes != 0 {
		t.Fatalf("设成「不限」的上限不该在复制后翻回默认值: %d(%s)", copied.ProxyCacheLimitBytes, dbtest.Backend())
	}
}
