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
