package database

import (
	"log"
	"video-master/models"

	"gorm.io/gorm"
)

const (
	// image_ai_descriptions 只能写字面量：它的模型已经从代码里删掉了，
	// 而这段清理正是要面对"表还在、模型已经没了"的库。
	imageAIDescriptionsTable = "image_ai_descriptions"
	// image_semantic_vectors 没有 GORM 模型（由 image_semantic_vector.go 的原始 DDL 建），
	// 名字以那份 DDL 为准。
	imageSemanticVectorsTable = "image_semantic_vectors"
)

// CleanupLegacyImageDescriptions 按 2026-08-18 裁决删除图片 AI 描述的遗留数据：
// 描述行本身，以及由描述参与生成的全部图片语义向量与索引记录。
//
// 这是**不可逆**的一次性清理，但写成启动时幂等检查：
//   - 描述表不存在（新装、或上一次已清理完）→ 整段空转；
//   - 存在 → 先清图片语义向量/索引/尝试记录，再清描述行，最后 DROP 掉描述表。
//
// 先删向量再删描述行，是为了让中途失败留下的状态仍然可判断：描述表还在，就说明没清完，
// 下次启动会重来。任何一步失败都只记日志并原样退出，不做降级路径，也不阻断应用启动
// ——清理失败不该让用户开不了应用，而它天然幂等，下次启动会自然重试。
//
// 直接后果：图片语义检索在用户手动重跑一次图片语义索引任务之前返回空结果。
// 这是裁决明确接受的代价，不要在别处加"向量缺失就回退关键词搜索"之类的兜底把它盖住。
func CleanupLegacyImageDescriptions(db *gorm.DB) {
	if db == nil || !db.Migrator().HasTable(imageAIDescriptionsTable) {
		return
	}

	var descriptions int64
	if err := db.Table(imageAIDescriptionsTable).Count(&descriptions).Error; err != nil {
		log.Printf("[LegacyImageDescriptions] 统计描述行失败，本次跳过清理: %v", err)
		return
	}

	vectors, err := deleteImageSemanticRows(db)
	if err != nil {
		log.Printf("[LegacyImageDescriptions] 清理图片语义数据失败，本次跳过清理: %v", err)
		return
	}
	if err := db.Exec("DELETE FROM " + imageAIDescriptionsTable).Error; err != nil {
		log.Printf("[LegacyImageDescriptions] 删除描述行失败，本次跳过清理: %v", err)
		return
	}
	if err := db.Migrator().DropTable(imageAIDescriptionsTable); err != nil {
		log.Printf("[LegacyImageDescriptions] 删除描述表失败，下次启动会重试: %v", err)
		return
	}
	log.Printf("[LegacyImageDescriptions] 已删除 %d 条图片 AI 描述与 %d 条图片语义向量；"+
		"图片语义检索需要手动重跑一次索引任务才会恢复", descriptions, vectors)
}

// deleteImageSemanticRows 清空图片侧的语义向量与索引记录，返回删除的向量行数。
// 只碰 image_semantic_* 三张表：任何触及 video_semantic_* 的语句都是缺陷。
func deleteImageSemanticRows(db *gorm.DB) (int64, error) {
	var vectors int64
	if db.Migrator().HasTable(imageSemanticVectorsTable) {
		result := db.Exec("DELETE FROM " + imageSemanticVectorsTable)
		if result.Error != nil {
			return 0, result.Error
		}
		vectors = result.RowsAffected
	}
	// 这两张表的模型还在，直接交给 GORM 解析表名，不要自己拼字符串：
	// GORM 的复数化把 ImageSemanticIndex 变成 image_semantic_indices（不是 ...indexes），
	// 手写字面量会让 HasTable 永远为假，索引记录被静默漏删。
	for _, model := range []interface{}{&models.ImageSemanticIndex{}, &models.ImageSemanticIndexAttempt{}} {
		if !db.Migrator().HasTable(model) {
			continue
		}
		if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(model).Error; err != nil {
			return vectors, err
		}
	}
	return vectors, nil
}
