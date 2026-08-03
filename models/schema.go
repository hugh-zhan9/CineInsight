package models

func AllModels() []interface{} {
	return []interface{}{
		&Video{},
		&VideoTrashEntry{},
		&SubtitleSegment{},
		&SubtitleIndexState{},
		&Tag{},
		&AITaggingRun{},
		&AITagCandidate{},
		&AITagApprovalRecord{},
		&AITaggingState{},
		&AITagAgentStep{},
		&VideoVisualFingerprint{},
		&VideoSameSourceRelation{},
		&AISameSourceEvaluation{},
		&ShortFeedInteraction{},
		&ShortFeedTagPreference{},
		&Settings{},
		&ScanDirectory{},
		&SavedLibraryView{},
		&Person{},
		&VideoPerson{},
		&MediaCollection{},
		&CollectionVideo{},
		&VideoLocalMetadataState{},
		&VideoTechnicalMetadata{},
		&MediaStream{},
	}
}
