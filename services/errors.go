package services

import "errors"

// 统一错误定义（sentinel errors），便于前端可靠判断错误类型
var (
	ErrVideoExists   = errors.New("VIDEO_EXISTS")   // 视频已存在
	ErrTagExists     = errors.New("TAG_EXISTS")     // 标签已存在
	ErrNoVideos      = errors.New("NO_VIDEOS")      // 没有可播放的视频
	ErrUnsupportedOS = errors.New("UNSUPPORTED_OS") // 不支持的操作系统
)
