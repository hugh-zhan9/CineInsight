package services

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
	"video-master/services/btclient"
	"video-master/services/btmanifest"
)

// This opt-in test transfers only generated media between two owned loopback
// clients. Prowlarr is an HTTP fixture; every downloader/import operation is real.
func TestBTIsolatedSelectiveDownloadToLibrary(t *testing.T) {
	if os.Getenv("CINEINSIGHT_BT_QB_PROBE") != "1" {
		t.Skip("explicit isolated-profile test only")
	}
	binary, err := findBrowserDownloadFFmpeg()
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "generated.mp4")
	generate, stopGenerate := context.WithTimeout(context.Background(), 10*time.Second)
	command := exec.CommandContext(generate, "nice", "-n", "10", binary, "-nostdin", "-v", "error", "-threads", "2", "-filter_threads", "1", "-f", "lavfi", "-i", "testsrc2=size=64x64:rate=5:duration=1", "-c:v", "libx264", "-threads", "2", "-pix_fmt", "yuv420p", "-y", source)
	output, err := command.CombinedOutput()
	stopGenerate()
	if err != nil {
		t.Fatalf("synthetic video generation: %v %s", err, output)
	}
	videoBytes, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	payloads := [][]byte{videoBytes, append([]byte(nil), videoBytes...), []byte("1\n00:00:00,000 --> 00:00:00,500\nSynthetic fixture\n")}
	names := []string{"chosen.mp4", "unselected.mp4", "chosen.srt"}
	var joined []byte
	files := []any{}
	for i, name := range names {
		joined = append(joined, payloads[i]...)
		files = append(files, map[string]any{"length": len(payloads[i]), "path": []any{name}})
	}
	var pieces []byte
	for offset := 0; offset < len(joined); offset += 16384 {
		end := offset + 16384
		if end > len(joined) {
			end = len(joined)
		}
		sum := sha1.Sum(joined[offset:end])
		pieces = append(pieces, sum[:]...)
	}
	torrent := btTestBencode(map[string]any{"info": map[string]any{"name": "synthetic-season", "piece length": 16384, "pieces": string(pieces), "files": files}})
	manifest, err := btmanifest.Parse(torrent)
	if err != nil {
		t.Fatal(err)
	}
	oldDB := database.DB
	database.DB = dbtest.Open(t)
	t.Cleanup(func() { database.DB = oldDB })
	if err := database.DB.Create(&models.Settings{VideoExtensions: ".mp4"}).Error; err != nil {
		t.Fatal(err)
	}
	var seed, leech *btQBProbe
	t.Cleanup(func() {
		for _, probe := range []*btQBProbe{seed, leech} {
			if probe == nil {
				continue
			}
			select {
			case <-probe.done:
			default:
				t.Error("owned qB process still running")
			}
			if _, err := os.Stat(probe.root); !errors.Is(err, os.ErrNotExist) {
				t.Error("owned profile not removed", err)
			}
			t.Logf("owned qB cleanup verified pid=%d", probe.process.Process.Pid)
		}
	})
	seed = btStartQBProbe(t, "e2e_seed")
	leech = btStartQBProbe(t, "e2e_leech")
	seedRoot := filepath.Join(seed.root, "content")
	if err := os.MkdirAll(filepath.Join(seedRoot, manifest.Name), 0700); err != nil {
		t.Fatal(err)
	}
	seedRoot, err = filepath.EvalSymlinks(seedRoot)
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		if err := os.WriteFile(filepath.Join(seedRoot, manifest.Name, name), payloads[i], 0600); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := seed.client.QBAddStopped(context.Background(), btclient.QBAddOptions{Torrent: torrent, SavePath: seedRoot, OwnerTag: "synthetic-seed"})
	if err != nil {
		t.Fatal(err)
	}
	btWaitQB(t, seed, hash, func(row btclient.QBTorrent) bool { return btStopped(row.State) })
	if err := seed.client.QBRun(context.Background(), hash, true); err != nil {
		t.Fatal(err)
	}
	btWaitQB(t, seed, hash, func(row btclient.QBTorrent) bool {
		return row.Progress == 1 && (row.State == "uploading" || row.State == "stalledUP")
	})
	var proxyCalls atomic.Int32
	var prowlarr *httptest.Server
	prowlarr = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "synthetic-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/indexer":
			json.NewEncoder(w).Encode([]map[string]any{{"id": 1, "name": "Synthetic", "protocol": "torrent", "enable": true}})
		case "/api/v1/search":
			json.NewEncoder(w).Encode([]map[string]any{{"title": "Synthetic season", "indexerId": 1, "indexer": "Synthetic", "protocol": "torrent", "size": len(joined), "downloadUrl": prowlarr.URL + "/1/download?link=synthetic"}})
		case "/1/download":
			proxyCalls.Add(1)
			w.Header().Set("Content-Type", "application/x-bittorrent")
			w.Write(torrent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(prowlarr.Close)
	root, err := filepath.EvalSymlinks(filepath.Join(leech.root, "downloads"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	store := btMemorySecrets{}
	videos := NewVideoService(NewMediaProbeService())
	policy := NewBTPathPolicy(nil)
	videos.SetBTPathPolicy(policy)
	var importer *BTImporter
	service := NewBTService(t.TempDir(), BTServiceDeps{Credentials: func(context.Context, *gorm.DB) (*BTCredentialManager, error) {
		return NewBTCredentialManager(store), nil
	}, EnsureNamespace: WriteBTNamespaceMarker, ValidateRoot: ValidateBTDownloadRoot, ImportFile: func(ctx context.Context, taskID, fileID string) error {
		_, err := importer.Import(ctx, taskID, fileID)
		return err
	}})
	importer = NewBTImporter(videos, policy, service)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if _, err := service.Connection().Configure(ctx, BTConnectionRequest{ProwlarrBase: prowlarr.URL, QBBase: leech.base, DownloadRoot: root, Enabled: true, ProwlarrKey: BTSecretChange{Operation: "replace", Value: "synthetic-key"}}); err != nil {
		t.Fatal(err)
	}
	found, err := service.Search(ctx, "Synthetic", nil)
	if err != nil || len(found.Resources) != 1 {
		t.Fatalf("search: %v %+v", err, found)
	}
	draft, err := service.Preview(ctx, found.Resources[0].Token)
	if err != nil {
		t.Fatal(err)
	}
	if proxyCalls.Load() != 1 || len(draft.Files) != 3 {
		t.Fatal("real proxy/manifest path not exercised")
	}
	before, err := leech.client.QBTorrents(ctx, hash)
	if err != nil || len(before) != 0 {
		t.Fatal("preview created remote torrent", err)
	}
	var selectedID string
	for _, file := range draft.Files {
		if file.Path == "synthetic-season/chosen.mp4" {
			selectedID = file.ID
		}
	}
	if selectedID == "" {
		t.Fatal("selected video absent")
	}
	submitted, err := service.Submit(ctx, BTSubmitRequest{DraftID: draft.ID, ExpectedRevision: draft.Revision, SelectedFileIDs: []string{selectedID}})
	if err != nil {
		t.Fatal(err)
	}
	service.Start(ctx)
	wait := func(label string, accept func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !accept() {
			if ctx.Err() != nil || time.Now().After(deadline) {
				snapshot, _ := service.Get(context.Background(), submitted.ID)
				remote, _ := leech.client.QBTorrents(context.Background(), hash)
				remoteFiles, _ := leech.client.QBFiles(context.Background(), hash)
				t.Logf("failed task stage=%s pending=%s error=%s revision=%d remote=%+v files=%+v", snapshot.Stage, snapshot.PendingAction, snapshot.ErrorCode, snapshot.Revision, remote, remoteFiles)
				t.Fatalf("%s timed out", label)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	wait("priority and start", func() bool {
		if err := service.Poll(ctx); err != nil {
			t.Fatal(err)
		}
		v, e := service.Get(ctx, submitted.ID)
		return e == nil && v.Stage == "active"
	})
	remoteFiles, err := leech.client.QBFiles(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range remoteFiles {
		if (file.Priority > 0) != (file.Name == "synthetic-season/chosen.mp4") {
			t.Fatalf("priority mismatch %+v", file)
		}
	}
	properties, err := leech.client.QBProperties(ctx, hash)
	if err != nil || properties.TotalDownloaded != 0 {
		t.Fatal("payload before explicit loopback peer", err)
	}
	leech.post(t, "/api/v2/torrents/addPeers", url.Values{"hashes": {hash}, "peers": {fmt.Sprintf("127.0.0.1:%d", seed.peerPort)}})
	var final BTTaskView
	wait("selected video import", func() bool {
		if err := service.Poll(ctx); err != nil {
			t.Fatal(err)
		}
		var e error
		final, e = service.Get(ctx, submitted.ID)
		return e == nil && final.ImportedCount == 1
	})
	var rows []models.Video
	if err := database.DB.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	selectedPath := filepath.Join(draft.SavePath, "synthetic-season", "chosen.mp4")
	if len(rows) != 1 || rows[0].Path != selectedPath || rows[0].Name != "chosen.mp4" || rows[0].IsStale || rows[0].Duration <= 0 {
		t.Fatalf("unexpected library: %+v", rows)
	}
	actual, err := os.ReadFile(selectedPath)
	if err != nil || sha256.Sum256(actual) != sha256.Sum256(videoBytes) {
		t.Fatal("import changed payload", err)
	}
	for _, file := range final.Files {
		if file.ID != selectedID && (file.Selected || file.ImportState == "imported" || file.VideoID != nil) {
			t.Fatal("unselected file imported", file.Path)
		}
	}
	unselectedPath := filepath.Join(draft.SavePath, "synthetic-season", "unselected.mp4")
	if _, err := videos.AddVideo(unselectedPath); !errors.Is(err, ErrBTPending) {
		t.Fatalf("ordinary Add bypassed unselected protection: %v", err)
	}
	scan := videos.SyncAffectedDirectories([]models.ScanDirectory{{Path: root}}, []string{draft.SavePath})
	if len(scan.Errors) != 0 || scan.Added != 0 {
		t.Fatalf("scan admitted unselected payload: %+v", scan)
	}
	for i := 0; i < 2; i++ {
		if err := service.Poll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := database.DB.Model(&models.Video{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("repeat import duplicated row", count, err)
	}
	// Exercise explicit user controls through the coordinator, then independently
	// read qB's state. These commands must preserve the original file selection.
	for _, action := range []string{"stop", "start"} {
		current, err := service.Get(ctx, submitted.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Control(ctx, BTControlRequest{TaskID: current.ID, ExpectedRevision: current.Revision, Action: action}); err != nil {
			t.Fatal(action, err)
		}
		var observed btclient.QBTorrent
		wait("user control "+action, func() bool {
			if err := service.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			current, err := service.Get(ctx, submitted.ID)
			if err != nil || current.Stage != "active" {
				return false
			}
			remote, err := leech.client.QBTorrents(ctx, hash)
			if err != nil || len(remote) != 1 {
				return false
			}
			observed = remote[0]
			if action == "stop" {
				return btStopped(observed.State)
			}
			return observed.State == "uploading" || observed.State == "stalledUP" || observed.State == "forcedUP"
		})
		t.Logf("production Control(%s) independently observed qB=%s", action, observed.State)
	}
	remoteFiles, err = leech.client.QBFiles(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range remoteFiles {
		if (file.Priority > 0) != (file.Name == "synthetic-season/chosen.mp4") {
			t.Fatalf("controls changed selection %+v", file)
		}
	}
	if err := service.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	btWaitQB(t, leech, hash, func(row btclient.QBTorrent) bool {
		return row.State == "uploading" || row.State == "stalledUP" || row.State == "forcedUP"
	})
	time.Sleep(300 * time.Millisecond)
	running, err := leech.client.QBTorrents(ctx, hash)
	if err != nil || len(running) != 1 || btStopped(running[0].State) {
		t.Fatal("local Stop stopped external seeding", err)
	}
	actual, err = os.ReadFile(selectedPath)
	if err != nil || sha256.Sum256(actual) != sha256.Sum256(videoBytes) {
		t.Fatal("payload changed after local stop", err)
	}
	t.Logf("production search/proxy/select/import verified: video_id=%d selected=%s bytes=%d sha256=%x qB_after_Stop=%s", rows[0].ID, final.Files[0].Path, len(actual), sha256.Sum256(actual), running[0].State)
}
