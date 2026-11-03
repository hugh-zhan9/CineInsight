package main

import (
	"context"
	"errors"
	"time"
	"video-master/services"
)

func (a *App) beginPhotoOperation() (context.Context, func(), error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, nil, err
	}
	if a.imageLibraryService == nil {
		done()
		return nil, nil, errors.New("图片服务尚未就绪")
	}
	limited, cancel := context.WithTimeout(ctx, 20*time.Second)
	return limited, func() { cancel(); done() }, nil
}
func (a *App) QueryPhotoMap(request services.PhotoMapRequest) services.PhotoMapResultEnvelope {
	ctx, done, err := a.beginPhotoOperation()
	if err != nil {
		return services.PhotoMapResultEnvelope{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.imageLibraryService.QueryPhotoMap(ctx, request)
	if err != nil {
		return services.PhotoMapResultEnvelope{Status: "error", Error: a.experienceError(err)}
	}
	return services.PhotoMapResultEnvelope{Status: "ok", Data: data}
}
func (a *App) ListPhotoMapCluster(request services.PhotoMapPageRequest) services.PhotoMapImagesResult {
	ctx, done, err := a.beginPhotoOperation()
	if err != nil {
		return services.PhotoMapImagesResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.imageLibraryService.ListPhotoMapCluster(ctx, request)
	if err != nil {
		return services.PhotoMapImagesResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.PhotoMapImagesResult{Status: "ok", Data: data}
}
func (a *App) ListPhotoRecapImages(request services.PhotoRecapImagesRequest) services.PhotoMapImagesResult {
	ctx, done, err := a.beginPhotoOperation()
	if err != nil {
		return services.PhotoMapImagesResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.imageLibraryService.ListPhotoRecapImages(ctx, request)
	if err != nil {
		return services.PhotoMapImagesResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.PhotoMapImagesResult{Status: "ok", Data: data}
}
func (a *App) SavePhotoRecap(request services.PhotoRecapInput) services.PhotoRecapResult {
	ctx, done, err := a.beginPhotoOperation()
	if err != nil {
		return services.PhotoRecapResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.imageLibraryService.SavePhotoRecap(ctx, request)
	if err != nil {
		return services.PhotoRecapResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.PhotoRecapResult{Status: "ok", Data: data}
}
func (a *App) ListPhotoRecaps(request services.PhotoRecapQuery) services.PhotoRecapListResult {
	ctx, done, err := a.beginPhotoOperation()
	if err != nil {
		return services.PhotoRecapListResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.imageLibraryService.ListPhotoRecaps(ctx, request)
	if err != nil {
		return services.PhotoRecapListResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.PhotoRecapListResult{Status: "ok", Data: data}
}
func (a *App) DeletePhotoRecap(id uint, revision uint64) services.ExperienceMutationResult {
	ctx, done, err := a.beginPhotoOperation()
	if err != nil {
		return services.ExperienceMutationResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	if err := a.imageLibraryService.DeletePhotoRecap(ctx, id, revision); err != nil {
		return services.ExperienceMutationResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ExperienceMutationResult{Status: "ok", Data: &services.ExperienceMutation{Changed: true}}
}
func (a *App) beginMapTileOperation() (context.Context, func(), error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, nil, err
	}
	if a.mapTiles == nil {
		done()
		return nil, nil, errors.New("底图服务尚未就绪")
	}
	return ctx, done, nil
}
func (a *App) GetMapTileStatus() services.MapTileStatusResult {
	ctx, done, err := a.beginMapTileOperation()
	if err != nil {
		return services.MapTileStatusResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.mapTiles.Status(ctx)
	if err != nil {
		return services.MapTileStatusResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.MapTileStatusResult{Status: "ok", Data: data}
}
func (a *App) SetMapTilesEnabled(request services.MapTilePolicyRequest) services.MapTileStatusResult {
	ctx, done, err := a.beginMapTileOperation()
	if err != nil {
		return services.MapTileStatusResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.mapTiles.SetEnabled(ctx, request)
	if err != nil {
		return services.MapTileStatusResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.MapTileStatusResult{Status: "ok", Data: data}
}
func (a *App) ClearMapTileCache() services.MapTileStatusResult {
	ctx, done, err := a.beginMapTileOperation()
	if err != nil {
		return services.MapTileStatusResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	data, err := a.mapTiles.ClearCache(ctx)
	if err != nil {
		return services.MapTileStatusResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.MapTileStatusResult{Status: "ok", Data: data}
}
