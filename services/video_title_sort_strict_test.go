package services

import "video-master/models"

// services 的整套测试兼作「有没有写入路径绕过片名排序键」的探测器（UI 二期 §2.3）：按条件更新
// display_title / name 却没经 UpdateVideoTitleFields 的写入在这里直接报错，而不是悄悄把键清空。
func init() { models.StrictVideoTitleSortKey = true }
