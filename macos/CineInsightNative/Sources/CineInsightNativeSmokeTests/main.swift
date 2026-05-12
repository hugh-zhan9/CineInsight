import Foundation
import CineInsightNativeCore

func assertEqual<T: Equatable>(_ actual: T, _ expected: T, _ message: String) {
    if actual != expected {
        fatalError("\(message): expected \(expected), got \(actual)")
    }
}

let configuration = DaemonLaunchConfiguration(
    executablePath: "/tmp/cine-daemon",
    port: 18088,
    token: "secret-token"
)
assertEqual(configuration.baseURL.absoluteString, "http://127.0.0.1:18088", "base URL")
assertEqual(configuration.authorizationHeader, "Bearer secret-token", "authorization header")

let manager = DaemonLifecycleManager()
assertEqual(manager.state, .stopped, "initial daemon state")
assertEqual(manager.message, "Daemon stopped", "initial daemon message")
if manager.health != nil {
    fatalError("initial health should be nil")
}

let client = NativeAPIClient(configuration: configuration)
assertEqual(
    client.absoluteURL(for: "/api/videos")?.absoluteString,
    "http://127.0.0.1:18088/api/videos",
    "client absolute URL"
)

let filterRequest = VideoFilterRequest(keyword: "clip", tagIds: [1], limit: 20)
let encodedFilter = try JSONEncoder.cineInsight.encode(filterRequest)
let filterObject = try JSONSerialization.jsonObject(with: encodedFilter) as? [String: Any]
assertEqual(filterObject?["keyword"] as? String, "clip", "filter keyword encoding")
assertEqual(filterObject?["tag_ids"] as? [Int], [1], "filter tag encoding")
assertEqual(filterObject?["limit"] as? Int, 20, "filter limit encoding")

let cleanupRequest = CleanupAnalyzeRequest(maxDurationSeconds: 30, minWidth: 800, minHeight: 480)
let encodedCleanup = try JSONEncoder.cineInsight.encode(cleanupRequest)
let cleanupObject = try JSONSerialization.jsonObject(with: encodedCleanup) as? [String: Any]
assertEqual(cleanupObject?["max_duration_seconds"] as? Double, 30, "cleanup duration encoding")
assertEqual(cleanupObject?["min_width"] as? Int, 800, "cleanup width encoding")
assertEqual(cleanupObject?["min_height"] as? Int, 480, "cleanup height encoding")

let addVideoRequest = AddVideoRequest(path: "/library/clip.mp4")
let encodedAddVideo = try JSONEncoder.cineInsight.encode(addVideoRequest)
let addVideoObject = try JSONSerialization.jsonObject(with: encodedAddVideo) as? [String: Any]
assertEqual(addVideoObject?["path"] as? String, "/library/clip.mp4", "add video path encoding")

let feedbackRequest = ShortFeedFeedbackRequest(liked: true, favorited: false, viewed: true)
let encodedFeedback = try JSONEncoder.cineInsight.encode(feedbackRequest)
let feedbackObject = try JSONSerialization.jsonObject(with: encodedFeedback) as? [String: Any]
assertEqual(feedbackObject?["liked"] as? Bool, true, "short feed liked encoding")
assertEqual(feedbackObject?["favorited"] as? Bool, false, "short feed favorited encoding")
assertEqual(feedbackObject?["viewed"] as? Bool, true, "short feed viewed encoding")

let data = """
{
  "service": "cine-daemon",
  "status": "ok",
  "version": "0.1.0",
  "app_compat_version": "0.1",
  "schema": {
    "status": "unchecked",
    "required_tables": ["videos", "tags"],
    "missing_tables": []
  },
  "database": {
    "configured": false,
    "connected": false
  }
}
""".data(using: .utf8)!

let health = try JSONDecoder.cineInsight.decode(DaemonHealth.self, from: data)
assertEqual(health.service, "cine-daemon", "health service")
assertEqual(health.schema.requiredTables, ["videos", "tags"], "required tables")
assertEqual(health.database.configured, false, "database configured")

let videoData = """
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
""".data(using: .utf8)!

let videoPage = try JSONDecoder.cineInsight.decode(VideoListResponse.self, from: videoData)
assertEqual(videoPage.videos[0].name, "clip.mp4", "video name")
assertEqual(videoPage.videos[0].tags[0].name, "keep", "video tag name")
assertEqual(videoPage.nextCursor?.id, 3, "video next cursor")

let scanData = """
{
  "files": [
    {"path": "/library/clip.mp4", "size": 100}
  ]
}
""".data(using: .utf8)!

let scanResponse = try JSONDecoder.cineInsight.decode(ScanDirectoryResponse.self, from: scanData)
assertEqual(scanResponse.files[0].path, "/library/clip.mp4", "scan path")
assertEqual(scanResponse.files[0].size, 100, "scan size")

let mutationData = """
{
  "video": {
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
    "tags": [],
    "created_at": null,
    "updated_at": null,
    "score": 4.0
  },
  "ok": true,
  "reason_code": null,
  "user_message": null
}
""".data(using: .utf8)!

let mutationResponse = try JSONDecoder.cineInsight.decode(VideoMutationResponse.self, from: mutationData)
assertEqual(mutationResponse.ok, true, "mutation ok")
assertEqual(mutationResponse.video?.name, "clip.mp4", "mutation video")

let previewData = """
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
""".data(using: .utf8)!

let preview = try JSONDecoder.cineInsight.decode(PreviewSessionResponse.self, from: previewData)
assertEqual(preview.mode, .inline, "preview mode")
assertEqual(preview.inlineSource?.mime, "video/mp4", "preview mime")

let playbackData = """
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
""".data(using: .utf8)!

let playback = try JSONDecoder.cineInsight.decode(PlaybackAttemptResponse.self, from: playbackData)
assertEqual(playback.dispatchSucceeded, false, "playback dispatch")
assertEqual(playback.reconcileResult?.didMarkStale, true, "playback stale reconcile")

let tagListData = """
{
  "tags": [
    {"id": 1, "name": "sport", "color": "#0D9488"}
  ]
}
""".data(using: .utf8)!

let tagList = try JSONDecoder.cineInsight.decode(TagListResponse.self, from: tagListData)
assertEqual(tagList.tags[0].name, "sport", "tag list name")

let directoryListData = """
{
  "directories": [
    {"id": 1, "path": "/library", "alias": "Library"}
  ]
}
""".data(using: .utf8)!

let directoryList = try JSONDecoder.cineInsight.decode(
    ScanDirectoryListResponse.self,
    from: directoryListData
)
assertEqual(directoryList.directories[0].alias, "Library", "scan directory alias")

let settingsData = """
{
  "video_extensions": ".mp4,.mkv",
  "play_weight": 2.0,
  "short_feed_max_duration_minutes": 5,
  "theme": "dark",
  "deepl_api_key_configured": true,
  "ai_tagging_api_key_configured": false,
  "ai_tagging_frame_count": 5,
  "ai_tagging_subtitle_char_limit": 4000,
  "ai_tagging_startup_batch_size": 10
}
""".data(using: .utf8)!

let settings = try JSONDecoder.cineInsight.decode(PublicSettings.self, from: settingsData)
assertEqual(settings.theme, "dark", "settings theme")
assertEqual(settings.deeplApiKeyConfigured, true, "settings deepl configured")

let subtitleSearchData = """
{
  "matches": [
    {
      "video": {
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
        "tags": [],
        "created_at": null,
        "updated_at": null,
        "score": 4.0
      },
      "segment": {
        "index": 1,
        "start_time_ms": 1000,
        "end_time_ms": 3000,
        "text": "hello world",
        "lines": ["hello world"]
      }
    }
  ]
}
""".data(using: .utf8)!

let subtitleSearch = try JSONDecoder.cineInsight.decode(
    SubtitleSearchResponse.self,
    from: subtitleSearchData
)
assertEqual(subtitleSearch.matches[0].segment.text, "hello world", "subtitle search text")

let aiCandidatesData = """
{
  "candidates": [
    {
      "id": 1,
      "video_id": 3,
      "suggested_name": "Night",
      "normalized_name": "night",
      "matched_tag_id": null,
      "confidence": "high",
      "reasoning": "frame",
      "source_summary": "evidence",
      "status": "approved"
    }
  ]
}
""".data(using: .utf8)!

let aiCandidates = try JSONDecoder.cineInsight.decode(
    AITagCandidateListResponse.self,
    from: aiCandidatesData
)
assertEqual(aiCandidates.candidates[0].status, .approved, "ai candidate status")

let shortFeedData = """
{
  "id": 3,
  "name": "clip.mp4",
  "duration": 30.0,
  "width": 1920,
  "height": 1080,
  "tags": [{"id": 9, "name": "keep", "color": "#ffffff"}]
}
""".data(using: .utf8)!

let shortFeed = try JSONDecoder.cineInsight.decode(ShortFeedVideoRecord.self, from: shortFeedData)
assertEqual(shortFeed.tags[0].name, "keep", "short feed tag")

let cleanupData = """
{
  "duplicate_groups": [
    {"original_id": 1, "candidate_ids": [2], "reason": "same"}
  ],
  "low_duration_ids": [1],
  "low_resolution_ids": [2]
}
""".data(using: .utf8)!

let cleanup = try JSONDecoder.cineInsight.decode(CleanupAnalysisRecord.self, from: cleanupData)
assertEqual(cleanup.duplicateGroups[0].candidateIds, [2], "cleanup candidates")

let diagnosticsData = """
{
  "video_count": 3,
  "tag_count": 1,
  "subtitle_segment_count": 2,
  "ai_candidate_count": 1,
  "short_feed_interaction_count": 1,
  "redacted_settings": {
    "video_extensions": ".mp4",
    "play_weight": 2.0,
    "short_feed_max_duration_minutes": 5,
    "theme": "system",
    "deepl_api_key_configured": true,
    "ai_tagging_api_key_configured": true,
    "ai_tagging_frame_count": 5,
    "ai_tagging_subtitle_char_limit": 4000,
    "ai_tagging_startup_batch_size": 10
  }
}
""".data(using: .utf8)!

let diagnostics = try JSONDecoder.cineInsight.decode(DiagnosticsSnapshot.self, from: diagnosticsData)
assertEqual(diagnostics.videoCount, 3, "diagnostics video count")
assertEqual(diagnostics.redactedSettings.aiTaggingApiKeyConfigured, true, "diagnostics redaction")

print("CineInsightNative smoke tests passed")
