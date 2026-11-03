package services

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"io"
	"math"
	"sort"
	"time"
	"video-master/database"
	"video-master/models"
)

const photoMercatorLatitude = 85.0511287798066

var ErrPhotoFilterInvalid = errors.New("photo_filter_invalid")
var ErrPhotoCursorInvalid = errors.New("photo_cursor_invalid")
var ErrPhotoRecapConflict = errors.New("photo_recap_conflict")

type PhotoSelection struct {
	PersonIDs []uint `json:"person_ids"`
	DateFrom  string `json:"date_from"`
	DateTo    string `json:"date_to"`
}
type PhotoBounds struct {
	West  float64 `json:"west"`
	South float64 `json:"south"`
	East  float64 `json:"east"`
	North float64 `json:"north"`
}
type PhotoMapRequest struct {
	Bounds PhotoBounds `json:"bounds"`
	Zoom   int         `json:"zoom"`
	PhotoSelection
}
type PhotoMapCluster struct {
	Key           string  `json:"key"`
	CenterLat     float64 `json:"center_lat"`
	CenterLon     float64 `json:"center_lon"`
	Count         int64   `json:"count"`
	SampleImageID uint    `json:"sample_image_id"`
}
type PhotoMapResult struct {
	Clusters           []PhotoMapCluster `json:"clusters"`
	MatchedCount       int64             `json:"matched_count"`
	MissingGPSCount    int64             `json:"missing_gps_count"`
	UndatedCount       int64             `json:"undated_count"`
	UnprojectableCount int64             `json:"unprojectable_count"`
	GridLevel          int               `json:"grid_level"`
}
type PhotoMapPageRequest struct {
	Query   PhotoMapRequest `json:"query"`
	CellKey string          `json:"cell_key"`
	Cursor  string          `json:"cursor"`
	Limit   int             `json:"limit"`
}
type PhotoRecapImagesRequest struct {
	Selection PhotoSelection `json:"selection"`
	Cursor    string         `json:"cursor"`
	Limit     int            `json:"limit"`
}
type PhotoMapImage struct {
	models.Image
	TakenDate string `json:"taken_date"`
}
type PhotoMapImagePage struct {
	Items      []PhotoMapImage `json:"items"`
	NextCursor string          `json:"next_cursor"`
	Total      int64           `json:"total"`
}
type photoCursor struct {
	Binding string
	Kind    string
	Level   int
	X       int
	Y       int
	ID      uint
	TakenAt *time.Time
	Expires int64
}
type signedPhotoCursor struct {
	Value photoCursor
	MAC   []byte
}

func normalizePhotoSelection(selection PhotoSelection, datesRequired bool) (PhotoSelection, ImageFilter, error) {
	if len(selection.PersonIDs) > 200 {
		return selection, ImageFilter{}, ErrPhotoFilterInvalid
	}
	for _, id := range selection.PersonIDs {
		if id == 0 || id > experienceMaxSafeInteger {
			return selection, ImageFilter{}, ErrPhotoFilterInvalid
		}
	}
	selection.PersonIDs = uniqueUintIDs(selection.PersonIDs)
	if selection.PersonIDs == nil {
		selection.PersonIDs = []uint{}
	}
	sort.Slice(selection.PersonIDs, func(i, j int) bool { return selection.PersonIDs[i] < selection.PersonIDs[j] })
	filter := ImageFilter{PersonIDs: selection.PersonIDs, SortMode: ImageSortTaken}
	if datesRequired && (selection.DateFrom == "" || selection.DateTo == "") {
		return selection, filter, ErrPhotoFilterInvalid
	}
	for _, part := range []struct {
		text string
		end  bool
	}{{selection.DateFrom, false}, {selection.DateTo, true}} {
		if part.text == "" {
			continue
		}
		date, err := time.ParseInLocation("2006-01-02", part.text, time.Local)
		if err != nil || date.Year() < 1 {
			return selection, filter, ErrPhotoFilterInvalid
		}
		if part.end {
			end := date.AddDate(0, 0, 1).Add(-time.Nanosecond)
			filter.TakenBefore = &end
		} else {
			filter.TakenAfter = &date
		}
	}
	if filter.TakenAfter != nil && filter.TakenBefore != nil && filter.TakenAfter.After(*filter.TakenBefore) {
		return selection, filter, ErrPhotoFilterInvalid
	}
	return selection, filter, nil
}
func normalizePhotoMap(request PhotoMapRequest) (PhotoMapRequest, ImageFilter, error) {
	for _, value := range []float64{request.Bounds.West, request.Bounds.East, request.Bounds.South, request.Bounds.North} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return request, ImageFilter{}, ErrPhotoFilterInvalid
		}
	}
	b := request.Bounds
	if b.West < -180 || b.West > 180 || b.East < -180 || b.East > 180 || b.South < -90 || b.North > 90 || b.South >= b.North || b.West == b.East || request.Zoom < 0 || request.Zoom > 19 {
		return request, ImageFilter{}, ErrPhotoFilterInvalid
	}
	selection, filter, err := normalizePhotoSelection(request.PhotoSelection, false)
	request.PhotoSelection = selection
	return request, filter, err
}
func photoMissingPeople(db *gorm.DB, ids []uint) ([]uint, error) {
	result := []uint{}
	if len(ids) == 0 {
		return result, nil
	}
	var found []uint
	if err := db.Model(&models.Person{}).Where("id IN ?", ids).Pluck("id", &found).Error; err != nil {
		return nil, err
	}
	known := map[uint]bool{}
	for _, id := range found {
		known[id] = true
	}
	for _, id := range ids {
		if !known[id] {
			result = append(result, id)
		}
	}
	return result, nil
}
func requirePhotoPeople(db *gorm.DB, ids []uint) error {
	missing, err := photoMissingPeople(db, ids)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return ErrPhotoFilterInvalid
	}
	return nil
}
func photoQueryBinding(value any) string {
	encoded, _ := json.Marshal(struct {
		DB    string
		Query any
	}{fmt.Sprintf("%p", database.DB), value})
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}
func (s *ImageLibraryService) signPhotoCursor(value photoCursor) string {
	s.photoCursorOnce.Do(func() { s.photoCursorKey = []byte(uuid.NewString()) })
	raw, _ := json.Marshal(value)
	mac := hmac.New(sha256.New, s.photoCursorKey)
	mac.Write(raw)
	encoded, _ := json.Marshal(signedPhotoCursor{value, mac.Sum(nil)})
	return base64.RawURLEncoding.EncodeToString(encoded)
}
func (s *ImageLibraryService) readPhotoCursor(token, kind, binding string) (photoCursor, error) {
	var signed signedPhotoCursor
	if len(token) == 0 || len(token) > 4096 {
		return signed.Value, ErrPhotoCursorInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return signed.Value, ErrPhotoCursorInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&signed) != nil {
		return signed.Value, ErrPhotoCursorInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return signed.Value, ErrPhotoCursorInvalid
	}
	s.photoCursorOnce.Do(func() { s.photoCursorKey = []byte(uuid.NewString()) })
	payload, _ := json.Marshal(signed.Value)
	mac := hmac.New(sha256.New, s.photoCursorKey)
	mac.Write(payload)
	value := signed.Value
	if !hmac.Equal(signed.MAC, mac.Sum(nil)) || value.Kind != kind || value.Binding != binding || value.Expires <= time.Now().Unix() || value.ID > experienceMaxSafeInteger || value.Level < 0 || value.Level > 20 {
		return value, ErrPhotoCursorInvalid
	}
	n := 1 << value.Level
	if value.X < 0 || value.X >= n || value.Y < 0 || value.Y >= n {
		return value, ErrPhotoCursorInvalid
	}
	return value, nil
}
func photoPageLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}
