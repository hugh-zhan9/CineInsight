package services

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/pbkdf2"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"video-master/models"
	"video-master/services/btclient"
	"video-master/services/btmanifest"
)

type btQBProbe struct {
	client     *btclient.Client
	base, root string
	peerPort   int
	process    *exec.Cmd
	done       chan struct{}
}

func btProbePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}
func btStartQBProbe(t *testing.T, kind string) *btQBProbe {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "ciqb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	name := "ci_pt_" + kind + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	dir := filepath.Join(root, "qBittorrent_"+name, "config")
	if os.MkdirAll(dir, 0700) != nil {
		t.Fatal("profile directory")
	}
	apiPort, peerPort := btProbePort(t), btProbePort(t)
	for peerPort == apiPort {
		peerPort = btProbePort(t)
	}
	download := filepath.Join(root, "downloads")
	if os.MkdirAll(download, 0700) != nil {
		t.Fatal("download directory")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		t.Fatal(err)
	}
	password := uuid.NewString()
	hash := pbkdf2.Key([]byte(password), salt, 100000, 64, sha512.New)
	passwordLine := "WebUI\\Username=ci_probe\nWebUI\\Password_PBKDF2=@ByteArray(" + base64.StdEncoding.EncodeToString(salt) + ":" + base64.StdEncoding.EncodeToString(hash) + ")\n"
	config := fmt.Sprintf("[Network]\nPortForwardingEnabled=false\n[LegalNotice]\nAccepted=true\n[Preferences]\nWebUI\\Enabled=true\nWebUI\\Address=127.0.0.1\nWebUI\\Port=%d\nWebUI\\LocalHostAuth=false\nWebUI\\UseUPnP=false\nGeneral\\CloseToTray=false\nGeneral\\CheckForUpdates=false\n[BitTorrent]\nSession\\DHTEnabled=false\nSession\\LSDEnabled=false\nSession\\PeXEnabled=false\nSession\\PortForwardingEnabled=false\nSession\\Interface=lo0\nSession\\InterfaceAddress=127.0.0.1\nSession\\Port=%d\nSession\\DefaultSavePath=%s\nSession\\TempPathEnabled=false\n", apiPort, peerPort, download)
	config = strings.Replace(config, "[Preferences]\n", "[Preferences]\n"+passwordLine, 1)
	if err := os.WriteFile(filepath.Join(dir, "qBittorrent.ini"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(root, "process.log"))
	if err != nil {
		t.Fatal(err)
	}
	processContext, stopProcess := context.WithTimeout(context.Background(), 75*time.Second)
	t.Cleanup(stopProcess)
	command := exec.CommandContext(processContext, "nice", "-n", "10", "/Applications/qBittorrent.app/Contents/MacOS/qbittorrent", "--profile="+root, "--configuration="+name, "--confirm-legal-notice", "--no-splash", "--webui-port="+strconv.Itoa(apiPort), "--torrenting-port="+strconv.Itoa(peerPort))
	command.Stdout = log
	command.Stderr = log
	if err := command.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done); log.Close() }()
	probe := &btQBProbe{base: fmt.Sprintf("http://127.0.0.1:%d", apiPort), root: root, peerPort: peerPort, process: command, done: done}
	probe.client, err = btclient.New(probe.base, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The WebUI port may have been stolen or reused. Terminate only the
		// process handle created above; cleanup must never write to an endpoint.
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
		probe.client.Close()
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = probe.client.CheckQBProfile(ctx)
		cancel()
		if err == nil {
			break
		}
		select {
		case <-done:
			t.Fatal("isolated client exited before ready")
		default:
		}
		if time.Now().After(deadline) {
			_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					relative, _ := filepath.Rel(root, path)
					t.Log("isolated diagnostic file:", relative)
					if strings.HasSuffix(path, ".log") {
						raw, _ := os.ReadFile(path)
						for _, line := range strings.Split(string(raw), "\n") {
							lower := strings.ToLower(line)
							if !strings.Contains(lower, "password") && !strings.Contains(line, "密码") && (strings.Contains(lower, "webui") || strings.Contains(lower, "web ui") || strings.Contains(lower, "failed") || strings.Contains(lower, "error")) {
								t.Log(line)
							}
						}
					}
					if strings.HasSuffix(path, ".ini") {
						raw, _ := os.ReadFile(path)
						for _, line := range strings.Split(string(raw), "\n") {
							for _, key := range []string{"WebUI\\Enabled=", "WebUI\\Port=", "WebUI\\Address=", "WebUI\\LocalHostAuth="} {
								if strings.HasPrefix(line, key) {
									t.Log(line)
								}
							}
						}
					}
				}
				return nil
			})
			t.Fatalf("isolated WebUI unavailable (%v)", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	output, err := exec.Command("lsof", "-nP", "-a", "-p", strconv.Itoa(command.Process.Pid), "-iTCP:"+strconv.Itoa(apiPort), "-sTCP:LISTEN").Output()
	if err != nil || !strings.Contains(string(output), "127.0.0.1:"+strconv.Itoa(apiPort)) {
		t.Fatal("WebUI listener is not owned by the isolated process")
	}
	var preferences map[string]any
	probe.getJSON(t, "/api/v2/app/preferences", &preferences)
	if port, ok := preferences["listen_port"].(float64); !ok || int(port) != peerPort {
		t.Fatal("isolated peer port not applied")
	}
	peerOutput, err := exec.Command("lsof", "-nP", "-a", "-p", strconv.Itoa(command.Process.Pid), "-iTCP:"+strconv.Itoa(peerPort), "-sTCP:LISTEN").Output()
	if err != nil || !strings.Contains(string(peerOutput), "127.0.0.1:"+strconv.Itoa(peerPort)) {
		t.Fatal("peer listener is not owned loopback")
	}

	for _, key := range []string{"dht", "lsd", "pex", "upnp", "web_ui_upnp"} {
		if value, ok := preferences[key].(bool); !ok || value {
			t.Fatalf("isolated network setting not disabled: %s", key)
		}
	}
	if path, _ := preferences["save_path"].(string); filepath.Clean(path) != filepath.Clean(download) {
		t.Fatal("isolated save path was not applied")
	}
	if address, _ := preferences["current_interface_address"].(string); address != "127.0.0.1" {
		t.Fatal("peer interface is not loopback")
	}
	build, err := probe.client.QBMetadataBuild(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("isolated %s pid=%d api_port=%d peer_port=%d libtorrent=%s", kind, command.Process.Pid, apiPort, peerPort, build.Libtorrent)
	return probe
}
func (p *btQBProbe) getJSON(t *testing.T, path string, result any) {
	t.Helper()
	client := http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(p.base + path)
	if err != nil {
		t.Fatal("probe read failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(result) != nil {
		t.Fatal("probe response invalid")
	}
}
func (p *btQBProbe) post(t *testing.T, path string, values url.Values) {
	t.Helper()
	client := http.Client{Timeout: 3 * time.Second}
	response, err := client.PostForm(p.base+path, values)
	if err != nil {
		t.Fatal("probe write failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("probe write rejected: %s %d", path, response.StatusCode)
	}
}
func btWaitQB(t *testing.T, probe *btQBProbe, hash string, accept func(btclient.QBTorrent) bool) btclient.QBTorrent {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		rows, err := probe.client.QBTorrents(context.Background(), hash)
		if err == nil && len(rows) == 1 && accept(rows[0]) {
			return rows[0]
		}
		if time.Now().After(deadline) {
			t.Fatal("isolated torrent state timed out")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
func TestBTIsolatedQBMetadataCapability(t *testing.T) {
	if os.Getenv("CINEINSIGHT_BT_QB_PROBE") != "1" {
		t.Skip("explicit isolated-profile test only")
	}
	seed, leech := btStartQBProbe(t, "seed"), btStartQBProbe(t, "leech")
	btProbeLayouts(t, leech)

	for _, stopped := range []bool{true, false} {
		name := fmt.Sprintf("probe-%v.bin", stopped)
		payload := []byte(strings.Repeat("local synthetic data", 1024))
		sum := sha1.Sum(payload)
		// Public synthetic metadata enables BEP 9; all discovery is disabled and
		// the only peer is explicitly supplied on loopback. No announce/webseed.
		data := btTestBencode(map[string]any{"info": map[string]any{"name": name, "length": len(payload), "piece length": 32768, "pieces": string(sum[:])}})
		manifest, err := btmanifest.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		hash := manifest.InfoHashV1
		seedRoot := filepath.Join(seed.root, fmt.Sprintf("seed-%v", stopped))
		os.MkdirAll(seedRoot, 0700)
		os.WriteFile(filepath.Join(seedRoot, name), payload, 0600)
		if _, err := seed.client.QBAddStopped(context.Background(), btclient.QBAddOptions{Torrent: data, SavePath: seedRoot, OwnerTag: "probe-seed"}); err != nil {
			t.Fatal(err)
		}
		btWaitQB(t, seed, hash, func(row btclient.QBTorrent) bool { return btStopped(row.State) })
		if err := seed.client.QBRun(context.Background(), hash, true); err != nil {
			t.Fatal(err)
		}
		btWaitQB(t, seed, hash, func(row btclient.QBTorrent) bool {
			return row.Progress == 1 && (row.State == "uploading" || row.State == "stalledUP" || row.State == "forcedUP")
		})
		magnet := "magnet:?xt=urn:btih:" + hash
		target := filepath.Join(leech.root, fmt.Sprintf("leech-%v", stopped))
		os.MkdirAll(target, 0700)
		if stopped {
			leech.post(t, "/api/v2/torrents/add", url.Values{"urls": {magnet}, "stopped": {strconv.FormatBool(stopped)}, "stopCondition": {"MetadataReceived"}, "autoTMM": {"false"}, "contentLayout": {"Original"}, "savepath": {target}, "useDownloadPath": {"false"}, "tags": {"isolated-probe"}})
		} else {
			actual, err := leech.client.QBAddStopped(context.Background(), btclient.QBAddOptions{Magnet: magnet, MetadataOnly: true, SavePath: target, OwnerTag: "isolated-probe"})
			if err != nil || actual != hash {
				t.Fatal("verified production metadata add failed", err)
			}
		}
		btWaitQB(t, leech, hash, func(row btclient.QBTorrent) bool { return true })
		if stopped {
			if err := leech.client.QBRun(context.Background(), hash, true); err != nil {
				t.Fatal(err)
			}
		}
		leech.post(t, "/api/v2/torrents/addPeers", url.Values{"hashes": {hash}, "peers": {fmt.Sprintf("127.0.0.1:%d", seed.peerPort)}})
		deadline := time.Now().Add(15 * time.Second)
		var properties btclient.QBProperties
		var files []btclient.QBFile
		var observed btclient.QBTorrent
		for {
			rows, _ := leech.client.QBTorrents(context.Background(), hash)
			if len(rows) == 1 {
				observed = rows[0]
			}
			properties, _ = leech.client.QBProperties(context.Background(), hash)
			files, _ = leech.client.QBFiles(context.Background(), hash)
			if len(files) > 0 && (btStopped(observed.State) || properties.TotalDownloaded > 0) {
				break
			}
			if time.Now().After(deadline) {
				var peers map[string]any
				leech.getJSON(t, "/api/v2/sync/torrentPeers?hash="+hash, &peers)
				t.Logf("isolated peer observation: %+v", peers)
				t.Fatalf("metadata capability timed out: state=%s downloaded=%d files=%d", observed.State, properties.TotalDownloaded, len(files))
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("MetadataReceived stopped=%v state=%s downloaded=%d files=%+v", stopped, observed.State, properties.TotalDownloaded, files)
		if !stopped {
			if !btStopped(observed.State) || properties.TotalDownloaded != 0 {
				t.Fatal("running MetadataReceived leaked payload")
			}
			for _, f := range files {
				if f.Progress != 0 {
					t.Fatal("metadata-only file has payload")
				}
			}
			disk, _ := os.ReadFile(filepath.Join(target, name))
			for _, value := range disk {
				if value != 0 {
					t.Fatal("metadata-only wrote payload content")
				}
			}
			// Observe another scheduling interval before accepting the no-payload result.
			time.Sleep(500 * time.Millisecond)
			later, err := leech.client.QBProperties(context.Background(), hash)
			if err != nil || later.TotalDownloaded != 0 {
				t.Fatal("payload appeared after metadata stop")
			}
		}
		if err := leech.client.QBDeleteMetadata(context.Background(), hash); err != nil {
			t.Fatal(err)
		}
	}

}

// All fixtures have no announce/webseed and only synthetic payload hashes.
func btProbeLayouts(t *testing.T, probe *btQBProbe) {
	a, b := []byte("aaaa"), []byte("bbbb")
	h1 := sha1.Sum(append(append([]byte{}, a...), b...))
	r1, r2 := sha256.Sum256(a), sha256.Sum256(b)
	leaf := func(root [32]byte) any {
		return map[string]any{"": map[string]any{"length": 4, "pieces root": string(root[:])}}
	}
	v1 := map[string]any{"name": "season", "private": 1, "piece length": 16384, "pieces": string(h1[:]), "files": []any{map[string]any{"length": 4, "path": []any{"one.mkv"}}, map[string]any{"length": 4, "path": []any{"two.txt"}}}}
	v2 := map[string]any{"name": "display", "private": 1, "meta version": 2, "piece length": 16384, "file tree": map[string]any{"one.mkv": leaf(r1), "sub": map[string]any{"two.txt": leaf(r2)}}}
	firstPiece := append(append([]byte{}, a...), make([]byte, 16380)...)
	p1, p2 := sha1.Sum(firstPiece), sha1.Sum(b)
	hybrid := map[string]any{"name": "season", "private": 1, "meta version": 2, "piece length": 16384, "pieces": string(p1[:]) + string(p2[:]), "files": []any{map[string]any{"length": 4, "path": []any{"one.mkv"}}, map[string]any{"length": 16380, "path": []any{".pad", "16380"}, "attr": "p"}, map[string]any{"length": 4, "path": []any{"two.txt"}}}, "file tree": map[string]any{"one.mkv": leaf(r1), "two.txt": leaf(r2)}}
	single := map[string]any{"name": "advisory", "private": 1, "meta version": 2, "piece length": 16384, "file tree": map[string]any{"one.mkv": leaf(r1)}}
	nested := map[string]any{"name": "display", "private": 1, "meta version": 2, "piece length": 16384, "file tree": map[string]any{"dir": map[string]any{"one.mkv": leaf(r1)}}}
	common := map[string]any{"name": "display", "private": 1, "meta version": 2, "piece length": 16384, "file tree": map[string]any{"display": map[string]any{"one.mkv": leaf(r1), "two.txt": leaf(r2)}}}
	for _, test := range []struct {
		name string
		info map[string]any
	}{{"v1", v1}, {"v2", v2}, {"v2-single", single}, {"v2-nested", nested}, {"v2-common-root", common}, {"hybrid", hybrid}} {
		t.Run(test.name, func(t *testing.T) {
			top := map[string]any{"info": test.info}
			if test.name != "v1" {
				top["piece layers"] = map[string]any{}
			}
			data := btTestBencode(top)
			m, err := btmanifest.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(probe.root, "layout-"+test.name)
			if os.Mkdir(root, 0700) != nil {
				t.Fatal("root")
			}
			root, err = filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			tag := "probe-" + test.name
			if _, err := probe.client.QBAddStopped(context.Background(), btclient.QBAddOptions{Torrent: data, SavePath: root, OwnerTag: tag}); err != nil {
				t.Fatal(err)
			}
			expected := models.BTTask{ID: uuid.NewString(), CanonicalRoot: root, OwnerTag: tag, InfoHashV1: btString(m.InfoHashV1), InfoHashV2: btString(m.InfoHashV2)}
			var observed btclient.QBTorrent
			for _, candidate := range btCandidateHashes(expected) {
				rows, _ := probe.client.QBTorrents(context.Background(), candidate)
				if len(rows) > 0 {
					observed = rows[0]
					break
				}
			}
			if observed.Hash == "" {
				t.Fatal("actual routing identity missing")
			}
			observed = btWaitQB(t, probe, observed.Hash, func(row btclient.QBTorrent) bool { return btStopped(row.State) })
			properties, err := probe.client.QBProperties(context.Background(), observed.Hash)
			if err != nil || !btHashMatches(expected, properties) {
				t.Fatal("full identity differs", err)
			}
			expected.QBHash = observed.Hash
			if _, _, err := btFindRemote(context.Background(), probe.client, expected); err != nil {
				t.Fatal("production ownership/alias verification failed", err)
			}

			rows, err := probe.client.QBFiles(context.Background(), observed.Hash)
			if err != nil {
				t.Fatal(err)
			}
			layout, err := btOriginalLayout(m)
			if err != nil {
				t.Fatal(err)
			}
			files := btManifestFiles(expected.ID, layout)
			mapping, err := btMapFiles(files, rows)
			t.Logf("%s full_v1=%s full_v2=%s routing_hash=%s files=%+v parsed=%+v", test.name, properties.InfoHashV1, properties.InfoHashV2, observed.Hash, rows, m.Files)
			if err != nil {
				t.Fatal("original paths not mapped", err)
			}
			indices := []int{}
			selected := -1
			for _, f := range files {
				if f.Padding {
					continue
				}
				indices = append(indices, mapping[f.ManifestFileID].Index)
				if selected < 0 {
					selected = mapping[f.ManifestFileID].Index
				}
			}
			if err := probe.client.QBPriority(context.Background(), observed.Hash, indices, 0); err != nil {
				t.Fatal(err)
			}
			if err := probe.client.QBPriority(context.Background(), observed.Hash, []int{selected}, 1); err != nil {
				t.Fatal(err)
			}
			rows, err = probe.client.QBFiles(context.Background(), observed.Hash)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range rows {
				if (f.Priority > 0) != (f.Index == selected) {
					t.Fatal("priority readback differs")
				}
			}
			if err := probe.client.QBRun(context.Background(), observed.Hash, true); err != nil {
				t.Fatal(err)
			}
			btWaitQB(t, probe, observed.Hash, func(row btclient.QBTorrent) bool { return !btStopped(row.State) })
			if err := probe.client.QBRun(context.Background(), observed.Hash, false); err != nil {
				t.Fatal(err)
			}
			btWaitQB(t, probe, observed.Hash, func(row btclient.QBTorrent) bool { return btStopped(row.State) })
		})
	}
}
