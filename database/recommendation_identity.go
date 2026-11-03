package database

import (
	"fmt"
	"gorm.io/gorm"
	"video-master/models"
)

// validateRecommendationIdentity runs before AutoMigrate can install the tighter
// key. Conflicting historical preferences are never silently deleted.
func validateRecommendationIdentity(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.RecommendationFeedback{}) {
		return nil
	}
	var duplicate struct {
		TargetKind string
		TargetID   uint
		Count      int64
	}
	err := db.Model(&models.RecommendationFeedback{}).Select("target_kind, target_id, COUNT(*) AS count").Group("target_kind, target_id").Having("COUNT(*) > 1").Limit(1).Scan(&duplicate).Error
	if err != nil {
		return err
	}
	if duplicate.Count > 1 {
		return fmt.Errorf("recommendation_identity_conflict: 同一目标有多个推荐偏好，请核对后再升级")
	}
	return nil
}

func finishRecommendationIdentity(db *gorm.DB) error {
	if !db.Migrator().HasIndex(&models.RecommendationFeedback{}, "idx_recommendation_feedback_identity") {
		return fmt.Errorf("推荐目标唯一约束未建立")
	}
	if db.Migrator().HasIndex(&models.RecommendationFeedback{}, "idx_recommendation_feedback_target") {
		return db.Migrator().DropIndex(&models.RecommendationFeedback{}, "idx_recommendation_feedback_target")
	}
	return nil
}
