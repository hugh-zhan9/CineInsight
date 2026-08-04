package database

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"video-master/models"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var DB *gorm.DB

// PostgresCLIConfig contains the connection fields needed by PostgreSQL client
// tools. Passwords are intentionally exposed only as environment values so
// callers never need to place credentials in process arguments.
type PostgresCLIConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Database string
	SSLMode  string
}

func PostgresCLIConfigFromEnv() (PostgresCLIConfig, error) {
	config := PostgresCLIConfig{
		Host:     os.Getenv("PG_HOST"),
		Port:     os.Getenv("PG_PORT"),
		User:     os.Getenv("PG_USER"),
		Password: os.Getenv("PG_PASSWORD"),
		Database: os.Getenv("PG_DB"),
		SSLMode:  os.Getenv("PG_SSLMODE"),
	}
	if config.Host == "" {
		return PostgresCLIConfig{}, fmt.Errorf("PG_HOST 不能为空")
	}
	if config.User == "" {
		return PostgresCLIConfig{}, fmt.Errorf("PG_USER 不能为空")
	}
	if config.Database == "" {
		return PostgresCLIConfig{}, fmt.Errorf("PG_DB 不能为空")
	}
	if config.Port == "" {
		config.Port = "5432"
	}
	if config.SSLMode == "" {
		config.SSLMode = "disable"
	}
	return config, nil
}

func (config PostgresCLIConfig) Environment() []string {
	return []string{
		"PGHOST=" + config.Host,
		"PGPORT=" + config.Port,
		"PGUSER=" + config.User,
		"PGPASSWORD=" + config.Password,
		"PGDATABASE=" + config.Database,
		"PGSSLMODE=" + config.SSLMode,
	}
}

func loadEnvConfig() {
	paths := []string{".env"}

	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		paths = append(paths,
			filepath.Join(exeDir, ".env"),
			filepath.Join(exeDir, "..", "Resources", ".env"),
		)
	}

	seen := make(map[string]struct{}, len(paths))
	uniquePaths := make([]string, 0, len(paths))
	for _, path := range paths {
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		uniquePaths = append(uniquePaths, clean)
	}

	for _, path := range uniquePaths {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		_ = godotenv.Load(path)
	}
}

func postgresDSNFromEnv() (string, error) {
	config, err := PostgresCLIConfigFromEnv()
	if err != nil {
		return "", err
	}
	timezone := os.Getenv("PG_TIMEZONE")

	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		config.Host, config.Port, config.User, config.Password, config.Database, config.SSLMode,
	)
	if timezone != "" {
		dsn = fmt.Sprintf("%s TimeZone=%s", dsn, timezone)
	}
	return dsn, nil
}

// Init 初始化数据库
func Init() error {
	loadEnvConfig()

	// 获取用户数据目录
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("获取用户目录失败: %w", err)
	}

	// 创建应用数据目录
	dataDir := filepath.Join(homeDir, ".video-master")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("创建数据目录失败: %w", err)
	}

	dsn, err := postgresDSNFromEnv()
	if err != nil {
		return err
	}

	// 连接数据库
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}
	published := false
	defer func() {
		if published {
			return
		}
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	}()

	// 如果表存在，先清理重复数据，避免 AutoMigrate 创建唯一索引失败
	if db.Migrator().HasTable(&models.Video{}) {
		if err := cleanupReimportedSoftDeletedVideos(db); err != nil {
			return fmt.Errorf("清理软删除重导入视频失败: %w", err)
		}
		if err := cleanupDuplicateVideos(db); err != nil {
			return fmt.Errorf("清理重复视频失败: %w", err)
		}
	}

	settingsTableExisted := db.Migrator().HasTable(&models.Settings{})
	libraryWatchColumnExisted := settingsTableExisted && db.Migrator().HasColumn(&models.Settings{}, "library_watch_enabled")
	localMetadataColumnExisted := settingsTableExisted && db.Migrator().HasColumn(&models.Settings{}, "local_metadata_enabled")
	aiQualityColumnExisted := settingsTableExisted && db.Migrator().HasColumn(&models.Settings{}, "ai_quality_enabled")

	// 自动迁移数据表
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	if err := migrateLibraryWatchSetting(db, settingsTableExisted, libraryWatchColumnExisted); err != nil {
		return fmt.Errorf("迁移实时同步设置失败: %w", err)
	}
	if err := migrateWorkflowFeatureSettings(db, settingsTableExisted, localMetadataColumnExisted, aiQualityColumnExisted); err != nil {
		return fmt.Errorf("迁移本地工作流设置失败: %w", err)
	}
	if err := ensureVideoPathUniqueIndex(db); err != nil {
		return fmt.Errorf("创建视频路径唯一索引失败: %w", err)
	}
	if err := ensureMediaDetailConstraints(db); err != nil {
		return fmt.Errorf("创建媒体详情约束失败: %w", err)
	}
	ensureCoreQueryIndexes(db)
	ensureAITaggingIndexes(db)
	ensureShortFeedIndexes(db)
	ensureSubtitleSearchIndexes(db)
	// 初始化默认设置
	var settings models.Settings
	if err := db.First(&settings).Error; err == gorm.ErrRecordNotFound {
		// 默认支持的视频格式
		defaultExts := ".mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt"
		settings = models.Settings{
			ConfirmBeforeDelete:          true,
			DeleteOriginalFile:           false,
			VideoExtensions:              defaultExts,
			PlayWeight:                   2.0, // 默认 1次播放 = 2次随机播放
			RandomHalfLifeDays:           90,
			AutoScanOnStartup:            false,
			LibraryWatchEnabled:          true,
			LocalMetadataEnabled:         true,
			AIQualityEnabled:             true,
			ShortFeedMaxDurationMinutes:  5,
			ShortFeedFeedbackSyncEnabled: true,
			LogEnabled:                   false,
			SubtitleTranslationProvider:  "deepl",
			SubtitleWhisperXModel:        "medium",
			SubtitleWhisperXBatchSize:    8,
			AITaggingFrameCount:          0,
			AITaggingImagesPerRequest:    10,
			AITaggingSubtitleCharLimit:   4000,
			AITaggingStartupBatchSize:    10,
			AITaggingMaxExtraFrames:      20,
			BackupRetentionCount:         7,
			BackupIntervalHours:          24,
		}
		if err := db.Create(&settings).Error; err != nil {
			return fmt.Errorf("初始化默认设置失败: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("读取默认设置失败: %w", err)
	}
	if err := registerMaintenanceCallbacks(db); err != nil {
		return fmt.Errorf("注册数据库维护屏障失败: %w", err)
	}
	DB = db
	published = true
	return nil
}

func migrateLibraryWatchSetting(db *gorm.DB, settingsTableExisted, libraryWatchColumnExisted bool) error {
	if !settingsTableExisted || libraryWatchColumnExisted {
		return nil
	}
	return db.Model(&models.Settings{}).Where("1 = 1").Update("library_watch_enabled", false).Error
}

func migrateWorkflowFeatureSettings(db *gorm.DB, settingsTableExisted, localMetadataColumnExisted, aiQualityColumnExisted bool) error {
	if !settingsTableExisted {
		return nil
	}
	if !localMetadataColumnExisted {
		if err := db.Model(&models.Settings{}).Where("1 = 1").Update("local_metadata_enabled", false).Error; err != nil {
			return err
		}
	}
	if !aiQualityColumnExisted {
		return db.Model(&models.Settings{}).Where("1 = 1").Update("ai_quality_enabled", true).Error
	}
	return nil
}

func cleanupDuplicateVideos(db *gorm.DB) error {
	type duplicatePath struct {
		Path   string
		KeepID uint
	}

	var duplicates []duplicatePath
	if err := db.Raw(`
		SELECT path, MAX(id) AS keep_id
		FROM videos
		WHERE deleted_at IS NULL AND path <> ''
		GROUP BY path
		HAVING COUNT(*) > 1
	`).Scan(&duplicates).Error; err != nil {
		return err
	}

	for _, d := range duplicates {
		var duplicateIDs []uint
		if err := db.Raw(`
			SELECT id
			FROM videos
			WHERE path = ? AND deleted_at IS NULL AND id <> ?
		`, d.Path, d.KeepID).Scan(&duplicateIDs).Error; err != nil {
			return err
		}
		if len(duplicateIDs) == 0 {
			continue
		}

		if err := db.Exec(`
			INSERT INTO video_tags(video_id, tag_id)
			SELECT ?, tag_id FROM video_tags WHERE video_id IN ?
			ON CONFLICT DO NOTHING
		`, d.KeepID, duplicateIDs).Error; err != nil {
			return err
		}
		if err := db.Exec(`DELETE FROM video_tags WHERE video_id IN ?`, duplicateIDs).Error; err != nil {
			return err
		}
		if err := db.Unscoped().Where("id IN ?", duplicateIDs).Delete(&models.Video{}).Error; err != nil {
			return err
		}
	}

	return nil
}

func cleanupReimportedSoftDeletedVideos(db *gorm.DB) error {
	type reimportedPath struct {
		Path string
	}

	var paths []reimportedPath
	if err := db.Raw(`
		SELECT active.path
		FROM videos active
		WHERE active.deleted_at IS NULL AND active.path <> ''
		  AND EXISTS (
			SELECT 1
			FROM videos deleted
			WHERE deleted.path = active.path
			  AND deleted.deleted_at IS NOT NULL
		  )
		GROUP BY active.path
	`).Scan(&paths).Error; err != nil {
		return err
	}

	for _, item := range paths {
		if err := db.Exec(`
			UPDATE videos
			SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
			WHERE path = ? AND deleted_at IS NULL
		`, item.Path).Error; err != nil {
			return err
		}
		log.Printf("清理软删除后重导入的视频 path=%s", item.Path)
	}
	return nil
}

func ensureVideoPathUniqueIndex(db *gorm.DB) error {
	return db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_videos_path_active
		ON videos(path)
		WHERE deleted_at IS NULL AND path <> ''
	`).Error
}

func ensureMediaDetailConstraints(db *gorm.DB) error {
	statements := []string{
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_videos_personal_rating') THEN
				ALTER TABLE videos ADD CONSTRAINT chk_videos_personal_rating
				CHECK (personal_rating IS NULL OR (
					personal_rating >= 0 AND personal_rating <= 10 AND
					personal_rating * 2 = CAST(personal_rating * 2 AS INTEGER)
				));
			END IF;
		END $$`,
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_collection_videos_position') THEN
				ALTER TABLE collection_videos ADD CONSTRAINT chk_collection_videos_position CHECK (position > 0);
			END IF;
		END $$`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_media_collections_name_active
		 ON media_collections(normalized_name) WHERE deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_media_streams_video_index
		 ON media_streams(video_id, stream_index)`,
		`CREATE INDEX IF NOT EXISTS idx_videos_rating_active
		 ON videos(personal_rating DESC, id DESC) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_video_people_person_video
		 ON video_people(person_id, video_id)`,
		`CREATE INDEX IF NOT EXISTS idx_collection_videos_collection_position
		 ON collection_videos(collection_id, position, video_id)`,
		`CREATE INDEX IF NOT EXISTS idx_collection_videos_video
		 ON collection_videos(video_id, collection_id)`,
		`CREATE INDEX IF NOT EXISTS idx_media_streams_video_type
		 ON media_streams(video_id, stream_type)`,
		`CREATE INDEX IF NOT EXISTS idx_video_perceptual_hashes_source
		 ON video_perceptual_hashes(source_size, source_mod_time_ns)`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

func ensureCoreQueryIndexes(db *gorm.DB) {
	statements := []string{
		`CREATE INDEX IF NOT EXISTS idx_videos_directory_active ON videos(directory) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_videos_size_active ON videos(size) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_videos_height_active ON videos(height) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_videos_stale_active ON videos(is_stale) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_videos_score_inputs_active ON videos(play_count, random_play_count, size, id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_videos_favorite_active ON videos(is_favorite, id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_videos_watched_active ON videos(is_watched, id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_videos_watch_progress_active ON videos(watch_position_seconds, id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_videos_last_played_active ON videos(last_played_at DESC, id DESC) WHERE deleted_at IS NULL AND last_played_at IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_video_tags_tag_video ON video_tags(tag_id, video_id)`,
		`CREATE INDEX IF NOT EXISTS idx_video_tags_video_tag ON video_tags(video_id, tag_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_saved_library_views_name_active ON saved_library_views(name) WHERE deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_enhancement_tasks_active_source ON video_enhancement_tasks(video_id) WHERE status IN ('queued', 'running', 'cancel_requested')`,
		`CREATE INDEX IF NOT EXISTS idx_enhancement_tasks_queue ON video_enhancement_tasks(created_at, id) WHERE status IN ('queued', 'running', 'cancel_requested')`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			log.Printf("创建查询索引失败: %v sql=%s", err, statement)
		}
	}
}

func ensureAITaggingIndexes(db *gorm.DB) {
	if err := db.Exec(`DROP INDEX IF EXISTS idx_ai_tag_candidate_unique_pending`).Error; err != nil {
		log.Printf("删除旧 AI 标签唯一索引失败: %v", err)
	}
	statements := []string{
		`CREATE INDEX IF NOT EXISTS idx_ai_tag_candidates_video_status ON ai_tag_candidates(video_id, status)`,
		`CREATE INDEX IF NOT EXISTS idx_ai_tag_candidates_matched_status ON ai_tag_candidates(matched_tag_id, status)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_ai_tag_approval_video_tag ON ai_tag_approval_records(video_id, tag_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_ai_tag_approval_records_candidate_id ON ai_tag_approval_records(candidate_id)`,
		`CREATE INDEX IF NOT EXISTS idx_ai_tagging_states_status_processed ON ai_tagging_states(status, last_processed_at)`,
		`CREATE INDEX IF NOT EXISTS idx_ai_tag_agent_steps_video_created ON ai_tag_agent_steps(video_id, created_at)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_ai_tag_agent_step_run_round ON ai_tag_agent_steps(video_id, evidence_fingerprint, attempt, round)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_video_visual_fingerprints_video_id ON video_visual_fingerprints(video_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_video_same_source_pair ON video_same_source_relations(video_a_id, video_b_id)`,
		`CREATE INDEX IF NOT EXISTS idx_video_same_source_unread ON video_same_source_relations(status, is_unread, updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_video_same_source_a_status ON video_same_source_relations(video_a_id, status)`,
		`CREATE INDEX IF NOT EXISTS idx_video_same_source_b_status ON video_same_source_relations(video_b_id, status)`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			log.Printf("创建 AI 标签索引失败: %v sql=%s", err, statement)
		}
	}
}

func ensureShortFeedIndexes(db *gorm.DB) {
	statements := []string{
		`CREATE INDEX IF NOT EXISTS idx_short_feed_interactions_favorited_video ON short_feed_interactions(favorited, video_id)`,
		`CREATE INDEX IF NOT EXISTS idx_short_feed_interactions_liked_video ON short_feed_interactions(liked, video_id)`,
		`CREATE INDEX IF NOT EXISTS idx_short_feed_tag_preferences_score ON short_feed_tag_preferences(score)`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			log.Printf("创建短视频 Feed 索引失败: %v sql=%s", err, statement)
		}
	}
}

func ensureSubtitleSearchIndexes(db *gorm.DB) {
	if err := db.Exec(`CREATE EXTENSION IF NOT EXISTS pg_trgm`).Error; err != nil {
		log.Printf("创建 pg_trgm 扩展失败，字幕搜索仍可用但模糊搜索索引不可用: %v", err)
		return
	}
	if err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_subtitle_segments_text_trgm
		ON subtitle_segments
		USING GIN (LOWER(text) gin_trgm_ops)
	`).Error; err != nil {
		log.Printf("创建字幕模糊搜索索引失败，字幕搜索仍可用但可能较慢: %v", err)
	}
}

// Close 关闭数据库连接
func Close() error {
	if DB == nil {
		return nil
	}
	sqlDB, err := DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
