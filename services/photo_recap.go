package services

import (
	"context"
	"encoding/json"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"strings"
	"unicode/utf8"
	"video-master/database"
	"video-master/models"
)

type PhotoRecapInput struct {
	ID               uint           `json:"id"`
	ExpectedRevision uint64         `json:"expected_revision"`
	Name             string         `json:"name"`
	Selection        PhotoSelection `json:"selection"`
}
type PhotoPersonRef struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}
type PhotoRecapView struct {
	People           []PhotoPersonRef  `json:"people"`
	Recap            models.PhotoRecap `json:"recap"`
	Selection        PhotoSelection    `json:"selection"`
	InvalidPersonIDs []uint            `json:"invalid_person_ids"`
}
type PhotoRecapQuery struct {
	CursorID uint `json:"cursor_id"`
	Limit    int  `json:"limit"`
}
type PhotoRecapPage struct {
	Items      []PhotoRecapView `json:"items"`
	NextCursor uint             `json:"next_cursor"`
}

func photoRecapView(db *gorm.DB, recap models.PhotoRecap) (*PhotoRecapView, error) {
	selection := PhotoSelection{DateFrom: recap.DateFrom, DateTo: recap.DateTo, PersonIDs: []uint{}}
	if json.Unmarshal([]byte(recap.PersonIDsJSON), &selection.PersonIDs) != nil {
		return nil, ErrPhotoFilterInvalid
	}
	invalid, err := photoMissingPeople(db, selection.PersonIDs)
	if err != nil {
		return nil, err
	}
	people := []PhotoPersonRef{}
	if len(selection.PersonIDs) > 0 {
		var rows []models.Person
		if err := db.Select("id", "display_name", "original_name").Where("id IN ?", selection.PersonIDs).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, person := range rows {
			name := person.DisplayName
			if name == "" {
				name = person.OriginalName
			}
			people = append(people, PhotoPersonRef{ID: person.ID, Name: name})
		}
	}
	return &PhotoRecapView{Recap: recap, Selection: selection, InvalidPersonIDs: invalid, People: people}, nil
}
func (s *ImageLibraryService) SavePhotoRecap(ctx context.Context, request PhotoRecapInput) (*PhotoRecapView, error) {
	name := strings.TrimSpace(request.Name)
	if !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > 100 || request.ID > experienceMaxSafeInteger || request.ExpectedRevision >= experienceMaxSafeInteger || request.ID == 0 && request.ExpectedRevision != 0 || request.ID != 0 && request.ExpectedRevision == 0 {
		return nil, ErrPhotoFilterInvalid
	}
	selection, _, err := normalizePhotoSelection(request.Selection, true)
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(selection.PersonIDs)
	var result *PhotoRecapView
	err = database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		tx = tx.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
		if err := requirePhotoPeople(tx, selection.PersonIDs); err != nil {
			return err
		}
		recap := models.PhotoRecap{ID: request.ID, Name: name, DateFrom: selection.DateFrom, DateTo: selection.DateTo, PersonIDsJSON: string(encoded), Revision: 1}
		if request.ID == 0 {
			if err := tx.Create(&recap).Error; err != nil {
				return err
			}
		} else {
			changed := tx.Model(&models.PhotoRecap{}).Where("id = ? AND revision = ?", request.ID, request.ExpectedRevision).Updates(map[string]any{"name": name, "date_from": selection.DateFrom, "date_to": selection.DateTo, "person_ids_json": string(encoded), "revision": gorm.Expr("revision + 1")})
			if changed.Error != nil {
				return changed.Error
			}
			if changed.RowsAffected != 1 {
				return ErrPhotoRecapConflict
			}
			if err := tx.First(&recap, request.ID).Error; err != nil {
				return err
			}
		}
		var err error
		result, err = photoRecapView(tx, recap)
		return err
	})
	return result, err
}
func (s *ImageLibraryService) ListPhotoRecaps(ctx context.Context, request PhotoRecapQuery) (*PhotoRecapPage, error) {
	if request.CursorID > experienceMaxSafeInteger {
		return nil, ErrPhotoFilterInvalid
	}
	limit := photoPageLimit(request.Limit)
	result := &PhotoRecapPage{Items: []PhotoRecapView{}}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
		query := db.Order("id DESC")
		if request.CursorID != 0 {
			query = query.Where("id < ?", request.CursorID)
		}
		var rows []models.PhotoRecap
		if err := query.Limit(limit + 1).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) > limit {
			rows = rows[:limit]
			result.NextCursor = rows[len(rows)-1].ID
		}
		for _, row := range rows {
			view, err := photoRecapView(db, row)
			if err != nil {
				return err
			}
			result.Items = append(result.Items, *view)
		}
		return nil
	})
	return result, err
}
func (s *ImageLibraryService) DeletePhotoRecap(ctx context.Context, id uint, revision uint64) error {
	if id == 0 || id > experienceMaxSafeInteger || revision == 0 || revision >= experienceMaxSafeInteger {
		return ErrPhotoFilterInvalid
	}
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		result := db.Where("id = ? AND revision = ?", id, revision).Delete(&models.PhotoRecap{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPhotoRecapConflict
		}
		return nil
	})
}
