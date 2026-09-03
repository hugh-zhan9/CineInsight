// 外部测试包，与 schema_ext_test.go 同因：dbtest 依赖 database，内部测试再依赖
// dbtest 会形成导入环。这里验证 ApplySchema 之后帧哈希两张表的真实形态。
package database_test

import (
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// video_frame_hash_sequences：video_id 主键兼外键、computed_at 索引、blob 序列
// 能原样读回。两个后端都必须建得出来（SQLite 是 blob，Postgres 是 bytea）。
func TestFrameHashSequenceTableOnBothBackends(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	// 幂等：老库升级会再跑一遍。
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重复建立 schema 应当幂等(%s): %v", dbtest.Backend(), err)
	}
	if !db.Migrator().HasTable(&models.VideoFrameHashSequence{}) {
		t.Fatalf("video_frame_hash_sequences 表缺失(%s)", dbtest.Backend())
	}
	if !db.Migrator().HasIndex(&models.VideoFrameHashSequence{}, "ComputedAt") {
		t.Fatalf("computed_at 索引缺失(%s)", dbtest.Backend())
	}

	video := models.Video{Name: "seq.mp4", Path: "/tmp/frame-hash-seq.mp4", Directory: "/tmp"}
	if err := db.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	blob := []byte{0, 0, 0, 0, 0, 0, 0, 1, 255, 255, 255, 255, 255, 255, 255, 255}
	row := models.VideoFrameHashSequence{
		VideoID: video.ID, IntervalMS: 2000, Hashes: blob, FrameCount: 2,
		SourceSize: 1234, SourceModTimeNS: 5678, ComputedAt: time.Now(),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("写入序列失败(%s): %v", dbtest.Backend(), err)
	}
	// 主键：一个视频最多一行。
	if err := db.Create(&models.VideoFrameHashSequence{
		VideoID: video.ID, IntervalMS: 2000, Hashes: blob, FrameCount: 2, ComputedAt: time.Now(),
	}).Error; err == nil {
		t.Fatalf("同一视频的第二行应被主键拒绝(%s)", dbtest.Backend())
	}
	// 外键：不存在的视频不得写序列。
	if err := db.Create(&models.VideoFrameHashSequence{
		VideoID: video.ID + 9999, IntervalMS: 2000, Hashes: blob, FrameCount: 2, ComputedAt: time.Now(),
	}).Error; err == nil {
		t.Fatalf("不存在的视频应被外键拒绝(%s)", dbtest.Backend())
	}

	var loaded models.VideoFrameHashSequence
	if err := db.First(&loaded, "video_id = ?", video.ID).Error; err != nil {
		t.Fatalf("读回序列失败: %v", err)
	}
	if loaded.IntervalMS != 2000 || loaded.FrameCount != 2 {
		t.Fatalf("间隔与帧数应原样读回(%s): %+v", dbtest.Backend(), loaded)
	}
	if len(loaded.Hashes) != len(blob) {
		t.Fatalf("blob 长度应原样读回(%s): %d != %d", dbtest.Backend(), len(loaded.Hashes), len(blob))
	}
	for index := range blob {
		if loaded.Hashes[index] != blob[index] {
			t.Fatalf("blob 第 %d 字节被改写(%s): %d != %d", index, dbtest.Backend(), loaded.Hashes[index], blob[index])
		}
	}

	// 失败行只写 last_error、序列为空：NOT NULL 的 blob 列必须容得下零长度，
	// 否则回填一失败就连原因都记不下来。
	failure := models.Video{Name: "fail.mp4", Path: "/tmp/frame-hash-fail.mp4", Directory: "/tmp"}
	if err := db.Create(&failure).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	if err := db.Create(&models.VideoFrameHashSequence{
		VideoID: failure.ID, IntervalMS: 2000, Hashes: []byte{}, FrameCount: 0,
		ComputedAt: time.Now(), LastError: "ffmpeg 抽帧失败",
	}).Error; err != nil {
		t.Fatalf("失败行应能写入(%s): %v", dbtest.Backend(), err)
	}

	// 软删除保留序列（视频进回收站不等于要重算），硬删除才级联清掉。
	if err := db.Delete(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("软删除视频失败: %v", err)
	}
	var count int64
	if err := db.Model(&models.VideoFrameHashSequence{}).Where("video_id = ?", video.ID).Count(&count).Error; err != nil {
		t.Fatalf("统计序列失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("软删除视频应保留序列: count=%d", count)
	}
	if err := db.Unscoped().Delete(&models.Video{}, video.ID).Error; err != nil {
		t.Fatalf("硬删除视频失败: %v", err)
	}
	if err := db.Model(&models.VideoFrameHashSequence{}).Where("video_id = ?", video.ID).Count(&count).Error; err != nil {
		t.Fatalf("统计序列失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("硬删除视频应级联清掉序列: count=%d", count)
	}
}

// clip_dismissals：(video_full_id, video_clip_id) 唯一，且方向有意义——
// 反过来的一对是另一条记录，不能被唯一键当成重复。
func TestClipDismissalPairUniquenessIsDirectional(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	if !db.Migrator().HasTable(&models.ClipDismissal{}) {
		t.Fatalf("clip_dismissals 表缺失(%s)", dbtest.Backend())
	}

	dismissal := models.ClipDismissal{
		VideoFullID: 1, VideoClipID: 2,
		FullSourceSize: 100, FullSourceModTimeNS: 111,
		ClipSourceSize: 20, ClipSourceModTimeNS: 222,
	}
	if err := db.Create(&dismissal).Error; err != nil {
		t.Fatalf("写入忽略记录失败(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Create(&models.ClipDismissal{VideoFullID: 1, VideoClipID: 2}).Error; err == nil {
		t.Fatalf("同一对的第二条应被唯一键拒绝(%s)", dbtest.Backend())
	}
	// 角色互换是另一对（A 是 B 的截取 ≠ B 是 A 的截取），必须能写下来。
	if err := db.Create(&models.ClipDismissal{VideoFullID: 2, VideoClipID: 1}).Error; err != nil {
		t.Fatalf("角色互换的一对应当可以写入(%s): %v", dbtest.Backend(), err)
	}
}

// 帧哈希的自动开关默认关，且不带 gorm default 标签：老库升级后不需要迁移，
// 新库初始化出来就是关的。
func TestAutoFrameHashSequenceDefaultsOff(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatalf("读取默认设置失败: %v", err)
	}
	if settings.AutoFrameHashSequence {
		t.Fatalf("新库的自动帧哈希开关应为关(%s)", dbtest.Backend())
	}
	// 用户打开之后再跑一次 ApplySchema（模拟重启）不许把它翻回去。
	if err := db.Model(&models.Settings{}).Where("1 = 1").Update("auto_frame_hash_sequence", true).Error; err != nil {
		t.Fatalf("打开开关失败: %v", err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重复建立 schema 失败: %v", err)
	}
	if err := db.First(&settings).Error; err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if !settings.AutoFrameHashSequence {
		t.Fatalf("用户打开的开关不该被迁移翻回关(%s)", dbtest.Backend())
	}
}
