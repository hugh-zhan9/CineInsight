package services

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// worker 会话协议测试：用测试二进制自身扮演 sidecar（helper process），不依赖 Python。
const sceneWorkerHelperEnv = "CINEINSIGHT_SCENE_WORKER_HELPER"

func TestSceneWorkerHelperProcess(t *testing.T) {
	mode := os.Getenv(sceneWorkerHelperEnv)
	if mode == "" {
		return
	}
	defer os.Exit(0)
	out := bufio.NewWriter(os.Stdout)
	emit := func(payload any) {
		data, _ := json.Marshal(payload)
		_, _ = out.Write(append(data, '\n'))
		_ = out.Flush()
	}
	model := SceneLocalModelID
	if mode == "wrongmodel" {
		model = "other@1"
	}
	_, _ = out.WriteString("onnxruntime noise on stdout\n")
	emit(map[string]any{"ready": true, "model": model})
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var request struct {
			ID    int64    `json:"id"`
			Op    string   `json:"op"`
			Paths []string `json:"paths"`
			Texts []string `json:"texts"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &request)
		switch mode {
		case "hang":
			continue
		case "exit":
			return
		case "mismatch":
			emit(map[string]any{"id": request.ID + 100, "embeddings": []string{}})
			continue
		case "huge":
			emit(map[string]any{"id": request.ID, "embeddings": []string{strings.Repeat("A", 2<<20)}})
			continue
		}
		if request.Op == "bogus" {
			emit(map[string]any{"id": request.ID, "error": "unknown_op"})
			continue
		}
		count := len(request.Paths) + len(request.Texts)
		embeddings := make([]string, 0, count)
		for index := 0; index < count; index++ {
			blob := make([]byte, sceneEmbeddingDims*4)
			binary.LittleEndian.PutUint32(blob, math.Float32bits(float32(request.ID)+float32(index)/10))
			embeddings = append(embeddings, base64.StdEncoding.EncodeToString(blob))
		}
		_, _ = out.WriteString("stray log line\n")
		emit(map[string]any{"id": request.ID, "embeddings": embeddings})
	}
}

func startHelperSceneWorker(t *testing.T, mode string) (sceneEmbedder, error) {
	t.Helper()
	env := append(os.Environ(), sceneWorkerHelperEnv+"="+mode)
	return startSceneWorkerProcess(context.Background(), os.Args[0], "-test.run=^TestSceneWorkerHelperProcess$", env)
}

func TestSceneWorkerProcessRoundTripsEmbeddings(t *testing.T) {
	session, err := startHelperSceneWorker(t, "ok")
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer session.Close()
	images, err := session.EmbedImages(context.Background(), []string{"/tmp/a.jpg", "/tmp/b.jpg"})
	if err != nil || len(images) != 2 || len(images[0]) != sceneEmbeddingDims {
		t.Fatalf("图片向量不对: %v %d", err, len(images))
	}
	if images[0][0] != 1 || math.Abs(float64(images[1][0])-1.1) > 1e-6 {
		t.Fatalf("向量应按顺序对应请求 id=1: %v %v", images[0][0], images[1][0])
	}
	texts, err := session.EmbedTexts(context.Background(), []string{"雨夜"})
	if err != nil || len(texts) != 1 || texts[0][0] != 2 {
		t.Fatalf("第二个请求 id 应为 2: %v %v", err, texts)
	}
	if _, err := session.EmbedImages(context.Background(), make([]string, 33)); err == nil {
		t.Fatal("超过 32 张应在 Go 侧拒绝")
	}
	if _, err := session.EmbedTexts(context.Background(), make([]string, 9)); err == nil {
		t.Fatal("超过 8 条应在 Go 侧拒绝")
	}
	process := session.(*sceneWorkerProcess)
	if _, err := process.request(context.Background(), map[string]any{"op": "bogus"}, 1); err == nil || errors.Is(err, errSceneWorkerGone) {
		t.Fatalf("单条错误不应让会话作废: %v", err)
	}
	if process.Gone() {
		t.Fatal("单条错误后会话仍应可用")
	}
}

func TestSceneWorkerProcessFailureModes(t *testing.T) {
	if _, err := startHelperSceneWorker(t, "wrongmodel"); !errors.Is(err, errSceneWorkerGone) {
		t.Fatalf("模型标识不符应启动失败: %v", err)
	}
	session, err := startHelperSceneWorker(t, "mismatch")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.EmbedTexts(context.Background(), []string{"x"}); !errors.Is(err, errSceneWorkerGone) {
		t.Fatalf("应答错位应让会话作废: %v", err)
	}
	_ = session.Close()

	session, err = startHelperSceneWorker(t, "exit")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.EmbedTexts(context.Background(), []string{"x"}); !errors.Is(err, errSceneWorkerGone) {
		t.Fatalf("进程退出应报 gone: %v", err)
	}
	_ = session.Close()

	previous := sceneWorkerRequestTimeout
	sceneWorkerRequestTimeout = 200 * time.Millisecond
	defer func() { sceneWorkerRequestTimeout = previous }()
	session, err = startHelperSceneWorker(t, "hang")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := session.EmbedTexts(ctx, []string{"x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应返回 ctx 错误: %v", err)
	}
	if !session.(*sceneWorkerProcess).Gone() {
		t.Fatal("取消后会话应作废（迟到的应答会错位）")
	}
	_ = session.Close()
	session, _ = startHelperSceneWorker(t, "hang")
	if _, err := session.EmbedTexts(context.Background(), []string{"x"}); !errors.Is(err, errSceneWorkerGone) {
		t.Fatalf("超时应让会话作废: %v", err)
	}
	_ = session.Close()
}

// fakeSession 记录 Close 次数，可设置为作废。
type fakeSession struct {
	mu     sync.Mutex
	closed int
	gone   bool
	id     int
}

func (f *fakeSession) EmbedImages(context.Context, []string) ([][]float32, error) {
	return [][]float32{make([]float32, sceneEmbeddingDims)}, nil
}
func (f *fakeSession) EmbedTexts(context.Context, []string) ([][]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gone {
		return nil, fmt.Errorf("%w: exited", errSceneWorkerGone)
	}
	return [][]float32{{float32(f.id)}}, nil
}
func (f *fakeSession) Close() error {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
	return nil
}
func (f *fakeSession) closedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// 常驻会话：复用、空闲超时后关闭、作废后重建、Close 后拒绝使用。
func TestSceneWorkerHostReusesIdlesAndRebuilds(t *testing.T) {
	var mu sync.Mutex
	sessions := []*fakeSession{}
	host := newSceneWorkerHost(func(context.Context) (sceneEmbedder, error) {
		mu.Lock()
		defer mu.Unlock()
		session := &fakeSession{id: len(sessions) + 1}
		sessions = append(sessions, session)
		return session, nil
	})
	host.idle = 50 * time.Millisecond
	for range 3 {
		vectors, err := host.EmbedTexts(context.Background(), []string{"x"})
		if err != nil || vectors[0][0] != 1 {
			t.Fatalf("应复用第一个会话: %v %v", vectors, err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for sessions[0].closedCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sessions[0].closedCount() != 1 {
		t.Fatal("空闲超时后应关闭会话")
	}
	if vectors, _ := host.EmbedTexts(context.Background(), []string{"x"}); vectors[0][0] != 2 {
		t.Fatalf("关闭后再次使用应拉起新会话: %v", vectors)
	}
	sessions[1].mu.Lock()
	sessions[1].gone = true
	sessions[1].mu.Unlock()
	if _, err := host.EmbedTexts(context.Background(), []string{"x"}); !errors.Is(err, errSceneWorkerGone) {
		t.Fatalf("作废会话应报 gone: %v", err)
	}
	if vectors, _ := host.EmbedTexts(context.Background(), []string{"x"}); vectors[0][0] != 3 {
		t.Fatalf("作废后应重建会话: %v", vectors)
	}
	host.Close()
	if _, err := host.EmbedTexts(context.Background(), []string{"x"}); !errors.Is(err, errSceneWorkerGone) {
		t.Fatalf("Close 之后应拒绝使用: %v", err)
	}
	if sessions[2].closedCount() != 1 {
		t.Fatal("Close 应关闭当前会话")
	}
}
