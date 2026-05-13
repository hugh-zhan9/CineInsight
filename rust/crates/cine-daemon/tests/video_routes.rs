use axum::body::Body;
use cine_api::{
    AITagCandidateListResponse, AITaggingStatusSummary, CleanupAnalysisRecord, CleanupStatus,
    DiagnosticsSnapshot, PlaybackAttemptResponse, PreviewSessionResponse, PublicSettings,
    ScanDirectoryListResponse, ScanDirectoryResponse, ShortFeedInteractionRecord,
    ShortFeedServerStatus, ShortFeedVideoRecord, SubtitleGenerateResult, SubtitleJobStatus,
    SubtitleSearchResponse, TagListResponse, TagRecord, VideoListResponse, VideoMutationResponse,
};
use cine_daemon::{app, serve_listener, DaemonConfig, DaemonState};
use cine_db::{
    seed_library_management_fixture, seed_remaining_slices_fixture,
    seed_video_file_operation_fixture, seed_video_query_fixture,
};
use http::{header::AUTHORIZATION, Request, StatusCode};
use std::fs;
use tempfile::TempDir;
use tower::ServiceExt;

#[tokio::test]
async fn short_feed_status_points_to_public_browser_server() {
    let assets = TempDir::new().expect("short feed assets");
    fs::create_dir_all(assets.path().join("assets")).expect("assets dir");
    fs::write(
        assets.path().join("short.html"),
        r#"<html><body><div id="short-feed-app"></div></body></html>"#,
    )
    .expect("short html");

    let state = DaemonState::for_test("secret-token").with_short_feed_server_for_test(
        assets.path(),
        "127.0.0.1",
        0,
        0,
    );
    let app = app(state);

    let response = app
        .oneshot(
            Request::builder()
                .uri("/api/short-feed/status")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let status: ShortFeedServerStatus = serde_json::from_slice(&bytes).unwrap();
    assert!(status.running);
    assert!(status.port > 0);
    assert!(status.url.ends_with("/short/"));

    let html = reqwest::get(&status.url)
        .await
        .expect("open short feed")
        .text()
        .await
        .expect("short feed html");
    assert!(html.contains("short-feed-app"));
}

#[tokio::test]
async fn video_routes_require_bearer_token() {
    let app = app(DaemonState::for_test("secret-token"));

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/videos")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();

    assert_eq!(response.status(), StatusCode::UNAUTHORIZED);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/videos/search")
                .body(Body::from("{}"))
                .unwrap(),
        )
        .await
        .unwrap();

    assert_eq!(response.status(), StatusCode::UNAUTHORIZED);

    let response = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/videos/random-candidate")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();

    assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
}

#[tokio::test]
async fn video_file_operation_routes_require_bearer_token() {
    let app = app(DaemonState::for_test("secret-token"));

    for (method, uri, body) in [
        ("POST", "/api/videos/scan", r#"{"path":"/tmp"}"#),
        ("POST", "/api/videos/add", r#"{"path":"/tmp/a.mp4"}"#),
        ("GET", "/api/videos/by-directory?path=/tmp", ""),
        ("POST", "/api/videos/1/rename", r#"{"name":"renamed"}"#),
        ("POST", "/api/videos/1/relocate", r#"{"path":"/tmp/b.mp4"}"#),
        ("POST", "/api/videos/1/refresh-metadata", ""),
        ("POST", "/api/videos/1/open-directory", ""),
        ("POST", "/api/videos/1/delete", r#"{"delete_file":false}"#),
        (
            "POST",
            "/api/videos/batch/delete",
            r#"{"video_ids":[1],"delete_file":false}"#,
        ),
        (
            "POST",
            "/api/videos/batch/tags/add",
            r#"{"video_ids":[1],"tag_id":10}"#,
        ),
        (
            "POST",
            "/api/videos/batch/tags/remove",
            r#"{"video_ids":[1],"tag_id":10}"#,
        ),
        (
            "POST",
            "/api/videos/batch/refresh-metadata",
            r#"{"video_ids":[1]}"#,
        ),
    ] {
        let response = app
            .clone()
            .oneshot(
                Request::builder()
                    .method(method)
                    .uri(uri)
                    .body(Body::from(body))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::UNAUTHORIZED, "{uri}");
    }
}

#[tokio::test]
async fn library_management_routes_require_bearer_token() {
    let app = app(DaemonState::for_test("secret-token"));

    for (method, uri, body) in [
        ("GET", "/api/tags", ""),
        ("POST", "/api/tags", r#"{"name":"sport"}"#),
        (
            "POST",
            "/api/tags/1",
            r##"{"name":"sport","color":"#fff"}"##,
        ),
        ("POST", "/api/tags/1/delete", ""),
        ("POST", "/api/videos/1/tags", r#"{"tag_id":10}"#),
        ("POST", "/api/videos/1/tags/delete", r#"{"tag_id":10}"#),
        ("GET", "/api/settings", ""),
        (
            "POST",
            "/api/settings",
            r#"{"video_extensions":".mp4","play_weight":2.0}"#,
        ),
        ("GET", "/api/scan-directories", ""),
        (
            "POST",
            "/api/scan-directories",
            r#"{"path":"/library","alias":"Library"}"#,
        ),
        (
            "POST",
            "/api/scan-directories/1",
            r#"{"path":"/library","alias":"Library"}"#,
        ),
        ("POST", "/api/scan-directories/1/delete", ""),
        ("POST", "/api/scan-directories/sync", ""),
    ] {
        let response = app
            .clone()
            .oneshot(
                Request::builder()
                    .method(method)
                    .uri(uri)
                    .body(Body::from(body))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::UNAUTHORIZED, "{uri}");
    }
}

#[tokio::test]
async fn video_library_parity_routes_expose_legacy_operations() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres route test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let original_path = root.path().join("original.mp4");
    let relocated_path = root.path().join("relocated.mp4");
    fs::write(&original_path, b"video").expect("write original video");
    fs::write(&relocated_path, b"relocated video").expect("write relocated video");
    let pool = seed_video_file_operation_fixture(&database_url)
        .await
        .expect("seed fixture");
    let video = cine_db::add_video(&pool, &original_path)
        .await
        .expect("add video");
    let app = app(DaemonState::with_pool_for_test("secret-token", pool));

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri(format!("/api/videos/{}/relocate", video.id))
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(format!(
                    r#"{{"path":"{}"}}"#,
                    relocated_path.to_string_lossy()
                )))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let payload: VideoMutationResponse = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(
        payload.video.expect("relocated video").name,
        "relocated.mp4"
    );

    let escaped_root = root.path().to_string_lossy().replace('/', "%2F");
    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri(format!("/api/videos/by-directory?path={escaped_root}"))
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let page: VideoListResponse = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(page.videos.len(), 1);
    assert_eq!(page.videos[0].path, relocated_path.to_string_lossy());

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/videos/batch/tags/add")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(format!(
                    r#"{{"video_ids":[{}],"tag_id":10}}"#,
                    video.id
                )))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let batch: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(batch["requested"], 1);
    assert_eq!(batch["succeeded"], 1);
    assert_eq!(batch["failed"], 0);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri(format!("/api/videos/{}/open-directory", video.id))
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::NO_CONTENT);

    let response = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/videos/batch/delete")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(format!(
                    r#"{{"video_ids":[{}],"delete_file":false}}"#,
                    video.id
                )))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let batch: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(batch["requested"], 1);
    assert_eq!(batch["succeeded"], 1);
    assert_eq!(batch["failed"], 0);
}

#[tokio::test]
async fn remaining_slice_routes_require_bearer_token() {
    let app = app(DaemonState::for_test("secret-token"));

    for (method, uri, body) in [
        ("GET", "/api/short-feed/status", ""),
        (
            "POST",
            "/api/logs/frontend",
            r#"{"level":"info","source":"test","message":"hello"}"#,
        ),
        ("GET", "/api/subtitles/engines", ""),
        ("POST", "/api/subtitles/prepare", r#"{"engine":"whisperx"}"#),
        ("GET", "/api/subtitles/dependencies", ""),
        ("POST", "/api/subtitles/dependencies/download", ""),
        (
            "POST",
            "/api/subtitles/generate",
            r#"{"video_id":1,"engine":"whisperx","source_lang":"auto"}"#,
        ),
        (
            "POST",
            "/api/subtitles/force-generate",
            r#"{"video_id":1,"engine":"whisperx","source_lang":"auto"}"#,
        ),
        ("POST", "/api/subtitles/cancel", ""),
        ("GET", "/api/subtitles/search?keyword=world", ""),
        ("GET", "/api/videos/1/subtitles", ""),
        (
            "POST",
            "/api/videos/1/subtitles/index",
            r#"{"path":"/tmp/a.srt"}"#,
        ),
        ("GET", "/api/ai-tags/candidates", ""),
        (
            "POST",
            "/api/ai-tags/candidates",
            r#"{"video_id":1,"suggested_name":"Night","normalized_name":"night","matched_tag_id":null,"confidence":"high","reasoning":"","source_summary":""}"#,
        ),
        ("POST", "/api/ai-tags/candidates/1/approve", ""),
        ("POST", "/api/ai-tags/candidates/1/reject", ""),
        ("POST", "/api/ai-tags/videos/1/reject-pending", ""),
        ("POST", "/api/ai-tags/videos/1/retry", ""),
        ("GET", "/api/ai-tags/status-summary", ""),
        ("GET", "/api/short-feed/next", ""),
        (
            "POST",
            "/api/short-feed/videos/1/feedback",
            r#"{"liked":true,"favorited":true,"viewed":true}"#,
        ),
        (
            "POST",
            "/api/cleanup/analyze",
            r#"{"max_duration_seconds":300.0,"min_width":640,"min_height":360}"#,
        ),
        (
            "POST",
            "/api/cleanup/start",
            r#"{"max_duration_seconds":300.0,"min_width":640,"min_height":360}"#,
        ),
        ("GET", "/api/cleanup/status", ""),
        ("GET", "/api/diagnostics", ""),
    ] {
        let response = app
            .clone()
            .oneshot(
                Request::builder()
                    .method(method)
                    .uri(uri)
                    .body(Body::from(body))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::UNAUTHORIZED, "{uri}");
    }
}

#[tokio::test]
async fn scan_directory_route_returns_legacy_scanned_files() {
    let root = TempDir::new().expect("temp root");
    let video_path = root.path().join("route-scan.mp4");
    fs::write(&video_path, b"video").expect("write video");
    let old_time = filetime::FileTime::from_unix_time(0, 0);
    filetime::set_file_mtime(&video_path, old_time).expect("set old mtime");
    let app = app(DaemonState::for_test("secret-token"));

    let response = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/videos/scan")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(format!(
                    r#"{{"path":"{}","extensions":"mp4"}}"#,
                    root.path().to_string_lossy()
                )))
                .unwrap(),
        )
        .await
        .unwrap();

    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let payload: ScanDirectoryResponse = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(payload.files.len(), 1);
    assert_eq!(payload.files[0].size, 5);
}

#[tokio::test]
async fn video_list_route_returns_contract_shape_with_valid_token() {
    let app = app(DaemonState::for_test("secret-token"));

    let response = app
        .oneshot(
            Request::builder()
                .uri("/api/videos")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();

    assert_eq!(response.status(), StatusCode::OK);

    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let payload: serde_json::Value = serde_json::from_slice(&bytes).unwrap();

    assert!(payload["videos"].is_array());
    assert!(payload.get("next_cursor").is_some());
}

#[tokio::test]
async fn video_search_route_accepts_empty_body_as_default_filter() {
    let app = app(DaemonState::for_test("secret-token"));

    let response = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/videos/search")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();

    assert_eq!(response.status(), StatusCode::OK);
}

#[tokio::test]
async fn video_list_route_reads_postgres_fixture_when_pool_is_configured() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres route test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let pool = seed_video_query_fixture(&database_url)
        .await
        .expect("seed fixture");
    let app = app(DaemonState::with_pool_for_test("secret-token", pool));

    let response = app
        .oneshot(
            Request::builder()
                .uri("/api/videos")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();

    assert_eq!(response.status(), StatusCode::OK);

    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let payload: VideoListResponse = serde_json::from_slice(&bytes).unwrap();
    let names = payload
        .videos
        .iter()
        .map(|video| video.name.as_str())
        .collect::<Vec<_>>();

    assert_eq!(
        names,
        vec![
            "zero-large.mp4",
            "zero-small.mp4",
            "two-large.mp4",
            "cat_sleep.mp4",
            "cat_run.mp4",
        ]
    );
}

#[tokio::test]
async fn daemon_listener_serves_video_routes_from_configured_postgres_pool() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres listener test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let pool = seed_video_query_fixture(&database_url)
        .await
        .expect("seed fixture");
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind listener");
    let address = listener.local_addr().expect("local address");
    let server = tokio::spawn(serve_listener(
        listener,
        DaemonConfig {
            token: "secret-token".to_string(),
            pool: Some(pool),
            enable_system_dispatch: false,
            asr_sidecar_dir: None,
            asr_python_bin: None,
            asr_runtime_dir: None,
            short_feed_assets_dir: None,
            short_feed_bind_address: "0.0.0.0".to_string(),
            short_feed_port_start: 18088,
            short_feed_port_end: 18108,
            skip_audio_extract: false,
        },
    ));

    let client = reqwest::Client::new();
    let response = client
        .get(format!("http://{address}/api/videos"))
        .bearer_auth("secret-token")
        .send()
        .await
        .expect("send request");

    assert_eq!(response.status(), reqwest::StatusCode::OK);

    let payload: VideoListResponse = response.json().await.expect("decode response");
    assert_eq!(payload.videos[0].name, "zero-large.mp4");

    server.abort();
}

#[tokio::test]
async fn video_file_operation_routes_mutate_postgres_fixture() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres route test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let video_path = root.path().join("route-video.mp4");
    fs::write(&video_path, b"video").expect("write video");
    let pool = seed_video_file_operation_fixture(&database_url)
        .await
        .expect("seed fixture");
    let app = app(DaemonState::with_pool_for_test("secret-token", pool));

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/videos/add")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(format!(
                    r#"{{"path":"{}"}}"#,
                    video_path.to_string_lossy()
                )))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let payload: VideoMutationResponse = serde_json::from_slice(&bytes).unwrap();
    let video = payload.video.expect("added video");
    assert_eq!(video.name, "route-video.mp4");

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri(format!("/api/videos/{}/rename", video.id))
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(r#"{"name":"route-renamed"}"#))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let payload: VideoMutationResponse = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(
        payload.video.expect("renamed video").name,
        "route-renamed.mp4"
    );

    let response = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri(format!("/api/videos/{}/delete", video.id))
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(r#"{"delete_file":false}"#))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let payload: VideoMutationResponse = serde_json::from_slice(&bytes).unwrap();
    assert!(payload.ok);
    assert!(payload.video.is_none());
}

#[tokio::test]
async fn preview_and_playback_routes_expose_native_library_behaviors() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres route test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let video_path = root.path().join("route-play.mp4");
    fs::write(&video_path, b"video").expect("write video");
    let pool = seed_video_file_operation_fixture(&database_url)
        .await
        .expect("seed fixture");
    let video = cine_db::add_video(&pool, &video_path)
        .await
        .expect("add video");
    let app = app(DaemonState::with_pool_for_test("secret-token", pool));

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri(format!("/api/videos/{}/preview-session", video.id))
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let preview: PreviewSessionResponse = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(preview.mode, cine_api::PreviewMode::Inline);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri(format!("/api/videos/{}/preview-externally", video.id))
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::NO_CONTENT);

    let response = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri(format!("/api/videos/{}/play", video.id))
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let playback: PlaybackAttemptResponse = serde_json::from_slice(&bytes).unwrap();
    assert!(playback.dispatch_succeeded);
    assert_eq!(playback.video.expect("played video").play_count, 1);
}

#[tokio::test]
async fn library_management_routes_mutate_postgres_fixture_and_redact_settings() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres route test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let pool = seed_library_management_fixture(&database_url)
        .await
        .expect("seed fixture");
    let app = app(DaemonState::with_pool_for_test("secret-token", pool));

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/tags")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(r##"{"name":"route-tag","color":"#123456"}"##))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let tag: TagRecord = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(tag.name, "route-tag");

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri(format!("/api/tags/{}", tag.id))
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(
                    r##"{"name":"route-renamed","color":"#abcdef"}"##,
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let renamed: TagRecord = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(renamed.name, "route-renamed");

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/tags")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let tags: TagListResponse = serde_json::from_slice(&bytes).unwrap();
    assert!(tags.tags.iter().any(|tag| tag.name == "route-renamed"));

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/videos/1/tags")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(format!(r#"{{"tag_id":{}}}"#, renamed.id)))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::NO_CONTENT);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/videos/1/tags/delete")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(format!(r#"{{"tag_id":{}}}"#, renamed.id)))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::NO_CONTENT);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri(format!("/api/tags/{}/delete", renamed.id))
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::NO_CONTENT);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/settings")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(
                    r##"{
                        "confirm_before_delete": false,
                        "delete_original_file": false,
                        "video_extensions": ".mp4,.mkv",
                        "play_weight": 2.5,
                        "auto_scan_on_startup": true,
                        "short_feed_max_duration_minutes": 0,
                        "theme": "dark",
                        "log_enabled": true,
                        "bilingual_enabled": false,
                        "bilingual_lang": "zh",
                        "deepl_api_key": "deepl-secret",
                        "ai_tagging_base_url": "https://example.invalid",
                        "ai_tagging_api_key": "ai-secret",
                        "ai_tagging_model": "vision-model",
                        "ai_tagging_frame_count": 0,
                        "ai_tagging_subtitle_char_limit": 0,
                        "ai_tagging_startup_batch_size": 0
                    }"##,
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/settings")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let settings: PublicSettings = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(settings.video_extensions, ".mp4,.mkv");
    assert_eq!(settings.short_feed_max_duration_minutes, 5);
    assert!(settings.deepl_api_key_configured);
    assert!(settings.ai_tagging_api_key_configured);
    let settings_value: serde_json::Value = serde_json::to_value(settings).unwrap();
    assert!(settings_value.get("deepl_api_key").is_none());
    assert!(settings_value.get("ai_tagging_api_key").is_none());

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/scan-directories")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"path":"/library/route","alias":"Route Library"}"#,
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let directory: cine_api::ScanDirectoryRecord = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(directory.alias, "Route Library");

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri(format!("/api/scan-directories/{}", directory.id))
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"path":"/library/route-renamed","alias":"Renamed"}"#,
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/scan-directories")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let directories: ScanDirectoryListResponse = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(directories.directories[0].path, "/library/route-renamed");

    let response = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri(format!("/api/scan-directories/{}/delete", directory.id))
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::NO_CONTENT);
}

#[tokio::test]
async fn settings_route_returns_default_settings_without_database_for_native_shell() {
    let app = app(DaemonState::for_test("secret-token"));

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/settings")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let settings: PublicSettings = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(settings.theme, "system");
    assert_eq!(settings.bilingual_lang, "zh");
    assert!(!settings.deepl_api_key_configured);

    let response = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/settings")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(
                    r##"{
                        "confirm_before_delete": true,
                        "delete_original_file": false,
                        "video_extensions": ".mp4",
                        "play_weight": 2.0,
                        "auto_scan_on_startup": false,
                        "short_feed_max_duration_minutes": 5,
                        "theme": "dark",
                        "log_enabled": false,
                        "bilingual_enabled": false,
                        "bilingual_lang": "zh",
                        "deepl_api_key": "",
                        "ai_tagging_base_url": "",
                        "ai_tagging_api_key": "",
                        "ai_tagging_model": "",
                        "ai_tagging_frame_count": 5,
                        "ai_tagging_subtitle_char_limit": 4000,
                        "ai_tagging_startup_batch_size": 10
                    }"##,
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
}

#[tokio::test]
async fn remaining_slice_routes_expose_subtitles_ai_short_feed_cleanup_and_diagnostics() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres route test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let srt_path = root.path().join("short-a.srt");
    fs::write(&srt_path, "1\n00:00:01,000 --> 00:00:03,000\nhello world\n").expect("write srt");
    fs::write(root.path().join("short-a.mp4"), b"same-content").expect("write a");
    fs::write(root.path().join("short-b.mp4"), b"same-content").expect("write b");
    fs::write(root.path().join("long.mp4"), b"long").expect("write long");
    let pool = seed_remaining_slices_fixture(&database_url, root.path())
        .await
        .expect("seed fixture");
    let app = app(DaemonState::with_pool_for_test("secret-token", pool));

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/subtitles/engines")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/subtitles/generate")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"video_id":1,"engine":"whisperx","source_lang":"auto"}"#,
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/videos/1/subtitles/index")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(format!(
                    r#"{{"path":"{}"}}"#,
                    srt_path.to_string_lossy()
                )))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/subtitles/search?keyword=world")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let search: SubtitleSearchResponse = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(search.matches[0].segment.text, "hello world");

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/ai-tags/candidates")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"video_id":1,"suggested_name":"Night","normalized_name":"night","matched_tag_id":null,"confidence":"high","reasoning":"frame","source_summary":"evidence"}"#,
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/ai-tags/candidates/1/approve")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/ai-tags/candidates")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let candidates: AITagCandidateListResponse = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(
        candidates.candidates[0].status,
        cine_api::AITagCandidateStatus::Approved
    );

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/short-feed/status")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let short_status: ShortFeedServerStatus = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(
        short_status.allowed_access,
        "loopback/private-lan/link-local only, no login"
    );

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/logs/frontend")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"level":"info","source":"smoke","message":"native log"}"#,
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::NO_CONTENT);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/ai-tags/videos/1/reject-pending")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let rejected: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
    assert!(rejected["rejected"].as_i64().unwrap_or_default() >= 0);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/ai-tags/videos/1/retry")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::NO_CONTENT);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/ai-tags/status-summary")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let summary: AITaggingStatusSummary = serde_json::from_slice(&bytes).unwrap();
    assert!(summary.config_available);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/short-feed/next")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let next: ShortFeedVideoRecord = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(next.id, 1);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/short-feed/videos/1/feedback")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"liked":true,"favorited":true,"viewed":true}"#,
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let feedback: ShortFeedInteractionRecord = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(feedback.view_count, 1);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/cleanup/analyze")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"max_duration_seconds":300.0,"min_width":640,"min_height":360}"#,
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let cleanup: CleanupAnalysisRecord = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(cleanup.duplicate_groups[0].candidate_ids, vec![2]);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/cleanup/start")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"max_duration_seconds":300.0,"min_width":640,"min_height":360}"#,
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let cleanup_status: CleanupStatus = serde_json::from_slice(&bytes).unwrap();
    assert!(cleanup_status.running || cleanup_status.completed);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/cleanup/status")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let _cleanup_status: CleanupStatus = serde_json::from_slice(&bytes).unwrap();

    let response = app
        .oneshot(
            Request::builder()
                .uri("/api/diagnostics")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let diagnostics: DiagnosticsSnapshot = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(diagnostics.video_count, 3);
    assert!(diagnostics.redacted_settings.deepl_api_key_configured);
}

#[tokio::test]
async fn subtitle_status_and_cancel_routes_expose_single_active_job_contract_without_database() {
    let app = app(DaemonState::for_test("secret-token"));

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/subtitles/status")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let status: SubtitleJobStatus = serde_json::from_slice(&bytes).unwrap();
    assert!(!status.running);
    assert_eq!(status.progress.action, "idle");

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/subtitles/cancel")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::NO_CONTENT);

    let response = app
        .oneshot(
            Request::builder()
                .uri("/api/subtitles/status")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let status: SubtitleJobStatus = serde_json::from_slice(&bytes).unwrap();
    assert!(status.cancelled);
    assert_eq!(status.result.expect("cancel result").status, "cancelled");
}

#[tokio::test]
async fn subtitle_prepare_creates_runtime_layout_and_updates_dependency_status() {
    let runtime_dir = TempDir::new().expect("runtime root");
    let sidecar_dir = TempDir::new().expect("sidecar root");
    fs::write(
        sidecar_dir.path().join("whisperx_worker.py"),
        "print('ok')\n",
    )
    .expect("write fake whisperx worker");
    fs::write(
        sidecar_dir.path().join("qwen_asr_worker.py"),
        "print('ok')\n",
    )
    .expect("write fake qwen worker");
    let app = app(DaemonState::for_test("secret-token")
        .with_asr_sidecar_for_test(sidecar_dir.path(), "python3", true)
        .with_asr_runtime_for_test(runtime_dir.path()));

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/subtitles/prepare")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(r#"{"engine":"whisperx"}"#))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::NO_CONTENT);
    assert!(runtime_dir
        .path()
        .join("whisperx_sidecar/venv/bin/python3")
        .exists());
    assert!(runtime_dir
        .path()
        .join("whisperx_sidecar/whisperx_worker.py")
        .exists());

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/subtitles/dependencies")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let deps: std::collections::BTreeMap<String, bool> = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(deps.get("whisper"), Some(&true));
    assert_eq!(deps.get("model"), Some(&true));

    let response = app
        .oneshot(
            Request::builder()
                .uri("/api/subtitles/engines")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let engines: Vec<cine_api::SubtitleEngineStatus> = serde_json::from_slice(&bytes).unwrap();
    let whisper = engines
        .into_iter()
        .find(|engine| engine.engine == cine_api::SubtitleEngine::Whisperx)
        .expect("whisper status");
    assert!(whisper.available);
    assert!(!whisper.needs_prepare);
}

#[tokio::test]
async fn subtitle_generate_dispatches_python_sidecar_and_indexes_srt() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres route test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let sidecar_dir = TempDir::new().expect("sidecar root");
    let worker_path = sidecar_dir.path().join("whisperx_worker.py");
    fs::write(
        &worker_path,
        r#"#!/usr/bin/env python3
import json
print(json.dumps({
    "language": "en",
    "segments": [
        {"start": 0.0, "end": 1.25, "text": "hello from rust sidecar"},
        {"start": 1.25, "end": 2.0, "text": "indexed subtitle"}
    ]
}))
"#,
    )
    .expect("write fake worker");
    fs::write(root.path().join("short-a.mp4"), b"fake video").expect("write a");
    fs::write(root.path().join("short-b.mp4"), b"same-content").expect("write b");
    fs::write(root.path().join("long.mp4"), b"long").expect("write long");
    let pool = seed_remaining_slices_fixture(&database_url, root.path())
        .await
        .expect("seed fixture");
    let state = DaemonState::with_pool_for_test("secret-token", pool).with_asr_sidecar_for_test(
        sidecar_dir.path(),
        "python3",
        true,
    );
    let app = app(state);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/subtitles/generate")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"video_id":1,"engine":"whisperx","source_lang":"auto"}"#,
                ))
                .unwrap(),
        )
        .await
        .unwrap();

    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let payload: SubtitleGenerateResult = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(payload.status, "success");
    let srt_path = root.path().join("short-a.srt");
    let content = fs::read_to_string(&srt_path).expect("read generated srt");
    assert!(content.contains("hello from rust sidecar"));
    assert!(content.contains("00:00:00,000 --> 00:00:01,250"));

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/subtitles/search?keyword=sidecar")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let payload: SubtitleSearchResponse = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(payload.matches.len(), 1);
    assert_eq!(payload.matches[0].video.id, 1);
}

#[tokio::test]
async fn subtitle_generation_reports_status_can_cancel_and_preserves_force_eligible_failures() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres route test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let sidecar_dir = TempDir::new().expect("sidecar root");
    fs::write(
        sidecar_dir.path().join("whisperx_worker.py"),
        r#"#!/usr/bin/env python3
import json
segments = [{"start": float(i), "end": float(i) + 0.4, "text": "repeat"} for i in range(40)]
print(json.dumps({"language": "en", "segments": segments}))
"#,
    )
    .expect("write fake worker");
    fs::write(root.path().join("short-a.mp4"), b"fake video").expect("write a");
    fs::write(root.path().join("short-b.mp4"), b"same-content").expect("write b");
    fs::write(root.path().join("long.mp4"), b"long").expect("write long");
    let pool = seed_remaining_slices_fixture(&database_url, root.path())
        .await
        .expect("seed fixture");
    let state = DaemonState::with_pool_for_test("secret-token", pool).with_asr_sidecar_for_test(
        sidecar_dir.path(),
        "python3",
        true,
    );
    let app = app(state);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/subtitles/generate")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"video_id":1,"engine":"whisperx","source_lang":"auto"}"#,
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let payload: SubtitleGenerateResult = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(payload.status, "validation_failed");
    assert_eq!(
        payload.validation_code.as_deref(),
        Some("hallucination_detected")
    );
    assert!(payload.force_eligible);
    assert!(!root.path().join("short-a.srt").exists());

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/subtitles/force-generate")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"video_id":1,"engine":"whisperx","source_lang":"auto"}"#,
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let payload: SubtitleGenerateResult = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(payload.status, "success");
    assert!(root.path().join("short-a.srt").exists());

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/subtitles/status")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let status: SubtitleJobStatus = serde_json::from_slice(&bytes).unwrap();
    assert!(status.completed);
    assert_eq!(status.progress.phase, "finalizing");

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/subtitles/cancel")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::NO_CONTENT);

    let response = app
        .oneshot(
            Request::builder()
                .uri("/api/subtitles/status")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let status: SubtitleJobStatus = serde_json::from_slice(&bytes).unwrap();
    assert!(status.cancelled);
    assert_eq!(status.result.expect("cancel result").status, "cancelled");
}

#[tokio::test]
async fn subtitle_generation_merges_bilingual_deepl_translation_when_enabled() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres route test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let sidecar_dir = TempDir::new().expect("sidecar root");
    fs::write(
        sidecar_dir.path().join("whisperx_worker.py"),
        r#"#!/usr/bin/env python3
import json
print(json.dumps({
    "language": "en",
    "segments": [{"start": 0.0, "end": 1.0, "text": "hello world"}]
}))
"#,
    )
    .expect("write fake worker");
    fs::write(root.path().join("short-a.mp4"), b"fake video").expect("write a");
    fs::write(root.path().join("short-b.mp4"), b"same-content").expect("write b");
    fs::write(root.path().join("long.mp4"), b"long").expect("write long");
    let pool = seed_remaining_slices_fixture(&database_url, root.path())
        .await
        .expect("seed fixture");
    sqlx::query("UPDATE settings SET bilingual_enabled = true, bilingual_lang = 'zh', deepl_api_key = 'test-key'")
        .execute(&pool)
        .await
        .expect("enable bilingual");
    let state = DaemonState::with_pool_for_test("secret-token", pool)
        .with_asr_sidecar_for_test(sidecar_dir.path(), "python3", true)
        .with_deepl_translator_for_test(|texts: Vec<String>, _target: String| {
            texts
                .iter()
                .map(|text| format!("{text} zh"))
                .collect::<Vec<_>>()
        });
    let app = app(state);

    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/subtitles/generate")
                .header(AUTHORIZATION, "Bearer secret-token")
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"video_id":1,"engine":"whisperx","source_lang":"auto"}"#,
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let payload: SubtitleGenerateResult = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(payload.status, "success");
    let srt = fs::read_to_string(root.path().join("short-a.srt")).expect("read srt");
    assert!(srt.contains("hello world\nhello world zh"));
}

#[tokio::test]
async fn subtitle_cancel_terminates_running_python_sidecar() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres route test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let sidecar_dir = TempDir::new().expect("sidecar root");
    fs::write(
        sidecar_dir.path().join("whisperx_worker.py"),
        r#"#!/usr/bin/env python3
import json, time
time.sleep(5)
print(json.dumps({"language": "en", "segments": [{"start": 0.0, "end": 1.0, "text": "late"}]}))
"#,
    )
    .expect("write slow worker");
    fs::write(root.path().join("short-a.mp4"), b"fake video").expect("write a");
    fs::write(root.path().join("short-b.mp4"), b"same-content").expect("write b");
    fs::write(root.path().join("long.mp4"), b"long").expect("write long");
    let pool = seed_remaining_slices_fixture(&database_url, root.path())
        .await
        .expect("seed fixture");
    let app = app(
        DaemonState::with_pool_for_test("secret-token", pool).with_asr_sidecar_for_test(
            sidecar_dir.path(),
            "python3",
            true,
        ),
    );
    let generate_app = app.clone();
    let generate = tokio::spawn(async move {
        generate_app
            .oneshot(
                Request::builder()
                    .method("POST")
                    .uri("/api/subtitles/generate")
                    .header(AUTHORIZATION, "Bearer secret-token")
                    .header("content-type", "application/json")
                    .body(Body::from(
                        r#"{"video_id":1,"engine":"whisperx","source_lang":"auto"}"#,
                    ))
                    .unwrap(),
            )
            .await
            .unwrap()
    });
    tokio::time::sleep(std::time::Duration::from_millis(250)).await;
    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/subtitles/cancel")
                .header(AUTHORIZATION, "Bearer secret-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::NO_CONTENT);
    let response = tokio::time::timeout(std::time::Duration::from_secs(2), generate)
        .await
        .expect("generate should be cancelled promptly")
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = axum::body::to_bytes(response.into_body(), 1024 * 1024)
        .await
        .unwrap();
    let result: SubtitleGenerateResult = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(result.status, "cancelled");
    assert!(!root.path().join("short-a.srt").exists());
}
