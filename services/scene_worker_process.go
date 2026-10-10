package services

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// sceneWorkerMaxImages / sceneWorkerMaxTexts 是协议规定的单次请求上限。
	sceneWorkerMaxImages = 32
	sceneWorkerMaxTexts  = 8
	// sceneWorkerIdleTimeout：检索用的常驻会话空闲这么久后关闭（合同「本地运行时」）。
	sceneWorkerIdleTimeout = 5 * time.Minute
)

var (
	// sceneWorkerStartupTimeout 是等 worker 加载模型的上限；sceneWorkerRequestTimeout 是
	// 单个请求（≤32 张 224×224）的上限。变量而非常量：协议测试要把它们调短。
	sceneWorkerStartupTimeout = 3 * time.Minute
	sceneWorkerRequestTimeout = 2 * time.Minute
	// errSceneWorkerGone 表示 sidecar 进程已不可用，会话必须重建。
	errSceneWorkerGone = errors.New("scene_worker_gone")
	// errSceneWorkerStartup 包住"拉起 worker 失败"：建索引据此整轮停止，而不是逐个视频重试（M-4）。
	errSceneWorkerStartup = errors.New("scene_worker_startup_failed")
	// sceneWorkerMaxLineBytes 是 worker 单行输出的上限：32 个向量约 90 KB，超过上限按协议错误处理（M-7）。
	sceneWorkerMaxLineBytes = 1 << 20
)

// sceneEmbedder 是 worker 会话的抽象：测试里用可控向量的替身。
type sceneEmbedder interface {
	EmbedImages(ctx context.Context, paths []string) ([][]float32, error)
	EmbedTexts(ctx context.Context, texts []string) ([][]float32, error)
	Close() error
}

// sceneWorkerProcess 是常驻 Python sidecar 的一问一答会话（仿 faceWorkerProcess）。
type sceneWorkerProcess struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	lines    chan sceneWorkerLine
	failed   chan error
	stderr   *boundedLogBuffer
	done     chan struct{}
	closeOne sync.Once

	mu     sync.Mutex
	nextID int64
	closed bool
	gone   bool
}

type sceneWorkerLine struct {
	Ready      bool            `json:"ready"`
	Model      string          `json:"model"`
	ID         json.RawMessage `json:"id"`
	Error      string          `json:"error"`
	Embeddings []string        `json:"embeddings"`
}

// startSceneWorkerProcess 启动 sidecar 并等 ready 行；模型标识对不上同样判失败。
func startSceneWorkerProcess(ctx context.Context, python, script string, env []string) (sceneEmbedder, error) {
	// 不用 CommandContext：生命周期由 Close 管（关 stdin 让它自己退，超时再 kill）。
	cmd := exec.Command(python, script)
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &boundedLogBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: start", errSceneWorkerGone)
	}
	session := &sceneWorkerProcess{
		cmd: cmd, stdin: stdin, stderr: stderr,
		lines:  make(chan sceneWorkerLine, 4),
		failed: make(chan error, 1),
		done:   make(chan struct{}),
	}
	go session.readLoop(stdout)

	timeout := time.NewTimer(sceneWorkerStartupTimeout)
	defer timeout.Stop()
	select {
	case line := <-session.lines:
		if line.Ready && line.Model == SceneLocalModelID {
			return session, nil
		}
		_ = session.Close()
		return nil, fmt.Errorf("%w: unexpected ready line", errSceneWorkerGone)
	case <-session.failed:
		_ = session.Close()
		// stderr 尾部只给日志的长度，不给内容：加载失败的诊断文本可能带模型路径。
		log.Printf("[Scene] worker 启动失败 stderr_bytes=%d", len(session.stderr.String()))
		return nil, fmt.Errorf("%w: exited during startup", errSceneWorkerGone)
	case <-timeout.C:
		_ = session.Close()
		return nil, fmt.Errorf("%w: startup timeout", errSceneWorkerGone)
	case <-ctx.Done():
		_ = session.Close()
		return nil, ctx.Err()
	}
}

func (p *sceneWorkerProcess) readLoop(stdout io.ReadCloser) {
	reader := bufio.NewReaderSize(stdout, 64*1024)
	defer stdout.Close()
	for {
		raw, err := readSceneWorkerLine(reader, sceneWorkerMaxLineBytes)
		if errors.Is(err, errSceneWorkerLineTooLong) {
			// 超长输出是协议错误：会话作废，交给 Close 收尾（M-7）。
			p.markGone()
			p.report(fmt.Errorf("%w: line exceeds %d bytes", errSceneWorkerGone, sceneWorkerMaxLineBytes))
			return
		}
		if trimmed := strings.TrimSpace(string(raw)); trimmed != "" {
			var line sceneWorkerLine
			if decodeErr := json.Unmarshal([]byte(trimmed), &line); decodeErr != nil {
				// 只丢这一行；真正的错位由请求 id 比对兜住。
				log.Printf("[Scene] 忽略 sidecar 的非 JSON 输出 bytes=%d", len(trimmed))
			} else if !p.deliver(line) {
				return
			}
		}
		if err != nil {
			if err == io.EOF {
				p.report(errSceneWorkerGone)
			} else {
				p.report(err)
			}
			return
		}
	}
}

// deliver 先尝试直接交给等待中的请求（通道有缓冲），只有交不出去时才看会话是否已关闭，
// 不会因为 done 已关闭就丢掉一个活请求的应答（M-1）。
func (p *sceneWorkerProcess) deliver(line sceneWorkerLine) bool {
	select {
	case p.lines <- line:
		return true
	default:
	}
	select {
	case p.lines <- line:
		return true
	case <-p.done:
		return false
	}
}

var errSceneWorkerLineTooLong = errors.New("scene_worker_line_too_long")

// readSceneWorkerLine 读一行（含结尾换行），超过 limit 字节就返回 errSceneWorkerLineTooLong。
func readSceneWorkerLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(line)+len(chunk) > limit {
			return nil, errSceneWorkerLineTooLong
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

// report 把致命错误交给等待方：通道有 1 格缓冲，非阻塞写入；已有一条在排队时丢弃。
func (p *sceneWorkerProcess) report(err error) {
	select {
	case p.failed <- err:
	default:
	}
}

func (p *sceneWorkerProcess) markGone() {
	p.mu.Lock()
	p.gone = true
	p.mu.Unlock()
}

// Gone 报告会话是否已作废（宿主据此丢弃并重建）。
func (p *sceneWorkerProcess) Gone() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gone || p.closed
}

func (p *sceneWorkerProcess) EmbedImages(ctx context.Context, paths []string) ([][]float32, error) {
	if len(paths) == 0 || len(paths) > sceneWorkerMaxImages {
		return nil, fmt.Errorf("scene_worker_batch_size: %d", len(paths))
	}
	return p.request(ctx, map[string]any{"op": "embed_images", "paths": paths}, len(paths))
}

func (p *sceneWorkerProcess) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 || len(texts) > sceneWorkerMaxTexts {
		return nil, fmt.Errorf("scene_worker_batch_size: %d", len(texts))
	}
	return p.request(ctx, map[string]any{"op": "embed_text", "texts": texts}, len(texts))
}

// request 发一行请求并等同 id 的应答。一问一答：管道缓冲区很小，批量灌入会双向阻塞。
// 超时、进程退出、ctx 取消或应答错位都会让会话作废——迟到的应答会与下一个请求错位。
func (p *sceneWorkerProcess) request(ctx context.Context, payload map[string]any, want int) ([][]float32, error) {
	p.mu.Lock()
	if p.closed || p.gone {
		p.mu.Unlock()
		return nil, errSceneWorkerGone
	}
	p.nextID++
	id := p.nextID
	p.mu.Unlock()

	payload["id"] = id
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if _, err := p.stdin.Write(append(data, '\n')); err != nil {
		p.markGone()
		return nil, fmt.Errorf("%w: write", errSceneWorkerGone)
	}
	timeout := time.NewTimer(sceneWorkerRequestTimeout)
	defer timeout.Stop()
	select {
	case line := <-p.lines:
		if strings.TrimSpace(string(line.ID)) != fmt.Sprint(id) {
			p.markGone()
			return nil, fmt.Errorf("%w: response id mismatch", errSceneWorkerGone)
		}
		if line.Error != "" {
			return nil, fmt.Errorf("scene_worker_error: %s", truncateLogSnippet(line.Error, 64))
		}
		if len(line.Embeddings) != want {
			return nil, fmt.Errorf("scene_worker_error: expected %d embeddings, got %d", want, len(line.Embeddings))
		}
		vectors := make([][]float32, 0, want)
		for _, encoded := range line.Embeddings {
			vector, err := decodeSceneEmbedding(encoded)
			if err != nil {
				return nil, err
			}
			vectors = append(vectors, vector)
		}
		return vectors, nil
	case <-p.failed:
		p.markGone()
		return nil, fmt.Errorf("%w: exited", errSceneWorkerGone)
	case <-p.done:
		// 会话被关闭（空闲回收不会碰在用的会话，这里只剩退出与作废）：立刻返回，不干等到超时。
		p.markGone()
		return nil, fmt.Errorf("%w: closed", errSceneWorkerGone)
	case <-timeout.C:
		p.markGone()
		return nil, fmt.Errorf("%w: request timeout", errSceneWorkerGone)
	case <-ctx.Done():
		p.markGone()
		return nil, ctx.Err()
	}
}

// Close 关 stdin 让 worker 自然退出；10 秒不退就 kill。
func (p *sceneWorkerProcess) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	p.closeOne.Do(func() { close(p.done) })
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		<-done
	}
	return nil
}

func decodeSceneEmbedding(encoded string) ([]float32, error) {
	blob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("scene_worker_error: undecodable embedding")
	}
	if len(blob) != sceneEmbeddingDims*4 {
		return nil, fmt.Errorf("scene_worker_error: embedding has %d bytes", len(blob))
	}
	vector := make([]float32, sceneEmbeddingDims)
	for index := range vector {
		vector[index] = math.Float32frombits(binary.LittleEndian.Uint32(blob[index*4:]))
	}
	return vector, nil
}

// sceneWorkerHost 持有唯一一个常驻 worker 会话，建索引与检索共用：同一时刻只有一个
// 请求在飞（一问一答），空闲 sceneWorkerIdleTimeout 后关闭进程，下次使用时重新拉起。
// 会话作废（超时、退出、取消）时丢弃，下一次使用重建。
type sceneWorkerHost struct {
	start func(ctx context.Context) (sceneEmbedder, error)
	idle  time.Duration
	// slot 是容量 1 的信号量：等待者能随 ctx 放弃，不会被一个长批次无限期挡住。
	slot chan struct{}

	mu         sync.Mutex
	session    sceneEmbedder
	timer      *time.Timer
	generation uint64
	stopped    bool
}

func newSceneWorkerHost(start func(ctx context.Context) (sceneEmbedder, error)) *sceneWorkerHost {
	return &sceneWorkerHost{start: start, idle: sceneWorkerIdleTimeout, slot: make(chan struct{}, 1)}
}

// newSceneRuntimeWorkerHost 用真实运行时拉起 worker。
func newSceneRuntimeWorkerHost(runtime *SceneRuntime) *sceneWorkerHost {
	return newSceneWorkerHost(func(ctx context.Context) (sceneEmbedder, error) {
		python, script, env, err := runtime.WorkerEnvironment()
		if err != nil {
			return nil, err
		}
		return startSceneWorkerProcess(ctx, python, script, env)
	})
}

func (h *sceneWorkerHost) EmbedImages(ctx context.Context, paths []string) ([][]float32, error) {
	var vectors [][]float32
	err := h.use(ctx, func(session sceneEmbedder) (err error) {
		vectors, err = session.EmbedImages(ctx, paths)
		return err
	})
	return vectors, err
}

func (h *sceneWorkerHost) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	var vectors [][]float32
	err := h.use(ctx, func(session sceneEmbedder) (err error) {
		vectors, err = session.EmbedTexts(ctx, texts)
		return err
	})
	return vectors, err
}

func (h *sceneWorkerHost) use(ctx context.Context, fn func(sceneEmbedder) error) error {
	select {
	case h.slot <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-h.slot }()

	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return errSceneWorkerGone
	}
	if h.timer != nil {
		h.timer.Stop()
	}
	// 使用期间把会话从宿主上摘下并推进代次：已经触发、正在排队等锁的 closeIdle 带的是旧代次，
	// 也看不到会话，不可能关掉正在用的进程（M-1）。用完再挂回去。
	session := h.session
	h.session = nil
	h.generation++
	h.mu.Unlock()

	if session == nil {
		started, err := h.start(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: %w", errSceneWorkerStartup, err)
		}
		session = started
	}
	err := fn(session)
	gone := errors.Is(err, errSceneWorkerGone) || ctx.Err() != nil
	if checker, ok := session.(interface{ Gone() bool }); ok && checker.Gone() {
		gone = true
	}

	h.mu.Lock()
	if gone || h.stopped {
		h.session = nil
		h.mu.Unlock()
		go session.Close()
		return err
	}
	h.session = session
	h.generation++
	generation := h.generation
	h.timer = time.AfterFunc(h.idle, func() { h.closeIdle(generation) })
	h.mu.Unlock()
	return err
}

func (h *sceneWorkerHost) closeIdle(generation uint64) {
	h.mu.Lock()
	if generation != h.generation || h.session == nil {
		h.mu.Unlock()
		return
	}
	session := h.session
	h.session = nil
	h.mu.Unlock()
	_ = session.Close()
}

// Close 在应用退出时关闭会话；之后的使用一律返回 errSceneWorkerGone。
func (h *sceneWorkerHost) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.stopped = true
	if h.timer != nil {
		h.timer.Stop()
	}
	session := h.session
	h.session = nil
	h.mu.Unlock()
	if session != nil {
		_ = session.Close()
	}
}
