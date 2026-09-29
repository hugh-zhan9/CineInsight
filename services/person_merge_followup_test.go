package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"video-master/database"
	"video-master/models"
)

// META05：来源人物的人脸候选迁到目标，(cluster_id, person_id) 冲突的行去重。
func TestPersonServiceMETA05MergeMovesFaceCandidates(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	target, _ := svc.CreatePerson("目标", "")
	source, _ := svc.CreatePerson("来源", "")
	source2, _ := svc.CreatePerson("来源二", "")
	clusters := make([]models.FaceCluster, 3)
	for i := range clusters {
		clusters[i] = models.FaceCluster{Status: models.FaceClusterStatusUnnamed}
		if err := database.DB.Create(&clusters[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	candidates := []models.FacePersonCandidate{
		{ClusterID: clusters[0].ID, PersonID: target.ID, Similarity: 0.9, CreatedAt: now, UpdatedAt: now},
		{ClusterID: clusters[0].ID, PersonID: source.ID, Similarity: 0.8, CreatedAt: now, UpdatedAt: now}, // 与目标冲突
		{ClusterID: clusters[1].ID, PersonID: source.ID, Similarity: 0.7, CreatedAt: now, UpdatedAt: now},
		{ClusterID: clusters[1].ID, PersonID: source2.ID, Similarity: 0.6, CreatedAt: now, UpdatedAt: now}, // 两个来源同簇
		{ClusterID: clusters[2].ID, PersonID: source2.ID, Similarity: 0.5, CreatedAt: now, UpdatedAt: now},
	}
	if err := database.DB.Create(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MergePeople(target.ID, []uint{source.ID, source2.ID}); err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	assertConversionCount(t, "face_person_candidates", "person_id <> ?", []any{target.ID}, 0)
	assertConversionCount(t, "face_person_candidates", "person_id = ?", []any{target.ID}, 3)
	var kept models.FacePersonCandidate
	if err := database.DB.Where("cluster_id = ? AND person_id = ?", clusters[0].ID, target.ID).First(&kept).Error; err != nil || kept.Similarity != 0.9 {
		t.Fatalf("冲突行应保留目标已有的候选: %+v err=%v", kept, err)
	}
	var moved models.FacePersonCandidate
	if err := database.DB.Where("cluster_id = ? AND person_id = ?", clusters[1].ID, target.ID).First(&moved).Error; err != nil || moved.Similarity != 0.7 {
		t.Fatalf("同簇的多个来源候选应去重并取最高相似度: %+v err=%v", moved, err)
	}
}

// META05：头像复制失败只给警告，合并已提交、不回滚；来源头像文件照常清理。
func TestPersonServiceMETA05MergeAvatarCopyFailureWarnsButKeepsMerge(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	svc := NewPersonService(dataDir)
	target, _ := svc.CreatePerson("目标", "")
	source, _ := svc.CreatePerson("来源", "")
	video := p017Video(t, "m.mp4")
	if err := svc.AddPersonVideos(source.ID, []uint{video.ID}); err != nil {
		t.Fatal(err)
	}
	avatarSource := filepath.Join(t.TempDir(), "avatar.png")
	writeTestPNG(t, avatarSource)
	withAvatar, err := svc.SetPersonAvatar(source.ID, avatarSource)
	if err != nil || withAvatar.AvatarPath == "" {
		t.Fatalf("设置来源头像失败: %+v err=%v", withAvatar, err)
	}
	// 让目标的头像目录建不出来：同名普通文件占位。
	blocker := filepath.Join(dataDir, "media-details", "people", itoa(target.ID))
	if err := os.MkdirAll(filepath.Dir(blocker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := svc.MergePeople(target.ID, []uint{source.ID})
	if err != nil {
		t.Fatalf("头像复制失败不应让合并失败: %v", err)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("应返回一条头像警告: %+v", result.Warnings)
	}
	assertConversionCount(t, "people", "id = ?", []any{source.ID}, 0)
	assertConversionCount(t, "video_people", "person_id = ?", []any{target.ID}, 1)
	var merged models.Person
	if err := database.DB.First(&merged, target.ID).Error; err != nil || merged.AvatarPath != "" {
		t.Fatalf("复制失败时目标不应写入头像路径: %+v err=%v", merged, err)
	}
}

// META05：事务失败（来源不存在）时不得留下已复制的头像文件——复制发生在提交之后。
func TestPersonServiceMETA05FailedMergeLeavesNoOrphanAvatar(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	svc := NewPersonService(dataDir)
	target, _ := svc.CreatePerson("目标", "")
	source, _ := svc.CreatePerson("来源", "")
	avatarSource := filepath.Join(t.TempDir(), "avatar.png")
	writeTestPNG(t, avatarSource)
	if _, err := svc.SetPersonAvatar(source.ID, avatarSource); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MergePeople(target.ID, []uint{source.ID, 9999}); err == nil {
		t.Fatal("含不存在来源的合并应失败")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "media-details", "people", itoa(target.ID))); !os.IsNotExist(err) {
		t.Fatalf("失败的合并不得在目标下留下头像文件: err=%v", err)
	}
	assertConversionCount(t, "people", "1=1", nil, 2)
}

// META02：转换 → 合并人物 → 撤销转换：关系从合并后的目标上移除，目标的其他关系不受影响。
func TestTagPersonConversionMETA02UndoAfterMergeRemovesRelationsFromTarget(t *testing.T) {
	tag, videos, _ := conversionFixture(t)
	tags := &TagService{}
	people := NewPersonService(t.TempDir())
	result, err := tags.ConvertTagToPerson(TagPersonConversionRequest{TagID: tag.ID, TagName: tag.Name, CreateNew: true})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := people.CreatePerson("合并目标", "")
	other := p017Video(t, "other.mp4")
	if err := people.AddPersonVideos(target.ID, []uint{other.ID}); err != nil {
		t.Fatal(err)
	}
	var record models.TagPersonConversion
	if err := database.DB.First(&record, result.ConversionID).Error; err != nil {
		t.Fatal(err)
	}
	sourceID := record.PersonID
	if _, err := people.MergePeople(target.ID, []uint{sourceID}); err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if err := database.DB.First(&record, result.ConversionID).Error; err != nil || record.PersonID != target.ID || record.PersonCreated {
		t.Fatalf("转换记录应改指向目标且不再算新建: %+v err=%v", record, err)
	}
	assertConversionCount(t, "video_people", "person_id = ?", []any{target.ID}, 3) // other + 两个打标视频（含已软删的）

	if _, err := tags.UndoTagPersonConversion(result.ConversionID); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	assertConversionCount(t, "video_people", "person_id = ? AND video_id = ?", []any{target.ID, videos[0].ID}, 0)
	assertConversionCount(t, "video_people", "person_id = ? AND video_id = ?", []any{target.ID, videos[1].ID}, 0)
	assertConversionCount(t, "video_people", "person_id = ? AND video_id = ?", []any{target.ID, other.ID}, 1)
	assertConversionCount(t, "people", "id = ?", []any{target.ID}, 1)
}
