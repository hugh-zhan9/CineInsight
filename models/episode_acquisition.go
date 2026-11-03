package models

import (
	"gorm.io/gorm"
	"time"
)

// CollectionEpisodeRevision coordinates all membership and episode mutations.
type CollectionEpisodeRevision struct {
	CollectionID uint            `gorm:"primaryKey;autoIncrement:false" json:"collection_id"`
	Collection   MediaCollection `gorm:"foreignKey:CollectionID;constraint:OnDelete:CASCADE" json:"-"`
	Revision     int64           `gorm:"not null;default:1" json:"revision"`
}

func (CollectionEpisodeRevision) TableName() string { return "collection_episode_revision" }

// AfterCreate creates the revision in the same GORM transaction, including NFO
// imports. The database migrator deliberately uses SkipHooks when copying rows.
func (collection *MediaCollection) AfterCreate(tx *gorm.DB) error {
	return tx.Create(&CollectionEpisodeRevision{CollectionID: collection.ID, Revision: 1}).Error
}

// EpisodeAcquisitionOrigin stores only the user's local collection intent. Task
// and file progress remain authoritative in BTTask/BTFile; media IDs are logical.
type EpisodeAcquisitionOrigin struct {
	ID                    string    `gorm:"primaryKey;size:36" json:"id"`
	TaskID                string    `gorm:"size:36;not null;uniqueIndex:idx_episode_acquisition_file,priority:1" json:"task_id"`
	FileID                string    `gorm:"size:64;not null;uniqueIndex:idx_episode_acquisition_file,priority:2" json:"file_id"`
	File                  BTFile    `gorm:"foreignKey:TaskID,FileID;references:TaskID,ManifestFileID;constraint:OnDelete:CASCADE,OnUpdate:CASCADE" json:"-"`
	CollectionID          uint      `gorm:"not null;index;uniqueIndex:idx_episode_acquisition_file,priority:3" json:"collection_id"`
	CollectionTitle       string    `gorm:"type:text;not null" json:"collection_title"`
	RequestedSeason       int       `gorm:"not null" json:"requested_season"`
	RequestedEpisodesJSON string    `gorm:"type:text;not null" json:"-"`
	MappedSeason          *int      `json:"mapped_season"`
	MappedEpisode         *int      `json:"mapped_episode"`
	ConfirmedMapping      bool      `gorm:"not null" json:"confirmed_mapping"`
	Revision              int64     `gorm:"not null" json:"revision"`
	CreatedAt             time.Time `json:"created_at" ts_type:"string"`
}
