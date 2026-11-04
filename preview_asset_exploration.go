package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"video-master/services"
)

func (a *App) serveExplorationAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query()
	entry, err := strconv.ParseUint(query.Get("exploration_entry"), 10, 64)
	if err != nil || entry == 0 {
		http.Error(w, "invalid exploration context", http.StatusBadRequest)
		return
	}
	request := services.ExplorationAssetContext{ExplorationContext: services.ExplorationContext{DatabaseEpoch: query.Get("exploration_epoch"), Scope: query.Get("exploration_scope"), SceneID: query.Get("exploration_scene")}, EntryID: uint(entry), SourceVersion: query.Get("exploration_source")}
	kind, asset, prefix := "", "", ""
	for _, route := range []struct{ prefix, kind, asset string }{{"/preview/thumbnail/", "video", "thumbnail"}, {"/preview/image-thumbnail/", "image", "thumbnail"}, {"/preview/image/", "image", "image"}, {"/preview/media/", "video", "media"}, {"/preview/frame/", "video", "frame"}} {
		if strings.HasPrefix(r.URL.Path, route.prefix) {
			prefix, kind, asset = route.prefix, route.kind, route.asset
			break
		}
	}
	if prefix == "" {
		http.Error(w, "invalid exploration asset", http.StatusBadRequest)
		return
	}
	id, err := assetVideoIDFromPath(r.URL.Path, prefix)
	if err != nil {
		http.Error(w, "invalid media id", http.StatusBadRequest)
		return
	}
	edge := 96
	if value := query.Get("edge"); value != "" {
		edge, err = strconv.Atoi(value)
		if err != nil {
			http.Error(w, "invalid image size", http.StatusBadRequest)
			return
		}
	}
	var ms int64
	if asset == "frame" {
		ms, err = strconv.ParseInt(query.Get("ms"), 10, 64)
		if err != nil || ms < 0 {
			http.Error(w, "invalid frame time", http.StatusBadRequest)
			return
		}
	}
	owner, done, err := a.beginExplorationOperation()
	if err != nil {
		http.Error(w, "exploration unavailable", http.StatusServiceUnavailable)
		return
	}
	defer done()
	ctx, cancel := context.WithCancel(owner)
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	media, err := a.mediaExploration.ResolveAsset(ctx, request, kind, id, asset, edge, ms, a.thumbnailService, a.imageThumbnail, a.videoService)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, services.ErrExplorationInvalid) {
			status = http.StatusBadRequest
		} else if errors.Is(err, services.ErrExplorationUnavailable) {
			status = http.StatusNotFound
		}
		http.Error(w, "探索内容已变化或暂不可用，请重新打开", status)
		return
	}
	defer media.Close()
	w.Header().Set("Content-Type", media.MIME)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "exploration-media", media.ModTime, media.Reader)
}
