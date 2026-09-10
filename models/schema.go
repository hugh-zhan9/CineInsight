package models

func AllModels() []interface{} {
	// 这份清单的顺序既是建表顺序，也是双向迁移器逐表复制的顺序，因此必须是
	// **拓扑有序**的：被外键引用的表一律排在引用方之前，否则切换数据库后端时
	// 会撞 FOREIGN KEY constraint failed。改动顺序请让
	// models.TestAllModelsIsTopologicallyOrdered 保持通过。
	// （隐式多对多的关联表由迁移器在全部模型之后单独复制，不受此约束。）
	return []interface{}{
		&Video{},
		// Image 紧跟 Video：short_feed_image_interactions、image_people、
		// image_trash_entries 等一大批表都有外键指向 images，它排在后面的话
		// 那些表会先被复制而撞外键。它自己只有 many2many 关联（image_tags），
		// 提到这里不会引入新的依赖倒置。
		&Image{},
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
		&ShortFeedImageInteraction{},
		&ShortFeedTagPreference{},
		&Settings{},
		&ScanDirectory{},
		&SavedLibraryView{},
		&WatchlistEntry{},
		&Person{},
		&VideoPerson{},
		&MediaCollection{},
		&CollectionVideo{},
		&VideoLocalMetadataState{},
		&VideoTechnicalMetadata{},
		&VideoPerceptualHash{},
		&NearDuplicateDismissal{},
		&VideoEnhancementTask{},
		&MediaStream{},
		&ImagePerson{},
		&ImageDirectory{},
		&ImageTrashEntry{},
		&ImageAITagCandidate{},
		&ImageAITagApprovalRecord{},
		&ImageAITaggingState{},
		&ImageNearDuplicateDismissal{},
		&PlayEvent{},
		&TranslationGlossaryEntry{},
		&CollectionSuggestion{},
		&CollectionSuggestionMember{},
		&VideoPlaybackProxy{},
		&VideoFrameHashSequence{},
		&ClipDismissal{},
		// FaceCluster 必须排在 FaceObservation 之前：observations.cluster_id 有
		// 外键指向 clusters，而双向迁移器是按这份清单的顺序逐表复制的，
		// 先搬 observations 会直接撞 FOREIGN KEY constraint failed。
		// （本清单的顺序即建表与迁移顺序，被引用的表一律排在引用方之前。）
		&FaceCluster{},
		&FaceObservation{},
		&FacePersonCandidate{},
	}
}
