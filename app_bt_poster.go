package main

import (
	"context"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) serveBTPoster(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/preview/bt-poster/")
	_, tokenErr := hex.DecodeString(strings.TrimPrefix(token, "ci-bt-"))
	if len(token) != 54 || !strings.HasPrefix(token, "ci-bt-") || tokenErr != nil || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	ctx, cancel, err := a.btContext()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer cancel()
	// Closing the image request must also cancel its remote fetch.
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	data, mime, err := a.bt.service.Poster(ctx, token)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func (a *App) serveBTDetailImage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", 405)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/preview/bt-detail-image/"), "/")
	if len(parts) != 3 || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	token := parts[0]
	detailOrder, orderErr := strconv.ParseUint(parts[1], 10, 64)
	_, tokenErr := hex.DecodeString(strings.TrimPrefix(token, "ci-bt-"))
	index, err := strconv.Atoi(parts[2])
	if len(token) != 54 || !strings.HasPrefix(token, "ci-bt-") || tokenErr != nil || err != nil || orderErr != nil || detailOrder == 0 || index < 0 || index >= 16 {
		http.NotFound(w, r)
		return
	}
	ctx, cancel, err := a.btContext()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	data, mime, err := a.bt.service.DetailImage(ctx, token, detailOrder, index)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}
