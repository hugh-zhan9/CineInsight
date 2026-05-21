package models

func AllModels() []interface{} {
	return []interface{}{
		&Video{},
		&SubtitleSegment{},
		&SubtitleIndexState{},
		&Tag{},
		&FaceCluster{},
		&VideoFace{},
		&VideoEmbedding{},
		&AITagCandidate{},
		&AITagApprovalRecord{},
		&AITaggingState{},
		&ShortFeedInteraction{},
		&ShortFeedTagPreference{},
		&Settings{},
		&ScanDirectory{},
	}
}
