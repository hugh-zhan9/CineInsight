package services

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func decodeProxiedTarget(t *testing.T, line string) string {
	t.Helper()
	index := strings.Index(line, "/seg/")
	if index < 0 {
		t.Fatalf("这一行没有被改写成代理地址：%s", line)
	}
	// 形如 /seg/<base64><.ext>。标签行后面还跟着别的属性（IV=… 之类），
	// 先切到引号或逗号为止，再去掉扩展名——base64url 里不会出现点号。
	encoded := strings.TrimSpace(line[index+len("/seg/"):])
	if cut := strings.IndexAny(encoded, `",;`); cut >= 0 {
		encoded = encoded[:cut]
	}
	if dot := strings.LastIndex(encoded, "."); dot > 0 {
		encoded = encoded[:dot]
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("代理地址解不开：%v（原始行 %s）", err, line)
	}
	return string(raw)
}

func TestRewritePlaylistCoversSegmentsKeysAndMaps(t *testing.T) {
	playlist := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\",IV=0x00",
		"#EXT-X-MAP:URI=\"init.mp4\"",
		"#EXTINF:10,",
		"seg0.ts",
		"#EXTINF:10,",
		"https://other.cdn.example/seg1.ts",
	}, "\n")

	out := RewritePlaylistThroughProxy(playlist, "https://cdn.example/v/index.m3u8", "/bridge/v1/stream/sid")
	lines := strings.Split(out, "\n")

	// 分片行、密钥、初始化段都要改写——漏掉任何一类，播放器就会绕过代理
	// 直接去源站，于是又变回没有请求头的老样子。
	if got := decodeProxiedTarget(t, lines[1]); got != "https://cdn.example/v/key.bin" {
		t.Fatalf("密钥地址没改对：%s", got)
	}
	if got := decodeProxiedTarget(t, lines[2]); got != "https://cdn.example/v/init.mp4" {
		t.Fatalf("初始化段没改对：%s", got)
	}
	if got := decodeProxiedTarget(t, lines[4]); got != "https://cdn.example/v/seg0.ts" {
		t.Fatalf("相对分片没按基址解析：%s", got)
	}
	// 跨域的绝对地址同样要走代理
	if got := decodeProxiedTarget(t, lines[6]); got != "https://other.cdn.example/seg1.ts" {
		t.Fatalf("绝对地址分片没改对：%s", got)
	}
	// 非地址的标签行原样保留
	if lines[0] != "#EXTM3U" || !strings.Contains(lines[1], "IV=0x00") {
		t.Fatalf("标签的其余属性被破坏了：%v", lines[:2])
	}
}

func TestStreamProxyServesAndRewrites(t *testing.T) {
	var gotReferer string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("Referer")
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:10,\nseg0.ts\n"))
			return
		}
		_, _ = w.Write([]byte("segment-bytes"))
	}))
	defer origin.Close()

	proxy := NewStreamProxy()
	id, err := proxy.CreateSession(origin.URL+"/v/index.m3u8", map[string]string{"Referer": "https://page/x"})
	if err != nil {
		t.Fatalf("建会话失败：%v", err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeStream(w, r, "/bridge/v1/stream")
	})

	// 取播放列表：源站应当收到 Referer，回给播放器的列表应当已经改写
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/bridge/v1/stream/"+id+"/index.m3u8", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("取播放列表失败：%d %s", recorder.Code, recorder.Body.String())
	}
	if gotReferer != "https://page/x" {
		t.Fatalf("代理没有把 Referer 带给源站，实际 %q", gotReferer)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "seg0.ts\n") && !strings.Contains(body, "/seg/") {
		t.Fatalf("播放列表没有被改写：%s", body)
	}

	// 按改写后的地址取分片，同样要带上 Referer
	var segLine string
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "/seg/") {
			segLine = strings.TrimSpace(line)
		}
	}
	if segLine == "" {
		t.Fatalf("没找到改写后的分片行：%s", body)
	}
	gotReferer = ""
	segRecorder := httptest.NewRecorder()
	handler.ServeHTTP(segRecorder, httptest.NewRequest(http.MethodGet, segLine, nil))
	if segRecorder.Code != http.StatusOK || segRecorder.Body.String() != "segment-bytes" {
		t.Fatalf("取分片失败：%d %s", segRecorder.Code, segRecorder.Body.String())
	}
	if gotReferer != "https://page/x" {
		t.Fatalf("取分片时没带 Referer，实际 %q", gotReferer)
	}
}

func TestStreamProxyRejectsBadSessionAndTarget(t *testing.T) {
	proxy := NewStreamProxy()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeStream(w, r, "/bridge/v1/stream")
	})

	// 会话 ID 是随机秘密，猜不到就取不到东西
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/bridge/v1/stream/nonexistent/index.m3u8", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("不存在的会话应当 404，实际 %d", recorder.Code)
	}

	id, _ := proxy.CreateSession("https://a.example/v.m3u8", nil)
	// 会话存在也不能拿它去取任意协议：file:// 会变成"读本机文件"
	evil := base64.RawURLEncoding.EncodeToString([]byte("file:///etc/passwd"))
	bad := httptest.NewRecorder()
	handler.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/bridge/v1/stream/"+id+"/seg/"+evil+".ts", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("file:// 目标应当 400，实际 %d", bad.Code)
	}

	if _, err := proxy.CreateSession("file:///etc/passwd", nil); err == nil {
		t.Fatal("非 http 地址不该能建会话")
	}
}

// IINA 靠扩展名判断"这是不是能播的东西"：入口地址结尾是 /root 时它连请求都不发，
// 代理侧一条访问日志都没有。所以入口必须带一个播放器认得的扩展名。
func TestStreamProxyRootNameCarriesPlayableExtension(t *testing.T) {
	proxy := NewStreamProxy()
	for target, want := range map[string]string{
		"https://a/x/index.m3u8?auth=1": "index.m3u8",
		"https://a/x/movie.mp4":         "media.mp4",
		"https://a/x/movie.mkv":         "media.mkv",
		// 认不出扩展名时按 HLS 处理，绝不返回一个没有扩展名的名字
		"https://a/play/12345": "index.m3u8",
	} {
		id, err := proxy.CreateSession(target, nil)
		if err != nil {
			t.Fatalf("%s 建会话失败：%v", target, err)
		}
		if got := proxy.RootName(id); got != want {
			t.Fatalf("%s 的入口名应当是 %s，实际 %s", target, want, got)
		}
		if !strings.Contains(proxy.RootName(id), ".") {
			t.Fatalf("入口名必须带扩展名：%s", proxy.RootName(id))
		}
	}
}

// 经过代理之后，播放器还能不能知道总时长、能不能跳转。
//
// 这条用真的 HLS 素材验证：改写如果碰坏了 EXTINF 或 EXT-X-ENDLIST，
// 播放器就会把它当成直播流，进度条直接不可拖。
func TestStreamProxyKeepsPlaylistSeekable(t *testing.T) {
	ffmpeg, err := findBrowserDownloadFFmpeg()
	if err != nil {
		t.Skipf("没装 ffmpeg，跳过：%v", err)
	}
	ffprobe := strings.TrimSuffix(ffmpeg, "ffmpeg") + "ffprobe"
	if _, statErr := os.Stat(ffprobe); statErr != nil {
		t.Skipf("没有 ffprobe，跳过")
	}

	dir := t.TempDir()
	// 造一条 6 秒的 HLS VOD
	gen := exec.Command(ffmpeg, "-nostdin", "-v", "error",
		"-f", "lavfi", "-i", "testsrc=size=128x128:rate=10:duration=6",
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-hls_time", "2", "-hls_playlist_type", "vod",
		"-hls_segment_filename", filepath.Join(dir, "seg%d.ts"),
		filepath.Join(dir, "index.m3u8"))
	if out, genErr := gen.CombinedOutput(); genErr != nil {
		t.Skipf("造 HLS 素材失败：%v %s", genErr, string(out))
	}

	origin := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer origin.Close()

	proxy := NewStreamProxy()
	id, err := proxy.CreateSession(origin.URL+"/index.m3u8", map[string]string{"Referer": "https://page/x"})
	if err != nil {
		t.Fatalf("建会话失败：%v", err)
	}
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeStream(w, r, "/bridge/v1/stream")
	}))
	defer bridge.Close()

	playURL := fmt.Sprintf("%s/bridge/v1/stream/%s/%s", bridge.URL, id, proxy.RootName(id))

	// 先确认改写后的播放列表没把可跳转的标记弄丢
	body, err := http.Get(playURL)
	if err != nil {
		t.Fatalf("取代理播放列表失败：%v", err)
	}
	defer body.Body.Close()
	raw, _ := io.ReadAll(body.Body)
	playlist := string(raw)
	if !strings.Contains(playlist, "#EXT-X-ENDLIST") {
		t.Fatalf("改写把 ENDLIST 弄丢了，播放器会当成直播流而禁掉进度条：\n%s", playlist)
	}
	if !strings.Contains(playlist, "#EXTINF:") {
		t.Fatalf("改写把 EXTINF 弄丢了，播放器算不出时长：\n%s", playlist)
	}

	// 最终判据：ffprobe 经代理能不能读出总时长
	probe := exec.Command(ffprobe, "-v", "error",
		"-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", playURL)
	out, probeErr := probe.CombinedOutput()
	if probeErr != nil {
		t.Fatalf("ffprobe 读不了代理地址：%v %s", probeErr, string(out))
	}
	duration := strings.TrimSpace(string(out))
	if duration == "" || strings.HasPrefix(duration, "N/A") {
		t.Fatalf("经代理之后读不出总时长（进度条会不可拖）：%q", duration)
	}
	t.Logf("经代理读到的总时长 = %s 秒", duration)
}
