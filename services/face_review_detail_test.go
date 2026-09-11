package services

import (
	"context"
	"errors"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestFaceClusterSourceDetailsPageAndAvailability(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	video := seedFaceTestVideo(t, "video.mp4")
	image := seedFaceTestImage(t, "photo.jpg")
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	first := seedFaceObservation(t, cluster.ID, models.FaceMediaKindVideo, video.ID, "v", models.FaceAppendStatusNone, .9)
	if err := database.DB.Model(&first).Update("frame_ms", 0).Error; err != nil {
		t.Fatal(err)
	}
	seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, image.ID, "i", models.FaceAppendStatusNone, .9)
	seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, 999999, "missing", models.FaceAppendStatusNone, .9)
	other := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, nil)
	seedFaceObservation(t, other.ID, models.FaceMediaKindImage, image.ID, "other", models.FaceAppendStatusNone, .9)
	svc := newFaceReviewTestService()
	page, err := svc.GetFaceClusterObservations(ctx, cluster.ID, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Observations) != 1 || page.NextID != first.ID || page.Observations[0].Name != video.Name || page.Observations[0].FrameMS != 0 {
		t.Fatalf("bad first page: %+v", page)
	}
	page, err = svc.GetFaceClusterObservations(ctx, cluster.ID, page.NextID, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Observations) != 2 || page.NextID != 0 || page.Observations[0].Name != image.Name || page.Observations[0].FrameMS != -1 || page.Observations[1].Unavailable != "原记录已移除" {
		t.Fatalf("bad remaining page: %+v", page)
	}
	if err := database.DB.Model(&video).Update("is_stale", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&image).Error; err != nil {
		t.Fatal(err)
	}
	page, err = svc.GetFaceClusterObservations(ctx, cluster.ID, 0, 30)
	if err != nil {
		t.Fatal(err)
	}
	if page.Observations[0].Unavailable != "路径失效" || page.Observations[1].Unavailable != "已删除" {
		t.Fatalf("availability: %+v", page)
	}
	if _, err := svc.GetFaceClusterObservations(ctx, 999999, 0, 30); !errors.Is(err, ErrFaceClusterNotFound) {
		t.Fatalf("unknown cluster: %v", err)
	}
	for _, model := range []any{&models.Person{}, &models.ImagePerson{}, &models.VideoPerson{}} {
		var count int64
		if err := database.DB.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("read changed %T: %d %v", model, count, err)
		}
	}
}
