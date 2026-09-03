// Package appdata 统一解析应用数据目录，并把历史遗留的 ~/.video-master
// 一次性改名成 ~/.CineInsight。
//
// 目录名早年跟着 Go 模块名叫 video-master，与产品名对不上。改名只做纯改名：
// 同一个家目录下的 rename 是原子的，不复制、不合并、不删除。任何一步失败都
// 直接报错——静默退回一个空目录会让用户以为整个库没了。
package appdata

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	// DirName 是当前的数据目录名。
	DirName = ".CineInsight"
	// LegacyDirName 是 2026-09 之前使用的目录名。
	LegacyDirName = ".video-master"
)

// Resolve 返回数据目录的绝对路径，必要时先把旧目录改名过来。
// 幂等：新目录已经存在时直接返回，不再碰旧目录。
func Resolve() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户目录失败: %w", err)
	}
	return ResolveIn(homeDir)
}

// ResolveIn 是 Resolve 的可测形式，家目录由调用方给出。
func ResolveIn(homeDir string) (string, error) {
	target := filepath.Join(homeDir, DirName)
	legacy := filepath.Join(homeDir, LegacyDirName)

	targetInfo, targetErr := os.Lstat(target)
	if targetErr == nil {
		if !targetInfo.IsDir() {
			return "", fmt.Errorf("数据目录被同名文件占用: %s", target)
		}
		// 新目录已经在用了。旧目录若还在，留着不动：合并两份数据的语义不明确，
		// 该由用户自己决定，程序不替他做主。
		return target, nil
	}
	if !os.IsNotExist(targetErr) {
		return "", fmt.Errorf("检查数据目录失败: %w", targetErr)
	}

	legacyInfo, legacyErr := os.Lstat(legacy)
	if legacyErr != nil {
		if os.IsNotExist(legacyErr) {
			return target, nil // 全新安装
		}
		return "", fmt.Errorf("检查旧数据目录失败: %w", legacyErr)
	}
	if !legacyInfo.IsDir() {
		return target, nil // 同名的不是目录，不认它
	}
	if err := os.Rename(legacy, target); err != nil {
		return "", fmt.Errorf("把数据目录从 %s 改名到 %s 失败，请手动改名后重启：%w", LegacyDirName, DirName, err)
	}
	return target, nil
}
