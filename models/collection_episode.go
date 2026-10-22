package models

// CollectionEpisodeAssignment is an explicit correction scoped to collection membership.
type CollectionEpisodeAssignment struct {
	CollectionID uint            `gorm:"primaryKey;autoIncrement:false" json:"collection_id"`
	VideoID      uint            `gorm:"primaryKey;autoIncrement:false" json:"video_id"`
	Member       CollectionVideo `gorm:"belongsTo:Member;foreignKey:CollectionID,VideoID;references:CollectionID,VideoID;constraint:OnDelete:CASCADE,OnUpdate:CASCADE" json:"-"`
	Season       *int            `json:"season"`
	Episode      *int            `json:"episode"`
	Excluded     bool            `gorm:"not null" json:"excluded"`
	Revision     int64           `gorm:"not null" json:"revision"`
}

// CollectionSeasonExpectation records a user's total, never an online inference.
type CollectionSeasonExpectation struct {
	CollectionID uint            `gorm:"primaryKey;autoIncrement:false" json:"collection_id"`
	Collection   MediaCollection `gorm:"foreignKey:CollectionID;constraint:OnDelete:CASCADE" json:"-"`
	Season       int             `gorm:"primaryKey;autoIncrement:false" json:"season"`
	EpisodeCount int             `gorm:"not null;check:chk_collection_season_count,episode_count > 0" json:"episode_count"`
	Revision     int64           `gorm:"not null" json:"revision"`
}
