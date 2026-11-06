//go:build nativeplaybackprobe && darwin

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"video-master/database"
	"video-master/models"
	"video-master/services"
)

func (p *ProbeApp) setupOwner() error {
	root, err := os.MkdirTemp("", "cine-native-owner-")
	if err != nil {
		return err
	}
	p.ownerDirectory = root
	db, err := gorm.Open(sqlite.Open(filepath.Join(root, "fixture.sqlite")), &gorm.Config{})
	if err != nil {
		return err
	}
	database.DB = db
	if err = db.AutoMigrate(models.AllModels()...); err != nil {
		return err
	}
	if err = db.Create(&models.Settings{}).Error; err != nil {
		return err
	}
	if err = db.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		return err
	}
	input, err := os.Open(p.source)
	if err != nil {
		return err
	}
	defer input.Close()
	path := filepath.Join(root, "fixture-1"+filepath.Ext(p.source))
	output, err := os.Create(path)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, input)
	output.Close()
	if err != nil {
		return err
	}
	for i := 1; i <= 4; i++ {
		target := filepath.Join(root, fmt.Sprintf("fixture-%d%s", i, filepath.Ext(p.source)))
		if i > 1 {
			if err = os.Link(path, target); err != nil {
				return err
			}
		}
		row := models.Video{Name: filepath.Base(target), Path: target, Directory: root, Duration: 4.05}
		if err = db.Create(&row).Error; err != nil {
			return err
		}
		subtitle := []byte("1\n00:00:00,000 --> 00:00:02,000\n本机字幕 <literal>\n第二行\n\n2\n00:00:02,000 --> 00:00:04,000\n字幕末尾\n")
		if err = os.WriteFile(target[:len(target)-len(filepath.Ext(target))]+".srt", subtitle, 0600); err != nil {
			return err
		}
	}
	p.ownerVideos = services.NewVideoService(nil)
	p.ownerSubtitles = services.NewSubtitleWorkbenchService(services.NewSubtitleService(root))
	p.ownerManager = services.NewNativePlaybackService(func(ctx context.Context, r services.PlaybackSubtitleRequest) (*services.PlaybackSubtitle, error) {
		if r.QueueToken != "" && p.ownerQueue != nil {
			return p.ownerQueue.GetPlaybackSubtitle(ctx, p.ownerSubtitles, r)
		}
		return p.ownerSubtitles.GetPlaybackSubtitle(ctx, r)
	})
	p.ownerManager.SetEmitter(func(state services.NativePlaybackState) { runtime.EventsEmit(p.ctx, "native-playback-state", state) })
	p.ownerCloser = services.RegisterNativePlaybackSourceCloser(func(ctx context.Context, paths []string) (func(), error) {
		return p.ownerManager.BeginSourceMutation(ctx, paths...)
	})
	return nil
}
func (p *ProbeApp) GetNativePlaybackCapability() services.NativePlaybackCapability {
	return p.ownerManager.Capability()
}
func (p *ProbeApp) AcquireNativePlaybackOwner(r services.NativeOwnerRequest) (services.NativeOwner, error) {
	return p.ownerManager.Acquire(r)
}
func (p *ProbeApp) ReleaseNativePlaybackOwner(r services.NativeOwnerRequestRef) error {
	return p.ownerManager.Release(context.Background(), r)
}
func (p *ProbeApp) OpenNativePlayback(r services.NativePlaybackOpenRequest) (services.NativePlaybackState, error) {
	return p.ownerManager.Open(context.Background(), r)
}
func (p *ProbeApp) CommandNativePlayback(r services.NativePlaybackCommand) (services.NativePlaybackCommandResult, error) {
	return p.ownerManager.Command(context.Background(), r)
}
func (p *ProbeApp) SetNativePlaybackViewport(r services.NativePlaybackViewport) (services.NativePlaybackRevision, error) {
	return p.ownerManager.SetViewport(r)
}
func (p *ProbeApp) GetNativePlaybackSnapshot(r services.NativePlaybackRef) (services.NativePlaybackState, error) {
	return p.ownerManager.Snapshot(r)
}
func (p *ProbeApp) CloseNativePlayback(r services.NativePlaybackCloseRequest) error {
	return p.ownerManager.Close(context.Background(), r)
}
func (p *ProbeApp) ConfigureNativePlaybackSubtitles(r services.NativePlaybackSubtitles) (services.NativePlaybackSubtitleResult, error) {
	return p.ownerManager.ConfigureSubtitles(context.Background(), r)
}
func (p *ProbeApp) GetPlaybackSubtitle(r services.PlaybackSubtitleRequest) (*services.PlaybackSubtitle, error) {
	if r.QueueToken != "" && p.ownerQueue != nil {
		return p.ownerQueue.GetPlaybackSubtitle(context.Background(), p.ownerSubtitles, r)
	}
	return p.ownerSubtitles.GetPlaybackSubtitle(context.Background(), r)
}
func (p *ProbeApp) GetPlaybackSession(id uint) (*services.PreviewSession, error) {
	session, err := p.ownerVideos.GetPreviewSession(id)
	if err != nil {
		return nil, err
	}
	return services.SelectNativePlaybackSession(session, p.ownerManager.Capability()), nil
}
func (p *ProbeApp) ResolveVisibleVideoForPlayback(id uint) (*services.VisiblePlaybackSource, error) {
	return p.ownerVideos.ResolveVisibleVideoForPlayback(context.Background(), id)
}
func (p *ProbeApp) RenameFixture(id uint) error {
	return p.ownerVideos.RenameVideo(id, "renamed-native-fixture"+filepath.Ext(p.source))
}
func (p *ProbeApp) stopOwner() error {
	if p.ownerQueue != nil {
		if err := p.ownerQueue.Close(); err != nil {
			return err
		}
	}
	if p.ownerManager == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.ownerManager.Stop(ctx)
}

func (p *ProbeApp) PrepareNativeQueueFixture() error {
	p.ownerVideos.SetNativePlaybackCapabilityProvider(p.ownerManager.Capability)
	p.ownerQueue = services.NewPlaybackQueueService(p.ownerVideos, nil, func(sequence uint64) { runtime.EventsEmit(p.ctx, "owner-queue-change", sequence) })
	if err := p.ownerQueue.Initialize(context.Background()); err != nil {
		return err
	}
	snapshot, err := p.ownerQueue.Snapshot(context.Background(), services.QueueQuery{Limit: 10})
	if err != nil {
		return err
	}
	if err = p.ownerQueue.Edit(context.Background(), services.QueueEdit{Action: "append_videos", VideoIDs: []uint{3, 4}, ExpectedRevision: snapshot.State.Revision}); err != nil {
		return err
	}
	snapshot, err = p.ownerQueue.Snapshot(context.Background(), services.QueueQuery{Limit: 10})
	if err != nil {
		return err
	}
	autoplay := true
	if err = p.ownerQueue.Edit(context.Background(), services.QueueEdit{Action: "configure", Player: "inline", Autoplay: &autoplay, ExpectedRevision: snapshot.State.Revision}); err != nil {
		return err
	}
	snapshot, err = p.ownerQueue.Snapshot(context.Background(), services.QueueQuery{Limit: 10})
	if err != nil {
		return err
	}
	p.ownerManager.SetQueueAuthorizer(p.ownerQueue.AuthorizeNativeSource)
	return p.ownerQueue.Play(context.Background(), snapshot.Items[0].ID, snapshot.State.Revision)
}
func (p *ProbeApp) GetPlaybackQueue(query services.QueueQuery) (*services.QueueSnapshot, error) {
	return p.ownerQueue.Snapshot(context.Background(), query)
}
func (p *ProbeApp) ReportInlineQueuePlayback(event services.InlineQueueEvent) error {
	return p.ownerQueue.ReportInline(context.Background(), event)
}
