package services

const experienceMaxSafeInteger = 9007199254740991

// ExperienceError is the bounded error projection for new desktop feature contracts.
type ExperienceError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type RecommendationPreferenceState struct {
	Preference *RecommendationFeedbackItem `json:"preference"`
}
type RecommendationPreferenceResult struct {
	Status string                         `json:"status"`
	Data   *RecommendationPreferenceState `json:"data"`
	Error  *ExperienceError               `json:"error"`
}
type TagMergePreviewResult struct {
	Status string                     `json:"status"`
	Data   *TagMergePreferencePreview `json:"data"`
	Error  *ExperienceError           `json:"error"`
}
type ReviewedTagMergeResult struct {
	Status string           `json:"status"`
	Data   *MergeTagsResult `json:"data"`
	Error  *ExperienceError `json:"error"`
}

type EpisodeSearchResult struct {
	Status string                 `json:"status"`
	Data   *EpisodeResourceSearch `json:"data"`
	Error  *ExperienceError       `json:"error"`
}
type EpisodeDownloadResult struct {
	Status string           `json:"status"`
	Data   *BTTaskView      `json:"data"`
	Error  *ExperienceError `json:"error"`
}
type EpisodeAcquisitionListResult struct {
	Status string                  `json:"status"`
	Data   *EpisodeAcquisitionPage `json:"data"`
	Error  *ExperienceError        `json:"error"`
}
type EpisodeAssociationPreviewResult struct {
	Status string                     `json:"status"`
	Data   *EpisodeAssociationPreview `json:"data"`
	Error  *ExperienceError           `json:"error"`
}
type EpisodeAssociationConfirmResult struct {
	Status string                    `json:"status"`
	Data   *EpisodeAssociationResult `json:"data"`
	Error  *ExperienceError          `json:"error"`
}

type EpisodeCandidatesResult struct {
	Status string                `json:"status"`
	Data   *EpisodeCandidatePage `json:"data"`
	Error  *ExperienceError      `json:"error"`
}

type TextAIStatusResult struct {
	Status string           `json:"status"`
	Data   *TextAIStatus    `json:"data"`
	Error  *ExperienceError `json:"error"`
}
type LibraryQueryDraftResult struct {
	Status string             `json:"status"`
	Data   *LibraryQueryDraft `json:"data"`
	Error  *ExperienceError   `json:"error"`
}
type ParsedLibraryQueryResult struct {
	Status string                  `json:"status"`
	Data   *ParsedLibraryQueryPage `json:"data"`
	Error  *ExperienceError        `json:"error"`
}

type LibraryQueryCancelResult struct {
	Status string                    `json:"status"`
	Data   *LibraryQueryRequestState `json:"data"`
	Error  *ExperienceError          `json:"error"`
}

type ExperienceMutation struct {
	Changed bool `json:"changed"`
}
type ClipTopicResult struct {
	Status string           `json:"status"`
	Data   *ClipTopicDTO    `json:"data"`
	Error  *ExperienceError `json:"error"`
}
type ClipTopicListResult struct {
	Status string           `json:"status"`
	Data   *ClipTopicPage   `json:"data"`
	Error  *ExperienceError `json:"error"`
}
type ClipTopicMembersResult struct {
	Status string                `json:"status"`
	Data   *ClipTopicMembersPage `json:"data"`
	Error  *ExperienceError      `json:"error"`
}
type ClipBookmarkSaveResult struct {
	Status string              `json:"status"`
	Data   *ClipBookmarkResult `json:"data"`
	Error  *ExperienceError    `json:"error"`
}
type BookmarkTopicReferencesResult struct {
	Status string                   `json:"status"`
	Data   *BookmarkTopicReferences `json:"data"`
	Error  *ExperienceError         `json:"error"`
}
type ClipQueuePreviewResult struct {
	Status string            `json:"status"`
	Data   *ClipQueuePreview `json:"data"`
	Error  *ExperienceError  `json:"error"`
}
type ClipQueueResult struct {
	Status string                 `json:"status"`
	Data   *ClipQueueAppendResult `json:"data"`
	Error  *ExperienceError       `json:"error"`
}
type ExperienceMutationResult struct {
	Status string              `json:"status"`
	Data   *ExperienceMutation `json:"data"`
	Error  *ExperienceError    `json:"error"`
}

type DialogueVideoResult struct {
	Status string             `json:"status"`
	Data   *DialogueVideoPage `json:"data"`
	Error  *ExperienceError   `json:"error"`
}

type DialogueHitResult struct {
	Status string           `json:"status"`
	Data   *DialogueHitPage `json:"data"`
	Error  *ExperienceError `json:"error"`
}

type RankedSearchResult struct {
	Status string            `json:"status"`
	Data   *RankedSearchPage `json:"data"`
	Error  *ExperienceError  `json:"error"`
}

type ChapterSetsResult struct {
	Status string            `json:"status"`
	Data   *VideoChapterSets `json:"data"`
	Error  *ExperienceError  `json:"error"`
}

type ChapterSetResult struct {
	Status string           `json:"status"`
	Data   *ChapterSetView  `json:"data"`
	Error  *ExperienceError `json:"error"`
}

type ChapterResolutionResult struct {
	Status string             `json:"status"`
	Data   *ChapterResolution `json:"data"`
	Error  *ExperienceError   `json:"error"`
}

type ChapterPreparationResult struct {
	Status string                   `json:"status"`
	Data   *ChapterDraftPreparation `json:"data"`
	Error  *ExperienceError         `json:"error"`
}

type ChapterStartResult struct {
	Status string             `json:"status"`
	Data   *ChapterDraftStart `json:"data"`
	Error  *ExperienceError   `json:"error"`
}

type ChapterJobListResult struct {
	Status string           `json:"status"`
	Data   *ChapterJobPage  `json:"data"`
	Error  *ExperienceError `json:"error"`
}

type ChapterDraftResult struct {
	Status string            `json:"status"`
	Data   *ChapterDraftView `json:"data"`
	Error  *ExperienceError  `json:"error"`
}

type ChapterApplicationPreviewResult struct {
	Status string                     `json:"status"`
	Data   *ChapterApplicationPreview `json:"data"`
	Error  *ExperienceError           `json:"error"`
}

type ChapterApplicationResult struct {
	Status string                     `json:"status"`
	Data   *ChapterApplicationReceipt `json:"data"`
	Error  *ExperienceError           `json:"error"`
}

type ChapterSetListResult struct {
	Status string           `json:"status"`
	Data   *ChapterSetPage  `json:"data"`
	Error  *ExperienceError `json:"error"`
}

type PhotoMapResultEnvelope struct {
	Status string           `json:"status"`
	Data   *PhotoMapResult  `json:"data"`
	Error  *ExperienceError `json:"error"`
}

type PhotoMapImagesResult struct {
	Status string             `json:"status"`
	Data   *PhotoMapImagePage `json:"data"`
	Error  *ExperienceError   `json:"error"`
}

type PhotoRecapResult struct {
	Status string           `json:"status"`
	Data   *PhotoRecapView  `json:"data"`
	Error  *ExperienceError `json:"error"`
}

type PhotoRecapListResult struct {
	Status string           `json:"status"`
	Data   *PhotoRecapPage  `json:"data"`
	Error  *ExperienceError `json:"error"`
}

type MapTileStatusResult struct {
	Status string           `json:"status"`
	Data   *MapTileStatus   `json:"data"`
	Error  *ExperienceError `json:"error"`
}
