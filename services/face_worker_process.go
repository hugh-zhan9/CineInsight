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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// errFaceWorkerGone 表示 sidecar 进程已经不在了：继续喂图没有意义，
// 本轮标 interrupted，下次启动对账续跑（4.4.2）。
var errFaceWorkerGone = errors.New("人脸 sidecar 已退出")

// faceFrameMaxWidth 是喂给 sidecar 的帧宽上限。
// 比 AI 打标的 512 大：一个远景里的人脸在 512 宽上只有十几个像素，检测器根本看不见。
// 再往上也没用——检测器自己会把图缩到 640。
const faceFrameMaxWidth = 1280

const faceFrameQuality = 3

// faceWorkerProcess 是常驻的 Python sidecar 会话。
//
// 一问一答（写一行、读一行）而不是"先灌满再读"：管道缓冲区只有几十 KB，
// 一张脸的响应就有 3 KB，批量灌进去会双向阻塞死锁。模型加载的开销靠进程常驻
// 摊掉，这已经是主要成本。
type faceWorkerProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan faceWorkerLine
	failed chan error
	stderr *boundedLogBuffer
	// done 在 Close 时关闭：读取协程正卡在"把这一行交出去"时也能退出。
	// 少了它，一次超时之后的会话会留下一个永远阻塞在 channel 上的协程和一个
	// 打开的管道——一轮分析漏一个，跑一天就攒一把。
	done     chan struct{}
	closeOne sync.Once

	mu     sync.Mutex
	closed bool
	gone   bool
}

type faceWorkerLine struct {
	Ready bool             `json:"ready"`
	ID    string           `json:"id"`
	Error string           `json:"error"`
	Faces []faceWorkerFace `json:"faces"`
}

type faceWorkerFace struct {
	BBox      []float64 `json:"bbox"`
	Quality   float64   `json:"quality"`
	Embedding string    `json:"embedding"`
}

// boundedLogBuffer 只留最后 8 KB stderr：加载日志很长，出错时也只需要尾部。
type boundedLogBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *boundedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > 8192 {
		b.data = b.data[len(b.data)-8192:]
	}
	return len(p), nil
}

func (b *boundedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

// startFaceWorkerProcess 启动 sidecar 并等它把模型加载完（ready 行）。
func startFaceWorkerProcess(ctx context.Context, runtime *FaceRuntime) (FaceWorkerSession, error) {
	python, script, env, err := runtime.WorkerEnvironment()
	if err != nil {
		return nil, err
	}
	// 有意用 exec.Command 而不是 CommandContext：进程的生命周期由 Close 管
	// （关 stdin 让它自己退，10 秒不退再 kill）。挂在分析的 ctx 上会在取消的那一刻
	// SIGKILL，正在写的那一行响应就成了半截 JSON。
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
		return nil, fmt.Errorf("启动人脸 sidecar 失败: %w", err)
	}

	session := &faceWorkerProcess{
		cmd:    cmd,
		stdin:  stdin,
		lines:  make(chan faceWorkerLine, 8),
		failed: make(chan error, 1),
		stderr: stderr,
		done:   make(chan struct{}),
	}
	go session.readLoop(stdout)

	timeout := time.NewTimer(faceWorkerStartupTimeout)
	defer timeout.Stop()
	for {
		select {
		case line := <-session.lines:
			if line.Ready {
				return session, nil
			}
			// 加载阶段不该有别的响应；出现了就是协议对不上。
			_ = session.Close()
			return nil, fmt.Errorf("人脸 sidecar 启动应答异常")
		case err := <-session.failed:
			_ = session.Close()
			return nil, fmt.Errorf("人脸 sidecar 启动失败: %v（%s）", err, truncateLogSnippet(strings.TrimSpace(stderr.String()), 400))
		case <-timeout.C:
			_ = session.Close()
			return nil, errors.New("人脸 sidecar 启动超时")
		case <-ctx.Done():
			_ = session.Close()
			return nil, ctx.Err()
		}
	}
}

func (p *faceWorkerProcess) readLoop(stdout io.ReadCloser) {
	reader := bufio.NewReaderSize(stdout, 64*1024)
	defer stdout.Close()
	for {
		raw, err := reader.ReadString('\n')
		trimmed := strings.TrimSpace(raw)
		if trimmed != "" {
			var line faceWorkerLine
			if decodeErr := json.Unmarshal([]byte(trimmed), &line); decodeErr != nil {
				// 只丢这一行：Python 生态里随手往 stdout 打一行东西的库不少
				// （警告、进度条残留），为此把整个会话判死会白白中断一整轮分析。
				// 真正的错位由 Detect 的 id 比对兜住。
				log.Printf("[Face] 忽略 sidecar 的非 JSON 输出 bytes=%d", len(trimmed))
				continue
			}
			select {
			case p.lines <- line:
			case <-p.done:
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				p.report(err)
				return
			}
			p.report(errFaceWorkerGone)
			return
		}
	}
}

// report 把致命错误交给等待方；已经 Close 或已经有一条在排队时直接丢掉。
func (p *faceWorkerProcess) report(err error) {
	select {
	case p.failed <- err:
	case <-p.done:
	default:
	}
}

// Detect 送一张图并等这张图的结果。
func (p *faceWorkerProcess) Detect(ctx context.Context, requestID, imagePath string) ([]DetectedFace, error) {
	p.mu.Lock()
	if p.closed || p.gone {
		p.mu.Unlock()
		return nil, errFaceWorkerGone
	}
	p.mu.Unlock()

	request, err := json.Marshal(map[string]string{"id": requestID, "path": imagePath})
	if err != nil {
		return nil, err
	}
	if _, err := p.stdin.Write(append(request, '\n')); err != nil {
		p.markGone()
		return nil, fmt.Errorf("%w: %v", errFaceWorkerGone, err)
	}

	timeout := time.NewTimer(faceWorkerRequestTimeout)
	defer timeout.Stop()
	select {
	case line := <-p.lines:
		if line.ID != requestID {
			// 响应与请求对不上号，后面的每一条都会错位：只能判定会话已废。
			p.markGone()
			return nil, fmt.Errorf("%w: 应答与请求不匹配", errFaceWorkerGone)
		}
		if line.Error != "" {
			return nil, errors.New(line.Error)
		}
		return decodeFaceWorkerFaces(line.Faces)
	case err := <-p.failed:
		p.markGone()
		return nil, fmt.Errorf("%w: %v（%s）", errFaceWorkerGone, err, truncateLogSnippet(strings.TrimSpace(p.stderr.String()), 300))
	case <-timeout.C:
		p.markGone()
		return nil, fmt.Errorf("%w: 单张检测超时", errFaceWorkerGone)
	case <-ctx.Done():
		// 取消时这一张的响应还在路上：留着会和下一个请求的响应错位，
		// 会话就此作废（本轮反正要收尾，Close 会关掉进程）。
		p.markGone()
		return nil, ctx.Err()
	}
}

func (p *faceWorkerProcess) markGone() {
	p.mu.Lock()
	p.gone = true
	p.mu.Unlock()
}

// Close 关 stdin 让 worker 自然退出；超时就 kill。
func (p *faceWorkerProcess) Close() error {
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
		return nil
	case <-time.After(10 * time.Second):
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		<-done
		return nil
	}
}

func decodeFaceWorkerFaces(faces []faceWorkerFace) ([]DetectedFace, error) {
	out := make([]DetectedFace, 0, len(faces))
	for _, face := range faces {
		if len(face.BBox) != 4 {
			return nil, fmt.Errorf("sidecar 返回的 bbox 长度异常: %d", len(face.BBox))
		}
		vector, err := decodeFaceWorkerEmbedding(face.Embedding)
		if err != nil {
			return nil, err
		}
		out = append(out, DetectedFace{
			BBox:      faceBBox{X: face.BBox[0], Y: face.BBox[1], W: face.BBox[2], H: face.BBox[3]},
			Quality:   face.Quality,
			Embedding: vector,
		})
	}
	return out, nil
}

func decodeFaceWorkerEmbedding(encoded string) ([]float32, error) {
	blob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("sidecar 返回的向量无法解码: %w", err)
	}
	if len(blob) != faceEmbeddingBytes {
		return nil, fmt.Errorf("sidecar 返回的向量长度异常: %d 字节", len(blob))
	}
	vector := make([]float32, faceEmbeddingDims)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return vector, nil
}

// extractFaceFrames 按 AI 打标的位置规划抽 JPEG 到临时目录（D-018）。
//
// 复用 planAITaggingFrameCount / planAITaggingFramePositions：同一部片子两个任务
// 看的是同一批时间点，用户在待审面板里对得上。不复用打标的帧缓存——那是 data URL
// 而且用完就删（4.4.5）。
func extractFaceFrames(ctx context.Context, candidate faceMediaCandidate, dir string) ([]faceFrame, []string) {
	if strings.TrimSpace(candidate.Path) == "" {
		return nil, []string{"视频路径为空"}
	}
	ffmpeg := findMediaBinary("ffmpeg")
	if ffmpeg == "" {
		return nil, []string{"ffmpeg 不可用，无法抽帧"}
	}
	if _, err := os.Stat(candidate.Path); err != nil {
		return nil, []string{fmt.Sprintf("视频文件不可读: %v", err)}
	}
	count := planAITaggingFrameCount(candidate.Duration)
	positions := planAITaggingFramePositions(candidate.Duration, count)
	frames := make([]faceFrame, 0, len(positions))
	warnings := make([]string, 0)
	for index, position := range positions {
		if ctx.Err() != nil {
			return frames, append(warnings, "抽帧已取消")
		}
		outPath := filepath.Join(dir, fmt.Sprintf("frame-%d.jpg", index))
		cmd := exec.CommandContext(ctx, ffmpeg,
			"-y",
			"-ss", strconv.FormatFloat(position, 'f', 2, 64),
			"-i", candidate.Path,
			"-frames:v", "1",
			"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", faceFrameMaxWidth),
			"-q:v", strconv.Itoa(faceFrameQuality),
			outPath,
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			warnings = append(warnings, fmt.Sprintf("第 %d 帧抽取失败: %v %s", index+1, err, truncateLogSnippet(string(output), 160)))
			continue
		}
		if info, err := os.Stat(outPath); err != nil || info.Size() == 0 {
			warnings = append(warnings, fmt.Sprintf("第 %d 帧为空", index+1))
			continue
		}
		frameMS := int64(math.Round(position * 1000))
		frames = append(frames, faceFrame{Path: outPath, FrameMS: &frameMS})
	}
	return frames, warnings
}
