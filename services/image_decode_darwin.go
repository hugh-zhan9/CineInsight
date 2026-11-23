//go:build darwin

package services

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// sipsConvertJPEG 优先用进程内 ImageIO 硬件加速缩图，失败时回退到系统 sips 命令。
func sipsConvertJPEG(ctx context.Context, sourcePath, destinationPath string, maxEdge int) error {
	if err := nativeFastImageThumbnail(ctx, sourcePath, destinationPath, maxEdge); err == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, imageDecodeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sips",
		"-s", "format", "jpeg",
		"--resampleHeightWidthMax", strconv.Itoa(maxEdge),
		sourcePath,
		"--out", destinationPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sips: %w: %s", err, truncateLogSnippet(string(output), 400))
	}
	return nil
}

// sipsProbeDimensions 优先用进程内 ImageIO 读文件头尺寸，失败时回退到 sips。
func sipsProbeDimensions(ctx context.Context, sourcePath string) (int, int, error) {
	if w, h, err := nativeFastImageProbe(ctx, sourcePath); err == nil {
		return w, h, nil
	}
	ctx, cancel := context.WithTimeout(ctx, imageDecodeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sips", "-g", "pixelWidth", "-g", "pixelHeight", sourcePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf("sips: %w: %s", err, truncateLogSnippet(string(output), 400))
	}
	width, height := 0, 0
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(line, "pixelWidth:"); ok {
			width, _ = strconv.Atoi(strings.TrimSpace(value))
		}
		if value, ok := strings.CutPrefix(line, "pixelHeight:"); ok {
			height, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	if width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("sips 未返回有效尺寸: %s", truncateLogSnippet(string(output), 200))
	}
	return width, height, nil
}
