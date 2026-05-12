use std::{fs, path::Path};

use cine_db::{
    add_video, play_video_with_dispatch, preview_externally_with_dispatch, preview_session,
    seed_video_file_operation_fixture, PlaybackDispatch, PlaybackError, PreviewMode,
};
use tempfile::TempDir;

#[tokio::test]
async fn preview_session_returns_inline_external_or_missing_without_mutating_stats() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let inline_path = root.path().join("inline.mp4");
    let external_path = root.path().join("external.mkv");
    let missing_path = root.path().join("missing.mp4");
    fs::write(&inline_path, b"inline").expect("write inline");
    fs::write(&external_path, b"external").expect("write external");

    let pool = seed_video_file_operation_fixture(&database_url)
        .await
        .expect("seed fixture");
    let inline = add_video(&pool, &inline_path).await.expect("add inline");
    let external = add_video(&pool, &external_path)
        .await
        .expect("add external");
    let missing = add_video(&pool, &missing_path).await;
    assert!(missing.is_err(), "add requires a real file");
    sqlx::query("INSERT INTO videos(name, path, directory, size) VALUES ($1, $2, $3, $4)")
        .bind("missing.mp4")
        .bind(missing_path.to_string_lossy().to_string())
        .bind(root.path().to_string_lossy().to_string())
        .bind(7_i64)
        .execute(&pool)
        .await
        .expect("insert missing video");
    let missing_id: i64 = sqlx::query_scalar("SELECT id FROM videos WHERE name = 'missing.mp4'")
        .fetch_one(&pool)
        .await
        .expect("missing id");

    let inline_session = preview_session(&pool, inline.id)
        .await
        .expect("inline session");
    assert_eq!(inline_session.mode, PreviewMode::Inline);
    assert_eq!(
        inline_session.inline_source.expect("inline").mime,
        "video/mp4"
    );

    let external_session = preview_session(&pool, external.id)
        .await
        .expect("external session");
    assert_eq!(external_session.mode, PreviewMode::ExternalPreview);
    assert_eq!(
        external_session
            .external_action
            .expect("external")
            .action_id,
        "preview_externally"
    );

    let missing_session = preview_session(&pool, missing_id)
        .await
        .expect("missing session");
    assert_eq!(missing_session.mode, PreviewMode::Unsupported);
    assert_eq!(missing_session.reason_code.as_deref(), Some("file_missing"));

    let stats: (i32, i32, Option<String>) = sqlx::query_as(
        "SELECT play_count, random_play_count, last_played_at::text FROM videos WHERE id = $1",
    )
    .bind(inline.id)
    .fetch_one(&pool)
    .await
    .expect("stats");
    assert_eq!(stats, (0, 0, None));
}

#[tokio::test]
async fn external_preview_dispatch_does_not_update_formal_play_stats() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let path = root.path().join("preview.mp4");
    fs::write(&path, b"preview").expect("write preview");
    let pool = seed_video_file_operation_fixture(&database_url)
        .await
        .expect("seed fixture");
    let video = add_video(&pool, &path).await.expect("add video");

    let mut dispatch = RecordingDispatch::default();
    preview_externally_with_dispatch(&pool, video.id, &mut dispatch)
        .await
        .expect("preview externally");

    assert_eq!(dispatch.paths, vec![path.to_string_lossy().to_string()]);
    let stats: (i32, i32, Option<String>) = sqlx::query_as(
        "SELECT play_count, random_play_count, last_played_at::text FROM videos WHERE id = $1",
    )
    .bind(video.id)
    .fetch_one(&pool)
    .await
    .expect("stats");
    assert_eq!(stats, (0, 0, None));
}

#[tokio::test]
async fn formal_playback_updates_stats_only_after_dispatch_success_and_marks_missing_stale() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let path = root.path().join("formal.mp4");
    fs::write(&path, b"formal").expect("write formal");
    let pool = seed_video_file_operation_fixture(&database_url)
        .await
        .expect("seed fixture");
    let video = add_video(&pool, &path).await.expect("add video");

    let mut failed = RecordingDispatch {
        fail: true,
        ..RecordingDispatch::default()
    };
    let failed_result = play_video_with_dispatch(&pool, video.id, false, &mut failed)
        .await
        .expect("domain result");
    assert!(!failed_result.dispatch_succeeded);
    assert_eq!(
        failed_result.reason_code.as_deref(),
        Some("dispatch_failed")
    );
    assert_eq!(stats_for_video(&pool, video.id).await, (0, 0, None, false));

    let mut success = RecordingDispatch::default();
    let success_result = play_video_with_dispatch(&pool, video.id, false, &mut success)
        .await
        .expect("play video");
    assert!(success_result.dispatch_succeeded);
    let stats = stats_for_video(&pool, video.id).await;
    assert_eq!(stats.0, 1);
    assert_eq!(stats.1, 0);
    assert!(stats.2.is_some());
    assert!(!stats.3);

    fs::remove_file(&path).expect("remove video");
    let mut dispatch = RecordingDispatch::default();
    let missing_result = play_video_with_dispatch(&pool, video.id, false, &mut dispatch)
        .await
        .expect("missing result");
    assert!(!missing_result.dispatch_succeeded);
    assert_eq!(missing_result.reason_code.as_deref(), Some("file_missing"));
    assert!(missing_result
        .reconcile_result
        .as_ref()
        .is_some_and(|result| result.did_mark_stale));
    assert!(stats_for_video(&pool, video.id).await.3);
}

async fn stats_for_video(pool: &sqlx::PgPool, id: i64) -> (i32, i32, Option<String>, bool) {
    sqlx::query_as(
        "SELECT play_count, random_play_count, last_played_at::text, is_stale FROM videos WHERE id = $1",
    )
    .bind(id)
    .fetch_one(pool)
    .await
    .expect("stats")
}

#[derive(Default)]
struct RecordingDispatch {
    paths: Vec<String>,
    fail: bool,
}

impl PlaybackDispatch for RecordingDispatch {
    fn dispatch(&mut self, path: &Path) -> Result<(), PlaybackError> {
        self.paths.push(path.to_string_lossy().to_string());
        if self.fail {
            Err(PlaybackError::DispatchFailed("blocked".to_string()))
        } else {
            Ok(())
        }
    }
}
