//! PostgreSQL compatibility foundation for CineInsight.

use std::{
    collections::{BTreeMap, BTreeSet},
    ffi::OsStr,
    fs,
    hash::{Hash, Hasher},
    path::{Path, PathBuf},
    time::{Duration, SystemTime},
};

use cine_api::{AITaggingStatusSummary, RandomCandidateResponse, VideoListResponse};
use cine_domain::{clamp_play_weight, video_score, VideoCursor, VideoSummary, VideoTagSummary};
use rand::Rng;
use serde::{Deserialize, Serialize};
use sqlx::{postgres::PgPoolOptions, PgPool, Row};
use thiserror::Error;

const DEFAULT_VIDEO_EXTENSIONS: &[&str] = &[
    ".mp4", ".avi", ".mkv", ".mov", ".wmv", ".flv", ".webm", ".m4v", ".ts", ".3gp", ".mpg",
    ".mpeg", ".rm", ".rmvb", ".vob", ".divx", ".f4v", ".asf", ".qt",
];

const DEFAULT_TRASH_DIR_NAME: &str = "trash";
const RECENT_ACTIVE_FILE_THRESHOLD: Duration = Duration::from_secs(5 * 60);
const TEMP_VIDEO_STEM_SUFFIXES: &[&str] = &[".temp", "_temp", "-temp", ".tmp", "_tmp", "-tmp"];
pub const DEFAULT_AUTO_SCAN_INTERVAL_SECONDS: i32 = 12 * 60 * 60;

const PREVIEW_MEDIA_ROUTE_PREFIX: &str = "/preview/media/";
const SHORT_FEED_INLINE_MIMES: &[(&str, &str)] = &[
    (".mp4", "video/mp4"),
    (".m4v", "video/x-m4v"),
    (".webm", "video/webm"),
    (".ogv", "video/ogg"),
    (".ogg", "video/ogg"),
];
const TAG_COLOR_PALETTE: &[&str] = &[
    "#0D9488", "#3b82f6", "#ef4444", "#10b981", "#f59e0b", "#8b5cf6", "#ec4899", "#06b6d4",
    "#f97316", "#6366f1", "#14b8a6", "#e11d48", "#84cc16",
];

pub const REQUIRED_LEGACY_TABLES: &[&str] = &[
    "videos",
    "subtitle_segments",
    "subtitle_index_states",
    "tags",
    "ai_tag_candidates",
    "ai_tag_approval_records",
    "ai_tagging_states",
    "short_feed_interactions",
    "short_feed_tag_preferences",
    "settings",
    "scan_directories",
    "video_tags",
];

pub const REQUIRED_LEGACY_INDEXES: &[&str] = &[
    "idx_videos_path_active",
    "idx_videos_directory_active",
    "idx_videos_size_active",
    "idx_videos_height_active",
    "idx_videos_stale_active",
    "idx_videos_score_inputs_active",
    "idx_video_tags_tag_video",
    "idx_video_tags_video_tag",
    "idx_ai_tag_candidates_video_status",
    "idx_ai_tag_candidates_matched_status",
    "idx_ai_tag_approval_video_tag",
    "idx_ai_tag_approval_records_candidate_id",
    "idx_ai_tagging_states_status_processed",
    "idx_short_feed_interactions_favorited_video",
    "idx_short_feed_interactions_liked_video",
    "idx_short_feed_tag_preferences_score",
];

pub const REQUIRED_LEGACY_EXTENSIONS: &[&str] = &[];

pub const OPTIONAL_LEGACY_INDEXES: &[&str] = &["idx_subtitle_segments_text_trgm"];

pub const OPTIONAL_LEGACY_EXTENSIONS: &[&str] = &["pg_trgm"];

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PgConfig {
    pub host: String,
    pub user: String,
    pub database: String,
    pub port: u16,
    pub password: Option<String>,
    pub sslmode: String,
    pub timezone: Option<String>,
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum PgConfigError {
    #[error("{0} cannot be empty")]
    Missing(&'static str),
    #[error("PG_PORT must be a valid u16: {0}")]
    InvalidPort(String),
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Serialize)]
pub struct VideoFilter {
    pub keyword: Option<String>,
    #[serde(default)]
    pub tag_ids: Vec<i64>,
    pub min_size: Option<i64>,
    pub max_size: Option<i64>,
    pub min_height: Option<i32>,
    pub max_height: Option<i32>,
    pub cursor: Option<VideoCursor>,
    pub limit: Option<i64>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct ScannedFile {
    pub path: String,
    pub size: i64,
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct MediaMetadata {
    pub duration: f64,
    pub resolution: String,
    pub width: i32,
    pub height: i32,
}

#[derive(Clone, Debug, Default, Eq, PartialEq, Serialize)]
pub struct ScanSyncResult {
    pub directories: usize,
    pub scanned: usize,
    pub added: usize,
    pub deleted: usize,
    pub relocated: usize,
    pub metadata_refreshed: usize,
    pub skipped: usize,
    pub errors: Vec<ScanSyncError>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct ScanSyncError {
    pub operation: String,
    pub directory: Option<String>,
    pub path: Option<String>,
    pub error: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "kebab-case")]
pub enum PreviewMode {
    Inline,
    ExternalPreview,
    Unsupported,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct PreviewSourceDescriptor {
    pub locator_strategy: String,
    pub locator_value: String,
    pub mime: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct PreviewExternalAction {
    pub action_id: String,
    pub button_label: String,
    pub hint: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct PreviewSession {
    pub video_id: i64,
    pub mode: PreviewMode,
    pub display_name: String,
    pub inline_source: Option<PreviewSourceDescriptor>,
    pub external_action: Option<PreviewExternalAction>,
    pub reason_code: Option<String>,
    pub reason_message: Option<String>,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct PlaybackAttemptResponse {
    pub video: Option<VideoSummary>,
    pub dispatch_succeeded: bool,
    pub user_message: Option<String>,
    pub reason_code: Option<String>,
    pub reconcile_result: Option<PlaybackReconcileResult>,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct PlaybackReconcileResult {
    pub video_id: i64,
    pub did_mark_stale: bool,
    pub did_relocate: bool,
    pub did_refresh_metadata: bool,
    pub needs_reload: bool,
    pub updated_video: Option<VideoSummary>,
    pub reason_code: Option<String>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct TagRecord {
    pub id: i64,
    pub name: String,
    pub color: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct ScanDirectoryRecord {
    pub id: i64,
    pub path: String,
    pub alias: String,
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct PublicSettings {
    pub confirm_before_delete: bool,
    pub delete_original_file: bool,
    pub video_extensions: String,
    pub play_weight: f64,
    pub auto_scan_on_startup: bool,
    pub auto_scan_interval_seconds: i32,
    pub short_feed_max_duration_minutes: i32,
    pub theme: String,
    pub log_enabled: bool,
    pub bilingual_enabled: bool,
    pub bilingual_lang: String,
    pub deepl_api_key_configured: bool,
    pub ai_tagging_base_url: String,
    pub ai_tagging_api_key_configured: bool,
    pub ai_tagging_model: String,
    pub ai_tagging_frame_count: i32,
    pub ai_tagging_subtitle_char_limit: i32,
    pub ai_tagging_startup_batch_size: i32,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SubtitleGenerationSettings {
    pub bilingual_enabled: bool,
    pub bilingual_lang: String,
    pub deepl_api_key: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct SubtitleSegmentRecord {
    pub index: i32,
    pub start_time_ms: i64,
    pub end_time_ms: i64,
    pub text: String,
    pub lines: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct SubtitleIndexStateRecord {
    pub video_id: i64,
    pub subtitle_path: String,
    pub subtitle_mod_time: i64,
    pub subtitle_size: i64,
    pub segment_count: i32,
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct SubtitleSearchMatch {
    pub video: VideoSummary,
    pub segment: SubtitleSegmentRecord,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub enum AITagCandidateStatus {
    Pending,
    Approved,
    Rejected,
    Superseded,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AITagCandidateInput {
    pub video_id: i64,
    pub suggested_name: String,
    pub normalized_name: String,
    pub matched_tag_id: Option<i64>,
    pub confidence: String,
    pub reasoning: String,
    pub source_summary: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct AITagCandidateRecord {
    pub id: i64,
    pub video_id: i64,
    pub suggested_name: String,
    pub normalized_name: String,
    pub matched_tag_id: Option<i64>,
    pub confidence: String,
    pub reasoning: String,
    pub source_summary: String,
    pub status: AITagCandidateStatus,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ShortFeedFeedback {
    pub liked: Option<bool>,
    pub favorited: Option<bool>,
    pub viewed: bool,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct ShortFeedInteractionRecord {
    pub video_id: i64,
    pub liked: bool,
    pub favorited: bool,
    pub view_count: i32,
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct ShortFeedVideoRecord {
    pub id: i64,
    pub name: String,
    pub path: String,
    pub duration: f64,
    pub width: i32,
    pub height: i32,
    pub tags: Vec<VideoTagSummary>,
    pub media_url: String,
    pub media_mime: String,
    pub liked: bool,
    pub favorited: bool,
    pub reason_code: String,
    pub reason_message: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct CleanupDuplicateGroup {
    pub original_id: i64,
    pub candidate_ids: Vec<i64>,
    pub reason: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct CleanupAnalysisRecord {
    pub duplicate_groups: Vec<CleanupDuplicateGroup>,
    pub low_duration_ids: Vec<i64>,
    pub low_resolution_ids: Vec<i64>,
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct DiagnosticsSnapshot {
    pub video_count: i64,
    pub tag_count: i64,
    pub subtitle_segment_count: i64,
    pub ai_candidate_count: i64,
    pub short_feed_interaction_count: i64,
    pub redacted_settings: PublicSettings,
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct SettingsUpdate {
    pub confirm_before_delete: bool,
    pub delete_original_file: bool,
    pub video_extensions: String,
    pub play_weight: f64,
    pub auto_scan_on_startup: bool,
    pub auto_scan_interval_seconds: i32,
    pub short_feed_max_duration_minutes: i32,
    pub theme: String,
    pub log_enabled: bool,
    pub bilingual_enabled: bool,
    pub bilingual_lang: String,
    pub deepl_api_key: String,
    pub ai_tagging_base_url: String,
    pub ai_tagging_api_key: String,
    pub ai_tagging_model: String,
    pub ai_tagging_frame_count: i32,
    pub ai_tagging_subtitle_char_limit: i32,
    pub ai_tagging_startup_batch_size: i32,
}

impl Default for SettingsUpdate {
    fn default() -> Self {
        Self {
            confirm_before_delete: false,
            delete_original_file: false,
            video_extensions: String::new(),
            play_weight: 2.0,
            auto_scan_on_startup: false,
            auto_scan_interval_seconds: DEFAULT_AUTO_SCAN_INTERVAL_SECONDS,
            short_feed_max_duration_minutes: 5,
            theme: "system".to_string(),
            log_enabled: false,
            bilingual_enabled: false,
            bilingual_lang: "zh".to_string(),
            deepl_api_key: String::new(),
            ai_tagging_base_url: String::new(),
            ai_tagging_api_key: String::new(),
            ai_tagging_model: String::new(),
            ai_tagging_frame_count: 5,
            ai_tagging_subtitle_char_limit: 4000,
            ai_tagging_startup_batch_size: 10,
        }
    }
}

#[derive(Clone, Debug, Eq, Ord, PartialEq, PartialOrd)]
struct ScanFileFingerprint {
    name: String,
    size: i64,
}

impl VideoFilter {
    fn normalized(self) -> Self {
        Self {
            min_size: positive_i64(self.min_size),
            max_size: positive_i64(self.max_size),
            min_height: positive_i32(self.min_height),
            max_height: positive_i32(self.max_height),
            ..self
        }
    }
}

fn positive_i64(value: Option<i64>) -> Option<i64> {
    value.filter(|value| *value > 0)
}

fn positive_i32(value: Option<i32>) -> Option<i32> {
    value.filter(|value| *value > 0)
}

pub fn normalize_auto_scan_interval_seconds(value: i32) -> i32 {
    if value > 0 {
        value
    } else {
        DEFAULT_AUTO_SCAN_INTERVAL_SECONDS
    }
}

async fn ensure_settings_schema(pool: &PgPool) -> Result<(), LibraryManagementError> {
    sqlx::query(
        r#"
        ALTER TABLE settings
        ADD COLUMN IF NOT EXISTS auto_scan_interval_seconds INTEGER NOT NULL DEFAULT 43200
        "#,
    )
    .execute(pool)
    .await
    .map_err(|_| LibraryManagementError::DatabaseWrite)?;
    Ok(())
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum VideoQueryError {
    #[error("NATIVE_TEST_DATABASE_URL must point to a scratch PostgreSQL database")]
    MissingNativeTestDatabaseUrl,
    #[error("database query failed: {0}")]
    Sql(String),
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum VideoMutationError {
    #[error("video file path is empty")]
    EmptyPath,
    #[error("scan root is not a directory")]
    NotDirectory,
    #[error("video file does not exist")]
    FileMissing,
    #[error("path is not a file")]
    NotFile,
    #[error("path is not a supported video file")]
    NotVideoFile,
    #[error("video already exists")]
    VideoExists,
    #[error("video was not found")]
    VideoNotFound,
    #[error("target file already exists")]
    TargetExists,
    #[error("file name is invalid")]
    InvalidFileName,
    #[error("filesystem operation failed")]
    Filesystem,
    #[error("database write failed")]
    DatabaseWrite,
    #[error("metadata is unavailable")]
    MetadataUnavailable,
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum PlaybackError {
    #[error("dispatch failed: {0}")]
    DispatchFailed(String),
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum TagMutationError {
    #[error("tag already exists")]
    TagExists,
    #[error("tag was not found")]
    TagNotFound,
    #[error("database write failed")]
    DatabaseWrite,
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum LibraryManagementError {
    #[error("record was not found")]
    NotFound,
    #[error("database write failed")]
    DatabaseWrite,
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum NativeSliceError {
    #[error("record was not found")]
    NotFound,
    #[error("subtitle parse failed")]
    SubtitleParse,
    #[error("filesystem operation failed")]
    Filesystem,
    #[error("database write failed")]
    DatabaseWrite,
}

pub trait PlaybackDispatch {
    fn dispatch(&mut self, path: &Path) -> Result<(), PlaybackError>;
}

impl From<sqlx::Error> for VideoQueryError {
    fn from(value: sqlx::Error) -> Self {
        Self::Sql(value.to_string())
    }
}

pub fn load_native_test_database_url() -> Result<String, VideoQueryError> {
    native_test_database_url_from_value(std::env::var("NATIVE_TEST_DATABASE_URL").ok())
}

pub fn native_test_database_url_from_value(
    value: Option<String>,
) -> Result<String, VideoQueryError> {
    value
        .map(|value| value.trim().to_string())
        .filter(|value| !value.is_empty())
        .ok_or(VideoQueryError::MissingNativeTestDatabaseUrl)
}

pub fn load_pg_config_from_env() -> Result<PgConfig, PgConfigError> {
    let host = required_env("PG_HOST")?;
    let user = required_env("PG_USER")?;
    let database = required_env("PG_DB")?;
    let port_text = std::env::var("PG_PORT").unwrap_or_else(|_| "5432".to_string());
    let port = port_text
        .parse::<u16>()
        .map_err(|_| PgConfigError::InvalidPort(port_text.clone()))?;

    Ok(PgConfig {
        host,
        user,
        database,
        port,
        password: optional_env("PG_PASSWORD"),
        sslmode: optional_env("PG_SSLMODE").unwrap_or_else(|| "disable".to_string()),
        timezone: optional_env("PG_TIMEZONE"),
    })
}

fn required_env(name: &'static str) -> Result<String, PgConfigError> {
    std::env::var(name)
        .ok()
        .map(|value| value.trim().to_string())
        .filter(|value| !value.is_empty())
        .ok_or(PgConfigError::Missing(name))
}

fn optional_env(name: &'static str) -> Option<String> {
    std::env::var(name)
        .ok()
        .map(|value| value.trim().to_string())
        .filter(|value| !value.is_empty())
}

pub async fn list_videos(
    pool: &PgPool,
    filter: VideoFilter,
) -> Result<VideoListResponse, VideoQueryError> {
    let play_weight = load_play_weight(pool).await?;
    let filter = filter.normalized();
    let limit = filter.limit.unwrap_or(50).clamp(1, 200);
    let fetch_limit = limit + 1;

    let rows = fetch_video_rows(pool, &filter, play_weight, fetch_limit).await?;
    let mut videos = rows
        .into_iter()
        .map(|row| map_video_row(row, play_weight))
        .collect::<Result<Vec<_>, _>>()?;

    for video in &mut videos {
        video.tags = load_video_tags(pool, video.id).await?;
    }

    let has_more = videos.len() > limit as usize;
    if has_more {
        videos.truncate(limit as usize);
    }
    let next_cursor = if has_more {
        videos.last().map(|video| VideoCursor {
            score: video.score,
            size: video.size,
            id: video.id,
        })
    } else {
        None
    };

    Ok(VideoListResponse {
        videos,
        next_cursor,
    })
}

pub async fn random_candidate(pool: &PgPool) -> Result<RandomCandidateResponse, VideoQueryError> {
    let sample = rand::rng().random_range(0.0..1.0);
    random_candidate_with_sample(pool, sample).await
}

pub async fn random_candidate_with_sample(
    pool: &PgPool,
    sample: f64,
) -> Result<RandomCandidateResponse, VideoQueryError> {
    let page = list_videos(
        pool,
        VideoFilter {
            limit: Some(200),
            ..VideoFilter::default()
        },
    )
    .await?;

    Ok(match choose_weighted_candidate(page.videos, sample) {
        Some(video) => RandomCandidateResponse {
            video: Some(video),
            reason_code: None,
            user_message: None,
        },
        None => RandomCandidateResponse {
            video: None,
            reason_code: Some("no_videos".to_string()),
            user_message: Some("随机播放失败：当前没有可播放的视频记录。".to_string()),
        },
    })
}

pub fn scan_directory_with_extensions(
    root: impl AsRef<Path>,
    extensions: &str,
) -> Result<Vec<ScannedFile>, VideoMutationError> {
    let root = clean_path(root.as_ref())?;
    let metadata = fs::metadata(&root).map_err(|_| VideoMutationError::FileMissing)?;
    if !metadata.is_dir() {
        return Err(VideoMutationError::NotDirectory);
    }

    let extensions = parse_video_extensions(extensions);
    let mut files = Vec::new();
    scan_directory_recursive(&root, &extensions, &mut files)?;
    files.sort_by(|left, right| left.path.cmp(&right.path));
    Ok(files)
}

pub async fn add_video(
    pool: &PgPool,
    path: impl AsRef<Path>,
) -> Result<VideoSummary, VideoMutationError> {
    let path = clean_path(path.as_ref())?;
    let metadata = fs::metadata(&path).map_err(|_| VideoMutationError::FileMissing)?;
    if !metadata.is_file() {
        return Err(VideoMutationError::NotFile);
    }
    if is_known_non_video_source_path(&path) {
        return Err(VideoMutationError::NotVideoFile);
    }

    let path_text = path_to_string(&path);
    if any_video_id_by_path(pool, &path_text).await?.is_some() {
        return Err(VideoMutationError::VideoExists);
    }

    let directory = path
        .parent()
        .map(path_to_string)
        .ok_or(VideoMutationError::EmptyPath)?;
    let name = file_name_string(&path)?;
    let size = metadata.len() as i64;
    let row = sqlx::query(
        r#"
        INSERT INTO videos(name, path, directory, size, duration, resolution, width, height, is_stale)
        VALUES ($1, $2, $3, $4, 0, '', 0, 0, false)
        RETURNING id
        "#,
    )
    .bind(name)
    .bind(path_text)
    .bind(directory)
    .bind(size)
    .fetch_one(pool)
    .await
    .map_err(map_insert_error)?;
    let id: i64 = row
        .try_get("id")
        .map_err(|_| VideoMutationError::DatabaseWrite)?;

    active_video_by_id(pool, id).await
}

pub async fn delete_video(
    pool: &PgPool,
    id: i64,
    delete_file: bool,
) -> Result<(), VideoMutationError> {
    let video = active_video_by_id(pool, id).await?;

    if delete_file {
        move_file_to_trash(Path::new(&video.path))?;
    }

    let result = sqlx::query("UPDATE videos SET deleted_at = now(), updated_at = now() WHERE id = $1 AND deleted_at IS NULL")
        .bind(id)
        .execute(pool)
        .await
        .map_err(|_| VideoMutationError::DatabaseWrite)?;
    if result.rows_affected() == 0 {
        return Err(VideoMutationError::VideoNotFound);
    }

    Ok(())
}

pub async fn rename_video(
    pool: &PgPool,
    id: i64,
    new_name: &str,
) -> Result<VideoSummary, VideoMutationError> {
    let mut new_name = new_name.trim().to_string();
    if new_name.is_empty() || new_name.contains('/') || new_name.contains('\\') {
        return Err(VideoMutationError::InvalidFileName);
    }

    let video = active_video_by_id(pool, id).await?;
    let old_path = PathBuf::from(&video.path);
    if Path::new(&new_name).extension().is_none() {
        if let Some(ext) = old_path.extension().and_then(OsStr::to_str) {
            new_name.push('.');
            new_name.push_str(ext);
        }
    }

    let new_path = Path::new(&video.directory).join(&new_name);
    if old_path == new_path {
        return Ok(video);
    }
    if new_path.exists() {
        return Err(VideoMutationError::TargetExists);
    }

    fs::rename(&old_path, &new_path).map_err(|_| VideoMutationError::Filesystem)?;

    let new_path_text = path_to_string(&new_path);
    let result = sqlx::query(
        r#"
        UPDATE videos
        SET name = $1, path = $2, directory = $3, updated_at = now()
        WHERE id = $4 AND deleted_at IS NULL
        "#,
    )
    .bind(&new_name)
    .bind(&new_path_text)
    .bind(path_to_string(
        new_path.parent().ok_or(VideoMutationError::EmptyPath)?,
    ))
    .bind(id)
    .execute(pool)
    .await;

    if result.is_err() {
        let _ = fs::rename(&new_path, &old_path);
        return Err(VideoMutationError::DatabaseWrite);
    }

    active_video_by_id(pool, id).await
}

pub async fn relocate_video(
    pool: &PgPool,
    id: i64,
    new_path: impl AsRef<Path>,
) -> Result<VideoSummary, VideoMutationError> {
    let new_path = clean_path(new_path.as_ref())?;
    let metadata = fs::metadata(&new_path).map_err(|_| VideoMutationError::FileMissing)?;
    if !metadata.is_file() {
        return Err(VideoMutationError::NotFile);
    }

    let new_path_text = path_to_string(&new_path);
    if let Some(existing_id) = active_video_id_by_path(pool, &new_path_text).await? {
        if existing_id != id {
            return Err(VideoMutationError::VideoExists);
        }
    }

    let result = sqlx::query(
        r#"
        UPDATE videos
        SET name = $1,
            path = $2,
            directory = $3,
            size = $4,
            duration = 0,
            resolution = '',
            width = 0,
            height = 0,
            is_stale = false,
            updated_at = now()
        WHERE id = $5 AND deleted_at IS NULL
        "#,
    )
    .bind(file_name_string(&new_path)?)
    .bind(new_path_text)
    .bind(path_to_string(
        new_path.parent().ok_or(VideoMutationError::EmptyPath)?,
    ))
    .bind(metadata.len() as i64)
    .bind(id)
    .execute(pool)
    .await
    .map_err(|_| VideoMutationError::DatabaseWrite)?;
    if result.rows_affected() == 0 {
        return Err(VideoMutationError::VideoNotFound);
    }

    active_video_by_id(pool, id).await
}

pub async fn list_videos_by_directory(
    pool: &PgPool,
    directory: impl AsRef<Path>,
) -> Result<Vec<VideoSummary>, VideoQueryError> {
    let root = directory.as_ref().to_string_lossy().trim().to_string();
    if root.is_empty() || root == "." {
        return Ok(Vec::new());
    }
    let root = path_to_string(&PathBuf::from(root));
    let prefix = format!("{root}{}", std::path::MAIN_SEPARATOR);
    let play_weight = load_play_weight(pool).await?;
    let rows = sqlx::query(
        r#"
        SELECT
            v.id,
            v.name,
            v.path,
            v.directory,
            v.size,
            COALESCE(v.duration, 0) AS duration,
            COALESCE(v.resolution, '') AS resolution,
            COALESCE(v.width, 0) AS width,
            COALESCE(v.height, 0) AS height,
            COALESCE(v.is_stale, false) AS is_stale,
            COALESCE(v.play_count, 0) AS play_count,
            COALESCE(v.random_play_count, 0) AS random_play_count,
            v.last_played_at::text AS last_played_at,
            v.created_at::text AS created_at,
            v.updated_at::text AS updated_at,
            (COALESCE(v.play_count, 0) * $1::float8 + COALESCE(v.random_play_count, 0)) AS score
        FROM videos v
        WHERE v.deleted_at IS NULL
          AND (v.directory = $2 OR v.directory LIKE ($3 || '%'))
        ORDER BY v.id DESC
        "#,
    )
    .bind(play_weight)
    .bind(&root)
    .bind(&prefix)
    .fetch_all(pool)
    .await?;

    let mut videos = Vec::new();
    for row in rows {
        let mut video = map_video_row(row, play_weight)?;
        video.tags = load_video_tags(pool, video.id).await?;
        videos.push(video);
    }
    Ok(videos)
}

pub async fn refresh_video_metadata_with_probe<F>(
    pool: &PgPool,
    id: i64,
    probe: F,
) -> Result<VideoSummary, VideoMutationError>
where
    F: FnOnce(&Path) -> Option<MediaMetadata>,
{
    let video = active_video_by_id(pool, id).await?;
    let metadata = probe(Path::new(&video.path)).ok_or(VideoMutationError::MetadataUnavailable)?;
    if metadata.duration == 0.0 && metadata.resolution.is_empty() {
        return Err(VideoMutationError::MetadataUnavailable);
    }

    update_video_metadata(pool, id, &metadata).await?;
    active_video_by_id(pool, id).await
}

pub async fn open_video_directory_with_dispatch<D>(
    pool: &PgPool,
    id: i64,
    dispatch: &mut D,
) -> Result<(), VideoMutationError>
where
    D: PlaybackDispatch,
{
    let video = active_video_by_id(pool, id).await?;
    dispatch
        .dispatch(Path::new(&video.directory))
        .map_err(|_| VideoMutationError::Filesystem)
}

pub async fn sync_scan_directories_with_probe<F>(
    pool: &PgPool,
    roots: &[PathBuf],
    extensions: &str,
    probe: F,
) -> Result<ScanSyncResult, VideoMutationError>
where
    F: Fn(&Path) -> Option<MediaMetadata>,
{
    let mut result = ScanSyncResult::default();
    let mut clean_roots = Vec::new();
    let mut scanned_by_path = BTreeMap::<String, ScannedFile>::new();

    for root in roots {
        result.directories += 1;
        let scanned = match scan_directory_with_extensions(root, extensions) {
            Ok(scanned) => scanned,
            Err(error) => {
                result.record_error("scan", Some(path_to_string(root)), None, &error);
                continue;
            }
        };
        clean_roots.push(clean_path(root)?);
        result.scanned += scanned.len();
        for file in scanned {
            scanned_by_path.insert(file.path.clone(), file);
        }
    }

    let active_videos = active_videos_under_roots(pool, &clean_roots).await?;
    let mut existing_by_path = BTreeMap::<String, VideoSummary>::new();
    let mut missing_videos = Vec::<VideoSummary>::new();
    for video in active_videos {
        if scanned_by_path.contains_key(&video.path) {
            if video.duration == 0.0 || video.resolution.is_empty() || video.height == 0 {
                match probe(Path::new(&video.path)) {
                    Some(metadata) => {
                        update_video_metadata(pool, video.id, &metadata).await?;
                        result.metadata_refreshed += 1;
                    }
                    None => result.record_error(
                        "refresh_metadata",
                        Some(video.directory.clone()),
                        Some(video.path.clone()),
                        &VideoMutationError::MetadataUnavailable,
                    ),
                }
            }
            existing_by_path.insert(video.path.clone(), video);
        } else {
            missing_videos.push(video);
        }
    }

    let mut new_files = scanned_by_path
        .values()
        .filter(|file| !existing_by_path.contains_key(&file.path))
        .cloned()
        .collect::<Vec<_>>();
    new_files.sort_by(|left, right| left.path.cmp(&right.path));

    let mut missing_by_fingerprint = BTreeMap::<ScanFileFingerprint, Vec<VideoSummary>>::new();
    for video in &missing_videos {
        missing_by_fingerprint
            .entry(fingerprint_video(video))
            .or_default()
            .push(video.clone());
    }
    let mut new_file_counts = BTreeMap::<ScanFileFingerprint, usize>::new();
    for file in &new_files {
        *new_file_counts
            .entry(fingerprint_scanned_file(file))
            .or_default() += 1;
    }

    let mut relocated_ids = BTreeSet::<i64>::new();
    let mut consumed_new_paths = BTreeSet::<String>::new();
    for file in &new_files {
        let key = fingerprint_scanned_file(file);
        let candidates = missing_by_fingerprint
            .get(&key)
            .cloned()
            .unwrap_or_default();
        if candidates.len() != 1 || new_file_counts.get(&key).copied().unwrap_or(0) != 1 {
            continue;
        }
        let video = &candidates[0];
        match relocate_video(pool, video.id, &file.path).await {
            Ok(_) => {
                result.relocated += 1;
                relocated_ids.insert(video.id);
                consumed_new_paths.insert(file.path.clone());
            }
            Err(error) => result.record_error(
                "relocate",
                Some(video.directory.clone()),
                Some(file.path.clone()),
                &error,
            ),
        }
    }

    for file in &new_files {
        if consumed_new_paths.contains(&file.path) {
            continue;
        }
        match add_video(pool, &file.path).await {
            Ok(_) => result.added += 1,
            Err(VideoMutationError::VideoExists) => result.skipped += 1,
            Err(error) => result.record_error(
                "add",
                Path::new(&file.path).parent().map(path_to_string),
                Some(file.path.clone()),
                &error,
            ),
        }
    }

    for video in &missing_videos {
        if relocated_ids.contains(&video.id) {
            continue;
        }
        match delete_video(pool, video.id, false).await {
            Ok(()) => result.deleted += 1,
            Err(error) => result.record_error(
                "delete",
                Some(video.directory.clone()),
                Some(video.path.clone()),
                &error,
            ),
        }
    }

    Ok(result)
}

pub async fn preview_session(pool: &PgPool, id: i64) -> Result<PreviewSession, VideoMutationError> {
    let video = active_video_by_id(pool, id).await?;
    let path = Path::new(&video.path);
    let metadata = match fs::metadata(path) {
        Ok(metadata) => metadata,
        Err(_) => {
            return Ok(PreviewSession {
                video_id: video.id,
                mode: PreviewMode::Unsupported,
                display_name: video.name,
                inline_source: None,
                external_action: None,
                reason_code: Some("file_missing".to_string()),
                reason_message: Some("源文件不存在，当前无法预览。".to_string()),
            })
        }
    };
    if metadata.is_dir() {
        return Ok(PreviewSession {
            video_id: video.id,
            mode: PreviewMode::Unsupported,
            display_name: video.name,
            inline_source: None,
            external_action: None,
            reason_code: Some("path_is_directory".to_string()),
            reason_message: Some("当前路径不是可预览的视频文件。".to_string()),
        });
    }

    if let Some(mime) = inline_preview_mime(path) {
        return Ok(PreviewSession {
            video_id: video.id,
            mode: PreviewMode::Inline,
            display_name: video.name,
            inline_source: Some(PreviewSourceDescriptor {
                locator_strategy: "asset_route".to_string(),
                locator_value: format!("{PREVIEW_MEDIA_ROUTE_PREFIX}{}", video.id),
                mime: mime.to_string(),
            }),
            external_action: None,
            reason_code: None,
            reason_message: None,
        });
    }

    Ok(PreviewSession {
        video_id: video.id,
        mode: PreviewMode::ExternalPreview,
        display_name: video.name,
        inline_source: None,
        external_action: Some(PreviewExternalAction {
            action_id: "preview_externally".to_string(),
            button_label: "使用系统播放器预览".to_string(),
            hint: "将使用系统播放器进行预览，不计正式播放统计，这不是正式播放。".to_string(),
        }),
        reason_code: Some("inline_not_supported".to_string()),
        reason_message: Some(
            "当前文件格式不适合在应用内稳定预览，可改用系统播放器预览。".to_string(),
        ),
    })
}

pub async fn preview_externally_with_dispatch<D>(
    pool: &PgPool,
    id: i64,
    dispatch: &mut D,
) -> Result<(), VideoMutationError>
where
    D: PlaybackDispatch,
{
    let video = active_video_by_id(pool, id).await?;
    dispatch
        .dispatch(Path::new(&video.path))
        .map_err(|_| VideoMutationError::Filesystem)
}

pub async fn play_video_with_dispatch<D>(
    pool: &PgPool,
    id: i64,
    random: bool,
    dispatch: &mut D,
) -> Result<PlaybackAttemptResponse, VideoMutationError>
where
    D: PlaybackDispatch,
{
    let video = active_video_by_id(pool, id).await?;
    let path = Path::new(&video.path);
    let metadata = match fs::metadata(path) {
        Ok(metadata) => metadata,
        Err(_) => {
            return playback_failure_with_stale(
                pool,
                video,
                "file_missing",
                "源文件不存在或已被移动。",
                true,
            )
            .await;
        }
    };
    if metadata.is_dir() {
        return playback_failure_with_stale(
            pool,
            video,
            "path_is_directory",
            "当前路径不是可播放文件。",
            true,
        )
        .await;
    }

    if let Err(error) = dispatch.dispatch(path) {
        return Ok(PlaybackAttemptResponse {
            video: Some(video.clone()),
            dispatch_succeeded: false,
            user_message: Some(format!(
                "播放失败: {} ({})\n原因: {}",
                video.name, video.path, error
            )),
            reason_code: Some("dispatch_failed".to_string()),
            reconcile_result: None,
        });
    }

    let counter_column = if random {
        "random_play_count"
    } else {
        "play_count"
    };
    let sql = format!(
        "UPDATE videos SET {counter_column} = {counter_column} + 1, last_played_at = now(), is_stale = false, updated_at = now() WHERE id = $1 AND deleted_at IS NULL"
    );
    sqlx::query(&sql)
        .bind(id)
        .execute(pool)
        .await
        .map_err(|_| VideoMutationError::DatabaseWrite)?;
    let updated = active_video_by_id(pool, id).await?;

    Ok(PlaybackAttemptResponse {
        video: Some(updated),
        dispatch_succeeded: true,
        user_message: None,
        reason_code: None,
        reconcile_result: None,
    })
}

fn parse_video_extensions(extensions: &str) -> BTreeSet<String> {
    let parsed = extensions
        .split(',')
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(|value| {
            if value.starts_with('.') {
                value.to_ascii_lowercase()
            } else {
                format!(".{}", value.to_ascii_lowercase())
            }
        })
        .collect::<BTreeSet<_>>();

    if parsed.is_empty() {
        DEFAULT_VIDEO_EXTENSIONS
            .iter()
            .map(|value| value.to_string())
            .collect()
    } else {
        parsed
    }
}

fn inline_preview_mime(path: &Path) -> Option<&'static str> {
    match path
        .extension()
        .and_then(OsStr::to_str)
        .map(|value| value.to_ascii_lowercase())
        .as_deref()
    {
        Some("mp4") => Some("video/mp4"),
        Some("m4v") => Some("video/x-m4v"),
        Some("webm") => Some("video/webm"),
        Some("ogv" | "ogg") => Some("video/ogg"),
        _ => None,
    }
}

async fn playback_failure_with_stale(
    pool: &PgPool,
    video: VideoSummary,
    reason_code: &str,
    detail: &str,
    should_mark_stale: bool,
) -> Result<PlaybackAttemptResponse, VideoMutationError> {
    let mut updated_video = video.clone();
    let mut did_mark_stale = false;
    if should_mark_stale {
        sqlx::query("UPDATE videos SET is_stale = true, updated_at = now() WHERE id = $1 AND deleted_at IS NULL")
            .bind(video.id)
            .execute(pool)
            .await
            .map_err(|_| VideoMutationError::DatabaseWrite)?;
        updated_video = active_video_by_id(pool, video.id).await?;
        did_mark_stale = updated_video.is_stale;
    }

    Ok(PlaybackAttemptResponse {
        video: Some(updated_video.clone()),
        dispatch_succeeded: false,
        user_message: Some(format!(
            "播放失败: {} ({})\n原因: {}",
            video.name, video.path, detail
        )),
        reason_code: Some(reason_code.to_string()),
        reconcile_result: Some(PlaybackReconcileResult {
            video_id: video.id,
            did_mark_stale,
            did_relocate: false,
            did_refresh_metadata: false,
            needs_reload: true,
            updated_video: Some(updated_video),
            reason_code: Some(reason_code.to_string()),
        }),
    })
}

pub struct SystemPlaybackDispatch;

impl PlaybackDispatch for SystemPlaybackDispatch {
    fn dispatch(&mut self, path: &Path) -> Result<(), PlaybackError> {
        let status = std::process::Command::new("open")
            .arg(path)
            .spawn()
            .map_err(|error| PlaybackError::DispatchFailed(error.to_string()))?;
        drop(status);
        Ok(())
    }
}

pub async fn list_tags(pool: &PgPool) -> Result<Vec<TagRecord>, TagMutationError> {
    let rows = sqlx::query(
        "SELECT id, name, COALESCE(color, '') AS color FROM tags WHERE deleted_at IS NULL ORDER BY name ASC, id ASC",
    )
    .fetch_all(pool)
    .await
    .map_err(|_| TagMutationError::DatabaseWrite)?;

    rows.into_iter()
        .map(|row| {
            Ok(TagRecord {
                id: row
                    .try_get("id")
                    .map_err(|_| TagMutationError::DatabaseWrite)?,
                name: row
                    .try_get("name")
                    .map_err(|_| TagMutationError::DatabaseWrite)?,
                color: row
                    .try_get("color")
                    .map_err(|_| TagMutationError::DatabaseWrite)?,
            })
        })
        .collect()
}

pub async fn create_tag(
    pool: &PgPool,
    name: &str,
    color: &str,
) -> Result<TagRecord, TagMutationError> {
    let name = name.trim();
    let color = color.trim();
    if active_tag_id_by_name(pool, name).await?.is_some() {
        return Err(TagMutationError::TagExists);
    }

    let color = if color.is_empty() {
        let count: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM tags")
            .fetch_one(pool)
            .await
            .map_err(|_| TagMutationError::DatabaseWrite)?;
        TAG_COLOR_PALETTE[count as usize % TAG_COLOR_PALETTE.len()].to_string()
    } else {
        color.to_string()
    };

    if let Some(id) = soft_deleted_tag_id_by_name(pool, name).await? {
        sqlx::query(
            "UPDATE tags SET color = $1, deleted_at = NULL, updated_at = now() WHERE id = $2",
        )
        .bind(&color)
        .bind(id)
        .execute(pool)
        .await
        .map_err(|_| TagMutationError::DatabaseWrite)?;
        return tag_by_id(pool, id).await;
    }

    let id: i64 = sqlx::query_scalar("INSERT INTO tags(name, color) VALUES ($1, $2) RETURNING id")
        .bind(name)
        .bind(&color)
        .fetch_one(pool)
        .await
        .map_err(map_tag_insert_error)?;
    tag_by_id(pool, id).await
}

pub async fn update_tag(
    pool: &PgPool,
    id: i64,
    name: &str,
    color: &str,
) -> Result<TagRecord, TagMutationError> {
    let name = name.trim();
    let color = color.trim();
    if active_tag_id_by_name(pool, name)
        .await?
        .is_some_and(|existing_id| existing_id != id)
    {
        return Err(TagMutationError::TagExists);
    }
    sqlx::query("DELETE FROM tags WHERE name = $1 AND deleted_at IS NOT NULL")
        .bind(name)
        .execute(pool)
        .await
        .map_err(|_| TagMutationError::DatabaseWrite)?;
    let result =
        sqlx::query("UPDATE tags SET name = $1, color = $2, updated_at = now() WHERE id = $3 AND deleted_at IS NULL")
            .bind(name)
            .bind(color)
            .bind(id)
            .execute(pool)
            .await
            .map_err(|_| TagMutationError::DatabaseWrite)?;
    if result.rows_affected() == 0 {
        return Err(TagMutationError::TagNotFound);
    }
    tag_by_id(pool, id).await
}

pub async fn delete_tag(pool: &PgPool, id: i64) -> Result<(), TagMutationError> {
    sqlx::query("DELETE FROM video_tags WHERE tag_id = $1")
        .bind(id)
        .execute(pool)
        .await
        .map_err(|_| TagMutationError::DatabaseWrite)?;
    let result = sqlx::query("UPDATE tags SET deleted_at = now(), updated_at = now() WHERE id = $1 AND deleted_at IS NULL")
        .bind(id)
        .execute(pool)
        .await
        .map_err(|_| TagMutationError::DatabaseWrite)?;
    if result.rows_affected() == 0 {
        return Err(TagMutationError::TagNotFound);
    }
    Ok(())
}

pub async fn assign_tag_to_video(
    pool: &PgPool,
    video_id: i64,
    tag_id: i64,
) -> Result<(), TagMutationError> {
    if active_tag_id_by_id(pool, tag_id).await?.is_none() {
        return Err(TagMutationError::TagNotFound);
    }
    sqlx::query("INSERT INTO video_tags(video_id, tag_id) VALUES ($1, $2) ON CONFLICT DO NOTHING")
        .bind(video_id)
        .bind(tag_id)
        .execute(pool)
        .await
        .map_err(|_| TagMutationError::DatabaseWrite)?;
    Ok(())
}

pub async fn remove_tag_from_video(
    pool: &PgPool,
    video_id: i64,
    tag_id: i64,
) -> Result<(), TagMutationError> {
    sqlx::query("DELETE FROM video_tags WHERE video_id = $1 AND tag_id = $2")
        .bind(video_id)
        .bind(tag_id)
        .execute(pool)
        .await
        .map_err(|_| TagMutationError::DatabaseWrite)?;
    Ok(())
}

pub async fn update_settings(
    pool: &PgPool,
    input: SettingsUpdate,
) -> Result<(), LibraryManagementError> {
    ensure_settings_schema(pool).await?;
    sqlx::query(
        r#"
        UPDATE settings SET
            confirm_before_delete = $1,
            delete_original_file = $2,
            video_extensions = $3,
            play_weight = $4,
            auto_scan_on_startup = $5,
            auto_scan_interval_seconds = $6,
            short_feed_max_duration_minutes = $7,
            theme = $8,
            log_enabled = $9,
            bilingual_enabled = $10,
            bilingual_lang = $11,
            deepl_api_key = COALESCE(NULLIF($12, ''), deepl_api_key),
            ai_tagging_base_url = $13,
            ai_tagging_api_key = COALESCE(NULLIF($14, ''), ai_tagging_api_key),
            ai_tagging_model = $15,
            ai_tagging_frame_count = $16,
            ai_tagging_subtitle_char_limit = $17,
            ai_tagging_startup_batch_size = $18,
            updated_at = now()
        WHERE id = 1
        "#,
    )
    .bind(input.confirm_before_delete)
    .bind(input.delete_original_file)
    .bind(input.video_extensions)
    .bind(input.play_weight)
    .bind(input.auto_scan_on_startup)
    .bind(normalize_auto_scan_interval_seconds(
        input.auto_scan_interval_seconds,
    ))
    .bind(positive_or_default(
        input.short_feed_max_duration_minutes,
        5,
    ))
    .bind(input.theme)
    .bind(input.log_enabled)
    .bind(input.bilingual_enabled)
    .bind(input.bilingual_lang)
    .bind(input.deepl_api_key)
    .bind(input.ai_tagging_base_url)
    .bind(input.ai_tagging_api_key)
    .bind(input.ai_tagging_model)
    .bind(positive_or_default(input.ai_tagging_frame_count, 5))
    .bind(positive_or_default(
        input.ai_tagging_subtitle_char_limit,
        4000,
    ))
    .bind(positive_or_default(input.ai_tagging_startup_batch_size, 10))
    .execute(pool)
    .await
    .map_err(|_| LibraryManagementError::DatabaseWrite)?;
    Ok(())
}

pub async fn get_public_settings(pool: &PgPool) -> Result<PublicSettings, LibraryManagementError> {
    ensure_settings_schema(pool).await?;
    let row = sqlx::query(
        r#"
        SELECT
            COALESCE(confirm_before_delete, false) AS confirm_before_delete,
            COALESCE(delete_original_file, false) AS delete_original_file,
            COALESCE(video_extensions, '') AS video_extensions,
            COALESCE(play_weight, 2.0) AS play_weight,
            COALESCE(auto_scan_on_startup, false) AS auto_scan_on_startup,
            COALESCE(auto_scan_interval_seconds, 43200) AS auto_scan_interval_seconds,
            COALESCE(short_feed_max_duration_minutes, 5) AS short_feed_max_duration_minutes,
            COALESCE(theme, 'system') AS theme,
            COALESCE(log_enabled, false) AS log_enabled,
            COALESCE(bilingual_enabled, false) AS bilingual_enabled,
            COALESCE(bilingual_lang, 'zh') AS bilingual_lang,
            COALESCE(deepl_api_key, '') AS deepl_api_key,
            COALESCE(ai_tagging_base_url, '') AS ai_tagging_base_url,
            COALESCE(ai_tagging_api_key, '') AS ai_tagging_api_key,
            COALESCE(ai_tagging_model, '') AS ai_tagging_model,
            COALESCE(ai_tagging_frame_count, 5) AS ai_tagging_frame_count,
            COALESCE(ai_tagging_subtitle_char_limit, 4000) AS ai_tagging_subtitle_char_limit,
            COALESCE(ai_tagging_startup_batch_size, 10) AS ai_tagging_startup_batch_size
        FROM settings
        ORDER BY id ASC
        LIMIT 1
        "#,
    )
    .fetch_one(pool)
    .await
    .map_err(|_| LibraryManagementError::NotFound)?;

    let deepl_api_key: String = row
        .try_get("deepl_api_key")
        .map_err(|_| LibraryManagementError::DatabaseWrite)?;
    let ai_tagging_api_key: String = row
        .try_get("ai_tagging_api_key")
        .map_err(|_| LibraryManagementError::DatabaseWrite)?;
    Ok(PublicSettings {
        confirm_before_delete: row
            .try_get("confirm_before_delete")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        delete_original_file: row
            .try_get("delete_original_file")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        video_extensions: row
            .try_get("video_extensions")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        play_weight: row
            .try_get("play_weight")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        auto_scan_on_startup: row
            .try_get("auto_scan_on_startup")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        auto_scan_interval_seconds: normalize_auto_scan_interval_seconds(
            row.try_get("auto_scan_interval_seconds")
                .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        ),
        short_feed_max_duration_minutes: row
            .try_get("short_feed_max_duration_minutes")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        theme: row
            .try_get("theme")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        log_enabled: row
            .try_get("log_enabled")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        bilingual_enabled: row
            .try_get("bilingual_enabled")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        bilingual_lang: row
            .try_get("bilingual_lang")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        deepl_api_key_configured: !deepl_api_key.trim().is_empty(),
        ai_tagging_base_url: row
            .try_get("ai_tagging_base_url")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        ai_tagging_api_key_configured: !ai_tagging_api_key.trim().is_empty(),
        ai_tagging_model: row
            .try_get("ai_tagging_model")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        ai_tagging_frame_count: row
            .try_get("ai_tagging_frame_count")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        ai_tagging_subtitle_char_limit: row
            .try_get("ai_tagging_subtitle_char_limit")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        ai_tagging_startup_batch_size: row
            .try_get("ai_tagging_startup_batch_size")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
    })
}

pub async fn get_subtitle_generation_settings(
    pool: &PgPool,
) -> Result<SubtitleGenerationSettings, LibraryManagementError> {
    let row = sqlx::query(
        r#"
        SELECT
            COALESCE(bilingual_enabled, false) AS bilingual_enabled,
            COALESCE(bilingual_lang, 'zh') AS bilingual_lang,
            COALESCE(deepl_api_key, '') AS deepl_api_key
        FROM settings
        ORDER BY id ASC
        LIMIT 1
        "#,
    )
    .fetch_one(pool)
    .await
    .map_err(|_| LibraryManagementError::NotFound)?;
    Ok(SubtitleGenerationSettings {
        bilingual_enabled: row
            .try_get("bilingual_enabled")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        bilingual_lang: row
            .try_get("bilingual_lang")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        deepl_api_key: row
            .try_get("deepl_api_key")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
    })
}

pub async fn list_scan_directories(
    pool: &PgPool,
) -> Result<Vec<ScanDirectoryRecord>, LibraryManagementError> {
    let rows = sqlx::query(
        "SELECT id, path, COALESCE(alias, '') AS alias FROM scan_directories WHERE deleted_at IS NULL ORDER BY created_at DESC, id DESC",
    )
    .fetch_all(pool)
    .await
    .map_err(|_| LibraryManagementError::DatabaseWrite)?;
    rows.into_iter()
        .map(|row| {
            Ok(ScanDirectoryRecord {
                id: row
                    .try_get("id")
                    .map_err(|_| LibraryManagementError::DatabaseWrite)?,
                path: row
                    .try_get("path")
                    .map_err(|_| LibraryManagementError::DatabaseWrite)?,
                alias: row
                    .try_get("alias")
                    .map_err(|_| LibraryManagementError::DatabaseWrite)?,
            })
        })
        .collect()
}

pub async fn add_scan_directory(
    pool: &PgPool,
    path: &str,
    alias: &str,
) -> Result<ScanDirectoryRecord, LibraryManagementError> {
    let id: i64 = sqlx::query_scalar(
        "INSERT INTO scan_directories(path, alias) VALUES ($1, $2) RETURNING id",
    )
    .bind(path)
    .bind(alias)
    .fetch_one(pool)
    .await
    .map_err(|_| LibraryManagementError::DatabaseWrite)?;
    scan_directory_by_id(pool, id).await
}

pub async fn update_scan_directory(
    pool: &PgPool,
    id: i64,
    path: &str,
    alias: &str,
) -> Result<ScanDirectoryRecord, LibraryManagementError> {
    let result = sqlx::query(
        "UPDATE scan_directories SET path = $1, alias = $2, updated_at = now() WHERE id = $3 AND deleted_at IS NULL",
    )
    .bind(path)
    .bind(alias)
    .bind(id)
    .execute(pool)
    .await
    .map_err(|_| LibraryManagementError::DatabaseWrite)?;
    if result.rows_affected() == 0 {
        return Err(LibraryManagementError::NotFound);
    }
    scan_directory_by_id(pool, id).await
}

pub async fn delete_scan_directory(pool: &PgPool, id: i64) -> Result<(), LibraryManagementError> {
    let result = sqlx::query(
        "UPDATE scan_directories SET deleted_at = now(), updated_at = now() WHERE id = $1 AND deleted_at IS NULL",
    )
    .bind(id)
    .execute(pool)
    .await
    .map_err(|_| LibraryManagementError::DatabaseWrite)?;
    if result.rows_affected() == 0 {
        return Err(LibraryManagementError::NotFound);
    }
    Ok(())
}

pub async fn index_subtitle_file(
    pool: &PgPool,
    video_id: i64,
    srt_path: impl AsRef<Path>,
) -> Result<SubtitleIndexStateRecord, NativeSliceError> {
    let srt_path = srt_path.as_ref();
    let content = fs::read_to_string(srt_path).map_err(|_| NativeSliceError::Filesystem)?;
    let segments = parse_srt(&content)?;
    let metadata = fs::metadata(srt_path).map_err(|_| NativeSliceError::Filesystem)?;
    let mod_time = metadata
        .modified()
        .ok()
        .and_then(|time| time.duration_since(SystemTime::UNIX_EPOCH).ok())
        .map(|duration| duration.as_nanos() as i64)
        .unwrap_or_default();
    let subtitle_path = srt_path.to_string_lossy().to_string();

    let mut tx = pool
        .begin()
        .await
        .map_err(|_| NativeSliceError::DatabaseWrite)?;
    sqlx::query("DELETE FROM subtitle_segments WHERE video_id = $1")
        .bind(video_id)
        .execute(&mut *tx)
        .await
        .map_err(|_| NativeSliceError::DatabaseWrite)?;

    let mut indexed_count = 0;
    for segment in segments {
        let text = segment.text.trim();
        if text.is_empty() {
            continue;
        }
        indexed_count += 1;
        sqlx::query(
            r#"
            INSERT INTO subtitle_segments(video_id, segment_index, start_time_ms, end_time_ms, text, subtitle_path, subtitle_mod_time)
            VALUES ($1, $2, $3, $4, $5, $6, $7)
            "#,
        )
        .bind(video_id)
        .bind(indexed_count)
        .bind(segment.start_time_ms)
        .bind(segment.end_time_ms)
        .bind(text)
        .bind(&subtitle_path)
        .bind(mod_time)
        .execute(&mut *tx)
        .await
        .map_err(|_| NativeSliceError::DatabaseWrite)?;
    }

    sqlx::query(
        r#"
        INSERT INTO subtitle_index_states(video_id, subtitle_path, subtitle_mod_time, subtitle_size, segment_count, last_checked_at, updated_at)
        VALUES ($1, $2, $3, $4, $5, now(), now())
        ON CONFLICT (video_id) DO UPDATE SET
            subtitle_path = EXCLUDED.subtitle_path,
            subtitle_mod_time = EXCLUDED.subtitle_mod_time,
            subtitle_size = EXCLUDED.subtitle_size,
            segment_count = EXCLUDED.segment_count,
            last_checked_at = now(),
            updated_at = now()
        "#,
    )
    .bind(video_id)
    .bind(&subtitle_path)
    .bind(mod_time)
    .bind(metadata.len() as i64)
    .bind(indexed_count)
    .execute(&mut *tx)
    .await
    .map_err(|_| NativeSliceError::DatabaseWrite)?;
    tx.commit()
        .await
        .map_err(|_| NativeSliceError::DatabaseWrite)?;

    Ok(SubtitleIndexStateRecord {
        video_id,
        subtitle_path,
        subtitle_mod_time: mod_time,
        subtitle_size: metadata.len() as i64,
        segment_count: indexed_count,
    })
}

pub async fn get_subtitle_segments(
    pool: &PgPool,
    video_id: i64,
) -> Result<Vec<SubtitleSegmentRecord>, NativeSliceError> {
    let rows = sqlx::query(
        r#"
        SELECT segment_index, start_time_ms, end_time_ms, text
        FROM subtitle_segments
        WHERE video_id = $1
        ORDER BY segment_index ASC
        "#,
    )
    .bind(video_id)
    .fetch_all(pool)
    .await
    .map_err(|_| NativeSliceError::DatabaseWrite)?;
    rows.into_iter().map(subtitle_segment_from_row).collect()
}

pub async fn subtitle_path_for_video(
    pool: &PgPool,
    video_id: i64,
) -> Result<PathBuf, NativeSliceError> {
    let video = active_video_by_id(pool, video_id)
        .await
        .map_err(|_| NativeSliceError::NotFound)?;
    let path = Path::new(&video.path);
    Ok(path.with_extension("srt"))
}

pub async fn video_and_subtitle_paths_for_video(
    pool: &PgPool,
    video_id: i64,
) -> Result<(PathBuf, PathBuf), NativeSliceError> {
    let video = active_video_by_id(pool, video_id)
        .await
        .map_err(|_| NativeSliceError::NotFound)?;
    let video_path = PathBuf::from(video.path);
    let subtitle_path = video_path.with_extension("srt");
    Ok((video_path, subtitle_path))
}

pub async fn search_subtitle_matches(
    pool: &PgPool,
    keyword: &str,
    limit: i64,
) -> Result<Vec<SubtitleSearchMatch>, NativeSliceError> {
    let keyword = keyword.trim();
    if keyword.is_empty() {
        return Ok(Vec::new());
    }
    let limit = if limit > 0 { limit } else { 20 };
    let pattern = format!("%{}%", escape_like(keyword.to_ascii_lowercase().as_str()));
    let rows = sqlx::query(
        r#"
        SELECT video_id, MIN(segment_index) AS segment_index
        FROM subtitle_segments
        WHERE LOWER(text) LIKE $1 ESCAPE '\'
        GROUP BY video_id
        ORDER BY video_id DESC
        LIMIT $2
        "#,
    )
    .bind(pattern)
    .bind(limit)
    .fetch_all(pool)
    .await
    .map_err(|_| NativeSliceError::DatabaseWrite)?;

    let mut matches = Vec::with_capacity(rows.len());
    for row in rows {
        let video_id: i64 = row
            .try_get("video_id")
            .map_err(|_| NativeSliceError::DatabaseWrite)?;
        let segment_index: i32 = row
            .try_get("segment_index")
            .map_err(|_| NativeSliceError::DatabaseWrite)?;
        let video = active_video_by_id(pool, video_id)
            .await
            .map_err(|_| NativeSliceError::DatabaseWrite)?;
        let segment_row = sqlx::query(
            r#"
            SELECT segment_index, start_time_ms, end_time_ms, text
            FROM subtitle_segments
            WHERE video_id = $1 AND segment_index = $2
            "#,
        )
        .bind(video_id)
        .bind(segment_index)
        .fetch_one(pool)
        .await
        .map_err(|_| NativeSliceError::DatabaseWrite)?;
        matches.push(SubtitleSearchMatch {
            video,
            segment: subtitle_segment_from_row(segment_row)?,
        });
    }
    Ok(matches)
}

pub async fn create_ai_tag_candidate(
    pool: &PgPool,
    input: AITagCandidateInput,
) -> Result<AITagCandidateRecord, NativeSliceError> {
    let id: i64 = sqlx::query_scalar(
        r#"
        INSERT INTO ai_tag_candidates(video_id, suggested_name, normalized_name, matched_tag_id, confidence, reasoning, source_summary, status)
        VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending')
        RETURNING id
        "#,
    )
    .bind(input.video_id)
    .bind(input.suggested_name)
    .bind(input.normalized_name)
    .bind(input.matched_tag_id)
    .bind(input.confidence)
    .bind(input.reasoning)
    .bind(input.source_summary)
    .fetch_one(pool)
    .await
    .map_err(|_| NativeSliceError::DatabaseWrite)?;
    ai_tag_candidate_by_id(pool, id).await
}

pub async fn approve_ai_tag_candidate(
    pool: &PgPool,
    id: i64,
) -> Result<AITagCandidateRecord, NativeSliceError> {
    let mut candidate = ai_tag_candidate_by_id(pool, id).await?;
    let tag_id = if let Some(tag_id) = candidate.matched_tag_id {
        tag_id
    } else {
        let normalized = candidate.normalized_name.trim();
        if let Some(existing_id) = active_tag_id_by_name(pool, normalized)
            .await
            .map_err(|_| NativeSliceError::DatabaseWrite)?
        {
            existing_id
        } else {
            create_tag(pool, normalized, "")
                .await
                .map_err(|_| NativeSliceError::DatabaseWrite)?
                .id
        }
    };

    let mut tx = pool
        .begin()
        .await
        .map_err(|_| NativeSliceError::DatabaseWrite)?;
    sqlx::query("INSERT INTO video_tags(video_id, tag_id) VALUES ($1, $2) ON CONFLICT DO NOTHING")
        .bind(candidate.video_id)
        .bind(tag_id)
        .execute(&mut *tx)
        .await
        .map_err(|_| NativeSliceError::DatabaseWrite)?;
    sqlx::query(
        r#"
        INSERT INTO ai_tag_approval_records(video_id, tag_id, candidate_id)
        VALUES ($1, $2, $3)
        ON CONFLICT DO NOTHING
        "#,
    )
    .bind(candidate.video_id)
    .bind(tag_id)
    .bind(id)
    .execute(&mut *tx)
    .await
    .map_err(|_| NativeSliceError::DatabaseWrite)?;
    sqlx::query("UPDATE ai_tag_candidates SET status = 'approved', matched_tag_id = $1, approved_at = now(), updated_at = now() WHERE id = $2")
        .bind(tag_id)
        .bind(id)
        .execute(&mut *tx)
        .await
        .map_err(|_| NativeSliceError::DatabaseWrite)?;
    tx.commit()
        .await
        .map_err(|_| NativeSliceError::DatabaseWrite)?;
    candidate = ai_tag_candidate_by_id(pool, id).await?;
    Ok(candidate)
}

pub async fn reject_ai_tag_candidate(
    pool: &PgPool,
    id: i64,
) -> Result<AITagCandidateRecord, NativeSliceError> {
    let result = sqlx::query(
        "UPDATE ai_tag_candidates SET status = 'rejected', rejected_at = now(), updated_at = now() WHERE id = $1",
    )
    .bind(id)
    .execute(pool)
    .await
    .map_err(|_| NativeSliceError::DatabaseWrite)?;
    if result.rows_affected() == 0 {
        return Err(NativeSliceError::NotFound);
    }
    ai_tag_candidate_by_id(pool, id).await
}

pub async fn reject_pending_ai_tag_candidates_by_video(
    pool: &PgPool,
    video_id: i64,
) -> Result<i64, NativeSliceError> {
    let result = sqlx::query(
        "UPDATE ai_tag_candidates SET status = 'rejected', rejected_at = now(), updated_at = now() WHERE video_id = $1 AND status = 'pending'",
    )
    .bind(video_id)
    .execute(pool)
    .await
    .map_err(|_| NativeSliceError::DatabaseWrite)?;
    Ok(result.rows_affected() as i64)
}

pub async fn retry_ai_tagging(pool: &PgPool, video_id: i64) -> Result<(), NativeSliceError> {
    let mut tx = pool
        .begin()
        .await
        .map_err(|_| NativeSliceError::DatabaseWrite)?;
    sqlx::query(
        "UPDATE ai_tag_candidates SET status = 'superseded', updated_at = now() WHERE video_id = $1 AND status = 'pending'",
    )
    .bind(video_id)
    .execute(&mut *tx)
    .await
    .map_err(|_| NativeSliceError::DatabaseWrite)?;
    sqlx::query(
        r#"
        INSERT INTO ai_tagging_states(video_id, status, skip_reason, evidence_fingerprint, last_error, updated_at)
        VALUES ($1, 'pending', '', '', '', now())
        ON CONFLICT (video_id) DO UPDATE SET
            status = 'pending',
            skip_reason = '',
            evidence_fingerprint = '',
            last_error = '',
            updated_at = now()
        "#,
    )
    .bind(video_id)
    .execute(&mut *tx)
    .await
    .map_err(|_| NativeSliceError::DatabaseWrite)?;
    tx.commit()
        .await
        .map_err(|_| NativeSliceError::DatabaseWrite)
}

pub async fn ai_tagging_status_summary(
    pool: &PgPool,
) -> Result<AITaggingStatusSummary, NativeSliceError> {
    let pending =
        sqlx::query_scalar("SELECT COUNT(*) FROM ai_tag_candidates WHERE status = 'pending'")
            .fetch_one(pool)
            .await
            .map_err(|_| NativeSliceError::DatabaseWrite)?;
    async fn count_state(pool: &PgPool, status: &str) -> Result<i64, NativeSliceError> {
        sqlx::query_scalar("SELECT COUNT(*) FROM ai_tagging_states WHERE status = $1")
            .bind(status)
            .fetch_one(pool)
            .await
            .map_err(|_| NativeSliceError::DatabaseWrite)
    }
    Ok(AITaggingStatusSummary {
        config_available: true,
        pending,
        processing: count_state(pool, "processing").await?,
        completed: count_state(pool, "completed").await?,
        skipped: count_state(pool, "skipped").await?,
        failed: count_state(pool, "failed").await?,
    })
}

pub async fn list_ai_tag_candidates(
    pool: &PgPool,
    status: Option<AITagCandidateStatus>,
) -> Result<Vec<AITagCandidateRecord>, NativeSliceError> {
    let rows = if let Some(status) = status {
        sqlx::query(
            r#"
            SELECT id, video_id, suggested_name, normalized_name, matched_tag_id, confidence, reasoning, source_summary, status
            FROM ai_tag_candidates
            WHERE status = $1
            ORDER BY id ASC
            "#,
        )
        .bind(ai_status_as_str(&status))
        .fetch_all(pool)
        .await
    } else {
        sqlx::query(
            r#"
            SELECT id, video_id, suggested_name, normalized_name, matched_tag_id, confidence, reasoning, source_summary, status
            FROM ai_tag_candidates
            ORDER BY id ASC
            "#,
        )
        .fetch_all(pool)
        .await
    }
    .map_err(|_| NativeSliceError::DatabaseWrite)?;
    rows.into_iter().map(ai_tag_candidate_from_row).collect()
}

pub async fn next_short_feed_video(
    pool: &PgPool,
    exclude_ids: &[i64],
) -> Result<ShortFeedVideoRecord, NativeSliceError> {
    let row = sqlx::query(
        r#"
        SELECT videos.id, videos.name, videos.path, videos.duration, videos.width, videos.height,
               COALESCE(short_feed_interactions.liked, false) AS liked,
               COALESCE(short_feed_interactions.favorited, false) AS favorited
        FROM videos
        LEFT JOIN short_feed_interactions ON short_feed_interactions.video_id = videos.id
        WHERE deleted_at IS NULL
          AND is_stale = false
          AND duration > 0
          AND duration < (SELECT COALESCE(short_feed_max_duration_minutes, 5) * 60.0 FROM settings ORDER BY id ASC LIMIT 1)
          AND NOT (id = ANY($1::bigint[]))
        ORDER BY id ASC
        LIMIT 1
        "#,
    )
    .bind(exclude_ids)
    .fetch_optional(pool)
    .await
    .map_err(|_| NativeSliceError::DatabaseWrite)?
    .ok_or(NativeSliceError::NotFound)?;
    let id: i64 = row
        .try_get("id")
        .map_err(|_| NativeSliceError::DatabaseWrite)?;
    let path: String = row
        .try_get("path")
        .map_err(|_| NativeSliceError::DatabaseWrite)?;
    Ok(ShortFeedVideoRecord {
        id,
        name: row
            .try_get("name")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        path: path.clone(),
        duration: row
            .try_get("duration")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        width: row
            .try_get("width")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        height: row
            .try_get("height")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        tags: load_video_tags(pool, id)
            .await
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        media_url: format!("/short-media/{id}"),
        media_mime: inline_video_mime(&path).unwrap_or_default().to_string(),
        liked: row
            .try_get("liked")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        favorited: row
            .try_get("favorited")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        reason_code: String::new(),
        reason_message: String::new(),
    })
}

pub async fn short_feed_favorite_videos(
    pool: &PgPool,
) -> Result<Vec<ShortFeedVideoRecord>, NativeSliceError> {
    let rows = sqlx::query(
        r#"
        SELECT videos.id, videos.name, videos.path, videos.duration, videos.width, videos.height,
               COALESCE(short_feed_interactions.liked, false) AS liked,
               COALESCE(short_feed_interactions.favorited, false) AS favorited
        FROM videos
        JOIN short_feed_interactions ON short_feed_interactions.video_id = videos.id
        WHERE videos.deleted_at IS NULL
          AND videos.is_stale = false
          AND short_feed_interactions.favorited = true
          AND videos.duration > 0
          AND videos.duration < (SELECT COALESCE(short_feed_max_duration_minutes, 5) * 60.0 FROM settings ORDER BY id ASC LIMIT 1)
        ORDER BY short_feed_interactions.updated_at DESC
        "#,
    )
    .fetch_all(pool)
    .await
    .map_err(|_| NativeSliceError::DatabaseWrite)?;

    let mut videos = Vec::with_capacity(rows.len());
    for row in rows {
        let id: i64 = row
            .try_get("id")
            .map_err(|_| NativeSliceError::DatabaseWrite)?;
        let path: String = row
            .try_get("path")
            .map_err(|_| NativeSliceError::DatabaseWrite)?;
        videos.push(ShortFeedVideoRecord {
            id,
            name: row
                .try_get("name")
                .map_err(|_| NativeSliceError::DatabaseWrite)?,
            path: path.clone(),
            duration: row
                .try_get("duration")
                .map_err(|_| NativeSliceError::DatabaseWrite)?,
            width: row
                .try_get("width")
                .map_err(|_| NativeSliceError::DatabaseWrite)?,
            height: row
                .try_get("height")
                .map_err(|_| NativeSliceError::DatabaseWrite)?,
            tags: load_video_tags(pool, id)
                .await
                .map_err(|_| NativeSliceError::DatabaseWrite)?,
            media_url: format!("/short-media/{id}"),
            media_mime: inline_video_mime(&path).unwrap_or_default().to_string(),
            liked: row
                .try_get("liked")
                .map_err(|_| NativeSliceError::DatabaseWrite)?,
            favorited: row
                .try_get("favorited")
                .map_err(|_| NativeSliceError::DatabaseWrite)?,
            reason_code: String::new(),
            reason_message: String::new(),
        });
    }
    Ok(videos)
}

pub async fn short_feed_media(
    pool: &PgPool,
    video_id: i64,
) -> Result<ShortFeedVideoRecord, NativeSliceError> {
    let row = sqlx::query(
        r#"
        SELECT videos.id, videos.name, videos.path, videos.duration, videos.width, videos.height,
               COALESCE(short_feed_interactions.liked, false) AS liked,
               COALESCE(short_feed_interactions.favorited, false) AS favorited
        FROM videos
        LEFT JOIN short_feed_interactions ON short_feed_interactions.video_id = videos.id
        WHERE videos.id = $1
          AND videos.deleted_at IS NULL
          AND videos.is_stale = false
          AND videos.duration > 0
          AND videos.duration < (SELECT COALESCE(short_feed_max_duration_minutes, 5) * 60.0 FROM settings ORDER BY id ASC LIMIT 1)
        "#,
    )
    .bind(video_id)
    .fetch_optional(pool)
    .await
    .map_err(|_| NativeSliceError::DatabaseWrite)?
    .ok_or(NativeSliceError::NotFound)?;
    let path: String = row
        .try_get("path")
        .map_err(|_| NativeSliceError::DatabaseWrite)?;
    Ok(ShortFeedVideoRecord {
        id: video_id,
        name: row
            .try_get("name")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        path: path.clone(),
        duration: row
            .try_get("duration")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        width: row
            .try_get("width")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        height: row
            .try_get("height")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        tags: load_video_tags(pool, video_id)
            .await
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        media_url: format!("/short-media/{video_id}"),
        media_mime: inline_video_mime(&path)
            .unwrap_or("application/octet-stream")
            .to_string(),
        liked: row
            .try_get("liked")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        favorited: row
            .try_get("favorited")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        reason_code: String::new(),
        reason_message: String::new(),
    })
}

pub async fn record_short_feed_feedback(
    pool: &PgPool,
    video_id: i64,
    feedback: ShortFeedFeedback,
) -> Result<ShortFeedInteractionRecord, NativeSliceError> {
    let current = sqlx::query(
        "SELECT liked, favorited, view_count FROM short_feed_interactions WHERE video_id = $1",
    )
    .bind(video_id)
    .fetch_optional(pool)
    .await
    .map_err(|_| NativeSliceError::DatabaseWrite)?;
    let mut liked = false;
    let mut favorited = false;
    let mut view_count = 0;
    if let Some(row) = current {
        liked = row
            .try_get("liked")
            .map_err(|_| NativeSliceError::DatabaseWrite)?;
        favorited = row
            .try_get("favorited")
            .map_err(|_| NativeSliceError::DatabaseWrite)?;
        view_count = row
            .try_get("view_count")
            .map_err(|_| NativeSliceError::DatabaseWrite)?;
    }
    if let Some(value) = feedback.liked {
        liked = value;
    }
    if let Some(value) = feedback.favorited {
        favorited = value;
    }
    if feedback.viewed {
        view_count += 1;
    }

    sqlx::query(
        r#"
        INSERT INTO short_feed_interactions(video_id, liked, favorited, view_count, last_viewed_at, liked_at, favorited_at, updated_at)
        VALUES ($1, $2, $3, $4, CASE WHEN $5 THEN now() ELSE NULL END, CASE WHEN $2 THEN now() ELSE NULL END, CASE WHEN $3 THEN now() ELSE NULL END, now())
        ON CONFLICT (video_id) DO UPDATE SET
            liked = EXCLUDED.liked,
            favorited = EXCLUDED.favorited,
            view_count = EXCLUDED.view_count,
            last_viewed_at = EXCLUDED.last_viewed_at,
            liked_at = EXCLUDED.liked_at,
            favorited_at = EXCLUDED.favorited_at,
            updated_at = now()
        "#,
    )
    .bind(video_id)
    .bind(liked)
    .bind(favorited)
    .bind(view_count)
    .bind(feedback.viewed)
    .execute(pool)
    .await
    .map_err(|_| NativeSliceError::DatabaseWrite)?;

    Ok(ShortFeedInteractionRecord {
        video_id,
        liked,
        favorited,
        view_count,
    })
}

fn inline_video_mime(path: &str) -> Option<&'static str> {
    let extension = Path::new(path)
        .extension()
        .and_then(OsStr::to_str)
        .map(|value| format!(".{}", value.to_ascii_lowercase()))?;
    SHORT_FEED_INLINE_MIMES
        .iter()
        .find_map(|(candidate, mime)| (*candidate == extension).then_some(*mime))
}

pub async fn start_cleanup_analysis(
    pool: &PgPool,
    max_duplicate_duration_seconds: f64,
    min_width: i32,
    min_height: i32,
) -> Result<CleanupAnalysisRecord, NativeSliceError> {
    let rows = sqlx::query(
        r#"
        SELECT id, path, size, duration, width, height
        FROM videos
        WHERE deleted_at IS NULL
        ORDER BY id ASC
        "#,
    )
    .fetch_all(pool)
    .await
    .map_err(|_| NativeSliceError::DatabaseWrite)?;
    let mut low_duration_ids = Vec::new();
    let mut low_resolution_ids = Vec::new();
    let mut buckets: BTreeMap<(i64, u64), Vec<i64>> = BTreeMap::new();

    for row in rows {
        let id: i64 = row
            .try_get("id")
            .map_err(|_| NativeSliceError::DatabaseWrite)?;
        let path: String = row
            .try_get("path")
            .map_err(|_| NativeSliceError::DatabaseWrite)?;
        let size: i64 = row
            .try_get("size")
            .map_err(|_| NativeSliceError::DatabaseWrite)?;
        let duration: f64 = row
            .try_get("duration")
            .map_err(|_| NativeSliceError::DatabaseWrite)?;
        let width: i32 = row
            .try_get("width")
            .map_err(|_| NativeSliceError::DatabaseWrite)?;
        let height: i32 = row
            .try_get("height")
            .map_err(|_| NativeSliceError::DatabaseWrite)?;
        if max_duplicate_duration_seconds > 0.0
            && duration > 0.0
            && duration < max_duplicate_duration_seconds
        {
            low_duration_ids.push(id);
        }
        if min_width > 0 && min_height > 0 && (width < min_width || height < min_height) {
            low_resolution_ids.push(id);
        }
        if let Ok(data) = fs::read(&path) {
            buckets
                .entry((size, stable_hash(&data)))
                .or_default()
                .push(id);
        }
    }

    let duplicate_groups = buckets
        .into_values()
        .filter(|ids| ids.len() > 1)
        .map(|ids| CleanupDuplicateGroup {
            original_id: ids[0],
            candidate_ids: ids[1..].to_vec(),
            reason: "file size and sample hash match".to_string(),
        })
        .collect();

    Ok(CleanupAnalysisRecord {
        duplicate_groups,
        low_duration_ids,
        low_resolution_ids,
    })
}

pub async fn diagnostics_snapshot(pool: &PgPool) -> Result<DiagnosticsSnapshot, NativeSliceError> {
    Ok(DiagnosticsSnapshot {
        video_count: count_table(pool, "videos").await?,
        tag_count: count_table(pool, "tags").await?,
        subtitle_segment_count: count_table(pool, "subtitle_segments").await?,
        ai_candidate_count: count_table(pool, "ai_tag_candidates").await?,
        short_feed_interaction_count: count_table(pool, "short_feed_interactions").await?,
        redacted_settings: get_public_settings(pool)
            .await
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
    })
}

async fn active_tag_id_by_name(pool: &PgPool, name: &str) -> Result<Option<i64>, TagMutationError> {
    sqlx::query_scalar("SELECT id FROM tags WHERE name = $1 AND deleted_at IS NULL")
        .bind(name)
        .fetch_optional(pool)
        .await
        .map_err(|_| TagMutationError::DatabaseWrite)
}

async fn soft_deleted_tag_id_by_name(
    pool: &PgPool,
    name: &str,
) -> Result<Option<i64>, TagMutationError> {
    sqlx::query_scalar("SELECT id FROM tags WHERE name = $1 AND deleted_at IS NOT NULL")
        .bind(name)
        .fetch_optional(pool)
        .await
        .map_err(|_| TagMutationError::DatabaseWrite)
}

async fn active_tag_id_by_id(pool: &PgPool, id: i64) -> Result<Option<i64>, TagMutationError> {
    sqlx::query_scalar("SELECT id FROM tags WHERE id = $1 AND deleted_at IS NULL")
        .bind(id)
        .fetch_optional(pool)
        .await
        .map_err(|_| TagMutationError::DatabaseWrite)
}

async fn tag_by_id(pool: &PgPool, id: i64) -> Result<TagRecord, TagMutationError> {
    let row = sqlx::query("SELECT id, name, COALESCE(color, '') AS color FROM tags WHERE id = $1")
        .bind(id)
        .fetch_optional(pool)
        .await
        .map_err(|_| TagMutationError::DatabaseWrite)?
        .ok_or(TagMutationError::TagNotFound)?;
    Ok(TagRecord {
        id: row
            .try_get("id")
            .map_err(|_| TagMutationError::DatabaseWrite)?,
        name: row
            .try_get("name")
            .map_err(|_| TagMutationError::DatabaseWrite)?,
        color: row
            .try_get("color")
            .map_err(|_| TagMutationError::DatabaseWrite)?,
    })
}

fn map_tag_insert_error(error: sqlx::Error) -> TagMutationError {
    let text = error.to_string().to_ascii_lowercase();
    if text.contains("unique") || text.contains("duplicate") {
        TagMutationError::TagExists
    } else {
        TagMutationError::DatabaseWrite
    }
}

fn subtitle_segment_from_row(
    row: sqlx::postgres::PgRow,
) -> Result<SubtitleSegmentRecord, NativeSliceError> {
    let text: String = row
        .try_get("text")
        .map_err(|_| NativeSliceError::DatabaseWrite)?;
    Ok(SubtitleSegmentRecord {
        index: row
            .try_get("segment_index")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        start_time_ms: row
            .try_get("start_time_ms")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        end_time_ms: row
            .try_get("end_time_ms")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        lines: split_subtitle_lines(&text),
        text,
    })
}

fn split_subtitle_lines(text: &str) -> Vec<String> {
    let lines = text
        .lines()
        .map(str::trim)
        .filter(|line| !line.is_empty())
        .map(ToString::to_string)
        .collect::<Vec<_>>();
    if lines.is_empty() && !text.trim().is_empty() {
        vec![text.trim().to_string()]
    } else {
        lines
    }
}

fn parse_srt(content: &str) -> Result<Vec<SubtitleSegmentRecord>, NativeSliceError> {
    let normalized = content
        .replace("\r\n", "\n")
        .replace('\r', "\n")
        .trim_start_matches('\u{feff}')
        .trim()
        .to_string();
    if normalized.is_empty() {
        return Ok(Vec::new());
    }
    let mut segments = Vec::new();
    for (block_index, block) in normalized.split("\n\n").enumerate() {
        if let Some(segment) = parse_srt_block(block_index + 1, block)? {
            segments.push(segment);
        }
    }
    Ok(segments)
}

fn parse_srt_block(
    fallback_index: usize,
    block: &str,
) -> Result<Option<SubtitleSegmentRecord>, NativeSliceError> {
    let lines = block
        .lines()
        .map(str::trim)
        .filter(|line| !line.is_empty())
        .collect::<Vec<_>>();
    if lines.len() < 2 {
        return Ok(None);
    }
    let mut time_line_index = 0;
    let mut index = fallback_index as i32;
    if let Ok(parsed) = lines[0].parse::<i32>() {
        index = parsed;
        time_line_index = 1;
    }
    if lines.len() <= time_line_index + 1 {
        return Ok(None);
    }
    let (start_time_ms, mut end_time_ms) = parse_srt_range(lines[time_line_index])?;
    if end_time_ms < start_time_ms {
        end_time_ms = start_time_ms;
    }
    let text = lines[time_line_index + 1..].join("\n").trim().to_string();
    if text.is_empty() {
        return Ok(None);
    }
    Ok(Some(SubtitleSegmentRecord {
        index,
        start_time_ms,
        end_time_ms,
        lines: split_subtitle_lines(&text),
        text,
    }))
}

fn parse_srt_range(line: &str) -> Result<(i64, i64), NativeSliceError> {
    let Some((start, end)) = line.split_once("-->") else {
        return Err(NativeSliceError::SubtitleParse);
    };
    Ok((parse_srt_timestamp(start)?, parse_srt_timestamp(end)?))
}

fn parse_srt_timestamp(value: &str) -> Result<i64, NativeSliceError> {
    let cleaned = value.trim().replace(',', ".");
    let parts = cleaned.split([':', '.']).collect::<Vec<_>>();
    if parts.len() != 4 {
        return Err(NativeSliceError::SubtitleParse);
    }
    let hours = parts[0]
        .parse::<i64>()
        .map_err(|_| NativeSliceError::SubtitleParse)?;
    let minutes = parts[1]
        .parse::<i64>()
        .map_err(|_| NativeSliceError::SubtitleParse)?;
    let seconds = parts[2]
        .parse::<i64>()
        .map_err(|_| NativeSliceError::SubtitleParse)?;
    let millis = parts[3]
        .parse::<i64>()
        .map_err(|_| NativeSliceError::SubtitleParse)?;
    Ok(hours * 3_600_000 + minutes * 60_000 + seconds * 1_000 + millis)
}

fn escape_like(value: &str) -> String {
    value
        .replace('\\', "\\\\")
        .replace('%', "\\%")
        .replace('_', "\\_")
}

fn ai_status_as_str(status: &AITagCandidateStatus) -> &'static str {
    match status {
        AITagCandidateStatus::Pending => "pending",
        AITagCandidateStatus::Approved => "approved",
        AITagCandidateStatus::Rejected => "rejected",
        AITagCandidateStatus::Superseded => "superseded",
    }
}

fn ai_status_from_str(value: &str) -> AITagCandidateStatus {
    match value {
        "approved" => AITagCandidateStatus::Approved,
        "rejected" => AITagCandidateStatus::Rejected,
        "superseded" => AITagCandidateStatus::Superseded,
        _ => AITagCandidateStatus::Pending,
    }
}

async fn ai_tag_candidate_by_id(
    pool: &PgPool,
    id: i64,
) -> Result<AITagCandidateRecord, NativeSliceError> {
    let row = sqlx::query(
        r#"
        SELECT id, video_id, suggested_name, normalized_name, matched_tag_id, confidence, reasoning, source_summary, status
        FROM ai_tag_candidates
        WHERE id = $1
        "#,
    )
    .bind(id)
    .fetch_optional(pool)
    .await
    .map_err(|_| NativeSliceError::DatabaseWrite)?
    .ok_or(NativeSliceError::NotFound)?;
    ai_tag_candidate_from_row(row)
}

fn ai_tag_candidate_from_row(
    row: sqlx::postgres::PgRow,
) -> Result<AITagCandidateRecord, NativeSliceError> {
    let status: String = row
        .try_get("status")
        .map_err(|_| NativeSliceError::DatabaseWrite)?;
    Ok(AITagCandidateRecord {
        id: row
            .try_get("id")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        video_id: row
            .try_get("video_id")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        suggested_name: row
            .try_get("suggested_name")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        normalized_name: row
            .try_get("normalized_name")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        matched_tag_id: row
            .try_get("matched_tag_id")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        confidence: row
            .try_get("confidence")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        reasoning: row
            .try_get("reasoning")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        source_summary: row
            .try_get("source_summary")
            .map_err(|_| NativeSliceError::DatabaseWrite)?,
        status: ai_status_from_str(&status),
    })
}

fn stable_hash(data: &[u8]) -> u64 {
    let mut hasher = std::collections::hash_map::DefaultHasher::new();
    data.hash(&mut hasher);
    hasher.finish()
}

async fn count_table(pool: &PgPool, table_name: &str) -> Result<i64, NativeSliceError> {
    let query = format!("SELECT COUNT(*) FROM {table_name}");
    sqlx::query_scalar::<_, i64>(&query)
        .fetch_one(pool)
        .await
        .map_err(|_| NativeSliceError::DatabaseWrite)
}

fn positive_or_default(value: i32, fallback: i32) -> i32 {
    if value > 0 {
        value
    } else {
        fallback
    }
}

async fn scan_directory_by_id(
    pool: &PgPool,
    id: i64,
) -> Result<ScanDirectoryRecord, LibraryManagementError> {
    let row = sqlx::query(
        "SELECT id, path, COALESCE(alias, '') AS alias FROM scan_directories WHERE id = $1 AND deleted_at IS NULL",
    )
    .bind(id)
    .fetch_optional(pool)
    .await
    .map_err(|_| LibraryManagementError::DatabaseWrite)?
    .ok_or(LibraryManagementError::NotFound)?;
    Ok(ScanDirectoryRecord {
        id: row
            .try_get("id")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        path: row
            .try_get("path")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
        alias: row
            .try_get("alias")
            .map_err(|_| LibraryManagementError::DatabaseWrite)?,
    })
}

impl ScanSyncResult {
    fn record_error(
        &mut self,
        operation: &str,
        directory: Option<String>,
        path: Option<String>,
        error: &VideoMutationError,
    ) {
        self.skipped += 1;
        self.errors.push(ScanSyncError {
            operation: operation.to_string(),
            directory,
            path,
            error: error.to_string(),
        });
    }
}

fn fingerprint_scanned_file(file: &ScannedFile) -> ScanFileFingerprint {
    ScanFileFingerprint {
        name: Path::new(&file.path)
            .file_name()
            .and_then(OsStr::to_str)
            .unwrap_or_default()
            .to_string(),
        size: file.size,
    }
}

fn fingerprint_video(video: &VideoSummary) -> ScanFileFingerprint {
    ScanFileFingerprint {
        name: video.name.clone(),
        size: video.size,
    }
}

fn scan_directory_recursive(
    dir: &Path,
    extensions: &BTreeSet<String>,
    files: &mut Vec<ScannedFile>,
) -> Result<(), VideoMutationError> {
    let entries = fs::read_dir(dir).map_err(|_| VideoMutationError::Filesystem)?;
    for entry in entries {
        let entry = entry.map_err(|_| VideoMutationError::Filesystem)?;
        let path = entry.path();
        let metadata = entry
            .metadata()
            .map_err(|_| VideoMutationError::Filesystem)?;

        if should_skip_hidden_path(&path) {
            continue;
        }

        if metadata.is_dir() {
            if is_trash_dir_name(path.file_name().and_then(OsStr::to_str).unwrap_or_default()) {
                continue;
            }
            scan_directory_recursive(&path, extensions, files)?;
            continue;
        }

        if !metadata.is_file()
            || is_trash_path(&path)
            || has_temp_video_suffix(&path)
            || is_recently_active_file(&metadata)
            || is_known_non_video_source_path(&path)
        {
            continue;
        }

        let ext = path
            .extension()
            .and_then(OsStr::to_str)
            .map(|value| format!(".{}", value.to_ascii_lowercase()));
        if ext.as_ref().is_some_and(|ext| extensions.contains(ext)) {
            files.push(ScannedFile {
                path: path_to_string(&path),
                size: metadata.len() as i64,
            });
        }
    }

    Ok(())
}

fn clean_path(path: &Path) -> Result<PathBuf, VideoMutationError> {
    let value = path.to_string_lossy().trim().to_string();
    if value.is_empty() || value == "." {
        return Err(VideoMutationError::EmptyPath);
    }
    Ok(PathBuf::from(value))
}

fn path_to_string(path: &Path) -> String {
    path.to_string_lossy().to_string()
}

fn file_name_string(path: &Path) -> Result<String, VideoMutationError> {
    path.file_name()
        .and_then(OsStr::to_str)
        .map(str::to_string)
        .filter(|value| !value.is_empty())
        .ok_or(VideoMutationError::InvalidFileName)
}

fn should_skip_hidden_path(path: &Path) -> bool {
    path.file_name()
        .and_then(OsStr::to_str)
        .is_some_and(|name| name != "." && name.starts_with('.'))
}

fn is_trash_dir_name(name: &str) -> bool {
    name.trim().eq_ignore_ascii_case(DEFAULT_TRASH_DIR_NAME)
}

fn is_trash_path(path: &Path) -> bool {
    path.components().any(|component| {
        component
            .as_os_str()
            .to_str()
            .is_some_and(is_trash_dir_name)
    })
}

fn has_temp_video_suffix(path: &Path) -> bool {
    let Some(base_name) = path
        .file_name()
        .and_then(OsStr::to_str)
        .map(|value| value.to_ascii_lowercase())
    else {
        return false;
    };
    let ext = Path::new(&base_name)
        .extension()
        .and_then(OsStr::to_str)
        .map(|value| format!(".{value}"))
        .unwrap_or_default();
    let stem = base_name.strip_suffix(&ext).unwrap_or(&base_name);

    TEMP_VIDEO_STEM_SUFFIXES
        .iter()
        .any(|suffix| stem == suffix.trim_start_matches('.') || stem.ends_with(suffix))
}

fn is_known_non_video_source_path(path: &Path) -> bool {
    let Some(base_name) = path
        .file_name()
        .and_then(OsStr::to_str)
        .map(|value| value.to_ascii_lowercase())
    else {
        return false;
    };

    if base_name.ends_with(".d.ts") || base_name.ends_with(".d.tsx") {
        return true;
    }

    let ext = path
        .extension()
        .and_then(OsStr::to_str)
        .map(|value| value.to_ascii_lowercase());
    if !matches!(ext.as_deref(), Some("ts" | "tsx")) {
        return false;
    }
    if path.components().any(|component| {
        component
            .as_os_str()
            .to_str()
            .is_some_and(|value| value == "node_modules")
    }) {
        return true;
    }

    let Ok(data) = fs::read(path) else {
        return false;
    };
    let sample = String::from_utf8_lossy(&data).trim().to_ascii_lowercase();
    if sample.is_empty() {
        return false;
    }
    [
        "export ",
        "import ",
        "interface ",
        "type ",
        "declare ",
        "namespace ",
        "const ",
        "let ",
        "var ",
        "function ",
        "class ",
    ]
    .iter()
    .any(|marker| sample.contains(marker))
}

fn is_recently_active_file(metadata: &fs::Metadata) -> bool {
    metadata
        .modified()
        .ok()
        .and_then(|modified| SystemTime::now().duration_since(modified).ok())
        .is_some_and(|age| age < RECENT_ACTIVE_FILE_THRESHOLD)
}

async fn active_video_id_by_path(
    pool: &PgPool,
    path: &str,
) -> Result<Option<i64>, VideoMutationError> {
    sqlx::query_scalar("SELECT id FROM videos WHERE path = $1 AND deleted_at IS NULL")
        .bind(path)
        .fetch_optional(pool)
        .await
        .map_err(|_| VideoMutationError::DatabaseWrite)
}

async fn any_video_id_by_path(
    pool: &PgPool,
    path: &str,
) -> Result<Option<i64>, VideoMutationError> {
    sqlx::query_scalar("SELECT id FROM videos WHERE path = $1")
        .bind(path)
        .fetch_optional(pool)
        .await
        .map_err(|_| VideoMutationError::DatabaseWrite)
}

async fn active_video_by_id(pool: &PgPool, id: i64) -> Result<VideoSummary, VideoMutationError> {
    let play_weight = load_play_weight(pool)
        .await
        .map_err(|_| VideoMutationError::DatabaseWrite)?;
    let row = sqlx::query(
        r#"
        SELECT
            v.id,
            v.name,
            v.path,
            v.directory,
            v.size,
            COALESCE(v.duration, 0) AS duration,
            COALESCE(v.resolution, '') AS resolution,
            COALESCE(v.width, 0) AS width,
            COALESCE(v.height, 0) AS height,
            COALESCE(v.is_stale, false) AS is_stale,
            COALESCE(v.play_count, 0) AS play_count,
            COALESCE(v.random_play_count, 0) AS random_play_count,
            v.last_played_at::text AS last_played_at,
            v.created_at::text AS created_at,
            v.updated_at::text AS updated_at,
            (COALESCE(v.play_count, 0) * $2::float8 + COALESCE(v.random_play_count, 0)) AS score
        FROM videos v
        WHERE v.id = $1 AND v.deleted_at IS NULL
        "#,
    )
    .bind(id)
    .bind(play_weight)
    .fetch_optional(pool)
    .await
    .map_err(|_| VideoMutationError::DatabaseWrite)?
    .ok_or(VideoMutationError::VideoNotFound)?;

    let mut video =
        map_video_row(row, play_weight).map_err(|_| VideoMutationError::DatabaseWrite)?;
    video.tags = load_video_tags(pool, video.id)
        .await
        .map_err(|_| VideoMutationError::DatabaseWrite)?;
    Ok(video)
}

async fn active_videos_under_roots(
    pool: &PgPool,
    roots: &[PathBuf],
) -> Result<Vec<VideoSummary>, VideoMutationError> {
    if roots.is_empty() {
        return Ok(Vec::new());
    }

    let play_weight = load_play_weight(pool)
        .await
        .map_err(|_| VideoMutationError::DatabaseWrite)?;
    let rows = sqlx::query(
        r#"
        SELECT
            v.id,
            v.name,
            v.path,
            v.directory,
            v.size,
            COALESCE(v.duration, 0) AS duration,
            COALESCE(v.resolution, '') AS resolution,
            COALESCE(v.width, 0) AS width,
            COALESCE(v.height, 0) AS height,
            COALESCE(v.is_stale, false) AS is_stale,
            COALESCE(v.play_count, 0) AS play_count,
            COALESCE(v.random_play_count, 0) AS random_play_count,
            v.last_played_at::text AS last_played_at,
            v.created_at::text AS created_at,
            v.updated_at::text AS updated_at,
            (COALESCE(v.play_count, 0) * $1::float8 + COALESCE(v.random_play_count, 0)) AS score
        FROM videos v
        WHERE v.deleted_at IS NULL
        ORDER BY v.id ASC
        "#,
    )
    .bind(play_weight)
    .fetch_all(pool)
    .await
    .map_err(|_| VideoMutationError::DatabaseWrite)?;

    let mut videos = Vec::new();
    for row in rows {
        let mut video =
            map_video_row(row, play_weight).map_err(|_| VideoMutationError::DatabaseWrite)?;
        if video_belongs_to_roots(&video, roots) {
            video.tags = load_video_tags(pool, video.id)
                .await
                .map_err(|_| VideoMutationError::DatabaseWrite)?;
            videos.push(video);
        }
    }
    Ok(videos)
}

fn video_belongs_to_roots(video: &VideoSummary, roots: &[PathBuf]) -> bool {
    roots.iter().any(|root| {
        let root_text = path_to_string(root);
        let prefix = format!("{root_text}{}", std::path::MAIN_SEPARATOR);
        video.directory == root_text
            || video.directory.starts_with(&prefix)
            || video.path.starts_with(&prefix)
    })
}

async fn update_video_metadata(
    pool: &PgPool,
    id: i64,
    metadata: &MediaMetadata,
) -> Result<(), VideoMutationError> {
    let result = sqlx::query(
        r#"
        UPDATE videos
        SET duration = $1,
            resolution = $2,
            width = $3,
            height = $4,
            updated_at = now()
        WHERE id = $5 AND deleted_at IS NULL
        "#,
    )
    .bind(metadata.duration)
    .bind(&metadata.resolution)
    .bind(metadata.width)
    .bind(metadata.height)
    .bind(id)
    .execute(pool)
    .await
    .map_err(|_| VideoMutationError::DatabaseWrite)?;
    if result.rows_affected() == 0 {
        return Err(VideoMutationError::VideoNotFound);
    }
    Ok(())
}

fn map_insert_error(error: sqlx::Error) -> VideoMutationError {
    let text = error.to_string().to_ascii_lowercase();
    if text.contains("unique") || text.contains("duplicate") {
        VideoMutationError::VideoExists
    } else {
        VideoMutationError::DatabaseWrite
    }
}

fn move_file_to_trash(path: &Path) -> Result<(), VideoMutationError> {
    if !path.exists() {
        return Ok(());
    }
    if is_trash_path(path) {
        return Ok(());
    }
    let trash_dir = path
        .parent()
        .ok_or(VideoMutationError::EmptyPath)?
        .join(DEFAULT_TRASH_DIR_NAME);
    fs::create_dir_all(&trash_dir).map_err(|_| VideoMutationError::Filesystem)?;
    let mut target = trash_dir.join(file_name_string(path)?);
    if target.exists() {
        let ext = path
            .extension()
            .and_then(OsStr::to_str)
            .map(|value| format!(".{value}"))
            .unwrap_or_default();
        let stem = path.file_stem().and_then(OsStr::to_str).unwrap_or("video");
        target = trash_dir.join(format!("{stem}_native{ext}"));
    }
    fs::rename(path, target).map_err(|_| VideoMutationError::Filesystem)
}

fn choose_weighted_candidate(videos: Vec<VideoSummary>, sample: f64) -> Option<VideoSummary> {
    let max_score = videos.iter().map(|video| video.score).reduce(f64::max)?;
    let weights = videos
        .iter()
        .map(|video| max_score - video.score + 1.0)
        .collect::<Vec<_>>();
    let total_weight = weights.iter().sum::<f64>();
    let mut threshold = sample.clamp(0.0, 0.999_999_999_999_999_9) * total_weight;

    for (video, weight) in videos.into_iter().zip(weights) {
        if threshold <= weight {
            return Some(video);
        }
        threshold -= weight;
    }

    None
}

async fn load_play_weight(pool: &PgPool) -> Result<f64, VideoQueryError> {
    let value = sqlx::query_scalar::<_, Option<f64>>(
        r#"
        SELECT play_weight
        FROM settings
        ORDER BY id ASC
        LIMIT 1
        "#,
    )
    .fetch_optional(pool)
    .await?
    .flatten()
    .unwrap_or(2.0);

    Ok(clamp_play_weight(value))
}

async fn fetch_video_rows(
    pool: &PgPool,
    filter: &VideoFilter,
    play_weight: f64,
    limit: i64,
) -> Result<Vec<sqlx::postgres::PgRow>, VideoQueryError> {
    let keyword = filter
        .keyword
        .as_deref()
        .map(str::trim)
        .filter(|value| !value.is_empty());
    let keyword_pattern = keyword.map(|value| format!("%{}%", value));
    let tag_ids = filter.tag_ids.clone();
    let tag_count = tag_ids.len() as i64;
    let cursor_score = filter.cursor.map(|cursor| cursor.score);
    let cursor_size = filter.cursor.map(|cursor| cursor.size);
    let cursor_id = filter.cursor.map(|cursor| cursor.id);

    let rows = sqlx::query(
        r#"
        SELECT
            v.id,
            v.name,
            v.path,
            v.directory,
            v.size,
            COALESCE(v.duration, 0) AS duration,
            COALESCE(v.resolution, '') AS resolution,
            COALESCE(v.width, 0) AS width,
            COALESCE(v.height, 0) AS height,
            COALESCE(v.is_stale, false) AS is_stale,
            COALESCE(v.play_count, 0) AS play_count,
            COALESCE(v.random_play_count, 0) AS random_play_count,
            v.last_played_at::text AS last_played_at,
            v.created_at::text AS created_at,
            v.updated_at::text AS updated_at,
            (COALESCE(v.play_count, 0) * $1::float8 + COALESCE(v.random_play_count, 0)) AS score
        FROM videos v
        WHERE v.deleted_at IS NULL
          AND ($2::text IS NULL OR v.name LIKE $2 OR v.path LIKE $2)
          AND ($3::bigint IS NULL OR v.size >= $3)
          AND ($4::bigint IS NULL OR v.size < $4)
          AND ($5::integer IS NULL OR v.height >= $5)
          AND ($6::integer IS NULL OR v.height <= $6)
          AND (
            $7::bigint = 0
            OR v.id IN (
                SELECT vt.video_id
                FROM video_tags vt
                WHERE vt.tag_id = ANY($8::bigint[])
                GROUP BY vt.video_id
                HAVING COUNT(DISTINCT vt.tag_id) = $7
            )
          )
          AND (
            $9::float8 IS NULL
            OR ((COALESCE(v.play_count, 0) * $1::float8 + COALESCE(v.random_play_count, 0)) > $9)
            OR ((COALESCE(v.play_count, 0) * $1::float8 + COALESCE(v.random_play_count, 0)) = $9 AND v.size < $10)
            OR ((COALESCE(v.play_count, 0) * $1::float8 + COALESCE(v.random_play_count, 0)) = $9 AND v.size = $10 AND v.id < $11)
          )
        ORDER BY score ASC, v.size DESC, v.id DESC
        LIMIT $12
        "#,
    )
    .bind(play_weight)
    .bind(keyword_pattern)
    .bind(filter.min_size)
    .bind(filter.max_size)
    .bind(filter.min_height)
    .bind(filter.max_height)
    .bind(tag_count)
    .bind(tag_ids)
    .bind(cursor_score)
    .bind(cursor_size)
    .bind(cursor_id)
    .bind(limit)
    .fetch_all(pool)
    .await?;

    Ok(rows)
}

fn map_video_row(
    row: sqlx::postgres::PgRow,
    play_weight: f64,
) -> Result<VideoSummary, VideoQueryError> {
    let play_count: i32 = row.try_get("play_count")?;
    let random_play_count: i32 = row.try_get("random_play_count")?;

    Ok(VideoSummary {
        id: row.try_get::<i64, _>("id")?,
        name: row.try_get("name")?,
        path: row.try_get("path")?,
        directory: row.try_get("directory")?,
        size: row.try_get::<i64, _>("size")?,
        duration: row.try_get::<f64, _>("duration")?,
        resolution: row.try_get("resolution")?,
        width: row.try_get("width")?,
        height: row.try_get("height")?,
        is_stale: row.try_get("is_stale")?,
        play_count,
        random_play_count,
        last_played_at: row.try_get("last_played_at")?,
        tags: Vec::new(),
        created_at: row.try_get("created_at")?,
        updated_at: row.try_get("updated_at")?,
        score: video_score(play_count, random_play_count, play_weight),
    })
}

async fn load_video_tags(
    pool: &PgPool,
    video_id: i64,
) -> Result<Vec<VideoTagSummary>, VideoQueryError> {
    let rows = sqlx::query(
        r#"
        SELECT t.id, t.name, COALESCE(t.color, '') AS color
        FROM tags t
        JOIN video_tags vt ON vt.tag_id = t.id
        WHERE vt.video_id = $1 AND t.deleted_at IS NULL
        ORDER BY t.name ASC, t.id ASC
        "#,
    )
    .bind(video_id)
    .fetch_all(pool)
    .await?;

    rows.into_iter()
        .map(|row| {
            Ok(VideoTagSummary {
                id: row.try_get("id")?,
                name: row.try_get("name")?,
                color: row.try_get("color")?,
            })
        })
        .collect()
}

pub async fn seed_video_query_fixture(database_url: &str) -> Result<PgPool, VideoQueryError> {
    let pool = PgPoolOptions::new()
        .max_connections(1)
        .connect(database_url)
        .await?;
    let schema_name = format!("video_query_fixture_{}", uuid::Uuid::new_v4().simple());

    sqlx::query(&format!("CREATE SCHEMA {schema_name}"))
        .execute(&pool)
        .await?;
    sqlx::query(&format!("SET search_path TO {schema_name}"))
        .execute(&pool)
        .await?;

    let statements = [
        r#"
        CREATE TABLE settings (
            id BIGSERIAL PRIMARY KEY,
            play_weight DOUBLE PRECISION NOT NULL DEFAULT 2.0,
            auto_scan_interval_seconds INTEGER NOT NULL DEFAULT 43200
        )
        "#,
        r#"
        CREATE TABLE videos (
            id BIGSERIAL PRIMARY KEY,
            name TEXT NOT NULL,
            path TEXT NOT NULL,
            directory TEXT NOT NULL,
            size BIGINT NOT NULL DEFAULT 0,
            duration DOUBLE PRECISION NOT NULL DEFAULT 0,
            resolution TEXT NOT NULL DEFAULT '',
            width INTEGER NOT NULL DEFAULT 0,
            height INTEGER NOT NULL DEFAULT 0,
            is_stale BOOLEAN NOT NULL DEFAULT false,
            play_count INTEGER NOT NULL DEFAULT 0,
            random_play_count INTEGER NOT NULL DEFAULT 0,
            last_played_at TIMESTAMPTZ NULL,
            created_at TIMESTAMPTZ NULL DEFAULT now(),
            updated_at TIMESTAMPTZ NULL DEFAULT now(),
            deleted_at TIMESTAMPTZ NULL
        )
        "#,
        r#"
        CREATE TABLE tags (
            id BIGSERIAL PRIMARY KEY,
            name TEXT NOT NULL,
            color TEXT NOT NULL DEFAULT '',
            deleted_at TIMESTAMPTZ NULL
        )
        "#,
        r#"
        CREATE TABLE video_tags (
            video_id BIGINT NOT NULL,
            tag_id BIGINT NOT NULL
        )
        "#,
    ];

    for statement in statements {
        sqlx::query(statement).execute(&pool).await?;
    }

    sqlx::query("INSERT INTO settings(id, play_weight) VALUES (1, 2.0)")
        .execute(&pool)
        .await?;
    sqlx::query(
        r#"
        INSERT INTO tags(id, name, color, deleted_at) VALUES
            (10, 'sport', '#ffffff', NULL),
            (11, 'sleep', '#111111', NULL)
        "#,
    )
    .execute(&pool)
    .await?;
    sqlx::query(
        r#"
        INSERT INTO videos(id, name, path, directory, size, height, play_count, random_play_count, deleted_at) VALUES
            (1, 'zero-small.mp4', '/tmp/zero-small.mp4', '/tmp', 10, 720, 0, 0, NULL),
            (2, 'two-large.mp4', '/tmp/two-large.mp4', '/tmp', 1000, 1080, 1, 0, NULL),
            (3, 'zero-large.mp4', '/tmp/zero-large.mp4', '/tmp', 100, 1080, 0, 0, NULL),
            (4, 'deleted.mp4', '/tmp/deleted.mp4', '/tmp', 500, 1080, 0, 0, now()),
            (5, 'cat_run.mp4', '/tmp/cat_run.mp4', '/tmp', 99, 720, 2, 0, NULL),
            (6, 'cat_sleep.mp4', '/tmp/cat_sleep.mp4', '/tmp', 101, 720, 2, 0, NULL)
        "#,
    )
    .execute(&pool)
    .await?;
    sqlx::query(
        r#"
        INSERT INTO video_tags(video_id, tag_id) VALUES
            (5, 10),
            (6, 11)
        "#,
    )
    .execute(&pool)
    .await?;

    Ok(pool)
}

pub async fn seed_video_file_operation_fixture(
    database_url: &str,
) -> Result<PgPool, VideoQueryError> {
    let pool = PgPoolOptions::new()
        .max_connections(1)
        .connect(database_url)
        .await?;
    let schema_name = format!("video_file_fixture_{}", uuid::Uuid::new_v4().simple());

    sqlx::query(&format!("CREATE SCHEMA {schema_name}"))
        .execute(&pool)
        .await?;
    sqlx::query(&format!("SET search_path TO {schema_name}"))
        .execute(&pool)
        .await?;

    let statements = [
        r#"
        CREATE TABLE settings (
            id BIGSERIAL PRIMARY KEY,
            play_weight DOUBLE PRECISION NOT NULL DEFAULT 2.0,
            auto_scan_interval_seconds INTEGER NOT NULL DEFAULT 43200
        )
        "#,
        r#"
        CREATE TABLE videos (
            id BIGSERIAL PRIMARY KEY,
            name TEXT NOT NULL,
            path TEXT NOT NULL,
            directory TEXT NOT NULL,
            size BIGINT NOT NULL DEFAULT 0,
            duration DOUBLE PRECISION NOT NULL DEFAULT 0,
            resolution TEXT NOT NULL DEFAULT '',
            width INTEGER NOT NULL DEFAULT 0,
            height INTEGER NOT NULL DEFAULT 0,
            is_stale BOOLEAN NOT NULL DEFAULT false,
            play_count INTEGER NOT NULL DEFAULT 0,
            random_play_count INTEGER NOT NULL DEFAULT 0,
            last_played_at TIMESTAMPTZ NULL,
            created_at TIMESTAMPTZ NULL DEFAULT now(),
            updated_at TIMESTAMPTZ NULL DEFAULT now(),
            deleted_at TIMESTAMPTZ NULL
        )
        "#,
        r#"
        CREATE UNIQUE INDEX idx_videos_path_active ON videos(path) WHERE deleted_at IS NULL
        "#,
        r#"
        CREATE TABLE tags (
            id BIGSERIAL PRIMARY KEY,
            name TEXT NOT NULL,
            color TEXT NOT NULL DEFAULT '',
            deleted_at TIMESTAMPTZ NULL
        )
        "#,
        r#"
        CREATE TABLE video_tags (
            video_id BIGINT NOT NULL,
            tag_id BIGINT NOT NULL
        )
        "#,
    ];

    for statement in statements {
        sqlx::query(statement).execute(&pool).await?;
    }

    sqlx::query("INSERT INTO settings(id, play_weight) VALUES (1, 2.0)")
        .execute(&pool)
        .await?;
    sqlx::query(
        "INSERT INTO tags(id, name, color, deleted_at) VALUES (10, 'sport', '#ffffff', NULL)",
    )
    .execute(&pool)
    .await?;

    Ok(pool)
}

pub async fn seed_library_management_fixture(
    database_url: &str,
) -> Result<PgPool, VideoQueryError> {
    let pool = PgPoolOptions::new()
        .max_connections(1)
        .connect(database_url)
        .await?;
    let schema_name = format!(
        "library_management_fixture_{}",
        uuid::Uuid::new_v4().simple()
    );

    sqlx::query(&format!("CREATE SCHEMA {schema_name}"))
        .execute(&pool)
        .await?;
    sqlx::query(&format!("SET search_path TO {schema_name}"))
        .execute(&pool)
        .await?;

    let statements = [
        r#"
        CREATE TABLE videos (
            id BIGSERIAL PRIMARY KEY,
            name TEXT NOT NULL DEFAULT '',
            path TEXT NOT NULL DEFAULT '',
            directory TEXT NOT NULL DEFAULT '',
            deleted_at TIMESTAMPTZ NULL
        )
        "#,
        r#"
        CREATE TABLE tags (
            id BIGSERIAL PRIMARY KEY,
            name TEXT NOT NULL UNIQUE,
            color TEXT NOT NULL DEFAULT '',
            created_at TIMESTAMPTZ NULL DEFAULT now(),
            updated_at TIMESTAMPTZ NULL DEFAULT now(),
            deleted_at TIMESTAMPTZ NULL
        )
        "#,
        r#"
        CREATE TABLE video_tags (
            video_id BIGINT NOT NULL,
            tag_id BIGINT NOT NULL,
            UNIQUE(video_id, tag_id)
        )
        "#,
        r#"
        CREATE TABLE settings (
            id BIGSERIAL PRIMARY KEY,
            confirm_before_delete BOOLEAN NOT NULL DEFAULT false,
            delete_original_file BOOLEAN NOT NULL DEFAULT false,
            video_extensions TEXT NOT NULL DEFAULT '',
            play_weight DOUBLE PRECISION NOT NULL DEFAULT 2.0,
            auto_scan_on_startup BOOLEAN NOT NULL DEFAULT false,
            auto_scan_interval_seconds INTEGER NOT NULL DEFAULT 43200,
            short_feed_max_duration_minutes INTEGER NOT NULL DEFAULT 5,
            theme TEXT NOT NULL DEFAULT 'system',
            log_enabled BOOLEAN NOT NULL DEFAULT false,
            bilingual_enabled BOOLEAN NOT NULL DEFAULT false,
            bilingual_lang TEXT NOT NULL DEFAULT 'zh',
            deepl_api_key TEXT NOT NULL DEFAULT '',
            ai_tagging_base_url TEXT NOT NULL DEFAULT '',
            ai_tagging_api_key TEXT NOT NULL DEFAULT '',
            ai_tagging_model TEXT NOT NULL DEFAULT '',
            ai_tagging_frame_count INTEGER NOT NULL DEFAULT 5,
            ai_tagging_subtitle_char_limit INTEGER NOT NULL DEFAULT 4000,
            ai_tagging_startup_batch_size INTEGER NOT NULL DEFAULT 10,
            updated_at TIMESTAMPTZ NULL DEFAULT now()
        )
        "#,
        r#"
        CREATE TABLE scan_directories (
            id BIGSERIAL PRIMARY KEY,
            path TEXT NOT NULL,
            alias TEXT NOT NULL DEFAULT '',
            created_at TIMESTAMPTZ NULL DEFAULT now(),
            updated_at TIMESTAMPTZ NULL DEFAULT now(),
            deleted_at TIMESTAMPTZ NULL
        )
        "#,
    ];

    for statement in statements {
        sqlx::query(statement).execute(&pool).await?;
    }

    sqlx::query("INSERT INTO settings(id) VALUES (1)")
        .execute(&pool)
        .await?;
    sqlx::query("INSERT INTO videos(id, name, path, directory) VALUES (1, 'movie.mp4', '/library/movie.mp4', '/library')")
        .execute(&pool)
        .await?;

    Ok(pool)
}

pub async fn seed_remaining_slices_fixture(
    database_url: &str,
    root: &Path,
) -> Result<PgPool, VideoQueryError> {
    let pool = PgPoolOptions::new()
        .max_connections(1)
        .connect(database_url)
        .await?;
    let schema_name = format!("remaining_slices_fixture_{}", uuid::Uuid::new_v4().simple());

    sqlx::query(&format!("CREATE SCHEMA {schema_name}"))
        .execute(&pool)
        .await?;
    sqlx::query(&format!("SET search_path TO {schema_name}"))
        .execute(&pool)
        .await?;

    let statements = [
        r#"
        CREATE TABLE settings (
            id BIGSERIAL PRIMARY KEY,
            play_weight DOUBLE PRECISION NOT NULL DEFAULT 2.0,
            short_feed_max_duration_minutes INTEGER NOT NULL DEFAULT 5,
            video_extensions TEXT NOT NULL DEFAULT '',
            auto_scan_interval_seconds INTEGER NOT NULL DEFAULT 43200,
            theme TEXT NOT NULL DEFAULT 'system',
            bilingual_enabled BOOLEAN NOT NULL DEFAULT false,
            bilingual_lang TEXT NOT NULL DEFAULT 'zh',
            deepl_api_key TEXT NOT NULL DEFAULT '',
            ai_tagging_api_key TEXT NOT NULL DEFAULT '',
            ai_tagging_frame_count INTEGER NOT NULL DEFAULT 5,
            ai_tagging_subtitle_char_limit INTEGER NOT NULL DEFAULT 4000,
            ai_tagging_startup_batch_size INTEGER NOT NULL DEFAULT 10
        )
        "#,
        r#"
        CREATE TABLE videos (
            id BIGSERIAL PRIMARY KEY,
            name TEXT NOT NULL,
            path TEXT NOT NULL,
            directory TEXT NOT NULL,
            size BIGINT NOT NULL DEFAULT 0,
            duration DOUBLE PRECISION NOT NULL DEFAULT 0,
            resolution TEXT NOT NULL DEFAULT '',
            width INTEGER NOT NULL DEFAULT 0,
            height INTEGER NOT NULL DEFAULT 0,
            is_stale BOOLEAN NOT NULL DEFAULT false,
            play_count INTEGER NOT NULL DEFAULT 0,
            random_play_count INTEGER NOT NULL DEFAULT 0,
            last_played_at TIMESTAMPTZ NULL,
            created_at TIMESTAMPTZ NULL DEFAULT now(),
            updated_at TIMESTAMPTZ NULL DEFAULT now(),
            deleted_at TIMESTAMPTZ NULL
        )
        "#,
        r#"
        CREATE TABLE tags (
            id BIGSERIAL PRIMARY KEY,
            name TEXT NOT NULL UNIQUE,
            color TEXT NOT NULL DEFAULT '',
            created_at TIMESTAMPTZ NULL DEFAULT now(),
            updated_at TIMESTAMPTZ NULL DEFAULT now(),
            deleted_at TIMESTAMPTZ NULL
        )
        "#,
        r#"
        CREATE TABLE video_tags (
            video_id BIGINT NOT NULL,
            tag_id BIGINT NOT NULL,
            UNIQUE(video_id, tag_id)
        )
        "#,
        r#"
        CREATE TABLE subtitle_segments (
            id BIGSERIAL PRIMARY KEY,
            video_id BIGINT NOT NULL,
            segment_index INTEGER NOT NULL,
            start_time_ms BIGINT NOT NULL,
            end_time_ms BIGINT NOT NULL,
            text TEXT NOT NULL,
            subtitle_path TEXT NOT NULL,
            subtitle_mod_time BIGINT NOT NULL,
            created_at TIMESTAMPTZ NULL DEFAULT now(),
            updated_at TIMESTAMPTZ NULL DEFAULT now(),
            UNIQUE(video_id, segment_index)
        )
        "#,
        r#"
        CREATE TABLE subtitle_index_states (
            id BIGSERIAL PRIMARY KEY,
            video_id BIGINT NOT NULL UNIQUE,
            subtitle_path TEXT NOT NULL,
            subtitle_mod_time BIGINT NOT NULL,
            subtitle_size BIGINT NOT NULL,
            segment_count INTEGER NOT NULL,
            last_checked_at TIMESTAMPTZ NULL DEFAULT now(),
            created_at TIMESTAMPTZ NULL DEFAULT now(),
            updated_at TIMESTAMPTZ NULL DEFAULT now()
        )
        "#,
        r#"
        CREATE TABLE ai_tag_candidates (
            id BIGSERIAL PRIMARY KEY,
            video_id BIGINT NOT NULL,
            suggested_name TEXT NOT NULL,
            normalized_name TEXT NOT NULL,
            matched_tag_id BIGINT NULL,
            confidence TEXT NOT NULL,
            reasoning TEXT NOT NULL DEFAULT '',
            source_summary TEXT NOT NULL DEFAULT '',
            status TEXT NOT NULL DEFAULT 'pending',
            created_at TIMESTAMPTZ NULL DEFAULT now(),
            updated_at TIMESTAMPTZ NULL DEFAULT now(),
            approved_at TIMESTAMPTZ NULL,
            rejected_at TIMESTAMPTZ NULL
        )
        "#,
        r#"
        CREATE TABLE ai_tag_approval_records (
            id BIGSERIAL PRIMARY KEY,
            video_id BIGINT NOT NULL,
            tag_id BIGINT NOT NULL,
            candidate_id BIGINT NOT NULL UNIQUE,
            created_at TIMESTAMPTZ NULL DEFAULT now(),
            UNIQUE(video_id, tag_id)
        )
        "#,
        r#"
        CREATE TABLE ai_tagging_states (
            id BIGSERIAL PRIMARY KEY,
            video_id BIGINT NOT NULL UNIQUE,
            status TEXT NOT NULL DEFAULT 'pending',
            skip_reason TEXT NOT NULL DEFAULT '',
            evidence_fingerprint TEXT NOT NULL DEFAULT '',
            attempt_count INTEGER NOT NULL DEFAULT 0,
            last_error TEXT NOT NULL DEFAULT '',
            last_processed_at TIMESTAMPTZ NULL,
            created_at TIMESTAMPTZ NULL DEFAULT now(),
            updated_at TIMESTAMPTZ NULL DEFAULT now()
        )
        "#,
        r#"
        CREATE TABLE short_feed_interactions (
            id BIGSERIAL PRIMARY KEY,
            video_id BIGINT NOT NULL UNIQUE,
            liked BOOLEAN NOT NULL DEFAULT false,
            favorited BOOLEAN NOT NULL DEFAULT false,
            view_count INTEGER NOT NULL DEFAULT 0,
            last_viewed_at TIMESTAMPTZ NULL,
            liked_at TIMESTAMPTZ NULL,
            favorited_at TIMESTAMPTZ NULL,
            created_at TIMESTAMPTZ NULL DEFAULT now(),
            updated_at TIMESTAMPTZ NULL DEFAULT now()
        )
        "#,
        r#"
        CREATE TABLE short_feed_tag_preferences (
            id BIGSERIAL PRIMARY KEY,
            tag_id BIGINT NOT NULL UNIQUE,
            score DOUBLE PRECISION NOT NULL DEFAULT 0,
            created_at TIMESTAMPTZ NULL DEFAULT now(),
            updated_at TIMESTAMPTZ NULL DEFAULT now()
        )
        "#,
    ];
    for statement in statements {
        sqlx::query(statement).execute(&pool).await?;
    }

    let short_a = root.join("short-a.mp4").to_string_lossy().to_string();
    let short_b = root.join("short-b.mp4").to_string_lossy().to_string();
    let long = root.join("long.mp4").to_string_lossy().to_string();
    let root_text = root.to_string_lossy().to_string();
    sqlx::query(
        "INSERT INTO settings(id, short_feed_max_duration_minutes, deepl_api_key, ai_tagging_api_key) VALUES (1, 5, 'deepl-secret', 'ai-secret')",
    )
    .execute(&pool)
    .await?;
    sqlx::query(
        r#"
        INSERT INTO tags(id, name, color, deleted_at) VALUES
            (10, 'city', '#0D9488', NULL)
        "#,
    )
    .execute(&pool)
    .await?;
    sqlx::query(
        r#"
        INSERT INTO videos(id, name, path, directory, size, duration, resolution, width, height, is_stale, deleted_at)
        VALUES
            (1, 'short-a.mp4', $1, $4, 12, 120.0, '1280x720', 1280, 720, false, NULL),
            (2, 'short-b.mp4', $2, $4, 12, 120.0, '320x240', 320, 240, false, NULL),
            (3, 'long.mp4', $3, $4, 4, 900.0, '1920x1080', 1920, 1080, false, NULL)
        "#,
    )
    .bind(short_a)
    .bind(short_b)
    .bind(long)
    .bind(root_text)
    .execute(&pool)
    .await?;
    sqlx::query("INSERT INTO video_tags(video_id, tag_id) VALUES (1, 10)")
        .execute(&pool)
        .await?;

    Ok(pool)
}

#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct LegacySchemaSnapshot {
    pub tables: Vec<String>,
    pub indexes: Vec<String>,
    pub extensions: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct LegacySchemaStatus {
    pub compatible: bool,
    pub required_tables: &'static [&'static str],
    pub required_indexes: &'static [&'static str],
    pub required_extensions: &'static [&'static str],
    pub optional_indexes: &'static [&'static str],
    pub optional_extensions: &'static [&'static str],
    pub missing_tables: Vec<String>,
    pub missing_indexes: Vec<String>,
    pub missing_extensions: Vec<String>,
    pub missing_optional_indexes: Vec<String>,
    pub missing_optional_extensions: Vec<String>,
}

pub fn legacy_schema_status(snapshot: &LegacySchemaSnapshot) -> LegacySchemaStatus {
    let tables = normalized_set(&snapshot.tables);
    let indexes = normalized_set(&snapshot.indexes);
    let extensions = normalized_set(&snapshot.extensions);

    let missing_tables = missing_values(REQUIRED_LEGACY_TABLES, &tables);
    let missing_indexes = missing_values(REQUIRED_LEGACY_INDEXES, &indexes);
    let missing_extensions = missing_values(REQUIRED_LEGACY_EXTENSIONS, &extensions);
    let missing_optional_indexes = missing_values(OPTIONAL_LEGACY_INDEXES, &indexes);
    let missing_optional_extensions = missing_values(OPTIONAL_LEGACY_EXTENSIONS, &extensions);
    let compatible =
        missing_tables.is_empty() && missing_indexes.is_empty() && missing_extensions.is_empty();

    LegacySchemaStatus {
        compatible,
        required_tables: REQUIRED_LEGACY_TABLES,
        required_indexes: REQUIRED_LEGACY_INDEXES,
        required_extensions: REQUIRED_LEGACY_EXTENSIONS,
        optional_indexes: OPTIONAL_LEGACY_INDEXES,
        optional_extensions: OPTIONAL_LEGACY_EXTENSIONS,
        missing_tables,
        missing_indexes,
        missing_extensions,
        missing_optional_indexes,
        missing_optional_extensions,
    }
}

fn normalized_set(values: &[String]) -> BTreeSet<String> {
    values
        .iter()
        .map(|value| value.trim().to_ascii_lowercase())
        .filter(|value| !value.is_empty())
        .collect()
}

fn missing_values(required: &[&str], actual: &BTreeSet<String>) -> Vec<String> {
    required
        .iter()
        .copied()
        .filter(|value| !actual.contains(*value))
        .map(str::to_string)
        .collect()
}
