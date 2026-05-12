---
source: models/, database/database.go
captured_at: 2026-05-12
scope: PostgreSQL compatibility baseline for Rust + SwiftUI replacement
---

# PostgreSQL Schema Inventory

The native replacement must treat the current GORM/PostgreSQL schema as a legacy compatibility baseline.

## Tables From `models.AllModels()`

- `videos`
- `subtitle_segments`
- `subtitle_index_states`
- `tags`
- `ai_tag_candidates`
- `ai_tag_approval_records`
- `ai_tagging_states`
- `short_feed_interactions`
- `short_feed_tag_preferences`
- `settings`
- `scan_directories`
- GORM many-to-many join table: `video_tags`

## Required Compatibility Semantics

- Primary keys are unsigned integer IDs in Go; Rust should model database IDs conservatively as `i64`/`i32` at SQL boundary and domain newtypes where useful.
- `deleted_at IS NULL` means active row for soft-deleted tables.
- Active video paths are unique where `deleted_at IS NULL AND path <> ''`.
- Tags use soft-delete restore semantics for same-name recreation.
- `video_tags` is the canonical many-to-many video/tag table.
- Settings currently assume a singleton row.
- Subtitle search indexes filesystem `.srt` state into PostgreSQL.
- AI tagging state is idempotency-sensitive and must not be reset by migration.
- Short-feed interactions and tag preferences are long-lived user signals.

## Explicit Indexes and Extensions

From `database/database.go`:

- `idx_videos_path_active`: unique on `videos(path)` where active and non-empty.
- `idx_videos_directory_active`: `videos(directory)` where active.
- `idx_videos_size_active`: `videos(size)` where active.
- `idx_videos_height_active`: `videos(height)` where active.
- `idx_videos_stale_active`: `videos(is_stale)` where active.
- `idx_videos_score_inputs_active`: `videos(play_count, random_play_count, size, id)` where active.
- `idx_video_tags_tag_video`: `video_tags(tag_id, video_id)`.
- `idx_video_tags_video_tag`: `video_tags(video_id, tag_id)`.
- `idx_ai_tag_candidates_video_status`: `ai_tag_candidates(video_id, status)`.
- `idx_ai_tag_candidates_matched_status`: `ai_tag_candidates(matched_tag_id, status)`.
- `idx_ai_tag_approval_video_tag`: unique on `ai_tag_approval_records(video_id, tag_id)`.
- `idx_ai_tag_approval_records_candidate_id`: unique on `ai_tag_approval_records(candidate_id)`.
- `idx_ai_tagging_states_status_processed`: `ai_tagging_states(status, last_processed_at)`.
- `idx_short_feed_interactions_favorited_video`: `short_feed_interactions(favorited, video_id)`.
- `idx_short_feed_interactions_liked_video`: `short_feed_interactions(liked, video_id)`.
- `idx_short_feed_tag_preferences_score`: `short_feed_tag_preferences(score)`.
- Optional performance enhancement: `pg_trgm` extension, when available.
- Optional performance enhancement: `idx_subtitle_segments_text_trgm`, a GIN trigram index on `LOWER(text)`.

The legacy Go startup path logs and continues if `pg_trgm` or the trigram index cannot be created, so the Rust compatibility validator must not reject a database solely because these optional subtitle-search acceleration objects are missing.

## Foundation Build Fixture Plan

This first build slice does not require a live user database. It provides:

- Static schema validator tests for required legacy table/index names plus optional extension/index warnings.
- SQLx-ready configuration loading from `PG_HOST`, `PG_USER`, `PG_DB`, `PG_PORT`, `PG_PASSWORD`, `PG_SSLMODE`, `PG_TIMEZONE`.
- A future fixture slot under `db/fixtures/` for captured legacy dumps.

Live-data migration tests are intentionally deferred until a safe copy of a real or representative database is available.
