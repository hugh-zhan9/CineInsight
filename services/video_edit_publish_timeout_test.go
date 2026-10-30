package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"video-master/database"
	"video-master/models"
)

// 刷盘或等路径锁慢于入库预算时，数据库仍应得到完整的执行时间。
func TestVideoEditPublishBudgetStartsAfterPreparation(t *testing.T) {
	for _, phase := range []string{"sync", "path_lock"} {
		t.Run(phase, func(t *testing.T) {
			service, media := newFakeEditService(t)
			service.publishTimeout = 100 * time.Millisecond
			service.syncPublishFile = func(path string) error {
				if err := fsyncEnhancementFile(path); err != nil {
					return err
				}
				if phase == "sync" {
					time.Sleep(3 * service.publishTimeout)
				} else {
					libraryPathMutationMu.Lock()
					go func() {
						time.Sleep(3 * service.publishTimeout)
						libraryPathMutationMu.Unlock()
					}()
				}
				return nil
			}
			view, originals := queuedMerge(t, service, media)
			done := waitEditProject(t, service, view.ID, 10*time.Second, models.VideoEditStatusCompleted, models.VideoEditStatusFailed)
			if done.Status != models.VideoEditStatusCompleted {
				t.Fatalf("慢准备阶段不应耗尽查名/入库预算: %+v", done.Items[0])
			}
			_, _ = outputPathOf(t, done, 1)
			if got := readFileString(t, originals[0].Path); got != "source:a.mkv" {
				t.Fatalf("原片变化: %q", got)
			}
		})
	}
}

// 真正阻塞的查名查询仍须受预算约束，失败时不发布成品。
func TestVideoEditPublishQueryStillHasDeadline(t *testing.T) {
	service, media := newFakeEditService(t)
	service.publishTimeout = 100 * time.Millisecond
	const callback = "test:publish_query_timeout"
	if err := database.DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "videos" {
			return
		}
		deadline, ok := tx.Statement.Context.Deadline()
		if !ok || time.Until(deadline) > service.publishTimeout {
			return
		}
		<-tx.Statement.Context.Done()
		tx.AddError(tx.Statement.Context.Err())
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Callback().Query().Remove(callback) })
	view, _ := queuedMerge(t, service, media)
	done := waitEditProject(t, service, view.ID, 10*time.Second, models.VideoEditStatusFailed, models.VideoEditStatusCompleted)
	if done.Items[0].ErrorCode != "publish_failed" || done.Items[0].OutputVideoID != nil {
		t.Fatalf("慢查询应失败且不入库: %+v", done.Items[0])
	}
	// 同时验证错误来源确实是查名期限，不是编码/校验路径。
	if got := done.Items[0].ErrorMessage; !(strings.Contains(got, "无法检查输出文件名") && strings.Contains(got, context.DeadlineExceeded.Error())) {
		t.Fatalf("unexpected error: %s", got)
	}
}
