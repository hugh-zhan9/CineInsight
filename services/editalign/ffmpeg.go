package editalign

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// readHashSequence 单趟读取 [startMS, startMS+durationMS) 的 4fps（或 stepMS）dHash 序列。
// 一个 ffmpeg 进程、参数数组、不经 shell；读失败先杀进程再 Wait，否则 ffmpeg 会卡在满管道上。
func readHashSequence(ctx context.Context, ffmpegBin, path string, startMS, durationMS, stepMS int64) (Sequence, error) {
	if ffmpegBin == "" || path == "" {
		return Sequence{}, errors.New("editalign: 缺少 ffmpeg 或视频路径")
	}
	if stepMS <= 0 || startMS < 0 {
		return Sequence{}, fmt.Errorf("editalign: 非法采样参数 start=%d step=%d", startMS, stepMS)
	}
	args := []string{"-v", "warning", "-nostdin", "-fflags", "+discardcorrupt"}
	if startMS > 0 {
		args = append(args, "-ss", formatSeconds(startMS))
	}
	args = append(args, "-i", path)
	if durationMS > 0 {
		args = append(args, "-t", formatSeconds(durationMS))
	}
	args = append(args,
		"-map", "0:V:0",
		"-vf", fmt.Sprintf("fps=1000/%d,scale=9:8:flags=area,format=gray", stepMS),
		"-f", "rawvideo", "pipe:1")

	sequence := Sequence{StartMS: startMS, StepMS: stepMS}
	err := runFFmpegStream(ctx, ffmpegBin, path, args, hashGrayBytes, func(frame []byte) error {
		if len(sequence.Hashes) >= maxHashFrames {
			return fmt.Errorf("指纹帧数超过上限 %d", maxHashFrames)
		}
		hash, err := HashGray9x8(frame)
		if err != nil {
			return err
		}
		sequence.Hashes = append(sequence.Hashes, hash)
		return nil
	})
	if err != nil {
		return Sequence{}, err
	}
	return sequence, nil
}

// runFFmpegStream 启动 ffmpeg，把 stdout 按 frameBytes 切帧交给 consume。
// consume 或读取出错时立即杀进程；ctx 取消时返回 ctx.Err()。错误里只带擦除路径后的 stderr 尾部。
func runFFmpegStream(ctx context.Context, ffmpegBin, path string, args []string, frameBytes int, consume func([]byte) error) error {
	command := exec.CommandContext(ctx, ffmpegBin, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return ffmpegError("创建 ffmpeg 输出管道失败", err, "", path, ffmpegBin)
	}
	stderr := &tailBuffer{limit: stderrTailBytes}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return ffmpegError("启动 ffmpeg 失败", err, "", path, ffmpegBin)
	}
	readErr := readRawFrames(stdout, frameBytes, consume)
	if readErr != nil && command.Process != nil {
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if readErr != nil {
		return ffmpegError("读取 ffmpeg 输出失败", readErr, stderr.String(), path, ffmpegBin)
	}
	if waitErr != nil {
		return ffmpegError("ffmpeg 抽帧失败", waitErr, stderr.String(), path, ffmpegBin)
	}
	return nil
}

func readRawFrames(reader io.Reader, frameBytes int, consume func([]byte) error) error {
	buffered := bufio.NewReaderSize(reader, max(frameBytes*64, 4096))
	frame := make([]byte, frameBytes)
	for {
		if _, err := io.ReadFull(buffered, frame); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return fmt.Errorf("输出被截断，不是 %d 字节的整数倍", frameBytes)
			}
			return err
		}
		if err := consume(frame); err != nil {
			return err
		}
	}
}

// tailBuffer 只保留写入内容的最后 limit 字节（ffmpeg stderr 尾部）。
type tailBuffer struct {
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*t.limit {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.limit:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	data := t.buf
	if len(data) > t.limit {
		data = data[len(data)-t.limit:]
	}
	return strings.TrimSpace(string(data))
}

// ffmpegError 组装错误并擦除其中的绝对路径（来源路径、临时文件、ffmpeg 自身路径）。
func ffmpegError(what string, err error, stderrTail string, paths ...string) error {
	message := fmt.Sprintf("editalign: %s: %v", what, err)
	if stderrTail != "" {
		message += ": " + stderrTail
	}
	return errors.New(scrubPaths(message, paths...))
}

// absolutePathPattern 只认绝对路径：斜杠或盘符前是开头、空白或常见分隔符，根之后至少一个
// 路径字符；末尾的标点不算路径，"<path>: 原因"这种可读边界才不会被吃掉。
var absolutePathPattern = regexp.MustCompile(`(^|[\s:=(（"'“])(?:/|[A-Za-z]:\\)[^\s'"”，。；）)]*[^\s'"”，。；）):,.;!?]`)

// scrubPaths 先整串替换已知路径（可能含空格），再把剩余的绝对路径擦成 <path>。
func scrubPaths(text string, paths ...string) string {
	for _, path := range paths {
		if path != "" {
			text = strings.ReplaceAll(text, path, "<path>")
		}
	}
	return absolutePathPattern.ReplaceAllString(text, "${1}<path>")
}

func formatSeconds(ms int64) string {
	return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64)
}
