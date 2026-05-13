use cine_api::{
    AITagCandidateInput, AITagCandidateListResponse, AITagCandidateStatus, AddVideoRequest,
    CleanupAnalysisRecord, DeleteVideoRequest, DiagnosticsSnapshot, IndexSubtitleRequest,
    PlaybackAttemptResponse, PreviewMode, PreviewSessionResponse, PublicSettings,
    RenameVideoRequest, ScanDirectoryListResponse, ScanDirectoryRequest, ScanDirectoryResponse,
    SettingsUpdateRequest, ShortFeedFeedbackRequest, ShortFeedInteractionRecord,
    ShortFeedVideoRecord, SubtitleIndexStateRecord, SubtitleSearchResponse, TagListResponse,
    TagMutationRequest, VideoFilterRequest, VideoListResponse, VideoMutationResponse,
    VideoTagMutationRequest,
};

#[test]
fn decodes_video_list_response_contract() {
    let payload = r##"
    {
      "videos": [
        {
          "id": 3,
          "name": "clip.mp4",
          "path": "/library/clip.mp4",
          "directory": "/library",
          "size": 100,
          "duration": 12.5,
          "resolution": "1920x1080",
          "width": 1920,
          "height": 1080,
          "is_stale": false,
          "play_count": 1,
          "random_play_count": 2,
          "last_played_at": null,
          "tags": [{"id": 9, "name": "keep", "color": "#ffffff"}],
          "created_at": null,
          "updated_at": null,
          "score": 4.0
        }
      ],
      "next_cursor": {"score": 4.0, "size": 100, "id": 3}
    }
    "##;

    let decoded: VideoListResponse = serde_json::from_str(payload).expect("valid response");

    assert_eq!(decoded.videos[0].id, 3);
    assert_eq!(decoded.videos[0].tags[0].name, "keep");
    assert_eq!(decoded.next_cursor.expect("cursor").id, 3);
}

#[test]
fn decodes_filter_request_with_tag_and_cursor_fields() {
    let payload = r#"
    {
      "keyword": "clip",
      "tag_ids": [1, 2],
      "min_size": 10,
      "max_size": 1000,
      "min_height": 720,
      "max_height": 2160,
      "cursor": {"score": 0.0, "size": 100, "id": 3},
      "limit": 50
    }
    "#;

    let decoded: VideoFilterRequest = serde_json::from_str(payload).expect("valid request");

    assert_eq!(decoded.keyword.as_deref(), Some("clip"));
    assert_eq!(decoded.tag_ids, vec![1, 2]);
    assert_eq!(decoded.cursor.expect("cursor").id, 3);
}

#[test]
fn decodes_video_file_operation_contracts() {
    let add: AddVideoRequest =
        serde_json::from_str(r#"{"path":"/library/new.mp4"}"#).expect("add request");
    assert_eq!(add.path, "/library/new.mp4");

    let rename: RenameVideoRequest =
        serde_json::from_str(r#"{"name":"better-name"}"#).expect("rename request");
    assert_eq!(rename.name, "better-name");

    let delete: DeleteVideoRequest =
        serde_json::from_str(r#"{"delete_file":true}"#).expect("delete request");
    assert!(delete.delete_file);

    let response: VideoMutationResponse = serde_json::from_str(
        r#"
        {
          "video": {
            "id": 3,
            "name": "clip.mp4",
            "path": "/library/clip.mp4",
            "directory": "/library",
            "size": 100,
            "duration": 0.0,
            "resolution": "",
            "width": 0,
            "height": 0,
            "is_stale": false,
            "play_count": 0,
            "random_play_count": 0,
            "last_played_at": null,
            "tags": [],
            "created_at": null,
            "updated_at": null,
            "score": 0.0
          },
          "ok": true,
          "reason_code": null,
          "user_message": null
        }
        "#,
    )
    .expect("mutation response");

    assert!(response.ok);
    assert_eq!(response.video.expect("video").name, "clip.mp4");
}

#[test]
fn decodes_scan_directory_contracts() {
    let request: ScanDirectoryRequest =
        serde_json::from_str(r#"{"path":"/library","extensions":"mp4,mkv"}"#)
            .expect("scan request");
    assert_eq!(request.path, "/library");
    assert_eq!(request.extensions.as_deref(), Some("mp4,mkv"));

    let response: ScanDirectoryResponse =
        serde_json::from_str(r#"{"files":[{"path":"/library/a.mp4","size":5}]}"#)
            .expect("scan response");
    assert_eq!(response.files[0].size, 5);
}

#[test]
fn decodes_preview_and_playback_contracts() {
    let preview: PreviewSessionResponse = serde_json::from_str(
        r#"
        {
          "video_id": 3,
          "mode": "inline",
          "display_name": "clip.mp4",
          "inline_source": {
            "locator_strategy": "asset_route",
            "locator_value": "/preview/media/3",
            "mime": "video/mp4"
          },
          "external_action": null,
          "reason_code": null,
          "reason_message": null
        }
        "#,
    )
    .expect("preview response");
    assert_eq!(preview.mode, PreviewMode::Inline);
    assert_eq!(preview.inline_source.expect("inline").mime, "video/mp4");

    let playback: PlaybackAttemptResponse = serde_json::from_str(
        r#"
        {
          "video": null,
          "dispatch_succeeded": false,
          "user_message": "播放失败",
          "reason_code": "file_missing",
          "reconcile_result": {
            "video_id": 3,
            "did_mark_stale": true,
            "did_relocate": false,
            "did_refresh_metadata": false,
            "needs_reload": true,
            "updated_video": null,
            "reason_code": "file_missing"
          }
        }
        "#,
    )
    .expect("playback response");
    assert!(!playback.dispatch_succeeded);
    assert!(playback
        .reconcile_result
        .is_some_and(|result| result.did_mark_stale));
}

#[test]
fn decodes_library_management_contracts() {
    let tags: TagListResponse =
        serde_json::from_str(r##"{"tags":[{"id":1,"name":"sport","color":"#0D9488"}]}"##)
            .expect("tag list");
    assert_eq!(tags.tags[0].name, "sport");

    let tag_request: TagMutationRequest =
        serde_json::from_str(r##"{"name":"sleep","color":"#ffffff"}"##).expect("tag request");
    assert_eq!(tag_request.color, "#ffffff");

    let video_tag_request: VideoTagMutationRequest =
        serde_json::from_str(r#"{"tag_id":10}"#).expect("video tag request");
    assert_eq!(video_tag_request.tag_id, 10);

    let dirs: ScanDirectoryListResponse =
        serde_json::from_str(r#"{"directories":[{"id":1,"path":"/library","alias":"Library"}]}"#)
            .expect("directory list");
    assert_eq!(dirs.directories[0].path, "/library");

    let settings: PublicSettings = serde_json::from_str(
        r#"
        {
          "video_extensions": ".mp4,.mkv",
          "play_weight": 2.0,
          "confirm_before_delete": true,
          "delete_original_file": false,
          "auto_scan_on_startup": true,
          "short_feed_max_duration_minutes": 5,
          "theme": "dark",
          "log_enabled": true,
          "bilingual_enabled": true,
          "bilingual_lang": "zh",
          "deepl_api_key_configured": true,
          "ai_tagging_base_url": "https://example.invalid/v1",
          "ai_tagging_api_key_configured": false,
          "ai_tagging_model": "vision-model",
          "ai_tagging_frame_count": 5,
          "ai_tagging_subtitle_char_limit": 4000,
          "ai_tagging_startup_batch_size": 10
        }
        "#,
    )
    .expect("public settings");
    assert!(settings.confirm_before_delete);
    assert!(settings.auto_scan_on_startup);
    assert!(settings.log_enabled);
    assert_eq!(settings.bilingual_lang, "zh");
    assert_eq!(settings.ai_tagging_model, "vision-model");
    assert!(settings.deepl_api_key_configured);

    let update: SettingsUpdateRequest = serde_json::from_str(
        r#"
        {
          "confirm_before_delete": true,
          "delete_original_file": false,
          "video_extensions": ".mp4",
          "play_weight": 2.0,
          "auto_scan_on_startup": true,
          "short_feed_max_duration_minutes": 5,
          "theme": "system",
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
        }
        "#,
    )
    .expect("settings update");
    assert!(update.confirm_before_delete);
}

#[test]
fn decodes_remaining_slice_contracts() {
    let index: IndexSubtitleRequest =
        serde_json::from_str(r#"{"path":"/library/movie.srt"}"#).expect("index request");
    assert_eq!(index.path, "/library/movie.srt");

    let index_state: SubtitleIndexStateRecord = serde_json::from_str(
        r#"{"video_id":3,"subtitle_path":"/library/movie.srt","subtitle_mod_time":10,"subtitle_size":100,"segment_count":2}"#,
    )
    .expect("index state");
    assert_eq!(index_state.segment_count, 2);

    let search: SubtitleSearchResponse = serde_json::from_str(
        r##"
        {
          "matches": [
            {
              "video": {
                "id": 3,
                "name": "clip.mp4",
                "path": "/library/clip.mp4",
                "directory": "/library",
                "size": 100,
                "duration": 0.0,
                "resolution": "",
                "width": 0,
                "height": 0,
                "is_stale": false,
                "play_count": 0,
                "random_play_count": 0,
                "last_played_at": null,
                "tags": [],
                "created_at": null,
                "updated_at": null,
                "score": 0.0
              },
              "segment": {
                "index": 1,
                "start_time_ms": 1000,
                "end_time_ms": 2000,
                "text": "hello world",
                "lines": ["hello world"]
              }
            }
          ]
        }
        "##,
    )
    .expect("subtitle search");
    assert_eq!(search.matches[0].segment.text, "hello world");

    let candidate_input: AITagCandidateInput = serde_json::from_str(
        r#"{"video_id":3,"suggested_name":"Night","normalized_name":"night","matched_tag_id":null,"confidence":"high","reasoning":"frame","source_summary":"evidence"}"#,
    )
    .expect("candidate input");
    assert_eq!(candidate_input.confidence, "high");

    let candidates: AITagCandidateListResponse = serde_json::from_str(
        r#"{"candidates":[{"id":1,"video_id":3,"suggested_name":"Night","normalized_name":"night","matched_tag_id":null,"confidence":"high","reasoning":"frame","source_summary":"evidence","status":"approved"}]}"#,
    )
    .expect("candidate list");
    assert_eq!(
        candidates.candidates[0].status,
        AITagCandidateStatus::Approved
    );

    let feedback: ShortFeedFeedbackRequest =
        serde_json::from_str(r#"{"liked":true,"favorited":false,"viewed":true}"#)
            .expect("feedback");
    assert_eq!(feedback.liked, Some(true));

    let interaction: ShortFeedInteractionRecord =
        serde_json::from_str(r#"{"video_id":3,"liked":true,"favorited":false,"view_count":1}"#)
            .expect("interaction");
    assert_eq!(interaction.view_count, 1);

    let feed_video: ShortFeedVideoRecord = serde_json::from_str(
        r##"{"id":3,"name":"clip.mp4","duration":30.0,"width":1920,"height":1080,"tags":[{"id":1,"name":"city","color":"#0D9488"}],"media_url":"/short-media/3","media_mime":"video/mp4","liked":true,"favorited":false,"reason_code":"","reason_message":""}"##,
    )
    .expect("feed video");
    assert_eq!(feed_video.tags[0].name, "city");
    assert_eq!(feed_video.media_url, "/short-media/3");
    assert!(feed_video.liked);

    let cleanup: CleanupAnalysisRecord = serde_json::from_str(
        r#"{"duplicate_groups":[{"original_id":1,"candidate_ids":[2],"reason":"same"}],"low_duration_ids":[1],"low_resolution_ids":[2]}"#,
    )
    .expect("cleanup");
    assert_eq!(cleanup.duplicate_groups[0].candidate_ids, vec![2]);

    let diagnostics: DiagnosticsSnapshot = serde_json::from_str(
        r#"
        {
          "video_count": 3,
          "tag_count": 1,
          "subtitle_segment_count": 2,
          "ai_candidate_count": 1,
          "short_feed_interaction_count": 1,
          "redacted_settings": {
            "confirm_before_delete": true,
            "delete_original_file": false,
            "video_extensions": ".mp4",
            "play_weight": 2.0,
            "auto_scan_on_startup": true,
            "short_feed_max_duration_minutes": 5,
            "theme": "system",
            "log_enabled": false,
            "bilingual_enabled": true,
            "bilingual_lang": "zh",
            "deepl_api_key_configured": true,
            "ai_tagging_base_url": "https://example.invalid/v1",
            "ai_tagging_api_key_configured": true,
            "ai_tagging_model": "vision-model",
            "ai_tagging_frame_count": 5,
            "ai_tagging_subtitle_char_limit": 4000,
            "ai_tagging_startup_batch_size": 10
          }
        }
        "#,
    )
    .expect("diagnostics");
    assert!(diagnostics.redacted_settings.deepl_api_key_configured);
}
