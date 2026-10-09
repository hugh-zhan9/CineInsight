//go:build darwin

package services

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"time"
)

func consolidationMachineID() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return "", fmt.Errorf("读取本机硬件身份: %w", err)
	}
	match := regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([0-9A-Fa-f-]{36})"`).FindSubmatch(output)
	if len(match) != 2 {
		return "", fmt.Errorf("本机硬件身份不可用")
	}
	return string(match[1]), nil
}
