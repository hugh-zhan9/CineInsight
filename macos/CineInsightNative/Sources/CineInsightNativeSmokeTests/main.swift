import Foundation
import CineInsightNativeCore

func assertEqual<T: Equatable>(_ actual: T, _ expected: T, _ message: String) {
    if actual != expected {
        fatalError("\(message): expected \(expected), got \(actual)")
    }
}

final class SmokeURLProtocol: URLProtocol {
    nonisolated(unsafe) static var handler: ((URLRequest) throws -> (HTTPURLResponse, Data))?

    override class func canInit(with request: URLRequest) -> Bool {
        true
    }

    override class func canonicalRequest(for request: URLRequest) -> URLRequest {
        request
    }

    override func startLoading() {
        guard let handler = Self.handler else {
            client?.urlProtocol(self, didFailWithError: URLError(.badServerResponse))
            return
        }
        do {
            let (response, data) = try handler(request)
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }

    override func stopLoading() {}
}

let configuration = DaemonLaunchConfiguration(
    executablePath: "/tmp/cine-daemon",
    port: 18088,
    token: "secret-token"
)
assertEqual(configuration.baseURL.absoluteString, "http://127.0.0.1:18088", "base URL")
assertEqual(configuration.authorizationHeader, "Bearer secret-token", "authorization header")

let bundleResourceURL = FileManager.default.temporaryDirectory
    .appendingPathComponent("cineinsight-smoke-\(UUID().uuidString)")
try FileManager.default.createDirectory(
    at: bundleResourceURL.appendingPathComponent("bin"),
    withIntermediateDirectories: true
)
try FileManager.default.createDirectory(
    at: bundleResourceURL.appendingPathComponent("short-feed"),
    withIntermediateDirectories: true
)
let bundledDaemonURL = bundleResourceURL.appendingPathComponent("bin/cine-daemon")
FileManager.default.createFile(atPath: bundledDaemonURL.path, contents: Data())
try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: bundledDaemonURL.path)
let bundledConfiguration = DaemonLaunchConfiguration.defaultConfiguration(
    environment: [
        "CINE_DAEMON_PORT": "19090",
        "CINE_DAEMON_TOKEN": "bundle-token"
    ],
    bundleResourceURL: bundleResourceURL
)
assertEqual(bundledConfiguration.executablePath, bundledDaemonURL.path, "bundled daemon path")
assertEqual(bundledConfiguration.shortFeedAssetsPath, bundleResourceURL.appendingPathComponent("short-feed").path, "bundled short feed assets")
assertEqual(bundledConfiguration.port, 19090, "bundled daemon port")

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

let largeSizeFilter = VideoSizeFilter.large.requestBounds
assertEqual(largeSizeFilter.minSize, 1_073_741_824, "large size filter min")
assertEqual(largeSizeFilter.maxSize, nil, "large size filter max")
let hdResolutionFilter = VideoResolutionFilter.hd.requestBounds
assertEqual(hdResolutionFilter.minHeight, 720, "hd resolution filter min")
assertEqual(hdResolutionFilter.maxHeight, 1079, "hd resolution filter max")

let cleanupRequest = CleanupAnalyzeRequest(maxDurationSeconds: 30, minWidth: 800, minHeight: 480)
let encodedCleanup = try JSONEncoder.cineInsight.encode(cleanupRequest)
let cleanupObject = try JSONSerialization.jsonObject(with: encodedCleanup) as? [String: Any]
assertEqual(cleanupObject?["max_duration_seconds"] as? Double, 30, "cleanup duration encoding")
assertEqual(cleanupObject?["min_width"] as? Int, 800, "cleanup width encoding")
assertEqual(cleanupObject?["min_height"] as? Int, 480, "cleanup height encoding")

let settingsUpdate = SettingsUpdateRequest(
    videoExtensions: ".mp4,.mkv",
    playWeight: 2.5,
    shortFeedMaxDurationMinutes: 7,
    theme: "dark",
    aiTaggingFrameCount: 4,
    aiTaggingSubtitleCharLimit: 3000,
    aiTaggingStartupBatchSize: 12
)
let encodedSettingsUpdate = try JSONEncoder.cineInsight.encode(settingsUpdate)
let settingsUpdateObject = try JSONSerialization.jsonObject(with: encodedSettingsUpdate) as? [String: Any]
assertEqual(settingsUpdateObject?["video_extensions"] as? String, ".mp4,.mkv", "settings update extensions")
assertEqual(settingsUpdateObject?["play_weight"] as? Double, 2.5, "settings update play weight")
assertEqual(settingsUpdateObject?["short_feed_max_duration_minutes"] as? Int, 7, "settings update short feed")
assertEqual(settingsUpdateObject?["ai_tagging_frame_count"] as? Int, 4, "settings update ai frames")
assertEqual(settingsUpdateObject?["ai_tagging_base_url"] as? String, "", "settings update ai base URL")

let addVideoRequest = AddVideoRequest(path: "/library/clip.mp4")
let encodedAddVideo = try JSONEncoder.cineInsight.encode(addVideoRequest)
let addVideoObject = try JSONSerialization.jsonObject(with: encodedAddVideo) as? [String: Any]
assertEqual(addVideoObject?["path"] as? String, "/library/clip.mp4", "add video path encoding")

let relocateRequest = RelocateVideoRequest(path: "/library/moved.mp4")
let encodedRelocate = try JSONEncoder.cineInsight.encode(relocateRequest)
let relocateObject = try JSONSerialization.jsonObject(with: encodedRelocate) as? [String: Any]
assertEqual(relocateObject?["path"] as? String, "/library/moved.mp4", "relocate path encoding")

let batchDeleteRequest = BatchVideoRequest(videoIds: [3, 4], deleteFile: false)
let encodedBatchDelete = try JSONEncoder.cineInsight.encode(batchDeleteRequest)
let batchDeleteObject = try JSONSerialization.jsonObject(with: encodedBatchDelete) as? [String: Any]
assertEqual(batchDeleteObject?["video_ids"] as? [Int], [3, 4], "batch video ids encoding")
assertEqual(batchDeleteObject?["delete_file"] as? Bool, false, "batch delete flag encoding")

let batchTagRequest = BatchVideoTagRequest(videoIds: [3], tagId: 9)
let encodedBatchTag = try JSONEncoder.cineInsight.encode(batchTagRequest)
let batchTagObject = try JSONSerialization.jsonObject(with: encodedBatchTag) as? [String: Any]
assertEqual(batchTagObject?["video_ids"] as? [Int], [3], "batch tag video ids encoding")
assertEqual(batchTagObject?["tag_id"] as? Int, 9, "batch tag id encoding")

assertEqual(
    client.absoluteURL(for: "/api/videos/by-directory?path=%2Flibrary")?.absoluteString,
    "http://127.0.0.1:18088/api/videos/by-directory?path=%2Flibrary",
    "client by-directory URL"
)

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

let subtitlePrepareRequest = SubtitlePrepareRequest(engine: .whisperx)
let encodedSubtitlePrepare = try JSONEncoder.cineInsight.encode(subtitlePrepareRequest)
let subtitlePrepareObject = try JSONSerialization.jsonObject(with: encodedSubtitlePrepare) as? [String: Any]
assertEqual(subtitlePrepareObject?["engine"] as? String, "whisperx", "subtitle prepare engine")

let subtitleGenerateRequest = SubtitleGenerateRequest(videoId: 3, engine: .whisperx, sourceLang: "auto")
let encodedSubtitleGenerate = try JSONEncoder.cineInsight.encode(subtitleGenerateRequest)
let subtitleGenerateObject = try JSONSerialization.jsonObject(with: encodedSubtitleGenerate) as? [String: Any]
assertEqual(subtitleGenerateObject?["video_id"] as? Int, 3, "subtitle generate video")
assertEqual(subtitleGenerateObject?["engine"] as? String, "whisperx", "subtitle generate engine")

let subtitleEngineData = """
[
  {
    "engine": "whisperx",
    "display_name": "WhisperX",
    "supported": true,
    "available": false,
    "needs_prepare": true,
    "prepare_mode": "managed",
    "reason_code": "missing_runtime",
    "source_lang_mode": "shared",
    "reason_message": "Runtime missing",
    "prepare_hint": "Install runtime"
  }
]
""".data(using: .utf8)!

let subtitleEngines = try JSONDecoder.cineInsight.decode([SubtitleEngineStatus].self, from: subtitleEngineData)
assertEqual(subtitleEngines[0].engine, .whisperx, "subtitle engine decode")
assertEqual(subtitleEngines[0].needsPrepare, true, "subtitle engine needs prepare")

let subtitleResultData = """
{
  "status": "success",
  "video_id": 3,
  "path": "/library/clip.srt",
  "message": null,
  "validation_code": null,
  "force_eligible": false,
  "engine": "whisperx",
  "source_lang": "auto"
}
""".data(using: .utf8)!

let subtitleResult = try JSONDecoder.cineInsight.decode(SubtitleGenerateResult.self, from: subtitleResultData)
assertEqual(subtitleResult.status, "success", "subtitle result status")
assertEqual(subtitleResult.engine, .whisperx, "subtitle result engine")

let subtitleStatusData = """
{
  "running": true,
  "completed": false,
  "cancelled": false,
  "progress": {
    "action": "generate",
    "engine": "whisperx",
    "phase": "transcribing",
    "percent": 20,
    "message": "Transcribing",
    "cancellable": true
  },
  "result": null,
  "error": null
}
""".data(using: .utf8)!

let subtitleStatus = try JSONDecoder.cineInsight.decode(SubtitleJobStatus.self, from: subtitleStatusData)
assertEqual(subtitleStatus.running, true, "subtitle status running")
assertEqual(subtitleStatus.progress.phase, "transcribing", "subtitle progress phase")
assertEqual(subtitleStatus.progress.engine, .whisperx, "subtitle progress engine")

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
  "confirm_before_delete": true,
  "delete_original_file": false,
  "video_extensions": ".mp4,.mkv",
  "play_weight": 2.0,
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
""".data(using: .utf8)!

let settings = try JSONDecoder.cineInsight.decode(PublicSettings.self, from: settingsData)
assertEqual(settings.theme, "dark", "settings theme")
assertEqual(settings.deeplApiKeyConfigured, true, "settings deepl configured")
assertEqual(settings.confirmBeforeDelete, true, "settings confirm delete")
assertEqual(settings.aiTaggingModel, "vision-model", "settings ai model")

let settingsOnlySessionConfiguration = URLSessionConfiguration.ephemeral
settingsOnlySessionConfiguration.protocolClasses = [SmokeURLProtocol.self]
SmokeURLProtocol.handler = { request in
    let path = request.url?.path ?? ""
    let responseData: Data
    let status: Int
    if path == "/api/settings" {
        responseData = settingsData
        status = 200
    } else if path == "/api/subtitles/engines" {
        responseData = "[]".data(using: .utf8)!
        status = 200
    } else if path == "/api/subtitles/status" {
        responseData = """
        {"running":false,"video_id":null,"engine":null,"source_lang":null,"started_at_ms":null,"completed_at_ms":null,"progress":{"stage":"idle","message":"","current":0,"total":0,"path":null,"cancellable":false},"result":null,"error":null}
        """.data(using: .utf8)!
        status = 200
    } else if path == "/api/short-feed/status" {
        responseData = """
        {"running":false,"url":"","lan_urls":[],"bind_address":"","port":0,"fallback_used":false,"startup_error":"","allowed_access":"local-network"}
        """.data(using: .utf8)!
        status = 200
    } else if path == "/api/cleanup/status" {
        responseData = """
        {"running":false,"completed":false,"progress":{"stage":"idle","message":"","current":0,"total":0,"path":null,"cancellable":false},"analysis":null,"error":null}
        """.data(using: .utf8)!
        status = 200
    } else {
        responseData = Data()
        status = 503
    }
    let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: nil)!
    return (response, responseData)
}
let settingsOnlyClient = NativeAPIClient(
    configuration: DaemonLaunchConfiguration(executablePath: "/tmp/cine-daemon", port: 18089, token: "secret-token"),
    session: URLSession(configuration: settingsOnlySessionConfiguration)
)
let settingsOnlyViewModel = LibraryViewModel(client: settingsOnlyClient)
await settingsOnlyViewModel.loadAll()
assertEqual(settingsOnlyViewModel.settings?.theme, "dark", "loadAll keeps settings when library routes fail")

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
let groupedCandidates = aiCandidates.candidates.groupedByVideo()
assertEqual(groupedCandidates.count, 1, "ai candidates grouped count")
assertEqual(groupedCandidates[0].videoId, 3, "ai candidates grouped video")
assertEqual(groupedCandidates[0].pendingCount, 0, "ai candidates grouped pending count")

let aiSummaryData = """
{
  "config_available": true,
  "pending": 1,
  "processing": 2,
  "completed": 3,
  "skipped": 4,
  "failed": 5
}
""".data(using: .utf8)!

let aiSummary = try JSONDecoder.cineInsight.decode(AITaggingStatusSummary.self, from: aiSummaryData)
assertEqual(aiSummary.configAvailable, true, "ai summary config")
assertEqual(aiSummary.failed, 5, "ai summary failed")

let rejectByVideoData = """
{"rejected": 2}
""".data(using: .utf8)!
let rejectByVideo = try JSONDecoder.cineInsight.decode(
    RejectAITagCandidatesByVideoResponse.self,
    from: rejectByVideoData
)
assertEqual(rejectByVideo.rejected, 2, "reject pending by video")

let shortFeedData = """
{
  "id": 3,
  "name": "clip.mp4",
  "duration": 30.0,
  "width": 1920,
  "height": 1080,
  "tags": [{"id": 9, "name": "keep", "color": "#ffffff"}],
  "media_url": "/short-media/3",
  "media_mime": "video/mp4",
  "liked": true,
  "favorited": false,
  "reason_code": "",
  "reason_message": ""
}
""".data(using: .utf8)!

let shortFeed = try JSONDecoder.cineInsight.decode(ShortFeedVideoRecord.self, from: shortFeedData)
assertEqual(shortFeed.tags[0].name, "keep", "short feed tag")
assertEqual(shortFeed.mediaUrl, "/short-media/3", "short feed media url")
assertEqual(shortFeed.liked, true, "short feed liked")

let shortFeedStatusData = """
{
  "running": true,
  "bind_address": "127.0.0.1",
  "port": 18088,
  "url": "http://127.0.0.1:18088/short",
  "lan_urls": [],
  "startup_error": "",
  "fallback_used": false,
  "allowed_access": "loopback/private-lan/link-local only, no login"
}
""".data(using: .utf8)!

let shortFeedStatus = try JSONDecoder.cineInsight.decode(ShortFeedServerStatus.self, from: shortFeedStatusData)
assertEqual(shortFeedStatus.running, true, "short feed status running")
assertEqual(shortFeedStatus.allowedAccess, "loopback/private-lan/link-local only, no login", "short feed allowed access")

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
assertEqual(cleanup.allCandidateIds, [1, 2], "cleanup all candidate ids")

let cleanupStatusData = """
{
  "running": false,
  "completed": true,
  "error": "",
  "progress": {
    "stage": "done",
    "message": "Cleanup analysis completed",
    "current": 1,
    "total": 1,
    "path": ""
  },
  "analysis": {
    "duplicate_groups": [],
    "low_duration_ids": [],
    "low_resolution_ids": []
  },
  "started_at": "1",
  "updated_at": "2"
}
""".data(using: .utf8)!

let cleanupStatus = try JSONDecoder.cineInsight.decode(CleanupStatus.self, from: cleanupStatusData)
assertEqual(cleanupStatus.completed, true, "cleanup status completed")
assertEqual(cleanupStatus.progress.stage, "done", "cleanup status stage")

let diagnosticsData = """
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
""".data(using: .utf8)!

let diagnostics = try JSONDecoder.cineInsight.decode(DiagnosticsSnapshot.self, from: diagnosticsData)
assertEqual(diagnostics.videoCount, 3, "diagnostics video count")
assertEqual(diagnostics.redactedSettings.aiTaggingApiKeyConfigured, true, "diagnostics redaction")

let batchResultData = """
{
  "requested": 2,
  "succeeded": 1,
  "failed": 1,
  "errors": [{"video_id": 4, "error": "video was not found"}]
}
""".data(using: .utf8)!

let batchResult = try JSONDecoder.cineInsight.decode(BatchVideoOperationResult.self, from: batchResultData)
assertEqual(batchResult.requested, 2, "batch requested")
assertEqual(batchResult.errors[0].videoId, 4, "batch error video id")

let scanSyncData = """
{
  "directories": 1,
  "scanned": 3,
  "added": 1,
  "deleted": 1,
  "relocated": 1,
  "metadata_refreshed": 0,
  "skipped": 0,
  "errors": []
}
""".data(using: .utf8)!

let scanSync = try JSONDecoder.cineInsight.decode(ScanSyncResponse.self, from: scanSyncData)
assertEqual(scanSync.directories, 1, "scan sync directories")
assertEqual(scanSync.relocated, 1, "scan sync relocated")

print("CineInsightNative smoke tests passed")
