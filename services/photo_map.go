package services

import (
	"context"
	"database/sql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"math"
	"sort"
	"time"
	"video-master/database"
	"video-master/models"
)

const photoValidGPSSQL = `images.gps_latitude IS NOT NULL AND images.gps_longitude IS NOT NULL AND images.gps_latitude BETWEEN -90 AND 90 AND images.gps_longitude BETWEEN -180 AND 180`

type photoGeoRow struct {
	ID           uint
	GPSLatitude  *float64
	GPSLongitude *float64
	TakenAt      *time.Time
}
type photoCellID struct{ X, Y int }
type photoCellAggregate struct {
	Count                                                 int64
	Latitude, SinLongitude, CosLongitude, SampleLongitude float64
	SampleID                                              uint
}

func photoCell(lat, lon float64, level int) photoCellID {
	if lon == 180 {
		lon = -180
	}
	n := float64(int64(1) << level)
	x := int(math.Floor((lon + 180) / 360 * n))
	radians := lat * math.Pi / 180
	y := int(math.Floor((1 - math.Log(math.Tan(math.Pi/4+radians/2))/math.Pi) / 2 * n))
	return photoCellID{max(0, min(int(n)-1, x)), max(0, min(int(n)-1, y))}
}
func addPhotoCell(target *photoCellAggregate, other photoCellAggregate) {
	target.Count += other.Count
	target.Latitude += other.Latitude
	target.SinLongitude += other.SinLongitude
	target.CosLongitude += other.CosLongitude
	if target.SampleID == 0 || other.SampleID < target.SampleID {
		target.SampleID = other.SampleID
		target.SampleLongitude = other.SampleLongitude
	}
}
func photoMapRows(db *gorm.DB, filter ImageFilter, bounds PhotoBounds) *gorm.DB {
	query := applyImageFilterFrom(db.Model(&models.Image{}), filter, db).Where(photoValidGPSSQL).Where("images.gps_latitude BETWEEN ? AND ?", -photoMercatorLatitude, photoMercatorLatitude).Where("images.gps_latitude BETWEEN ? AND ?", bounds.South, bounds.North)
	if bounds.West > bounds.East {
		return query.Where("images.gps_longitude >= ? OR images.gps_longitude <= ?", bounds.West, bounds.East)
	}
	return query.Where("images.gps_longitude BETWEEN ? AND ?", bounds.West, bounds.East)
}
func scanPhotoGeo(ctx context.Context, query *gorm.DB, visit func(photoGeoRow) bool) error {
	rows, err := query.Select("images.id,images.gps_latitude,images.gps_longitude,images.taken_at").Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var row photoGeoRow
		if err := query.ScanRows(rows, &row); err != nil {
			return err
		}
		if !visit(row) {
			return nil
		}
	}
	return rows.Err()
}
func (s *ImageLibraryService) QueryPhotoMap(ctx context.Context, request PhotoMapRequest) (*PhotoMapResult, error) {
	request, filter, err := normalizePhotoMap(request)
	if err != nil {
		return nil, err
	}
	result := &PhotoMapResult{Clusters: []PhotoMapCluster{}, GridLevel: request.Zoom + 1}
	cells := map[photoCellID]*photoCellAggregate{}
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
			if err := requirePhotoPeople(tx, filter.PersonIDs); err != nil {
				return err
			}
			base := func() *gorm.DB { return applyImageFilterFrom(tx.Model(&models.Image{}), filter, tx) }
			if err := base().Where("NOT (" + photoValidGPSSQL + ")").Count(&result.MissingGPSCount).Error; err != nil {
				return err
			}
			if err := base().Where("images.taken_at IS NULL").Count(&result.UndatedCount).Error; err != nil {
				return err
			}
			if err := base().Where(photoValidGPSSQL).Where("images.gps_latitude < ? OR images.gps_latitude > ?", -photoMercatorLatitude, photoMercatorLatitude).Count(&result.UnprojectableCount).Error; err != nil {
				return err
			}
			return scanPhotoGeo(ctx, photoMapRows(tx, filter, request.Bounds), func(row photoGeoRow) bool {
				if row.GPSLatitude == nil || row.GPSLongitude == nil {
					return true
				}
				lat, lon := *row.GPSLatitude, *row.GPSLongitude
				if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) {
					return true
				}
				id := photoCell(lat, lon, result.GridLevel)
				if cells[id] == nil {
					cells[id] = &photoCellAggregate{}
				}
				addPhotoCell(cells[id], photoCellAggregate{Count: 1, Latitude: lat, SinLongitude: math.Sin(lon * math.Pi / 180), CosLongitude: math.Cos(lon * math.Pi / 180), SampleID: row.ID, SampleLongitude: lon})
				result.MatchedCount++
				for len(cells) > 1000 && result.GridLevel > 0 {
					parents := map[photoCellID]*photoCellAggregate{}
					for key, value := range cells {
						parent := photoCellID{key.X / 2, key.Y / 2}
						if parents[parent] == nil {
							parents[parent] = &photoCellAggregate{}
						}
						addPhotoCell(parents[parent], *value)
					}
					cells = parents
					result.GridLevel--
				}
				return true
			})
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	})
	if err != nil {
		return nil, err
	}
	binding := photoQueryBinding(request)
	expires := time.Now().Add(10 * time.Minute).Unix()
	for id, cell := range cells {
		lon := cell.SampleLongitude
		if math.Hypot(cell.SinLongitude, cell.CosLongitude) > 1e-12 {
			lon = math.Atan2(cell.SinLongitude, cell.CosLongitude) * 180 / math.Pi
		}
		token := s.signPhotoCursor(photoCursor{Binding: binding, Kind: "cell", Level: result.GridLevel, X: id.X, Y: id.Y, Expires: expires})
		result.Clusters = append(result.Clusters, PhotoMapCluster{Key: token, CenterLat: cell.Latitude / float64(cell.Count), CenterLon: lon, Count: cell.Count, SampleImageID: cell.SampleID})
	}
	// Stable response order keeps markers predictable without claiming a frozen library.
	sortPhotoClusters(result.Clusters)
	return result, nil
}
func photoPageQuery(query *gorm.DB, cursor photoCursor) *gorm.DB {
	if cursor.ID != 0 {
		if cursor.TakenAt == nil {
			query = query.Where("images.taken_at IS NULL AND images.id < ?", cursor.ID)
		} else {
			query = query.Where("images.taken_at IS NULL OR images.taken_at < ? OR (images.taken_at = ? AND images.id < ?)", *cursor.TakenAt, *cursor.TakenAt, cursor.ID)
		}
	}
	return query.Order("CASE WHEN images.taken_at IS NULL THEN 1 ELSE 0 END ASC,images.taken_at DESC,images.id DESC")
}
func loadPhotoMapImages(tx *gorm.DB, ids []uint) ([]PhotoMapImage, error) {
	result := make([]PhotoMapImage, 0, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []models.Image
	if err := tx.Where("id IN ?", ids).Preload("Tags").Find(&rows).Error; err != nil {
		return nil, err
	}
	byID := map[uint]models.Image{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	for _, id := range ids {
		image, ok := byID[id]
		if !ok {
			continue
		}
		date := ""
		if image.TakenAt != nil {
			date = image.TakenAt.In(time.Local).Format("2006-01-02")
		}
		result = append(result, PhotoMapImage{Image: image, TakenDate: date})
	}
	return result, nil
}
func (s *ImageLibraryService) ListPhotoMapCluster(ctx context.Context, request PhotoMapPageRequest) (*PhotoMapImagePage, error) {
	query, filter, err := normalizePhotoMap(request.Query)
	if err != nil {
		return nil, err
	}
	cell, err := s.readPhotoCursor(request.CellKey, "cell", photoQueryBinding(query))
	if err != nil {
		return nil, err
	}
	binding := photoQueryBinding(struct {
		Query PhotoMapRequest
		Cell  string
	}{query, request.CellKey})
	cursor := photoCursor{Binding: binding, Kind: "map_page", Expires: cell.Expires}
	if request.Cursor != "" {
		cursor, err = s.readPhotoCursor(request.Cursor, "map_page", binding)
		if err != nil || cursor.ID == 0 {
			return nil, ErrPhotoCursorInvalid
		}
	}
	limit := photoPageLimit(request.Limit)
	result := &PhotoMapImagePage{Items: []PhotoMapImage{}}
	ids := []uint{}
	matches := func(row photoGeoRow) bool {
		return row.GPSLatitude != nil && row.GPSLongitude != nil && photoCell(*row.GPSLatitude, *row.GPSLongitude, cell.Level) == (photoCellID{cell.X, cell.Y})
	}
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
			if err := requirePhotoPeople(tx, filter.PersonIDs); err != nil {
				return err
			}
			base := func() *gorm.DB { return photoMapRows(tx, filter, query.Bounds) }
			if err := scanPhotoGeo(ctx, base(), func(row photoGeoRow) bool {
				if matches(row) {
					result.Total++
				}
				return true
			}); err != nil {
				return err
			}
			if err := scanPhotoGeo(ctx, photoPageQuery(base(), cursor), func(row photoGeoRow) bool {
				if matches(row) {
					ids = append(ids, row.ID)
				}
				return len(ids) < limit+1
			}); err != nil {
				return err
			}
			more := len(ids) > limit
			if more {
				ids = ids[:limit]
			}
			var err error
			result.Items, err = loadPhotoMapImages(tx, ids)
			if err != nil {
				return err
			}
			if more && len(result.Items) > 0 {
				last := result.Items[len(result.Items)-1]
				cursor.ID = last.ID
				cursor.TakenAt = last.TakenAt
				result.NextCursor = s.signPhotoCursor(cursor)
			}
			return nil
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	})
	return result, err
}
func (s *ImageLibraryService) ListPhotoRecapImages(ctx context.Context, request PhotoRecapImagesRequest) (*PhotoMapImagePage, error) {
	selection, filter, err := normalizePhotoSelection(request.Selection, true)
	if err != nil {
		return nil, err
	}
	binding := photoQueryBinding(selection)
	cursor := photoCursor{Binding: binding, Kind: "recap_page", Expires: time.Now().Add(10 * time.Minute).Unix()}
	if request.Cursor != "" {
		cursor, err = s.readPhotoCursor(request.Cursor, "recap_page", binding)
		if err != nil || cursor.ID == 0 {
			return nil, ErrPhotoCursorInvalid
		}
	}
	result := &PhotoMapImagePage{Items: []PhotoMapImage{}}
	limit := photoPageLimit(request.Limit)
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
			if err := requirePhotoPeople(tx, filter.PersonIDs); err != nil {
				return err
			}
			base := func() *gorm.DB {
				return applyImageFilterFrom(tx.Model(&models.Image{}), filter, tx).Where("images.taken_at IS NOT NULL")
			}
			if err := base().Count(&result.Total).Error; err != nil {
				return err
			}
			var ids []uint
			if err := photoPageQuery(base(), cursor).Limit(limit+1).Pluck("images.id", &ids).Error; err != nil {
				return err
			}
			more := len(ids) > limit
			if more {
				ids = ids[:limit]
			}
			var err error
			result.Items, err = loadPhotoMapImages(tx, ids)
			if err != nil {
				return err
			}
			if more && len(result.Items) > 0 {
				last := result.Items[len(result.Items)-1]
				cursor.ID = last.ID
				cursor.TakenAt = last.TakenAt
				result.NextCursor = s.signPhotoCursor(cursor)
			}
			return nil
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	})
	return result, err
}

func sortPhotoClusters(clusters []PhotoMapCluster) {
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].SampleImageID < clusters[j].SampleImageID })
}
