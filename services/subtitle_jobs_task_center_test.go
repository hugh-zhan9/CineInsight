package services

import (
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// 任务中心的字幕任务：最近 N 条之外，待确认、排队中与运行中的任务一条不落（待确认只能在任务中心处理）。
func TestListSubtitleJobsWithOpenKeepsOpenJobsBeyondRecentLimit(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := libVisVideo(t, "/lib/a/x.mp4", nil)
	base := time.Now().Add(-48 * time.Hour)
	create := func(status SubtitleQueueTaskStatus, at time.Time) models.SubtitleJob {
		t.Helper()
		job := models.SubtitleJob{VideoID: video.ID, Status: string(status), CreatedAt: at, UpdatedAt: at}
		if err := database.DB.Create(&job).Error; err != nil {
			t.Fatal(err)
		}
		return job
	}
	confirm := create(SubtitleQueueTaskStatusNeedsConfirmation, base)
	queued := create(SubtitleQueueTaskStatusQueued, base.Add(time.Minute))
	oldFailed := create(SubtitleQueueTaskStatusFailed, base.Add(2*time.Minute))
	for i := 0; i < 5; i++ {
		create(SubtitleQueueTaskStatusSucceeded, base.Add(time.Hour+time.Duration(i)*time.Minute))
	}

	plain, err := (&SubtitleService{}).ListSubtitleJobs(3)
	if err != nil || len(plain) != 3 {
		t.Fatalf("前置：最近 3 条应只有成功的: %+v err=%v", plain, err)
	}
	items, err := (&SubtitleService{}).ListSubtitleJobsWithOpen(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 {
		t.Fatalf("应为最近 3 条 + 待确认 + 排队中 = 5，实际 %d: %+v", len(items), items)
	}
	ids := map[uint]bool{}
	for index, item := range items {
		ids[item.ID] = true
		if index > 0 && items[index-1].ID < item.ID && item.Status == SubtitleQueueTaskStatusSucceeded {
			t.Fatalf("应按更新时间从新到旧: %+v", items)
		}
	}
	if !ids[confirm.ID] || !ids[queued.ID] {
		t.Fatalf("待确认与排队中的任务应在列表里: %+v", items)
	}
	if ids[oldFailed.ID] {
		t.Fatalf("已结束的旧任务仍按最近条数截断: %+v", items)
	}
	if last := items[len(items)-1]; last.ID != confirm.ID {
		t.Fatalf("最旧的待确认任务应排在最后: %+v", items)
	}
}
