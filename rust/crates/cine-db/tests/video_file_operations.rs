use std::{fs, path::Path, time::SystemTime};

use cine_db::{
    add_video, delete_video, refresh_video_metadata_with_probe, relocate_video, rename_video,
    scan_directory_with_extensions, seed_video_file_operation_fixture,
    sync_scan_directories_with_probe, MediaMetadata, VideoMutationError,
};
use filetime::FileTime;
use tempfile::TempDir;

#[test]
fn scans_video_files_with_legacy_skip_rules_and_extension_normalization() {
    let root = TempDir::new().expect("temp root");
    write_old_file(root.path().join("movie.mp4"), b"video");
    write_old_file(root.path().join("clip.MKV"), b"video");
    write_old_file(root.path().join(".hidden.mp4"), b"video");
    write_old_file(root.path().join("draft.tmp.mp4"), b"video");
    write_old_file(
        root.path().join("component.ts"),
        b"export const notVideo = true;",
    );

    let trash_dir = root.path().join(".cineinsight_trash");
    fs::create_dir(&trash_dir).expect("trash dir");
    write_old_file(trash_dir.join("trashed.mp4"), b"video");

    let nested = root.path().join("nested");
    fs::create_dir(&nested).expect("nested dir");
    write_old_file(nested.join("episode.mov"), b"video");

    let files =
        scan_directory_with_extensions(root.path(), "mp4, .mkv,.mov").expect("scan directory");
    let names = files
        .iter()
        .map(|file| {
            Path::new(&file.path)
                .file_name()
                .unwrap()
                .to_string_lossy()
                .to_string()
        })
        .collect::<Vec<_>>();

    assert_eq!(names, vec!["clip.MKV", "movie.mp4", "episode.mov"]);
    assert!(files.iter().all(|file| file.size == 5));
}

#[tokio::test]
async fn adds_and_soft_deletes_video_records_without_touching_duplicate_paths() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let path = root.path().join("new-video.mp4");
    fs::write(&path, b"video bytes").expect("write video");

    let pool = seed_video_file_operation_fixture(&database_url)
        .await
        .expect("seed fixture");

    let video = add_video(&pool, &path).await.expect("add video");
    assert_eq!(video.name, "new-video.mp4");
    assert_eq!(video.size, 11);

    let duplicate = add_video(&pool, &path)
        .await
        .expect_err("duplicate active path should fail");
    assert_eq!(duplicate, VideoMutationError::VideoExists);

    delete_video(&pool, video.id, false)
        .await
        .expect("soft delete");
    let deleted_at: Option<String> =
        sqlx::query_scalar("SELECT deleted_at::text FROM videos WHERE id = $1")
            .bind(video.id)
            .fetch_one(&pool)
            .await
            .expect("deleted_at");
    assert!(deleted_at.is_some());
    assert!(
        path.exists(),
        "soft delete should not remove the original file"
    );

    let reimport = add_video(&pool, &path)
        .await
        .expect_err("soft-deleted path should not be reimported");
    assert_eq!(reimport, VideoMutationError::VideoExists);
}

#[tokio::test]
async fn rename_preserves_extension_rejects_collisions_and_rolls_back_file_on_db_failure() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let old_path = root.path().join("old-name.mp4");
    let collision_path = root.path().join("taken.mp4");
    fs::write(&old_path, b"video").expect("write old video");
    fs::write(&collision_path, b"other").expect("write collision");

    let pool = seed_video_file_operation_fixture(&database_url)
        .await
        .expect("seed fixture");
    let video = add_video(&pool, &old_path).await.expect("add video");

    let collision = rename_video(&pool, video.id, "taken")
        .await
        .expect_err("existing target should fail");
    assert_eq!(collision, VideoMutationError::TargetExists);

    let renamed = rename_video(&pool, video.id, "renamed")
        .await
        .expect("rename video");
    assert_eq!(renamed.name, "renamed.mp4");
    assert!(root.path().join("renamed.mp4").exists());
    assert!(!old_path.exists());

    fs::write(&old_path, b"blocker").expect("rewrite old file");
    let blocker = add_video(&pool, &old_path).await.expect("add blocker");
    let rollback_target = root.path().join("rollback.mp4");
    sqlx::query("INSERT INTO videos(name, path, directory, size) VALUES ($1, $2, $3, $4)")
        .bind("rollback.mp4")
        .bind(rollback_target.to_string_lossy().to_string())
        .bind(root.path().to_string_lossy().to_string())
        .bind(0_i64)
        .execute(&pool)
        .await
        .expect("insert path conflict");

    let error = rename_video(&pool, blocker.id, "rollback")
        .await
        .expect_err("db path conflict should fail after file rename");
    assert_eq!(error, VideoMutationError::DatabaseWrite);
    assert!(
        old_path.exists(),
        "failed DB update should restore old file path"
    );
    assert!(
        !rollback_target.exists(),
        "rollback should remove renamed file path"
    );
}

#[tokio::test]
async fn relocate_updates_path_and_preserves_existing_tag_links() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let old_path = root.path().join("tagged.mp4");
    let new_path = root.path().join("moved.mp4");
    fs::write(&old_path, b"video").expect("write old video");
    fs::write(&new_path, b"video moved").expect("write moved video");

    let pool = seed_video_file_operation_fixture(&database_url)
        .await
        .expect("seed fixture");
    let video = add_video(&pool, &old_path).await.expect("add video");
    sqlx::query("INSERT INTO video_tags(video_id, tag_id) VALUES ($1, 10)")
        .bind(video.id)
        .execute(&pool)
        .await
        .expect("tag video");

    let relocated = relocate_video(&pool, video.id, &new_path)
        .await
        .expect("relocate video");
    assert_eq!(relocated.name, "moved.mp4");
    assert_eq!(relocated.size, 11);

    let tag_count: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM video_tags WHERE video_id = $1")
        .bind(video.id)
        .fetch_one(&pool)
        .await
        .expect("tag count");
    assert_eq!(tag_count, 1);
}

#[tokio::test]
async fn refresh_video_metadata_uses_probe_result_and_requires_real_metadata() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let path = root.path().join("metadata.mp4");
    fs::write(&path, b"video").expect("write video");
    let pool = seed_video_file_operation_fixture(&database_url)
        .await
        .expect("seed fixture");
    let video = add_video(&pool, &path).await.expect("add video");

    let refreshed = refresh_video_metadata_with_probe(&pool, video.id, |_| {
        Some(MediaMetadata {
            duration: 12.5,
            resolution: "1920x1080".to_string(),
            width: 1920,
            height: 1080,
        })
    })
    .await
    .expect("refresh metadata");

    assert_eq!(refreshed.duration, 12.5);
    assert_eq!(refreshed.resolution, "1920x1080");
    assert_eq!(refreshed.width, 1920);
    assert_eq!(refreshed.height, 1080);

    let error = refresh_video_metadata_with_probe(&pool, video.id, |_| None)
        .await
        .expect_err("missing metadata should fail");
    assert_eq!(error, VideoMutationError::MetadataUnavailable);
}

#[tokio::test]
async fn sync_scan_directories_adds_deletes_relocates_and_preserves_tags() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let existing_path = root.path().join("existing.mp4");
    let moved_path = root.path().join("moved.mp4");
    let new_path = root.path().join("new.mp4");
    write_old_file(&existing_path, b"existing");
    write_old_file(&moved_path, b"moved-video");
    write_old_file(&new_path, b"new-video");

    let pool = seed_video_file_operation_fixture(&database_url)
        .await
        .expect("seed fixture");
    let existing = add_video(&pool, &existing_path)
        .await
        .expect("add existing");
    sqlx::query("UPDATE videos SET duration = 0, resolution = '', height = 0 WHERE id = $1")
        .bind(existing.id)
        .execute(&pool)
        .await
        .expect("clear metadata");
    let old_dir = root.path().join("old");
    fs::create_dir(&old_dir).expect("old dir");
    let missing_same_fingerprint_path = old_dir.join("moved.mp4");
    write_old_file(&missing_same_fingerprint_path, b"moved-video");
    let relocated = add_video(&pool, &missing_same_fingerprint_path)
        .await
        .expect("add relocated source");
    sqlx::query("INSERT INTO video_tags(video_id, tag_id) VALUES ($1, 10)")
        .bind(relocated.id)
        .execute(&pool)
        .await
        .expect("tag relocated");
    fs::remove_file(&missing_same_fingerprint_path).expect("remove old moved file");
    let deleted_path = root.path().join("deleted.mp4");
    write_old_file(&deleted_path, b"deleted");
    let deleted = add_video(&pool, &deleted_path).await.expect("add deleted");
    fs::remove_file(&deleted_path).expect("remove deleted file");

    let result =
        sync_scan_directories_with_probe(&pool, &[root.path().to_path_buf()], "mp4", |_| {
            Some(MediaMetadata {
                duration: 9.0,
                resolution: "640x360".to_string(),
                width: 640,
                height: 360,
            })
        })
        .await
        .expect("sync scan directories");

    assert_eq!(result.directories, 1);
    assert_eq!(result.scanned, 3);
    assert_eq!(result.added, 1);
    assert_eq!(result.relocated, 1);
    assert_eq!(result.deleted, 1);
    assert_eq!(result.metadata_refreshed, 1);
    assert_eq!(result.errors.len(), 0);

    let moved_count: i64 = sqlx::query_scalar(
        "SELECT COUNT(*) FROM videos WHERE id = $1 AND path = $2 AND deleted_at IS NULL",
    )
    .bind(relocated.id)
    .bind(moved_path.to_string_lossy().to_string())
    .fetch_one(&pool)
    .await
    .expect("moved count");
    assert_eq!(moved_count, 1);

    let tag_count: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM video_tags WHERE video_id = $1")
        .bind(relocated.id)
        .fetch_one(&pool)
        .await
        .expect("tag count");
    assert_eq!(tag_count, 1);

    let deleted_at: Option<String> =
        sqlx::query_scalar("SELECT deleted_at::text FROM videos WHERE id = $1")
            .bind(deleted.id)
            .fetch_one(&pool)
            .await
            .expect("deleted_at");
    assert!(deleted_at.is_some());
}

fn write_old_file(path: impl AsRef<Path>, contents: &[u8]) {
    fs::write(path.as_ref(), contents).expect("write file");
    let old_time = FileTime::from_system_time(SystemTime::UNIX_EPOCH);
    filetime::set_file_mtime(path.as_ref(), old_time).expect("set mtime");
}
