package services

import (
	"context"
	"errors"
	"os"

	"gorm.io/gorm"
	"video-master/database"
)

func resolveNativeSource(ctx context.Context, r NativePlaybackOpenRequest) (*nativeSource, error) {
	return resolveNativeSourceGuarded(ctx, r, nil)
}
func resolveNativeSourceGuarded(ctx context.Context, r NativePlaybackOpenRequest, admit func(context.Context, string) (func(), error)) (*nativeSource, error) {
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			unlock()
		}
	}()
	var source *nativeSource
	var releaseAdmission func()
	defer func() {
		if !accepted && releaseAdmission != nil {
			releaseAdmission()
		}
	}()
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		video, info, err := bookmarkSourceInScope(db, r.VideoID)
		if err != nil {
			return err
		}
		if videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano()) != r.SourceVersion {
			return ErrQueueSourceChanged
		}
		if admit != nil {
			releaseAdmission, err = admit(ctx, video.Path)
			if err != nil {
				return err
			}
		}
		file, err := os.Open(video.Path)
		if err != nil {
			return err
		}
		actual, err := file.Stat()
		if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) || !playbackProxyFingerprintOf(info).matches(playbackProxyFingerprintOf(actual)) {
			file.Close()
			return ErrQueueSourceChanged
		}
		source = &nativeSource{file: file, path: video.Path, info: actual, videoID: video.ID, version: r.SourceVersion, duration: video.Duration, unlock: func() {
			if releaseAdmission != nil {
				releaseAdmission()
				releaseAdmission = nil
			}
			unlock()
		}}
		return nil
	})
	if err != nil {
		return nil, err
	}
	accepted = true
	return source, nil
}
func verifyNativeSource(ctx context.Context, source *nativeSource) error {

	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		video, info, err := bookmarkSourceInScope(db, source.videoID)
		if err != nil {
			return err
		}
		if video.Path != source.path || !os.SameFile(info, source.info) || videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano()) != source.version {
			return ErrQueueSourceChanged
		}
		actual, err := source.file.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(actual, source.info) || !playbackProxyFingerprintOf(actual).matches(playbackProxyFingerprintOf(source.info)) {
			return ErrQueueSourceChanged
		}
		return ctx.Err()
	})
}
func nativeSourceError(err error) string {
	if errors.Is(err, ErrQueueSourceChanged) {
		return "source_changed"
	}
	return "source_unavailable"
}
