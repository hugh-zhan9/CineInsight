use cine_domain::{
    apply_video_cursor, clamp_play_weight, sort_video_summaries, video_score, VideoCursor,
    VideoSummary,
};

fn summary(id: i64, size: i64, play_count: i32, random_play_count: i32) -> VideoSummary {
    VideoSummary {
        id,
        name: format!("{id}.mp4"),
        path: format!("/tmp/{id}.mp4"),
        directory: "/tmp".to_string(),
        size,
        duration: 0.0,
        resolution: String::new(),
        width: 0,
        height: 0,
        is_stale: false,
        play_count,
        random_play_count,
        last_played_at: None,
        tags: Vec::new(),
        created_at: None,
        updated_at: None,
        score: video_score(play_count, random_play_count, 2.0),
    }
}

#[test]
fn clamps_play_weight_like_legacy_service() {
    assert_eq!(clamp_play_weight(0.0), 0.1);
    assert_eq!(clamp_play_weight(0.05), 0.1);
    assert_eq!(clamp_play_weight(2.0), 2.0);
}

#[test]
fn sorts_by_score_ascending_size_descending_id_descending() {
    let mut videos = vec![
        summary(1, 10, 0, 0),
        summary(2, 100, 1, 0),
        summary(3, 100, 0, 0),
        summary(4, 100, 0, 0),
    ];

    sort_video_summaries(&mut videos);

    let ids: Vec<i64> = videos.iter().map(|video| video.id).collect();
    assert_eq!(ids, vec![4, 3, 1, 2]);
}

#[test]
fn applies_legacy_cursor_condition_after_seen_tuple() {
    let videos = vec![
        summary(4, 100, 0, 0),
        summary(3, 100, 0, 0),
        summary(1, 10, 0, 0),
        summary(2, 100, 1, 0),
    ];

    let page = apply_video_cursor(
        &videos,
        Some(VideoCursor {
            score: 0.0,
            size: 100,
            id: 3,
        }),
    );

    let ids: Vec<i64> = page.iter().map(|video| video.id).collect();
    assert_eq!(ids, vec![1, 2]);
}
