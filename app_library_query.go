package main

import "video-master/services"

func (a *App) GetTextAIStatus() services.TextAIStatusResult {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return services.TextAIStatusResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	if a.textAI == nil {
		return services.TextAIStatusResult{Status: "error", Error: a.experienceError(services.ErrTextAIUnavailable)}
	}
	data, err := a.textAI.Status(ctx)
	if err != nil {
		return services.TextAIStatusResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.TextAIStatusResult{Status: "ok", Data: data}
}
func (a *App) ParseLibraryQuery(request services.ParseLibraryQueryRequest) services.LibraryQueryDraftResult {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return services.LibraryQueryDraftResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	if a.libraryQueries == nil {
		return services.LibraryQueryDraftResult{Status: "error", Error: a.experienceError(services.ErrTextAIUnavailable)}
	}
	data, err := a.libraryQueries.Parse(ctx, request)
	if err != nil {
		return services.LibraryQueryDraftResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.LibraryQueryDraftResult{Status: "ok", Data: data}
}
func (a *App) SearchParsedLibraryQuery(request services.SearchParsedLibraryQueryRequest) services.ParsedLibraryQueryResult {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return services.ParsedLibraryQueryResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	if a.libraryQueries == nil {
		return services.ParsedLibraryQueryResult{Status: "error", Error: a.experienceError(services.ErrTextAIUnavailable)}
	}
	data, err := a.libraryQueries.Search(ctx, request)
	if err != nil {
		return services.ParsedLibraryQueryResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.ParsedLibraryQueryResult{Status: "ok", Data: data}
}

func (a *App) CancelLibraryQuery(requestID string) services.LibraryQueryCancelResult {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return services.LibraryQueryCancelResult{Status: "error", Error: a.experienceError(err)}
	}
	defer done()
	if a.libraryQueries == nil {
		return services.LibraryQueryCancelResult{Status: "error", Error: a.experienceError(services.ErrTextAIUnavailable)}
	}
	data, err := a.libraryQueries.Cancel(ctx, requestID)
	if err != nil {
		return services.LibraryQueryCancelResult{Status: "error", Error: a.experienceError(err)}
	}
	return services.LibraryQueryCancelResult{Status: "ok", Data: data}
}
