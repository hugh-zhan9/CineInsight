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
		&VideoAutomaticTagOverride{},
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
		// 人脸链路写入记录（META-04）：没有外键（多态媒体引用，人物删除由审阅对账清理），
		// 排在哪里都满足拓扑序，放在人脸表旁边便于查找。
		&FaceRelationWrite{},
		// 年度电影榜单的三张表放在末尾：它们之间与对既有表都没有外键
		// （标记表按豆瓣 ID 字符串逻辑关联条目缓存表，理由见 models/movie_chart.go），
		// 因此不引入新的拓扑序约束，追加在哪里都行，追加在末尾最省事。
		&MovieChartEntry{},
		&MovieChartMark{},
		&MovieChartYearState{},
		// 产品完善度批次（2026-09-29）的七张新表，追加在末尾。其中 subtitle_jobs、
		// movie_video_links、cleanup_video_dismissals 有外键指向 videos，videos 是清单第一项，
		// 所以排在哪里都满足拓扑序；其余四张不建外键（见各模型注释）。
		&MigrationStagedSource{},
		&SubtitleJob{},
		&TagPersonConversion{},
		&MovieVideoLink{},
		&JellyfinSession{},
		&BrowserDownloadTask{},
		&CleanupVideoDismissal{},
		&CleanupConsolidationTask{},
		&CleanupConsolidationTaskPlan{},
		&CleanupConsolidationTaskItem{},
		// User-owned notes survive media deletion. Their logical references have
		// no cascade FK; migrator preserves consumed IDs to prevent reassignment.
		&VideoBookmark{},
		&ViewingDiaryEntry{},
		&PlaybackQueueState{},
		&PlaybackQueueEntry{},
		// 多版本聚合（D-MW-VERSIONS）：成员表外键指向 videos 与版本组表，版本组表排在成员表之前。
		&VideoVersionGroup{},
		&VideoVersionMember{},
		&VideoVersionSuggestionDismissal{},
		// 场景检索的画面索引（D-MW-SCENES）：两张表只有指向 videos 的外键，videos 是清单
		// 第一项，追加在末尾满足拓扑序。
		&SceneIndexState{},
		&SceneVisualSegment{},
		// 视频工作台（视频编辑合同）：items.project_id 级联指向 projects，projects 必须在前；
		// 两张表对 videos 都只有逻辑引用（来源在配方/计划 JSON 里，成品 output_video_id 无外键）。
		&VideoEditProject{},
		&VideoEditItem{},
	}
}
