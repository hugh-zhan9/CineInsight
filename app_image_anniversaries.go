package main

import (
	"fmt"
	"video-master/services"
)

func (a *App) ListImageAnniversaries(request services.ImageAnniversaryRequest) (*services.ImageAnniversaryPage, error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, err
	}
	defer done()

	if reason := a.databaseUnavailableReason(); reason != "" {
		return nil, fmt.Errorf("%s", reason)
	}
	if a.imageLibraryService == nil {
		return nil, fmt.Errorf("图片服务未初始化")
	}
	return a.imageLibraryService.ListImageAnniversariesContext(ctx, request)
}
