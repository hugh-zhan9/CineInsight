use cine_db::{
    add_scan_directory, assign_tag_to_video, create_tag, delete_scan_directory, delete_tag,
    get_public_settings, list_scan_directories, list_tags, remove_tag_from_video,
    seed_library_management_fixture, update_scan_directory, update_settings, update_tag,
    PublicSettings, SettingsUpdate, TagMutationError,
};

#[tokio::test]
async fn tags_use_default_palette_restore_soft_deleted_names_and_clean_rename_conflicts() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let pool = seed_library_management_fixture(&database_url)
        .await
        .expect("seed fixture");

    let first = create_tag(&pool, "sport", "")
        .await
        .expect("create first tag");
    assert_eq!(first.color, "#0D9488");

    let duplicate = create_tag(&pool, "sport", "")
        .await
        .expect_err("duplicate active tag");
    assert_eq!(duplicate, TagMutationError::TagExists);

    delete_tag(&pool, first.id).await.expect("soft delete tag");
    let restored = create_tag(&pool, "sport", "#123456")
        .await
        .expect("restore deleted tag");
    assert_eq!(restored.id, first.id);
    assert_eq!(restored.color, "#123456");

    let rename_target = create_tag(&pool, "rename-target", "")
        .await
        .expect("create rename target");
    sqlx::query("INSERT INTO tags(name, color, deleted_at) VALUES ('archived', '#999999', now())")
        .execute(&pool)
        .await
        .expect("insert soft-deleted conflict");

    update_tag(&pool, rename_target.id, "archived", "#abcdef")
        .await
        .expect("rename over soft-deleted conflict");
    let tags = list_tags(&pool).await.expect("list tags");
    assert!(tags
        .iter()
        .any(|tag| tag.name == "archived" && tag.id == rename_target.id));

    let active_conflict = update_tag(&pool, rename_target.id, "sport", "#ffffff")
        .await
        .expect_err("active duplicate rename should fail");
    assert_eq!(active_conflict, TagMutationError::TagExists);
}

#[tokio::test]
async fn settings_update_preserves_defaults_and_public_read_redacts_secrets() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let pool = seed_library_management_fixture(&database_url)
        .await
        .expect("seed fixture");

    update_settings(
        &pool,
        SettingsUpdate {
            confirm_before_delete: true,
            delete_original_file: true,
            video_extensions: ".mp4,.mkv".to_string(),
            play_weight: 0.0,
            auto_scan_on_startup: true,
            short_feed_max_duration_minutes: 0,
            theme: "dark".to_string(),
            log_enabled: true,
            bilingual_enabled: true,
            bilingual_lang: "en".to_string(),
            deepl_api_key: "deepl-secret".to_string(),
            ai_tagging_base_url: "https://example.invalid/v1".to_string(),
            ai_tagging_api_key: "ai-secret".to_string(),
            ai_tagging_model: "vision-model".to_string(),
            ai_tagging_frame_count: 0,
            ai_tagging_subtitle_char_limit: 0,
            ai_tagging_startup_batch_size: 0,
            ..SettingsUpdate::default()
        },
    )
    .await
    .expect("update settings");

    let public = get_public_settings(&pool).await.expect("public settings");
    assert_eq!(
        public,
        PublicSettings {
            confirm_before_delete: true,
            delete_original_file: true,
            video_extensions: ".mp4,.mkv".to_string(),
            play_weight: 0.0,
            auto_scan_on_startup: true,
            short_feed_max_duration_minutes: 5,
            theme: "dark".to_string(),
            log_enabled: true,
            bilingual_enabled: true,
            bilingual_lang: "en".to_string(),
            deepl_api_key_configured: true,
            ai_tagging_base_url: "https://example.invalid/v1".to_string(),
            ai_tagging_api_key_configured: true,
            ai_tagging_model: "vision-model".to_string(),
            ai_tagging_frame_count: 5,
            ai_tagging_subtitle_char_limit: 4000,
            ai_tagging_startup_batch_size: 10,
        }
    );

    update_settings(
        &pool,
        SettingsUpdate {
            video_extensions: ".mov".to_string(),
            play_weight: 1.5,
            short_feed_max_duration_minutes: 12,
            theme: "system".to_string(),
            deepl_api_key: "".to_string(),
            ai_tagging_api_key: "".to_string(),
            ..SettingsUpdate::default()
        },
    )
    .await
    .expect("update settings with empty secrets");

    let public = get_public_settings(&pool).await.expect("public settings");
    assert!(public.deepl_api_key_configured);
    assert!(public.ai_tagging_api_key_configured);
}

#[tokio::test]
async fn scan_directory_crud_orders_active_directories_and_soft_deletes() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let pool = seed_library_management_fixture(&database_url)
        .await
        .expect("seed fixture");

    let first = add_scan_directory(&pool, "/library/a", "A")
        .await
        .expect("add first directory");
    let second = add_scan_directory(&pool, "/library/b", "B")
        .await
        .expect("add second directory");

    update_scan_directory(&pool, first.id, "/library/a2", "A2")
        .await
        .expect("update directory");
    delete_scan_directory(&pool, second.id)
        .await
        .expect("delete directory");

    let dirs = list_scan_directories(&pool)
        .await
        .expect("list directories");
    assert_eq!(dirs.len(), 1);
    assert_eq!(dirs[0].id, first.id);
    assert_eq!(dirs[0].path, "/library/a2");
    assert_eq!(dirs[0].alias, "A2");
}

#[tokio::test]
async fn video_tag_assignment_is_idempotent_and_removable() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let pool = seed_library_management_fixture(&database_url)
        .await
        .expect("seed fixture");

    let tag = create_tag(&pool, "assignable", "")
        .await
        .expect("create tag");
    assign_tag_to_video(&pool, 1, tag.id)
        .await
        .expect("assign tag");
    assign_tag_to_video(&pool, 1, tag.id)
        .await
        .expect("assign tag idempotently");

    let count: i64 =
        sqlx::query_scalar("SELECT COUNT(*) FROM video_tags WHERE video_id = 1 AND tag_id = $1")
            .bind(tag.id)
            .fetch_one(&pool)
            .await
            .expect("count video tags");
    assert_eq!(count, 1);

    remove_tag_from_video(&pool, 1, tag.id)
        .await
        .expect("remove tag");
    let count: i64 =
        sqlx::query_scalar("SELECT COUNT(*) FROM video_tags WHERE video_id = 1 AND tag_id = $1")
            .bind(tag.id)
            .fetch_one(&pool)
            .await
            .expect("count removed video tags");
    assert_eq!(count, 0);
}
