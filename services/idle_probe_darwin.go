//go:build darwin

package services

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// idleProbeSupported 说明本平台能真的探测空闲（D-031）。
const idleProbeSupported = true

// 探测命令必须很快返回；卡住的探测会把等待中的任务一起拖住。
const idleProbeCommandTimeout = 5 * time.Second

// ioreg 把 HIDIdleTime 打成 `"HIDIdleTime" = 123456789`（纳秒）。
var hidIdleTimePattern = regexp.MustCompile(`"HIDIdleTime"\s*=\s*(\d+)`)

// probeSystemIdle 读一次系统空闲时长与供电状态。
// 任一命令失败都直接返回错误：调用方按"不空闲 + probe_failed"处理，
// 绝不猜一个"大概空闲"的值。
func probeSystemIdle(ctx context.Context) (IdleSample, error) {
	idle, err := probeHIDIdleTime(ctx)
	if err != nil {
		return IdleSample{}, err
	}
	onAC, err := probeACPower(ctx)
	if err != nil {
		return IdleSample{}, err
	}
	return IdleSample{Idle: idle, OnACPower: onAC}, nil
}

func probeHIDIdleTime(ctx context.Context) (time.Duration, error) {
	output, err := runIdleProbeCommand(ctx, "ioreg", "-c", "IOHIDSystem", "-d", "4")
	if err != nil {
		return 0, fmt.Errorf("ioreg 探测失败: %w", err)
	}
	return parseHIDIdleTime(output)
}

// parseHIDIdleTime 从 ioreg 输出里取出空闲纳秒数。
func parseHIDIdleTime(output string) (time.Duration, error) {
	match := hidIdleTimePattern.FindStringSubmatch(output)
	if match == nil {
		return 0, fmt.Errorf("ioreg 输出里没有 HIDIdleTime")
	}
	nanoseconds, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("HIDIdleTime 解析失败: %w", err)
	}
	if nanoseconds < 0 {
		return 0, fmt.Errorf("HIDIdleTime 为负数: %d", nanoseconds)
	}
	return time.Duration(nanoseconds) * time.Nanosecond, nil
}

func probeACPower(ctx context.Context) (bool, error) {
	output, err := runIdleProbeCommand(ctx, "pmset", "-g", "batt")
	if err != nil {
		return false, fmt.Errorf("pmset 探测失败: %w", err)
	}
	return parseACPower(output), nil
}

// parseACPower 判断当前是不是靠电池。
//
// 判"有没有 Battery Power"而不是"有没有 AC Power"：台式机（没有电池）的输出里
// 只有 "Now drawing from 'AC Power'" 或干脆没有电池信息，按后者判会把它误报成
// 靠电池，于是"只在接电源时跑"这个选项在台式机上永远不满足。
func parseACPower(output string) bool {
	return !strings.Contains(output, "Battery Power")
}

func runIdleProbeCommand(parent context.Context, name string, args ...string) (string, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, idleProbeCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		// stderr 是这类失败唯一有用的线索，别把它扔了。
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("%w: %s", err, truncateLogSnippet(string(exitErr.Stderr), 400))
		}
		return "", err
	}
	return string(output), nil
}
