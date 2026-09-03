//go:build !darwin

package services

import (
	"context"
	"time"
)

// idleProbeSupported 说明本平台没有可用的空闲探测（D-031）。
const idleProbeSupported = false

// idleProbeAlwaysIdle 是非 darwin 平台返回的空闲时长：足够大到任何阈值都成立。
const idleProbeAlwaysIdle = 365 * 24 * time.Hour

// probeSystemIdle 在非 darwin 平台一律报告"空闲且接着电源"。
// ioreg / pmset 是 macOS 专有的，这里既没有等价物也不打算引入依赖；
// 结果就是空闲门在别的平台上等同于关闭（README / GUIDE 已写明）。
func probeSystemIdle(context.Context) (IdleSample, error) {
	return IdleSample{Idle: idleProbeAlwaysIdle, OnACPower: true}, nil
}
