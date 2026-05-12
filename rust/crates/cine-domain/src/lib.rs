//! Shared domain contracts for the native CineInsight replacement.

use serde::{Deserialize, Serialize};

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct VideoTagSummary {
    pub id: i64,
    pub name: String,
    pub color: String,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct VideoSummary {
    pub id: i64,
    pub name: String,
    pub path: String,
    pub directory: String,
    pub size: i64,
    pub duration: f64,
    pub resolution: String,
    pub width: i32,
    pub height: i32,
    pub is_stale: bool,
    pub play_count: i32,
    pub random_play_count: i32,
    pub last_played_at: Option<String>,
    pub tags: Vec<VideoTagSummary>,
    pub created_at: Option<String>,
    pub updated_at: Option<String>,
    pub score: f64,
}

#[derive(Clone, Copy, Debug, Deserialize, PartialEq, Serialize)]
pub struct VideoCursor {
    pub score: f64,
    pub size: i64,
    pub id: i64,
}

pub fn clamp_play_weight(play_weight: f64) -> f64 {
    if play_weight < 0.1 {
        0.1
    } else {
        play_weight
    }
}

pub fn video_score(play_count: i32, random_play_count: i32, play_weight: f64) -> f64 {
    f64::from(play_count) * clamp_play_weight(play_weight) + f64::from(random_play_count)
}

pub fn sort_video_summaries(videos: &mut [VideoSummary]) {
    videos.sort_by(|left, right| {
        left.score
            .total_cmp(&right.score)
            .then_with(|| right.size.cmp(&left.size))
            .then_with(|| right.id.cmp(&left.id))
    });
}

pub fn is_after_cursor(video: &VideoSummary, cursor: VideoCursor) -> bool {
    video.score > cursor.score
        || (video.score == cursor.score && video.size < cursor.size)
        || (video.score == cursor.score && video.size == cursor.size && video.id < cursor.id)
}

pub fn apply_video_cursor(
    videos: &[VideoSummary],
    cursor: Option<VideoCursor>,
) -> Vec<VideoSummary> {
    match cursor {
        Some(cursor) => videos
            .iter()
            .filter(|video| is_after_cursor(video, cursor))
            .cloned()
            .collect(),
        None => videos.to_vec(),
    }
}
