// 外部测试包，与 schema_ext_test.go 同因：dbtest 依赖 database，内部测试再依赖
// dbtest 会形成导入环。这里验证的是 ApplySchema 之后 image_people 的真实形态。
package database_test

import (
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// image_people 与 video_people 对称（D-015）：复合主键、两侧级联、
// (person_id, image_id) 索引。两个后端都必须建得出来。
func TestImagePeopleTableMirrorsVideoPeopleOnBothBackends(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	// 幂等：老库升级会再跑一遍，补索引的语句不能第二次报错。
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重复建立 schema 应当幂等(%s): %v", dbtest.Backend(), err)
	}
	if !db.Migrator().HasTable(&models.ImagePerson{}) {
		t.Fatalf("image_people 表缺失(%s)", dbtest.Backend())
	}
	if !db.Migrator().HasIndex(&models.ImagePerson{}, "idx_image_people_person_image") {
		t.Fatalf("image_people 人物侧索引缺失(%s)", dbtest.Backend())
	}

	person := models.Person{DisplayName: "级联人物"}
	if err := db.Create(&person).Error; err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	image := models.Image{Name: "cascade.jpg", Path: "/tmp/cascade.jpg", Directory: "/tmp", Size: 1, Format: "jpg"}
	if err := db.Create(&image).Error; err != nil {
		t.Fatalf("创建图片失败: %v", err)
	}
	if err := db.Create(&models.ImagePerson{ImageID: image.ID, PersonID: person.ID}).Error; err != nil {
		t.Fatalf("建立图片人物关系失败: %v", err)
	}
	// 复合主键：同一对不得重复。
	if err := db.Create(&models.ImagePerson{ImageID: image.ID, PersonID: person.ID}).Error; err == nil {
		t.Fatalf("重复的图片人物关系应被主键拒绝(%s)", dbtest.Backend())
	}
	// 外键：不存在的图片不得建关系。
	if err := db.Create(&models.ImagePerson{ImageID: image.ID + 9999, PersonID: person.ID}).Error; err == nil {
		t.Fatalf("不存在的图片应被外键拒绝(%s)", dbtest.Backend())
	}

	// 软删除保留关系——图片进回收站不等于人物失去这张图片。
	if err := db.Delete(&models.Image{}, image.ID).Error; err != nil {
		t.Fatalf("软删除图片失败: %v", err)
	}
	var count int64
	if err := db.Model(&models.ImagePerson{}).Where("person_id = ?", person.ID).Count(&count).Error; err != nil {
		t.Fatalf("统计关系失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("软删除图片应保留关系: count=%d", count)
	}

	// 硬删除图片才级联清关系。
	if err := db.Unscoped().Delete(&models.Image{}, image.ID).Error; err != nil {
		t.Fatalf("硬删除图片失败: %v", err)
	}
	if err := db.Model(&models.ImagePerson{}).Where("person_id = ?", person.ID).Count(&count).Error; err != nil {
		t.Fatalf("统计关系失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("硬删除图片应级联清理关系: count=%d", count)
	}
}

// 人物侧删除同样级联：人物被清理时不得留下悬空的 image_people 行。
func TestImagePeopleCascadesWhenPersonIsDeleted(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	person := models.Person{DisplayName: "被删人物"}
	if err := db.Create(&person).Error; err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	image := models.Image{Name: "person-cascade.jpg", Path: "/tmp/person-cascade.jpg", Directory: "/tmp", Size: 1, Format: "jpg"}
	if err := db.Create(&image).Error; err != nil {
		t.Fatalf("创建图片失败: %v", err)
	}
	if err := db.Create(&models.ImagePerson{ImageID: image.ID, PersonID: person.ID}).Error; err != nil {
		t.Fatalf("建立图片人物关系失败: %v", err)
	}
	if err := db.Delete(&models.Person{}, person.ID).Error; err != nil {
		t.Fatalf("删除人物失败: %v", err)
	}
	var count int64
	if err := db.Model(&models.ImagePerson{}).Where("image_id = ?", image.ID).Count(&count).Error; err != nil {
		t.Fatalf("统计关系失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("删除人物应级联清理图片关系: count=%d", count)
	}
}
