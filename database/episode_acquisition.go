package database

import "gorm.io/gorm"

func ensureCollectionEpisodeRoots(db *gorm.DB) error {
	return db.Exec(`INSERT INTO collection_episode_revision (collection_id, revision)
		SELECT id, 1 FROM media_collections WHERE 1 = 1
		ON CONFLICT (collection_id) DO NOTHING`).Error
}
