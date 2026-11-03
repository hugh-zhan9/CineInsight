package models

import "time"

// ClipTopic organizes existing bookmarks; it does not own media or copied notes.
type ClipTopic struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:100;not null" json:"name"`
	Note      string    `gorm:"type:text;not null;default:''" json:"note"`
	Revision  uint64    `gorm:"not null;default:1" json:"revision"`
	CreatedAt time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt time.Time `json:"updated_at" ts_type:"string"`
}
type ClipTopicMember struct {
	TopicID    uint          `gorm:"primaryKey;autoIncrement:false;uniqueIndex:idx_clip_topic_position,priority:1" json:"topic_id"`
	Topic      ClipTopic     `gorm:"foreignKey:TopicID;constraint:OnDelete:CASCADE" json:"-"`
	BookmarkID uint          `gorm:"primaryKey;autoIncrement:false;index" json:"bookmark_id"`
	Bookmark   VideoBookmark `gorm:"foreignKey:BookmarkID;constraint:OnDelete:CASCADE" json:"-"`
	Position   int64         `gorm:"not null;check:chk_clip_topic_position,position > 0;uniqueIndex:idx_clip_topic_position,priority:2" json:"position"`
}
