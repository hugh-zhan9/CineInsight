package main

import (
	"context"
	"errors"
	"video-master/services"
)

func (a *App) beginEpisodeOperation(needsBT bool) (context.Context, func(), error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, nil, err
	}
	if a.collectionService == nil {
		done()
		return nil, nil, errors.New("作品集服务尚未就绪")
	}
	if needsBT {
		if a.bt == nil || a.bt.service == nil {
			done()
			return nil, nil, services.ErrBTConnection
		}
		a.bt.mu.Lock()
		ready := a.bt.ready
		a.bt.mu.Unlock()
		if !ready {
			done()
			return nil, nil, services.ErrBTRestorePending
		}
	}
	return ctx, done, nil
}
func (a *App) PrepareEpisodeResourceSearch(request services.EpisodeResourceSearchRequest) services.EpisodeSearchResult {
	ctx, done, err := a.beginEpisodeOperation(false)
	if err != nil {
		return services.EpisodeSearchResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.collectionService.PrepareEpisodeResourceSearch(ctx, request)
	if err != nil {
		return services.EpisodeSearchResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.EpisodeSearchResult{Status: "ok", Data: data}
}
func (a *App) SubmitEpisodeDownload(request services.EpisodeDownloadRequest) services.EpisodeDownloadResult {
	ctx, done, err := a.beginEpisodeOperation(true)
	if err != nil {
		return services.EpisodeDownloadResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.bt.service.SubmitEpisodeDownload(ctx, a.collectionService, request)
	if err != nil {
		return services.EpisodeDownloadResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.EpisodeDownloadResult{Status: "ok", Data: &data}
}
func (a *App) ListEpisodeAcquisitions(request services.EpisodeAcquisitionRequest) services.EpisodeAcquisitionListResult {
	ctx, done, err := a.beginEpisodeOperation(true)
	if err != nil {
		return services.EpisodeAcquisitionListResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.collectionService.ListEpisodeAcquisitions(ctx, request)
	if err != nil {
		return services.EpisodeAcquisitionListResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.EpisodeAcquisitionListResult{Status: "ok", Data: data}
}
func (a *App) PreviewEpisodeAssociation(request services.EpisodeAssociationRequest) services.EpisodeAssociationPreviewResult {
	ctx, done, err := a.beginEpisodeOperation(true)
	if err != nil {
		return services.EpisodeAssociationPreviewResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.collectionService.PreviewEpisodeAssociation(ctx, request)
	if err != nil {
		return services.EpisodeAssociationPreviewResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.EpisodeAssociationPreviewResult{Status: "ok", Data: data}
}
func (a *App) ConfirmEpisodeAssociation(token string) services.EpisodeAssociationConfirmResult {
	ctx, done, err := a.beginEpisodeOperation(true)
	if err != nil {
		return services.EpisodeAssociationConfirmResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.collectionService.ConfirmEpisodeAssociation(ctx, token)
	if err != nil {
		return services.EpisodeAssociationConfirmResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.EpisodeAssociationConfirmResult{Status: "ok", Data: data}
}

func (a *App) DescribeEpisodeCandidates(request services.EpisodeCandidatesRequest) services.EpisodeCandidatesResult {
	ctx, done, err := a.beginEpisodeOperation(true)
	if err != nil {
		return services.EpisodeCandidatesResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.bt.service.DescribeEpisodeCandidates(ctx, a.collectionService, request)
	if err != nil {
		return services.EpisodeCandidatesResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.EpisodeCandidatesResult{Status: "ok", Data: data}
}
