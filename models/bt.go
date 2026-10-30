package models

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// BTConnection owns an external service identity; retired rows preserve task history.
type BTConnection struct {
	ID            string `gorm:"primaryKey;size:36" json:"-"`
	Slot          string `gorm:"size:64;not null;uniqueIndex" json:"-"`
	TaskEpoch     uint64 `gorm:"not null" json:"-"`
	Revision      uint64 `gorm:"not null" json:"-"`
	ProwlarrBase  string `gorm:"type:text;not null" json:"-"`
	QBBase        string `gorm:"type:text;not null" json:"-"`
	QBUsername    string `gorm:"size:200;not null" json:"-"`
	CredentialRef string `gorm:"size:80;not null" json:"-"`
	DownloadRoot  string `gorm:"type:text;not null" json:"-"`
	Enabled       bool   `gorm:"not null" json:"-"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// BTTask records durable intent and ownership, never a tracker download URL.
type BTTask struct {
	ID                string       `gorm:"primaryKey;size:36"`
	ConnectionID      string       `gorm:"size:36;not null;index;uniqueIndex:idx_bt_task_v1,priority:1;uniqueIndex:idx_bt_task_v2,priority:1"`
	Connection        BTConnection `gorm:"foreignKey:ConnectionID;constraint:OnDelete:RESTRICT" json:"-"`
	Revision          uint64       `gorm:"not null"`
	AttemptGeneration uint64       `gorm:"not null"`
	Title             string       `gorm:"type:text;not null"`
	IndexerID         int          `gorm:"not null"`
	IndexerName       string       `gorm:"type:text;not null"`
	IntentKey         string       `gorm:"size:200;not null;uniqueIndex"`
	OwnerTag          string       `gorm:"size:80;not null;uniqueIndex"`
	ManifestDigest    string       `gorm:"size:64;not null"`
	InfoHashV1        *string      `gorm:"column:infohash_v1;size:40;uniqueIndex:idx_bt_task_v1,priority:2"`
	InfoHashV2        *string      `gorm:"column:infohash_v2;size:64;uniqueIndex:idx_bt_task_v2,priority:2"`
	QBHash            string       `gorm:"size:64;not null"`
	SaveRoot          string       `gorm:"type:text;not null"`
	CanonicalRoot     string       `gorm:"type:text;not null"`
	RootKey           string       `gorm:"size:64;not null;uniqueIndex"`
	Stage             string       `gorm:"size:32;not null;index"`
	PendingAction     string       `gorm:"size:24;not null;default:''"`
	UnresolvedWrite   bool         `gorm:"not null;default:false"`
	ObservationEpoch  uint64       `gorm:"not null;default:0"`
	RemoteState       string       `gorm:"size:64;not null"`
	LastObservedAt    *time.Time
	SelectionDigest   string `gorm:"size:64;not null"`
	ErrorCode         string `gorm:"size:64;not null"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// BTFile tracks selected and unselected files so incomplete data remains protected.
type BTFile struct {
	TaskID              string  `gorm:"primaryKey;size:36;uniqueIndex:idx_bt_file_path,priority:1"`
	ManifestFileID      string  `gorm:"primaryKey;size:64;check:bt_file_manifest_id_length,length(manifest_file_id) = 64"`
	Position            int     `gorm:"not null"`
	Task                BTTask  `gorm:"foreignKey:TaskID;constraint:OnDelete:RESTRICT" json:"-"`
	RelativePath        string  `gorm:"type:text;not null"`
	PathKey             *string `gorm:"size:64;uniqueIndex:idx_bt_file_path,priority:2"`
	Padding             bool    `gorm:"not null;check:bt_file_path_contract,(padding = true AND path_key IS NULL AND selectable = false AND selected = false) OR (padding = false AND path_key IS NOT NULL AND path_key <> '' AND relative_path <> '' AND (selected = false OR selectable = true))"`
	Selectable          bool    `gorm:"not null"`
	Size                int64   `gorm:"not null"`
	QBFileIndex         *int
	Selected            bool    `gorm:"not null"`
	Progress            float64 `gorm:"not null"`
	EligibilityRevision uint64  `gorm:"not null"`
	ObservedSize        int64   `gorm:"not null"`
	ObservedMtime       int64   `gorm:"not null"`
	ImportState         string  `gorm:"size:32;not null"`
	PublicationState    string  `gorm:"size:24;not null;default:''"`
	DuplicateResult     string  `gorm:"type:text;not null;default:''"`
	VideoID             *uint
	ErrorCode           string `gorm:"size:64;not null"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// BeforeSave requires absent hashes to be NULL and known hashes to be canonical.
func (t *BTTask) BeforeSave(*gorm.DB) error {
	for _, entry := range []struct {
		value  *string
		length int
	}{{t.InfoHashV1, 40}, {t.InfoHashV2, 64}} {
		if entry.value == nil {
			continue
		}
		if len(*entry.value) != entry.length {
			return errors.New("bt_hash_invalid")
		}
		for _, c := range *entry.value {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return errors.New("bt_hash_invalid")
			}
		}
	}
	return nil
}

// BeforeCreate reserves path identity for payload files; repeated padding paths are legal.
func (f *BTFile) BeforeCreate(*gorm.DB) error {
	if len(f.ManifestFileID) != 64 || f.Position < 0 {
		return errors.New("bt_file_id_invalid")
	}
	for _, c := range f.ManifestFileID {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return errors.New("bt_file_id_invalid")
		}
	}

	if f.Padding {
		if f.PathKey != nil || f.Selectable || f.Selected {
			return errors.New("bt_padding_invalid")
		}
		return nil
	}
	if f.PathKey == nil || *f.PathKey == "" || f.RelativePath == "" || f.Selected && !f.Selectable {
		return errors.New("bt_file_invalid")
	}
	return nil
}
