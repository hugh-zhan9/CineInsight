use cine_db::{
    list_videos, native_test_database_url_from_value, random_candidate_with_sample,
    seed_video_query_fixture, VideoFilter, VideoQueryError,
};

#[test]
fn requires_explicit_native_test_database_url_for_video_fixture_tests() {
    let error =
        native_test_database_url_from_value(None).expect_err("missing test database should fail");

    assert_eq!(error, VideoQueryError::MissingNativeTestDatabaseUrl);
}

#[tokio::test]
async fn lists_active_videos_in_legacy_score_order_from_postgres_fixture() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };

    let pool = seed_video_query_fixture(&database_url)
        .await
        .expect("seed fixture");

    let page = list_videos(&pool, VideoFilter::default())
        .await
        .expect("list videos");

    let names: Vec<String> = page.videos.iter().map(|video| video.name.clone()).collect();
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
    assert!(names.iter().all(|name| name != "deleted.mp4"));
}

#[tokio::test]
async fn filters_keyword_tags_size_height_and_cursor_against_postgres_fixture() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };

    let pool = seed_video_query_fixture(&database_url)
        .await
        .expect("seed fixture");

    let page = list_videos(
        &pool,
        VideoFilter {
            keyword: Some("cat".to_string()),
            tag_ids: vec![10],
            min_size: Some(0),
            max_size: Some(200),
            min_height: Some(0),
            max_height: Some(1080),
            limit: Some(20),
            ..VideoFilter::default()
        },
    )
    .await
    .expect("filtered list");

    let names: Vec<String> = page.videos.iter().map(|video| video.name.clone()).collect();
    assert_eq!(names, vec!["cat_run.mp4"]);
}

#[tokio::test]
async fn random_candidate_uses_score_model_without_mutating_stats() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };

    let pool = seed_video_query_fixture(&database_url)
        .await
        .expect("seed fixture");

    let candidate = random_candidate_with_sample(&pool, 0.0)
        .await
        .expect("random candidate");
    let video = candidate.video.expect("video candidate");

    assert_eq!(video.name, "zero-large.mp4");

    let page = list_videos(
        &pool,
        VideoFilter {
            limit: Some(1),
            ..VideoFilter::default()
        },
    )
    .await
    .expect("list after candidate");
    assert_eq!(page.videos[0].play_count, 0);
    assert_eq!(page.videos[0].random_play_count, 0);
}

#[tokio::test]
async fn random_candidate_uses_legacy_weighted_selection_not_first_sorted_row() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };

    let pool = seed_video_query_fixture(&database_url)
        .await
        .expect("seed fixture");

    let candidate = random_candidate_with_sample(&pool, 0.90)
        .await
        .expect("random candidate");
    let video = candidate.video.expect("video candidate");

    assert_eq!(video.name, "cat_sleep.mp4");
}

#[tokio::test]
async fn clamps_play_weight_before_sql_ordering_and_cursor_scores() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };

    let pool = seed_video_query_fixture(&database_url)
        .await
        .expect("seed fixture");
    sqlx::query("UPDATE settings SET play_weight = 0")
        .execute(&pool)
        .await
        .expect("update play weight");

    let page = list_videos(
        &pool,
        VideoFilter {
            limit: Some(3),
            ..VideoFilter::default()
        },
    )
    .await
    .expect("list videos");

    let names: Vec<String> = page.videos.iter().map(|video| video.name.clone()).collect();
    assert_eq!(
        names,
        vec!["zero-large.mp4", "zero-small.mp4", "two-large.mp4",]
    );
    assert_eq!(page.videos[2].score, 0.1);
}

#[tokio::test]
async fn treats_non_positive_size_and_height_filters_as_unset_like_legacy() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };

    let pool = seed_video_query_fixture(&database_url)
        .await
        .expect("seed fixture");

    let page = list_videos(
        &pool,
        VideoFilter {
            max_size: Some(0),
            max_height: Some(0),
            limit: Some(20),
            ..VideoFilter::default()
        },
    )
    .await
    .expect("list videos");

    let names: Vec<String> = page.videos.iter().map(|video| video.name.clone()).collect();
    assert_eq!(names.len(), 5);
    assert!(names.iter().all(|name| name != "deleted.mp4"));
}
