package services

import (
	"context"
	"errors"
)

// 产品完善度批次（2026-09-29）的共享契约：下游切片直接引用，不各自再定义一份。
// 失效原因与回收站模式常量在 models（models.StaleReason*、models.TrashMode*、
// models.TrashStateFileGone），登记表 key image_cleanup 在 background_task_registry.go。

// personTagNamespace 是「人物」分类的标签命名空间（D-PC28 规则 6、D-PC34）。
//
// tag_person_conversion.go 里目前仍是字面量 "人物"，与本常量同义；该文件不在 P-001 的写入
// 范围内，由后续切片统一改为引用本常量。
const personTagNamespace = "人物"

// 回收站与按身份屏蔽的哨兵错误（D-PC01~03）。文案面向用户，不含绝对路径（G-3）；
// 调用方用 errors.Is 判断，再按各自的通道映射成错误码（例如手机端 409 trash_unsupported）。
var (
	// ErrVideoBlockedByUserDelete：同路径的软删行是用户「只删记录」留下的，且文件身份未变，
	// 扫描不得重新收录（D-PC03）。video_scan.go 据此把它计入 blocked_user_delete。
	ErrVideoBlockedByUserDelete = errors.New("该文件已被你从片库移除，扫描不会重新收录")
	// ErrTrashUnsupportedVolume：所在磁盘不支持系统废纸篓，或平台不是 macOS（D-PC02）。
	ErrTrashUnsupportedVolume = errors.New("该磁盘不支持废纸篓")
	// ErrTrashPermissionDenied：没有权限把文件移到废纸篓。
	ErrTrashPermissionDenied = errors.New("没有权限把文件移到废纸篓")
	// ErrTrashIdentityMismatch：废纸篓里的文件与删除时记录的身份不一致，拒绝恢复或永久删除。
	ErrTrashIdentityMismatch = errors.New("废纸篓里的文件与删除时不一致，已拒绝操作")
)

// WatchStateObserver 由 VideoService 在 is_watched 实际翻转、事务提交之后回调（D-PC52）。
// 依赖方向是 App → 两个服务，由 App 注入 MovieChartService，服务之间不互相引用。
type WatchStateObserver interface {
	OnVideoWatchedChangedContext(ctx context.Context, videoID uint, watched bool)
}

// LinkedVideoWatchSetter 是榜单一侧「取消已看 / 标已看」回写关联视频的入口。
// 它不触发 WatchStateObserver，避免两侧互相回调成环。
type LinkedVideoWatchSetter interface {
	SetVideoWatchedFromLink(videoID uint, watched bool) error
}
