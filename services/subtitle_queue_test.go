package services

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSubtitleTaskQueueRunsOneTaskAtATimeAndExposesQueuedSnapshot(t *testing.T) {
	release := map[uint]chan struct{}{
		1: make(chan struct{}),
		2: make(chan struct{}),
	}
	started := make(chan uint, 2)
	queue := newSubtitleTaskQueue(nil, func(ctx context.Context, task *subtitleQueueTask) (*SubtitleGenerateResult, error) {
		started <- task.Request.VideoID
		select {
		case <-ctx.Done():
			return &SubtitleGenerateResult{Status: SubtitleResultStatusCancelled, VideoID: task.Request.VideoID}, nil
		case <-release[task.Request.VideoID]:
			return &SubtitleGenerateResult{Status: SubtitleResultStatusSuccess, VideoID: task.Request.VideoID}, nil
		}
	})

	firstDone := submitSubtitleQueueTask(t, queue, &subtitleQueueTask{
		TaskID:    1,
		Request:   SubtitleGenerateRequest{VideoID: 1, Engine: SubtitleEngineWhisperX},
		VideoName: "one.mp4",
	})
	if got := waitStartedVideoID(t, started); got != 1 {
		t.Fatalf("first task should start first, got video_id=%d", got)
	}

	secondDone := submitSubtitleQueueTask(t, queue, &subtitleQueueTask{
		TaskID:    2,
		Request:   SubtitleGenerateRequest{VideoID: 2, Engine: SubtitleEngineWhisperX},
		VideoName: "two.mp4",
	})
	waitForSubtitleQueueSnapshot(t, queue, func(snapshot SubtitleQueueSnapshot) bool {
		return snapshot.ActiveTask != nil &&
			snapshot.ActiveTask.VideoID == 1 &&
			len(snapshot.QueuedTasks) == 1 &&
			snapshot.QueuedTasks[0].VideoID == 2 &&
			snapshot.QueuedTasks[0].Position == 1
	})
	assertNoUnexpectedStartedTask(t, started)

	close(release[1])
	if result := <-firstDone; result.err != nil || result.result.Status != SubtitleResultStatusSuccess {
		t.Fatalf("first task result=%+v err=%v", result.result, result.err)
	}
	if got := waitStartedVideoID(t, started); got != 2 {
		t.Fatalf("second task should start after first finishes, got video_id=%d", got)
	}
	close(release[2])
	if result := <-secondDone; result.err != nil || result.result.Status != SubtitleResultStatusSuccess {
		t.Fatalf("second task result=%+v err=%v", result.result, result.err)
	}
}

func TestSubtitleTaskQueueCancelsQueuedTaskWithoutStartingIt(t *testing.T) {
	releaseFirst := make(chan struct{})
	started := make(chan uint, 2)
	queue := newSubtitleTaskQueue(nil, func(ctx context.Context, task *subtitleQueueTask) (*SubtitleGenerateResult, error) {
		started <- task.Request.VideoID
		if task.Request.VideoID == 1 {
			select {
			case <-ctx.Done():
				return &SubtitleGenerateResult{Status: SubtitleResultStatusCancelled, VideoID: task.Request.VideoID}, nil
			case <-releaseFirst:
			}
		}
		return &SubtitleGenerateResult{Status: SubtitleResultStatusSuccess, VideoID: task.Request.VideoID}, nil
	})

	firstDone := submitSubtitleQueueTask(t, queue, &subtitleQueueTask{
		TaskID:    1,
		Request:   SubtitleGenerateRequest{VideoID: 1, Engine: SubtitleEngineWhisperX},
		VideoName: "one.mp4",
	})
	if got := waitStartedVideoID(t, started); got != 1 {
		t.Fatalf("first task should start first, got video_id=%d", got)
	}

	secondDone := submitSubtitleQueueTask(t, queue, &subtitleQueueTask{
		TaskID:    2,
		Request:   SubtitleGenerateRequest{VideoID: 2, Engine: SubtitleEngineWhisperX},
		VideoName: "two.mp4",
	})
	waitForSubtitleQueueSnapshot(t, queue, func(snapshot SubtitleQueueSnapshot) bool {
		return len(snapshot.QueuedTasks) == 1 && snapshot.QueuedTasks[0].TaskID == 2
	})

	if err := queue.cancelTask(2); err != nil {
		t.Fatalf("cancel queued task failed: %v", err)
	}
	if result := <-secondDone; result.err != nil || result.result.Status != SubtitleResultStatusCancelled {
		t.Fatalf("queued task should return cancelled result, got result=%+v err=%v", result.result, result.err)
	}
	assertNoUnexpectedStartedTask(t, started)

	close(releaseFirst)
	if result := <-firstDone; result.err != nil || result.result.Status != SubtitleResultStatusSuccess {
		t.Fatalf("first task result=%+v err=%v", result.result, result.err)
	}
}

type subtitleQueueResult struct {
	result *SubtitleGenerateResult
	err    error
}

func submitSubtitleQueueTask(t *testing.T, queue *subtitleTaskQueue, task *subtitleQueueTask) <-chan subtitleQueueResult {
	t.Helper()
	done := make(chan subtitleQueueResult, 1)
	go func() {
		result, err := queue.submit(task)
		done <- subtitleQueueResult{result: result, err: err}
	}()
	return done
}

func waitStartedVideoID(t *testing.T, started <-chan uint) uint {
	t.Helper()
	select {
	case videoID := <-started:
		return videoID
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for task to start")
		return 0
	}
}

func assertNoUnexpectedStartedTask(t *testing.T, started <-chan uint) {
	t.Helper()
	select {
	case videoID := <-started:
		t.Fatalf("unexpected task started video_id=%d", videoID)
	case <-time.After(40 * time.Millisecond):
	}
}

func waitForSubtitleQueueSnapshot(t *testing.T, queue *subtitleTaskQueue, accept func(SubtitleQueueSnapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if accept(queue.snapshot()) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for expected queue snapshot: %+v", queue.snapshot())
}

func TestSubtitleTaskQueueCancelMissingTaskReturnsError(t *testing.T) {
	queue := newSubtitleTaskQueue(nil, func(ctx context.Context, task *subtitleQueueTask) (*SubtitleGenerateResult, error) {
		return &SubtitleGenerateResult{Status: SubtitleResultStatusSuccess, VideoID: task.Request.VideoID}, nil
	})
	if err := queue.cancelTask(99); err == nil || !errors.Is(err, ErrSubtitleTaskNotFound) {
		t.Fatalf("expected ErrSubtitleTaskNotFound, got %v", err)
	}
}
