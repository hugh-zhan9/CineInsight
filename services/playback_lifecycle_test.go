package services

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFormalPlaybackAdmissionRemainsClosedThroughMaintenance(t *testing.T) {
	var gate formalPlaybackAdmission
	ctx, done, err := gate.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wait, release := gate.quiesce()
	defer release()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("in-flight context not cancelled")
	}
	completed := make(chan struct{})
	go func() { wait(); close(completed) }()
	select {
	case <-completed:
		t.Fatal("quiesce did not wait for in-flight operation")
	default:
	}
	if _, _, err := gate.begin(context.Background()); !errors.Is(err, ErrPlaybackQuiesced) {
		t.Fatal(err)
	}
	done()
	done()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("quiesce did not finish")
	}
	if _, _, err := gate.begin(context.Background()); !errors.Is(err, ErrPlaybackQuiesced) {
		t.Fatal("admission reopened before release")
	}
	_, secondRelease := gate.quiesce()
	release()
	release()
	if _, _, err := gate.begin(context.Background()); !errors.Is(err, ErrPlaybackQuiesced) {
		t.Fatal("one releaser reopened another owner's pause")
	}
	secondRelease()
	fresh, finish, err := gate.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	if fresh.Err() != nil {
		t.Fatal("new generation inherited cancellation")
	}
}

func TestQuiescePlaybackKeepsRelocationClosedAfterDrain(t *testing.T) {
	ctx, done, err := formalPlaybacks.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan func(), 1)
	go func() { result <- QuiescePlayback() }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("formal preparation not cancelled")
	}
	done()
	var release func()
	select {
	case release = <-result:
	case <-time.After(time.Second):
		t.Fatal("quiesce did not complete")
	}
	defer release()
	if _, ok := beginPlaybackRelocation(); ok {
		playbackRelocateWG.Done()
		t.Fatal("relocation reopened after drain")
	}
	StopPlaybackRelocation() // A temporary caller may not lift maintenance's pause.
	if _, ok := beginPlaybackRelocation(); ok {
		playbackRelocateWG.Done()
		t.Fatal("temporary stop released persistent pause")
	}
	release()
	if _, ok := beginPlaybackRelocation(); !ok {
		t.Fatal("failure recovery did not reopen relocation")
	}
	playbackRelocateWG.Done()
}

func TestPlaybackQuiesceRejectsEveryFormalEntryBeforeEffects(t *testing.T) {
	for _, name := range []string{"play", "random", "filtered", "reroll"} {
		t.Run(name, func(t *testing.T) {
			video, _ := notesFixture(t)
			useManualRandomCommitClock(t)
			previousOpen, previousLookup := openWithDefaultFn, iinaCLILookup
			calls := 0
			openWithDefaultFn = func(string, bool) error { calls++; return nil }
			iinaCLILookup = func() (string, bool) { return "", false }
			t.Cleanup(func() { openWithDefaultFn = previousOpen; iinaCLILookup = previousLookup })
			randomCommits.register(&pendingRandomCommit{token: "must-remain", videoID: video.ID, at: time.Now(), request: RandomPlayRequest{}})
			release := QuiescePlayback()
			defer release()
			service := &VideoService{}
			var err error
			switch name {
			case "play":
				_, err = service.PlayVideo(video.ID)
			case "random":
				_, err = service.PlayRandomVideo()
			case "filtered":
				_, err = service.PlayRandomVideoWithFilter(RandomPlayRequest{})
			case "reroll":
				_, err = service.RerollRandom("must-remain")
			}
			if !errors.Is(err, ErrPlaybackQuiesced) {
				t.Fatalf("%s bypassed maintenance admission: %v", name, err)
			}
			if calls != 0 {
				t.Fatal("rejected request dispatched a player")
			}
			randomCommits.mu.Lock()
			pending := randomCommits.pending
			randomCommits.mu.Unlock()
			if pending == nil || pending.token != "must-remain" {
				t.Fatal("rejected request consumed pending random fact")
			}
		})
	}
}
