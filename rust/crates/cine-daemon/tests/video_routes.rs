use axum::body::Body;
use cine_api::{
    AITagCandidateListResponse, CleanupAnalysisRecord, DiagnosticsSnapshot,
    PlaybackAttemptResponse, PreviewSessionResponse, PublicSettings, ScanDirectoryListResponse,
    ScanDirectoryResponse, ShortFeedInteractionRecord, ShortFeedVideoRecord,
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
        ("POST", "/api/videos/1/rename", r#"{"name":"renamed"}"#),
        ("POST", "/api/videos/1/delete", r#"{"delete_file":false}"#),
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
async fn remaining_slice_routes_require_bearer_token() {
    let app = app(DaemonState::for_test("secret-token"));

    for (method, uri, body) in [
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
