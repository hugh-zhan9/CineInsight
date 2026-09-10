package database

import (
	"log"
	"strings"
	"video-master/models"

	"gorm.io/gorm"
)

// The previous version allowed duplicates. Keep the earliest note (there are no
// media references or other user fields), then install the unique index in the
// same transaction. New databases get the index from AutoMigrate.
func migrateWatchlistTitleUniqueness(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.WatchlistEntry{}) || db.Migrator().HasIndex(&models.WatchlistEntry{}, "idx_watchlist_title") {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("LOCK TABLE watchlist_entries IN ACCESS EXCLUSIVE MODE").Error; err != nil {
				return err
			}
		}
		var entries []models.WatchlistEntry
		if err := tx.Order("id ASC").Find(&entries).Error; err != nil {
			return err
		}
		seen := map[string]uint{}
		for _, entry := range entries {
			title := strings.TrimSpace(entry.Title)
			if kept, exists := seen[title]; exists {
				if err := tx.Delete(&models.WatchlistEntry{}, entry.ID).Error; err != nil {
					return err
				}
				log.Printf("想看片单合并重复项 kept_id=%d removed_id=%d", kept, entry.ID)
				continue
			}
			seen[title] = entry.ID
			if title != entry.Title {
				if err := tx.Model(&models.WatchlistEntry{}).Where("id = ?", entry.ID).Update("title", title).Error; err != nil {
					return err
				}
			}
		}
		return tx.Migrator().CreateIndex(&models.WatchlistEntry{}, "idx_watchlist_title")
	})
}
