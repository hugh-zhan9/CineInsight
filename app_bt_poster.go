package main

import (
	"context"
	"encoding/hex"
	"net/http"
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
