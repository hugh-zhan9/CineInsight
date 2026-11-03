package models

import "time"

// PhotoRecap stores reviewed local conditions, never copies photos or cascades people.
type PhotoRecap struct {
	ID            uint      `gorm:"primarykey" json:"id"`
	Name          string    `gorm:"type:text;not null" json:"name"`
	DateFrom      string    `gorm:"size:10;not null" json:"date_from"`
	DateTo        string    `gorm:"size:10;not null" json:"date_to"`
	PersonIDsJSON string    `gorm:"type:text;not null;default:'[]'" json:"-"`
	Revision      uint64    `gorm:"not null" json:"revision"`
	CreatedAt     time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt     time.Time `json:"updated_at" ts_type:"string"`
}
