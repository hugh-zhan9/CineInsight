---
source: app.go
captured_at: 2026-05-12
scope: Wails API parity inventory for Rust + SwiftUI replacement
---

# Wails API Parity Matrix

This matrix freezes the public Wails-bound behavior surface that the native macOS replacement must account for before the legacy Wails/Vue/Go app can retire.

## Startup and Diagnostics

| Legacy method | Native replacement area | Parity notes |
| --- | --- | --- |
| `GetStartupError` | Swift daemon health / diagnostics | Show startup, daemon, DB, and sidecar errors in native UI. |
| `GetShortFeedServerStatus` | Short feed status API/UI | Preserve status shape and allowed-access semantics. |
| `LogFrontend` | Swift log bridge | Preserve redaction and operational log support. |

## Video Library

| Legacy method | Native replacement area | Parity notes |
| --- | --- | --- |
| `GetAllVideos` | Video API compatibility | Legacy compatibility endpoint; paginated path remains preferred. |
| `GetVideosPaginated` | Video listing | Preserve cursor sort: `score ASC, size DESC, id DESC`. |
| `SearchVideos` | Video search | Keyword search over name/path. |
| `SearchSubtitleMatches` | Subtitle search | Preserve first-hit-per-video behavior and stale index refresh. |
| `SearchVideosByTags` | Video search | Preserve multi-tag AND semantics. |
| `SearchVideosWithFilters` | Video search | Preserve keyword, tags, size, height, cursor filters. |
| `SelectDirectory` | Swift/AppKit directory panel | Native-only UI responsibility. |
| `ScanDirectory` | Rust scan service | Preserve extension filtering. |
| `ScanDirectoryWithInfo` | Rust scan service | Preserve path + size output for relocation matching. |
| `RelocateVideo` | Rust video service | Preserve metadata and tags when path changes. |
| `RefreshVideoMetadata` | Rust media metadata service | Preserve ffprobe-based duration/resolution updates. |
| `BatchRefreshVideoMetadata` | Rust batch operations | Preserve per-video success/error summary. |
| `RenameVideo` | Rust video service | Preserve extension, collision rejection, DB rollback on file failure. |
| `AddVideo` | Rust video service | Preserve metadata probing and uniqueness constraints. |
| `GetVideosByDirectory` | Rust video service | Preserve directory/child path matching. |
| `DeleteVideo` | Rust video service | Preserve soft delete and optional file delete/trash behavior. |
| `BatchDeleteVideos` | Rust batch operations | Preserve result summary. |
| `OpenDirectory` | Swift/AppKit or Rust dispatch | Open containing directory in Finder. |

## Preview and Playback

| Legacy method | Native replacement area | Parity notes |
| --- | --- | --- |
| `GetPreviewSession` | Preview API + Swift AVKit UI | Preserve inline/external/unsupported modes and reason codes. |
| `PreviewExternally` | Preview dispatch | Preserve statistics-neutral external preview. |
| `PlayVideo` | Playback service | Preserve stats only after dispatch success. |
| `PlayRandomVideo` | Playback service | Preserve weighted random score behavior and failure messages. |

## Tags

| Legacy method | Native replacement area | Parity notes |
| --- | --- | --- |
| `AddTagToVideo` | Tag/video association | Preserve link idempotency. |
| `BatchAddTagToVideos` | Batch tag operations | Preserve batch result shape. |
| `RemoveTagFromVideo` | Tag/video association | Preserve unlink behavior. |
| `BatchRemoveTagFromVideos` | Batch tag operations | Preserve batch result shape. |
| `GetAllTags` | Tag service/UI | Active tags only. |
| `CreateTag` | Tag service/UI | Preserve color defaults and soft-delete restore behavior. |
| `UpdateTag` | Tag service/UI | Preserve rename conflict handling. |
| `DeleteTag` | Tag service/UI | Preserve soft-delete behavior. |

## AI Tagging

| Legacy method | Native replacement area | Parity notes |
| --- | --- | --- |
| `ListAITagCandidates` | AI review API/UI | Preserve filters by video, confidence, status. |
| `ApproveAITagCandidate` | AI review API/UI | Preserve candidate-to-tag approval records. |
| `RejectAITagCandidate` | AI review API/UI | Preserve rejection state. |
| `RejectAITagCandidatesByVideo` | AI review API/UI | Preserve bulk pending rejection. |
| `RetryAITagging` | AI worker control | Preserve retry state reset. |
| `GetAITaggingStatusSummary` | AI status API/UI | Preserve summary counts. |

## Settings and Directories

| Legacy method | Native replacement area | Parity notes |
| --- | --- | --- |
| `GetSettings` | Settings API/UI | Preserve all current fields and redacted display. |
| `UpdateSettings` | Settings API/UI | Preserve settings persistence and log toggling. |
| `GetAllDirectories` | Scan directory API/UI | Active directories only. |
| `AddDirectory` | Scan directory API/UI | Preserve path/alias fields. |
| `UpdateDirectory` | Scan directory API/UI | Preserve updates by ID. |
| `DeleteDirectory` | Scan directory API/UI | Preserve soft-delete behavior. |
| `SyncScanDirectories` | Scan sync service | Preserve added/relocated/deleted/refreshed/skipped/error counts. |

## Subtitles

| Legacy method | Native replacement area | Parity notes |
| --- | --- | --- |
| `GetSubtitleEngineStatuses` | Subtitle API/UI | Preserve WhisperX and Qwen status model. |
| `PrepareSubtitleEngine` | Sidecar preparation | Preserve progress and completion events. |
| `CheckSubtitleDependencies` | Sidecar diagnostics | Preserve dependency map semantics. |
| `DownloadSubtitleDependencies` | Sidecar preparation | Deprecated-compatible behavior. |
| `GenerateSubtitle` | Subtitle task service | Preserve engine/source language/bilingual/DeepL behavior. |
| `ForceGenerateSubtitle` | Subtitle task service | Preserve forced generation after validation failure. |
| `CancelSubtitle` | Subtitle task service | Preserve cancellation semantics. |
| `GetSubtitleSegments` | SRT parser/API | Preserve SRT path convention and segment shape. |

## Cleanup

| Legacy method | Native replacement area | Parity notes |
| --- | --- | --- |
| `GetCleanupCandidates` | Cleanup service | Preserve synchronous analysis behavior. |
| `StartCleanupAnalysis` | Cleanup task API/UI | Preserve asynchronous status startup. |
| `GetCleanupStatus` | Cleanup task API/UI | Preserve status/progress/result shape. |
