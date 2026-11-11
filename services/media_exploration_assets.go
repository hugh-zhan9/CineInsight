package services

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// ExplorationAsset owns the opened descriptors until the HTTP request finishes.
type ExplorationAsset struct {
	Reader  io.ReadSeeker
	MIME    string
	ModTime time.Time
	close   func()
}

func (a *ExplorationAsset) Close() {
	if a != nil && a.close != nil {
		a.close()
	}
}

func (s *MediaExplorationService) assetSource(ctx context.Context, r ExplorationAssetContext, kind string, id uint, requireSample bool) (string, error) {
	var path string
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		epoch, err := s.databaseEpoch(db)
		if err != nil {
			return err
		}
		if epoch != r.DatabaseEpoch {
			return ErrExplorationDatabase
		}
		db = explorationDB(db)
		state, err := explorationCheckContext(db, r.ExplorationContext)
		if err != nil {
			return err
		}
		var row models.MediaExplorationEntry
		if err := db.Where("id = ? AND state_id = ? AND scene_id = ? AND media_kind = ? AND media_id = ?", r.EntryID, state.ID, state.SceneID, kind, id).First(&row).Error; err != nil {
			return ErrExplorationUnavailable
		}
		videos, images, members, err := explorationMediaProjection(db, []models.MediaExplorationEntry{row})
		if err != nil {
			return err
		}
		if kind == "video" {
			video, ok := videos[id]
			if !ok || !explorationMembershipMatches(row, members) {
				return ErrExplorationUnavailable
			}
			path = video.Path
		} else {
			photo, ok := images[id]
			if !ok {
				return ErrExplorationUnavailable
			}
			path = photo.Path
		}
		if requireSample {
			var points []ExplorationSample
			if json.Unmarshal([]byte(row.SamplePointsJSON), &points) != nil {
				return ErrExplorationUnavailable
			}
			found := false
			for _, p := range points {
				if p.SourceVersion == r.SourceVersion && r.SourceVersion != "" {
					found = true
				}
			}
			if !found {
				return ErrBookmarkSourceChanged
			}
		}
		return nil
	})
	return path, err
}

// ResolveAsset authorizes every exploration request separately. Existing asset
// routes without exploration context retain their original behavior.
func (s *MediaExplorationService) ResolveAsset(ctx context.Context, r ExplorationAssetContext, kind string, id uint, asset string, edge int, ms int64, thumbs *ThumbnailService, images *ImageThumbnailService, videos *VideoService) (*ExplorationAsset, error) {
	if r.EntryID == 0 || id == 0 || (kind != "video" && kind != "image") {
		return nil, ErrExplorationInvalid
	}
	if asset != "thumbnail" && asset != "image" && asset != "media" && asset != "frame" {
		return nil, ErrExplorationInvalid
	}
	if kind == "image" && (asset == "frame" || asset == "media") || kind == "video" && asset == "image" {
		return nil, ErrExplorationInvalid
	}
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	requireSample := asset == "media" || asset == "frame"
	path, err := s.assetSource(ctx, r, kind, id, requireSample)
	if err != nil {
		return nil, err
	}
	source, err := os.Open(path)
	if err != nil {
		return nil, ErrExplorationUnavailable
	}
	var delivery *os.File
	accepted := false
	defer func() {
		if !accepted {
			source.Close()
			if delivery != nil && delivery != source {
				delivery.Close()
			}
		}
	}()
	info, err := source.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrExplorationUnavailable
	}
	if requireSample && videoSourceVersion(id, info.Size(), info.ModTime().UnixNano()) != r.SourceVersion {
		return nil, ErrBookmarkSourceChanged
	}
	result := &ExplorationAsset{ModTime: info.ModTime()}
	var reader io.ReadSeeker
	var delivered os.FileInfo
	switch asset {
	case "thumbnail":
		media, err := thumbs.resolveExplorationThumbnailUnderPathRead(ctx, kind, id, edge, images)
		if err != nil {
			return nil, err
		}
		delivery, err = os.Open(media.Path)
		if err != nil {
			return nil, err
		}
		reader = delivery
		result.MIME = "image/jpeg"
		result.ModTime = media.ModTime
	case "frame":
		data, at, err := thumbs.RenderFrameAt(ctx, id, ms, 960)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
		result.MIME = "image/jpeg"
		result.ModTime = at
	case "image":
		media, err := images.ResolveImageView(ctx, id)
		if err != nil {
			return nil, err
		}
		result.MIME = media.MIME
		if media.Path == path {
			delivery = source
		} else {
			delivery, err = os.Open(media.Path)
			if err != nil {
				return nil, err
			}
		}
		reader = delivery
	case "media":
		media, err := videos.ResolvePreviewMedia(id)
		if err != nil {
			return nil, err
		}
		result.MIME = media.MIME
		if media.Path == path {
			delivery = source
		} else {
			observed, err := os.Stat(media.Path)
			if err != nil {
				return nil, err
			}
			delivery, err = os.Open(media.Path)
			if err != nil {
				return nil, err
			}
			actual, err := delivery.Stat()
			if err != nil {
				return nil, err
			}
			if !os.SameFile(observed, actual) || !playbackProxyFingerprintOf(observed).matches(playbackProxyFingerprintOf(actual)) {
				return nil, ErrBookmarkSourceChanged
			}
		}
		reader = delivery
	}
	if delivery != nil {
		delivered, err = delivery.Stat()
		if err != nil {
			return nil, err
		}
		result.ModTime = delivered.ModTime()
	}
	// Generation may have taken time. Recheck scope and the exact opened source,
	// not merely the path that was valid before generation.
	currentPath, err := s.assetSource(ctx, r, kind, id, requireSample)
	if err != nil {
		return nil, err
	}
	if currentPath != path {
		return nil, ErrBookmarkSourceChanged
	}
	guard := &explorationAssetReader{reader: reader, ctx: ctx, source: source, sourcePath: path, sourceInfo: info, delivery: delivery, deliveryInfo: delivered}
	if err := guard.check(); err != nil {
		return nil, err
	}
	result.Reader = guard
	result.close = func() {
		source.Close()
		if delivery != nil && delivery != source {
			delivery.Close()
		}
	}
	accepted = true
	return result, nil
}

type explorationAssetReader struct {
	reader       io.ReadSeeker
	ctx          context.Context
	source       *os.File
	sourcePath   string
	sourceInfo   os.FileInfo
	delivery     *os.File
	deliveryInfo os.FileInfo
}

func (r *explorationAssetReader) check() error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	current, err := os.Stat(r.sourcePath)
	if err != nil {
		return ErrExplorationUnavailable
	}
	actual, err := r.source.Stat()
	if err != nil {
		return err
	}
	for _, info := range []os.FileInfo{current, actual} {
		if !os.SameFile(r.sourceInfo, info) || !playbackProxyFingerprintOf(r.sourceInfo).matches(playbackProxyFingerprintOf(info)) {
			return ErrBookmarkSourceChanged
		}
	}
	if r.delivery != nil {
		actual, err := r.delivery.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(r.deliveryInfo, actual) || !playbackProxyFingerprintOf(r.deliveryInfo).matches(playbackProxyFingerprintOf(actual)) {
			return ErrBookmarkSourceChanged
		}
	}
	return nil
}
func (r *explorationAssetReader) Read(p []byte) (int, error) {
	if err := r.check(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
func (r *explorationAssetReader) Seek(offset int64, whence int) (int64, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Seek(offset, whence)
}
