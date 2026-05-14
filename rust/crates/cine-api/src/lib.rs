//! Private API contracts shared by daemon and clients.

use serde::{Deserialize, Serialize};

pub use cine_domain::{VideoCursor, VideoSummary, VideoTagSummary};

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct HealthResponse {
    pub service: String,
    pub status: String,
    pub version: String,
    pub app_compat_version: String,
    pub schema: SchemaHealth,
    pub database: DatabaseHealth,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct SchemaHealth {
    pub status: String,
    pub required_tables: Vec<String>,
    pub missing_tables: Vec<String>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct DatabaseHealth {
    pub configured: bool,
    pub connected: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub host: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub database: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct VideoFilterRequest {
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

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct VideoListResponse {
    pub videos: Vec<VideoSummary>,
    pub next_cursor: Option<VideoCursor>,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct RandomCandidateResponse {
    pub video: Option<VideoSummary>,
    pub reason_code: Option<String>,
    pub user_message: Option<String>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct AddVideoRequest {
    pub path: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct ScanDirectoryRequest {
    pub path: String,
    #[serde(default)]
    pub extensions: Option<String>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct RenameVideoRequest {
    pub name: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct RelocateVideoRequest {
    pub path: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct DeleteVideoRequest {
    #[serde(default)]
    pub delete_file: bool,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct BatchVideoRequest {
    pub video_ids: Vec<i64>,
    #[serde(default)]
    pub delete_file: bool,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct BatchVideoTagRequest {
    pub video_ids: Vec<i64>,
    pub tag_id: i64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct BatchVideoOperationError {
    pub video_id: i64,
    pub error: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct BatchVideoOperationResult {
    pub requested: usize,
    pub succeeded: usize,
    pub failed: usize,
    pub errors: Vec<BatchVideoOperationError>,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct VideoMutationResponse {
    pub video: Option<VideoSummary>,
    pub ok: bool,
    pub reason_code: Option<String>,
    pub user_message: Option<String>,
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
pub struct PreviewSessionResponse {
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

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct TagRecord {
    pub id: i64,
    pub name: String,
    pub color: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct TagMutationRequest {
    pub name: String,
    #[serde(default)]
    pub color: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct TagListResponse {
    pub tags: Vec<TagRecord>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct VideoTagMutationRequest {
    pub tag_id: i64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct ScanDirectoryRecord {
    pub id: i64,
    pub path: String,
    pub alias: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct ScanDirectoryMutationRequest {
    pub path: String,
    #[serde(default)]
    pub alias: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct ScanDirectoryListResponse {
    pub directories: Vec<ScanDirectoryRecord>,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
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

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct SettingsUpdateRequest {
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

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct ScannedFileResponse {
    pub path: String,
    pub size: i64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct ScanDirectoryResponse {
    pub files: Vec<ScannedFileResponse>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct ScanSyncErrorRecord {
    pub operation: String,
    pub directory: Option<String>,
    pub path: Option<String>,
    pub error: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct ScanSyncResponse {
    pub directories: usize,
    pub scanned: usize,
    pub added: usize,
    pub deleted: usize,
    pub relocated: usize,
    pub metadata_refreshed: usize,
    pub skipped: usize,
    pub errors: Vec<ScanSyncErrorRecord>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct SubtitleSegmentRecord {
    pub index: i32,
    pub start_time_ms: i64,
    pub end_time_ms: i64,
    pub text: String,
    pub lines: Vec<String>,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct SubtitleSearchMatch {
    pub video: VideoSummary,
    pub segment: SubtitleSegmentRecord,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct SubtitleSearchResponse {
    pub matches: Vec<SubtitleSearchMatch>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct IndexSubtitleRequest {
    pub path: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct SubtitleIndexStateRecord {
    pub video_id: i64,
    pub subtitle_path: String,
    pub subtitle_mod_time: i64,
    pub subtitle_size: i64,
    pub segment_count: i32,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum SubtitleEngine {
    Whisperx,
    Qwen,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct SubtitleEngineStatus {
    pub engine: SubtitleEngine,
    pub display_name: String,
    pub supported: bool,
    pub available: bool,
    pub needs_prepare: bool,
    pub prepare_mode: String,
    pub reason_code: String,
    pub source_lang_mode: String,
    pub reason_message: String,
    pub prepare_hint: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct SubtitlePrepareRequest {
    pub engine: SubtitleEngine,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct SubtitleGenerateRequest {
    pub video_id: i64,
    pub engine: SubtitleEngine,
    #[serde(default)]
    pub source_lang: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct SubtitleGenerateResult {
    pub status: String,
    pub video_id: i64,
    pub path: Option<String>,
    pub message: Option<String>,
    pub validation_code: Option<String>,
    pub force_eligible: bool,
    pub engine: Option<SubtitleEngine>,
    pub source_lang: Option<String>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct SubtitleProgressRecord {
    pub action: String,
    pub engine: Option<SubtitleEngine>,
    pub phase: String,
    pub percent: i32,
    pub message: String,
    pub cancellable: bool,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct SubtitleJobStatus {
    pub running: bool,
    pub completed: bool,
    pub cancelled: bool,
    pub progress: SubtitleProgressRecord,
    pub result: Option<SubtitleGenerateResult>,
    pub error: Option<String>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum AITagCandidateStatus {
    Pending,
    Approved,
    Rejected,
    Superseded,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct AITagCandidateInput {
    pub video_id: i64,
    pub suggested_name: String,
    pub normalized_name: String,
    pub matched_tag_id: Option<i64>,
    pub confidence: String,
    pub reasoning: String,
    pub source_summary: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
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

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct AITagCandidateListResponse {
    pub candidates: Vec<AITagCandidateRecord>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct RejectAITagCandidatesByVideoResponse {
    pub rejected: i64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct AITaggingStatusSummary {
    pub config_available: bool,
    pub pending: i64,
    pub processing: i64,
    pub completed: i64,
    pub skipped: i64,
    pub failed: i64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct ShortFeedServerStatus {
    pub running: bool,
    pub bind_address: String,
    pub port: i32,
    pub url: String,
    pub lan_urls: Vec<String>,
    pub startup_error: String,
    pub fallback_used: bool,
    pub allowed_access: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct FrontendLogRequest {
    pub level: String,
    pub source: String,
    pub message: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct ShortFeedFeedbackRequest {
    pub liked: Option<bool>,
    pub favorited: Option<bool>,
    #[serde(default)]
    pub viewed: bool,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct ShortFeedInteractionRecord {
    pub video_id: i64,
    pub liked: bool,
    pub favorited: bool,
    pub view_count: i32,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct ShortFeedVideoRecord {
    pub id: i64,
    pub name: String,
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

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct CleanupDuplicateGroup {
    pub original_id: i64,
    pub candidate_ids: Vec<i64>,
    pub reason: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct CleanupAnalysisRecord {
    pub duplicate_groups: Vec<CleanupDuplicateGroup>,
    pub low_duration_ids: Vec<i64>,
    pub low_resolution_ids: Vec<i64>,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct CleanupProgressRecord {
    pub stage: String,
    pub message: String,
    pub current: i32,
    pub total: i32,
    pub path: String,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct CleanupStatus {
    pub running: bool,
    pub completed: bool,
    pub error: String,
    pub progress: CleanupProgressRecord,
    pub analysis: Option<CleanupAnalysisRecord>,
    pub started_at: Option<String>,
    pub updated_at: Option<String>,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct CleanupAnalyzeRequest {
    pub max_duration_seconds: f64,
    pub min_width: i32,
    pub min_height: i32,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct DiagnosticsSnapshot {
    pub video_count: i64,
    pub tag_count: i64,
    pub subtitle_segment_count: i64,
    pub ai_candidate_count: i64,
    pub short_feed_interaction_count: i64,
    pub redacted_settings: PublicSettings,
}
