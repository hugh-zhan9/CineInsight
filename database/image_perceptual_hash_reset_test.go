// 外部测试包：与 idle_scheduling_setting_test.go 同理，dbtest 依赖 database，
// database 的内部测试再依赖 dbtest 会形成导入环。走外部包才能跟着
// CINEINSIGHT_TEST_PG_DSN 在两个后端上各跑一次（TC-11 要求双后端）。
package database_test

import (
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/gorm"
)

func seedHashedImage(t *testing.T, db *gorm.DB, name string, softDeleted bool, stale bool) models.Image {
	t.Helper()
	img := models.Image{
		Name: name, Path: "/tmp/" + name, Directory: "/tmp",
		Size: 100, PerceptualHash: "abcd000000000000",
		HashSourceSize: 100, HashSourceModTimeNS: 123456789,
		IsStale: stale,
	}
	if err := db.Create(&img).Error; err != nil {
		t.Fatalf("创建图片失败(%s): %v", dbtest.Backend(), err)
	}
	if softDeleted {
		if err := db.Delete(&models.Image{}, img.ID).Error; err != nil {
			t.Fatalf("软删图片失败(%s): %v", dbtest.Backend(), err)
		}
	}
	return readImageRow(t, db, img.ID)
}

func readImageRow(t *testing.T, db *gorm.DB, id uint) models.Image {
	t.Helper()
	var got models.Image
	if err := db.Unscoped().First(&got, id).Error; err != nil {
		t.Fatalf("回读图片 %d 失败(%s): %v", id, dbtest.Backend(), err)
	}
	return got
}

// dropImagePerceptualHashSetting 把库还原成"还没跑过新版本"的样子：删掉这一列，
// 再按升级路径重跑 ApplySchema。走真实入口而不是直接调迁移函数，是因为这条迁移的
// 判据在 AutoMigrate 提交那一刻就被消费掉了——把判据的采集挪到 AutoMigrate 之后
// 这种改动，只测迁移函数本身是发现不了的。
func dropImagePerceptualHashSetting(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Migrator().DropColumn(&models.Settings{}, "auto_image_perceptual_hash"); err != nil {
		t.Fatalf("模拟老库删除列失败(%s): %v", dbtest.Backend(), err)
	}
	if db.Migrator().HasColumn(&models.Settings{}, "auto_image_perceptual_hash") {
		t.Fatalf("模拟老库失败：列仍然存在(%s)", dbtest.Backend())
	}
	recycleConnections(t, db)
}

// 老库升级：列是这一轮才建出来的 = 这个库第一次跑到新版本，清空全部旧指纹。
// 软删的和 is_stale 的一并清——它们将来可能恢复，留着旧算法的指纹只会让恢复之后
// 永远和新指纹比不上，而且不报任何异常。
func TestApplySchemaClearsOldImagePerceptualHashesOnFirstUpgrade(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	active := seedHashedImage(t, db, "active.jpg", false, false)
	deleted := seedHashedImage(t, db, "deleted.jpg", true, false)
	stale := seedHashedImage(t, db, "stale.jpg", false, true)

	dropImagePerceptualHashSetting(t, db)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("老库升级失败(%s): %v", dbtest.Backend(), err)
	}

	for _, seeded := range []models.Image{active, deleted, stale} {
		got := readImageRow(t, db, seeded.ID)
		if got.PerceptualHash != "" || got.HashSourceSize != 0 || got.HashSourceModTimeNS != 0 {
			t.Fatalf("图片 %s 的旧指纹未清空(%s): hash=%q size=%d mtime=%d",
				seeded.Name, dbtest.Backend(), got.PerceptualHash, got.HashSourceSize, got.HashSourceModTimeNS)
		}
		// D-CD02 的边界：这条迁移只许动那三列。软删状态尤其不能碰——把 deleted_at
		// 清掉等于在升级时静悄悄地恢复用户删过的图。
		if got.DeletedAt.IsValid() != seeded.DeletedAt.IsValid() {
			t.Fatalf("图片 %s 的软删状态被迁移改动了(%s): 之前 %v，之后 %v",
				seeded.Name, dbtest.Backend(), seeded.DeletedAt.IsValid(), got.DeletedAt.IsValid())
		}
		if got.IsStale != seeded.IsStale || got.Size != seeded.Size || got.Path != seeded.Path {
			t.Fatalf("图片 %s 的其余列被迁移改动了(%s): %+v", seeded.Name, dbtest.Backend(), got)
		}
		// 用 UpdateColumns 而不是 Updates 的原因就在这里：授权清空的只有三列，
		// updated_at 不在其中。
		if !got.UpdatedAt.Equal(seeded.UpdatedAt) {
			t.Fatalf("图片 %s 的 updated_at 不该被迁移刷新(%s): 之前 %v，之后 %v",
				seeded.Name, dbtest.Backend(), seeded.UpdatedAt, got.UpdatedAt)
		}
	}
}

// 列已经存在 = 不是第一次跑到新版本：一行都不许动，否则每次启动都会把补全任务
// 刚算出来的指纹又清掉，用户永远等不到近似重复出结果。
func TestApplySchemaKeepsImagePerceptualHashesOnLaterRuns(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	img := seedHashedImage(t, db, "kept.jpg", false, false)

	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重跑 schema 失败(%s): %v", dbtest.Backend(), err)
	}

	got := readImageRow(t, db, img.ID)
	if got.PerceptualHash != "abcd000000000000" {
		t.Fatalf("列已存在时不该清空指纹(%s)，实际 %q", dbtest.Backend(), got.PerceptualHash)
	}
}

// 清空只发生一次：升级那一轮清完之后，补全任务算出来的新指纹在后续启动里必须留住。
// 这条与上一条的区别在于它跨过了"列刚建出来"那一轮——判据被误写成每次都成立时，
// 只有这条会红。
func TestApplySchemaDoesNotClearAgainAfterUpgrade(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("建立 schema 失败(%s): %v", dbtest.Backend(), err)
	}
	old := seedHashedImage(t, db, "upgraded.jpg", false, false)
	dropImagePerceptualHashSetting(t, db)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("老库升级失败(%s): %v", dbtest.Backend(), err)
	}
	// 模拟补全任务重算出的新指纹。
	if err := db.Model(&models.Image{}).Where("id = ?", old.ID).
		UpdateColumns(map[string]interface{}{
			"perceptual_hash":         "ffff111111111111",
			"hash_source_size":        100,
			"hash_source_mod_time_ns": 123456789,
		}).Error; err != nil {
		t.Fatalf("写入新指纹失败(%s): %v", dbtest.Backend(), err)
	}

	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("重启重跑 schema 失败(%s): %v", dbtest.Backend(), err)
	}

	got := readImageRow(t, db, old.ID)
	if got.PerceptualHash != "ffff111111111111" {
		t.Fatalf("升级之后重算出的指纹不该再被清掉(%s)，实际 %q", dbtest.Backend(), got.PerceptualHash)
	}
}

// ApplySchema 跑完之后这一列必须在，否则上面那条幂等判据永远成立不了。
func TestApplySchemaAddsImagePerceptualHashSetting(t *testing.T) {
	db := dbtest.OpenRaw(t)
	if err := database.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema 失败(%s): %v", dbtest.Backend(), err)
	}
	if !db.Migrator().HasColumn(&models.Settings{}, "auto_image_perceptual_hash") {
		t.Fatalf("auto_image_perceptual_hash 列没有被建出来(%s)", dbtest.Backend())
	}
	// 默认关：这些任务都吃 CPU/IO，装完不该替用户占着机器。
	var settings models.Settings
	if err := db.First(&settings).Error; err != nil {
		t.Fatalf("读取设置失败(%s): %v", dbtest.Backend(), err)
	}
	if settings.AutoImagePerceptualHash {
		t.Fatalf("图片指纹自动补全应默认关闭(%s)", dbtest.Backend())
	}
}
