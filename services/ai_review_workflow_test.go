package services

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func workflowFixture(size int, approve func(context.Context, frozenReviewCandidate) (ReviewApprovalOutcome, error)) *ReviewApprovalWorkflow {
	return newReviewApprovalWorkflow(func(ctx context.Context, req ReviewApprovalRequest) (reviewApprovalBuild, error) {
		members := make([]frozenReviewCandidate, size)
		for i := range members {
			members[i].ID = uint(i + 1)
		}
		return reviewApprovalBuild{preview: ReviewApprovalPreview{Scope: req.Scope, Matched: size, MediaCount: size, LinkCount: size}, members: members}, nil
	}, approve, func() *BackgroundTaskRegistry { return nil }, BackgroundTaskAIReview)
}
func waitReviewWorkflow(t *testing.T, w *ReviewApprovalWorkflow) {
	t.Helper()
	done := make(chan struct{})
	go func() { w.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("review workflow did not stop")
	}
}
func approvedReviewItem(_ context.Context, member frozenReviewCandidate) (ReviewApprovalOutcome, error) {
	return ReviewApprovalOutcome{ID: member.ID, State: "approved"}, nil
}

func TestReviewWorkflowPreviewCapacityExpiryAndEmpty(t *testing.T) {
	w := workflowFixture(1, approvedReviewItem)
	now := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	first, err := w.Preview(context.Background(), ReviewApprovalRequest{Scope: "loaded"})
	if err != nil || first.Token == "" || first.Eligible != 1 {
		t.Fatalf("first %+v %v", first, err)
	}
	second, err := w.Preview(context.Background(), ReviewApprovalRequest{Scope: "filtered"})
	if err != nil || first.Token == second.Token {
		t.Fatalf("second %+v %v", second, err)
	}
	if _, err = w.Preview(context.Background(), ReviewApprovalRequest{}); !errors.Is(err, ErrReviewLimit) {
		t.Fatalf("capacity: %v", err)
	}
	now = now.Add(reviewPreviewLifetime)
	if _, err = w.Start(context.Background(), first.Token); !errors.Is(err, ErrReviewExpired) {
		t.Fatalf("expiry: %v", err)
	}
	third, err := w.Preview(context.Background(), ReviewApprovalRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Cancel(third.Token); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Start(context.Background(), third.Token); !errors.Is(err, ErrReviewExpired) {
		t.Fatalf("cancelled preview: %v", err)
	}
	empty, err := workflowFixture(0, approvedReviewItem).Preview(context.Background(), ReviewApprovalRequest{Scope: "loaded"})
	if err != nil || empty.Token != "" || empty.Matched != 0 {
		t.Fatalf("empty %+v %v", empty, err)
	}
}

func TestReviewWorkflowFreezesOnlyBuiltMembersAndPartialCancellation(t *testing.T) {
	entered, commit := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	w := workflowFixture(3, func(ctx context.Context, member frozenReviewCandidate) (ReviewApprovalOutcome, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-commit
		}
		return approvedReviewItem(ctx, member)
	})
	preview, err := w.Preview(context.Background(), ReviewApprovalRequest{})
	if err != nil {
		t.Fatal(err)
	}
	state, err := w.Start(context.Background(), preview.Token)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	again, err := w.Start(context.Background(), preview.Token)
	if err != nil || again.Token != state.Token {
		t.Fatalf("duplicate start %+v %v", again, err)
	}
	if _, err = w.Preview(context.Background(), ReviewApprovalRequest{}); !errors.Is(err, ErrReviewBusy) {
		t.Fatalf("preview while running: %v", err)
	}
	if err = w.Cancel(preview.Token); err != nil {
		t.Fatal(err)
	}
	close(commit)
	waitReviewWorkflow(t, w)
	state, err = w.State(preview.Token, 0, 200)
	if err != nil || state.State != "cancelled" || state.Processed != 1 || state.Succeeded != 1 || state.Remaining != 2 || calls.Load() != 1 {
		t.Fatalf("partial %+v calls=%d %v", state, calls.Load(), err)
	}
	if len(state.Results) != 1 || state.Results[0].ID != 1 {
		t.Fatalf("outcomes %+v", state.Results)
	}
}

func TestReviewWorkflowMaintenanceWaitsForLatePreviewAndPreventsPublication(t *testing.T) {
	entered, cancelObserved, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	w := workflowFixture(1, approvedReviewItem)
	w.build = func(ctx context.Context, _ ReviewApprovalRequest) (reviewApprovalBuild, error) {
		close(entered)
		<-ctx.Done()
		close(cancelObserved)
		<-release
		return reviewApprovalBuild{preview: ReviewApprovalPreview{Matched: 1}, members: []frozenReviewCandidate{{ID: 100}}}, nil
	}
	previewDone := make(chan error, 1)
	go func() { _, err := w.Preview(context.Background(), ReviewApprovalRequest{}); previewDone <- err }()
	<-entered
	if _, err := w.Preview(context.Background(), ReviewApprovalRequest{}); !errors.Is(err, ErrReviewBusy) {
		t.Fatalf("concurrent preview: %v", err)
	}
	stopped := make(chan struct{})
	go func() { w.CloseAndWait(); close(stopped) }()
	<-cancelObserved
	select {
	case <-stopped:
		t.Fatal("stopped before builder exited")
	default:
	}
	close(release)
	if err := <-previewDone; !errors.Is(err, ErrReviewClosed) {
		t.Fatalf("late preview: %v", err)
	}
	<-stopped
	if len(w.previews) != 0 {
		t.Fatal("closed workflow published a preview")
	}
	if _, err := w.Preview(context.Background(), ReviewApprovalRequest{}); !errors.Is(err, ErrReviewClosed) {
		t.Fatalf("closed admission: %v", err)
	}
}

func TestReviewWorkflowResultPaginationAndReplacedToken(t *testing.T) {
	w := workflowFixture(260, approvedReviewItem)
	preview, err := w.Preview(context.Background(), ReviewApprovalRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Start(context.Background(), preview.Token); err != nil {
		t.Fatal(err)
	}
	waitReviewWorkflow(t, w)
	first, err := w.State(preview.Token, 0, 500)
	if err != nil || len(first.Results) != 200 || first.NextAfter != 200 || !first.HasMore || first.Succeeded != 260 {
		t.Fatalf("first %+v %v", first, err)
	}
	second, err := w.State(preview.Token, first.NextAfter, 200)
	if err != nil || len(second.Results) != 60 || second.NextAfter != 260 || second.HasMore {
		t.Fatalf("second %+v %v", second, err)
	}
	if _, err = w.State(preview.Token, 999, 1); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	if _, err = w.State("", 1, 1); err == nil {
		t.Fatal("cursor without batch identity accepted")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	w.approve = func(ctx context.Context, m frozenReviewCandidate) (ReviewApprovalOutcome, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		return approvedReviewItem(ctx, m)
	}
	next, err := w.Preview(context.Background(), ReviewApprovalRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Start(context.Background(), next.Token); err != nil {
		t.Fatal(err)
	}
	<-entered
	defer func() { close(release); waitReviewWorkflow(t, w) }()
	if _, err = w.Start(context.Background(), preview.Token); !errors.Is(err, ErrReviewExpired) {
		t.Fatalf("old start must be expired: %v", err)
	}
	if _, err = w.State(preview.Token, 0, 1); !errors.Is(err, ErrReviewExpired) {
		t.Fatalf("old state: %v", err)
	}
}

func TestReviewWorkflowRegistryCallbacksCanReadStateAndFatalStops(t *testing.T) {
	var calls atomic.Int32
	w := workflowFixture(3, func(ctx context.Context, m frozenReviewCandidate) (ReviewApprovalOutcome, error) {
		if calls.Add(1) == 2 {
			return ReviewApprovalOutcome{}, errors.New("database unavailable")
		}
		return approvedReviewItem(ctx, m)
	})
	registry := NewBackgroundTaskRegistry()
	w.registry = func() *BackgroundTaskRegistry { return registry }
	registry.SetOnChange(func(_ []string) {
		if _, err := w.State("", 0, 0); err != nil {
			t.Error(err)
		}
	})
	preview, err := w.Preview(context.Background(), ReviewApprovalRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Start(context.Background(), preview.Token); err != nil {
		t.Fatal(err)
	}
	waitReviewWorkflow(t, w)
	state, err := w.State(preview.Token, 0, 10)
	if err != nil || state.State != "failed" || state.Succeeded != 1 || state.Remaining != 2 || calls.Load() != 2 {
		t.Fatalf("fatal %+v %v", state, err)
	}
	if len(registry.Snapshot()) != 0 {
		t.Fatal("task registration leaked")
	}
}

func TestReviewWorkflowRejectsOversizedPreviewWithoutTruncation(t *testing.T) {
	w := workflowFixture(1, approvedReviewItem)
	w.build = func(context.Context, ReviewApprovalRequest) (reviewApprovalBuild, error) {
		return reviewApprovalBuild{preview: ReviewApprovalPreview{Matched: maxReviewPreviewMembers + 1}, members: []frozenReviewCandidate{{ID: 1}}}, nil
	}
	preview, err := w.Preview(context.Background(), ReviewApprovalRequest{})
	if !errors.Is(err, ErrReviewLimit) || preview.Token != "" || len(w.previews) != 0 {
		t.Fatalf("oversized preview published %+v %v", preview, err)
	}
}

func TestReviewWorkflowBoundsCombinedUnconsumedMembers(t *testing.T) {
	w := workflowFixture(maxReviewPreviewMembers/2+1, approvedReviewItem)
	first, err := w.Preview(context.Background(), ReviewApprovalRequest{})
	if err != nil || first.Eligible != maxReviewPreviewMembers/2+1 {
		t.Fatalf("first %+v %v", first, err)
	}
	second, err := w.Preview(context.Background(), ReviewApprovalRequest{})
	if !errors.Is(err, ErrReviewLimit) || second.Token != "" {
		t.Fatalf("combined member limit %+v %v", second, err)
	}
	if err := w.Cancel(first.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Preview(context.Background(), ReviewApprovalRequest{}); err != nil {
		t.Fatalf("released capacity was not reusable: %v", err)
	}
}

func TestReviewWorkflowNotifiesRunningProgressAtMostOncePerSecond(t *testing.T) {
	var ticks atomic.Int64
	w := workflowFixture(5, func(ctx context.Context, member frozenReviewCandidate) (ReviewApprovalOutcome, error) {
		ticks.Add(int64(600 * time.Millisecond))
		return approvedReviewItem(ctx, member)
	})
	w.now = func() time.Time { return time.Unix(0, ticks.Load()) }
	var notifications atomic.Int32
	w.SetProgressNotifier(func() {
		state, err := w.State("", 0, 0)
		if err != nil || state.State != "running" || state.Processed == 0 {
			t.Errorf("progress %+v %v", state, err)
		}
		notifications.Add(1)
	})
	preview, err := w.Preview(context.Background(), ReviewApprovalRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Start(context.Background(), preview.Token); err != nil {
		t.Fatal(err)
	}
	waitReviewWorkflow(t, w)
	if notifications.Load() != 2 {
		t.Fatalf("running notifications=%d", notifications.Load())
	}
}
