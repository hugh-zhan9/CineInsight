package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"video-master/database"
	"video-master/models"
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
	if err := database.DB.Model(&models.FaceCluster{}).Where("id = ?", clusterID).
		Update("representative_observation_id", observation.ID).Error; err != nil {
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
