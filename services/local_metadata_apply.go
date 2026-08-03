package services

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrLocalMetadataConflict          = errors.New("metadata_conflict")
	ErrLocalMetadataOverwriteRequired = errors.New("metadata_overwrite_required")
)

type LocalMetadataResolution struct {
	NormalizedName string `json:"normalized_name"`
	Mode           string `json:"mode"`
	EntityID       uint   `json:"entity_id"`
}

type LocalMetadataApplyRequest struct {
	VideoID               uint                      `json:"video_id"`
	ManifestSHA256        string                    `json:"manifest_sha256"`
	CurrentSHA256         string                    `json:"current_sha256"`
	SelectedFields        []string                  `json:"selected_fields"`
	OverwriteFields       []string                  `json:"overwrite_fields"`
	PeopleResolutions     []LocalMetadataResolution `json:"people_resolutions"`
	CollectionResolutions []LocalMetadataResolution `json:"collection_resolutions"`
}

type LocalMetadataApplyResult struct {
	VideoID        uint     `json:"video_id"`
	ManifestSHA256 string   `json:"manifest_sha256"`
	AppliedFields  []string `json:"applied_fields"`
	Status         string   `json:"status"`
}

type VideoArtworkData struct {
	VideoID uint   `json:"video_id"`
	Kind    string `json:"kind"`
	MIME    string `json:"mime"`
	DataURL string `json:"data_url"`
}

func (s *LocalMetadataService) Apply(request LocalMetadataApplyRequest) (*LocalMetadataApplyResult, error) {
	if request.VideoID == 0 || strings.TrimSpace(request.ManifestSHA256) == "" || strings.TrimSpace(request.CurrentSHA256) == "" {
		return nil, errors.New("video, manifest and current fingerprints are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.people != nil {
		s.people.mu.Lock()
		defer s.people.mu.Unlock()
	}
	if s.collections != nil {
		s.collections.mu.Lock()
		defer s.collections.mu.Unlock()
	}

	diff, err := s.GetDiff(request.VideoID)
	if err != nil {
		return nil, err
	}
	if diff.ManifestSHA256 != request.ManifestSHA256 || diff.CurrentSHA256 != request.CurrentSHA256 {
		return nil, ErrLocalMetadataConflict
	}
	selected, overwrite, err := validateLocalMetadataSelection(diff, request.SelectedFields, request.OverwriteFields)
	if err != nil {
		return nil, err
	}
	sources, err := discoverLocalMetadataSourcesForManifest(request.VideoID, request.ManifestSHA256)
	if err != nil {
		return nil, err
	}

	imports := make(map[string]managedImageImport)
	cleanupImports := func() {
		seen := make(map[string]struct{})
		for _, imported := range imports {
			if !imported.Created {
				continue
			}
			if _, exists := seen[imported.RelativePath]; exists {
				continue
			}
			seen[imported.RelativePath] = struct{}{}
			_ = s.images.Remove(imported.RelativePath)
		}
	}
	if _, ok := selected["poster"]; ok {
		if sources.poster == nil {
			return nil, errors.New("selected poster source is missing")
		}
		imports["poster"], err = s.images.Import("videos", request.VideoID, sources.poster.path)
		if err != nil {
			cleanupImports()
			return nil, err
		}
	}
	if _, ok := selected["fanart"]; ok {
		if sources.fanart == nil {
			cleanupImports()
			return nil, errors.New("selected fanart source is missing")
		}
		imports["fanart"], err = s.images.Import("videos", request.VideoID, sources.fanart.path)
		if err != nil {
			cleanupImports()
			return nil, err
		}
	}
	if _, err := discoverLocalMetadataSourcesForManifest(request.VideoID, request.ManifestSHA256); err != nil {
		cleanupImports()
		return nil, err
	}

	peopleResolutions, err := indexLocalMetadataResolutions(request.PeopleResolutions)
	if err != nil {
		cleanupImports()
		return nil, err
	}
	collectionResolutions, err := indexLocalMetadataResolutions(request.CollectionResolutions)
	if err != nil {
		cleanupImports()
		return nil, err
	}
	appliedFields := make([]string, 0, len(selected))
	oldArtwork := make([]string, 0, 2)
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		video, currentPeople, currentCollections, loadErr := loadLockedLocalMetadataCurrent(tx, request.VideoID)
		if loadErr != nil {
			return loadErr
		}
		if localMetadataCurrentFingerprint(video, currentPeople, currentCollections) != request.CurrentSHA256 {
			return ErrLocalMetadataConflict
		}
		updates := make(map[string]any)
		if _, ok := selected["title"]; ok {
			updates["display_title"] = diff.Title.SourceValue
			appliedFields = append(appliedFields, "title")
		}
		if _, ok := selected["original_title"]; ok {
			updates["original_title"] = diff.OriginalTitle.SourceValue
			appliedFields = append(appliedFields, "original_title")
		}
		if _, ok := selected["description"]; ok {
			updates["description"] = diff.Description.SourceValue
			appliedFields = append(appliedFields, "description")
		}
		if imported, ok := imports["poster"]; ok {
			if video.PosterPath != "" && video.PosterPath != imported.RelativePath {
				oldArtwork = append(oldArtwork, video.PosterPath)
			}
			updates["poster_path"] = imported.RelativePath
			appliedFields = append(appliedFields, "poster")
		}
		if imported, ok := imports["fanart"]; ok {
			if video.FanartPath != "" && video.FanartPath != imported.RelativePath {
				oldArtwork = append(oldArtwork, video.FanartPath)
			}
			updates["fanart_path"] = imported.RelativePath
			appliedFields = append(appliedFields, "fanart")
		}
		if len(updates) > 0 {
			if err := tx.Model(&video).Updates(updates).Error; err != nil {
				return err
			}
		}
		if _, ok := selected["people"]; ok {
			ids, resolveErr := resolveLocalMetadataPeople(tx, diff.People.Source, peopleResolutions)
			if resolveErr != nil {
				return resolveErr
			}
			// Importing an NFO only rewrites relations; curated people and their avatars
			// stay even when the source no longer lists them.
			if replaceErr := replaceVideoPeopleKeepingEntities(tx, request.VideoID, ids); replaceErr != nil {
				return replaceErr
			}
			appliedFields = append(appliedFields, "people")
		}
		if _, ok := selected["collection"]; ok {
			ids, resolveErr := resolveLocalMetadataCollections(tx, diff.Collection.Source, collectionResolutions)
			if resolveErr != nil {
				return resolveErr
			}
			if err := replaceVideoCollectionsInTransaction(tx, request.VideoID, ids); err != nil {
				return err
			}
			appliedFields = append(appliedFields, "collection")
		}
		now := time.Now()
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "video_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"observed_manifest_sha256": request.ManifestSHA256, "applied_manifest_sha256": request.ManifestSHA256,
				"status": LocalMetadataStateCurrent, "last_error_code": "", "last_error": "", "last_checked_at": now, "applied_at": now,
			}),
		}).Create(&models.VideoLocalMetadataState{
			VideoID: request.VideoID, ObservedManifestSHA256: request.ManifestSHA256, AppliedManifestSHA256: request.ManifestSHA256,
			Status: LocalMetadataStateCurrent, LastCheckedAt: now, AppliedAt: &now,
		}).Error
	})
	if err != nil {
		cleanupImports()
		return nil, err
	}
	for _, path := range oldArtwork {
		s.removeUnreferencedVideoArtwork(path)
	}
	sort.Strings(appliedFields)
	_ = overwrite
	return &LocalMetadataApplyResult{
		VideoID: request.VideoID, ManifestSHA256: request.ManifestSHA256, AppliedFields: appliedFields, Status: LocalMetadataStateCurrent,
	}, nil
}

func (s *LocalMetadataService) ApplyDefaults(videoID uint) (*LocalMetadataApplyResult, error) {
	diff, err := s.GetDiff(videoID)
	if err != nil || diff.Status == LocalMetadataStateMissing {
		return nil, err
	}
	request := LocalMetadataApplyRequest{VideoID: videoID, ManifestSHA256: diff.ManifestSHA256, CurrentSHA256: diff.CurrentSHA256}
	unresolvedDefault := false
	for _, field := range []LocalMetadataScalarDiff{diff.Title, diff.OriginalTitle, diff.Description} {
		if field.DefaultSelected {
			request.SelectedFields = append(request.SelectedFields, field.Field)
		}
	}
	if diff.Poster.DefaultSelected {
		request.SelectedFields = append(request.SelectedFields, "poster")
	}
	if diff.Fanart.DefaultSelected {
		request.SelectedFields = append(request.SelectedFields, "fanart")
	}
	if diff.People.DefaultSelected {
		resolutions, complete := defaultLocalMetadataResolutions(diff.People.Source)
		if complete {
			request.SelectedFields = append(request.SelectedFields, "people")
			request.PeopleResolutions = resolutions
		} else {
			unresolvedDefault = true
		}
	}
	if diff.Collection.DefaultSelected {
		resolutions, complete := defaultLocalMetadataResolutions(diff.Collection.Source)
		if complete {
			request.SelectedFields = append(request.SelectedFields, "collection")
			request.CollectionResolutions = resolutions
		} else {
			unresolvedDefault = true
		}
	}
	if len(request.SelectedFields) == 0 {
		return &LocalMetadataApplyResult{VideoID: videoID, ManifestSHA256: diff.ManifestSHA256, Status: diff.Status}, nil
	}
	result, err := s.Apply(request)
	if err != nil || !unresolvedDefault {
		return result, err
	}
	if err := database.DB.Model(&models.VideoLocalMetadataState{}).Where("video_id = ?", videoID).Updates(map[string]any{
		"applied_manifest_sha256": "", "status": LocalMetadataStateUpdateAvailable,
	}).Error; err != nil {
		return nil, err
	}
	result.Status = LocalMetadataStateUpdateAvailable
	return result, nil
}

func (s *LocalMetadataService) ObserveVideo(videoID uint, isNew bool) error {
	if isNew {
		_, err := s.ApplyDefaults(videoID)
		return err
	}
	// Library scans call this for every existing video, so avoid re-reading and re-hashing
	// every NFO and artwork file when nothing about the sources has changed on disk.
	var video models.Video
	if err := database.DB.Select("id", "path").First(&video, videoID).Error; err != nil {
		return err
	}
	if s.sourcesUnchangedSinceLastObservation(videoID, video.Path) {
		return nil
	}
	_, err := s.GetDiff(videoID)
	return err
}

func (s *LocalMetadataService) ResolveVideoArtwork(videoID uint, kind string) (*VideoArtworkData, error) {
	var video models.Video
	if err := database.DB.Select("id", "poster_path", "fanart_path").First(&video, videoID).Error; err != nil {
		return nil, err
	}
	relative := ""
	switch kind {
	case "poster":
		relative = video.PosterPath
	case "fanart":
		relative = video.FanartPath
	default:
		return nil, errors.New("artwork kind must be poster or fanart")
	}
	asset, err := s.images.Resolve(relative)
	if err != nil {
		return nil, os.ErrNotExist
	}
	content, err := readBoundedLocalMetadataFile(asset.Path, managedImageMaxBytes)
	if err != nil {
		return nil, err
	}
	return &VideoArtworkData{
		VideoID: videoID, Kind: kind, MIME: asset.MIME,
		DataURL: fmt.Sprintf("data:%s;base64,%s", asset.MIME, base64.StdEncoding.EncodeToString(content)),
	}, nil
}

func validateLocalMetadataSelection(diff *LocalMetadataDiff, selectedFields, overwriteFields []string) (map[string]struct{}, map[string]struct{}, error) {
	allowed := map[string]any{
		"title": diff.Title, "original_title": diff.OriginalTitle, "description": diff.Description,
		"people": diff.People, "collection": diff.Collection, "poster": diff.Poster, "fanart": diff.Fanart,
	}
	selected := make(map[string]struct{}, len(selectedFields))
	overwrite := make(map[string]struct{}, len(overwriteFields))
	for _, field := range overwriteFields {
		if _, exists := allowed[field]; !exists {
			return nil, nil, fmt.Errorf("unknown overwrite field %q", field)
		}
		overwrite[field] = struct{}{}
	}
	for _, field := range selectedFields {
		value, exists := allowed[field]
		if !exists {
			return nil, nil, fmt.Errorf("unknown selected field %q", field)
		}
		selected[field] = struct{}{}
		requiresOverwrite, executable := false, false
		switch typed := value.(type) {
		case LocalMetadataScalarDiff:
			requiresOverwrite, executable = typed.RequiresOverwrite, typed.ChangeType == "fill" || typed.ChangeType == "overwrite"
		case LocalMetadataRelationDiff:
			requiresOverwrite, executable = typed.RequiresOverwrite, typed.ChangeType == "fill" || typed.ChangeType == "overwrite"
		case LocalMetadataArtworkDiff:
			requiresOverwrite, executable = typed.RequiresOverwrite, typed.ChangeType == "fill" || typed.ChangeType == "overwrite"
		}
		if !executable {
			return nil, nil, fmt.Errorf("field %q has no executable source change", field)
		}
		if requiresOverwrite {
			if _, confirmed := overwrite[field]; !confirmed {
				return nil, nil, fmt.Errorf("%w: %s", ErrLocalMetadataOverwriteRequired, field)
			}
		}
	}
	return selected, overwrite, nil
}

func discoverLocalMetadataSourcesForManifest(videoID uint, expected string) (localMetadataSources, error) {
	var video models.Video
	if err := database.DB.Select("id", "path").First(&video, videoID).Error; err != nil {
		return localMetadataSources{}, err
	}
	sources, err := discoverLocalMetadataSources(video.Path)
	if err != nil {
		return localMetadataSources{}, err
	}
	manifest, err := localMetadataManifest(sources.manifest)
	if err != nil {
		return localMetadataSources{}, err
	}
	if manifest != expected {
		return localMetadataSources{}, ErrLocalMetadataConflict
	}
	return sources, nil
}

func indexLocalMetadataResolutions(values []LocalMetadataResolution) (map[string]LocalMetadataResolution, error) {
	indexed := make(map[string]LocalMetadataResolution, len(values))
	for _, value := range values {
		value.NormalizedName = normalizeLocalMetadataName(value.NormalizedName)
		if value.NormalizedName == "" {
			return nil, errors.New("metadata resolution name is required")
		}
		if _, exists := indexed[value.NormalizedName]; exists {
			return nil, fmt.Errorf("duplicate metadata resolution %q", value.NormalizedName)
		}
		indexed[value.NormalizedName] = value
	}
	return indexed, nil
}

func defaultLocalMetadataResolutions(candidates []LocalMetadataEntityCandidate) ([]LocalMetadataResolution, bool) {
	resolutions := make([]LocalMetadataResolution, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.DefaultMode == "" {
			return nil, false
		}
		resolutions = append(resolutions, LocalMetadataResolution{
			NormalizedName: candidate.NormalizedName, Mode: candidate.DefaultMode, EntityID: candidate.DefaultEntityID,
		})
	}
	return resolutions, true
}

func loadLockedLocalMetadataCurrent(tx *gorm.DB, videoID uint) (models.Video, []LocalMetadataEntity, []LocalMetadataEntity, error) {
	var video models.Video
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&video, videoID).Error; err != nil {
		return models.Video{}, nil, nil, err
	}
	var people []models.Person
	if err := tx.Model(&models.Person{}).Joins("JOIN video_people ON video_people.person_id = people.id").
		Where("video_people.video_id = ?", videoID).Order("people.id ASC").Find(&people).Error; err != nil {
		return models.Video{}, nil, nil, err
	}
	currentPeople := make([]LocalMetadataEntity, 0, len(people))
	for _, person := range people {
		currentPeople = append(currentPeople, LocalMetadataEntity{ID: person.ID, Name: person.DisplayName, NormalizedName: normalizeLocalMetadataName(person.DisplayName)})
	}
	var collections []models.MediaCollection
	if err := tx.Model(&models.MediaCollection{}).Joins("JOIN collection_videos ON collection_videos.collection_id = media_collections.id").
		Where("collection_videos.video_id = ?", videoID).Order("media_collections.id ASC").Find(&collections).Error; err != nil {
		return models.Video{}, nil, nil, err
	}
	currentCollections := make([]LocalMetadataEntity, 0, len(collections))
	for _, collection := range collections {
		currentCollections = append(currentCollections, LocalMetadataEntity{ID: collection.ID, Name: collection.Name, NormalizedName: collection.NormalizedName})
	}
	return video, currentPeople, currentCollections, nil
}

func resolveLocalMetadataPeople(tx *gorm.DB, candidates []LocalMetadataEntityCandidate, resolutions map[string]LocalMetadataResolution) ([]uint, error) {
	ids := make([]uint, 0, len(candidates))
	for _, candidate := range candidates {
		resolution, exists := resolutions[candidate.NormalizedName]
		if !exists {
			return nil, fmt.Errorf("person resolution required for %q", candidate.SourceName)
		}
		switch resolution.Mode {
		case "existing":
			var person models.Person
			if resolution.EntityID == 0 || tx.First(&person, resolution.EntityID).Error != nil {
				return nil, fmt.Errorf("resolved person %q no longer exists", candidate.SourceName)
			}
			if normalizeLocalMetadataName(person.DisplayName) != candidate.NormalizedName && normalizeLocalMetadataName(person.OriginalName) != candidate.NormalizedName {
				return nil, fmt.Errorf("resolved person no longer matches %q", candidate.SourceName)
			}
			ids = append(ids, person.ID)
		case "create_new":
			person := models.Person{DisplayName: candidate.SourceName}
			if err := tx.Create(&person).Error; err != nil {
				return nil, err
			}
			ids = append(ids, person.ID)
		default:
			return nil, fmt.Errorf("unsupported person resolution mode %q", resolution.Mode)
		}
	}
	return uniqueSortedIDs(ids), nil
}

func resolveLocalMetadataCollections(tx *gorm.DB, candidates []LocalMetadataEntityCandidate, resolutions map[string]LocalMetadataResolution) ([]uint, error) {
	ids := make([]uint, 0, len(candidates))
	for _, candidate := range candidates {
		resolution, exists := resolutions[candidate.NormalizedName]
		if !exists {
			return nil, fmt.Errorf("collection resolution required for %q", candidate.SourceName)
		}
		switch resolution.Mode {
		case "existing":
			var collection models.MediaCollection
			if resolution.EntityID == 0 || tx.First(&collection, resolution.EntityID).Error != nil || collection.NormalizedName != candidate.NormalizedName {
				return nil, fmt.Errorf("resolved collection no longer matches %q", candidate.SourceName)
			}
			ids = append(ids, collection.ID)
		case "create_new":
			var collection models.MediaCollection
			err := tx.Where("normalized_name = ?", candidate.NormalizedName).First(&collection).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				collection = models.MediaCollection{Name: candidate.SourceName, NormalizedName: candidate.NormalizedName}
				if err := tx.Create(&collection).Error; err != nil {
					return nil, err
				}
			} else if err != nil {
				return nil, err
			}
			ids = append(ids, collection.ID)
		default:
			return nil, fmt.Errorf("unsupported collection resolution mode %q", resolution.Mode)
		}
	}
	return uniqueSortedIDs(ids), nil
}

func (s *LocalMetadataService) removeUnreferencedVideoArtwork(relativePath string) {
	if relativePath == "" {
		return
	}
	var count int64
	if err := database.DB.Model(&models.Video{}).Where("poster_path = ? OR fanart_path = ?", relativePath, relativePath).Count(&count).Error; err == nil && count == 0 {
		_ = s.images.Remove(relativePath)
	}
}
