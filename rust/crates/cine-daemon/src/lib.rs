//! Rust daemon foundation for the native CineInsight replacement.

use std::net::{Ipv4Addr, SocketAddr};

use anyhow::Context;
use axum::{
    body::Bytes,
    extract::{Path, Query, Request, State},
    http::{header::AUTHORIZATION, StatusCode},
    middleware::{self, Next},
    response::Response,
    routing::{get, post},
    Json, Router,
};
use cine_api::{
    AITagCandidateInput as ApiAITagCandidateInput, AITagCandidateListResponse,
    AITagCandidateRecord as ApiAITagCandidateRecord,
    AITagCandidateStatus as ApiAITagCandidateStatus, AddVideoRequest,
    CleanupAnalysisRecord as ApiCleanupAnalysisRecord, CleanupAnalyzeRequest,
    CleanupDuplicateGroup as ApiCleanupDuplicateGroup, DatabaseHealth, DeleteVideoRequest,
    DiagnosticsSnapshot as ApiDiagnosticsSnapshot, HealthResponse, IndexSubtitleRequest,
    PlaybackAttemptResponse as ApiPlaybackAttemptResponse, PreviewSessionResponse, PublicSettings,
    RandomCandidateResponse, RenameVideoRequest, ScanDirectoryListResponse,
    ScanDirectoryMutationRequest, ScanDirectoryRecord, ScanDirectoryRequest, ScanDirectoryResponse,
    ScannedFileResponse, SchemaHealth, SettingsUpdateRequest, ShortFeedFeedbackRequest,
    ShortFeedInteractionRecord as ApiShortFeedInteractionRecord,
    ShortFeedVideoRecord as ApiShortFeedVideoRecord,
    SubtitleIndexStateRecord as ApiSubtitleIndexStateRecord,
    SubtitleSearchMatch as ApiSubtitleSearchMatch, SubtitleSearchResponse,
    SubtitleSegmentRecord as ApiSubtitleSegmentRecord, TagListResponse, TagMutationRequest,
    TagRecord, VideoFilterRequest, VideoListResponse, VideoMutationResponse,
    VideoTagMutationRequest,
};
use cine_db::{
    add_scan_directory as mutate_add_scan_directory, add_video as mutate_add_video,
    approve_ai_tag_candidate as mutate_approve_ai_tag_candidate,
    assign_tag_to_video as mutate_assign_tag_to_video,
    create_ai_tag_candidate as mutate_create_ai_tag_candidate, create_tag as mutate_create_tag,
    delete_scan_directory as mutate_delete_scan_directory, delete_tag as mutate_delete_tag,
    delete_video as mutate_delete_video, diagnostics_snapshot as query_diagnostics_snapshot,
    get_public_settings as query_public_settings, get_subtitle_segments as query_subtitle_segments,
    index_subtitle_file as mutate_index_subtitle_file,
    list_ai_tag_candidates as query_ai_tag_candidates,
    list_scan_directories as query_scan_directories, list_tags as query_tags,
    list_videos as query_list_videos, load_pg_config_from_env,
    next_short_feed_video as query_next_short_feed_video, play_video_with_dispatch,
    preview_externally_with_dispatch, preview_session as query_preview_session,
    random_candidate as query_random_candidate,
    record_short_feed_feedback as mutate_short_feed_feedback,
    reject_ai_tag_candidate as mutate_reject_ai_tag_candidate,
    remove_tag_from_video as mutate_remove_tag_from_video, rename_video as mutate_rename_video,
    scan_directory_with_extensions, search_subtitle_matches as query_subtitle_matches,
    start_cleanup_analysis as query_cleanup_analysis,
    update_scan_directory as mutate_update_scan_directory, update_settings as mutate_settings,
    update_tag as mutate_update_tag, AITagCandidateInput, AITagCandidateStatus,
    LibraryManagementError, NativeSliceError, PgConfig, PlaybackDispatch, PlaybackError,
    PreviewMode, SettingsUpdate, ShortFeedFeedback, SystemPlaybackDispatch, TagMutationError,
    VideoFilter, VideoMutationError, REQUIRED_LEGACY_TABLES,
};
use serde::Deserialize;
use sqlx::{
    postgres::{PgConnectOptions, PgPoolOptions, PgSslMode},
    PgPool,
};

#[derive(Clone, Debug)]
pub struct DaemonConfig {
    pub token: String,
    pub pool: Option<PgPool>,
    pub enable_system_dispatch: bool,
}

impl DaemonConfig {
    pub async fn from_env() -> anyhow::Result<Self> {
        let token = std::env::var("CINE_DAEMON_TOKEN")
            .ok()
            .map(|value| value.trim().to_string())
            .filter(|value| !value.is_empty())
            .context("CINE_DAEMON_TOKEN cannot be empty")?;
        let config = load_pg_config_from_env().context("load PostgreSQL config")?;
        let pool = connect_pool(&config).await.context("connect PostgreSQL")?;

        Ok(Self {
            token,
            pool: Some(pool),
            enable_system_dispatch: true,
        })
    }
}

#[derive(Clone, Debug)]
pub struct DaemonState {
    token: String,
    version: String,
    app_compat_version: String,
    pool: Option<PgPool>,
    enable_system_dispatch: bool,
}

impl DaemonState {
    pub fn new(token: impl Into<String>) -> Self {
        Self {
            token: token.into(),
            version: env!("CARGO_PKG_VERSION").to_string(),
            app_compat_version: "0.1".to_string(),
            pool: None,
            enable_system_dispatch: false,
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
            ..Self::new(config.token)
        }
    }

    fn bearer_value(&self) -> String {
        format!("Bearer {}", self.token)
    }
}

pub fn app(state: DaemonState) -> Router {
    Router::new()
        .route("/health", get(health))
        .route("/api/videos", get(list_videos))
        .route("/api/videos/search", post(search_videos))
        .route("/api/videos/random-candidate", post(random_candidate))
        .route("/api/videos/scan", post(scan_directory))
        .route("/api/videos/add", post(add_video))
        .route("/api/videos/{id}/rename", post(rename_video))
        .route("/api/videos/{id}/delete", post(delete_video))
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
        .route("/api/scan-directories/{id}", post(update_scan_directory))
        .route(
            "/api/scan-directories/{id}/delete",
            post(delete_scan_directory),
        )
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
        .route("/api/short-feed/next", get(next_short_feed_video))
        .route(
            "/api/short-feed/videos/{id}/feedback",
            post(record_short_feed_feedback),
        )
        .route("/api/cleanup/analyze", post(analyze_cleanup))
        .route("/api/diagnostics", get(diagnostics))
        .route_layer(middleware::from_fn_with_state(
            state.clone(),
            require_bearer,
        ))
        .with_state(state)
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
        return Err(StatusCode::SERVICE_UNAVAILABLE);
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

#[derive(Deserialize)]
struct SubtitleSearchQuery {
    keyword: String,
    limit: Option<i64>,
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
        video_extensions: value.video_extensions,
        play_weight: value.play_weight,
        short_feed_max_duration_minutes: value.short_feed_max_duration_minutes,
        theme: value.theme,
        deepl_api_key_configured: value.deepl_api_key_configured,
        ai_tagging_api_key_configured: value.ai_tagging_api_key_configured,
        ai_tagging_frame_count: value.ai_tagging_frame_count,
        ai_tagging_subtitle_char_limit: value.ai_tagging_subtitle_char_limit,
        ai_tagging_startup_batch_size: value.ai_tagging_startup_batch_size,
    }
}

fn db_settings_update(value: SettingsUpdateRequest) -> SettingsUpdate {
    SettingsUpdate {
        confirm_before_delete: value.confirm_before_delete,
        delete_original_file: value.delete_original_file,
        video_extensions: value.video_extensions,
        play_weight: value.play_weight,
        auto_scan_on_startup: value.auto_scan_on_startup,
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
