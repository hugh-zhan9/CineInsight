package services

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"video-master/database"
	"video-master/models"
)

// Capture index source identities before ranking. Reading a new fingerprint only
// after ranking could authorize old positions after a concurrent replacement.
func rankedSourceStates(ctx context.Context, filter *LibraryFilter, modelID string) (map[uint]models.SceneIndexState, error) {
	result := map[uint]models.SceneIndexState{}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
		normalized := LibraryFilter{}
		if filter != nil {
			normalized = *filter
		}
		normalized.CollapseVersions = false
		normalized.SortMode = ""
		scope, err := dialogueScope(db, &normalized)
		if err != nil {
			return err
		}
		var states []models.SceneIndexState
		if err := db.Table(sceneStateTable(modelID)).Where("model_id = ? AND video_id IN (?)", modelID, scope.Select("videos.id")).Find(&states).Error; err != nil {
			return err
		}
		for _, state := range states {
			result[state.VideoID] = state
		}
		return nil
	})
	return result, err
}
func rankedSearchResult(ctx context.Context, result SceneSearchResult, states map[uint]models.SceneIndexState) (*RankedSearchPage, error) {
	page := &RankedSearchPage{Items: []SearchResultHit{}, Coverage: result.Coverage, Notices: result.Notices, Extent: "ranked_top_k", Truncated: result.Truncated}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
		for _, hit := range result.Hits {
			video, info, err := bookmarkSourceInScope(db, hit.VideoID)
			if err != nil && !errors.Is(err, ErrBookmarkSourceUnavailable) && !errors.Is(err, ErrBookmarkSourceChanged) {
				return err
			}
			source := ""
			state, ok := states[hit.VideoID]
			if err == nil && ok && state.ModelID == result.Coverage.VisualModelID && info.Size() == state.SourceSize && info.ModTime().UnixNano() == state.SourceMtimeNS {
				source = videoSourceVersion(video.ID, state.SourceSize, state.SourceMtimeNS)
			}
			page.Items = append(page.Items, sourceResultHit(hit, video, source))
		}
		return nil
	})
	return page, err
}
func (s *SceneSearchService) SearchVisualResults(ctx context.Context, request SceneSearchRequest) (*RankedSearchPage, error) {
	request.Mode = SceneModeVisual
	request, err := normalizeSceneSearchRequest(request)
	if err != nil {
		return nil, err
	}
	_, modelID, _, err := s.visualModel(ctx)
	if err != nil {
		return nil, err
	}
	states, err := rankedSourceStates(ctx, request.Filter, modelID)
	if err != nil {
		return nil, err
	}
	result, err := s.Search(ctx, request)
	if err != nil {
		return nil, err
	}
	return rankedSearchResult(ctx, result, states)
}
func (s *SceneSearchService) SearchImageResults(ctx context.Context, request SceneImageSearchRequest) (*RankedSearchPage, error) {
	states, err := rankedSourceStates(ctx, request.Filter, SceneAppleModelID)
	if err != nil {
		return nil, err
	}
	result, err := s.SearchByImage(ctx, request)
	if err != nil {
		return nil, err
	}
	return rankedSearchResult(ctx, result, states)
}
