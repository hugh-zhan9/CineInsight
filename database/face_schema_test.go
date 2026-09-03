// 外部测试包，与 image_people_schema_test.go 同因：dbtest 依赖 database，
// 内部测试再依赖 dbtest 会形成导入环。这里验证 ApplySchema 之后三张人脸表的真实形态。
package database_test

import (
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// 三张人脸表在两个后端都建得出来，且唯一键与索引与设计 5.1.2 一致。
func TestFaceTablesAndUniqueKeyOnBothBackends(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	// 幂等：老库升级会再跑一遍。
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重复建立 schema 应当幂等(%s): %v", dbtest.Backend(), err)
	}
	for _, model := range []interface{}{&models.FaceObservation{}, &models.FaceCluster{}, &models.FacePersonCandidate{}} {
		if !db.Migrator().HasTable(model) {
			t.Fatalf("人脸表缺失(%s): %T", dbtest.Backend(), model)
		}
	}
	if !db.Migrator().HasIndex(&models.FaceObservation{}, "idx_face_observations_identity") {
		t.Fatalf("观测唯一索引缺失(%s)", dbtest.Backend())
	}
	if !db.Migrator().HasIndex(&models.FaceObservation{}, "idx_face_observations_media") {
		t.Fatalf("观测媒体索引缺失(%s)", dbtest.Backend())
	}
	if !db.Migrator().HasIndex(&models.FaceCluster{}, "idx_face_clusters_status") {
		t.Fatalf("簇状态索引缺失(%s)", dbtest.Backend())
	}
	// 设计口径是 bbox / bbox_hash，不是 GORM 默认会拆出来的 b_box。
	for _, column := range []string{"bbox", "bbox_hash", "append_status", "embedding", "crop_path", "source_fingerprint"} {
		if !db.Migrator().HasColumn(&models.FaceObservation{}, column) {
			t.Fatalf("观测缺列 %s(%s)", column, dbtest.Backend())
		}
	}

	frame := int64(1500)
	observation := models.FaceObservation{
		MediaKind: models.FaceMediaKindVideo, MediaID: 7, SourceFingerprint: "1024-99",
		FrameMS: &frame, BBox: "0.1,0.1,0.2,0.2", BBoxHash: "100100200200", Quality: 0.75,
		Embedding: make([]byte, 2048), AppendStatus: models.FaceAppendStatusNone,
	}
	if err := db.Create(&observation).Error; err != nil {
		t.Fatalf("写观测失败(%s): %v", dbtest.Backend(), err)
	}
	// BLOB 往返：两个后端都要能原样读回 2048 字节。
	var reloaded models.FaceObservation
	if err := db.First(&reloaded, observation.ID).Error; err != nil {
		t.Fatalf("读观测失败: %v", err)
	}
	if len(reloaded.Embedding) != 2048 {
		t.Fatalf("向量往返后长度应为 2048，实际 %d(%s)", len(reloaded.Embedding), dbtest.Backend())
	}
	if reloaded.AppendStatus != models.FaceAppendStatusNone {
		t.Fatalf("append_status 默认应为 none，实际 %q", reloaded.AppendStatus)
	}

	// frame_ms 是 NOT NULL + 哨兵默认值：唯一键里一旦出现 NULL，两个后端都会
	// 把这一行当成"跟谁都不相等"，图片与头像的唯一约束就形同虚设。
	imageRow := models.FaceObservation{
		MediaKind: models.FaceMediaKindImage, MediaID: 9, SourceFingerprint: "2048-77",
		BBox: "0.2,0.2,0.3,0.3", BBoxHash: "200200300300", Quality: 0.5,
		Embedding: make([]byte, 2048), AppendStatus: models.FaceAppendStatusNone,
	}
	if err := db.Create(&imageRow).Error; err != nil {
		t.Fatalf("写图片观测失败(%s): %v", dbtest.Backend(), err)
	}
	var reloadedImage models.FaceObservation
	if err := db.First(&reloadedImage, imageRow.ID).Error; err != nil {
		t.Fatalf("读图片观测失败: %v", err)
	}
	if reloadedImage.FrameMS == nil || *reloadedImage.FrameMS != models.FaceFrameMSNone {
		t.Fatalf("未给帧位置时应落到哨兵默认值 -1(%s): %+v", dbtest.Backend(), reloadedImage.FrameMS)
	}
	imageDuplicate := imageRow
	imageDuplicate.ID = 0
	imageDuplicate.FrameMS = nil
	if err := db.Create(&imageDuplicate).Error; err == nil {
		t.Fatalf("图片观测的重复行应被唯一键拒绝(%s)——哨兵值让约束真正生效", dbtest.Backend())
	}
	var nullRows int64
	if err := db.Model(&models.FaceObservation{}).Where("frame_ms IS NULL").Count(&nullRows).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if nullRows != 0 {
		t.Fatalf("不该存在 frame_ms 为 NULL 的行(%s): %d", dbtest.Backend(), nullRows)
	}

	// 同一件源的同一个位置只容得下一条观测（幂等键，4.4.2）。
	duplicate := observation
	duplicate.ID = 0
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatalf("重复观测应被唯一键拒绝(%s)", dbtest.Backend())
	}
	// 换一个位置就是另一条观测。
	other := observation
	other.ID = 0
	other.BBoxHash = "300300200200"
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("同一帧的另一个位置应可入库(%s): %v", dbtest.Backend(), err)
	}
}

// 人物被删除时簇回到未命名（外键 SET NULL），候选行级联清掉（4.4.4）。
func TestFaceClusterFallsBackToUnnamedWhenPersonDeleted(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	person := models.Person{DisplayName: "人脸人物"}
	if err := db.Create(&person).Error; err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	cluster := models.FaceCluster{
		Status: models.FaceClusterStatusNamed, PersonID: &person.ID,
		Centroid: make([]byte, 2048), ObservationCount: 3,
	}
	if err := db.Create(&cluster).Error; err != nil {
		t.Fatalf("创建簇失败(%s): %v", dbtest.Backend(), err)
	}
	if err := db.Create(&models.FacePersonCandidate{ClusterID: cluster.ID, PersonID: person.ID, Similarity: 0.8}).Error; err != nil {
		t.Fatalf("创建候选失败: %v", err)
	}
	// 唯一键：同一对簇/人物只留一条建议。
	if err := db.Create(&models.FacePersonCandidate{ClusterID: cluster.ID, PersonID: person.ID, Similarity: 0.9}).Error; err == nil {
		t.Fatalf("重复候选应被唯一键拒绝(%s)", dbtest.Backend())
	}

	if err := db.Delete(&models.Person{}, person.ID).Error; err != nil {
		t.Fatalf("删除人物失败: %v", err)
	}
	var reloaded models.FaceCluster
	if err := db.First(&reloaded, cluster.ID).Error; err != nil {
		t.Fatalf("簇应仍然存在: %v", err)
	}
	if reloaded.PersonID != nil {
		t.Fatalf("人物删除后簇的 person_id 应置空(%s): %v", dbtest.Backend(), *reloaded.PersonID)
	}
	var candidates int64
	if err := db.Model(&models.FacePersonCandidate{}).Count(&candidates).Error; err != nil {
		t.Fatalf("统计候选失败: %v", err)
	}
	if candidates != 0 {
		t.Fatalf("人物删除后候选应级联清理(%s): %d", dbtest.Backend(), candidates)
	}
}

// 簇被删除时观测不跟着消失，只是回到"未归簇"（外键 SET NULL）：
// 观测是原始证据，簇只是对它的一种归组。
func TestFaceObservationSurvivesClusterDeletion(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	cluster := models.FaceCluster{Status: models.FaceClusterStatusUnnamed, Centroid: make([]byte, 2048), ObservationCount: 1}
	if err := db.Create(&cluster).Error; err != nil {
		t.Fatalf("创建簇失败: %v", err)
	}
	observation := models.FaceObservation{
		MediaKind: models.FaceMediaKindImage, MediaID: 3, SourceFingerprint: "10-20",
		BBox: "0,0,1,1", BBoxHash: "000000999999", ClusterID: &cluster.ID,
		Embedding: make([]byte, 2048), AppendStatus: models.FaceAppendStatusPending,
	}
	if err := db.Create(&observation).Error; err != nil {
		t.Fatalf("写观测失败: %v", err)
	}
	if err := db.Delete(&models.FaceCluster{}, cluster.ID).Error; err != nil {
		t.Fatalf("删除簇失败(%s): %v", dbtest.Backend(), err)
	}
	var reloaded models.FaceObservation
	if err := db.First(&reloaded, observation.ID).Error; err != nil {
		t.Fatalf("观测应仍然存在(%s): %v", dbtest.Backend(), err)
	}
	if reloaded.ClusterID != nil {
		t.Fatalf("簇删除后观测的 cluster_id 应置空(%s): %v", dbtest.Backend(), *reloaded.ClusterID)
	}
}

// 两列新设置：默认值都是零值，因此不需要显式迁移，也不该带 gorm default 标签。
func TestFaceSettingsColumnsDefaultToZeroValues(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatalf("读设置失败: %v", err)
	}
	if settings.AutoFaceAnalysis {
		t.Fatalf("auto_face_analysis 默认应为关(%s)", dbtest.Backend())
	}
	if settings.FaceModelMirrorURL != "" {
		t.Fatalf("face_model_mirror_url 默认应为空(%s): %q", dbtest.Backend(), settings.FaceModelMirrorURL)
	}
	// 用户关掉/清空之后重启不该被翻回来（沿用 P-003 的教训：默认值不靠 gorm 标签）。
	if err := db.Model(&models.Settings{}).Where("id = ?", settings.ID).
		Updates(map[string]interface{}{"auto_face_analysis": true, "face_model_mirror_url": "https://mirror.example.com/"}).Error; err != nil {
		t.Fatalf("改设置失败: %v", err)
	}
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重复建立 schema 失败: %v", err)
	}
	var after models.Settings
	if err := db.First(&after).Error; err != nil {
		t.Fatalf("读设置失败: %v", err)
	}
	if !after.AutoFaceAnalysis || after.FaceModelMirrorURL != "https://mirror.example.com/" {
		t.Fatalf("再次建 schema 不该改动用户设的值(%s): %+v", dbtest.Backend(), after)
	}
}
