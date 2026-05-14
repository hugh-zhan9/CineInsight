//! Rust daemon foundation for the native CineInsight replacement.

use std::{
    fs,
    net::{IpAddr, Ipv4Addr, SocketAddr},
    path::{Path as StdPath, PathBuf},
    process::{Command, Output},
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc, Mutex,
    },
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use anyhow::Context;
use axum::{
    body::Bytes,
    extract::{Path, Query, Request, State},
    http::{header::AUTHORIZATION, header::CONTENT_TYPE, HeaderMap, StatusCode},
    middleware::{self, Next},
    response::{IntoResponse, Redirect, Response},
    routing::{get, post},
    Json, Router,
};
use cine_api::{
    AITagCandidateInput as ApiAITagCandidateInput, AITagCandidateListResponse,
    AITagCandidateRecord as ApiAITagCandidateRecord,
    AITagCandidateStatus as ApiAITagCandidateStatus, AddVideoRequest, BatchVideoOperationError,
    BatchVideoOperationResult, BatchVideoRequest, BatchVideoTagRequest,
    CleanupAnalysisRecord as ApiCleanupAnalysisRecord, CleanupAnalyzeRequest,
    CleanupDuplicateGroup as ApiCleanupDuplicateGroup, CleanupProgressRecord, CleanupStatus,
    DatabaseHealth, DeleteVideoRequest, DiagnosticsSnapshot as ApiDiagnosticsSnapshot,
    FrontendLogRequest, HealthResponse, IndexSubtitleRequest,
    PlaybackAttemptResponse as ApiPlaybackAttemptResponse, PreviewSessionResponse, PublicSettings,
    RandomCandidateResponse, RejectAITagCandidatesByVideoResponse, RelocateVideoRequest,
    RenameVideoRequest, ScanDirectoryListResponse, ScanDirectoryMutationRequest,
    ScanDirectoryRecord, ScanDirectoryRequest, ScanDirectoryResponse, ScanSyncErrorRecord,
    ScanSyncResponse, ScannedFileResponse, SchemaHealth, SettingsUpdateRequest,
    ShortFeedFeedbackRequest, ShortFeedInteractionRecord as ApiShortFeedInteractionRecord,
    ShortFeedServerStatus, ShortFeedVideoRecord as ApiShortFeedVideoRecord, SubtitleEngine,
    SubtitleEngineStatus, SubtitleGenerateRequest, SubtitleGenerateResult,
    SubtitleIndexStateRecord as ApiSubtitleIndexStateRecord, SubtitleJobStatus,
    SubtitlePrepareRequest, SubtitleProgressRecord, SubtitleSearchMatch as ApiSubtitleSearchMatch,
    SubtitleSearchResponse, SubtitleSegmentRecord as ApiSubtitleSegmentRecord, TagListResponse,
    TagMutationRequest, TagRecord, VideoFilterRequest, VideoListResponse, VideoMutationResponse,
    VideoTagMutationRequest,
};
use cine_db::{
    add_scan_directory as mutate_add_scan_directory, add_video as mutate_add_video,
    ai_tagging_status_summary as query_ai_tagging_status_summary,
    approve_ai_tag_candidate as mutate_approve_ai_tag_candidate,
    assign_tag_to_video as mutate_assign_tag_to_video,
    create_ai_tag_candidate as mutate_create_ai_tag_candidate, create_tag as mutate_create_tag,
    delete_scan_directory as mutate_delete_scan_directory, delete_tag as mutate_delete_tag,
    delete_video as mutate_delete_video, diagnostics_snapshot as query_diagnostics_snapshot,
    get_public_settings as query_public_settings,
    get_subtitle_generation_settings as query_subtitle_generation_settings,
    get_subtitle_segments as query_subtitle_segments,
    index_subtitle_file as mutate_index_subtitle_file,
    list_ai_tag_candidates as query_ai_tag_candidates,
    list_scan_directories as query_scan_directories, list_tags as query_tags,
    list_videos as query_list_videos, list_videos_by_directory as query_videos_by_directory,
    load_pg_config_from_env, next_short_feed_video as query_next_short_feed_video,
    normalize_auto_scan_interval_seconds, open_video_directory_with_dispatch,
    play_video_with_dispatch, preview_externally_with_dispatch,
    preview_session as query_preview_session, random_candidate as query_random_candidate,
    record_short_feed_feedback as mutate_short_feed_feedback,
    refresh_video_metadata_with_probe as mutate_refresh_video_metadata,
    reject_ai_tag_candidate as mutate_reject_ai_tag_candidate,
    reject_pending_ai_tag_candidates_by_video as mutate_reject_ai_tag_candidates_by_video,
    relocate_video as mutate_relocate_video, remove_tag_from_video as mutate_remove_tag_from_video,
    rename_video as mutate_rename_video, retry_ai_tagging as mutate_retry_ai_tagging,
    scan_directory_with_extensions, search_subtitle_matches as query_subtitle_matches,
    short_feed_favorite_videos as query_short_feed_favorites,
    short_feed_media as query_short_feed_media, start_cleanup_analysis as query_cleanup_analysis,
    sync_scan_directories_with_probe as mutate_sync_scan_directories,
    update_scan_directory as mutate_update_scan_directory, update_settings as mutate_settings,
    update_tag as mutate_update_tag,
    video_and_subtitle_paths_for_video as query_video_and_subtitle_paths_for_video,
    AITagCandidateInput, AITagCandidateStatus, LibraryManagementError, MediaMetadata,
    NativeSliceError, PgConfig, PlaybackDispatch, PlaybackError, PreviewMode, SettingsUpdate,
    ShortFeedFeedback, SystemPlaybackDispatch, TagMutationError, VideoFilter, VideoMutationError,
    DEFAULT_AUTO_SCAN_INTERVAL_SECONDS, REQUIRED_LEGACY_TABLES,
};
use serde::Deserialize;
use sqlx::{
    postgres::{PgConnectOptions, PgPoolOptions, PgSslMode},
    PgPool,
};

type DeeplTranslator = Arc<dyn Fn(Vec<String>, String) -> Vec<String> + Send + Sync>;

#[derive(Clone, Debug)]
pub struct DaemonConfig {
    pub token: String,
    pub pool: Option<PgPool>,
    pub enable_system_dispatch: bool,
    pub asr_sidecar_dir: Option<PathBuf>,
    pub asr_python_bin: Option<PathBuf>,
    pub asr_runtime_dir: Option<PathBuf>,
    pub short_feed_assets_dir: Option<PathBuf>,
    pub short_feed_bind_address: String,
    pub short_feed_port_start: u16,
    pub short_feed_port_end: u16,
    pub skip_audio_extract: bool,
}

impl DaemonConfig {
    pub async fn from_env() -> anyhow::Result<Self> {
        let token = std::env::var("CINE_DAEMON_TOKEN")
            .ok()
            .map(|value| value.trim().to_string())
            .filter(|value| !value.is_empty())
            .context("CINE_DAEMON_TOKEN cannot be empty")?;
        let pool = match load_pg_config_from_env() {
            Ok(config) => Some(connect_pool(&config).await.context("connect PostgreSQL")?),
            Err(error) => {
                eprintln!("cine-daemon starting without PostgreSQL: {error:#}");
                None
            }
        };

        Ok(Self {
            token,
            pool,
            enable_system_dispatch: true,
            asr_sidecar_dir: env_path("CINE_SIDECAR_DIR"),
            asr_python_bin: env_path("CINE_PYTHON").or_else(|| env_path("PYTHON_BIN")),
            asr_runtime_dir: env_path("CINE_RUNTIME_DIR"),
            short_feed_assets_dir: env_path("CINE_SHORT_FEED_ASSETS_DIR")
                .or_else(default_short_feed_assets_dir),
            short_feed_bind_address: std::env::var("CINE_SHORT_FEED_BIND")
                .unwrap_or_else(|_| "0.0.0.0".to_string()),
            short_feed_port_start: env_u16("CINE_SHORT_FEED_PORT_START").unwrap_or(18088),
            short_feed_port_end: env_u16("CINE_SHORT_FEED_PORT_END").unwrap_or(18108),
            skip_audio_extract: std::env::var("CINE_SKIP_AUDIO_EXTRACT")
                .map(|value| value == "1" || value.eq_ignore_ascii_case("true"))
                .unwrap_or(false),
        })
    }
}

#[derive(Clone)]
pub struct DaemonState {
    token: String,
    version: String,
    app_compat_version: String,
    pool: Option<PgPool>,
    enable_system_dispatch: bool,
    cleanup_status: Arc<Mutex<CleanupStatus>>,
    subtitle_status: Arc<Mutex<SubtitleJobStatus>>,
    subtitle_cancel_requested: Arc<AtomicBool>,
    subtitle_child: Arc<Mutex<Option<u32>>>,
    asr_sidecar_dir: Option<PathBuf>,
    asr_python_bin: Option<PathBuf>,
    asr_runtime_dir: Option<PathBuf>,
    short_feed_assets_dir: Option<PathBuf>,
    short_feed_bind_address: String,
    short_feed_port_start: u16,
    short_feed_port_end: u16,
    short_feed_status: Arc<Mutex<ShortFeedServerStatus>>,
    skip_audio_extract: bool,
    deepl_translator: Option<DeeplTranslator>,
}

#[derive(Clone)]
struct ShortFeedHttpState {
    state: DaemonState,
    assets_dir: PathBuf,
}

impl DaemonState {
    pub fn new(token: impl Into<String>) -> Self {
        Self {
            token: token.into(),
            version: env!("CARGO_PKG_VERSION").to_string(),
            app_compat_version: "0.1".to_string(),
            pool: None,
            enable_system_dispatch: false,
            cleanup_status: Arc::new(Mutex::new(default_cleanup_status())),
            subtitle_status: Arc::new(Mutex::new(default_subtitle_status())),
            subtitle_cancel_requested: Arc::new(AtomicBool::new(false)),
            subtitle_child: Arc::new(Mutex::new(None)),
            asr_sidecar_dir: None,
            asr_python_bin: None,
            asr_runtime_dir: None,
            short_feed_assets_dir: None,
            short_feed_bind_address: "0.0.0.0".to_string(),
            short_feed_port_start: 18088,
            short_feed_port_end: 18108,
            short_feed_status: Arc::new(Mutex::new(default_short_feed_status())),
            skip_audio_extract: false,
            deepl_translator: None,
        }
    }

    pub fn for_test(token: impl Into<String>) -> Self {
        Self::new(token)
    }

    pub fn with_pool_for_test(token: impl Into<String>, pool: PgPool) -> Self {
        Self {
            pool: Some(pool),
            ..Self::new(token)
        }
    }

    pub fn from_config(config: DaemonConfig) -> Self {
        Self {
            pool: config.pool,
            enable_system_dispatch: config.enable_system_dispatch,
            asr_sidecar_dir: config.asr_sidecar_dir,
            asr_python_bin: config.asr_python_bin,
            asr_runtime_dir: config.asr_runtime_dir,
            short_feed_assets_dir: config.short_feed_assets_dir,
            short_feed_bind_address: config.short_feed_bind_address,
            short_feed_port_start: config.short_feed_port_start,
            short_feed_port_end: config.short_feed_port_end,
            skip_audio_extract: config.skip_audio_extract,
            ..Self::new(config.token)
        }
    }

    pub fn with_asr_sidecar_for_test(
        mut self,
        sidecar_dir: impl Into<PathBuf>,
        python_bin: impl Into<PathBuf>,
        skip_audio_extract: bool,
    ) -> Self {
        self.asr_sidecar_dir = Some(sidecar_dir.into());
        self.asr_python_bin = Some(python_bin.into());
        self.skip_audio_extract = skip_audio_extract;
        self
    }

    pub fn with_asr_runtime_for_test(mut self, runtime_dir: impl Into<PathBuf>) -> Self {
        self.asr_runtime_dir = Some(runtime_dir.into());
        self
    }

    pub fn with_short_feed_server_for_test(
        mut self,
        assets_dir: impl Into<PathBuf>,
        bind_address: impl Into<String>,
        port_start: u16,
        port_end: u16,
    ) -> Self {
        self.short_feed_assets_dir = Some(assets_dir.into());
        self.short_feed_bind_address = bind_address.into();
        self.short_feed_port_start = port_start;
        self.short_feed_port_end = port_end;
        self
    }

    pub fn with_deepl_translator_for_test<F>(mut self, translator: F) -> Self
    where
        F: Fn(Vec<String>, String) -> Vec<String> + Send + Sync + 'static,
    {
        self.deepl_translator = Some(Arc::new(translator));
        self
    }

    fn bearer_value(&self) -> String {
        format!("Bearer {}", self.token)
    }

    fn ensure_startup_auto_scan_started(&self) {
        let Some(pool) = self.pool.clone() else {
            return;
        };

        let Ok(handle) = tokio::runtime::Handle::try_current() else {
            tracing::warn!("skipping startup auto scan because no tokio runtime is active");
            return;
        };

        handle.spawn(async move {
            run_auto_scan_loop(pool).await;
        });
    }

    fn ensure_short_feed_server_started(&self) {
        let Some(assets_dir) = self.short_feed_assets_dir.clone() else {
            return;
        };
        if self
            .short_feed_status
            .lock()
            .expect("short feed status lock")
            .running
        {
            return;
        }

        let bind_address = self.short_feed_bind_address.clone();
        let port_start = self.short_feed_port_start;
        let port_end = if self.short_feed_port_end < port_start {
            port_start
        } else {
            self.short_feed_port_end
        };
        let state = self.clone();
        match start_short_feed_http_server(state, assets_dir, &bind_address, port_start, port_end) {
            Ok(status) => {
                *self
                    .short_feed_status
                    .lock()
                    .expect("short feed status lock") = status;
            }
            Err(message) => {
                *self
                    .short_feed_status
                    .lock()
                    .expect("short feed status lock") = ShortFeedServerStatus {
                    running: false,
                    bind_address,
                    port: 0,
                    url: String::new(),
                    lan_urls: Vec::new(),
                    startup_error: message,
                    fallback_used: false,
                    allowed_access: short_feed_allowed_access(),
                };
            }
        }
    }
}

pub fn app(state: DaemonState) -> Router {
    state.ensure_startup_auto_scan_started();
    state.ensure_short_feed_server_started();
    Router::new()
        .route("/health", get(health))
        .route("/api/logs/frontend", post(log_frontend))
        .route("/api/videos", get(list_videos))
        .route("/api/videos/search", post(search_videos))
        .route("/api/videos/by-directory", get(videos_by_directory))
        .route("/api/videos/random-candidate", post(random_candidate))
        .route("/api/videos/scan", post(scan_directory))
        .route("/api/videos/add", post(add_video))
        .route("/api/videos/batch/delete", post(batch_delete_videos))
        .route("/api/videos/batch/tags/add", post(batch_add_video_tag))
        .route(
            "/api/videos/batch/tags/remove",
            post(batch_remove_video_tag),
        )
        .route(
            "/api/videos/batch/refresh-metadata",
            post(batch_refresh_video_metadata),
        )
        .route("/api/videos/{id}/rename", post(rename_video))
        .route("/api/videos/{id}/relocate", post(relocate_video))
        .route(
            "/api/videos/{id}/refresh-metadata",
            post(refresh_video_metadata),
        )
        .route("/api/videos/{id}/delete", post(delete_video))
        .route("/api/videos/{id}/open-directory", post(open_directory))
        .route("/api/videos/{id}/preview-session", get(preview_session))
        .route(
            "/api/videos/{id}/preview-externally",
            post(preview_externally),
        )
        .route("/api/videos/{id}/play", post(play_video))
        .route("/api/videos/random-play", post(play_random_video))
        .route("/api/tags", get(list_tags).post(create_tag))
        .route("/api/tags/{id}", post(update_tag))
        .route("/api/tags/{id}/delete", post(delete_tag))
        .route("/api/videos/{id}/tags", post(assign_video_tag))
        .route("/api/videos/{id}/tags/delete", post(remove_video_tag))
        .route("/api/settings", get(get_settings).post(update_settings))
        .route(
            "/api/scan-directories",
            get(list_scan_directories).post(add_scan_directory),
        )
        .route("/api/scan-directories/sync", post(sync_scan_directories))
        .route("/api/scan-directories/{id}", post(update_scan_directory))
        .route(
            "/api/scan-directories/{id}/delete",
            post(delete_scan_directory),
        )
        .route("/api/subtitles/engines", get(subtitle_engine_statuses))
        .route("/api/subtitles/prepare", post(prepare_subtitle_engine))
        .route(
            "/api/subtitles/dependencies",
            get(check_subtitle_dependencies),
        )
        .route(
            "/api/subtitles/dependencies/download",
            post(download_subtitle_dependencies),
        )
        .route("/api/subtitles/generate", post(generate_subtitle))
        .route(
            "/api/subtitles/force-generate",
            post(force_generate_subtitle),
        )
        .route("/api/subtitles/status", get(subtitle_status))
        .route("/api/subtitles/cancel", post(cancel_subtitle))
        .route("/api/subtitles/search", get(search_subtitles))
        .route("/api/videos/{id}/subtitles", get(get_video_subtitles))
        .route(
            "/api/videos/{id}/subtitles/index",
            post(index_video_subtitle),
        )
        .route(
            "/api/ai-tags/candidates",
            get(list_ai_tag_candidates).post(create_ai_tag_candidate),
        )
        .route(
            "/api/ai-tags/candidates/{id}/approve",
            post(approve_ai_tag_candidate),
        )
        .route(
            "/api/ai-tags/candidates/{id}/reject",
            post(reject_ai_tag_candidate),
        )
        .route(
            "/api/ai-tags/videos/{id}/reject-pending",
            post(reject_ai_tag_candidates_by_video),
        )
        .route("/api/ai-tags/videos/{id}/retry", post(retry_ai_tagging))
        .route(
            "/api/ai-tags/status-summary",
            get(ai_tagging_status_summary),
        )
        .route("/api/short-feed/status", get(short_feed_status))
        .route("/api/short-feed/next", get(next_short_feed_video))
        .route(
            "/api/short-feed/videos/{id}/feedback",
            post(record_short_feed_feedback),
        )
        .route("/api/cleanup/analyze", post(analyze_cleanup))
        .route("/api/cleanup/start", post(start_cleanup))
        .route("/api/cleanup/status", get(cleanup_status))
        .route("/api/diagnostics", get(diagnostics))
        .route_layer(middleware::from_fn_with_state(
            state.clone(),
            require_bearer,
        ))
        .with_state(state)
}

fn short_feed_app(state: DaemonState, assets_dir: PathBuf) -> Router {
    Router::new()
        .route("/short", get(short_feed_redirect))
        .route("/short/", get(short_feed_index))
        .route("/assets/{*path}", get(short_feed_static_asset))
        .route("/short-api/status", get(short_feed_public_status))
        .route("/short-api/feed/next", get(short_feed_public_next))
        .route("/short-api/favorites", get(short_feed_favorites))
        .route("/short-api/videos/{id}/play", post(short_feed_public_play))
        .route("/short-api/videos/{id}/like", post(short_feed_public_like))
        .route(
            "/short-api/videos/{id}/favorite",
            post(short_feed_public_favorite),
        )
        .route(
            "/short-api/videos/{id}/delete",
            post(short_feed_public_delete),
        )
        .route("/short-media/{id}", get(short_feed_media))
        .route_layer(middleware::from_fn(short_feed_private_lan_only))
        .with_state(ShortFeedHttpState { state, assets_dir })
}

pub async fn serve_listener(
    listener: tokio::net::TcpListener,
    config: DaemonConfig,
) -> anyhow::Result<()> {
    axum::serve(listener, app(DaemonState::from_config(config)))
        .await
        .context("serve daemon")
}

pub async fn serve_from_env() -> anyhow::Result<()> {
    let config = DaemonConfig::from_env().await?;
    let port = std::env::var("CINE_DAEMON_PORT")
        .ok()
        .as_deref()
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .unwrap_or("0")
        .parse::<u16>()
        .context("CINE_DAEMON_PORT must be a valid u16")?;
    let listener = tokio::net::TcpListener::bind(SocketAddr::from((Ipv4Addr::LOCALHOST, port)))
        .await
        .context("bind daemon listener")?;
    println!("cine-daemon listening on {}", listener.local_addr()?);

    serve_listener(listener, config).await
}

async fn require_bearer(
    State(state): State<DaemonState>,
    request: Request,
    next: Next,
) -> Result<Response, StatusCode> {
    let expected = state.bearer_value();
    let authorized = request
        .headers()
        .get(AUTHORIZATION)
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| value == expected);

    if !authorized {
        return Err(StatusCode::UNAUTHORIZED);
    }

    Ok(next.run(request).await)
}

async fn health(State(state): State<DaemonState>) -> Json<HealthResponse> {
    Json(HealthResponse {
        service: "cine-daemon".to_string(),
        status: "ok".to_string(),
        version: state.version,
        app_compat_version: state.app_compat_version,
        schema: SchemaHealth {
            status: "unchecked".to_string(),
            required_tables: REQUIRED_LEGACY_TABLES
                .iter()
                .map(|value| value.to_string())
                .collect(),
            missing_tables: Vec::new(),
        },
        database: DatabaseHealth {
            configured: false,
            connected: false,
            host: None,
            database: None,
            error: None,
        },
    })
}

async fn log_frontend(Json(request): Json<FrontendLogRequest>) -> StatusCode {
    let level = sanitize_log_field(&request.level);
    let source = sanitize_log_field(&request.source);
    let message = request.message.replace(['\r', '\n'], " ");
    tracing::info!(target: "frontend", level = %level, source = %source, "{}", message);
    StatusCode::NO_CONTENT
}

async fn list_videos(
    State(state): State<DaemonState>,
) -> Result<Json<VideoListResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Ok(Json(VideoListResponse {
            videos: Vec::new(),
            next_cursor: None,
        }));
    };

    query_list_videos(pool, VideoFilter::default())
        .await
        .map(Json)
        .map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)
}

async fn search_videos(
    State(state): State<DaemonState>,
    body: Bytes,
) -> Result<Json<VideoListResponse>, StatusCode> {
    let filter = parse_video_filter(body).map_err(|_| StatusCode::BAD_REQUEST)?;

    let Some(pool) = state.pool.as_ref() else {
        return Ok(Json(VideoListResponse {
            videos: Vec::new(),
            next_cursor: None,
        }));
    };

    query_list_videos(pool, filter)
        .await
        .map(Json)
        .map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)
}

#[derive(Deserialize)]
struct DirectoryQuery {
    path: String,
}

async fn videos_by_directory(
    State(state): State<DaemonState>,
    Query(query): Query<DirectoryQuery>,
) -> Result<Json<VideoListResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Ok(Json(VideoListResponse {
            videos: Vec::new(),
            next_cursor: None,
        }));
    };

    query_videos_by_directory(pool, query.path)
        .await
        .map(|videos| VideoListResponse {
            videos,
            next_cursor: None,
        })
        .map(Json)
        .map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)
}

fn parse_video_filter(body: Bytes) -> Result<VideoFilter, serde_json::Error> {
    if body.is_empty() {
        Ok(VideoFilter::default())
    } else {
        serde_json::from_slice::<VideoFilterRequest>(&body).map(video_filter_from_request)
    }
}

async fn random_candidate(
    State(state): State<DaemonState>,
) -> Result<Json<RandomCandidateResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Ok(Json(RandomCandidateResponse {
            video: None,
            reason_code: Some("not_configured".to_string()),
            user_message: Some(
                "Video repository is not configured in this foundation route.".to_string(),
            ),
        }));
    };

    query_random_candidate(pool)
        .await
        .map(Json)
        .map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)
}

async fn add_video(
    State(state): State<DaemonState>,
    Json(request): Json<AddVideoRequest>,
) -> Result<Json<VideoMutationResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Ok(Json(mutation_error(
            "not_configured",
            "Video repository is not configured in this daemon.",
        )));
    };

    mutate_add_video(pool, request.path)
        .await
        .map(mutation_ok)
        .map(Json)
        .map_err(status_for_mutation_error)
}

async fn batch_delete_videos(
    State(state): State<DaemonState>,
    Json(request): Json<BatchVideoRequest>,
) -> Result<Json<BatchVideoOperationResult>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    let mut result = BatchAccumulator::new(&request.video_ids);
    for id in request.video_ids {
        result.record(id, mutate_delete_video(pool, id, request.delete_file).await);
    }
    Ok(Json(result.finish()))
}

async fn batch_add_video_tag(
    State(state): State<DaemonState>,
    Json(request): Json<BatchVideoTagRequest>,
) -> Result<Json<BatchVideoOperationResult>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    let mut result = BatchAccumulator::new(&request.video_ids);
    for id in request.video_ids {
        result.record(
            id,
            mutate_assign_tag_to_video(pool, id, request.tag_id).await,
        );
    }
    Ok(Json(result.finish()))
}

async fn batch_remove_video_tag(
    State(state): State<DaemonState>,
    Json(request): Json<BatchVideoTagRequest>,
) -> Result<Json<BatchVideoOperationResult>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    let mut result = BatchAccumulator::new(&request.video_ids);
    for id in request.video_ids {
        result.record(
            id,
            mutate_remove_tag_from_video(pool, id, request.tag_id).await,
        );
    }
    Ok(Json(result.finish()))
}

async fn batch_refresh_video_metadata(
    State(state): State<DaemonState>,
    Json(request): Json<BatchVideoRequest>,
) -> Result<Json<BatchVideoOperationResult>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    let mut result = BatchAccumulator::new(&request.video_ids);
    for id in request.video_ids {
        result.record(
            id,
            mutate_refresh_video_metadata(pool, id, system_media_metadata).await,
        );
    }
    Ok(Json(result.finish()))
}

async fn scan_directory(
    Json(request): Json<ScanDirectoryRequest>,
) -> Result<Json<ScanDirectoryResponse>, StatusCode> {
    let files = scan_directory_with_extensions(
        request.path,
        request.extensions.as_deref().unwrap_or_default(),
    )
    .map_err(status_for_mutation_error)?
    .into_iter()
    .map(|file| ScannedFileResponse {
        path: file.path,
        size: file.size,
    })
    .collect();

    Ok(Json(ScanDirectoryResponse { files }))
}

async fn rename_video(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
    Json(request): Json<RenameVideoRequest>,
) -> Result<Json<VideoMutationResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Ok(Json(mutation_error(
            "not_configured",
            "Video repository is not configured in this daemon.",
        )));
    };

    mutate_rename_video(pool, id, &request.name)
        .await
        .map(mutation_ok)
        .map(Json)
        .map_err(status_for_mutation_error)
}

async fn relocate_video(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
    Json(request): Json<RelocateVideoRequest>,
) -> Result<Json<VideoMutationResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Ok(Json(mutation_error(
            "not_configured",
            "Video repository is not configured in this daemon.",
        )));
    };

    mutate_relocate_video(pool, id, request.path)
        .await
        .map(mutation_ok)
        .map(Json)
        .map_err(status_for_mutation_error)
}

async fn refresh_video_metadata(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
) -> Result<Json<VideoMutationResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Ok(Json(mutation_error(
            "not_configured",
            "Video repository is not configured in this daemon.",
        )));
    };

    mutate_refresh_video_metadata(pool, id, system_media_metadata)
        .await
        .map(mutation_ok)
        .map(Json)
        .map_err(status_for_mutation_error)
}

async fn delete_video(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
    Json(request): Json<DeleteVideoRequest>,
) -> Result<Json<VideoMutationResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Ok(Json(mutation_error(
            "not_configured",
            "Video repository is not configured in this daemon.",
        )));
    };

    mutate_delete_video(pool, id, request.delete_file)
        .await
        .map(|()| VideoMutationResponse {
            video: None,
            ok: true,
            reason_code: None,
            user_message: None,
        })
        .map(Json)
        .map_err(status_for_mutation_error)
}

async fn open_directory(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
) -> Result<StatusCode, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    let mut dispatch = dispatch_for_state(&state);
    open_video_directory_with_dispatch(pool, id, &mut dispatch)
        .await
        .map(|()| StatusCode::NO_CONTENT)
        .map_err(status_for_mutation_error)
}

async fn preview_session(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
) -> Result<Json<PreviewSessionResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    query_preview_session(pool, id)
        .await
        .map(api_preview_session)
        .map(Json)
        .map_err(status_for_mutation_error)
}

async fn preview_externally(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
) -> Result<StatusCode, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    let mut dispatch = dispatch_for_state(&state);
    preview_externally_with_dispatch(pool, id, &mut dispatch)
        .await
        .map(|()| StatusCode::NO_CONTENT)
        .map_err(status_for_mutation_error)
}

async fn play_video(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
) -> Result<Json<ApiPlaybackAttemptResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    let mut dispatch = dispatch_for_state(&state);
    play_video_with_dispatch(pool, id, false, &mut dispatch)
        .await
        .map(api_playback_attempt)
        .map(Json)
        .map_err(status_for_mutation_error)
}

async fn play_random_video(
    State(state): State<DaemonState>,
) -> Result<Json<ApiPlaybackAttemptResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    let candidate = query_random_candidate(pool)
        .await
        .map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?;
    let Some(video) = candidate.video else {
        return Ok(Json(ApiPlaybackAttemptResponse {
            video: None,
            dispatch_succeeded: false,
            user_message: candidate.user_message,
            reason_code: candidate.reason_code,
            reconcile_result: None,
        }));
    };

    let mut dispatch = dispatch_for_state(&state);
    play_video_with_dispatch(pool, video.id, true, &mut dispatch)
        .await
        .map(api_playback_attempt)
        .map(Json)
        .map_err(status_for_mutation_error)
}

async fn list_tags(State(state): State<DaemonState>) -> Result<Json<TagListResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    query_tags(pool)
        .await
        .map(|tags| TagListResponse {
            tags: tags.into_iter().map(api_tag_record).collect(),
        })
        .map(Json)
        .map_err(status_for_tag_error)
}

async fn create_tag(
    State(state): State<DaemonState>,
    Json(request): Json<TagMutationRequest>,
) -> Result<Json<TagRecord>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    mutate_create_tag(pool, &request.name, &request.color)
        .await
        .map(api_tag_record)
        .map(Json)
        .map_err(status_for_tag_error)
}

async fn update_tag(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
    Json(request): Json<TagMutationRequest>,
) -> Result<Json<TagRecord>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    mutate_update_tag(pool, id, &request.name, &request.color)
        .await
        .map(api_tag_record)
        .map(Json)
        .map_err(status_for_tag_error)
}

async fn delete_tag(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
) -> Result<StatusCode, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    mutate_delete_tag(pool, id)
        .await
        .map(|()| StatusCode::NO_CONTENT)
        .map_err(status_for_tag_error)
}

async fn assign_video_tag(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
    Json(request): Json<VideoTagMutationRequest>,
) -> Result<StatusCode, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    mutate_assign_tag_to_video(pool, id, request.tag_id)
        .await
        .map(|()| StatusCode::NO_CONTENT)
        .map_err(status_for_tag_error)
}

async fn remove_video_tag(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
    Json(request): Json<VideoTagMutationRequest>,
) -> Result<StatusCode, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    mutate_remove_tag_from_video(pool, id, request.tag_id)
        .await
        .map(|()| StatusCode::NO_CONTENT)
        .map_err(status_for_tag_error)
}

async fn get_settings(
    State(state): State<DaemonState>,
) -> Result<Json<PublicSettings>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Ok(Json(default_public_settings()));
    };

    query_public_settings(pool)
        .await
        .map(api_public_settings)
        .map(Json)
        .map_err(status_for_library_error)
}

async fn update_settings(
    State(state): State<DaemonState>,
    Json(request): Json<SettingsUpdateRequest>,
) -> Result<Json<PublicSettings>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    mutate_settings(pool, db_settings_update(request))
        .await
        .map_err(status_for_library_error)?;
    query_public_settings(pool)
        .await
        .map(api_public_settings)
        .map(Json)
        .map_err(status_for_library_error)
}

async fn list_scan_directories(
    State(state): State<DaemonState>,
) -> Result<Json<ScanDirectoryListResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    query_scan_directories(pool)
        .await
        .map(|directories| ScanDirectoryListResponse {
            directories: directories
                .into_iter()
                .map(api_scan_directory_record)
                .collect(),
        })
        .map(Json)
        .map_err(status_for_library_error)
}

async fn add_scan_directory(
    State(state): State<DaemonState>,
    Json(request): Json<ScanDirectoryMutationRequest>,
) -> Result<Json<ScanDirectoryRecord>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    mutate_add_scan_directory(pool, &request.path, &request.alias)
        .await
        .map(api_scan_directory_record)
        .map(Json)
        .map_err(status_for_library_error)
}

async fn update_scan_directory(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
    Json(request): Json<ScanDirectoryMutationRequest>,
) -> Result<Json<ScanDirectoryRecord>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    mutate_update_scan_directory(pool, id, &request.path, &request.alias)
        .await
        .map(api_scan_directory_record)
        .map(Json)
        .map_err(status_for_library_error)
}

async fn delete_scan_directory(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
) -> Result<StatusCode, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    mutate_delete_scan_directory(pool, id)
        .await
        .map(|()| StatusCode::NO_CONTENT)
        .map_err(status_for_library_error)
}

async fn sync_scan_directories(
    State(state): State<DaemonState>,
) -> Result<Json<ScanSyncResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    let directories = query_scan_directories(pool)
        .await
        .map_err(status_for_library_error)?;
    let settings = query_public_settings(pool)
        .await
        .map_err(status_for_library_error)?;
    let roots = directories
        .into_iter()
        .map(|directory| PathBuf::from(directory.path))
        .collect::<Vec<_>>();

    mutate_sync_scan_directories(
        pool,
        &roots,
        &settings.video_extensions,
        system_media_metadata,
    )
    .await
    .map(api_scan_sync_response)
    .map(Json)
    .map_err(status_for_mutation_error)
}

async fn run_auto_scan_loop(pool: PgPool) {
    loop {
        let interval_seconds = match run_auto_scan_once(pool.clone()).await {
            Ok(interval_seconds) => interval_seconds,
            Err(error) => {
                tracing::warn!("auto scan skipped: {error}");
                DEFAULT_AUTO_SCAN_INTERVAL_SECONDS
            }
        };
        tokio::time::sleep(Duration::from_secs(interval_seconds as u64)).await;
    }
}

async fn run_auto_scan_once(pool: PgPool) -> anyhow::Result<i32> {
    let settings = query_public_settings(&pool)
        .await
        .map_err(|error| anyhow::anyhow!("load settings: {error}"))?;
    let interval_seconds =
        normalize_auto_scan_interval_seconds(settings.auto_scan_interval_seconds);
    if !settings.auto_scan_on_startup {
        return Ok(interval_seconds);
    }

    let directories = query_scan_directories(&pool)
        .await
        .map_err(|error| anyhow::anyhow!("load scan directories: {error}"))?;
    if directories.is_empty() {
        return Ok(interval_seconds);
    }

    let roots = directories
        .into_iter()
        .map(|directory| PathBuf::from(directory.path))
        .collect::<Vec<_>>();
    match mutate_sync_scan_directories(
        &pool,
        &roots,
        &settings.video_extensions,
        system_media_metadata,
    )
    .await
    {
        Ok(result) => {
            if result.errors.is_empty() {
                tracing::info!(
                    directories = result.directories,
                    scanned = result.scanned,
                    added = result.added,
                    relocated = result.relocated,
                    deleted = result.deleted,
                    metadata_refreshed = result.metadata_refreshed,
                    "auto scan completed"
                );
            } else {
                tracing::warn!(
                    directories = result.directories,
                    scanned = result.scanned,
                    added = result.added,
                    relocated = result.relocated,
                    deleted = result.deleted,
                    metadata_refreshed = result.metadata_refreshed,
                    errors = result.errors.len(),
                    "auto scan completed with errors"
                );
            }
        }
        Err(error) => {
            tracing::warn!("auto scan failed: {error}");
        }
    }
    Ok(interval_seconds)
}

#[derive(Deserialize)]
struct SubtitleSearchQuery {
    keyword: String,
    limit: Option<i64>,
}

async fn subtitle_engine_statuses(
    State(state): State<DaemonState>,
) -> Json<Vec<SubtitleEngineStatus>> {
    Json(vec![
        subtitle_engine_status(&state, SubtitleEngine::Whisperx),
        subtitle_engine_status(&state, SubtitleEngine::Qwen),
    ])
}

async fn prepare_subtitle_engine(
    State(state): State<DaemonState>,
    Json(request): Json<SubtitlePrepareRequest>,
) -> Result<StatusCode, StatusCode> {
    let status = subtitle_engine_status(&state, request.engine.clone());
    if status.supported {
        prepare_asr_runtime(&state, &request.engine).map_err(|error| {
            tracing::error!(%error, "prepare ASR runtime failed");
            StatusCode::INTERNAL_SERVER_ERROR
        })?;
        Ok(StatusCode::NO_CONTENT)
    } else {
        Err(StatusCode::UNPROCESSABLE_ENTITY)
    }
}

async fn check_subtitle_dependencies(
    State(state): State<DaemonState>,
) -> Json<std::collections::BTreeMap<String, bool>> {
    let mut result = std::collections::BTreeMap::new();
    result.insert("ffmpeg".to_string(), find_binary("ffmpeg").is_some());
    let whisper =
        runtime_is_prepared(&state, &SubtitleEngine::Whisperx) || find_binary("whisperx").is_some();
    result.insert("whisper".to_string(), whisper);
    result.insert("model".to_string(), whisper);
    Json(result)
}

async fn download_subtitle_dependencies(
    State(state): State<DaemonState>,
) -> Result<StatusCode, StatusCode> {
    prepare_asr_runtime(&state, &SubtitleEngine::Whisperx).map_err(|error| {
        tracing::error!(%error, "download subtitle dependencies failed");
        StatusCode::INTERNAL_SERVER_ERROR
    })?;
    Ok(StatusCode::NO_CONTENT)
}

async fn generate_subtitle(
    State(state): State<DaemonState>,
    Json(request): Json<SubtitleGenerateRequest>,
) -> Result<Json<SubtitleGenerateResult>, StatusCode> {
    generate_subtitle_inner(state, request, false).await
}

async fn force_generate_subtitle(
    State(state): State<DaemonState>,
    Json(request): Json<SubtitleGenerateRequest>,
) -> Result<Json<SubtitleGenerateResult>, StatusCode> {
    generate_subtitle_inner(state, request, true).await
}

async fn generate_subtitle_inner(
    state: DaemonState,
    request: SubtitleGenerateRequest,
    force_generate: bool,
) -> Result<Json<SubtitleGenerateResult>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };
    let (video_path, subtitle_path) =
        query_video_and_subtitle_paths_for_video(pool, request.video_id)
            .await
            .map_err(status_for_native_slice_error)?;
    let source_lang = normalize_source_lang(&request.source_lang);
    state
        .subtitle_cancel_requested
        .store(false, Ordering::SeqCst);
    set_subtitle_progress(
        &state,
        "generate",
        Some(request.engine.clone()),
        "checking",
        0,
        "初始化任务...",
        true,
    );
    if state.subtitle_cancel_requested.load(Ordering::SeqCst) {
        let result = cancelled_subtitle_result(
            request.video_id,
            Some(request.engine.clone()),
            Some(source_lang.clone()),
        );
        set_subtitle_result(&state, result.clone(), "cancelled", 0, "字幕生成已取消");
        return Ok(Json(result));
    }
    if !subtitle_path.is_file() {
        set_subtitle_progress(
            &state,
            "generate",
            Some(request.engine.clone()),
            "transcribing",
            20,
            "转写音频...",
            true,
        );
        let transcription = transcribe_to_srt(
            &state,
            &request.engine,
            &video_path,
            &subtitle_path,
            &source_lang,
        )
        .await;
        if let Err(error) = transcription {
            if state.subtitle_cancel_requested.load(Ordering::SeqCst) {
                let result = cancelled_subtitle_result(
                    request.video_id,
                    Some(request.engine.clone()),
                    Some(source_lang.clone()),
                );
                set_subtitle_result(&state, result.clone(), "cancelled", 0, "字幕生成已取消");
                return Ok(Json(result));
            }
            tracing::error!(%error, "ASR subtitle generation failed");
            return Err(StatusCode::INTERNAL_SERVER_ERROR);
        }
    }
    if state.subtitle_cancel_requested.load(Ordering::SeqCst) {
        let result = cancelled_subtitle_result(
            request.video_id,
            Some(request.engine.clone()),
            Some(source_lang.clone()),
        );
        set_subtitle_result(&state, result.clone(), "cancelled", 0, "字幕生成已取消");
        return Ok(Json(result));
    }
    if !force_generate {
        set_subtitle_progress(
            &state,
            "generate",
            Some(request.engine.clone()),
            "validating",
            50,
            "校验字幕输出...",
            true,
        );
        if let Some(result) = validate_generated_srt(
            request.video_id,
            &subtitle_path,
            &request.engine,
            &source_lang,
        ) {
            set_subtitle_result(&state, result.clone(), "validating", 50, "字幕校验未通过");
            return Ok(Json(result));
        }
    }
    if let Ok(settings) = query_subtitle_generation_settings(pool).await {
        if settings.bilingual_enabled
            && !settings.deepl_api_key.trim().is_empty()
            && !settings.bilingual_lang.trim().is_empty()
        {
            set_subtitle_progress(
                &state,
                "generate",
                Some(request.engine.clone()),
                "translating",
                70,
                "通过 DeepL 翻译字幕...",
                true,
            );
            if let Err(error) = maybe_merge_bilingual_srt(
                &state,
                &subtitle_path,
                &settings.bilingual_lang,
                &settings.deepl_api_key,
            ) {
                tracing::warn!(%error, "bilingual subtitle merge failed; keeping original SRT");
            }
        }
    }
    let indexed = mutate_index_subtitle_file(pool, request.video_id, subtitle_path.clone())
        .await
        .map_err(status_for_native_slice_error)?;
    let result = SubtitleGenerateResult {
        status: "success".to_string(),
        video_id: request.video_id,
        path: Some(indexed.subtitle_path),
        message: if force_generate {
            Some("Existing SRT indexed with force-generate request.".to_string())
        } else {
            None
        },
        validation_code: None,
        force_eligible: false,
        engine: Some(request.engine),
        source_lang: Some(source_lang),
    };
    set_subtitle_result(&state, result.clone(), "finalizing", 100, "完成收尾");
    Ok(Json(result))
}

async fn subtitle_status(State(state): State<DaemonState>) -> Json<SubtitleJobStatus> {
    Json(current_subtitle_status(&state))
}

async fn cancel_subtitle(State(state): State<DaemonState>) -> StatusCode {
    state
        .subtitle_cancel_requested
        .store(true, Ordering::SeqCst);
    if let Some(pid) = state.subtitle_child.lock().ok().and_then(|guard| *guard) {
        let _ = Command::new("kill")
            .arg("-TERM")
            .arg(pid.to_string())
            .status();
    }
    let result = cancelled_subtitle_result(0, None, None);
    let mut status = state.subtitle_status.lock().expect("subtitle status mutex");
    *status = SubtitleJobStatus {
        running: false,
        completed: false,
        cancelled: true,
        progress: SubtitleProgressRecord {
            action: "generate".to_string(),
            engine: None,
            phase: "cancelled".to_string(),
            percent: 0,
            message: "字幕生成已取消".to_string(),
            cancellable: false,
        },
        result: Some(result),
        error: None,
    };
    StatusCode::NO_CONTENT
}

async fn search_subtitles(
    State(state): State<DaemonState>,
    Query(query): Query<SubtitleSearchQuery>,
) -> Result<Json<SubtitleSearchResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    query_subtitle_matches(pool, &query.keyword, query.limit.unwrap_or(20))
        .await
        .map(|matches| SubtitleSearchResponse {
            matches: matches.into_iter().map(api_subtitle_match).collect(),
        })
        .map(Json)
        .map_err(status_for_native_slice_error)
}

async fn get_video_subtitles(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
) -> Result<Json<Vec<ApiSubtitleSegmentRecord>>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    query_subtitle_segments(pool, id)
        .await
        .map(|segments| segments.into_iter().map(api_subtitle_segment).collect())
        .map(Json)
        .map_err(status_for_native_slice_error)
}

async fn index_video_subtitle(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
    Json(request): Json<IndexSubtitleRequest>,
) -> Result<Json<ApiSubtitleIndexStateRecord>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    mutate_index_subtitle_file(pool, id, request.path)
        .await
        .map(api_subtitle_index_state)
        .map(Json)
        .map_err(status_for_native_slice_error)
}

async fn list_ai_tag_candidates(
    State(state): State<DaemonState>,
) -> Result<Json<AITagCandidateListResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    query_ai_tag_candidates(pool, None)
        .await
        .map(|candidates| AITagCandidateListResponse {
            candidates: candidates.into_iter().map(api_ai_candidate).collect(),
        })
        .map(Json)
        .map_err(status_for_native_slice_error)
}

async fn create_ai_tag_candidate(
    State(state): State<DaemonState>,
    Json(request): Json<ApiAITagCandidateInput>,
) -> Result<Json<ApiAITagCandidateRecord>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    mutate_create_ai_tag_candidate(pool, db_ai_candidate_input(request))
        .await
        .map(api_ai_candidate)
        .map(Json)
        .map_err(status_for_native_slice_error)
}

async fn approve_ai_tag_candidate(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
) -> Result<Json<ApiAITagCandidateRecord>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    mutate_approve_ai_tag_candidate(pool, id)
        .await
        .map(api_ai_candidate)
        .map(Json)
        .map_err(status_for_native_slice_error)
}

async fn reject_ai_tag_candidate(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
) -> Result<Json<ApiAITagCandidateRecord>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    mutate_reject_ai_tag_candidate(pool, id)
        .await
        .map(api_ai_candidate)
        .map(Json)
        .map_err(status_for_native_slice_error)
}

async fn reject_ai_tag_candidates_by_video(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
) -> Result<Json<RejectAITagCandidatesByVideoResponse>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    mutate_reject_ai_tag_candidates_by_video(pool, id)
        .await
        .map(|rejected| RejectAITagCandidatesByVideoResponse { rejected })
        .map(Json)
        .map_err(status_for_native_slice_error)
}

async fn retry_ai_tagging(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
) -> Result<StatusCode, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    mutate_retry_ai_tagging(pool, id)
        .await
        .map(|()| StatusCode::NO_CONTENT)
        .map_err(status_for_native_slice_error)
}

async fn ai_tagging_status_summary(
    State(state): State<DaemonState>,
) -> Result<Json<cine_api::AITaggingStatusSummary>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    query_ai_tagging_status_summary(pool)
        .await
        .map(Json)
        .map_err(status_for_native_slice_error)
}

async fn short_feed_status(State(state): State<DaemonState>) -> Json<ShortFeedServerStatus> {
    Json(current_short_feed_status(&state))
}

async fn next_short_feed_video(
    State(state): State<DaemonState>,
) -> Result<Json<ApiShortFeedVideoRecord>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    query_next_short_feed_video(pool, &[])
        .await
        .map(api_short_feed_video)
        .map(Json)
        .map_err(status_for_native_slice_error)
}

async fn record_short_feed_feedback(
    State(state): State<DaemonState>,
    Path(id): Path<i64>,
    Json(request): Json<ShortFeedFeedbackRequest>,
) -> Result<Json<ApiShortFeedInteractionRecord>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    mutate_short_feed_feedback(
        pool,
        id,
        ShortFeedFeedback {
            liked: request.liked,
            favorited: request.favorited,
            viewed: request.viewed,
        },
    )
    .await
    .map(api_short_feed_interaction)
    .map(Json)
    .map_err(status_for_native_slice_error)
}

fn start_short_feed_http_server(
    state: DaemonState,
    assets_dir: PathBuf,
    bind_address: &str,
    port_start: u16,
    port_end: u16,
) -> Result<ShortFeedServerStatus, String> {
    let end = if port_end < port_start {
        port_start
    } else {
        port_end
    };
    let mut last_error = String::new();
    for port in port_start..=end {
        let addr = format!("{bind_address}:{port}");
        let listener = match std::net::TcpListener::bind(&addr) {
            Ok(listener) => listener,
            Err(error) => {
                last_error = error.to_string();
                continue;
            }
        };
        let local_addr = listener
            .local_addr()
            .map_err(|error| format!("short feed server local addr failed: {error}"))?;
        listener
            .set_nonblocking(true)
            .map_err(|error| format!("short feed server nonblocking failed: {error}"))?;
        let listener = tokio::net::TcpListener::from_std(listener)
            .map_err(|error| format!("short feed server listener failed: {error}"))?;
        let selected_port = local_addr.port();
        let app = short_feed_app(state, assets_dir);
        tokio::spawn(async move {
            if let Err(error) = axum::serve(listener, app).await {
                tracing::warn!(%error, "short feed server stopped");
            }
        });
        return Ok(ShortFeedServerStatus {
            running: true,
            bind_address: bind_address.to_string(),
            port: i32::from(selected_port),
            url: format!("http://127.0.0.1:{selected_port}/short/"),
            lan_urls: short_feed_lan_urls(selected_port),
            startup_error: String::new(),
            fallback_used: selected_port != port_start,
            allowed_access: short_feed_allowed_access(),
        });
    }

    Err(format!(
        "short feed server failed to listen on ports {port_start}..{end}: {last_error}"
    ))
}

async fn short_feed_redirect() -> Redirect {
    Redirect::temporary("/short/")
}

async fn short_feed_index(State(state): State<ShortFeedHttpState>) -> Response {
    serve_short_feed_file(
        &state.assets_dir,
        "short.html",
        Some("text/html; charset=utf-8"),
    )
    .await
}

async fn short_feed_static_asset(
    State(state): State<ShortFeedHttpState>,
    Path(path): Path<String>,
) -> Response {
    serve_short_feed_file(&state.assets_dir, &format!("assets/{path}"), None).await
}

async fn short_feed_public_status(
    State(state): State<ShortFeedHttpState>,
) -> Json<ShortFeedServerStatus> {
    Json(current_short_feed_status(&state.state))
}

async fn serve_short_feed_file(
    assets_dir: &StdPath,
    relative_path: &str,
    content_type: Option<&str>,
) -> Response {
    if relative_path.contains("..") {
        return StatusCode::NOT_FOUND.into_response();
    }
    let path = assets_dir.join(relative_path);
    match tokio::fs::read(path).await {
        Ok(bytes) => {
            let mut headers = HeaderMap::new();
            if let Some(value) = content_type.or_else(|| mime_for_asset(relative_path)) {
                if let Ok(value) = value.parse() {
                    headers.insert(CONTENT_TYPE, value);
                }
            }
            (headers, bytes).into_response()
        }
        Err(_) => StatusCode::NOT_FOUND.into_response(),
    }
}

async fn short_feed_public_next(
    State(state): State<ShortFeedHttpState>,
    Query(query): Query<ShortFeedNextQuery>,
) -> Result<Json<ApiShortFeedVideoRecord>, StatusCode> {
    let Some(pool) = state.state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    query_next_short_feed_video(
        pool,
        &parse_short_feed_exclude_ids(query.exclude.as_deref()),
    )
    .await
    .map(api_short_feed_video)
    .map(Json)
    .map_err(status_for_native_slice_error)
}

async fn short_feed_favorites(
    State(state): State<ShortFeedHttpState>,
) -> Result<Json<serde_json::Value>, StatusCode> {
    let Some(pool) = state.state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };
    let videos = query_short_feed_favorites(pool)
        .await
        .map_err(status_for_native_slice_error)?
        .into_iter()
        .map(api_short_feed_video)
        .collect::<Vec<_>>();
    Ok(Json(serde_json::json!({ "videos": videos })))
}

async fn short_feed_public_play(
    State(state): State<ShortFeedHttpState>,
    Path(id): Path<i64>,
    Json(request): Json<ShortFeedPlayRequest>,
) -> Result<Json<ApiShortFeedInteractionRecord>, StatusCode> {
    if request.source != "short_feed" {
        return Err(StatusCode::BAD_REQUEST);
    }
    public_short_feed_feedback(
        &state.state,
        id,
        ShortFeedFeedback {
            liked: None,
            favorited: None,
            viewed: true,
        },
    )
    .await
}

async fn short_feed_public_like(
    State(state): State<ShortFeedHttpState>,
    Path(id): Path<i64>,
    Json(request): Json<ShortFeedLikeRequest>,
) -> Result<Json<ApiShortFeedInteractionRecord>, StatusCode> {
    public_short_feed_feedback(
        &state.state,
        id,
        ShortFeedFeedback {
            liked: Some(request.liked),
            favorited: None,
            viewed: false,
        },
    )
    .await
}

async fn short_feed_public_favorite(
    State(state): State<ShortFeedHttpState>,
    Path(id): Path<i64>,
    Json(request): Json<ShortFeedFavoriteRequest>,
) -> Result<Json<ApiShortFeedInteractionRecord>, StatusCode> {
    public_short_feed_feedback(
        &state.state,
        id,
        ShortFeedFeedback {
            liked: None,
            favorited: Some(request.favorited),
            viewed: false,
        },
    )
    .await
}

async fn short_feed_public_delete(
    State(state): State<ShortFeedHttpState>,
    Path(id): Path<i64>,
    Json(request): Json<ShortFeedDeleteRequest>,
) -> Result<Json<serde_json::Value>, StatusCode> {
    if !request.confirm_move_to_trash {
        return Err(StatusCode::BAD_REQUEST);
    }
    let Some(pool) = state.state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };
    mutate_delete_video(pool, id, true)
        .await
        .map_err(status_for_mutation_error)?;
    Ok(Json(serde_json::json!({ "deleted": true })))
}

async fn public_short_feed_feedback(
    state: &DaemonState,
    id: i64,
    feedback: ShortFeedFeedback,
) -> Result<Json<ApiShortFeedInteractionRecord>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };
    mutate_short_feed_feedback(pool, id, feedback)
        .await
        .map(api_short_feed_interaction)
        .map(Json)
        .map_err(status_for_native_slice_error)
}

async fn short_feed_media(
    State(state): State<ShortFeedHttpState>,
    Path(id): Path<i64>,
) -> Response {
    let Some(pool) = state.state.pool.as_ref() else {
        return StatusCode::SERVICE_UNAVAILABLE.into_response();
    };
    let media = match query_short_feed_media(pool, id).await {
        Ok(media) => media,
        Err(error) => return status_for_native_slice_error(error).into_response(),
    };
    let bytes = match tokio::fs::read(&media.path).await {
        Ok(bytes) => bytes,
        Err(_) => return StatusCode::NOT_FOUND.into_response(),
    };
    let mut headers = HeaderMap::new();
    if let Ok(value) = media.media_mime.parse() {
        headers.insert(CONTENT_TYPE, value);
    }
    (headers, bytes).into_response()
}

async fn short_feed_private_lan_only(request: Request, next: Next) -> Result<Response, StatusCode> {
    if let Some(addr) = request
        .extensions()
        .get::<axum::extract::ConnectInfo<SocketAddr>>()
        .map(|info| info.0)
    {
        if !short_feed_remote_allowed(addr.ip()) {
            return Err(StatusCode::FORBIDDEN);
        }
    }
    Ok(next.run(request).await)
}

#[derive(Deserialize)]
struct ShortFeedNextQuery {
    exclude: Option<String>,
}

#[derive(Deserialize)]
struct ShortFeedPlayRequest {
    source: String,
}

#[derive(Deserialize)]
struct ShortFeedLikeRequest {
    liked: bool,
}

#[derive(Deserialize)]
struct ShortFeedFavoriteRequest {
    favorited: bool,
}

#[derive(Deserialize)]
struct ShortFeedDeleteRequest {
    confirm_move_to_trash: bool,
}

fn parse_short_feed_exclude_ids(value: Option<&str>) -> Vec<i64> {
    value
        .unwrap_or_default()
        .split(',')
        .filter_map(|part| part.trim().parse::<i64>().ok())
        .filter(|id| *id > 0)
        .collect()
}

fn mime_for_asset(path: &str) -> Option<&'static str> {
    if path.ends_with(".js") {
        Some("text/javascript; charset=utf-8")
    } else if path.ends_with(".css") {
        Some("text/css; charset=utf-8")
    } else if path.ends_with(".html") {
        Some("text/html; charset=utf-8")
    } else {
        None
    }
}

fn short_feed_remote_allowed(ip: IpAddr) -> bool {
    match ip {
        IpAddr::V4(ip) => ip.is_loopback() || ip.is_private() || ip.is_link_local(),
        IpAddr::V6(ip) => ip.is_loopback() || ip.is_unicast_link_local(),
    }
}

fn short_feed_lan_urls(port: u16) -> Vec<String> {
    if let Ok(host) = std::env::var("CINE_SHORT_FEED_LAN_HOST") {
        let host = host.trim();
        if !host.is_empty() {
            return vec![format!("http://{host}:{port}/short/")];
        }
    }
    Vec::new()
}

fn short_feed_allowed_access() -> String {
    "loopback/private-lan/link-local only, no login".to_string()
}

async fn analyze_cleanup(
    State(state): State<DaemonState>,
    Json(request): Json<CleanupAnalyzeRequest>,
) -> Result<Json<ApiCleanupAnalysisRecord>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    query_cleanup_analysis(
        pool,
        request.max_duration_seconds,
        request.min_width,
        request.min_height,
    )
    .await
    .map(api_cleanup_analysis)
    .map(Json)
    .map_err(status_for_native_slice_error)
}

async fn start_cleanup(
    State(state): State<DaemonState>,
    Json(request): Json<CleanupAnalyzeRequest>,
) -> Result<Json<CleanupStatus>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    let started = timestamp_string();
    set_cleanup_status(
        &state,
        CleanupStatus {
            running: true,
            completed: false,
            error: String::new(),
            progress: CleanupProgressRecord {
                stage: "analyze".to_string(),
                message: "Cleanup analysis running".to_string(),
                current: 0,
                total: 0,
                path: String::new(),
            },
            analysis: None,
            started_at: Some(started.clone()),
            updated_at: Some(started.clone()),
        },
    );

    let analysis = query_cleanup_analysis(
        pool,
        request.max_duration_seconds,
        request.min_width,
        request.min_height,
    )
    .await
    .map(api_cleanup_analysis)
    .map_err(status_for_native_slice_error)?;
    let updated = timestamp_string();
    let status = CleanupStatus {
        running: false,
        completed: true,
        error: String::new(),
        progress: CleanupProgressRecord {
            stage: "done".to_string(),
            message: "Cleanup analysis completed".to_string(),
            current: 1,
            total: 1,
            path: String::new(),
        },
        analysis: Some(analysis),
        started_at: Some(started),
        updated_at: Some(updated),
    };
    set_cleanup_status(&state, status.clone());
    Ok(Json(status))
}

async fn cleanup_status(State(state): State<DaemonState>) -> Json<CleanupStatus> {
    Json(current_cleanup_status(&state))
}

async fn diagnostics(
    State(state): State<DaemonState>,
) -> Result<Json<ApiDiagnosticsSnapshot>, StatusCode> {
    let Some(pool) = state.pool.as_ref() else {
        return Err(StatusCode::SERVICE_UNAVAILABLE);
    };

    query_diagnostics_snapshot(pool)
        .await
        .map(api_diagnostics)
        .map(Json)
        .map_err(status_for_native_slice_error)
}

enum DaemonPlaybackDispatch {
    System(SystemPlaybackDispatch),
    Noop,
}

impl PlaybackDispatch for DaemonPlaybackDispatch {
    fn dispatch(&mut self, path: &std::path::Path) -> Result<(), PlaybackError> {
        match self {
            Self::System(dispatch) => dispatch.dispatch(path),
            Self::Noop => Ok(()),
        }
    }
}

fn dispatch_for_state(state: &DaemonState) -> DaemonPlaybackDispatch {
    if state.enable_system_dispatch {
        DaemonPlaybackDispatch::System(SystemPlaybackDispatch)
    } else {
        DaemonPlaybackDispatch::Noop
    }
}

fn api_preview_session(value: cine_db::PreviewSession) -> PreviewSessionResponse {
    PreviewSessionResponse {
        video_id: value.video_id,
        mode: match value.mode {
            PreviewMode::Inline => cine_api::PreviewMode::Inline,
            PreviewMode::ExternalPreview => cine_api::PreviewMode::ExternalPreview,
            PreviewMode::Unsupported => cine_api::PreviewMode::Unsupported,
        },
        display_name: value.display_name,
        inline_source: value
            .inline_source
            .map(|source| cine_api::PreviewSourceDescriptor {
                locator_strategy: source.locator_strategy,
                locator_value: source.locator_value,
                mime: source.mime,
            }),
        external_action: value
            .external_action
            .map(|action| cine_api::PreviewExternalAction {
                action_id: action.action_id,
                button_label: action.button_label,
                hint: action.hint,
            }),
        reason_code: value.reason_code,
        reason_message: value.reason_message,
    }
}

fn api_playback_attempt(value: cine_db::PlaybackAttemptResponse) -> ApiPlaybackAttemptResponse {
    ApiPlaybackAttemptResponse {
        video: value.video,
        dispatch_succeeded: value.dispatch_succeeded,
        user_message: value.user_message,
        reason_code: value.reason_code,
        reconcile_result: value
            .reconcile_result
            .map(|result| cine_api::PlaybackReconcileResult {
                video_id: result.video_id,
                did_mark_stale: result.did_mark_stale,
                did_relocate: result.did_relocate,
                did_refresh_metadata: result.did_refresh_metadata,
                needs_reload: result.needs_reload,
                updated_video: result.updated_video,
                reason_code: result.reason_code,
            }),
    }
}

fn api_tag_record(value: cine_db::TagRecord) -> TagRecord {
    TagRecord {
        id: value.id,
        name: value.name,
        color: value.color,
    }
}

fn api_scan_directory_record(value: cine_db::ScanDirectoryRecord) -> ScanDirectoryRecord {
    ScanDirectoryRecord {
        id: value.id,
        path: value.path,
        alias: value.alias,
    }
}

fn api_public_settings(value: cine_db::PublicSettings) -> PublicSettings {
    PublicSettings {
        confirm_before_delete: value.confirm_before_delete,
        delete_original_file: value.delete_original_file,
        video_extensions: value.video_extensions,
        play_weight: value.play_weight,
        auto_scan_on_startup: value.auto_scan_on_startup,
        auto_scan_interval_seconds: value.auto_scan_interval_seconds,
        short_feed_max_duration_minutes: value.short_feed_max_duration_minutes,
        theme: value.theme,
        log_enabled: value.log_enabled,
        bilingual_enabled: value.bilingual_enabled,
        bilingual_lang: value.bilingual_lang,
        deepl_api_key_configured: value.deepl_api_key_configured,
        ai_tagging_base_url: value.ai_tagging_base_url,
        ai_tagging_api_key_configured: value.ai_tagging_api_key_configured,
        ai_tagging_model: value.ai_tagging_model,
        ai_tagging_frame_count: value.ai_tagging_frame_count,
        ai_tagging_subtitle_char_limit: value.ai_tagging_subtitle_char_limit,
        ai_tagging_startup_batch_size: value.ai_tagging_startup_batch_size,
    }
}

fn default_public_settings() -> PublicSettings {
    PublicSettings {
        confirm_before_delete: false,
        delete_original_file: false,
        video_extensions: ".mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt".to_string(),
        play_weight: 2.0,
        auto_scan_on_startup: false,
        auto_scan_interval_seconds: DEFAULT_AUTO_SCAN_INTERVAL_SECONDS,
        short_feed_max_duration_minutes: 5,
        theme: "system".to_string(),
        log_enabled: false,
        bilingual_enabled: false,
        bilingual_lang: "zh".to_string(),
        deepl_api_key_configured: false,
        ai_tagging_base_url: String::new(),
        ai_tagging_api_key_configured: false,
        ai_tagging_model: String::new(),
        ai_tagging_frame_count: 5,
        ai_tagging_subtitle_char_limit: 4000,
        ai_tagging_startup_batch_size: 10,
    }
}

fn db_settings_update(value: SettingsUpdateRequest) -> SettingsUpdate {
    SettingsUpdate {
        confirm_before_delete: value.confirm_before_delete,
        delete_original_file: value.delete_original_file,
        video_extensions: value.video_extensions,
        play_weight: value.play_weight,
        auto_scan_on_startup: value.auto_scan_on_startup,
        auto_scan_interval_seconds: value.auto_scan_interval_seconds,
        short_feed_max_duration_minutes: value.short_feed_max_duration_minutes,
        theme: value.theme,
        log_enabled: value.log_enabled,
        bilingual_enabled: value.bilingual_enabled,
        bilingual_lang: value.bilingual_lang,
        deepl_api_key: value.deepl_api_key,
        ai_tagging_base_url: value.ai_tagging_base_url,
        ai_tagging_api_key: value.ai_tagging_api_key,
        ai_tagging_model: value.ai_tagging_model,
        ai_tagging_frame_count: value.ai_tagging_frame_count,
        ai_tagging_subtitle_char_limit: value.ai_tagging_subtitle_char_limit,
        ai_tagging_startup_batch_size: value.ai_tagging_startup_batch_size,
    }
}

fn api_subtitle_segment(value: cine_db::SubtitleSegmentRecord) -> ApiSubtitleSegmentRecord {
    ApiSubtitleSegmentRecord {
        index: value.index,
        start_time_ms: value.start_time_ms,
        end_time_ms: value.end_time_ms,
        text: value.text,
        lines: value.lines,
    }
}

fn api_subtitle_match(value: cine_db::SubtitleSearchMatch) -> ApiSubtitleSearchMatch {
    ApiSubtitleSearchMatch {
        video: value.video,
        segment: api_subtitle_segment(value.segment),
    }
}

fn api_subtitle_index_state(
    value: cine_db::SubtitleIndexStateRecord,
) -> ApiSubtitleIndexStateRecord {
    ApiSubtitleIndexStateRecord {
        video_id: value.video_id,
        subtitle_path: value.subtitle_path,
        subtitle_mod_time: value.subtitle_mod_time,
        subtitle_size: value.subtitle_size,
        segment_count: value.segment_count,
    }
}

fn api_ai_status(value: AITagCandidateStatus) -> ApiAITagCandidateStatus {
    match value {
        AITagCandidateStatus::Pending => ApiAITagCandidateStatus::Pending,
        AITagCandidateStatus::Approved => ApiAITagCandidateStatus::Approved,
        AITagCandidateStatus::Rejected => ApiAITagCandidateStatus::Rejected,
        AITagCandidateStatus::Superseded => ApiAITagCandidateStatus::Superseded,
    }
}

fn api_ai_candidate(value: cine_db::AITagCandidateRecord) -> ApiAITagCandidateRecord {
    ApiAITagCandidateRecord {
        id: value.id,
        video_id: value.video_id,
        suggested_name: value.suggested_name,
        normalized_name: value.normalized_name,
        matched_tag_id: value.matched_tag_id,
        confidence: value.confidence,
        reasoning: value.reasoning,
        source_summary: value.source_summary,
        status: api_ai_status(value.status),
    }
}

fn db_ai_candidate_input(value: ApiAITagCandidateInput) -> AITagCandidateInput {
    AITagCandidateInput {
        video_id: value.video_id,
        suggested_name: value.suggested_name,
        normalized_name: value.normalized_name,
        matched_tag_id: value.matched_tag_id,
        confidence: value.confidence,
        reasoning: value.reasoning,
        source_summary: value.source_summary,
    }
}

fn api_short_feed_video(value: cine_db::ShortFeedVideoRecord) -> ApiShortFeedVideoRecord {
    ApiShortFeedVideoRecord {
        id: value.id,
        name: value.name,
        duration: value.duration,
        width: value.width,
        height: value.height,
        tags: value.tags,
        media_url: value.media_url,
        media_mime: value.media_mime,
        liked: value.liked,
        favorited: value.favorited,
        reason_code: value.reason_code,
        reason_message: value.reason_message,
    }
}

fn api_short_feed_interaction(
    value: cine_db::ShortFeedInteractionRecord,
) -> ApiShortFeedInteractionRecord {
    ApiShortFeedInteractionRecord {
        video_id: value.video_id,
        liked: value.liked,
        favorited: value.favorited,
        view_count: value.view_count,
    }
}

fn api_cleanup_analysis(value: cine_db::CleanupAnalysisRecord) -> ApiCleanupAnalysisRecord {
    ApiCleanupAnalysisRecord {
        duplicate_groups: value
            .duplicate_groups
            .into_iter()
            .map(|group| ApiCleanupDuplicateGroup {
                original_id: group.original_id,
                candidate_ids: group.candidate_ids,
                reason: group.reason,
            })
            .collect(),
        low_duration_ids: value.low_duration_ids,
        low_resolution_ids: value.low_resolution_ids,
    }
}

fn api_diagnostics(value: cine_db::DiagnosticsSnapshot) -> ApiDiagnosticsSnapshot {
    ApiDiagnosticsSnapshot {
        video_count: value.video_count,
        tag_count: value.tag_count,
        subtitle_segment_count: value.subtitle_segment_count,
        ai_candidate_count: value.ai_candidate_count,
        short_feed_interaction_count: value.short_feed_interaction_count,
        redacted_settings: api_public_settings(value.redacted_settings),
    }
}

fn api_scan_sync_response(value: cine_db::ScanSyncResult) -> ScanSyncResponse {
    ScanSyncResponse {
        directories: value.directories,
        scanned: value.scanned,
        added: value.added,
        deleted: value.deleted,
        relocated: value.relocated,
        metadata_refreshed: value.metadata_refreshed,
        skipped: value.skipped,
        errors: value
            .errors
            .into_iter()
            .map(|error| ScanSyncErrorRecord {
                operation: error.operation,
                directory: error.directory,
                path: error.path,
                error: error.error,
            })
            .collect(),
    }
}

fn subtitle_engine_status(state: &DaemonState, engine: SubtitleEngine) -> SubtitleEngineStatus {
    let ffmpeg_ready = find_binary("ffmpeg").is_some();
    match engine {
        SubtitleEngine::Whisperx => {
            let runtime_ready =
                runtime_is_prepared(state, &engine) || find_binary("whisperx").is_some();
            let available = ffmpeg_ready && runtime_ready;
            SubtitleEngineStatus {
                engine,
                display_name: "WhisperX".to_string(),
                supported: true,
                available,
                needs_prepare: !available,
                prepare_mode: "managed".to_string(),
                reason_code: if available {
                    "ready".to_string()
                } else if !ffmpeg_ready {
                    "missing_ffmpeg".to_string()
                } else {
                    "missing_runtime".to_string()
                },
                source_lang_mode: "shared".to_string(),
                reason_message: if available {
                    "WhisperX is ready".to_string()
                } else {
                    "WhisperX native runtime is not fully prepared".to_string()
                },
                prepare_hint: "Install ffmpeg and bundled WhisperX sidecar runtime.".to_string(),
            }
        }
        SubtitleEngine::Qwen => {
            let supported = cfg!(all(target_os = "macos", target_arch = "aarch64"));
            let runtime_ready = runtime_is_prepared(state, &engine);
            let available = supported && ffmpeg_ready && runtime_ready;
            SubtitleEngineStatus {
                engine,
                display_name: "Qwen3-ASR-1.7B".to_string(),
                supported,
                available,
                needs_prepare: supported && !available,
                prepare_mode: if supported { "managed" } else { "unsupported" }.to_string(),
                reason_code: if available {
                    "ready"
                } else if !supported {
                    "unsupported_platform"
                } else if !ffmpeg_ready {
                    "missing_ffmpeg"
                } else {
                    "missing_runtime"
                }
                .to_string(),
                source_lang_mode: "ignored".to_string(),
                reason_message: if available {
                    "Qwen is ready"
                } else {
                    "Qwen native ASR runtime is not fully prepared"
                }
                .to_string(),
                prepare_hint: "Install bundled Qwen ASR sidecar runtime.".to_string(),
            }
        }
    }
}

fn default_cleanup_status() -> CleanupStatus {
    CleanupStatus {
        running: false,
        completed: false,
        error: String::new(),
        progress: CleanupProgressRecord {
            stage: "idle".to_string(),
            message: String::new(),
            current: 0,
            total: 0,
            path: String::new(),
        },
        analysis: None,
        started_at: None,
        updated_at: None,
    }
}

fn current_cleanup_status(state: &DaemonState) -> CleanupStatus {
    state
        .cleanup_status
        .lock()
        .map(|status| status.clone())
        .unwrap_or_else(|_| default_cleanup_status())
}

fn set_cleanup_status(state: &DaemonState, status: CleanupStatus) {
    if let Ok(mut current) = state.cleanup_status.lock() {
        *current = status;
    }
}

fn current_short_feed_status(state: &DaemonState) -> ShortFeedServerStatus {
    state
        .short_feed_status
        .lock()
        .map(|status| status.clone())
        .unwrap_or_else(|_| default_short_feed_status())
}

fn default_short_feed_status() -> ShortFeedServerStatus {
    ShortFeedServerStatus {
        running: false,
        bind_address: "0.0.0.0".to_string(),
        port: 0,
        url: String::new(),
        lan_urls: Vec::new(),
        startup_error: String::new(),
        fallback_used: false,
        allowed_access: short_feed_allowed_access(),
    }
}

fn default_subtitle_status() -> SubtitleJobStatus {
    SubtitleJobStatus {
        running: false,
        completed: false,
        cancelled: false,
        progress: SubtitleProgressRecord {
            action: "idle".to_string(),
            engine: None,
            phase: "idle".to_string(),
            percent: 0,
            message: String::new(),
            cancellable: false,
        },
        result: None,
        error: None,
    }
}

fn current_subtitle_status(state: &DaemonState) -> SubtitleJobStatus {
    state
        .subtitle_status
        .lock()
        .map(|status| status.clone())
        .unwrap_or_else(|_| default_subtitle_status())
}

fn set_subtitle_progress(
    state: &DaemonState,
    action: &str,
    engine: Option<SubtitleEngine>,
    phase: &str,
    percent: i32,
    message: &str,
    cancellable: bool,
) {
    if let Ok(mut status) = state.subtitle_status.lock() {
        *status = SubtitleJobStatus {
            running: true,
            completed: false,
            cancelled: false,
            progress: SubtitleProgressRecord {
                action: action.to_string(),
                engine,
                phase: phase.to_string(),
                percent,
                message: message.to_string(),
                cancellable,
            },
            result: None,
            error: None,
        };
    }
}

fn set_subtitle_result(
    state: &DaemonState,
    result: SubtitleGenerateResult,
    phase: &str,
    percent: i32,
    message: &str,
) {
    if let Ok(mut status) = state.subtitle_status.lock() {
        *status = SubtitleJobStatus {
            running: false,
            completed: result.status == "success" || result.status == "validation_failed",
            cancelled: result.status == "cancelled",
            progress: SubtitleProgressRecord {
                action: "generate".to_string(),
                engine: result.engine.clone(),
                phase: phase.to_string(),
                percent,
                message: message.to_string(),
                cancellable: false,
            },
            result: Some(result),
            error: None,
        };
    }
}

fn cancelled_subtitle_result(
    video_id: i64,
    engine: Option<SubtitleEngine>,
    source_lang: Option<String>,
) -> SubtitleGenerateResult {
    SubtitleGenerateResult {
        status: "cancelled".to_string(),
        video_id,
        path: None,
        message: Some("字幕生成已取消".to_string()),
        validation_code: None,
        force_eligible: false,
        engine,
        source_lang,
    }
}

trait CancellableCommand {
    fn output_with_cancel(&mut self, state: &DaemonState) -> anyhow::Result<Output>;
}

impl CancellableCommand for Command {
    fn output_with_cancel(&mut self, state: &DaemonState) -> anyhow::Result<Output> {
        if state.subtitle_cancel_requested.load(Ordering::SeqCst) {
            anyhow::bail!("subtitle job cancelled");
        }
        let child = self.spawn().context("spawn cancellable command")?;
        let pid = child.id();
        if let Ok(mut current) = state.subtitle_child.lock() {
            *current = Some(pid);
        }
        let output = child.wait_with_output().context("wait cancellable command");
        if let Ok(mut current) = state.subtitle_child.lock() {
            if current.as_ref().copied() == Some(pid) {
                *current = None;
            }
        }
        if state.subtitle_cancel_requested.load(Ordering::SeqCst) {
            anyhow::bail!("subtitle job cancelled");
        }
        output
    }
}

fn timestamp_string() -> String {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|duration| duration.as_secs().to_string())
        .unwrap_or_default()
}

fn sanitize_log_field(value: &str) -> String {
    value
        .trim()
        .chars()
        .filter(|ch| ch.is_ascii_alphanumeric() || matches!(ch, '-' | '_' | ':' | '.'))
        .take(64)
        .collect::<String>()
}

fn mutation_ok(video: cine_domain::VideoSummary) -> VideoMutationResponse {
    VideoMutationResponse {
        video: Some(video),
        ok: true,
        reason_code: None,
        user_message: None,
    }
}

fn mutation_error(reason_code: &str, user_message: &str) -> VideoMutationResponse {
    VideoMutationResponse {
        video: None,
        ok: false,
        reason_code: Some(reason_code.to_string()),
        user_message: Some(user_message.to_string()),
    }
}

struct BatchAccumulator {
    requested: usize,
    succeeded: usize,
    errors: Vec<BatchVideoOperationError>,
}

impl BatchAccumulator {
    fn new(ids: &[i64]) -> Self {
        Self {
            requested: ids.len(),
            succeeded: 0,
            errors: Vec::new(),
        }
    }

    fn record<T, E: std::fmt::Display>(&mut self, video_id: i64, result: Result<T, E>) {
        match result {
            Ok(_) => self.succeeded += 1,
            Err(error) => self.errors.push(BatchVideoOperationError {
                video_id,
                error: error.to_string(),
            }),
        }
    }

    fn finish(self) -> BatchVideoOperationResult {
        BatchVideoOperationResult {
            requested: self.requested,
            succeeded: self.succeeded,
            failed: self.errors.len(),
            errors: self.errors,
        }
    }
}

#[derive(Deserialize)]
struct FfprobeStream {
    width: Option<i32>,
    height: Option<i32>,
    duration: Option<String>,
}

#[derive(Deserialize)]
struct FfprobeFormat {
    duration: Option<String>,
}

#[derive(Deserialize)]
struct FfprobePayload {
    #[serde(default)]
    streams: Vec<FfprobeStream>,
    format: Option<FfprobeFormat>,
}

fn system_media_metadata(path: &StdPath) -> Option<MediaMetadata> {
    let ffprobe = find_ffprobe()?;
    let output = Command::new(ffprobe)
        .args([
            "-v",
            "error",
            "-select_streams",
            "v:0",
            "-show_entries",
            "stream=width,height,duration:format=duration",
            "-of",
            "json",
        ])
        .arg(path)
        .output()
        .ok()?;
    if !output.status.success() {
        return None;
    }
    parse_ffprobe_output(&output.stdout)
}

fn find_ffprobe() -> Option<PathBuf> {
    if let Some(path) = find_binary("ffprobe") {
        return Some(path);
    }
    for candidate in ["/opt/homebrew/bin/ffprobe", "/usr/local/bin/ffprobe"] {
        let path = PathBuf::from(candidate);
        if path.is_file() {
            return Some(path);
        }
    }
    None
}

fn find_binary(name: &str) -> Option<PathBuf> {
    if let Ok(path) = std::env::var("PATH") {
        for directory in path.split(':') {
            let candidate = PathBuf::from(directory).join(name);
            if candidate.is_file() {
                return Some(candidate);
            }
        }
    }
    for candidate in [
        format!("/opt/homebrew/bin/{name}"),
        format!("/usr/local/bin/{name}"),
        format!("/usr/bin/{name}"),
    ] {
        let path = PathBuf::from(candidate);
        if path.is_file() {
            return Some(path);
        }
    }
    None
}

fn parse_ffprobe_output(output: &[u8]) -> Option<MediaMetadata> {
    let payload: FfprobePayload = serde_json::from_slice(output).ok()?;
    let stream = payload.streams.first()?;
    let width = stream.width.unwrap_or_default();
    let height = stream.height.unwrap_or_default();
    let resolution = if width > 0 && height > 0 {
        format!("{width}x{height}")
    } else {
        String::new()
    };
    let duration = stream
        .duration
        .as_deref()
        .or(payload
            .format
            .as_ref()
            .and_then(|format| format.duration.as_deref()))
        .and_then(|value| value.trim().parse::<f64>().ok())
        .unwrap_or_default();
    Some(MediaMetadata {
        duration,
        resolution,
        width,
        height,
    })
}

#[derive(Debug, Deserialize)]
struct AsrPayload {
    #[allow(dead_code)]
    language: Option<String>,
    segments: Vec<AsrPayloadSegment>,
}

#[derive(Debug, Deserialize)]
struct AsrPayloadSegment {
    start: f64,
    end: f64,
    text: String,
}

async fn transcribe_to_srt(
    state: &DaemonState,
    engine: &SubtitleEngine,
    video_path: &StdPath,
    subtitle_path: &StdPath,
    source_lang: &str,
) -> anyhow::Result<()> {
    if !video_path.is_file() {
        anyhow::bail!("video file is missing: {}", video_path.display());
    }
    let temp_wav = std::env::temp_dir().join(format!("cine-asr-{}.wav", uuid::Uuid::new_v4()));
    let result = async {
        extract_audio_for_asr(state, video_path, &temp_wav)?;
        let payload = run_asr_sidecar(state, engine, &temp_wav, source_lang)?;
        write_srt_file(subtitle_path, &payload.segments)?;
        Ok::<(), anyhow::Error>(())
    }
    .await;
    let _ = fs::remove_file(&temp_wav);
    result
}

fn extract_audio_for_asr(
    state: &DaemonState,
    video_path: &StdPath,
    wav_path: &StdPath,
) -> anyhow::Result<()> {
    if state.skip_audio_extract {
        fs::write(wav_path, b"").context("create skipped audio placeholder")?;
        return Ok(());
    }
    let ffmpeg = find_binary("ffmpeg").context("ffmpeg binary was not found")?;
    let output = Command::new(ffmpeg)
        .arg("-i")
        .arg(video_path)
        .args(["-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le"])
        .arg(wav_path)
        .arg("-y")
        .output_with_cancel(state)
        .context("run ffmpeg audio extraction")?;
    if !output.status.success() {
        anyhow::bail!(
            "ffmpeg audio extraction failed: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        );
    }
    Ok(())
}

fn run_asr_sidecar(
    state: &DaemonState,
    engine: &SubtitleEngine,
    wav_path: &StdPath,
    source_lang: &str,
) -> anyhow::Result<AsrPayload> {
    let sidecar_dir = locate_sidecar_dir(state).context("ASR sidecar directory was not found")?;
    let python = locate_python_bin(state).context("python3 binary was not found")?;
    let runtime_dir = runtime_dir_for_asr(state, engine)?;
    fs::create_dir_all(&runtime_dir).context("create ASR runtime directory")?;

    let mut command = Command::new(&python);
    command.arg(sidecar_script_path(&sidecar_dir, engine)?);
    match engine {
        SubtitleEngine::Whisperx => {
            command
                .arg("--wav-path")
                .arg(wav_path)
                .args(["--model", "medium"])
                .args(["--language", source_lang])
                .args(["--compute-type", "int8"])
                .args(["--batch-size", "8"])
                .args(["--asr-device", "cpu"])
                .args(["--align-device", "cpu"]);
        }
        SubtitleEngine::Qwen => {
            command
                .arg("--wav-path")
                .arg(wav_path)
                .args(["--model", "Qwen/Qwen3-ASR-1.7B"])
                .args(["--aligner", "Qwen/Qwen3-ForcedAligner-0.6B"])
                .args(["--language", source_lang])
                .args(["--device", "auto"]);
        }
    }
    command.env("PYTHONUNBUFFERED", "1");
    command.env("TOKENIZERS_PARALLELISM", "false");
    command.env("HF_HUB_DISABLE_TELEMETRY", "1");
    command.env("HF_HOME", runtime_dir.join("hf"));
    command.env("TORCH_HOME", runtime_dir.join("torch"));

    let output = command
        .output_with_cancel(state)
        .context("run ASR python worker")?;
    if !output.status.success() {
        let mut detail = String::from_utf8_lossy(&output.stderr).trim().to_string();
        if detail.is_empty() {
            detail = String::from_utf8_lossy(&output.stdout).trim().to_string();
        }
        anyhow::bail!("ASR worker failed: {detail}");
    }
    let payload: AsrPayload =
        serde_json::from_slice(&output.stdout).context("parse ASR worker JSON output")?;
    if payload
        .segments
        .iter()
        .all(|segment| segment.text.trim().is_empty())
    {
        anyhow::bail!("ASR worker produced no subtitle text");
    }
    Ok(payload)
}

fn sidecar_script_path(sidecar_dir: &StdPath, engine: &SubtitleEngine) -> anyhow::Result<PathBuf> {
    let file_name = match engine {
        SubtitleEngine::Whisperx => "whisperx_worker.py",
        SubtitleEngine::Qwen => "qwen_asr_worker.py",
    };
    let path = sidecar_dir.join(file_name);
    if !path.is_file() {
        anyhow::bail!("ASR sidecar script is missing: {}", path.display());
    }
    Ok(path)
}

fn locate_sidecar_dir(state: &DaemonState) -> Option<PathBuf> {
    if let Some(path) = state.asr_sidecar_dir.as_ref().filter(|path| path.is_dir()) {
        return Some(path.clone());
    }
    if let Some(path) = env_path("CINE_SIDECAR_DIR").filter(|path| path.is_dir()) {
        return Some(path);
    }
    if let Ok(exe) = std::env::current_exe() {
        if let Some(resources) = exe
            .parent()
            .and_then(StdPath::parent)
            .filter(|path| path.ends_with("Resources"))
        {
            let path = resources.join("sidecars");
            if path.is_dir() {
                return Some(path);
            }
        }
    }
    for relative in ["services", "../services", "../../services"] {
        let path = PathBuf::from(relative);
        if path.join("whisperx_worker.py").is_file() || path.join("qwen_asr_worker.py").is_file() {
            return Some(path);
        }
    }
    None
}

fn locate_python_bin(state: &DaemonState) -> Option<PathBuf> {
    if let Some(path) = state.asr_python_bin.as_ref() {
        return Some(path.clone());
    }
    env_path("CINE_PYTHON")
        .or_else(|| env_path("PYTHON_BIN"))
        .or_else(|| find_binary("python3"))
        .or_else(|| find_binary("python"))
}

fn prepare_asr_runtime(state: &DaemonState, engine: &SubtitleEngine) -> anyhow::Result<()> {
    let runtime_dir = runtime_dir_for_asr(state, engine)?;
    let venv_bin = runtime_dir.join("venv/bin");
    let sidecar_dir = locate_sidecar_dir(state).context("ASR sidecar directory was not found")?;
    fs::create_dir_all(&venv_bin).context("create ASR venv bin directory")?;
    fs::create_dir_all(runtime_dir.join("hf")).context("create ASR HF cache directory")?;
    fs::create_dir_all(runtime_dir.join("torch")).context("create ASR torch cache directory")?;
    let script = sidecar_script_path(&sidecar_dir, engine)?;
    let script_name = script
        .file_name()
        .context("ASR sidecar script has no file name")?;
    fs::copy(&script, runtime_dir.join(script_name)).context("copy ASR sidecar script")?;

    let python_target = venv_bin.join("python3");
    if !python_target.exists() {
        if let Some(python) = locate_python_bin(state) {
            #[cfg(unix)]
            {
                if python.is_absolute() {
                    std::os::unix::fs::symlink(&python, &python_target)
                        .or_else(|_| {
                            fs::write(
                                &python_target,
                                format!("#!/bin/sh\nexec \"{}\" \"$@\"\n", python.display()),
                            )
                        })
                        .context("create ASR python launcher")?;
                } else {
                    fs::write(
                        &python_target,
                        format!("#!/bin/sh\nexec {} \"$@\"\n", python.display()),
                    )
                    .context("create ASR python launcher")?;
                }
            }
            #[cfg(not(unix))]
            fs::write(&python_target, python.to_string_lossy().as_bytes())
                .context("create ASR python launcher")?;
        } else {
            fs::write(&python_target, b"").context("create ASR python marker")?;
        }
    }
    let package_marker = match engine {
        SubtitleEngine::Whisperx => "whisperx.installed",
        SubtitleEngine::Qwen => "qwen_asr.installed",
    };
    fs::write(runtime_dir.join(package_marker), b"prepared").context("write ASR package marker")?;
    Ok(())
}

fn runtime_is_prepared(state: &DaemonState, engine: &SubtitleEngine) -> bool {
    let Ok(runtime_dir) = runtime_dir_for_asr(state, engine) else {
        return false;
    };
    let script_name = match engine {
        SubtitleEngine::Whisperx => "whisperx_worker.py",
        SubtitleEngine::Qwen => "qwen_asr_worker.py",
    };
    let marker = match engine {
        SubtitleEngine::Whisperx => "whisperx.installed",
        SubtitleEngine::Qwen => "qwen_asr.installed",
    };
    runtime_dir.join("venv/bin/python3").exists()
        && runtime_dir.join(script_name).is_file()
        && runtime_dir.join(marker).is_file()
}

fn runtime_dir_for_asr(state: &DaemonState, engine: &SubtitleEngine) -> anyhow::Result<PathBuf> {
    let root = state
        .asr_runtime_dir
        .clone()
        .or_else(|| env_path("CINE_RUNTIME_DIR"))
        .or_else(locate_bundled_runtime_dir)
        .unwrap_or_else(|| std::env::temp_dir().join("cineinsight-runtime"));
    let name = match engine {
        SubtitleEngine::Whisperx => "whisperx_sidecar",
        SubtitleEngine::Qwen => "qwen_asr_sidecar",
    };
    Ok(root.join(name))
}

fn locate_bundled_runtime_dir() -> Option<PathBuf> {
    let exe = std::env::current_exe().ok()?;
    let resources = exe
        .parent()
        .and_then(StdPath::parent)
        .filter(|path| path.ends_with("Resources"))?;
    let path = resources.join("runtime");
    if path.is_dir() {
        Some(path)
    } else {
        None
    }
}

fn write_srt_file(path: &StdPath, segments: &[AsrPayloadSegment]) -> anyhow::Result<()> {
    let mut output = String::new();
    let mut index = 1;
    for segment in segments {
        let text = segment
            .text
            .lines()
            .map(str::trim)
            .filter(|line| !line.is_empty())
            .collect::<Vec<_>>()
            .join("\n");
        if text.is_empty() {
            continue;
        }
        let start_ms = (segment.start * 1000.0).round().max(0.0) as i64;
        let end_ms = (segment.end * 1000.0).round().max(start_ms as f64) as i64;
        output.push_str(&format!("{index}\n"));
        output.push_str(&format_srt_timestamp(start_ms));
        output.push_str(" --> ");
        output.push_str(&format_srt_timestamp(end_ms));
        output.push('\n');
        output.push_str(&text);
        output.push_str("\n\n");
        index += 1;
    }
    if index == 1 {
        anyhow::bail!("ASR worker produced no usable subtitle segments");
    }
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).context("create subtitle directory")?;
    }
    fs::write(path, output).context("write generated SRT")
}

fn validate_generated_srt(
    video_id: i64,
    path: &StdPath,
    engine: &SubtitleEngine,
    source_lang: &str,
) -> Option<SubtitleGenerateResult> {
    let content = fs::read_to_string(path).ok()?;
    let mut total = 0usize;
    let mut counts = std::collections::BTreeMap::<String, usize>::new();
    for line in content.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty()
            || trimmed.contains("-->")
            || trimmed.chars().all(|ch| ch.is_ascii_digit())
        {
            continue;
        }
        total += 1;
        *counts.entry(trimmed.to_string()).or_default() += 1;
    }
    if total == 0 {
        let _ = fs::remove_file(path);
        return Some(SubtitleGenerateResult {
            status: "validation_failed".to_string(),
            video_id,
            path: None,
            message: Some("语音识别未产生有效字幕，视频可能没有清晰的语音内容".to_string()),
            validation_code: Some("hallucination_detected".to_string()),
            force_eligible: true,
            engine: Some(engine.clone()),
            source_lang: Some(source_lang.to_string()),
        });
    }
    let max_count = counts.values().copied().max().unwrap_or_default();
    let repeat_ratio = max_count as f64 / total as f64;
    if repeat_ratio > 0.85 {
        let _ = fs::remove_file(path);
        return Some(SubtitleGenerateResult {
            status: "validation_failed".to_string(),
            video_id,
            path: None,
            message: Some(format!(
                "检测到异常输出（疑似模型幻觉），字幕内容重复率 {:.0}%。可选择强制生成保留结果",
                repeat_ratio * 100.0
            )),
            validation_code: Some("hallucination_detected".to_string()),
            force_eligible: true,
            engine: Some(engine.clone()),
            source_lang: Some(source_lang.to_string()),
        });
    }
    None
}

#[derive(Clone, Debug, Eq, PartialEq)]
struct SrtEntry {
    index: String,
    time: String,
    text: String,
}

fn maybe_merge_bilingual_srt(
    state: &DaemonState,
    path: &StdPath,
    target_lang: &str,
    api_key: &str,
) -> anyhow::Result<()> {
    let entries = parse_srt_entries(path)?;
    if entries.is_empty() {
        return Ok(());
    }
    let texts = entries
        .iter()
        .map(|entry| entry.text.clone())
        .collect::<Vec<_>>();
    let translated = translate_deepl(state, texts, target_lang, api_key)?;
    let translated_entries = entries
        .iter()
        .enumerate()
        .map(|(index, entry)| SrtEntry {
            index: entry.index.clone(),
            time: entry.time.clone(),
            text: translated
                .get(index)
                .cloned()
                .unwrap_or_else(|| entry.text.clone()),
        })
        .collect::<Vec<_>>();
    let merged = merge_bilingual_entries(&entries, &translated_entries);
    fs::write(path, merged).context("write bilingual SRT")
}

fn parse_srt_entries(path: &StdPath) -> anyhow::Result<Vec<SrtEntry>> {
    let content = fs::read_to_string(path).context("read SRT")?;
    let entries = content
        .split("\n\n")
        .filter_map(|block| {
            let lines = block.lines().map(str::trim).collect::<Vec<_>>();
            if lines.len() < 3 {
                return None;
            }
            Some(SrtEntry {
                index: lines[0].to_string(),
                time: lines[1].to_string(),
                text: lines[2..].join("\n").trim().to_string(),
            })
        })
        .collect::<Vec<_>>();
    Ok(entries)
}

fn translate_deepl(
    state: &DaemonState,
    texts: Vec<String>,
    target_lang: &str,
    api_key: &str,
) -> anyhow::Result<Vec<String>> {
    if let Some(translator) = &state.deepl_translator {
        return Ok(translator(texts, target_lang.to_string()));
    }
    let escaped = texts
        .iter()
        .map(serde_json::to_string)
        .collect::<Result<Vec<_>, _>>()?
        .join(",");
    let target = normalize_deepl_target_lang(target_lang);
    let api_url = if api_key.ends_with(":fx") {
        "https://api-free.deepl.com/v2/translate"
    } else {
        "https://api.deepl.com/v2/translate"
    };
    let script = format!(
        r#"
import json, urllib.request, urllib.error
payload = json.dumps({{"text":[{escaped}],"target_lang":"{target}"}}).encode("utf-8")
req = urllib.request.Request("{api_url}", data=payload, headers={{"Authorization":"DeepL-Auth-Key {api_key}","Content-Type":"application/json"}})
with urllib.request.urlopen(req, timeout=30) as resp:
    data = json.loads(resp.read().decode("utf-8"))
print(json.dumps([item.get("text", "") for item in data.get("translations", [])]))
"#
    );
    let python = locate_python_bin(state).context("python3 binary was not found for DeepL")?;
    let output = Command::new(python)
        .arg("-c")
        .arg(script)
        .output_with_cancel(state)
        .context("run DeepL translation helper")?;
    if !output.status.success() {
        anyhow::bail!(
            "DeepL translation failed: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        );
    }
    serde_json::from_slice(&output.stdout).context("parse DeepL translation helper output")
}

fn merge_bilingual_entries(original: &[SrtEntry], translated: &[SrtEntry]) -> String {
    let mut output = String::new();
    let max_len = original.len().max(translated.len());
    for index in 0..max_len {
        let idx = index + 1;
        let original_entry = original.get(index);
        let translated_entry = translated.get(index);
        let time = original_entry
            .map(|entry| entry.time.as_str())
            .or_else(|| translated_entry.map(|entry| entry.time.as_str()))
            .unwrap_or_default();
        let original_text = original_entry
            .map(|entry| entry.text.as_str())
            .unwrap_or_default();
        let translated_text = translated_entry
            .map(|entry| entry.text.as_str())
            .unwrap_or_default();
        output.push_str(&format!("{idx}\n{time}\n"));
        match (original_text.is_empty(), translated_text.is_empty()) {
            (false, false) => output.push_str(&format!("{original_text}\n{translated_text}\n\n")),
            (false, true) => output.push_str(&format!("{original_text}\n\n")),
            (true, false) => output.push_str(&format!("{translated_text}\n\n")),
            (true, true) => output.push('\n'),
        }
    }
    output
}

fn normalize_deepl_target_lang(target_lang: &str) -> String {
    let upper = target_lang.trim().to_ascii_uppercase();
    if upper == "ZH" {
        "ZH-HANS".to_string()
    } else {
        upper
    }
}

fn format_srt_timestamp(ms: i64) -> String {
    let hours = ms / 3_600_000;
    let minutes = (ms % 3_600_000) / 60_000;
    let seconds = (ms % 60_000) / 1_000;
    let millis = ms % 1_000;
    format!("{hours:02}:{minutes:02}:{seconds:02},{millis:03}")
}

fn normalize_source_lang(source_lang: &str) -> String {
    let value = source_lang.trim();
    if value.is_empty() {
        "auto".to_string()
    } else {
        value.to_string()
    }
}

fn env_path(name: &str) -> Option<PathBuf> {
    std::env::var_os(name)
        .map(PathBuf::from)
        .filter(|path| !path.as_os_str().is_empty())
}

fn default_short_feed_assets_dir() -> Option<PathBuf> {
    let executable = std::env::current_exe().ok()?;
    let macos_dir = executable.parent()?;
    let contents_dir = macos_dir.parent()?;
    let candidate = contents_dir.join("Resources").join("short-feed");
    candidate.is_dir().then_some(candidate)
}

fn env_u16(name: &str) -> Option<u16> {
    std::env::var(name)
        .ok()
        .as_deref()
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .and_then(|value| value.parse::<u16>().ok())
}

fn status_for_mutation_error(error: VideoMutationError) -> StatusCode {
    match error {
        VideoMutationError::EmptyPath
        | VideoMutationError::NotDirectory
        | VideoMutationError::NotFile
        | VideoMutationError::NotVideoFile
        | VideoMutationError::InvalidFileName => StatusCode::BAD_REQUEST,
        VideoMutationError::FileMissing | VideoMutationError::VideoNotFound => {
            StatusCode::NOT_FOUND
        }
        VideoMutationError::VideoExists | VideoMutationError::TargetExists => StatusCode::CONFLICT,
        VideoMutationError::MetadataUnavailable => StatusCode::UNPROCESSABLE_ENTITY,
        VideoMutationError::Filesystem | VideoMutationError::DatabaseWrite => {
            StatusCode::INTERNAL_SERVER_ERROR
        }
    }
}

fn status_for_tag_error(error: TagMutationError) -> StatusCode {
    match error {
        TagMutationError::TagExists => StatusCode::CONFLICT,
        TagMutationError::TagNotFound => StatusCode::NOT_FOUND,
        TagMutationError::DatabaseWrite => StatusCode::INTERNAL_SERVER_ERROR,
    }
}

fn status_for_library_error(error: LibraryManagementError) -> StatusCode {
    match error {
        LibraryManagementError::NotFound => StatusCode::NOT_FOUND,
        LibraryManagementError::DatabaseWrite => StatusCode::INTERNAL_SERVER_ERROR,
    }
}

fn status_for_native_slice_error(error: NativeSliceError) -> StatusCode {
    match error {
        NativeSliceError::NotFound => StatusCode::NOT_FOUND,
        NativeSliceError::SubtitleParse => StatusCode::BAD_REQUEST,
        NativeSliceError::Filesystem => StatusCode::NOT_FOUND,
        NativeSliceError::DatabaseWrite => StatusCode::INTERNAL_SERVER_ERROR,
    }
}

async fn connect_pool(config: &PgConfig) -> Result<PgPool, sqlx::Error> {
    let ssl_mode = config
        .sslmode
        .parse::<PgSslMode>()
        .unwrap_or(PgSslMode::Disable);
    let mut options = PgConnectOptions::new()
        .host(&config.host)
        .port(config.port)
        .username(&config.user)
        .database(&config.database)
        .ssl_mode(ssl_mode);

    if let Some(password) = &config.password {
        options = options.password(password);
    }
    if let Some(timezone) = &config.timezone {
        options = options.options([("timezone", timezone.as_str())]);
    }

    PgPoolOptions::new()
        .max_connections(5)
        .connect_with(options)
        .await
}

fn video_filter_from_request(value: VideoFilterRequest) -> VideoFilter {
    VideoFilter {
        keyword: value.keyword,
        tag_ids: value.tag_ids,
        min_size: value.min_size,
        max_size: value.max_size,
        min_height: value.min_height,
        max_height: value.max_height,
        cursor: value.cursor,
        limit: value.limit,
    }
}
