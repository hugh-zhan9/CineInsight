package main

import (
	"context"
	"errors"
	"time"
	"video-master/services"
)

func (a *App) nativePlaybackDOMReady(ctx context.Context) {
	if a.nativePlayback == nil {
		return
	}
	ready, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a.nativePlayback.SetEmitter(func(state services.NativePlaybackState) {
		if a.videoStructure != nil {
			a.videoStructure.SetPlaybackActivity("native-"+state.SessionID, state.State == "opening" || state.State == "playing" || state.State == "seeking")
		}
		emitRuntimeEvent(ctx, "native-playback-state", state)
	})
	if err := a.nativePlayback.RegisterWindow(ready); err != nil {
		emitRuntimeEvent(ctx, "native-playback-ready", map[string]any{"available": false, "reason_message": "本机播放窗口尚未就绪"})
		return
	}
	a.nativeMutationMu.Lock()
	if a.nativeMutationRelease != nil {
		a.nativeMutationRelease()
	}
	a.nativeMutationRelease = services.RegisterNativePlaybackSourceCloser(func(ctx context.Context, paths []string) (func(), error) {
		return a.nativePlayback.BeginSourceMutation(ctx, paths...)
	})
	a.nativeMutationMu.Unlock()
	emitRuntimeEvent(ctx, "native-playback-ready", a.nativePlayback.Capability())
}
func (a *App) nativeContext() (context.Context, context.CancelFunc, error) {
	if a.nativePlayback == nil {
		return nil, nil, errors.New("native_unavailable: 本机播放组件未初始化")
	}
	if reason := a.databaseUnavailableReason(); reason != "" {
		return nil, nil, errors.New(reason)
	}
	ctx, cancel := context.WithTimeout(a.backgroundContext(), 30*time.Second)
	return ctx, cancel, nil
}
func (a *App) GetNativePlaybackCapability() services.NativePlaybackCapability {
	if a.nativePlayback == nil {
		return services.NativePlaybackCapability{ReasonCode: "native_unavailable", ReasonMessage: "本机播放组件未初始化"}
	}
	return a.nativePlayback.Capability()
}
func (a *App) AcquireNativePlaybackOwner(r services.NativeOwnerRequest) (services.NativeOwner, error) {
	_, cancel, err := a.nativeContext()
	if err != nil {
		return services.NativeOwner{}, err
	}
	defer cancel()
	return a.nativePlayback.Acquire(r)
}
func (a *App) ReleaseNativePlaybackOwner(r services.NativeOwnerRequestRef) error {
	if a.nativePlayback == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.nativePlayback.Release(ctx, r)
}
func (a *App) OpenNativePlayback(r services.NativePlaybackOpenRequest) (services.NativePlaybackState, error) {
	ctx, cancel, err := a.nativeContext()
	if err != nil {
		return services.NativePlaybackState{}, err
	}
	defer cancel()
	if a.videoStructure != nil {
		key := "native-opening-" + r.OwnerID
		a.videoStructure.SetPlaybackActivity(key, true)
		defer a.videoStructure.SetPlaybackActivity(key, false)
	}
	return a.nativePlayback.Open(ctx, r)
}
func (a *App) CommandNativePlayback(r services.NativePlaybackCommand) (services.NativePlaybackCommandResult, error) {
	ctx, cancel, err := a.nativeContext()
	if err != nil {
		return services.NativePlaybackCommandResult{}, err
	}
	defer cancel()
	return a.nativePlayback.Command(ctx, r)
}
func (a *App) SetNativePlaybackViewport(r services.NativePlaybackViewport) (services.NativePlaybackRevision, error) {
	_, cancel, err := a.nativeContext()
	if err != nil {
		return services.NativePlaybackRevision{}, err
	}
	defer cancel()
	return a.nativePlayback.SetViewport(r)
}
func (a *App) GetNativePlaybackSnapshot(r services.NativePlaybackRef) (services.NativePlaybackState, error) {
	_, cancel, err := a.nativeContext()
	if err != nil {
		return services.NativePlaybackState{}, err
	}
	defer cancel()
	return a.nativePlayback.Snapshot(r)
}
func (a *App) CloseNativePlayback(r services.NativePlaybackCloseRequest) error {
	if a.nativePlayback == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.nativePlayback.Close(ctx, r)
}
func (a *App) ConfigureNativePlaybackSubtitles(r services.NativePlaybackSubtitles) (services.NativePlaybackSubtitleResult, error) {
	ctx, cancel, err := a.nativeContext()
	if err != nil {
		return services.NativePlaybackSubtitleResult{}, err
	}
	defer cancel()
	return a.nativePlayback.ConfigureSubtitles(ctx, r)
}
func (a *App) GetPlaybackSession(videoID uint) (*services.PreviewSession, error) {
	if reason := a.databaseUnavailableReason(); reason != "" {
		return nil, errors.New(reason)
	}
	session, err := a.videoService.GetPreviewSession(videoID)
	if err != nil {
		return nil, err
	}
	return services.SelectNativePlaybackSession(session, a.GetNativePlaybackCapability()), nil
}
func (a *App) readNativePlaybackSubtitle(parent context.Context, r services.PlaybackSubtitleRequest) (*services.PlaybackSubtitle, error) {
	ctx, done, err := a.beginFunctionalPolish()
	if err != nil {
		return nil, err
	}
	defer done()
	read, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(parent, cancel)
	defer func() { stop(); cancel() }()
	if err := a.subtitleWorkbenchReady(); err != nil {
		return nil, err
	}
	if r.QueueToken != "" {
		queue, err := a.queueService()
		if err != nil {
			return nil, err
		}
		return queue.GetPlaybackSubtitle(read, a.subtitleWorkbench, r)
	}
	return a.subtitleWorkbench.GetPlaybackSubtitle(read, r)
}

func (a *App) releaseNativeMutationHook() {
	a.nativeMutationMu.Lock()
	defer a.nativeMutationMu.Unlock()
	if a.nativeMutationRelease != nil {
		a.nativeMutationRelease()
		a.nativeMutationRelease = nil
	}
}
