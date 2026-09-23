package models

// VideoAutomaticTagOverride records a user's decision about one automatic tag
// on one video. The current relationship remains in video_tags.
type VideoAutomaticTagOverride struct {
	ID            uint   `gorm:"primarykey" json:"id"`
	VideoID       uint   `gorm:"not null;uniqueIndex:idx_video_automatic_tag_override" json:"video_id"`
	Video         Video  `gorm:"constraint:OnDelete:CASCADE;" json:"-"`
	AutomaticKind string `gorm:"not null;uniqueIndex:idx_video_automatic_tag_override" json:"automatic_kind"`
	Present       bool   `gorm:"not null;default:false" json:"present"`
}
