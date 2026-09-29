package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 人脸审阅（D-019、D-015 写入部分）。
//
// 这一组测试要钉住的核心只有一条：video_people / image_people 只在用户动作的事务里
// 被写，其余任何路径（分析、吸收进已命名簇、清除数据）一行都不写。

// ===== 夹具 =====

func newFaceReviewTestService() *FaceReviewService {
	return &FaceReviewService{}
}

func seedFaceCluster(t *testing.T, status string, personID *uint, centroid []float32) models.FaceCluster {
	t.Helper()
	cluster := models.FaceCluster{Status: status, PersonID: personID}
	if centroid != nil {
		cluster.Centroid = encodeFaceEmbedding(centroid)
	}
	if err := database.DB.Create(&cluster).Error; err != nil {
		t.Fatalf("建簇失败: %v", err)
	}
	return cluster
}

// seedFaceObservation 造一条挂在簇上的观测。bboxHash 由调用方给出：观测唯一键包含它，
// 同一件媒体上的两条观测必须给不同的值；帧位置写哨兵值（非视频帧），唯一键里不能出现 NULL。
func seedFaceObservation(t *testing.T, clusterID uint, kind string, mediaID uint, bboxHash string, appendStatus string, quality float64) models.FaceObservation {
	t.Helper()
	frameMS := models.FaceFrameMSNone
	observation := models.FaceObservation{
		MediaKind:         kind,
		MediaID:           mediaID,
		SourceFingerprint: "fp-1",
		FrameMS:           &frameMS,
		BBox:              "0.100000,0.100000,0.200000,0.200000",
		BBoxHash:          bboxHash,
		Quality:           quality,
		Embedding:         encodeFaceEmbedding(faceUnitVector(0)),
		ClusterID:         &clusterID,
		AppendStatus:      appendStatus,
	}
	if err := database.DB.Create(&observation).Error; err != nil {
		t.Fatalf("建观测失败: %v", err)
	}
	// observation_count 与聚类（attachToFaceCluster 的 +1）同口径维护：分页按这一列排序（META-11）。
	if err := database.DB.Model(&models.FaceCluster{}).Where("id = ?", clusterID).
		Updates(map[string]interface{}{
			"representative_observation_id": observation.ID,
			"observation_count":             gorm.Expr("observation_count + 1"),
		}).Error; err != nil {
		t.Fatalf("写簇代表失败: %v", err)
	}
	return observation
}

// seedFacePersonSeed 给人物造一条头像观测（种子向量），候选展示要靠它重算相似度。
func seedFacePersonSeed(t *testing.T, personID uint, vector []float32) {
	t.Helper()
	frameMS := models.FaceFrameMSNone
	observation := models.FaceObservation{
		MediaKind:         models.FaceMediaKindPersonAvatar,
		MediaID:           personID,
		SourceFingerprint: "avatar-1",
		FrameMS:           &frameMS,
		BBox:              "0.100000,0.100000,0.200000,0.200000",
		BBoxHash:          "seed00000001",
		Quality:           0.9,
		Embedding:         encodeFaceEmbedding(vector),
		AppendStatus:      models.FaceAppendStatusNone,
	}
	if err := database.DB.Create(&observation).Error; err != nil {
		t.Fatalf("建头像观测失败: %v", err)
	}
}

func faceClusterByID(t *testing.T, clusterID uint) models.FaceCluster {
	t.Helper()
	var cluster models.FaceCluster
	if err := database.DB.First(&cluster, clusterID).Error; err != nil {
		t.Fatalf("读簇失败: %v", err)
	}
	return cluster
}

func faceObservationByID(t *testing.T, observationID uint) models.FaceObservation {
	t.Helper()
	var observation models.FaceObservation
	if err := database.DB.First(&observation, observationID).Error; err != nil {
		t.Fatalf("读观测失败: %v", err)
	}
	return observation
}

// ===== 命名 =====

// 命名未命名簇：建人物、按媒体去重写两张关系表、删候选、观测全部标已确认（7.3.1）。
func TestNameFaceClusterCreatesPersonAndWritesBothRelationTables(t *testing.T) {
	setupFaceTestDB(t)
	video := seedFaceTestVideo(t, "movie.mp4")
	image := seedFaceTestImage(t, "a.jpg")
	other := seedFaceTestPerson(t, "别人", false)
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	// 同一件视频上两条观测：关系只该写一行。
	first := seedFaceObservation(t, cluster.ID, models.FaceMediaKindVideo, video.ID, "000100010002", models.FaceAppendStatusNone, 0.9)
	second := seedFaceObservation(t, cluster.ID, models.FaceMediaKindVideo, video.ID, "000200020002", models.FaceAppendStatusNone, 0.7)
	third := seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, image.ID, "000300030002", models.FaceAppendStatusNone, 0.8)
	if err := database.DB.Create(&models.FacePersonCandidate{ClusterID: cluster.ID, PersonID: other.ID, Similarity: 0.7}).Error; err != nil {
		t.Fatalf("建候选失败: %v", err)
	}

	service := newFaceReviewTestService()
	view, err := service.NameFaceCluster(context.Background(), cluster.ID, "周迅", "Zhou Xun")
	if err != nil {
		t.Fatalf("命名簇失败: %v", err)
	}
	if view.Status != models.FaceClusterStatusNamed || view.PersonName != "周迅" {
		t.Fatalf("返回视图应是已命名的周迅: %+v", view)
	}
	if view.ObservationCount != 3 || view.VideoCount != 1 || view.ImageCount != 1 {
		t.Fatalf("视图计数不对（3 条观测 / 1 个视频 / 1 张图片）: %+v", view)
	}

	var person models.Person
	if err := database.DB.Where("display_name = ?", "周迅").First(&person).Error; err != nil {
		t.Fatalf("应新建人物: %v", err)
	}
	if person.OriginalName != "Zhou Xun" {
		t.Fatalf("原始名应保存: %q", person.OriginalName)
	}
	if count := countFaceRows(t, &models.VideoPerson{}, "person_id = ?", person.ID); count != 1 {
		t.Fatalf("同一视频的两条观测只该写一行 video_people，实际 %d", count)
	}
	if count := countFaceRows(t, &models.ImagePerson{}, "person_id = ?", person.ID); count != 1 {
		t.Fatalf("image_people 应有一行，实际 %d", count)
	}
	stored := faceClusterByID(t, cluster.ID)
	if stored.Status != models.FaceClusterStatusNamed || stored.PersonID == nil || *stored.PersonID != person.ID {
		t.Fatalf("簇应翻成 named 并指向新人物: %+v", stored)
	}
	if count := countFaceRows(t, &models.FacePersonCandidate{}, "cluster_id = ?", cluster.ID); count != 0 {
		t.Fatalf("命名后该簇候选应删除，实际 %d", count)
	}
	for _, id := range []uint{first.ID, second.ID, third.ID} {
		if status := faceObservationByID(t, id).AppendStatus; status != models.FaceAppendStatusConfirmed {
			t.Fatalf("首次命名等于确认簇内全部观测，观测 %d 状态 %q", id, status)
		}
	}
}

// 名称非法时什么都不写：错误码 person_name_invalid（7.3.1）。
func TestNameFaceClusterRejectsInvalidName(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, image.ID, "000100010002", models.FaceAppendStatusNone, 0.9)

	service := newFaceReviewTestService()
	if _, err := service.NameFaceCluster(context.Background(), cluster.ID, "   ", ""); !errors.Is(err, ErrFacePersonNameInvalid) {
		t.Fatalf("空名称应报 person_name_invalid: %v", err)
	}
	if count := countFaceRows(t, &models.Person{}, ""); count != 0 {
		t.Fatalf("名称非法不该建人物，实际 %d", count)
	}
	if count := countFaceRows(t, &models.ImagePerson{}, ""); count != 0 {
		t.Fatalf("名称非法不该写关系，实际 %d", count)
	}
	if faceClusterByID(t, cluster.ID).Status != models.FaceClusterStatusUnnamed {
		t.Fatal("名称非法簇状态不该变")
	}
}

func TestNameFaceClusterRejectsMissingCluster(t *testing.T) {
	setupFaceTestDB(t)
	service := newFaceReviewTestService()
	if _, err := service.NameFaceCluster(context.Background(), 9999, "周迅", ""); !errors.Is(err, ErrFaceClusterNotFound) {
		t.Fatalf("簇不存在应报 cluster_not_found: %v", err)
	}
}

// 两次命名同一个簇：第二次报 cluster_not_unnamed，人物与关系都不重复（4.4.4）。
func TestNameFaceClusterConcurrentSecondCallConflicts(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, image.ID, "000100010002", models.FaceAppendStatusNone, 0.9)

	service := newFaceReviewTestService()
	var wait sync.WaitGroup
	results := make([]error, 2)
	names := []string{"周迅", "汤唯"}
	wait.Add(2)
	for i := 0; i < 2; i++ {
		go func(index int) {
			defer wait.Done()
			_, err := service.NameFaceCluster(context.Background(), cluster.ID, names[index], "")
			results[index] = err
		}(i)
	}
	wait.Wait()

	succeeded, conflicted := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrFaceClusterNotUnnamed):
			conflicted++
		default:
			t.Fatalf("并发命名只该出现成功或 cluster_not_unnamed: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("并发命名应一次成功一次冲突，实际成功 %d 冲突 %d", succeeded, conflicted)
	}
	if count := countFaceRows(t, &models.Person{}, ""); count != 1 {
		t.Fatalf("只该建出一个人物，实际 %d", count)
	}
	if count := countFaceRows(t, &models.ImagePerson{}, ""); count != 1 {
		t.Fatalf("只该写一行 image_people，实际 %d", count)
	}
}

// 已命名的簇不能再被命名（顺序调用同样报 cluster_not_unnamed）。
func TestNameFaceClusterRejectsAlreadyNamedCluster(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	person := seedFaceTestPerson(t, "周迅", false)
	cluster := seedFaceCluster(t, models.FaceClusterStatusNamed, &person.ID, faceUnitVector(0))
	seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, image.ID, "000100010002", models.FaceAppendStatusNone, 0.9)

	service := newFaceReviewTestService()
	if _, err := service.NameFaceCluster(context.Background(), cluster.ID, "汤唯", ""); !errors.Is(err, ErrFaceClusterNotUnnamed) {
		t.Fatalf("已命名簇应报 cluster_not_unnamed: %v", err)
	}
	if count := countFaceRows(t, &models.Person{}, ""); count != 1 {
		t.Fatalf("失败的命名不该留下人物，实际 %d", count)
	}
}

// ===== 关联到现有人物 =====

func TestLinkFaceClusterUsesExistingPerson(t *testing.T) {
	setupFaceTestDB(t)
	video := seedFaceTestVideo(t, "movie.mp4")
	image := seedFaceTestImage(t, "a.jpg")
	person := seedFaceTestPerson(t, "周迅", false)
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	seedFaceObservation(t, cluster.ID, models.FaceMediaKindVideo, video.ID, "000100010002", models.FaceAppendStatusNone, 0.9)
	seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, image.ID, "000200020002", models.FaceAppendStatusNone, 0.8)
	if err := database.DB.Create(&models.FacePersonCandidate{ClusterID: cluster.ID, PersonID: person.ID, Similarity: 0.8}).Error; err != nil {
		t.Fatalf("建候选失败: %v", err)
	}

	service := newFaceReviewTestService()
	view, err := service.LinkFaceCluster(context.Background(), cluster.ID, person.ID)
	if err != nil {
		t.Fatalf("关联人物失败: %v", err)
	}
	if view.PersonID != person.ID || view.Status != models.FaceClusterStatusNamed {
		t.Fatalf("视图应指向已有人物: %+v", view)
	}
	if count := countFaceRows(t, &models.Person{}, ""); count != 1 {
		t.Fatalf("关联不该新建人物，实际 %d 个", count)
	}
	if count := countFaceRows(t, &models.VideoPerson{}, "person_id = ?", person.ID); count != 1 {
		t.Fatalf("video_people 应有一行，实际 %d", count)
	}
	if count := countFaceRows(t, &models.ImagePerson{}, "person_id = ?", person.ID); count != 1 {
		t.Fatalf("image_people 应有一行，实际 %d", count)
	}
	if count := countFaceRows(t, &models.FacePersonCandidate{}, "cluster_id = ?", cluster.ID); count != 0 {
		t.Fatalf("关联后候选应删除，实际 %d", count)
	}
}

// 同一个人被拆成两簇时，第二簇 Link 到同一人物即合并语义（4.4.4）：
// 已存在的关系靠 ON CONFLICT 跳过，不报错也不重复。
func TestLinkFaceClusterMergesSecondClusterIntoSamePerson(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	second := seedFaceTestImage(t, "b.jpg")
	person := seedFaceTestPerson(t, "周迅", false)
	first := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	seedFaceObservation(t, first.ID, models.FaceMediaKindImage, image.ID, "000100010002", models.FaceAppendStatusNone, 0.9)
	other := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(1))
	// 第二个簇里既有已经关联过的那张图，也有一张新图。
	seedFaceObservation(t, other.ID, models.FaceMediaKindImage, image.ID, "000200020002", models.FaceAppendStatusNone, 0.7)
	seedFaceObservation(t, other.ID, models.FaceMediaKindImage, second.ID, "000300030002", models.FaceAppendStatusNone, 0.7)

	service := newFaceReviewTestService()
	if _, err := service.LinkFaceCluster(context.Background(), first.ID, person.ID); err != nil {
		t.Fatalf("关联第一个簇失败: %v", err)
	}
	if _, err := service.LinkFaceCluster(context.Background(), other.ID, person.ID); err != nil {
		t.Fatalf("关联第二个簇失败: %v", err)
	}
	if count := countFaceRows(t, &models.ImagePerson{}, "person_id = ?", person.ID); count != 2 {
		t.Fatalf("两张图各一行关系，实际 %d", count)
	}
}

func TestLinkFaceClusterRejectsMissingPerson(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, image.ID, "000100010002", models.FaceAppendStatusNone, 0.9)

	service := newFaceReviewTestService()
	if _, err := service.LinkFaceCluster(context.Background(), cluster.ID, 4242); !errors.Is(err, ErrFacePersonNotFound) {
		t.Fatalf("人物不存在应报 person_not_found: %v", err)
	}
	if count := countFaceRows(t, &models.ImagePerson{}, ""); count != 0 {
		t.Fatalf("失败的关联不该写关系，实际 %d", count)
	}
	if faceClusterByID(t, cluster.ID).Status != models.FaceClusterStatusUnnamed {
		t.Fatal("失败的关联不该改簇状态")
	}
}

// ===== 忽略 =====

func TestIgnoreFaceClusterKeepsObservationsAndDropsCandidates(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	person := seedFaceTestPerson(t, "周迅", false)
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, image.ID, "000100010002", models.FaceAppendStatusNone, 0.9)
	if err := database.DB.Create(&models.FacePersonCandidate{ClusterID: cluster.ID, PersonID: person.ID, Similarity: 0.8}).Error; err != nil {
		t.Fatalf("建候选失败: %v", err)
	}

	service := newFaceReviewTestService()
	if err := service.IgnoreFaceCluster(context.Background(), cluster.ID); err != nil {
		t.Fatalf("忽略簇失败: %v", err)
	}
	if status := faceClusterByID(t, cluster.ID).Status; status != models.FaceClusterStatusIgnored {
		t.Fatalf("簇应翻成 ignored: %q", status)
	}
	if count := countFaceRows(t, &models.FaceObservation{}, "cluster_id = ?", cluster.ID); count != 1 {
		t.Fatalf("忽略保留观测（源不变就不再提示），实际 %d", count)
	}
	if count := countFaceRows(t, &models.FacePersonCandidate{}, ""); count != 0 {
		t.Fatalf("忽略后候选应删除，实际 %d", count)
	}
	if count := countFaceRows(t, &models.ImagePerson{}, ""); count != 0 {
		t.Fatalf("忽略不写任何关系，实际 %d", count)
	}
	// 再忽略一次是同一个终态，不报错。
	if err := service.IgnoreFaceCluster(context.Background(), cluster.ID); err != nil {
		t.Fatalf("重复忽略应幂等: %v", err)
	}
}

func TestIgnoreFaceClusterRejectsNamedCluster(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	person := seedFaceTestPerson(t, "周迅", false)
	cluster := seedFaceCluster(t, models.FaceClusterStatusNamed, &person.ID, faceUnitVector(0))
	seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, image.ID, "000100010002", models.FaceAppendStatusNone, 0.9)

	service := newFaceReviewTestService()
	if err := service.IgnoreFaceCluster(context.Background(), cluster.ID); !errors.Is(err, ErrFaceClusterNotUnnamed) {
		t.Fatalf("已命名簇不能被忽略: %v", err)
	}
}

// ===== 追加候选 =====

// 确认追加只写 pending 观测涉及的媒体，已确认过的观测不再重复写（D-019）。
func TestConfirmFaceClusterAppendWritesOnlyPendingMedia(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	added := seedFaceTestImage(t, "b.jpg")
	video := seedFaceTestVideo(t, "movie.mp4")
	person := seedFaceTestPerson(t, "周迅", false)
	cluster := seedFaceCluster(t, models.FaceClusterStatusNamed, &person.ID, faceUnitVector(0))
	confirmed := seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, image.ID, "000100010002", models.FaceAppendStatusConfirmed, 0.9)
	pendingImage := seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, added.ID, "000200020002", models.FaceAppendStatusPending, 0.8)
	pendingVideo := seedFaceObservation(t, cluster.ID, models.FaceMediaKindVideo, video.ID, "000300030002", models.FaceAppendStatusPending, 0.7)
	// 首次命名时已经写过的那一行关系。
	if err := database.DB.Create(&models.ImagePerson{ImageID: image.ID, PersonID: person.ID}).Error; err != nil {
		t.Fatalf("建既有关系失败: %v", err)
	}

	service := newFaceReviewTestService()
	views, err := service.ListFaceClusters(context.Background(), FaceClusterFilter{Status: models.FaceClusterStatusNamed})
	if err != nil {
		t.Fatalf("列簇失败: %v", err)
	}
	if len(views) != 1 || views[0].AppendPendingCount != 2 {
		t.Fatalf("已命名簇应报 2 条待追加观测: %+v", views)
	}
	if len(views[0].AppendPendingMedia) != 2 {
		t.Fatalf("追加候选应列出两件新媒体: %+v", views[0].AppendPendingMedia)
	}

	if err := service.ConfirmFaceClusterAppend(context.Background(), cluster.ID); err != nil {
		t.Fatalf("确认追加失败: %v", err)
	}
	if count := countFaceRows(t, &models.ImagePerson{}, "person_id = ?", person.ID); count != 2 {
		t.Fatalf("确认追加后 image_people 应有两行，实际 %d", count)
	}
	if count := countFaceRows(t, &models.VideoPerson{}, "person_id = ?", person.ID); count != 1 {
		t.Fatalf("确认追加后 video_people 应有一行，实际 %d", count)
	}
	for _, id := range []uint{pendingImage.ID, pendingVideo.ID} {
		if status := faceObservationByID(t, id).AppendStatus; status != models.FaceAppendStatusConfirmed {
			t.Fatalf("确认后 pending 观测 %d 应置 confirmed，实际 %q", id, status)
		}
	}
	if status := faceObservationByID(t, confirmed.ID).AppendStatus; status != models.FaceAppendStatusConfirmed {
		t.Fatalf("既有已确认观测状态不该变: %q", status)
	}
	// 重复确认是幂等的：没有 pending 就什么都不做。
	if err := service.ConfirmFaceClusterAppend(context.Background(), cluster.ID); err != nil {
		t.Fatalf("重复确认应幂等: %v", err)
	}
	if count := countFaceRows(t, &models.ImagePerson{}, "person_id = ?", person.ID); count != 2 {
		t.Fatalf("重复确认不该多写关系，实际 %d", count)
	}
}

// 忽略追加：观测置 dismissed（同一观测不再提示），一行关系都不写（D-019）。
func TestDismissFaceClusterAppendWritesNoRelations(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	added := seedFaceTestImage(t, "b.jpg")
	person := seedFaceTestPerson(t, "周迅", false)
	cluster := seedFaceCluster(t, models.FaceClusterStatusNamed, &person.ID, faceUnitVector(0))
	seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, image.ID, "000100010002", models.FaceAppendStatusConfirmed, 0.9)
	pending := seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, added.ID, "000200020002", models.FaceAppendStatusPending, 0.8)

	service := newFaceReviewTestService()
	if err := service.DismissFaceClusterAppend(context.Background(), cluster.ID); err != nil {
		t.Fatalf("忽略追加失败: %v", err)
	}
	if status := faceObservationByID(t, pending.ID).AppendStatus; status != models.FaceAppendStatusDismissed {
		t.Fatalf("忽略追加应把观测置 dismissed，实际 %q", status)
	}
	if count := countFaceRows(t, &models.ImagePerson{}, ""); count != 0 {
		t.Fatalf("忽略追加不写关系，实际 %d", count)
	}
	views, err := service.ListFaceClusters(context.Background(), FaceClusterFilter{})
	if err != nil {
		t.Fatalf("列簇失败: %v", err)
	}
	if len(views) != 1 || views[0].AppendPendingCount != 0 || len(views[0].AppendPendingMedia) != 0 {
		t.Fatalf("忽略后不该再提示这条追加候选: %+v", views)
	}
}

func TestConfirmFaceClusterAppendRequiresNamedCluster(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, image.ID, "000100010002", models.FaceAppendStatusPending, 0.9)

	service := newFaceReviewTestService()
	if err := service.ConfirmFaceClusterAppend(context.Background(), cluster.ID); !errors.Is(err, ErrFaceClusterNotNamed) {
		t.Fatalf("未命名簇确认追加应报 cluster_not_named: %v", err)
	}
	if count := countFaceRows(t, &models.ImagePerson{}, ""); count != 0 {
		t.Fatalf("失败的确认不该写关系，实际 %d", count)
	}
}

// ===== 列表 =====

// 列表按观测数降序，报出涉及的视频数与图片数，并只展示当前仍过阈值的候选人物。
func TestListFaceClustersOrdersByObservationsAndFiltersDriftedCandidates(t *testing.T) {
	setupFaceTestDB(t)
	firstImage := seedFaceTestImage(t, "a.jpg")
	secondImage := seedFaceTestImage(t, "b.jpg")
	video := seedFaceTestVideo(t, "movie.mp4")
	near := seedFaceTestPerson(t, "周迅", false)
	far := seedFaceTestPerson(t, "汤唯", false)
	// 种子向量：near 与簇代表完全同向（1.0 ≥ 0.6），far 与之正交（0 < 0.6）。
	seedFacePersonSeed(t, near.ID, faceUnitVector(0))
	seedFacePersonSeed(t, far.ID, faceUnitVector(3))

	small := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	seedFaceObservation(t, small.ID, models.FaceMediaKindImage, firstImage.ID, "000100010002", models.FaceAppendStatusNone, 0.9)
	big := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	seedFaceObservation(t, big.ID, models.FaceMediaKindImage, secondImage.ID, "000200020002", models.FaceAppendStatusNone, 0.9)
	seedFaceObservation(t, big.ID, models.FaceMediaKindVideo, video.ID, "000300030002", models.FaceAppendStatusNone, 0.8)
	seedFaceObservation(t, big.ID, models.FaceMediaKindVideo, video.ID, "000400040002", models.FaceAppendStatusNone, 0.7)
	for _, clusterID := range []uint{small.ID, big.ID} {
		for _, personID := range []uint{near.ID, far.ID} {
			if err := database.DB.Create(&models.FacePersonCandidate{ClusterID: clusterID, PersonID: personID, Similarity: 0.95}).Error; err != nil {
				t.Fatalf("建候选失败: %v", err)
			}
		}
	}

	service := newFaceReviewTestService()
	views, err := service.ListFaceClusters(context.Background(), FaceClusterFilter{})
	if err != nil {
		t.Fatalf("列簇失败: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("应返回两个簇，实际 %d", len(views))
	}
	if views[0].ID != big.ID {
		t.Fatalf("观测多的簇应排在前面: %+v", views)
	}
	if views[0].ObservationCount != 3 || views[0].VideoCount != 1 || views[0].ImageCount != 1 {
		t.Fatalf("同一视频两条观测只算一个视频: %+v", views[0])
	}
	if views[0].RepresentativeObservationID == 0 {
		t.Fatal("簇卡片需要代表观测以取裁剪图")
	}
	if len(views[0].Candidates) != 1 || views[0].Candidates[0].PersonID != near.ID {
		t.Fatalf("漂到阈值以下的旧候选不该展示: %+v", views[0].Candidates)
	}
	if views[0].Candidates[0].Similarity < facePersonSeedThreshold {
		t.Fatalf("展示的相似度应是重算值: %+v", views[0].Candidates[0])
	}
	if views[1].ObservationCount != 1 {
		t.Fatalf("第二个簇应只有一条观测: %+v", views[1])
	}
}

func TestListFaceClustersFiltersByStatusAndMediaKind(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	video := seedFaceTestVideo(t, "movie.mp4")
	person := seedFaceTestPerson(t, "周迅", false)
	imageOnly := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	seedFaceObservation(t, imageOnly.ID, models.FaceMediaKindImage, image.ID, "000100010002", models.FaceAppendStatusNone, 0.9)
	videoOnly := seedFaceCluster(t, models.FaceClusterStatusNamed, &person.ID, faceUnitVector(1))
	seedFaceObservation(t, videoOnly.ID, models.FaceMediaKindVideo, video.ID, "000200020002", models.FaceAppendStatusNone, 0.9)
	ignored := seedFaceCluster(t, models.FaceClusterStatusIgnored, nil, faceUnitVector(2))
	seedFaceObservation(t, ignored.ID, models.FaceMediaKindImage, image.ID, "000300030002", models.FaceAppendStatusNone, 0.9)

	service := newFaceReviewTestService()
	unnamed, err := service.ListFaceClusters(context.Background(), FaceClusterFilter{Status: models.FaceClusterStatusUnnamed})
	if err != nil {
		t.Fatalf("列未命名簇失败: %v", err)
	}
	if len(unnamed) != 1 || unnamed[0].ID != imageOnly.ID {
		t.Fatalf("状态筛选应只留未命名簇: %+v", unnamed)
	}
	videos, err := service.ListFaceClusters(context.Background(), FaceClusterFilter{MediaKind: models.FaceMediaKindVideo})
	if err != nil {
		t.Fatalf("按媒体类型列簇失败: %v", err)
	}
	if len(videos) != 1 || videos[0].ID != videoOnly.ID {
		t.Fatalf("媒体类型筛选应只留有视频观测的簇: %+v", videos)
	}
	if videos[0].PersonName != "周迅" {
		t.Fatalf("已命名簇应带出人物名: %+v", videos[0])
	}
	all, err := service.ListFaceClusters(context.Background(), FaceClusterFilter{})
	if err != nil {
		t.Fatalf("列全部簇失败: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("不筛时三个簇都在，实际 %d", len(all))
	}
}

func TestListFaceClustersOnEmptyLibrary(t *testing.T) {
	setupFaceTestDB(t)
	service := newFaceReviewTestService()
	views, err := service.ListFaceClusters(context.Background(), FaceClusterFilter{})
	if err != nil {
		t.Fatalf("空库列簇失败: %v", err)
	}
	if len(views) != 0 {
		t.Fatalf("空库应返回空列表，实际 %d", len(views))
	}
}

// ===== 人物删除 =====

// 人物被删除后簇回到未命名重新出现在面板上（4.4.4，外键 SET NULL）。
func TestFaceClusterReturnsToUnnamedAfterPersonDeleted(t *testing.T) {
	setupFaceTestDB(t)
	image := seedFaceTestImage(t, "a.jpg")
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, image.ID, "000100010002", models.FaceAppendStatusNone, 0.9)

	service := newFaceReviewTestService()
	if _, err := service.NameFaceCluster(context.Background(), cluster.ID, "周迅", ""); err != nil {
		t.Fatalf("命名簇失败: %v", err)
	}
	var person models.Person
	if err := database.DB.First(&person).Error; err != nil {
		t.Fatalf("读人物失败: %v", err)
	}
	if err := database.DB.Delete(&models.Person{}, person.ID).Error; err != nil {
		t.Fatalf("删除人物失败: %v", err)
	}
	// 外键把 person_id 置空，status 得由审阅侧拉回来。
	if stored := faceClusterByID(t, cluster.ID); stored.PersonID != nil {
		t.Fatalf("人物删除后簇的 person_id 应为空: %+v", stored.PersonID)
	}

	views, err := service.ListFaceClusters(context.Background(), FaceClusterFilter{Status: models.FaceClusterStatusUnnamed})
	if err != nil {
		t.Fatalf("列簇失败: %v", err)
	}
	if len(views) != 1 || views[0].ID != cluster.ID {
		t.Fatalf("簇应重新出现在未命名列表里: %+v", views)
	}
	if status := faceClusterByID(t, cluster.ID).Status; status != models.FaceClusterStatusUnnamed {
		t.Fatalf("簇状态应回到 unnamed，实际 %q", status)
	}
	// 关系随人物级联删除，簇可以重新命名。
	if count := countFaceRows(t, &models.ImagePerson{}, ""); count != 0 {
		t.Fatalf("人物删除应级联删关系，实际 %d", count)
	}
	if _, err := service.NameFaceCluster(context.Background(), cluster.ID, "汤唯", ""); err != nil {
		t.Fatalf("回到未命名的簇应能重新命名: %v", err)
	}
}

// ===== 与分析链路合起来：确认之前两表零写入 =====

// 完整链路（AC-11、AC-12、TC-05 审阅部分）：分析 → 命名 → 再分析吸收新观测只标
// pending → 确认追加才写第二行关系。
func TestFaceReviewWritesRelationsOnlyOnUserAction(t *testing.T) {
	setupFaceTestDB(t)
	first := seedFaceTestImage(t, "a.jpg")
	worker := newFakeFaceWorker()
	worker.set(models.FaceMediaKindImage, first.ID, faceAt(0.1, 0.1, 0.9, faceUnitVector(0)))
	analysis := newFaceAnalysisTestService(t, worker)
	runFaceAnalysis(t, analysis, FaceAnalysisScopeImages)

	if count := countFaceRows(t, &models.ImagePerson{}, ""); count != 0 {
		t.Fatalf("分析不写 image_people，实际 %d", count)
	}
	if count := countFaceRows(t, &models.VideoPerson{}, ""); count != 0 {
		t.Fatalf("分析不写 video_people，实际 %d", count)
	}

	review := newFaceReviewTestService()
	views, err := review.ListFaceClusters(context.Background(), FaceClusterFilter{Status: models.FaceClusterStatusUnnamed})
	if err != nil {
		t.Fatalf("列簇失败: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("分析后应有一个未命名簇，实际 %d", len(views))
	}
	if _, err := review.NameFaceCluster(context.Background(), views[0].ID, "周迅", ""); err != nil {
		t.Fatalf("命名簇失败: %v", err)
	}
	if count := countFaceRows(t, &models.ImagePerson{}, ""); count != 1 {
		t.Fatalf("命名后应有一行 image_people，实际 %d", count)
	}

	// 同一张脸出现在新图上：吸收进已命名簇只标 pending，关系不动。
	second := seedFaceTestImage(t, "b.jpg")
	worker.set(models.FaceMediaKindImage, second.ID, faceAt(0.2, 0.2, 0.8, faceUnitVector(0)))
	runFaceAnalysis(t, analysis, FaceAnalysisScopeImages)
	if count := countFaceRows(t, &models.ImagePerson{}, ""); count != 1 {
		t.Fatalf("吸收新观测不写关系，实际 %d 行", count)
	}
	if count := countFaceRows(t, &models.FaceObservation{}, "append_status = ?", models.FaceAppendStatusPending); count != 1 {
		t.Fatalf("新观测应标 pending，实际 %d 条", count)
	}

	named, err := review.ListFaceClusters(context.Background(), FaceClusterFilter{Status: models.FaceClusterStatusNamed})
	if err != nil {
		t.Fatalf("列已命名簇失败: %v", err)
	}
	if len(named) != 1 || named[0].AppendPendingCount != 1 {
		t.Fatalf("已命名簇应报一条追加候选: %+v", named)
	}
	if len(named[0].AppendPendingMedia) != 1 || named[0].AppendPendingMedia[0].Name != "b.jpg" {
		t.Fatalf("追加候选应指出是哪张新图: %+v", named[0].AppendPendingMedia)
	}

	if err := review.ConfirmFaceClusterAppend(context.Background(), named[0].ID); err != nil {
		t.Fatalf("确认追加失败: %v", err)
	}
	if count := countFaceRows(t, &models.ImagePerson{}, ""); count != 2 {
		t.Fatalf("确认追加后应有两行 image_people，实际 %d", count)
	}
	if count := countFaceRows(t, &models.FaceObservation{}, "append_status = ?", models.FaceAppendStatusPending); count != 0 {
		t.Fatalf("确认后不该再有 pending 观测，实际 %d", count)
	}
}

// ===== P-018 人脸可逆与分页（META-04 / META-11） =====

// namedFaceClusterOn 造一个已命名簇：每件媒体一条观测，经 LinkFaceCluster 写关系并置 confirmed。
func namedFaceClusterOn(t *testing.T, service *FaceReviewService, personID uint, videos []models.Video, hashPrefix string) models.FaceCluster {
	t.Helper()
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	for i, video := range videos {
		seedFaceObservation(t, cluster.ID, models.FaceMediaKindVideo, video.ID, fmt.Sprintf("%s%02d", hashPrefix, i), models.FaceAppendStatusNone, 0.9)
	}
	if _, err := service.LinkFaceCluster(context.Background(), cluster.ID, personID); err != nil {
		t.Fatalf("关联簇失败: %v", err)
	}
	return cluster
}

func videoRelationCount(t *testing.T, videoID, personID uint) int64 {
	t.Helper()
	return countFaceRows(t, &models.VideoPerson{}, "video_id = ? AND person_id = ?", videoID, personID)
}

// 恢复后回到未命名：ignored_at 清空，观测保留；重复恢复幂等；非忽略的已命名簇报 cluster_not_ignored。
func TestMETA04RestoreIgnoredClusterReturnsToUnnamed(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	video := seedFaceTestVideo(t, "a.mp4")
	person := seedFaceTestPerson(t, "周迅", false)
	service := newFaceReviewTestService()
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	seedFaceObservation(t, cluster.ID, models.FaceMediaKindVideo, video.ID, "r00000000001", models.FaceAppendStatusNone, 0.9)
	if err := service.IgnoreFaceCluster(ctx, cluster.ID); err != nil {
		t.Fatalf("忽略失败: %v", err)
	}
	if faceClusterByID(t, cluster.ID).IgnoredAt == nil {
		t.Fatal("忽略应写 ignored_at")
	}
	if err := service.RestoreFaceCluster(ctx, cluster.ID); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	restored := faceClusterByID(t, cluster.ID)
	if restored.Status != models.FaceClusterStatusUnnamed || restored.IgnoredAt != nil {
		t.Fatalf("恢复后应为未命名且无 ignored_at: %+v", restored)
	}
	if count := countFaceRows(t, &models.FaceObservation{}, "cluster_id = ?", cluster.ID); count != 1 {
		t.Fatalf("恢复不动观测: %d", count)
	}
	if err := service.RestoreFaceCluster(ctx, cluster.ID); err != nil {
		t.Fatalf("重复恢复应幂等: %v", err)
	}
	// 恢复后可以正常命名（回到未命名的簇能过 unnamed 校验）。
	if _, err := service.LinkFaceCluster(ctx, cluster.ID, person.ID); err != nil {
		t.Fatalf("恢复后应能关联: %v", err)
	}
	if err := service.RestoreFaceCluster(ctx, cluster.ID); !errors.Is(err, ErrFaceClusterNotIgnored) {
		t.Fatalf("已命名簇不能恢复: %v", err)
	}
	if err := service.RestoreFaceCluster(ctx, 9999); !errors.Is(err, ErrFaceClusterNotFound) {
		t.Fatalf("不存在的簇: %v", err)
	}
}

// 已忽略列表附忽略之后并入的观测数；历史行（无 ignored_at）显示 0；游标分页。
func TestMETA04ListIgnoredClustersReportsAbsorbedSinceIgnored(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	old := seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, 1, "i00000000001", models.FaceAppendStatusNone, 0.9)
	if err := service.IgnoreFaceCluster(ctx, cluster.ID); err != nil {
		t.Fatal(err)
	}
	ignoredAt := *faceClusterByID(t, cluster.ID).IgnoredAt
	if err := database.DB.Model(&models.FaceObservation{}).Where("id = ?", old.ID).
		UpdateColumn("updated_at", ignoredAt.Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		absorbed := seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, uint(10+i), fmt.Sprintf("i0000000001%d", i), models.FaceAppendStatusNone, 0.5)
		if err := database.DB.Model(&models.FaceObservation{}).Where("id = ?", absorbed.ID).
			UpdateColumn("updated_at", ignoredAt.Add(time.Minute)).Error; err != nil {
			t.Fatal(err)
		}
	}
	historical := seedFaceCluster(t, models.FaceClusterStatusIgnored, nil, faceUnitVector(1))
	seedFaceObservation(t, historical.ID, models.FaceMediaKindImage, 20, "i00000000020", models.FaceAppendStatusNone, 0.9)
	seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(2))

	page, err := service.ListIgnoredFaceClusters(ctx, 0, 1)
	if err != nil {
		t.Fatalf("列出已忽略失败: %v", err)
	}
	if len(page.Clusters) != 1 || page.Clusters[0].Cluster.ID != historical.ID || page.NextCursor != historical.ID {
		t.Fatalf("第一页应是 id 较大的历史行且有下一页: %+v", page)
	}
	if page.Clusters[0].AbsorbedSinceIgnored != 0 || page.Clusters[0].IgnoredAt != nil {
		t.Fatalf("历史行应显示 0: %+v", page.Clusters[0])
	}
	page, err = service.ListIgnoredFaceClusters(ctx, page.NextCursor, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Clusters) != 1 || page.Clusters[0].Cluster.ID != cluster.ID || page.NextCursor != 0 {
		t.Fatalf("第二页: %+v", page)
	}
	if got := page.Clusters[0].AbsorbedSinceIgnored; got != 2 {
		t.Fatalf("忽略后并入 2 条，实际 %d", got)
	}
	if page.Clusters[0].Cluster.ObservationCount != 3 {
		t.Fatalf("卡片观测数应现算为 3: %d", page.Clusters[0].Cluster.ObservationCount)
	}
}

// 解除关联只删「未被该人物其他 named 簇覆盖」的关系；预览列出全部并标出被覆盖的。
func TestMETA04UnlinkRemovesOnlyRelationsNotCoveredByOtherClusters(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	v1, v2, v3 := seedFaceTestVideo(t, "v1.mp4"), seedFaceTestVideo(t, "v2.mp4"), seedFaceTestVideo(t, "v3.mp4")
	person := seedFaceTestPerson(t, "周迅", false)
	clusterA := namedFaceClusterOn(t, service, person.ID, []models.Video{v1, v2}, "ua0000000")
	namedFaceClusterOn(t, service, person.ID, []models.Video{v2, v3}, "ub0000000")

	preview, err := service.PreviewFaceClusterUnlink(ctx, clusterA.ID)
	if err != nil {
		t.Fatalf("预览失败: %v", err)
	}
	if len(preview) != 2 || preview[0].MediaID != v1.ID || preview[0].Name != "v1.mp4" || preview[0].CoveredByOther ||
		preview[1].MediaID != v2.ID || !preview[1].CoveredByOther {
		t.Fatalf("预览应列出两件媒体且 v2 被其他簇覆盖: %+v", preview)
	}

	view, err := service.UnlinkFaceCluster(ctx, clusterA.ID, true)
	if err != nil {
		t.Fatalf("解除失败: %v", err)
	}
	if view.Status != models.FaceClusterStatusUnnamed || view.PersonID != 0 {
		t.Fatalf("解除后应为未命名: %+v", view)
	}
	if got := faceClusterByID(t, clusterA.ID); got.PersonID != nil {
		t.Fatalf("person_id 应置空: %+v", got)
	}
	if videoRelationCount(t, v1.ID, person.ID) != 0 {
		t.Fatal("v1 只由该簇覆盖，关系应删除")
	}
	if videoRelationCount(t, v2.ID, person.ID) != 1 || videoRelationCount(t, v3.ID, person.ID) != 1 {
		t.Fatal("v2 仍被其他簇覆盖、v3 不属于该簇，关系都应保留")
	}
	if _, err := service.UnlinkFaceCluster(ctx, clusterA.ID, true); !errors.Is(err, ErrFaceClusterNotNamed) {
		t.Fatalf("重复解除应报 cluster_not_named: %v", err)
	}
	if _, err := service.PreviewFaceClusterUnlink(ctx, clusterA.ID); !errors.Is(err, ErrFaceClusterNotNamed) {
		t.Fatalf("未命名簇不能预览: %v", err)
	}
}

// removeRelations=false 时只解除簇，关系一行不动；追加候选回到 none。
func TestMETA04UnlinkWithoutRemovingRelationsKeepsThem(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	v1, v2 := seedFaceTestVideo(t, "v1.mp4"), seedFaceTestVideo(t, "v2.mp4")
	person := seedFaceTestPerson(t, "周迅", false)
	cluster := namedFaceClusterOn(t, service, person.ID, []models.Video{v1}, "uk0000000")
	pending := seedFaceObservation(t, cluster.ID, models.FaceMediaKindVideo, v2.ID, "uk0000009", models.FaceAppendStatusPending, 0.5)

	if _, err := service.UnlinkFaceCluster(ctx, cluster.ID, false); err != nil {
		t.Fatalf("解除失败: %v", err)
	}
	if videoRelationCount(t, v1.ID, person.ID) != 1 {
		t.Fatal("不删关系时 v1 应保留")
	}
	if got := faceObservationByID(t, pending.ID).AppendStatus; got != models.FaceAppendStatusNone {
		t.Fatalf("解除后 pending 应回到 none: %q", got)
	}
}

// 并发下两次解除只有一次生效，另一次 cluster_not_named，关系只删一次。
func TestMETA04ConcurrentUnlinkTakesEffectOnce(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	v1 := seedFaceTestVideo(t, "v1.mp4")
	person := seedFaceTestPerson(t, "周迅", false)
	cluster := namedFaceClusterOn(t, service, person.ID, []models.Video{v1}, "uc0000000")

	results := make([]error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	for i := range results {
		go func(index int) {
			defer wait.Done()
			_, results[index] = service.UnlinkFaceCluster(ctx, cluster.ID, true)
		}(i)
	}
	wait.Wait()
	succeeded, lost := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrFaceClusterNotNamed):
			lost++
		default:
			t.Fatalf("并发解除只该成功或 cluster_not_named: %v", err)
		}
	}
	if succeeded != 1 || lost != 1 {
		t.Fatalf("应一次成功一次落败: 成功 %d 落败 %d", succeeded, lost)
	}
	if videoRelationCount(t, v1.ID, person.ID) != 0 {
		t.Fatal("关系应已删除")
	}
}

// 改派：关系去重迁移到目标人物；仍被原人物其他 named 簇覆盖的媒体在原人物侧保留。
func TestMETA04ReassignMovesRelationsWithDedupe(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	v1, v2, v3 := seedFaceTestVideo(t, "v1.mp4"), seedFaceTestVideo(t, "v2.mp4"), seedFaceTestVideo(t, "v3.mp4")
	source := seedFaceTestPerson(t, "周迅", false)
	target := seedFaceTestPerson(t, "汤唯", false)
	clusterA := namedFaceClusterOn(t, service, source.ID, []models.Video{v1, v2}, "ra0000000")
	namedFaceClusterOn(t, service, source.ID, []models.Video{v2, v3}, "rb0000000")
	// 目标人物已经在 v1 上有关系：迁移必须去重，不能报主键冲突。
	if err := database.DB.Create(&models.VideoPerson{VideoID: v1.ID, PersonID: target.ID}).Error; err != nil {
		t.Fatal(err)
	}

	view, err := service.ReassignFaceCluster(ctx, clusterA.ID, target.ID, true)
	if err != nil {
		t.Fatalf("改派失败: %v", err)
	}
	if view.PersonID != target.ID || view.PersonName != "汤唯" || view.Status != models.FaceClusterStatusNamed {
		t.Fatalf("簇应指向目标人物: %+v", view)
	}
	if videoRelationCount(t, v1.ID, target.ID) != 1 || videoRelationCount(t, v2.ID, target.ID) != 1 {
		t.Fatal("目标人物应有 v1、v2 各一条关系")
	}
	if videoRelationCount(t, v3.ID, target.ID) != 0 {
		t.Fatal("v3 不属于该簇，不能迁到目标人物")
	}
	if videoRelationCount(t, v1.ID, source.ID) != 0 {
		t.Fatal("v1 只由被改派的簇覆盖，原人物侧应删除")
	}
	if videoRelationCount(t, v2.ID, source.ID) != 1 || videoRelationCount(t, v3.ID, source.ID) != 1 {
		t.Fatal("v2 仍被原人物另一个簇覆盖、v3 不属于该簇，原人物侧应保留")
	}
}

// moveRelations=false 只改簇指向；目标不存在报 person_not_found；目标就是当前人物为空操作；未命名簇报 cluster_not_named。
func TestMETA04ReassignWithoutMovingRelationsAndGuards(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	v1 := seedFaceTestVideo(t, "v1.mp4")
	source := seedFaceTestPerson(t, "周迅", false)
	target := seedFaceTestPerson(t, "汤唯", false)
	cluster := namedFaceClusterOn(t, service, source.ID, []models.Video{v1}, "rg0000000")

	if _, err := service.ReassignFaceCluster(ctx, cluster.ID, 9999, true); !errors.Is(err, ErrFacePersonNotFound) {
		t.Fatalf("目标不存在: %v", err)
	}
	if _, err := service.ReassignFaceCluster(ctx, cluster.ID, source.ID, true); err != nil {
		t.Fatalf("目标就是当前人物应为空操作: %v", err)
	}
	if videoRelationCount(t, v1.ID, source.ID) != 1 {
		t.Fatal("空操作不能动关系")
	}
	if _, err := service.ReassignFaceCluster(ctx, cluster.ID, target.ID, false); err != nil {
		t.Fatalf("改派失败: %v", err)
	}
	if videoRelationCount(t, v1.ID, source.ID) != 1 || videoRelationCount(t, v1.ID, target.ID) != 0 {
		t.Fatal("moveRelations=false 不能动关系")
	}
	unnamed := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(1))
	if _, err := service.ReassignFaceCluster(ctx, unnamed.ID, target.ID, true); !errors.Is(err, ErrFaceClusterNotNamed) {
		t.Fatalf("未命名簇不能改派: %v", err)
	}
}

// 并发下同一次改派只迁移一次：关系不丢不重，簇最终指向目标。
func TestMETA04ConcurrentReassignTakesEffectOnce(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	v1 := seedFaceTestVideo(t, "v1.mp4")
	source := seedFaceTestPerson(t, "周迅", false)
	target := seedFaceTestPerson(t, "汤唯", false)
	cluster := namedFaceClusterOn(t, service, source.ID, []models.Video{v1}, "rc0000000")

	results := make([]error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	for i := range results {
		go func(index int) {
			defer wait.Done()
			_, results[index] = service.ReassignFaceCluster(ctx, cluster.ID, target.ID, true)
		}(i)
	}
	wait.Wait()
	for _, err := range results {
		if err != nil && !errors.Is(err, ErrFaceClusterConflict) {
			t.Fatalf("并发改派只该成功或 cluster_conflict: %v", err)
		}
	}
	if got := faceClusterByID(t, cluster.ID); got.PersonID == nil || *got.PersonID != target.ID {
		t.Fatalf("簇应指向目标: %+v", got)
	}
	if videoRelationCount(t, v1.ID, target.ID) != 1 || videoRelationCount(t, v1.ID, source.ID) != 0 {
		t.Fatal("关系应恰好迁到目标一次")
	}
}

// 并发下两次恢复只有一次生效，另一次幂等返回。
func TestMETA04ConcurrentRestoreTakesEffectOnce(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, 1, "rs0000000001", models.FaceAppendStatusNone, 0.9)
	if err := service.IgnoreFaceCluster(ctx, cluster.ID); err != nil {
		t.Fatal(err)
	}
	results := make([]error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	for i := range results {
		go func(index int) {
			defer wait.Done()
			results[index] = service.RestoreFaceCluster(ctx, cluster.ID)
		}(i)
	}
	wait.Wait()
	for _, err := range results {
		if err != nil {
			t.Fatalf("并发恢复应都成功（一次生效、一次幂等）: %v", err)
		}
	}
	if got := faceClusterByID(t, cluster.ID); got.Status != models.FaceClusterStatusUnnamed || got.IgnoredAt != nil {
		t.Fatalf("恢复后状态: %+v", got)
	}
}

// 逐条确认只写所选观测涉及的媒体，其余保持 pending；空数组仍是全部。
func TestMETA04ConfirmAppendObservationsWritesOnlySelected(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	v1, v2, v3 := seedFaceTestVideo(t, "v1.mp4"), seedFaceTestVideo(t, "v2.mp4"), seedFaceTestVideo(t, "v3.mp4")
	person := seedFaceTestPerson(t, "周迅", false)
	cluster := seedFaceCluster(t, models.FaceClusterStatusNamed, &person.ID, faceUnitVector(0))
	o1 := seedFaceObservation(t, cluster.ID, models.FaceMediaKindVideo, v1.ID, "cf0000000001", models.FaceAppendStatusPending, 0.9)
	o2 := seedFaceObservation(t, cluster.ID, models.FaceMediaKindVideo, v2.ID, "cf0000000002", models.FaceAppendStatusPending, 0.9)
	o3 := seedFaceObservation(t, cluster.ID, models.FaceMediaKindVideo, v3.ID, "cf0000000003", models.FaceAppendStatusPending, 0.9)

	if err := service.ConfirmFaceClusterAppendObservations(ctx, cluster.ID, []uint{o1.ID, 99999}); err != nil {
		t.Fatalf("逐条确认失败: %v", err)
	}
	if videoRelationCount(t, v1.ID, person.ID) != 1 || videoRelationCount(t, v2.ID, person.ID) != 0 || videoRelationCount(t, v3.ID, person.ID) != 0 {
		t.Fatal("只该写所选观测 o1 涉及的媒体")
	}
	if faceObservationByID(t, o1.ID).AppendStatus != models.FaceAppendStatusConfirmed ||
		faceObservationByID(t, o2.ID).AppendStatus != models.FaceAppendStatusPending ||
		faceObservationByID(t, o3.ID).AppendStatus != models.FaceAppendStatusPending {
		t.Fatal("只有 o1 转 confirmed，其余保持 pending")
	}
	if err := service.ConfirmFaceClusterAppendObservations(ctx, cluster.ID, []uint{o2.ID}); err != nil {
		t.Fatal(err)
	}
	if videoRelationCount(t, v2.ID, person.ID) != 1 || videoRelationCount(t, v3.ID, person.ID) != 0 {
		t.Fatal("第二次只确认 o2")
	}
	// 空数组表示全部剩余 pending（兼容旧行为）。
	if err := service.ConfirmFaceClusterAppendObservations(ctx, cluster.ID, nil); err != nil {
		t.Fatal(err)
	}
	if videoRelationCount(t, v3.ID, person.ID) != 1 || countFaceRows(t, &models.FaceObservation{}, "append_status = ?", models.FaceAppendStatusPending) != 0 {
		t.Fatal("空数组应确认剩余全部")
	}
}

// 已命名簇可以移除来源，且不动人物关系。
func TestMETA04RemoveObservationFromNamedClusterKeepsRelations(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	v1, v2 := seedFaceTestVideo(t, "v1.mp4"), seedFaceTestVideo(t, "v2.mp4")
	person := seedFaceTestPerson(t, "周迅", false)
	cluster := namedFaceClusterOn(t, service, person.ID, []models.Video{v1, v2}, "rm0000000")
	var observation models.FaceObservation
	if err := database.DB.Where("cluster_id = ? AND media_id = ?", cluster.ID, v1.ID).First(&observation).Error; err != nil {
		t.Fatal(err)
	}

	removed, err := service.RemoveFaceClusterObservation(ctx, cluster.ID, observation.ID)
	if err != nil || removed {
		t.Fatalf("已命名簇应允许移除来源且簇非空: removed=%v err=%v", removed, err)
	}
	if got := faceObservationByID(t, observation.ID); got.ClusterID != nil {
		t.Fatalf("观测应脱离簇: %+v", got)
	}
	if videoRelationCount(t, v1.ID, person.ID) != 1 || videoRelationCount(t, v2.ID, person.ID) != 1 {
		t.Fatal("移除来源不该动人物关系")
	}
	if got := faceClusterByID(t, cluster.ID); got.Status != models.FaceClusterStatusNamed {
		t.Fatalf("簇应保持 named: %+v", got)
	}
	ignored := seedFaceCluster(t, models.FaceClusterStatusIgnored, nil, faceUnitVector(1))
	other := seedFaceObservation(t, ignored.ID, models.FaceMediaKindVideo, v1.ID, "rm0000000009", models.FaceAppendStatusNone, 0.9)
	// 已忽略簇仍不允许，报 cluster_ignored（META-04 M-8）而不是 cluster_not_unnamed。
	if _, err := service.RemoveFaceClusterObservation(ctx, ignored.ID, other.ID); !errors.Is(err, ErrFaceClusterIgnored) {
		t.Fatalf("已忽略簇仍不允许: %v", err)
	}
}

// 键集分页：观测数相同的多个簇按 id 倒序，游标不重不漏；过滤条件生效。
func TestMETA11FaceClusterPageCursorStableWithTiedCounts(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	// 观测数：3 个簇各 2 条、3 个簇各 1 条、1 个簇 3 条、1 个空簇（0 条）。
	counts := []int{2, 1, 3, 2, 1, 2, 1, 0}
	expected := make([]uint, 0, len(counts))
	for i, count := range counts {
		cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(i%8))
		for j := 0; j < count; j++ {
			seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, uint(100*i+j+1), fmt.Sprintf("pg%04d%04d", i, j), models.FaceAppendStatusNone, 0.9)
		}
		expected = append(expected, cluster.ID)
	}
	ignored := seedFaceCluster(t, models.FaceClusterStatusIgnored, nil, faceUnitVector(1))
	seedFaceObservation(t, ignored.ID, models.FaceMediaKindImage, 9000, "pg99990000", models.FaceAppendStatusNone, 0.9)

	// 期望顺序：(观测数 DESC, id DESC)。
	want := append([]uint(nil), expected...)
	countByID := map[uint]int{}
	for i, id := range expected {
		countByID[id] = counts[i]
	}
	sort.Slice(want, func(i, j int) bool {
		if countByID[want[i]] != countByID[want[j]] {
			return countByID[want[i]] > countByID[want[j]]
		}
		return want[i] > want[j]
	})
	if len(want) != len(expected) {
		t.Fatalf("基线簇数不符: %d", len(want))
	}

	for _, pageSize := range []int{1, 2, 3, 100} {
		var got []uint
		cursor := FaceClusterCursorKey{}
		for guard := 0; guard < 20; guard++ {
			page, err := service.ListFaceClusterPage(ctx, FaceClusterFilter{Status: models.FaceClusterStatusUnnamed}, cursor, pageSize)
			if err != nil {
				t.Fatalf("分页失败: %v", err)
			}
			for _, view := range page.Clusters {
				got = append(got, view.ID)
			}
			if !page.HasMore {
				break
			}
			if page.Next.ID == 0 {
				t.Fatal("有下一页时游标不能为零值")
			}
			cursor = page.Next
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("页大小 %d 下顺序/完整性不符\n got %v\nwant %v", pageSize, got, want)
		}
	}

	// 翻页途中新增一个更大 id、同观测数的簇：已翻过的部分不重复，也不影响后续页。
	first, err := service.ListFaceClusterPage(ctx, FaceClusterFilter{Status: models.FaceClusterStatusUnnamed}, FaceClusterCursorKey{}, 2)
	if err != nil || len(first.Clusters) != 2 {
		t.Fatalf("首页: %+v err=%v", first, err)
	}
	late := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(3))
	seedFaceObservation(t, late.ID, models.FaceMediaKindImage, 8000, "pg88880000", models.FaceAppendStatusNone, 0.9)
	seedFaceObservation(t, late.ID, models.FaceMediaKindImage, 8001, "pg88880001", models.FaceAppendStatusNone, 0.9)
	rest, err := service.ListFaceClusterPage(ctx, FaceClusterFilter{Status: models.FaceClusterStatusUnnamed}, first.Next, 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uint]bool{first.Clusters[0].ID: true, first.Clusters[1].ID: true}
	for _, view := range rest.Clusters {
		if seen[view.ID] {
			t.Fatalf("翻页途中出现重复簇 %d", view.ID)
		}
	}
	for _, view := range rest.Clusters {
		if view.ID == late.ID {
			t.Fatal("新增簇排在游标之前，不该出现在后续页")
		}
	}
	if len(rest.Clusters) != len(want)-2 {
		t.Fatalf("后续页应恰好是原来剩余的簇: %d", len(rest.Clusters))
	}

	byStatus, err := service.ListFaceClusterPage(ctx, FaceClusterFilter{Status: models.FaceClusterStatusIgnored}, FaceClusterCursorKey{}, 10)
	if err != nil || len(byStatus.Clusters) != 1 || byStatus.Clusters[0].ID != ignored.ID || byStatus.HasMore {
		t.Fatalf("状态过滤: %+v err=%v", byStatus, err)
	}
	empty, err := service.ListFaceClusterPage(ctx, FaceClusterFilter{Status: models.FaceClusterStatusNamed}, FaceClusterCursorKey{}, 10)
	if err != nil || len(empty.Clusters) != 0 || empty.HasMore {
		t.Fatalf("空结果: %+v err=%v", empty, err)
	}
}

// ===== 人脸链路只删自己写入的关系（META-04 I-1）与复审 M 项 =====

func faceWriteCount(t *testing.T, personID, clusterID uint) int64 {
	t.Helper()
	return countFaceRows(t, &models.FaceRelationWrite{}, "person_id = ? AND cluster_id = ?", personID, clusterID)
}

func previewByMedia(views []FaceUnlinkMediaView) map[uint]FaceUnlinkMediaView {
	byID := make(map[uint]FaceUnlinkMediaView, len(views))
	for _, view := range views {
		byID[view.MediaID] = view
	}
	return byID
}

// NFO / 手动等来源先有的关系没有写入记录：关联时不记账，解除关联（删关系）后原样保留；
// 人脸链路新写的那条被删除。预览逐项标出来源与关系是否存在（M-7）。
func TestMETA04UnlinkKeepsRelationsNotWrittenByFaceChain(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	nfo, fresh, gone := seedFaceTestVideo(t, "nfo.mp4"), seedFaceTestVideo(t, "fresh.mp4"), seedFaceTestVideo(t, "gone.mp4")
	person := seedFaceTestPerson(t, "周迅", false)
	if err := database.DB.Create(&models.VideoPerson{VideoID: nfo.ID, PersonID: person.ID}).Error; err != nil {
		t.Fatal(err)
	}
	cluster := namedFaceClusterOn(t, service, person.ID, []models.Video{nfo, fresh, gone}, "wn0000000")
	if got := faceWriteCount(t, person.ID, cluster.ID); got != 2 {
		t.Fatalf("只有本次新插入的两条关系记账，NFO 那条不记: %d", got)
	}
	// 用户在别处手动删掉了 gone 的关系：预览里它 has_relation=false。
	if err := database.DB.Where("video_id = ? AND person_id = ?", gone.ID, person.ID).Delete(&models.VideoPerson{}).Error; err != nil {
		t.Fatal(err)
	}

	preview, err := service.PreviewFaceClusterUnlink(ctx, cluster.ID)
	if err != nil {
		t.Fatalf("预览失败: %v", err)
	}
	byID := previewByMedia(preview)
	if v := byID[nfo.ID]; !v.HasRelation || v.Source != FaceRelationSourceUnknown || v.CoveredByOther {
		t.Fatalf("NFO 关系应标 unknown 且存在: %+v", v)
	}
	if v := byID[fresh.ID]; !v.HasRelation || v.Source != FaceRelationSourceFace {
		t.Fatalf("人脸链路写的关系应标 face: %+v", v)
	}
	if v := byID[gone.ID]; v.HasRelation || v.Source != FaceRelationSourceUnknown {
		t.Fatalf("已不存在的关系应标 has_relation=false: %+v", v)
	}

	if _, err := service.UnlinkFaceCluster(ctx, cluster.ID, true); err != nil {
		t.Fatalf("解除失败: %v", err)
	}
	if videoRelationCount(t, nfo.ID, person.ID) != 1 {
		t.Fatal("NFO 已有的关系在解除关联后必须保留")
	}
	if videoRelationCount(t, fresh.ID, person.ID) != 0 {
		t.Fatal("人脸链路写入的关系应随解除关联删除")
	}
	if got := faceWriteCount(t, person.ID, cluster.ID); got != 0 {
		t.Fatalf("解除后本簇在原人物名下的写入记录应清空: %d", got)
	}
}

// 改派迁移：人脸链路写的关系从原人物移走，NFO 已有的关系留在原人物；目标人物侧按新插入记账。
func TestMETA04ReassignMovesOnlyFaceWrittenRelations(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	nfo, fresh := seedFaceTestVideo(t, "nfo.mp4"), seedFaceTestVideo(t, "fresh.mp4")
	source := seedFaceTestPerson(t, "周迅", false)
	target := seedFaceTestPerson(t, "汤唯", false)
	if err := database.DB.Create(&models.VideoPerson{VideoID: nfo.ID, PersonID: source.ID}).Error; err != nil {
		t.Fatal(err)
	}
	cluster := namedFaceClusterOn(t, service, source.ID, []models.Video{nfo, fresh}, "wr0000000")

	if _, err := service.ReassignFaceCluster(ctx, cluster.ID, target.ID, true); err != nil {
		t.Fatalf("改派失败: %v", err)
	}
	if videoRelationCount(t, nfo.ID, source.ID) != 1 {
		t.Fatal("NFO 已有的关系在改派后必须留在原人物")
	}
	if videoRelationCount(t, fresh.ID, source.ID) != 0 {
		t.Fatal("人脸链路写入的关系应从原人物迁走")
	}
	if videoRelationCount(t, nfo.ID, target.ID) != 1 || videoRelationCount(t, fresh.ID, target.ID) != 1 {
		t.Fatal("目标人物应得到簇内全部已确认媒体的关系")
	}
	if got := faceWriteCount(t, source.ID, cluster.ID); got != 0 {
		t.Fatalf("原人物名下本簇的写入记录应清空: %d", got)
	}
	if got := faceWriteCount(t, target.ID, cluster.ID); got != 2 {
		t.Fatalf("目标人物侧两条都是本次新插入，应记账: %d", got)
	}
	// 再改派回去：目标侧的两条都是本簇写的，全部迁走；原人物侧 NFO 那条仍在，fresh 重新写回。
	if _, err := service.ReassignFaceCluster(ctx, cluster.ID, source.ID, true); err != nil {
		t.Fatalf("改派回原人物失败: %v", err)
	}
	if videoRelationCount(t, nfo.ID, target.ID) != 0 || videoRelationCount(t, fresh.ID, target.ID) != 0 {
		t.Fatal("目标人物侧由本簇写入的关系应随改派迁走")
	}
	if videoRelationCount(t, nfo.ID, source.ID) != 1 || videoRelationCount(t, fresh.ID, source.ID) != 1 {
		t.Fatal("迁回后原人物应重新拥有两条关系")
	}
}

// 多簇覆盖：两个簇都确认了同一件媒体，关系由两者共同持有；解除其中一个时保留，
// 两个都解除后才删除。
func TestMETA04UnlinkKeepsRelationWhileAnotherNamedClusterHoldsIt(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	shared := seedFaceTestVideo(t, "shared.mp4")
	person := seedFaceTestPerson(t, "周迅", false)
	clusterA := namedFaceClusterOn(t, service, person.ID, []models.Video{shared}, "wa0000000")
	clusterB := namedFaceClusterOn(t, service, person.ID, []models.Video{shared}, "wb0000000")
	if faceWriteCount(t, person.ID, clusterA.ID) != 1 || faceWriteCount(t, person.ID, clusterB.ID) != 1 {
		t.Fatal("人脸链路写的关系再被另一个簇确认时，两个簇共同持有")
	}

	if _, err := service.UnlinkFaceCluster(ctx, clusterA.ID, true); err != nil {
		t.Fatal(err)
	}
	if videoRelationCount(t, shared.ID, person.ID) != 1 {
		t.Fatal("仍被另一个已命名簇持有的关系必须保留")
	}
	preview, err := service.PreviewFaceClusterUnlink(ctx, clusterB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v := previewByMedia(preview)[shared.ID]; v.Source != FaceRelationSourceFace || v.CoveredByOther || !v.HasRelation {
		t.Fatalf("剩下的簇独自持有这条关系: %+v", v)
	}
	if _, err := service.UnlinkFaceCluster(ctx, clusterB.ID, true); err != nil {
		t.Fatal(err)
	}
	if videoRelationCount(t, shared.ID, person.ID) != 0 {
		t.Fatal("最后一个持有者解除后关系应删除")
	}
}

// 关系被删后又由别的来源重建：旧的写入记录不再作数（新关系晚于记录），解除时保留。
func TestMETA04UnlinkKeepsRelationRecreatedByAnotherSource(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	video := seedFaceTestVideo(t, "v.mp4")
	person := seedFaceTestPerson(t, "周迅", false)
	cluster := namedFaceClusterOn(t, service, person.ID, []models.Video{video}, "wx0000000")
	if err := database.DB.Where("video_id = ? AND person_id = ?", video.ID, person.ID).Delete(&models.VideoPerson{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.VideoPerson{VideoID: video.ID, PersonID: person.ID, CreatedAt: time.Now().Add(time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewFaceClusterUnlink(ctx, cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v := previewByMedia(preview)[video.ID]; v.Source != FaceRelationSourceUnknown || !v.HasRelation {
		t.Fatalf("重建的关系来源不明: %+v", v)
	}
	if _, err := service.UnlinkFaceCluster(ctx, cluster.ID, true); err != nil {
		t.Fatal(err)
	}
	if videoRelationCount(t, video.ID, person.ID) != 1 {
		t.Fatal("别的来源重建的关系不能被人脸链路删掉")
	}
}

// 解除关联但保留关系：记录交还给用户（清空），之后再关联、再解除（删关系）也不会删它们；
// 确认追加新写出的关系照常记账、照常可删。
func TestMETA04KeptRelationsBecomeUserOwnedAfterUnlink(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	kept, appended := seedFaceTestVideo(t, "kept.mp4"), seedFaceTestVideo(t, "appended.mp4")
	person := seedFaceTestPerson(t, "周迅", false)
	cluster := namedFaceClusterOn(t, service, person.ID, []models.Video{kept}, "wk0000000")
	if _, err := service.UnlinkFaceCluster(ctx, cluster.ID, false); err != nil {
		t.Fatal(err)
	}
	if faceWriteCount(t, person.ID, cluster.ID) != 0 || videoRelationCount(t, kept.ID, person.ID) != 1 {
		t.Fatal("保留关系的解除：关系留着，记录清空")
	}
	if _, err := service.LinkFaceCluster(ctx, cluster.ID, person.ID); err != nil {
		t.Fatal(err)
	}
	pending := seedFaceObservation(t, cluster.ID, models.FaceMediaKindVideo, appended.ID, "wk0000009", models.FaceAppendStatusPending, 0.5)
	if err := service.ConfirmFaceClusterAppendObservations(ctx, cluster.ID, []uint{pending.ID}); err != nil {
		t.Fatal(err)
	}
	if faceWriteCount(t, person.ID, cluster.ID) != 1 {
		t.Fatal("只有确认追加新写出的那条记账")
	}
	if _, err := service.UnlinkFaceCluster(ctx, cluster.ID, true); err != nil {
		t.Fatal(err)
	}
	if videoRelationCount(t, kept.ID, person.ID) != 1 {
		t.Fatal("用户选择保留过的关系之后不能被人脸链路删除")
	}
	if videoRelationCount(t, appended.ID, person.ID) != 0 {
		t.Fatal("确认追加写出的关系应随解除删除")
	}
}

// 人物删除后，写入记录在下一次审阅对账时清掉（表上没有外键）。
func TestMETA04PersonDeletionPrunesFaceRelationWrites(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	video := seedFaceTestVideo(t, "v.mp4")
	person := seedFaceTestPerson(t, "周迅", false)
	other := seedFaceTestPerson(t, "汤唯", false)
	namedFaceClusterOn(t, service, person.ID, []models.Video{video}, "wp0000000")
	keep := namedFaceClusterOn(t, service, other.ID, []models.Video{video}, "wp0000001")
	if err := database.DB.Delete(&models.Person{}, person.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListFaceClusterPage(ctx, FaceClusterFilter{}, FaceClusterCursorKey{}, 10); err != nil {
		t.Fatal(err)
	}
	if got := countFaceRows(t, &models.FaceRelationWrite{}, "person_id = ?", person.ID); got != 0 {
		t.Fatalf("已删除人物的写入记录应清掉: %d", got)
	}
	if faceWriteCount(t, other.ID, keep.ID) != 1 {
		t.Fatal("其他人物的记录不受影响")
	}
}

// M-2：读快照与条件更新之间簇被别人改写。仍是 named、换了人物 → cluster_conflict（改派与解除都是）；
// 已不是 named → cluster_not_named。落败的事务整体回滚，关系与记录都不动。
func TestMETA04UnlinkAndReassignReportConflictWhenClusterChangesAfterSnapshot(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	video := seedFaceTestVideo(t, "v.mp4")
	source := seedFaceTestPerson(t, "周迅", false)
	target := seedFaceTestPerson(t, "汤唯", false)
	intruder := seedFaceTestPerson(t, "闯入者", false)
	cluster := namedFaceClusterOn(t, service, source.ID, []models.Video{video}, "wc0000000")
	t.Cleanup(func() { faceClusterAfterSnapshotHook = nil })

	rewrite := func(updates map[string]interface{}) {
		faceClusterAfterSnapshotHook = func(tx *gorm.DB, clusterID uint) {
			if err := tx.Model(&models.FaceCluster{}).Where("id = ?", clusterID).Updates(updates).Error; err != nil {
				t.Errorf("测试替身改写簇失败: %v", err)
			}
		}
	}
	rewrite(map[string]interface{}{"person_id": intruder.ID})
	if _, err := service.ReassignFaceCluster(ctx, cluster.ID, target.ID, true); !errors.Is(err, ErrFaceClusterConflict) {
		t.Fatalf("改派落败应报 cluster_conflict: %v", err)
	}
	if _, err := service.UnlinkFaceCluster(ctx, cluster.ID, true); !errors.Is(err, ErrFaceClusterConflict) {
		t.Fatalf("解除落败且簇仍为 named 应报 cluster_conflict: %v", err)
	}
	rewrite(map[string]interface{}{"status": models.FaceClusterStatusUnnamed, "person_id": nil})
	if _, err := service.UnlinkFaceCluster(ctx, cluster.ID, true); !errors.Is(err, ErrFaceClusterNotNamed) {
		t.Fatalf("簇已不是 named 应报 cluster_not_named: %v", err)
	}
	faceClusterAfterSnapshotHook = nil

	if got := faceClusterByID(t, cluster.ID); got.Status != models.FaceClusterStatusNamed || got.PersonID == nil || *got.PersonID != source.ID {
		t.Fatalf("落败的事务应整体回滚: %+v", got)
	}
	if videoRelationCount(t, video.ID, source.ID) != 1 || videoRelationCount(t, video.ID, target.ID) != 0 || faceWriteCount(t, source.ID, cluster.ID) != 1 {
		t.Fatal("落败时关系与写入记录都不动")
	}
}

// M-3：解除关联后再命名 / 关联，用户忽略过的追加保持 dismissed，不写关系。
func TestMETA04RelinkKeepsDismissedAppendsDismissed(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	confirmed, dismissed := seedFaceTestVideo(t, "confirmed.mp4"), seedFaceTestVideo(t, "dismissed.mp4")
	person := seedFaceTestPerson(t, "周迅", false)
	other := seedFaceTestPerson(t, "汤唯", false)
	cluster := namedFaceClusterOn(t, service, person.ID, []models.Video{confirmed}, "wd0000000")
	appended := seedFaceObservation(t, cluster.ID, models.FaceMediaKindVideo, dismissed.ID, "wd0000009", models.FaceAppendStatusPending, 0.5)
	if err := service.DismissFaceClusterAppend(ctx, cluster.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UnlinkFaceCluster(ctx, cluster.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.LinkFaceCluster(ctx, cluster.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if got := faceObservationByID(t, appended.ID).AppendStatus; got != models.FaceAppendStatusDismissed {
		t.Fatalf("忽略过的追加应保持 dismissed: %q", got)
	}
	if videoRelationCount(t, dismissed.ID, other.ID) != 0 {
		t.Fatal("忽略过的追加不能随再次关联写关系")
	}
	if videoRelationCount(t, confirmed.ID, other.ID) != 1 {
		t.Fatal("其余观测照常写关系")
	}
	if _, err := service.UnlinkFaceCluster(ctx, cluster.ID, false); err != nil {
		t.Fatal(err)
	}
	view, err := service.NameFaceCluster(ctx, cluster.ID, "新名字", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := faceObservationByID(t, appended.ID).AppendStatus; got != models.FaceAppendStatusDismissed {
		t.Fatalf("命名同样不动 dismissed: %q", got)
	}
	if videoRelationCount(t, dismissed.ID, view.PersonID) != 0 {
		t.Fatal("命名同样不为 dismissed 观测写关系")
	}
}

// M-1：命名与关联也持有 faceClusterAssignmentMu，与解除 / 改派 / 聚类串行。
func TestMETA04NameAndLinkHoldAssignmentMutex(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	video := seedFaceTestVideo(t, "v.mp4")
	person := seedFaceTestPerson(t, "周迅", false)
	linkCluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	seedFaceObservation(t, linkCluster.ID, models.FaceMediaKindVideo, video.ID, "wm0000001", models.FaceAppendStatusNone, 0.9)
	nameCluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(1))
	seedFaceObservation(t, nameCluster.ID, models.FaceMediaKindVideo, video.ID, "wm0000002", models.FaceAppendStatusNone, 0.9)

	actions := []struct {
		name string
		run  func() error
	}{
		{"link", func() error { _, err := service.LinkFaceCluster(ctx, linkCluster.ID, person.ID); return err }},
		{"name", func() error { _, err := service.NameFaceCluster(ctx, nameCluster.ID, "新人物", ""); return err }},
	}
	for _, action := range actions {
		faceClusterAssignmentMu.Lock()
		done := make(chan error, 1)
		go func(run func() error) { done <- run() }(action.run)
		select {
		case err := <-done:
			faceClusterAssignmentMu.Unlock()
			t.Fatalf("%s 没有等待 faceClusterAssignmentMu: %v", action.name, err)
		case <-time.After(150 * time.Millisecond):
		}
		faceClusterAssignmentMu.Unlock()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s 放锁后应完成: %v", action.name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s 放锁后仍未完成", action.name)
		}
	}
}

// M-4：分页按维护列 face_clusters.observation_count 排序（由 recomputeFaceClustersTx 维护），
// 游标就是列值；卡片上的观测数仍按观测行现算。
func TestMETA11FaceClusterPageOrdersByMaintainedObservationCount(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	service := newFaceReviewTestService()
	small := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	seedFaceObservation(t, small.ID, models.FaceMediaKindImage, 1, "oc00000001", models.FaceAppendStatusNone, 0.9)
	large := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(1))
	for i := 0; i < 3; i++ {
		seedFaceObservation(t, large.ID, models.FaceMediaKindImage, uint(10+i), fmt.Sprintf("oc0000001%d", i), models.FaceAppendStatusNone, 0.9)
	}
	// 移除来源走 recomputeFaceClustersTx：列随之从 3 变 2，排序跟着列走。
	var removed models.FaceObservation
	if err := database.DB.Where("cluster_id = ?", large.ID).Order("id ASC").First(&removed).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.RemoveFaceClusterObservation(ctx, large.ID, removed.ID); err != nil {
		t.Fatal(err)
	}
	if got := faceClusterByID(t, large.ID).ObservationCount; got != 2 {
		t.Fatalf("移除来源后维护列应重算为 2: %d", got)
	}
	first, err := service.ListFaceClusterPage(ctx, FaceClusterFilter{}, FaceClusterCursorKey{}, 1)
	if err != nil || len(first.Clusters) != 1 || first.Clusters[0].ID != large.ID || !first.HasMore {
		t.Fatalf("首页应是观测多的簇: %+v err=%v", first, err)
	}
	if first.Next != (FaceClusterCursorKey{Count: 2, ID: large.ID}) || first.Clusters[0].ObservationCount != 2 {
		t.Fatalf("游标取维护列的值: %+v", first)
	}
	rest, err := service.ListFaceClusterPage(ctx, FaceClusterFilter{}, first.Next, 10)
	if err != nil || len(rest.Clusters) != 1 || rest.Clusters[0].ID != small.ID || rest.HasMore {
		t.Fatalf("第二页: %+v err=%v", rest, err)
	}
}

// META-04：删除人物在同一事务里删掉它的写入记录（不等惰性清理）；清除人脸数据清空全部记录，
// 人物关系本身保留。否则簇 ID 复用后，新簇会把旧记录当成自己写的，解除关联时误删关系。
func TestMETA04PersonDeleteAndClearFaceDataDropFaceRelationWrites(t *testing.T) {
	setupFaceTestDB(t)
	service := newFaceReviewTestService()
	video := seedFaceTestVideo(t, "v.mp4")
	person := seedFaceTestPerson(t, "周迅", false)
	other := seedFaceTestPerson(t, "汤唯", false)
	namedFaceClusterOn(t, service, person.ID, []models.Video{video}, "wd0000000")
	keep := namedFaceClusterOn(t, service, other.ID, []models.Video{video}, "wd0000001")
	if countFaceRows(t, &models.FaceRelationWrite{}, "person_id = ?", person.ID) == 0 {
		t.Fatal("前置：人脸链路写入的关系应有记录")
	}
	if err := NewPersonService(t.TempDir()).DeletePerson(person.ID); err != nil {
		t.Fatal(err)
	}
	if got := countFaceRows(t, &models.FaceRelationWrite{}, "person_id = ?", person.ID); got != 0 {
		t.Fatalf("删除人物时应在同一事务里删掉写入记录: %d", got)
	}
	if faceWriteCount(t, other.ID, keep.ID) != 1 {
		t.Fatal("其他人物的记录不受影响")
	}

	analysis := newFaceAnalysisTestService(t, newFakeFaceWorker())
	if _, err := analysis.ClearFaceData(); err != nil {
		t.Fatal(err)
	}
	if got := countFaceRows(t, &models.FaceRelationWrite{}, ""); got != 0 {
		t.Fatalf("清除人脸数据应清空写入记录: %d", got)
	}
	if got := countFaceRows(t, &models.VideoPerson{}, "person_id = ?", other.ID); got != 1 {
		t.Fatalf("清除人脸数据不动人物关系: %d", got)
	}
}
