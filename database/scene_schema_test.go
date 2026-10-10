// 外部测试包（同 frame_hash_schema_test.go 的理由）：验证 ApplySchema 之后场景检索两张表
// 与三项设置的真实形态，两个后端都要建得出来。
package database_test

import (
	"bytes"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func sceneBlobFixture() []byte {
	blob := make([]byte, 516)
	for index := range blob {
		blob[index] = byte(index * 37)
	}
	blob[4], blob[5] = 0x00, 0xff
	return blob
}

func TestSceneIndexTablesOnBothBackends(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重复建立 schema 应当幂等(%s): %v", dbtest.Backend(), err)
	}
	for _, index := range []string{"idx_scene_visual_segments_model_id_id", "idx_scene_visual_segments_video_id"} {
		if !db.Migrator().HasIndex(&models.SceneVisualSegment{}, index) {
			t.Fatalf("%s 缺失(%s)", index, dbtest.Backend())
		}
	}
	video := models.Video{Name: "scene.mp4", Path: "/tmp/scene-schema.mp4", Directory: "/tmp", Size: 10}
	if err := db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	state := models.SceneIndexState{VideoID: video.ID, ModelID: "cn-clip-vit-b16-q8@1", Provider: "local", SourceSize: 10,
		SourceMtimeNS: 1_700_000_000_123_456_789, IntervalMS: 5000, SegmentCount: 2, Status: models.SceneIndexStatusIndexed, IndexedAt: &now}
	if err := db.Create(&state).Error; err != nil {
		t.Fatalf("写状态失败(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Create(&models.SceneIndexState{VideoID: video.ID, ModelID: "x", IntervalMS: 5000}).Error; err == nil {
		t.Fatalf("同一视频第二行状态应被主键拒绝(%s)", dbtest.Backend())
	}
	segments := []models.SceneVisualSegment{
		{VideoID: video.ID, ModelID: state.ModelID, StartMS: 0, EndMS: 5000, Embedding: sceneBlobFixture()},
		{VideoID: video.ID, ModelID: "external-caption:vision@1", StartMS: 5000, EndMS: 9000, Caption: "海边日落"},
	}
	if err := db.Create(&segments).Error; err != nil {
		t.Fatalf("写段失败(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Create(&models.SceneVisualSegment{VideoID: video.ID + 999, ModelID: "x", StartMS: 0, EndMS: 1}).Error; err == nil {
		t.Fatalf("不存在的视频应被外键拒绝(%s)", dbtest.Backend())
	}
	var loaded []models.SceneVisualSegment
	if err := db.Order("start_ms ASC").Find(&loaded).Error; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded[0].Embedding, sceneBlobFixture()) || loaded[1].Embedding != nil || loaded[1].Caption != "海边日落" {
		t.Fatalf("BLOB 应逐字节读回、外部描述段的向量为空(%s): %d bytes", dbtest.Backend(), len(loaded[0].Embedding))
	}
	var readState models.SceneIndexState
	if err := db.First(&readState, "video_id = ?", video.ID).Error; err != nil || readState.SourceMtimeNS != state.SourceMtimeNS {
		t.Fatalf("纳秒 mtime 应原样读回: %+v %v", readState, err)
	}
	// 视频永久删除时两张表的派生行随之级联删除。
	if err := db.Unscoped().Delete(&models.Video{}, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	var remaining int64
	db.Model(&models.SceneVisualSegment{}).Count(&remaining)
	var states int64
	db.Model(&models.SceneIndexState{}).Count(&states)
	if remaining != 0 || states != 0 {
		t.Fatalf("视频删除后应级联删除段与状态(%s): segments=%d states=%d", dbtest.Backend(), remaining, states)
	}
}

func TestSceneSettingsDefaultsAndMigration(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatal(err)
	}
	if settings.SceneVisualIntervalSeconds != 5 || settings.SceneVisualProvider != "local" || settings.SceneModelMirrorURL != "" {
		t.Fatalf("新库默认值不对(%s): interval=%d provider=%q mirror=%q", dbtest.Backend(),
			settings.SceneVisualIntervalSeconds, settings.SceneVisualProvider, settings.SceneModelMirrorURL)
	}
	if err := db.Model(&models.Settings{}).Where("id = ?", settings.ID).Updates(map[string]any{
		"scene_visual_interval_seconds": 12, "scene_visual_provider": "external", "scene_model_mirror_url": "https://hf-mirror.com",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	var after models.Settings
	_ = db.First(&after).Error
	if after.SceneVisualIntervalSeconds != 12 || after.SceneVisualProvider != "external" || after.SceneModelMirrorURL != "https://hf-mirror.com" {
		t.Fatalf("再次建 schema 不该改动用户设的值(%s): %+v", dbtest.Backend(), after)
	}
	// 老库升级：列刚加出来是 0，迁移补成 5。
	if err := db.Model(&models.Settings{}).Where("id = ?", settings.ID).Update("scene_visual_interval_seconds", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	var upgraded models.Settings
	_ = db.First(&upgraded).Error
	if upgraded.SceneVisualIntervalSeconds != database.DefaultSceneVisualIntervalSeconds {
		t.Fatalf("老库的 0 应被补成默认 5(%s): %d", dbtest.Backend(), upgraded.SceneVisualIntervalSeconds)
	}
}
