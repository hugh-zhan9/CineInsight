package services

import (
	"gorm.io/gorm"
	"strings"
	"testing"
	"time"
	"video-master/models"
)

const chapterBackupRequest = "00000000-0000-4000-8000-000000000081"
const chapterBackupTombstone = "00000000-0000-4000-8000-000000000082"

func seedChapterBackupRows(t *testing.T, db *gorm.DB, deletedVideoID uint, at time.Time) []any {
	t.Helper()
	set := &models.ChapterSet{VideoID: deletedVideoID, TitleSnapshot: "retained chapters", SourceSize: 123, SourceModTimeNS: at.UnixNano(), Revision: 7, CreatedAt: at, UpdatedAt: at}
	job := &models.ChapterDraftJob{RequestID: chapterBackupRequest, RequestDigest: strings.Repeat("d", 64), VideoID: &deletedVideoID, TitleSnapshot: "retained draft", Status: "draft_ready", Revision: 5, Attempt: 1, SourceSize: 123, SourceModTimeNS: at.UnixNano(), SubtitleFingerprint: strings.Repeat("a", 64), ExpectedChapterRevision: 7, ProviderRevision: "expired-after-restore", Model: "fixture", ScopeEndMS: 2000, Whole: false, CompletedChunks: 1, TotalChunks: 1, DraftJSON: `[{"id":0,"start_ms":1000,"end_ms":2000,"title":"draft","summary":"summary","evidence":[{"cue_id":1,"start_ms":1000,"end_ms":2000}]}]`, CreatedAt: at, UpdatedAt: at}
	for _, row := range []any{set, job} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	chapter := &models.VideoChapter{SetID: set.ID, Position: 1, StartMS: 1000, EndMS: 2000, Title: "chapter", Summary: "preserved manual text", OriginJobID: &job.ID}
	if err := db.Create(chapter).Error; err != nil {
		t.Fatal(err)
	}
	tombstone := &models.ChapterDraftJob{RequestID: chapterBackupTombstone, RequestDigest: strings.Repeat("e", 64), Status: "failed", Revision: 9, Attempt: 1, Compacted: true, CreatedAt: at, UpdatedAt: at}
	if err := db.Create(tombstone).Error; err != nil {
		t.Fatal(err)
	}
	return []any{set, chapter, job}
}
func assertChapterBackupFields(t *testing.T, db *gorm.DB) {
	t.Helper()
	var set models.ChapterSet
	var job, tombstone models.ChapterDraftJob
	var metadata models.VideoTechnicalMetadata
	for _, read := range []func() error{func() error { return db.First(&set).Error }, func() error { return db.Where("request_id = ?", chapterBackupRequest).First(&job).Error }, func() error { return db.Where("request_id = ?", chapterBackupTombstone).First(&tombstone).Error }, func() error { return db.First(&metadata).Error }} {
		if err := read(); err != nil {
			t.Fatal(err)
		}
	}
	if set.SourceSize != 123 || set.SourceModTimeNS != job.SourceModTimeNS || job.SourceSize != 123 || job.SubtitleFingerprint != strings.Repeat("a", 64) || job.RequestDigest != strings.Repeat("d", 64) || job.DraftJSON == "" || job.ExpectedChapterRevision != 7 {
		t.Fatalf("private chapter snapshot lost: %+v %+v", set, job)
	}
	if !tombstone.Compacted || tombstone.VideoID != nil || tombstone.RequestDigest != strings.Repeat("e", 64) {
		t.Fatalf("lost request tombstone %+v", tombstone)
	}
	if metadata.ChaptersJSON == nil || !strings.Contains(*metadata.ChaptersJSON, "embedded") || metadata.ChaptersError != "" {
		t.Fatalf("lost probe chapters %+v", metadata)
	}
}
func assertChapterBackupManifest(t *testing.T, manifest *BackupManifest) {
	t.Helper()
	for _, table := range []string{"chapter_sets", "video_chapters", "chapter_draft_jobs"} {
		if len(manifest.Models[table]) == 0 {
			t.Fatalf("missing chapter manifest %s", table)
		}
		if manifest.Backend == "sqlite" && manifest.Rows[table] < 1 {
			t.Fatalf("empty chapter backup %s", table)
		}
	}
	old := map[string][]string{}
	for table, columns := range manifest.Models {
		old[table] = append([]string{}, columns...)
	}
	for _, table := range []string{"chapter_sets", "video_chapters", "chapter_draft_jobs"} {
		delete(old, table)
	}
	columns := []string{}
	for _, column := range old["video_technical_metadata"] {
		if column != "chapters_json" && column != "chapters_error" {
			columns = append(columns, column)
		}
	}
	old["video_technical_metadata"] = columns
	if got := unknownModelSchema(manifest.Models, old, nil); len(got) != 5 {
		t.Fatalf("old app accepted chapter backup: %v", got)
	}
	if got := unknownModelSchema(old, manifest.Models, nil); len(got) != 0 {
		t.Fatalf("new app rejected old probe shape: %v", got)
	}
}
