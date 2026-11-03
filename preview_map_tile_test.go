package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"
)

func TestMapTileDesktopRouteServesOnlyValidatedCachedPNG(t *testing.T) {
	setupAppTestDB(t)
	if err := database.DB.Create(&models.Settings{}).Error; err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var body bytes.Buffer
	if err := png.Encode(&body, image.NewRGBA(image.Rect(0, 0, 256, 256))); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "map-tiles")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "osm-0-0-0.png"), body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(map[string]any{"key": "osm-0-0-0", "size": body.Len(), "expires": time.Now().Add(time.Hour)})
	if err := os.WriteFile(filepath.Join(directory, "osm-0-0-0.json"), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	service := services.NewMapTileService(root, &services.SettingsService{})
	defer service.StopAndWait()
	a := &App{mapTiles: service}
	status := a.GetMapTileStatus()
	if status.Status != "ok" || status.Data.Enabled {
		t.Fatalf("status %+v", status)
	}
	handler := newAssetHandler(a)
	path := "/preview/map-tile/osm/0/0/0.png?generation=" + status.Data.Generation
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
	if recorder.Code != 200 || !bytes.Equal(recorder.Body.Bytes(), body.Bytes()) || recorder.Header().Get("Content-Type") != "image/png" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("tile %d %s", recorder.Code, recorder.Body)
	}
	for _, request := range []struct {
		method, path string
		status       int
	}{{"HEAD", path, 405}, {"GET", "/preview/map-tile/other/0/0/0.png?generation=" + status.Data.Generation, 400}, {"GET", "/preview/map-tile/osm/0/1/0.png?generation=" + status.Data.Generation, 400}, {"GET", "/preview/map-tile/osm/0/0/0.png?generation=stale", 503}} {
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, httptest.NewRequest(request.method, request.path, nil))
		if out.Code != request.status {
			t.Fatalf("%s %d", request.path, out.Code)
		}
	}
	a.stopFunctionalPolish(false)
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, httptest.NewRequest(http.MethodGet, path, nil))
	if out.Code != 503 {
		t.Fatal("tile route crossed maintenance")
	}
	if _, err := service.Status(context.Background()); err == nil {
		t.Fatal("stopped owner served policy")
	}
}
