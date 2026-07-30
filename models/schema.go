package models

func AllModels() []interface{} {
	return []interface{}{
		&Video{},
		&VideoTrashEntry{},
		&SubtitleSegment{},
		&SubtitleIndexState{},
		&Tag{},
		&AITagCandidate{},
		&AITagApprovalRecord{},
		&AITaggingState{},
		&AITagAgentStep{},
		&VideoVisualFingerprint{},
		&VideoSameSourceRelation{},
		&ShortFeedInteraction{},
		&ShortFeedTagPreference{},
		&Settings{},
		&ScanDirectory{},
	}
}
