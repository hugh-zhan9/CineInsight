package services

import (
	"net/http"
	"time"
)

// JellyfinFailure 是请求日志里最后一条失败记录的内存副本（D-PC47、详细设计 §8.8）。RouteShape 与
// 日志行同一套脱敏：方法 + 路由形状，路径里的 ID 掩码为 {id}，不含查询参数、令牌、搜索词或文件路径。
type JellyfinFailure struct {
	At         time.Time `json:"at" ts_type:"string"`
	RouteShape string    `json:"route_shape"`
	Status     int       `json:"status"`
}

// JellyfinDiagnostics 是设置页展示的 Jellyfin 连接诊断（PLAY-14）。只在内存里，重启后清空。
// LastRequestAt / LastClient 只统计通过内网来源校验、进入服务处理的请求。
type JellyfinDiagnostics struct {
	Enabled       bool             `json:"enabled"`
	Listening     bool             `json:"listening"`
	LastRequestAt *time.Time       `json:"last_request_at" ts_type:"string"`
	LastClient    string           `json:"last_client"`
	LastFailure   *JellyfinFailure `json:"last_failure"`
}

// jellyfinDiagnosticsState 由 JellyfinServer.mu 保护。
type jellyfinDiagnosticsState struct {
	lastRequestAt *time.Time
	lastClient    string
	lastFailure   *JellyfinFailure
}

// noteRequest 记下一次请求。shape 为空（成功的流、图片、进度上报不写日志）时不可能是失败记录，
// 因为失败请求一律写日志。
func (s *JellyfinServer) noteRequest(at time.Time, client, method, shape string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diagnostics.lastRequestAt = &at
	if client != "" {
		s.diagnostics.lastClient = client
	}
	if status >= http.StatusBadRequest && shape != "" {
		s.diagnostics.lastFailure = &JellyfinFailure{At: at, RouteShape: jellyfinLogSafe(method, 16) + " " + shape, Status: status}
	}
}

// Diagnostics 返回当前开关与监听状态，以及最近一次请求、客户端和失败记录的副本。
func (s *JellyfinServer) Diagnostics() JellyfinDiagnostics {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := JellyfinDiagnostics{Enabled: s.status.Enabled, Listening: s.status.Running, LastClient: s.diagnostics.lastClient}
	if at := s.diagnostics.lastRequestAt; at != nil {
		copied := *at
		result.LastRequestAt = &copied
	}
	if failure := s.diagnostics.lastFailure; failure != nil {
		copied := *failure
		result.LastFailure = &copied
	}
	return result
}
