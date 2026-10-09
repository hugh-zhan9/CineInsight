package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"
)

func TestCleanupConsolidationSubtitleFamilyAndSharedCopy(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "owned"
		if shared {
			name = "shared"
		}
		t.Run(name, func(t *testing.T) {
			s, r, a, _, root := consolidationTestFixture(t, "near")
			sourceSRT := subtitleparser.SRTPathForVideo(a.Path)
			if err := os.WriteFile(sourceSRT, []byte(writerTestSRT), 0600); err != nil {
				t.Fatal(err)
			}
			language := strings.TrimSuffix(a.Path, ".mp4") + ".zh.ass"
			if err := os.WriteFile(language, []byte("ass"), 0644); err != nil {
				t.Fatal(err)
			}
			nfo := strings.TrimSuffix(a.Path, ".mp4") + ".nfo"
			if err := os.WriteFile(nfo, []byte("metadata"), 0644); err != nil {
				t.Fatal(err)
			}
			if shared {
				_ = consolidationTestVideo(t, strings.TrimSuffix(a.Path, ".mp4")+".mkv", "shared video")
			}
			// 目标旧同名附件必须触发家庭一致改名，而非覆盖或提前删除。
			occupied := filepath.Join(r.Destination, "a.srt")
			if err := os.WriteFile(occupied, []byte("external subtitle"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := s.ConfigureConsolidation(filepath.Join(root, "data"), nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.StopConsolidationsAndWait() })
			p, err := s.PreviewConsolidation(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(p.Items[0].DestinationPath, "保留版") {
				t.Fatal("collision not previewed")
			}
			result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
			if err != nil {
				t.Fatal(err)
			}
			state := waitConsolidation(t, s, result.ID)
			if state.Status != "completed" {
				t.Fatalf("%+v", state)
			}
			targetSRT := subtitleparser.SRTPathForVideo(p.Items[0].DestinationPath)
			if string(mustReadBytes(t, targetSRT)) != writerTestSRT || string(mustReadBytes(t, occupied)) != "external subtitle" {
				t.Fatal("subtitle data lost")
			}
			info, err := os.Stat(targetSRT)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("permissions lost", err)
			}
			_, err = os.Stat(sourceSRT)
			if shared {
				if err != nil {
					t.Fatal("shared source removed", err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal("owned attachment remains", err)
			}
			if shared && string(mustReadBytes(t, nfo)) != "metadata" {
				t.Fatal("ambiguous NFO moved")
			}
		})
	}
}

func TestCleanupConsolidationSubtitleBusyAndLateWriter(t *testing.T) {
	t.Run("busy", func(t *testing.T) {
		s, p, a, _, _ := prepareConsolidationRun(t, "near")
		release := lockSubtitleFile(subtitleparser.SRTPathForVideo(a.Path))
		result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
		if err != nil {
			release()
			t.Fatal(err)
		}
		state := waitConsolidation(t, s, result.ID)
		release()
		if state.Status != "failed" || !strings.Contains(state.Error, "字幕正在写入") {
			t.Fatalf("%+v", state)
		}
		if _, err := os.Stat(a.Path); err != nil {
			t.Fatal("busy moved source", err)
		}
	})
	t.Run("late-replace-and-restore", func(t *testing.T) {
		s, r, a, _, root := consolidationTestFixture(t, "near")
		target := subtitleparser.SRTPathForVideo(a.Path)
		writer := NewLibrarySubtitleFileWriter(filepath.Join(root, "data"))
		if _, err := writer.Replace(context.Background(), a.ID, target, []byte(writerTestSRT)); err != nil {
			t.Fatal(err)
		}
		saved, err := writer.Replace(context.Background(), a.ID, target, []byte(writerTestSRT+"\n"))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ConfigureConsolidation(filepath.Join(root, "data"), nil); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.StopConsolidationsAndWait() })
		p, err := s.PreviewConsolidation(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
		if err != nil {
			t.Fatal(err)
		}
		state := waitConsolidation(t, s, result.ID)
		if state.Status != "completed" {
			t.Fatalf("%+v", state)
		}
		if _, err := writer.Replace(context.Background(), a.ID, target, []byte("late translation")); err == nil {
			t.Fatal("late writer recreated old subtitle")
		}
		if _, err := writer.RestoreBackup(context.Background(), a.ID, target, saved.BackupID); err == nil {
			t.Fatal("late restore recreated old subtitle")
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatal("old subtitle recreated")
		}
		newTarget := subtitleparser.SRTPathForVideo(p.Items[0].DestinationPath)
		if _, err := writer.Replace(context.Background(), a.ID, newTarget, []byte(writerTestSRT)); err != nil {
			t.Fatal("new path rejected", err)
		}
		if err := database.DB.Delete(&models.Video{}, a.ID).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Replace(context.Background(), a.ID, newTarget, []byte("deleted video")); err == nil {
			t.Fatal("real DB missing row swallowed")
		}
	})
}

func TestCleanupConsolidationSubtitleBucketDedupAndAlias(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.srt")
	release, err := tryLockConsolidationSubtitles([]string{path, path, strings.ToUpper(path)})
	if err != nil {
		t.Fatal("duplicate actual bucket self deadlocked", err)
	}
	release()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	held := lockSubtitleFile(filepath.Join(alias, "a.srt"))
	defer held()
	release, err = tryLockConsolidationSubtitles([]string{path})
	if err == nil {
		release()
		t.Fatal("directory aliases escaped subtitle lock")
	}
}

func TestCleanupConsolidationSubtitleQueuedWriterRejectsOldPath(t *testing.T) {
	s, p, a, _, root := prepareConsolidationRun(t, "near")
	oldTarget := subtitleparser.SRTPathForVideo(a.Path)
	writer := NewSubtitleService(filepath.Join(root, "writer-data")).subtitleWriter()
	queued, result := make(chan struct{}), make(chan error, 1)
	s.consolidationHooks = &consolidationExecutionHooks{checkpoint: func(phase string, _ uint, _ int) error {
		if phase != "before_commit" {
			return nil
		}
		go func() {
			close(queued)
			release := lockSubtitleFile(oldTarget)
			defer release()
			_, err := writer.Replace(context.Background(), a.ID, oldTarget, []byte("late generated subtitle"))
			result <- err
		}()
		<-queued
		select {
		case <-result:
			return errors.New("queued writer escaped migration subtitle lock")
		default:
		}
		return nil
	}}
	task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	state := waitConsolidation(t, s, task.ID)
	if state.Status != "completed" {
		t.Fatalf("%+v", state)
	}
	if err := <-result; err == nil || !strings.Contains(err.Error(), "视频位置已变化") {
		t.Fatal("late write not rejected", err)
	}
	if _, err := os.Stat(oldTarget); !os.IsNotExist(err) {
		t.Fatal("late writer recreated old path")
	}
}

func TestCleanupConsolidationRecoveryRefreshesCommittedSubtitleIndex(t *testing.T) {
	s, r, a, _, root := consolidationTestFixture(t, "near")
	oldTarget := subtitleparser.SRTPathForVideo(a.Path)
	if err := os.WriteFile(oldTarget, []byte(writerTestSRT), 0644); err != nil {
		t.Fatal(err)
	}
	if err := indexSubtitleFileForVideoID(a.ID, oldTarget); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureConsolidation(filepath.Join(root, "data"), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.StopConsolidationsAndWait() })
	p, err := s.PreviewConsolidation(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	s.consolidationHooks = &consolidationExecutionHooks{checkpoint: func(phase string, _ uint, _ int) error {
		if phase == "committed" {
			return ErrConsolidationConflict
		}
		return nil
	}}
	task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = waitConsolidation(t, s, task.ID)
	var before models.SubtitleIndexState
	if err := database.DB.Where("video_id = ?", a.ID).First(&before).Error; err != nil || before.SubtitlePath != oldTarget {
		t.Fatal("crash fixture already indexed", err)
	}
	if err := s.RecoverConsolidations(context.Background()); err != nil {
		t.Fatal(err)
	}
	var after models.SubtitleIndexState
	if err := database.DB.Where("video_id = ?", a.ID).First(&after).Error; err != nil || after.SubtitlePath != subtitleparser.SRTPathForVideo(p.Items[0].DestinationPath) {
		t.Fatal("recovery left stale subtitle index", after.SubtitlePath, err)
	}
}

func TestCleanupConsolidationSubtitleMovesAllSharedOwners(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	a := consolidationTestVideo(t, filepath.Join(root, "source", "film.mp4"), "a")
	c := consolidationTestVideo(t, filepath.Join(root, "source", "film.mkv"), "c")
	b := consolidationTestVideo(t, filepath.Join(root, "target", "b.mp4"), "b")
	d := consolidationTestVideo(t, filepath.Join(root, "target", "d.mp4"), "d")
	subtitle := filepath.Join(root, "source", "film.srt")
	if err := os.WriteFile(subtitle, []byte(writerTestSRT), 0644); err != nil {
		t.Fatal(err)
	}
	nfo := filepath.Join(root, "source", "film.nfo")
	if err := os.WriteFile(nfo, []byte("shared nfo"), 0644); err != nil {
		t.Fatal(err)
	}
	analysis := &CleanupAnalysis{NearDuplicateGroups: []CleanupDuplicateGroup{{Original: a, Candidates: []models.Video{b}}, {Original: c, Candidates: []models.Video{d}}}}
	service, request := consolidationTestService(t, analysis)
	request.Destination = filepath.Join(root, "target")
	if err := service.ConfigureConsolidation(filepath.Join(root, "data"), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.StopConsolidationsAndWait() })
	p, err := service.PreviewConsolidation(context.Background(), request)
	if err != nil || len(p.Errors) > 0 {
		t.Fatalf("%+v %v", p, err)
	}
	if len(p.Items) != 2 || len(p.Items[0].Files) != 2 || len(p.Items[1].Files) != 2 || !p.Items[0].Files[1].CopyOnly || !p.Items[1].Files[1].CopyOnly {
		t.Fatal("invalid fixture")
	}
	task, err := service.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	state := waitConsolidation(t, service, task.ID)
	if state.Status != "completed" || state.Completed != 2 {
		t.Fatalf("valid batch invalidated its own shared-subtitle ownership: status=%s completed=%d error=%s", state.Status, state.Completed, state.Error)
	}
	for _, item := range p.Items {
		if string(mustReadBytes(t, item.Files[1].Destination)) != writerTestSRT {
			t.Fatal("shared subtitle target missing")
		}
	}
	if string(mustReadBytes(t, subtitle)) != writerTestSRT || string(mustReadBytes(t, nfo)) != "shared nfo" {
		t.Fatal("shared sources removed")
	}
}

func TestCleanupConsolidationSubtitleRechecksExternalChangesBetweenItems(t *testing.T) {
	for _, change := range []string{"new-owner", "new-attachment", "changed-attachment"} {
		t.Run(change, func(t *testing.T) {
			setupVideoServiceTestDB(t)
			root := t.TempDir()
			a := consolidationTestVideo(t, filepath.Join(root, "source", "first.mp4"), "aa")
			c := consolidationTestVideo(t, filepath.Join(root, "source", "second.mp4"), "cc")
			b := consolidationTestVideo(t, filepath.Join(root, "target", "b.mp4"), "bb")
			d := consolidationTestVideo(t, filepath.Join(root, "target", "d.mp4"), "dd")
			subtitle := subtitleparser.SRTPathForVideo(c.Path)
			if err := os.WriteFile(subtitle, []byte(writerTestSRT), 0644); err != nil {
				t.Fatal(err)
			}
			s, r := consolidationTestService(t, &CleanupAnalysis{NearDuplicateGroups: []CleanupDuplicateGroup{{Original: a, Candidates: []models.Video{b}}, {Original: c, Candidates: []models.Video{d}}}})
			r.Destination = filepath.Dir(b.Path)
			if err := s.ConfigureConsolidation(filepath.Join(root, "data"), nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.StopConsolidationsAndWait() })
			p, err := s.PreviewConsolidation(context.Background(), r)
			if err != nil || len(p.Errors) > 0 {
				t.Fatal(p, err)
			}
			s.consolidationHooks = &consolidationExecutionHooks{checkpoint: func(phase string, id uint, _ int) error {
				if phase != "committed" || id != a.ID {
					return nil
				}
				path := subtitle
				if change == "new-owner" {
					path = strings.TrimSuffix(c.Path, ".mp4") + ".mkv"
				} else if change == "new-attachment" {
					path = strings.TrimSuffix(c.Path, ".mp4") + ".zh.srt"
				}
				return os.WriteFile(path, []byte("external"), 0644)
			}}
			task, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
			if err != nil {
				t.Fatal(err)
			}
			state := waitConsolidation(t, s, task.ID)
			if state.Status != "failed" || state.Completed != 1 {
				t.Fatal("external changes accepted", state.Status, state.Completed)
			}
			if string(mustReadBytes(t, c.Path)) != "cc" {
				t.Fatal("second source moved")
			}
			want := writerTestSRT
			if change == "changed-attachment" {
				want = "external"
			}
			if string(mustReadBytes(t, subtitle)) != want {
				t.Fatal("source subtitle changed")
			}
		})
	}
}
