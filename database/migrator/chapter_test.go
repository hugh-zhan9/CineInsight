package migrator

import (
	"context"
	"gorm.io/gorm"
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestMigrateChapterManualDraftAndCompactedIdentityRoundTrip(t *testing.T) {
	source, middle, back := sequenceSQLite(t), dbtest.Open(t), sequenceSQLite(t)
	video := models.Video{Name: "embedded", Path: "/fixture/embedded.mp4"}
	if err := source.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	text := `[{"id":1,"start_ms":0,"end_ms":1000,"title":"embedded"}]`
	metadata := models.VideoTechnicalMetadata{VideoID: video.ID, ChaptersJSON: &text}
	if err := source.Create(&metadata).Error; err != nil {
		t.Fatal(err)
	}
	set := models.ChapterSet{VideoID: 999, TitleSnapshot: "deleted media retained", SourceSize: 123, SourceModTimeNS: 1700000000123456789, Revision: 7}
	if err := source.Create(&set).Error; err != nil {
		t.Fatal(err)
	}
	chapter := models.VideoChapter{SetID: set.ID, Position: 1, StartMS: 1000, EndMS: 2000, Title: "manual", Summary: "outside range identity"}
	if err := source.Create(&chapter).Error; err != nil {
		t.Fatal(err)
	}
	id := uint(999)
	job := models.ChapterDraftJob{RequestID: "00000000-0000-4000-8000-000000000071", RequestDigest: "request digest", VideoID: &id, Status: "draft_ready", Revision: 4, Attempt: 1, SourceSize: 123, SourceModTimeNS: set.SourceModTimeNS, SubtitleFingerprint: "fingerprint", ExpectedChapterRevision: 7, ProviderRevision: "retired-provider", ScopeStartMS: 1000, ScopeEndMS: 2000, DraftJSON: `[{"start_ms":1000,"end_ms":2000,"title":"draft","summary":"text","evidence":[{"cue_id":2,"start_ms":1000,"end_ms":2000}]}]`}
	if err := source.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	compacted := models.ChapterDraftJob{RequestID: "00000000-0000-4000-8000-000000000072", RequestDigest: "compacted digest", Status: "failed", Revision: 3, Attempt: 1, Compacted: true}
	if err := source.Create(&compacted).Error; err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to *gorm.DB }{{source, middle}, {middle, back}} {
		if _, err := Migrate(context.Background(), Options{Source: step.from, Target: step.to, TargetBackend: database.Backend(step.to.Dialector.Name())}); err != nil {
			t.Fatal(err)
		}
		var got models.ChapterSet
		var member models.VideoChapter
		var draft, tombstone models.ChapterDraftJob
		var probe models.VideoTechnicalMetadata
		for _, read := range []func() error{func() error { return step.to.First(&got, set.ID).Error }, func() error { return step.to.First(&member, chapter.ID).Error }, func() error { return step.to.First(&draft, job.ID).Error }, func() error { return step.to.First(&tombstone, compacted.ID).Error }, func() error { return step.to.First(&probe, video.ID).Error }} {
			if err := read(); err != nil {
				t.Fatal(err)
			}
		}
		if got.SourceModTimeNS != set.SourceModTimeNS || got.VideoID != 999 || got.Revision != 7 || member.ID != chapter.ID || member.Summary != chapter.Summary || draft.DraftJSON != job.DraftJSON || draft.SubtitleFingerprint != job.SubtitleFingerprint || draft.SourceModTimeNS != job.SourceModTimeNS || draft.RequestDigest != job.RequestDigest {
			t.Fatal("chapter snapshot/identity lost")
		}
		if !tombstone.Compacted || tombstone.RequestID != compacted.RequestID || tombstone.RequestDigest != compacted.RequestDigest || tombstone.VideoID != nil {
			t.Fatal("idempotency tombstone lost")
		}
		if probe.ChaptersJSON == nil || *probe.ChaptersJSON != text {
			t.Fatal("embedded snapshot lost")
		}
	}
}
