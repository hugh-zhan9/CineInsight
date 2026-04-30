package models

func AllModels() []interface{} {
	return []interface{}{
		&Video{},
		&SubtitleSegment{},
		&SubtitleIndexState{},
		&Tag{},
		&AITagCandidate{},
		&AITagApprovalRecord{},
		&AITaggingState{},
		&ShortFeedInteraction{},
		&ShortFeedTagPreference{},
		&Settings{},
		&ScanDirectory{},
	}
}
