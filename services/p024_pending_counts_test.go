package services

import (
	"context"
	"reflect"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// META-08：待处理收件箱的人脸计数与簇列表同口径——未命名（含对账时会回到未命名的
// 「named 但 person_id 为空」）与待确认追加；已忽略的簇两项都不算。
func TestMETA08FacePendingReviewCountsMatchClusterTabs(t *testing.T) {
	setupFaceTestDB(t)
	person := seedFaceTestPerson(t, "周迅", false)
	video := seedFaceTestVideo(t, "v.mp4")

	seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, nil)
	seedFaceCluster(t, models.FaceClusterStatusNamed, nil, nil) // 人物已被删：对账后回到未命名
	ignored := seedFaceCluster(t, models.FaceClusterStatusIgnored, nil, nil)
	seedFaceObservation(t, ignored.ID, models.FaceMediaKindVideo, video.ID, "ig0", models.FaceAppendStatusPending, 0.9)

	withPending := seedFaceCluster(t, models.FaceClusterStatusNamed, &person.ID, nil)
	seedFaceObservation(t, withPending.ID, models.FaceMediaKindVideo, video.ID, "p0", models.FaceAppendStatusConfirmed, 0.9)
	seedFaceObservation(t, withPending.ID, models.FaceMediaKindVideo, video.ID, "p1", models.FaceAppendStatusPending, 0.9)
	settled := seedFaceCluster(t, models.FaceClusterStatusNamed, &person.ID, nil)
	seedFaceObservation(t, settled.ID, models.FaceMediaKindVideo, video.ID, "s0", models.FaceAppendStatusConfirmed, 0.9)
	seedFaceObservation(t, settled.ID, models.FaceMediaKindVideo, video.ID, "s1", models.FaceAppendStatusDismissed, 0.9)

	unnamed, appendPending, err := (&FaceReviewService{}).CountPendingReview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if unnamed != 2 || appendPending != 1 {
		t.Fatalf("未命名应为 2、待确认追加应为 1: unnamed=%d append=%d", unnamed, appendPending)
	}
}

// META-08：同源待确认数与审阅列表同口径，但不受列表 200 条上限截断；已审阅或一端已删除的不算。
func TestMETA08SameSourceUnconfirmedCountIsNotTruncated(t *testing.T) {
	setupVideoServiceTestDB(t)
	videos := make([]models.Video, 24)
	for index := range videos {
		videos[index] = models.Video{Name: "v.mp4", Path: "/lib/same-source/" + string(rune('a'+index)) + ".mp4", Directory: "/lib/same-source"}
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	created := 0
	for i := 0; i < len(videos) && created < 232; i++ {
		for j := i + 1; j < len(videos) && created < 232; j++ {
			relation := models.VideoSameSourceRelation{
				VideoAID: videos[i].ID, VideoBID: videos[j].ID,
				VideoAFingerprint: "a", VideoBFingerprint: "b",
				Status: models.VideoSameSourceStatusDetected, Confidence: models.AITagConfidenceHigh,
				DetectionVersion: sameSourceFingerprintVersion, IsUnread: true,
			}
			if created == 230 {
				relation.ReviewedAt = &now // 已审阅：不算
			}
			if err := database.DB.Create(&relation).Error; err != nil {
				t.Fatal(err)
			}
			created++
		}
	}
	// 最后一条挂在一个随后被删除的视频上：不算。
	if err := database.DB.Delete(&models.Video{}, videos[len(videos)-1].ID).Error; err != nil {
		t.Fatal(err)
	}
	service := NewAISameSourceService(nil)
	listed, err := service.ListRelations(models.VideoSameSourceStatusDetected, false)
	if err != nil {
		t.Fatal(err)
	}
	count, err := service.UnconfirmedCount()
	if err != nil {
		t.Fatal(err)
	}
	var expected int64
	if err := database.DB.Model(&models.VideoSameSourceRelation{}).
		Where("status = ? AND reviewed_at IS NULL AND video_a_id <> ? AND video_b_id <> ?",
			models.VideoSameSourceStatusDetected, videos[len(videos)-1].ID, videos[len(videos)-1].ID).
		Count(&expected).Error; err != nil {
		t.Fatal(err)
	}
	if len(listed) != sameSourceShortlistLimit || expected <= int64(sameSourceShortlistLimit) || count != expected {
		t.Fatalf("计数应不受列表上限截断: listed=%d count=%d expected=%d", len(listed), count, expected)
	}
}

// MEDIA-10：「翻译已有字幕」不进队列，退出保护靠这份只读清单判定。
func TestMEDIA10ActiveTranslationVideoIDsTracksRegistrations(t *testing.T) {
	service := NewSubtitleService(t.TempDir())
	if got := service.ActiveTranslationVideoIDs(); len(got) != 0 {
		t.Fatalf("没有翻译时应为空: %v", got)
	}
	_, releaseA := service.registerTranslationCancel(context.Background(), 5)
	_, releaseB := service.registerTranslationCancel(context.Background(), 5)
	_, releaseC := service.registerTranslationCancel(context.Background(), 3)
	if got := service.ActiveTranslationVideoIDs(); !reflect.DeepEqual(got, []uint{3, 5}) {
		t.Fatalf("应按升序列出正在翻译的视频: %v", got)
	}
	releaseA()
	releaseC()
	if got := service.ActiveTranslationVideoIDs(); !reflect.DeepEqual(got, []uint{5}) {
		t.Fatalf("同一视频还有一个翻译在跑: %v", got)
	}
	releaseB()
	if got := service.ActiveTranslationVideoIDs(); len(got) != 0 {
		t.Fatalf("全部结束后应为空: %v", got)
	}
}
