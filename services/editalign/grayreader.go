package editalign

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// readGrayFrames 读取 [startMS, startMS+durationMS) 内全部原帧率 side×side 灰度帧及其真实 PTS。
//
// PTS 的来源：同一个 ffmpeg 进程把滤镜输出 split 成两路——像素以 rawvideo 写到 stdout，
// 同一批帧以 framecrc 写到一个临时文本文件（编码时基固定 1/1000，pts 直接是毫秒）。
// 两路都 -fps_mode passthrough，不补帧不丢帧，所以第 i 行 framecrc 就是第 i 帧像素。
// 输入端 -ss 使输出时间从窗口起点算起，绝对时间 = startMS + pts。
// 不用 pipe:3（Windows 不支持 ExtraFiles），也不解析 showinfo 日志。
func readGrayFrames(ctx context.Context, ffmpegBin, path string, side int, startMS, durationMS int64) ([]GrayFrame, error) {
	if ffmpegBin == "" || path == "" {
		return nil, errors.New("editalign: 缺少 ffmpeg 或视频路径")
	}
	if side < 2 || side > maxGraySide {
		return nil, fmt.Errorf("editalign: 灰度帧边长 %d 超出 2–%d", side, maxGraySide)
	}
	if startMS < 0 || durationMS <= 0 || durationMS > maxGrayWindowMS {
		return nil, fmt.Errorf("editalign: 读帧窗口 start=%d duration=%d 非法（时长需在 1–%dms）", startMS, durationMS, maxGrayWindowMS)
	}
	ptsFile, err := os.CreateTemp("", "editalign-pts-*.txt")
	if err != nil {
		return nil, errors.New(scrubPaths(fmt.Sprintf("editalign: 创建时间戳临时文件失败: %v", err)))
	}
	ptsPath := ptsFile.Name()
	_ = ptsFile.Close()
	defer os.Remove(ptsPath)

	args := []string{"-v", "warning", "-nostdin", "-y", "-fflags", "+discardcorrupt"}
	if startMS > 0 {
		args = append(args, "-ss", formatSeconds(startMS))
	}
	args = append(args, "-t", formatSeconds(durationMS), "-i", path,
		"-filter_complex", fmt.Sprintf("[0:V:0]scale=%d:%d:flags=area,format=gray,split=2[px][ts]", side, side),
		"-map", "[px]", "-fps_mode", "passthrough", "-f", "rawvideo", "pipe:1",
		"-map", "[ts]", "-fps_mode", "passthrough", "-enc_time_base", "1:1000", "-f", "framecrc", ptsPath)

	frameBytes := side * side
	var frames []GrayFrame
	err = runFFmpegStream(ctx, ffmpegBin, path, args, frameBytes, func(frame []byte) error {
		if len(frames) >= maxGrayFrames {
			return fmt.Errorf("窗口内帧数超过上限 %d", maxGrayFrames)
		}
		frames = append(frames, GrayFrame{Pixels: slices.Clone(frame)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	pts, err := readFrameCRCPTS(ptsPath)
	if err != nil {
		return nil, errors.New(scrubPaths(err.Error(), path, ptsPath))
	}
	if len(pts) != len(frames) {
		return nil, fmt.Errorf("editalign: 时间戳 %d 条与像素帧 %d 帧不一致", len(pts), len(frames))
	}
	for i := range frames {
		frames[i].PTSMS = startMS + pts[i]
	}
	slices.SortStableFunc(frames, func(a, b GrayFrame) int { return compareInt64(a.PTSMS, b.PTSMS) })
	return frames, nil
}

// readFrameCRCPTS 解析 framecrc 输出（"#tb 0: 1/1000" 头 + 每帧 "流, dts, pts, 时长, 大小, crc"），
// 返回以毫秒计的 pts。时基不是 1/1000 时按实际时基换算。
func readFrameCRCPTS(path string) ([]int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("editalign: 读取帧时间戳失败: %w", err)
	}
	defer file.Close()
	num, den := int64(1), int64(1000)
	var pts []int64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#tb 0:") {
			ratio := strings.TrimSpace(strings.TrimPrefix(line, "#tb 0:"))
			n, d, ok := strings.Cut(ratio, "/")
			num, err = strconv.ParseInt(strings.TrimSpace(n), 10, 64)
			if err == nil && ok {
				den, err = strconv.ParseInt(strings.TrimSpace(d), 10, 64)
			}
			if err != nil || !ok || num <= 0 || den <= 0 {
				return nil, fmt.Errorf("editalign: 无法解析帧时基 %q", ratio)
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 3 {
			return nil, fmt.Errorf("editalign: 无法解析帧时间戳行 %q", line)
		}
		value, err := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("editalign: 无法解析帧时间戳行 %q", line)
		}
		pts = append(pts, roundDiv(value*num*1000, den))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("editalign: 读取帧时间戳失败: %w", err)
	}
	return pts, nil
}
