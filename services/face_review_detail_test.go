package services

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestRemoveFaceSourcePreservesMediaAndExcludesItFromNaming(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	video := seedFaceTestVideo(t, "keep.mp4")
	image := seedFaceTestImage(t, "keep.jpg")
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(1))
	keep := seedFaceObservation(t, cluster.ID, models.FaceMediaKindVideo, video.ID, "keep", models.FaceAppendStatusNone, .8)
	exclude := seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, image.ID, "exclude", models.FaceAppendStatusNone, .9)
	svc := newFaceReviewTestService()
	empty, err := svc.RemoveFaceClusterObservation(ctx, cluster.ID, exclude.ID)
	if err != nil || empty {
		t.Fatalf("remove: empty=%v err=%v", empty, err)
	}
	observation := faceObservationByID(t, exclude.ID)
	if observation.ClusterID != nil || observation.AppendStatus != models.FaceAppendStatusDismissed || observation.SourceFingerprint != exclude.SourceFingerprint {
		t.Fatalf("excluded observation must retain analysis identity: %+v", observation)
	}
	updated := faceClusterByID(t, cluster.ID)
	if updated.ObservationCount != 1 || updated.RepresentativeObservationID == nil || *updated.RepresentativeObservationID != keep.ID || !bytes.Equal(updated.Centroid, keep.Embedding) {
		t.Fatalf("cluster not recomputed: %+v", updated)
	}
	page, err := svc.GetFaceClusterObservations(ctx, cluster.ID, 0, 30)
	if err != nil || len(page.Observations) != 1 || page.Observations[0].ObservationID != keep.ID {
		t.Fatalf("excluded source still visible: %+v %v", page, err)
	}
	person := models.Person{DisplayName: "Kept face"}
	if err := database.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LinkFaceCluster(ctx, cluster.ID, person.ID); err != nil {
		t.Fatal(err)
	}
	var linked, excluded, videos, images int64
	database.DB.Model(&models.VideoPerson{}).Where("person_id = ? AND video_id = ?", person.ID, video.ID).Count(&linked)
	database.DB.Model(&models.ImagePerson{}).Where("person_id = ?", person.ID).Count(&excluded)
	database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Count(&videos)
	database.DB.Model(&models.Image{}).Where("id = ?", image.ID).Count(&images)
	if linked != 1 || excluded != 0 || videos != 1 || images != 1 {
		t.Fatalf("unexpected relations/media: %d %d %d %d", linked, excluded, videos, images)
	}
}

func TestRemoveFaceSourceLastObservationAndStaleAnalysisSnapshot(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	image := seedFaceTestImage(t, "keep.jpg")
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	observation := seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, image.ID, "last", models.FaceAppendStatusNone, .9)
	person := models.Person{DisplayName: "Candidate"}
	if err := database.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.FacePersonCandidate{ClusterID: cluster.ID, PersonID: person.ID, Similarity: .9}).Error; err != nil {
		t.Fatal(err)
	}
	clusters, err := loadFaceClusterCentroids(ctx)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := newFaceReviewTestService().RemoveFaceClusterObservation(ctx, cluster.ID, observation.ID)
	if err != nil || !empty {
		t.Fatalf("last source: %v %v", empty, err)
	}
	var count int64
	for _, model := range []any{&models.FaceCluster{}, &models.FacePersonCandidate{}} {
		if err := database.DB.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("remaining %T: %d %v", model, count, err)
		}
	}
	if faceObservationByID(t, observation.ID).ClusterID != nil {
		t.Fatal("source not detached")
	}
	// A detector already running must discard the deleted cluster from its cache.
	next := observation
	next.ID = 0
	next.ClusterID = nil
	next.BBoxHash = "next"
	if err := database.DB.Create(&next).Error; err != nil {
		t.Fatal(err)
	}
	created, err := (&FaceAnalysisService{}).clusterObservations(ctx, []models.FaceObservation{next}, &clusters)
	if err != nil || created != 1 {
		t.Fatalf("stale snapshot: %d %v", created, err)
	}
	if got := faceObservationByID(t, next.ID); got.ClusterID == nil || *got.ClusterID == cluster.ID {
		t.Fatal("new observation attached to removed cluster")
	}
}

func TestRemoveFaceSourceRejectsWrongClusterAndNamedCluster(t *testing.T) {
	setupFaceTestDB(t)
	ctx := context.Background()
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	other := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	observation := seedFaceObservation(t, other.ID, models.FaceMediaKindImage, 9999, "source", models.FaceAppendStatusNone, .9)
	svc := newFaceReviewTestService()
	if _, err := svc.RemoveFaceClusterObservation(ctx, cluster.ID, observation.ID); !errors.Is(err, ErrFaceObservationNotInCluster) {
		t.Fatalf("wrong cluster: %v", err)
	}
	if err := database.DB.Model(&other).Update("status", models.FaceClusterStatusNamed).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RemoveFaceClusterObservation(ctx, other.ID, observation.ID); !errors.Is(err, ErrFaceClusterNotUnnamed) {
		t.Fatalf("named cluster: %v", err)
	}
	if got := faceObservationByID(t, observation.ID); got.ClusterID == nil || *got.ClusterID != other.ID {
		t.Fatal("rejected operation changed source")
	}
	if _, err := svc.RemoveFaceClusterObservation(ctx, 9999, observation.ID); !errors.Is(err, ErrFaceClusterNotFound) {
		t.Fatalf("missing cluster: %v", err)
	}
}

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
