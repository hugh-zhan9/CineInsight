package services

import (
	"gorm.io/gorm"
	"video-master/models"
)

// claimCollectionMutation owns the collection aggregate CAS. Normal edits read
// its current revision; reviewed changes additionally supply the expected one.
func claimCollectionMutation(tx *gorm.DB, collectionID uint, expected ...int64) error {
	var collection models.MediaCollection
	if err := tx.Select("id").First(&collection, collectionID).Error; err != nil {
		return err
	}
	var state models.CollectionEpisodeRevision
	if err := tx.First(&state, "collection_id = ?", collectionID).Error; err != nil {
		return err
	}
	if state.Revision < 1 || state.Revision >= experienceMaxSafeInteger || len(expected) > 1 || len(expected) == 1 && state.Revision != expected[0] {
		return ErrCollectionOrderConflict
	}
	result := tx.Model(&models.CollectionEpisodeRevision{}).Where("collection_id = ? AND revision = ?", collectionID, state.Revision).UpdateColumn("revision", state.Revision+1)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCollectionOrderConflict
	}
	return nil
}
