package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func newAssetHandler(app *App) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/preview/person-avatar/") {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			app.servePersonAvatar(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/preview/collection-cover/") {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			app.serveCollectionCover(w, r)
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			if strings.HasPrefix(r.URL.Path, "/preview/media/") {
				app.servePreviewMedia(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/preview/thumbnail/") {
				app.serveThumbnail(w, r)
				return
			}
		}

		http.NotFound(w, r)
	})
}

func (a *App) serveCollectionCover(w http.ResponseWriter, r *http.Request) {
	collectionID, err := assetVideoIDFromPath(r.URL.Path, "/preview/collection-cover/")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	asset, err := a.collectionService.ResolveCollectionCover(collectionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "collection cover not found", http.StatusNotFound)
			return
		}
		http.Error(w, "collection cover unavailable", http.StatusInternalServerError)
		return
	}
	file, err := os.Open(asset.Path)
	if err != nil {
		http.Error(w, "collection cover not found", http.StatusNotFound)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", asset.MIME)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, asset.DisplayName, asset.ModTime, file)
}

func (a *App) servePersonAvatar(w http.ResponseWriter, r *http.Request) {
	personID, err := assetVideoIDFromPath(r.URL.Path, "/preview/person-avatar/")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	asset, err := a.personService.ResolvePersonAvatar(personID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "person avatar not found", http.StatusNotFound)
			return
		}
		http.Error(w, "person avatar unavailable", http.StatusInternalServerError)
		return
	}
	file, err := os.Open(asset.Path)
	if err != nil {
		http.Error(w, "person avatar not found", http.StatusNotFound)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", asset.MIME)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, asset.DisplayName, asset.ModTime, file)
}

func (a *App) serveThumbnail(w http.ResponseWriter, r *http.Request) {
	videoID, err := assetVideoIDFromPath(r.URL.Path, "/preview/thumbnail/")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	media, err := a.thumbnailService.ResolveThumbnail(r.Context(), videoID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "thumbnail not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("thumbnail unavailable: %v", err), http.StatusInternalServerError)
		return
	}
	file, err := os.Open(media.Path)
	if err != nil {
		http.Error(w, "thumbnail not found", http.StatusNotFound)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, filepath.Base(media.Path), media.ModTime, file)
}

func (a *App) servePreviewMedia(w http.ResponseWriter, r *http.Request) {
	videoID, err := previewVideoIDFromPath(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	media, err := a.videoService.ResolvePreviewMedia(videoID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "preview media not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("preview media unavailable: %v", err), http.StatusInternalServerError)
		return
	}

	file, err := os.Open(media.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "preview media not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("open preview media failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer file.Close()

	if media.MIME != "" {
		w.Header().Set("Content-Type", media.MIME)
	}

	http.ServeContent(w, r, media.DisplayName, media.ModTime, file)
}

func previewVideoIDFromPath(path string) (uint, error) {
	return assetVideoIDFromPath(path, "/preview/media/")
}

func assetVideoIDFromPath(path, prefix string) (uint, error) {
	videoIDText := strings.TrimPrefix(path, prefix)
	if videoIDText == "" || videoIDText == path {
		return 0, fmt.Errorf("invalid asset path")
	}

	videoID, err := strconv.ParseUint(videoIDText, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid asset id")
	}

	return uint(videoID), nil
}
