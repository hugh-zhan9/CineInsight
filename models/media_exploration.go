package models

import "time"

// MediaExplorationState owns one scope's current round, scene and mutation receipt.
type MediaExplorationState struct {
	ID                 uint   `gorm:"primaryKey"`
	Scope              string `gorm:"size:16;not null;uniqueIndex"`
	Revision           uint64 `gorm:"not null"`
	RoundID            string `gorm:"size:36;not null"`
	SceneID            string `gorm:"size:36;not null"`
	SceneSequence      uint64 `gorm:"not null"`
	ViewStateJSON      string `gorm:"type:text;not null"`
	LastRequestID      string `gorm:"size:64;not null"`
	LastRequestHash    string `gorm:"size:64;not null"`
	LastRequestKind    string `gorm:"size:16;not null"`
	LastResultRevision uint64 `gorm:"not null"`
	LastResultSceneID  string `gorm:"size:36;not null"`
	UpdatedAt          time.Time
}

// MediaExplorationEntry stores logical media references, not media/watch facts.
// Inactive rows retain this round's presentation history across deletion/regrouping.
type MediaExplorationEntry struct {
	ID               uint                  `gorm:"primaryKey;index:idx_exploration_remaining,priority:4"`
	StateID          uint                  `gorm:"not null;uniqueIndex:idx_exploration_unit,priority:1;index:idx_exploration_remaining,priority:1;index:idx_exploration_scene,priority:1"`
	State            MediaExplorationState `gorm:"foreignKey:StateID;constraint:OnDelete:CASCADE"`
	UnitKey          string                `gorm:"size:64;not null;uniqueIndex:idx_exploration_unit,priority:2"`
	MediaKind        string                `gorm:"size:8;not null"`
	MediaID          uint                  `gorm:"not null"`
	MemberIDsJSON    string                `gorm:"type:text;not null"`
	OrderPosition    int64                 `gorm:"not null;index:idx_exploration_remaining,priority:3"`
	PresentedAt      *time.Time            `gorm:"index:idx_exploration_remaining,priority:2"`
	Available        bool                  `gorm:"not null"`
	SceneID          string                `gorm:"size:36;not null;index:idx_exploration_scene,priority:2"`
	Slot             int                   `gorm:"not null;index:idx_exploration_scene,priority:3"`
	SamplePointsJSON string                `gorm:"type:text;not null"`
}
