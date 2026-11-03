package main

import (
	"testing"
	"video-master/database"
	"video-master/services"
)

func experienceBindingCodes(a *App) []string {
	preference := a.SetRecommendationTagPreference(services.RecommendationTagPreferenceRequest{TagID: 1, Effect: "boost"})
	preview := a.PreviewTagMergePreferences([]uint{1}, 2)
	merge := a.MergeTagsFromPreferencePreview("missing")
	search := a.PrepareEpisodeResourceSearch(services.EpisodeResourceSearchRequest{})
	download := a.SubmitEpisodeDownload(services.EpisodeDownloadRequest{})
	sources := a.ListEpisodeAcquisitions(services.EpisodeAcquisitionRequest{})
	association := a.PreviewEpisodeAssociation(services.EpisodeAssociationRequest{})
	confirm := a.ConfirmEpisodeAssociation("missing")
	candidates := a.DescribeEpisodeCandidates(services.EpisodeCandidatesRequest{})
	textStatus := a.GetTextAIStatus()
	queryDraft := a.ParseLibraryQuery(services.ParseLibraryQueryRequest{})
	queryResult := a.SearchParsedLibraryQuery(services.SearchParsedLibraryQueryRequest{})
	queryCancel := a.CancelLibraryQuery("missing")
	topicSave := a.SaveClipTopic(services.ClipTopicInput{})
	topicList := a.ListClipTopics(services.ClipTopicQuery{})
	topicMembers := a.ListClipTopicMembers(services.ClipTopicMembersQuery{})
	topicEdit := a.EditClipTopic(services.ClipTopicEdit{})
	topicDelete := a.DeleteClipTopic(0, 0)
	clipBook := a.SaveClipBookmark(services.ClipBookmarkInput{})
	bookRefs := a.GetBookmarkTopicReferences(0, 0)
	clipPreview := a.PreviewClipQueue(services.ClipQueueRequest{})
	clipAppend := a.AppendClipQueue(services.AppendClipQueueRequest{})
	clipDiscard := a.DiscardClipQueuePreview("missing")
	dialogueGroups := a.SearchDialogueVideoGroups(services.DialogueSearchRequest{})
	dialogueHits := a.ListDialogueVideoHits(services.DialogueSearchRequest{})
	visual := a.SearchVisualResults(services.SceneSearchRequest{})
	image := a.SearchImageResults(services.SceneImageSearchRequest{})
	searchClips := a.PreviewSearchClips(services.SearchClipRequest{})
	chapterSets := a.GetVideoChapterSets(0)
	chapterList := a.ListChapterSets(services.ChapterSetQuery{})
	chapterSave := a.SaveVideoChapters(services.SaveChaptersRequest{})
	chapterResolve := a.ResolveChapter(services.ResolveChapterRequest{})
	chapterPrepare := a.PrepareChapterDraft(services.PrepareChapterRequest{})
	chapterStart := a.StartChapterDraft(services.StartChapterRequest{})
	chapterJobs := a.ListChapterDraftJobs(services.ChapterJobQuery{})
	chapterDraft := a.GetChapterDraft(0)
	chapterCancel := a.CancelChapterDraft(0, 0)
	chapterDiscard := a.DiscardChapterDraft(0, 0)
	chapterPreview := a.PreviewChapterApplication(services.ChapterApplicationRequest{})
	chapterApply := a.ApplyChapterDraft("missing")
	chapterDropPreview := a.DiscardChapterApplication("missing")
	chapterCancelAll := a.CancelAllChapterDrafts()
	photoMap := a.QueryPhotoMap(services.PhotoMapRequest{})
	photoCluster := a.ListPhotoMapCluster(services.PhotoMapPageRequest{})
	photoTimeline := a.ListPhotoRecapImages(services.PhotoRecapImagesRequest{})
	photoSave := a.SavePhotoRecap(services.PhotoRecapInput{})
	photoList := a.ListPhotoRecaps(services.PhotoRecapQuery{})
	photoDelete := a.DeletePhotoRecap(0, 0)
	mapStatus := a.GetMapTileStatus()
	mapPolicy := a.SetMapTilesEnabled(services.MapTilePolicyRequest{})
	mapClear := a.ClearMapTileCache()
	result := []string{}
	for _, item := range []struct {
		status string
		data   bool
		err    *services.ExperienceError
	}{
		{photoMap.Status, photoMap.Data != nil, photoMap.Error},
		{photoCluster.Status, photoCluster.Data != nil, photoCluster.Error},
		{photoTimeline.Status, photoTimeline.Data != nil, photoTimeline.Error},
		{photoSave.Status, photoSave.Data != nil, photoSave.Error},
		{photoList.Status, photoList.Data != nil, photoList.Error},
		{photoDelete.Status, photoDelete.Data != nil, photoDelete.Error},
		{mapStatus.Status, mapStatus.Data != nil, mapStatus.Error},
		{mapPolicy.Status, mapPolicy.Data != nil, mapPolicy.Error},
		{mapClear.Status, mapClear.Data != nil, mapClear.Error},
		{chapterSets.Status, chapterSets.Data != nil, chapterSets.Error},
		{chapterList.Status, chapterList.Data != nil, chapterList.Error},
		{chapterSave.Status, chapterSave.Data != nil, chapterSave.Error},
		{chapterResolve.Status, chapterResolve.Data != nil, chapterResolve.Error},
		{chapterPrepare.Status, chapterPrepare.Data != nil, chapterPrepare.Error},
		{chapterStart.Status, chapterStart.Data != nil, chapterStart.Error},
		{chapterJobs.Status, chapterJobs.Data != nil, chapterJobs.Error},
		{chapterDraft.Status, chapterDraft.Data != nil, chapterDraft.Error},
		{chapterCancel.Status, chapterCancel.Data != nil, chapterCancel.Error},
		{chapterDiscard.Status, chapterDiscard.Data != nil, chapterDiscard.Error},
		{chapterPreview.Status, chapterPreview.Data != nil, chapterPreview.Error},
		{chapterApply.Status, chapterApply.Data != nil, chapterApply.Error},
		{chapterDropPreview.Status, chapterDropPreview.Data != nil, chapterDropPreview.Error},
		{chapterCancelAll.Status, chapterCancelAll.Data != nil, chapterCancelAll.Error},
		{dialogueGroups.Status, dialogueGroups.Data != nil, dialogueGroups.Error}, {dialogueHits.Status, dialogueHits.Data != nil, dialogueHits.Error}, {visual.Status, visual.Data != nil, visual.Error}, {image.Status, image.Data != nil, image.Error}, {searchClips.Status, searchClips.Data != nil, searchClips.Error},
		{topicSave.Status, topicSave.Data != nil, topicSave.Error}, {topicList.Status, topicList.Data != nil, topicList.Error}, {topicMembers.Status, topicMembers.Data != nil, topicMembers.Error}, {topicEdit.Status, topicEdit.Data != nil, topicEdit.Error}, {topicDelete.Status, topicDelete.Data != nil, topicDelete.Error}, {clipBook.Status, clipBook.Data != nil, clipBook.Error}, {bookRefs.Status, bookRefs.Data != nil, bookRefs.Error}, {clipPreview.Status, clipPreview.Data != nil, clipPreview.Error}, {clipAppend.Status, clipAppend.Data != nil, clipAppend.Error}, {clipDiscard.Status, clipDiscard.Data != nil, clipDiscard.Error},
		{textStatus.Status, textStatus.Data != nil, textStatus.Error}, {queryDraft.Status, queryDraft.Data != nil, queryDraft.Error}, {queryResult.Status, queryResult.Data != nil, queryResult.Error}, {queryCancel.Status, queryCancel.Data != nil, queryCancel.Error},
		{search.Status, search.Data != nil, search.Error}, {download.Status, download.Data != nil, download.Error}, {sources.Status, sources.Data != nil, sources.Error}, {association.Status, association.Data != nil, association.Error}, {confirm.Status, confirm.Data != nil, confirm.Error}, {candidates.Status, candidates.Data != nil, candidates.Error},
		{preference.Status, preference.Data != nil, preference.Error}, {preview.Status, preview.Data != nil, preview.Error}, {merge.Status, merge.Data != nil, merge.Error},
	} {
		if item.status != "error" || item.data || item.err == nil {
			result = append(result, "invalid_error_projection")
		} else {
			result = append(result, item.err.Code)
		}
	}
	return result
}

func TestExperienceBindingsMaintenanceAndMissingOwners(t *testing.T) {
	a := functionalPolishFixture(t)
	release := database.BeginMaintenance()
	for i, code := range experienceBindingCodes(a) {
		if code != "maintenance" {
			t.Errorf("binding %d: %s", i, code)
		}
	}
	release()
	for i, code := range experienceBindingCodes(&App{}) {
		if code != "dependency_unavailable" {
			t.Errorf("missing owner %d: %s", i, code)
		}
	}
}

func TestExperienceBindingsRelaunchPendingCode(t *testing.T) {
	dataDir, _ := prepareConfigOnlySwitchBack(t)
	a := newMaintenanceTestApp(dataDir)
	t.Cleanup(func() {
		a.releaseDatabaseRestoreMode()
		a.databaseSwitchService = nil
		releasePlaybackAfterShutdownTest(a)
	})
	if result, err := a.SwitchBackendConfigOnly("sqlite"); err != nil || !result.Switched {
		t.Fatalf("switch %+v %v", result, err)
	}
	for i, code := range experienceBindingCodes(a) {
		if code != "relaunch_pending" {
			t.Errorf("binding %d: %s", i, code)
		}
	}
}
