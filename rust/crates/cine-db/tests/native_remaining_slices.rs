use cine_db::{
    approve_ai_tag_candidate, create_ai_tag_candidate, diagnostics_snapshot, get_subtitle_segments,
    index_subtitle_file, list_ai_tag_candidates, next_short_feed_video, record_short_feed_feedback,
    reject_ai_tag_candidate, search_subtitle_matches, seed_remaining_slices_fixture,
    start_cleanup_analysis, AITagCandidateInput, AITagCandidateStatus, ShortFeedFeedback,
};
use std::{fs, path::Path};
use tempfile::TempDir;

#[tokio::test]
async fn subtitles_index_segments_and_search_first_hit_per_video() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let srt_path = root.path().join("movie.srt");
    fs::write(
        &srt_path,
        "1\n00:00:01,000 --> 00:00:03,500\nhello world\n\n2\n00:00:04,000 --> 00:00:05,000\nworld again\n",
    )
    .expect("write srt");
    let pool = seed_remaining_slices_fixture(&database_url, root.path())
        .await
        .expect("seed fixture");

    let indexed = index_subtitle_file(&pool, 1, &srt_path)
        .await
        .expect("index subtitle");
    assert_eq!(indexed.segment_count, 2);

    let segments = get_subtitle_segments(&pool, 1).await.expect("get segments");
    assert_eq!(segments[0].start_time_ms, 1000);
    assert_eq!(segments[0].end_time_ms, 3500);

    let matches = search_subtitle_matches(&pool, "WORLD", 10)
        .await
        .expect("search subtitles");
    assert_eq!(matches.len(), 1);
    assert_eq!(matches[0].video.id, 1);
    assert_eq!(matches[0].segment.text, "hello world");
}

#[tokio::test]
async fn ai_tagging_candidates_can_be_approved_rejected_and_summarized() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    let pool = seed_remaining_slices_fixture(&database_url, root.path())
        .await
        .expect("seed fixture");

    let candidate = create_ai_tag_candidate(
        &pool,
        AITagCandidateInput {
            video_id: 1,
            suggested_name: "Night City".to_string(),
            normalized_name: "night city".to_string(),
            matched_tag_id: None,
            confidence: "high".to_string(),
            reasoning: "dark skyline".to_string(),
            source_summary: "frames and subtitles".to_string(),
        },
    )
    .await
    .expect("create candidate");
    let approved = approve_ai_tag_candidate(&pool, candidate.id)
        .await
        .expect("approve candidate");
    assert_eq!(approved.status, AITagCandidateStatus::Approved);

    let second = create_ai_tag_candidate(
        &pool,
        AITagCandidateInput {
            video_id: 2,
            suggested_name: "Noise".to_string(),
            normalized_name: "noise".to_string(),
            matched_tag_id: None,
            confidence: "low".to_string(),
            reasoning: "weak evidence".to_string(),
            source_summary: "subtitle only".to_string(),
        },
    )
    .await
    .expect("create second candidate");
    let rejected = reject_ai_tag_candidate(&pool, second.id)
        .await
        .expect("reject candidate");
    assert_eq!(rejected.status, AITagCandidateStatus::Rejected);

    let candidates = list_ai_tag_candidates(&pool, None)
        .await
        .expect("list candidates");
    assert_eq!(candidates.len(), 2);
    assert!(candidates
        .iter()
        .any(|item| item.status == AITagCandidateStatus::Approved));
    assert!(candidates
        .iter()
        .any(|item| item.status == AITagCandidateStatus::Rejected));
}

#[tokio::test]
async fn short_feed_feedback_cleanup_and_diagnostics_are_postgres_backed() {
    let Some(database_url) = std::env::var("NATIVE_TEST_DATABASE_URL").ok() else {
        eprintln!("skipping postgres fixture test: NATIVE_TEST_DATABASE_URL is not set");
        return;
    };
    let root = TempDir::new().expect("temp root");
    fs::write(root.path().join("short-a.mp4"), b"same-content").expect("write a");
    fs::write(root.path().join("short-b.mp4"), b"same-content").expect("write b");
    fs::write(root.path().join("long.mp4"), b"long").expect("write long");
    let pool = seed_remaining_slices_fixture(&database_url, root.path())
        .await
        .expect("seed fixture");

    let next = next_short_feed_video(&pool, &[])
        .await
        .expect("next short feed video");
    assert_eq!(next.id, 1);

    let feedback = record_short_feed_feedback(
        &pool,
        1,
        ShortFeedFeedback {
            liked: Some(true),
            favorited: Some(true),
            viewed: true,
        },
    )
    .await
    .expect("record feedback");
    assert!(feedback.liked);
    assert!(feedback.favorited);
    assert_eq!(feedback.view_count, 1);

    let cleanup = start_cleanup_analysis(&pool, 300.0, 640, 360)
        .await
        .expect("cleanup analysis");
    assert_eq!(cleanup.duplicate_groups.len(), 1);
    assert_eq!(cleanup.duplicate_groups[0].candidate_ids, vec![2]);
    assert_eq!(cleanup.low_resolution_ids, vec![2]);

    let diagnostics = diagnostics_snapshot(&pool)
        .await
        .expect("diagnostics snapshot");
    assert_eq!(diagnostics.video_count, 3);
    assert!(diagnostics.redacted_settings.deepl_api_key_configured);
    assert!(diagnostics.redacted_settings.ai_tagging_api_key_configured);
    assert!(!format!("{diagnostics:?}").contains("secret"));

    assert!(Path::new(&root.path().join("short-a.mp4")).exists());
}
