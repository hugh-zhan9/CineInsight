package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	LocalMetadataStateMissing         = "missing"
	LocalMetadataStateCurrent         = "current"
	LocalMetadataStateUpdateAvailable = "update_available"
	LocalMetadataStateError           = "error"
)

type LocalMetadataScalarDiff struct {
	Field             string `json:"field"`
	CurrentValue      string `json:"current_value"`
	SourceValue       string `json:"source_value"`
	ChangeType        string `json:"change_type"`
	DefaultSelected   bool   `json:"default_selected"`
	RequiresOverwrite bool   `json:"requires_overwrite"`
}

type LocalMetadataEntity struct {
	ID             uint   `json:"id"`
	Name           string `json:"name"`
	NormalizedName string `json:"normalized_name"`
}

type LocalMetadataEntityCandidate struct {
	SourceName      string                `json:"source_name"`
	NormalizedName  string                `json:"normalized_name"`
	Matches         []LocalMetadataEntity `json:"matches"`
	DefaultMode     string                `json:"default_mode"`
	DefaultEntityID uint                  `json:"default_entity_id"`
}

type LocalMetadataRelationDiff struct {
	Field             string                         `json:"field"`
	Current           []LocalMetadataEntity          `json:"current"`
	Source            []LocalMetadataEntityCandidate `json:"source"`
	ChangeType        string                         `json:"change_type"`
	DefaultSelected   bool                           `json:"default_selected"`
	RequiresOverwrite bool                           `json:"requires_overwrite"`
}

type LocalMetadataArtworkDiff struct {
	Field             string `json:"field"`
	HasCurrent        bool   `json:"has_current"`
	SourceName        string `json:"source_name"`
	ChangeType        string `json:"change_type"`
	DefaultSelected   bool   `json:"default_selected"`
	RequiresOverwrite bool   `json:"requires_overwrite"`
}

type LocalMetadataDiff struct {
	VideoID        uint                      `json:"video_id"`
	ManifestSHA256 string                    `json:"manifest_sha256"`
	CurrentSHA256  string                    `json:"current_sha256"`
	Status         string                    `json:"status"`
	Title          LocalMetadataScalarDiff   `json:"title"`
	OriginalTitle  LocalMetadataScalarDiff   `json:"original_title"`
	Description    LocalMetadataScalarDiff   `json:"description"`
	People         LocalMetadataRelationDiff `json:"people"`
	Collection     LocalMetadataRelationDiff `json:"collection"`
	Poster         LocalMetadataArtworkDiff  `json:"poster"`
	Fanart         LocalMetadataArtworkDiff  `json:"fanart"`
	Warnings       []string                  `json:"warnings"`
}

type localMetadataFile struct {
	kind     string
	name     string
	path     string
	size     int64
	mtimeNS  int64
	priority int
}

type localMetadataSources struct {
	nfo      *localMetadataFile
	poster   *localMetadataFile
	fanart   *localMetadataFile
	manifest []localMetadataFile
	warnings []string
}

type LocalMetadataService struct {
	images          *ManagedImageService
	people          *PersonService
	collections     *CollectionService
	mu              sync.Mutex
	backfillInit    sync.Once
	backfill        *localMetadataBackfill
	backfillLoad    func(context.Context) ([]models.Video, error)
	backfillProcess func(context.Context, uint) (bool, error)
}

func NewLocalMetadataService(dataDir string, people *PersonService, collections *CollectionService) *LocalMetadataService {
	service := &LocalMetadataService{
		images: NewManagedImageService(dataDir), people: people, collections: collections, backfill: &localMetadataBackfill{},
	}
	service.backfillLoad = loadLocalMetadataBackfillVideos
	service.backfillProcess = service.processLocalMetadataBackfillVideo
	return service
}

func (s *LocalMetadataService) backfillState() *localMetadataBackfill {
	s.backfillInit.Do(func() {
		if s.backfill == nil {
			s.backfill = &localMetadataBackfill{}
		}
	})
	return s.backfill
}

func (s *LocalMetadataService) GetDiff(videoID uint) (*LocalMetadataDiff, error) {
	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		return nil, err
	}
	sources, err := discoverLocalMetadataSources(video.Path)
	if err != nil {
		s.recordObservation(videoID, "", LocalMetadataStateError, "source_discovery_failed", err.Error())
		return nil, err
	}
	if len(sources.manifest) == 0 {
		if err := s.recordObservation(videoID, "", LocalMetadataStateMissing, "", ""); err != nil {
			return nil, err
		}
		return &LocalMetadataDiff{VideoID: videoID, Status: LocalMetadataStateMissing, Warnings: sources.warnings}, nil
	}
	manifest, err := localMetadataManifest(sources.manifest)
	if err != nil {
		s.recordObservation(videoID, "", LocalMetadataStateError, "manifest_failed", err.Error())
		return nil, err
	}
	document := LocalMetadataDocument{}
	if sources.nfo != nil {
		content, readErr := readBoundedLocalMetadataFile(sources.nfo.path, localMetadataNFOMaxBytes)
		if readErr != nil {
			s.recordObservation(videoID, manifest, LocalMetadataStateError, "nfo_read_failed", readErr.Error())
			return nil, readErr
		}
		document, err = parseLocalMovieNFO(content)
		if err != nil {
			s.recordObservation(videoID, manifest, LocalMetadataStateError, "nfo_invalid", err.Error())
			return nil, err
		}
	}
	state, err := s.recordObservedManifest(videoID, manifest, localMetadataSourceStat(sources.manifest))
	if err != nil {
		return nil, err
	}
	return s.buildDiff(video, document, sources, manifest, state)
}

// sourcesUnchangedSinceLastObservation reports whether every discovered source still has
// the size and mtime recorded last time, which lets a library scan skip reading and
// hashing the file contents. Explicitly opening the diff always does the full comparison.
func (s *LocalMetadataService) sourcesUnchangedSinceLastObservation(videoID uint, videoPath string) bool {
	sources, err := discoverLocalMetadataSources(videoPath)
	if err != nil || len(sources.manifest) == 0 {
		return false
	}
	var state models.VideoLocalMetadataState
	if err := database.DB.Select("video_id", "observed_source_stat").First(&state, "video_id = ?", videoID).Error; err != nil {
		return false
	}
	if state.ObservedSourceStat == "" {
		return false
	}
	return state.ObservedSourceStat == localMetadataSourceStat(sources.manifest)
}

func localMetadataSourceStat(files []localMetadataFile) string {
	parts := make([]string, 0, len(files))
	for _, file := range files {
		parts = append(parts, fmt.Sprintf("%s\x00%s\x00%d\x00%d", file.kind, strings.ToLower(file.name), file.size, file.mtimeNS))
	}
	sort.Strings(parts)
	digest := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(digest[:])
}

func (s *LocalMetadataService) recordObservedManifest(videoID uint, manifest, sourceStat string) (string, error) {
	now := time.Now()
	state := models.VideoLocalMetadataState{VideoID: videoID}
	err := database.DB.First(&state, "video_id = ?", videoID).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	status := LocalMetadataStateUpdateAvailable
	if state.AppliedManifestSHA256 != "" && state.AppliedManifestSHA256 == manifest {
		status = LocalMetadataStateCurrent
	}
	updates := models.VideoLocalMetadataState{
		VideoID: videoID, ObservedManifestSHA256: manifest, ObservedSourceStat: sourceStat,
		AppliedManifestSHA256: state.AppliedManifestSHA256, Status: status, LastCheckedAt: now,
	}
	if err := database.DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "video_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"observed_manifest_sha256": manifest, "observed_source_stat": sourceStat, "status": status,
			"last_error_code": "", "last_error": "", "last_checked_at": now,
		}),
	}).Create(&updates).Error; err != nil {
		return "", err
	}
	return status, nil
}

func (s *LocalMetadataService) recordObservation(videoID uint, manifest, status, errorCode, message string) error {
	now := time.Now()
	return database.DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "video_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"observed_manifest_sha256": manifest, "status": status, "last_error_code": errorCode,
			"last_error": message, "last_checked_at": now,
		}),
	}).Create(&models.VideoLocalMetadataState{
		VideoID: videoID, ObservedManifestSHA256: manifest, Status: status, LastErrorCode: errorCode,
		LastError: message, LastCheckedAt: now,
	}).Error
}

func (s *LocalMetadataService) buildDiff(video models.Video, source LocalMetadataDocument, files localMetadataSources, manifest, status string) (*LocalMetadataDiff, error) {
	people, err := loadCurrentLocalMetadataPeople(video.ID)
	if err != nil {
		return nil, err
	}
	collections, err := loadCurrentLocalMetadataCollections(video.ID)
	if err != nil {
		return nil, err
	}
	peopleSource, err := matchLocalMetadataPeople(source.People)
	if err != nil {
		return nil, err
	}
	collectionNames := []string{}
	if source.Collection != "" {
		collectionNames = append(collectionNames, source.Collection)
	}
	collectionSource, err := matchLocalMetadataCollections(collectionNames)
	if err != nil {
		return nil, err
	}
	return &LocalMetadataDiff{
		VideoID: video.ID, ManifestSHA256: manifest, CurrentSHA256: localMetadataCurrentFingerprint(video, people, collections), Status: status,
		Title:         scalarLocalMetadataDiff("title", video.DisplayTitle, source.Title),
		OriginalTitle: scalarLocalMetadataDiff("original_title", video.OriginalTitle, source.OriginalTitle),
		Description:   scalarLocalMetadataDiff("description", video.Description, source.Description),
		People:        relationLocalMetadataDiff("people", people, peopleSource),
		Collection:    relationLocalMetadataDiff("collection", collections, collectionSource),
		Poster:        artworkLocalMetadataDiff("poster", video.PosterPath != "", files.poster),
		Fanart:        artworkLocalMetadataDiff("fanart", video.FanartPath != "", files.fanart),
		Warnings:      files.warnings,
	}, nil
}

func localMetadataCurrentFingerprint(video models.Video, people, collections []LocalMetadataEntity) string {
	personIDs := make([]string, 0, len(people))
	for _, entity := range people {
		personIDs = append(personIDs, fmt.Sprintf("%d", entity.ID))
	}
	collectionIDs := make([]string, 0, len(collections))
	for _, entity := range collections {
		collectionIDs = append(collectionIDs, fmt.Sprintf("%d", entity.ID))
	}
	sort.Strings(personIDs)
	sort.Strings(collectionIDs)
	payload := strings.Join([]string{
		video.DisplayTitle, video.OriginalTitle, video.Description, video.PosterPath, video.FanartPath,
		strings.Join(personIDs, ","), strings.Join(collectionIDs, ","),
	}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

func scalarLocalMetadataDiff(field, current, source string) LocalMetadataScalarDiff {
	diff := LocalMetadataScalarDiff{Field: field, CurrentValue: current, SourceValue: source, ChangeType: "none"}
	if source == "" || source == current {
		if source != "" {
			diff.ChangeType = "same"
		}
		return diff
	}
	if current == "" {
		diff.ChangeType = "fill"
		diff.DefaultSelected = true
		return diff
	}
	diff.ChangeType = "overwrite"
	diff.RequiresOverwrite = true
	return diff
}

func relationLocalMetadataDiff(field string, current []LocalMetadataEntity, source []LocalMetadataEntityCandidate) LocalMetadataRelationDiff {
	diff := LocalMetadataRelationDiff{Field: field, Current: current, Source: source, ChangeType: "none"}
	if len(source) == 0 {
		return diff
	}
	currentNames := make([]string, 0, len(current))
	for _, entity := range current {
		currentNames = append(currentNames, entity.NormalizedName)
	}
	sourceNames := make([]string, 0, len(source))
	for _, candidate := range source {
		sourceNames = append(sourceNames, candidate.NormalizedName)
	}
	sort.Strings(currentNames)
	sort.Strings(sourceNames)
	if strings.Join(currentNames, "\x00") == strings.Join(sourceNames, "\x00") {
		diff.ChangeType = "same"
		return diff
	}
	if len(current) == 0 {
		diff.ChangeType = "fill"
		diff.DefaultSelected = true
		return diff
	}
	diff.ChangeType = "overwrite"
	diff.RequiresOverwrite = true
	return diff
}

func artworkLocalMetadataDiff(field string, hasCurrent bool, source *localMetadataFile) LocalMetadataArtworkDiff {
	diff := LocalMetadataArtworkDiff{Field: field, HasCurrent: hasCurrent, ChangeType: "none"}
	if source == nil {
		return diff
	}
	diff.SourceName = source.name
	if hasCurrent {
		diff.ChangeType = "overwrite"
		diff.RequiresOverwrite = true
	} else {
		diff.ChangeType = "fill"
		diff.DefaultSelected = true
	}
	return diff
}

func discoverLocalMetadataSources(videoPath string) (localMetadataSources, error) {
	directory := filepath.Dir(filepath.Clean(videoPath))
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return localMetadataSources{}, errors.New("video directory is unavailable")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return localMetadataSources{}, errors.New("video directory is unavailable")
	}
	stem := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	var nfos, posters, fanarts []localMetadataFile
	warnings := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		extension := strings.ToLower(filepath.Ext(name))
		base := strings.TrimSuffix(name, filepath.Ext(name))
		kind, priority := "", 0
		switch {
		case extension == ".nfo" && strings.EqualFold(base, stem):
			kind = "nfo"
		case isLocalArtworkExtension(extension):
			if value, found := localArtworkPriority(base, []string{stem + "-poster", "poster", "cover", "folder"}); found {
				kind, priority = "poster", value
			} else if value, found := localArtworkPriority(base, []string{stem + "-fanart", "fanart"}); found {
				kind, priority = "fanart", value
			}
		}
		if kind == "" {
			continue
		}
		path := filepath.Join(directory, name)
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil || !pathBelongsToAny(resolved, []string{resolvedDirectory}) {
			warnings = append(warnings, fmt.Sprintf("已忽略目录外或不可用的 %s 候选", kind))
			continue
		}
		info, statErr := os.Stat(resolved)
		if statErr != nil || !info.Mode().IsRegular() {
			warnings = append(warnings, fmt.Sprintf("已忽略不可读取的 %s 候选", kind))
			continue
		}
		file := localMetadataFile{kind: kind, name: name, path: resolved, size: info.Size(), mtimeNS: info.ModTime().UnixNano(), priority: priority}
		switch kind {
		case "nfo":
			nfos = append(nfos, file)
		case "poster":
			posters = append(posters, file)
		case "fanart":
			fanarts = append(fanarts, file)
		}
	}
	sortLocalMetadataFiles(nfos)
	sortLocalMetadataFiles(posters)
	sortLocalMetadataFiles(fanarts)
	if len(nfos) > 1 {
		warnings = append(warnings, "发现多个同名 NFO，已按文件名选择第一个")
	}
	if len(posters) > 1 {
		warnings = append(warnings, "发现多个 poster 候选，已按固定优先级选择")
	}
	if len(fanarts) > 1 {
		warnings = append(warnings, "发现多个 fanart 候选，已按固定优先级选择")
	}
	sources := localMetadataSources{warnings: warnings}
	if len(nfos) > 0 {
		sources.nfo = &nfos[0]
	}
	if len(posters) > 0 {
		sources.poster = &posters[0]
	}
	if len(fanarts) > 0 {
		sources.fanart = &fanarts[0]
	}
	sources.manifest = append(sources.manifest, nfos...)
	sources.manifest = append(sources.manifest, posters...)
	sources.manifest = append(sources.manifest, fanarts...)
	return sources, nil
}

func sortLocalMetadataFiles(files []localMetadataFile) {
	sort.Slice(files, func(i, j int) bool {
		if files[i].priority != files[j].priority {
			return files[i].priority < files[j].priority
		}
		left, right := strings.ToLower(files[i].name), strings.ToLower(files[j].name)
		if left != right {
			return left < right
		}
		return files[i].name < files[j].name
	})
}

func localArtworkPriority(base string, ordered []string) (int, bool) {
	for index, candidate := range ordered {
		if strings.EqualFold(base, candidate) {
			return index, true
		}
	}
	return 0, false
}

func isLocalArtworkExtension(extension string) bool {
	switch extension {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	default:
		return false
	}
}

func localMetadataManifest(files []localMetadataFile) (string, error) {
	parts := make([]string, 0, len(files))
	for _, file := range files {
		limit := managedImageMaxBytes
		if file.kind == "nfo" {
			limit = localMetadataNFOMaxBytes
		}
		content, err := readBoundedLocalMetadataFile(file.path, limit)
		if err != nil {
			// A candidate that cannot be read within its limit is never selected anyway.
			// Identify it by name, size and mtime so changes are still detected, instead
			// of failing discovery for every other field of this video.
			parts = append(parts, fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00unreadable", file.kind, strings.ToLower(file.name), file.size, file.mtimeNS))
			continue
		}
		digest := sha256.Sum256(content)
		parts = append(parts, fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%x", file.kind, strings.ToLower(file.name), file.size, file.mtimeNS, digest))
	}
	sort.Strings(parts)
	manifest := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(manifest[:]), nil
}

func readBoundedLocalMetadataFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("local metadata source is unavailable")
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, errors.New("local metadata source cannot be read")
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("local metadata source exceeds %d byte limit", limit)
	}
	return content, nil
}

func loadCurrentLocalMetadataPeople(videoID uint) ([]LocalMetadataEntity, error) {
	var people []models.Person
	if err := database.DB.Model(&models.Person{}).
		Joins("JOIN video_people ON video_people.person_id = people.id").
		Where("video_people.video_id = ?", videoID).Order("people.id ASC").Find(&people).Error; err != nil {
		return nil, err
	}
	entities := make([]LocalMetadataEntity, 0, len(people))
	for _, person := range people {
		entities = append(entities, LocalMetadataEntity{ID: person.ID, Name: person.DisplayName, NormalizedName: normalizeLocalMetadataName(person.DisplayName)})
	}
	return entities, nil
}

func loadCurrentLocalMetadataCollections(videoID uint) ([]LocalMetadataEntity, error) {
	var collections []models.MediaCollection
	if err := database.DB.Model(&models.MediaCollection{}).
		Joins("JOIN collection_videos ON collection_videos.collection_id = media_collections.id").
		Where("collection_videos.video_id = ?", videoID).Order("media_collections.id ASC").Find(&collections).Error; err != nil {
		return nil, err
	}
	entities := make([]LocalMetadataEntity, 0, len(collections))
	for _, collection := range collections {
		entities = append(entities, LocalMetadataEntity{ID: collection.ID, Name: collection.Name, NormalizedName: collection.NormalizedName})
	}
	return entities, nil
}

func matchLocalMetadataPeople(names []string) ([]LocalMetadataEntityCandidate, error) {
	result := make([]LocalMetadataEntityCandidate, 0, len(names))
	for _, name := range names {
		normalized := normalizeLocalMetadataName(name)
		var people []models.Person
		if err := database.DB.Where("LOWER(display_name) = ? OR LOWER(original_name) = ?", normalized, normalized).Order("id ASC").Find(&people).Error; err != nil {
			return nil, err
		}
		candidate := LocalMetadataEntityCandidate{SourceName: name, NormalizedName: normalized, Matches: make([]LocalMetadataEntity, 0, len(people))}
		for _, person := range people {
			candidate.Matches = append(candidate.Matches, LocalMetadataEntity{ID: person.ID, Name: person.DisplayName, NormalizedName: normalized})
		}
		switch len(candidate.Matches) {
		case 0:
			candidate.DefaultMode = "create_new"
		case 1:
			candidate.DefaultMode = "existing"
			candidate.DefaultEntityID = candidate.Matches[0].ID
		}
		result = append(result, candidate)
	}
	return result, nil
}

func matchLocalMetadataCollections(names []string) ([]LocalMetadataEntityCandidate, error) {
	result := make([]LocalMetadataEntityCandidate, 0, len(names))
	for _, name := range names {
		normalized := normalizeLocalMetadataName(name)
		var collections []models.MediaCollection
		if err := database.DB.Where("normalized_name = ?", normalized).Order("id ASC").Find(&collections).Error; err != nil {
			return nil, err
		}
		candidate := LocalMetadataEntityCandidate{SourceName: name, NormalizedName: normalized, Matches: make([]LocalMetadataEntity, 0, len(collections))}
		for _, collection := range collections {
			candidate.Matches = append(candidate.Matches, LocalMetadataEntity{ID: collection.ID, Name: collection.Name, NormalizedName: collection.NormalizedName})
		}
		if len(candidate.Matches) == 0 {
			candidate.DefaultMode = "create_new"
		} else if len(candidate.Matches) == 1 {
			candidate.DefaultMode = "existing"
			candidate.DefaultEntityID = candidate.Matches[0].ID
		}
		result = append(result, candidate)
	}
	return result, nil
}
