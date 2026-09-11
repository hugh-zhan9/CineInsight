package services

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"
)

func faceAvatarFixture(t *testing.T) (*FaceReviewService, models.FaceCluster, string) {
	t.Helper()
	dir := t.TempDir()
	analysis := NewFaceAnalysisService(dir, nil, nil)
	svc := NewFaceReviewService(dir, analysis)
	image := seedFaceTestImage(t, "avatar-source.jpg")
	cluster := seedFaceCluster(t, models.FaceClusterStatusUnnamed, nil, faceUnitVector(0))
	observation := seedFaceObservation(t, cluster.ID, models.FaceMediaKindImage, image.ID, "avatar", models.FaceAppendStatusNone, .9)
	path := filepath.Join(dir, "faces", "crop.jpg")
	writeFaceTestImage(t, path)
	if err := database.DB.Model(&observation).Update("crop_path", path).Error; err != nil {
		t.Fatal(err)
	}
	return svc, cluster, path
}

func TestNameFaceClusterOwnsAvatarAfterCropsRemoved(t *testing.T) {
	setupFaceTestDB(t)
	svc, cluster, crop := faceAvatarFixture(t)
	// Ensure the resolver uses the naming transaction instead of requiring a second connection.
	db, _ := database.DB.DB()
	db.SetMaxOpenConns(1)
	view, err := svc.NameFaceCluster(context.Background(), cluster.ID, "测试人物", "")
	if err != nil {
		t.Fatal(err)
	}
	var person models.Person
	if err := database.DB.First(&person, view.PersonID).Error; err != nil {
		t.Fatal(err)
	}
	if person.AvatarPath == "" {
		t.Fatal("avatar missing")
	}
	asset, err := svc.images.Resolve(person.AvatarPath)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(crop)
	if err := os.RemoveAll(filepath.Dir(crop)); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(asset.Path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("avatar did not survive crop deletion: %v", err)
	}
}

func TestNameFaceClusterAvatarFailureRollsBack(t *testing.T) {
	setupFaceTestDB(t)
	svc, cluster, _ := faceAvatarFixture(t)
	svc.resolveCrop = func(*gorm.DB, uint) (*FaceCropAsset, error) { return nil, errors.New("read failed") }
	if _, err := svc.NameFaceCluster(context.Background(), cluster.ID, "失败", ""); err == nil {
		t.Fatal("expected failure")
	}
	if c := countFaceRows(t, &models.Person{}, "1 = 1"); c != 0 {
		t.Fatalf("person persisted: %d", c)
	}
	if c := countFaceRows(t, &models.ImagePerson{}, "1 = 1"); c != 0 {
		t.Fatalf("relation persisted: %d", c)
	}
	if faceClusterByID(t, cluster.ID).Status != models.FaceClusterStatusUnnamed {
		t.Fatal("cluster claimed")
	}
}

func TestBackfillFaceAvatarPreservesCustomAndIsIdempotent(t *testing.T) {
	setupFaceTestDB(t)
	svc, cluster, _ := faceAvatarFixture(t)
	// Simulate the former naming code that did not save an avatar.
	view, err := newFaceReviewTestService().NameFaceCluster(context.Background(), cluster.ID, "旧人物", "")
	if err != nil {
		t.Fatal(err)
	}
	custom := seedFaceTestPerson(t, "自选头像", true)
	seedFaceCluster(t, models.FaceClusterStatusNamed, &custom.ID, nil)
	n, err := svc.BackfillNamedFaceAvatars(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("backfill: %d %v", n, err)
	}
	n, err = svc.BackfillNamedFaceAvatars(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("second backfill: %d %v", n, err)
	}
	var person models.Person
	if err := database.DB.First(&person, view.PersonID).Error; err != nil || person.AvatarPath == "" {
		t.Fatalf("avatar missing: %v", err)
	}
	var preserved models.Person
	if err := database.DB.First(&preserved, custom.ID).Error; err != nil || preserved.AvatarPath != custom.AvatarPath {
		t.Fatalf("custom avatar changed: %v", err)
	}
}

func TestNameFaceClusterRollsBackImportedAvatarWithRelations(t *testing.T) {
	setupFaceTestDB(t)
	svc, cluster, _ := faceAvatarFixture(t)
	if err := database.DB.Callback().Create().Before("gorm:create").Register("fail_face_relation", func(tx *gorm.DB) {
		if tx.Statement.Table == "image_people" {
			tx.AddError(errors.New("relation failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer database.DB.Callback().Create().Remove("fail_face_relation")
	if _, err := svc.NameFaceCluster(context.Background(), cluster.ID, "回滚", ""); err == nil {
		t.Fatal("expected failure")
	}
	if c := countFaceRows(t, &models.Person{}, "1 = 1"); c != 0 {
		t.Fatalf("person persisted: %d", c)
	}
	if err := filepath.WalkDir(svc.images.root, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			t.Errorf("unreferenced imported avatar left: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
