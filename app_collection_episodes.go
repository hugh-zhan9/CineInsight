package main

import (
	"fmt"
	"video-master/services"
)

func (a *App) GetCollectionEpisodeReport(collectionID, cursor uint, limit int) (*services.CollectionEpisodeReport, error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, err
	}
	defer done()

	if reason := a.databaseUnavailableReason(); reason != "" {
		return nil, fmt.Errorf("%s", reason)
	}
	if a.collectionService == nil {
		return nil, fmt.Errorf("作品集服务未初始化")
	}
	return a.collectionService.GetEpisodeReportContext(ctx, collectionID, cursor, limit)
}
func (a *App) SetCollectionEpisodeAssignments(collectionID uint, edits []services.CollectionEpisodeEdit) error {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return err
	}
	defer done()

	if reason := a.databaseUnavailableReason(); reason != "" {
		return fmt.Errorf("%s", reason)
	}
	if a.collectionService == nil {
		return fmt.Errorf("作品集服务未初始化")
	}
	return a.collectionService.SetEpisodeAssignmentsContext(ctx, collectionID, edits)
}
func (a *App) SetCollectionSeasonExpectation(collectionID uint, season, count int, expectedRevision int64) error {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return err
	}
	defer done()

	if reason := a.databaseUnavailableReason(); reason != "" {
		return fmt.Errorf("%s", reason)
	}
	if a.collectionService == nil {
		return fmt.Errorf("作品集服务未初始化")
	}
	return a.collectionService.SetSeasonExpectationContext(ctx, collectionID, season, count, expectedRevision)
}
