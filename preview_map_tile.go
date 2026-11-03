package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
)

// This route is registered only in the desktop Wails asset handler, never LAN/Jellyfin.
func (a *App) serveMapTile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", 405)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/preview/map-tile/"), "/")
	if len(parts) != 4 {
		http.NotFound(w, r)
		return
	}
	z, e1 := strconv.Atoi(parts[1])
	x, e2 := strconv.Atoi(parts[2])
	y, e3 := strconv.Atoi(strings.TrimSuffix(parts[3], ".png"))
	if e1 != nil || e2 != nil || e3 != nil || !strings.HasSuffix(parts[3], ".png") {
		http.Error(w, "invalid tile", 400)
		return
	}
	ctx, done, err := a.beginMapTileOperation()
	if err != nil {
		http.Error(w, "map unavailable", 503)
		return
	}
	defer done()
	requestCtx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	defer cancel()
	tile, err := a.mapTiles.Get(requestCtx, parts[0], z, x, y, r.URL.Query().Get("generation"))
	if err != nil {
		code := a.experienceError(err)
		status := http.StatusServiceUnavailable
		if code.Code == "invalid_input" {
			status = http.StatusBadRequest
		}
		http.Error(w, code.Code, status)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(tile.Bytes)
}
