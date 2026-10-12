package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// 备份清单与文件包（D-DH-01 / D-DH-02，docs/loopx/design/2026-10-12-data-care 4.1）。
//
// 一份备份是三个同名前缀的文件：快照（格式不变）、文件包 <前缀>.files.tar、清单 <前缀>.manifest.json。
// 清单最后发布：有清单的才是完整的一份；没有清单的快照是旧格式备份（仅数据库），
// 有文件包却没有清单的是半成品，轮转时清理。

const (
	backupManifestFormat = 1
	backupFilesSuffix    = ".files.tar"
	backupManifestSuffix = ".manifest.json"
	// BackupReasonInsufficientSpace：备份目录所在磁盘放不下这一份（快照 + 文件包），消息以它开头。
	BackupReasonInsufficientSpace = "backup_insufficient_space"
	// BackupReasonFromNewerVersion：清单里有当前版本不认识的表或列，拒绝恢复，消息以它开头。
	BackupReasonFromNewerVersion = "backup_from_newer_version"
)

// backupFilesDirectories 是文件包收入的数据目录子树（相对数据目录，D-DH-01）：
// 人物头像、作品集封面、海报副本、想看封面、壁纸、字幕备份。缓存类目录（缩略图、代理、
// 人脸裁剪、榜单海报）不在其中；.env 有数据库口令，也不在其中。
var backupFilesDirectories = []string{
	"media-details/people",
	"media-details/collections",
	"media-details/videos",
	"media-details/watchlist",
	"wallpapers",
	"subtitle-backups",
}

// BackupManifest 是一份备份的清单（4.1 的结构）。Rows 与 QuickCheck 只有 SQLite 快照有。
type BackupManifest struct {
	Format     int                  `json:"format"`
	Backend    string               `json:"backend"`
	CreatedAt  time.Time            `json:"created_at"`
	Snapshot   BackupManifestFile   `json:"snapshot"`
	Files      BackupManifestFiles  `json:"files"`
	Schema     BackupManifestSchema `json:"schema"`
	Rows       map[string]int64     `json:"rows,omitempty"`
	QuickCheck string               `json:"quick_check,omitempty"`
}

type BackupManifestFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type BackupManifestFiles struct {
	Name    string                `json:"name"`
	Size    int64                 `json:"size"`
	SHA256  string                `json:"sha256"`
	Entries []BackupManifestEntry `json:"entries"`
}

type BackupManifestEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// BackupManifestSchema：SQLite 取自快照（表 → 列）；Postgres 取 pg_restore --list 的表名，列为空。
type BackupManifestSchema struct {
	Tables map[string][]string `json:"tables"`
}

// backupMemberPattern 认出一份备份的公共前缀与它的三种成员。
var backupMemberPattern = regexp.MustCompile(`^(cineinsight-\d{8}-\d{6}\.\d{9})(\.sqlite|\.dump|\.files\.tar|\.manifest\.json)$`)

// backupStem 去掉快照后缀，得到三件套的公共前缀。
func backupStem(snapshotName string) string {
	return strings.TrimSuffix(strings.TrimSuffix(snapshotName, sqliteBackupFileSuffix), backupFileSuffix)
}

func backupFilesName(snapshotName string) string {
	return backupStem(snapshotName) + backupFilesSuffix
}

func backupManifestName(snapshotName string) string {
	return backupStem(snapshotName) + backupManifestSuffix
}

// errBackupManifestMissing：这份快照没有清单（旧格式备份）。
var errBackupManifestMissing = errors.New("备份没有清单")

// loadBackupManifest 读出快照对应的清单。没有清单返回 errBackupManifestMissing；
// 清单存在但读不出、或者记的不是这份快照，返回其他错误（备份不完整，不能当旧格式备份对待）。
func loadBackupManifest(directory, snapshotName string) (*BackupManifest, error) {
	path := filepath.Join(directory, backupManifestName(snapshotName))
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, errBackupManifestMissing
	}
	if err != nil {
		return nil, errors.New("备份清单无法读取")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("备份清单不是普通文件")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("备份清单无法读取")
	}
	var manifest BackupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, errors.New("备份清单已损坏")
	}
	if manifest.Format != backupManifestFormat {
		return nil, fmt.Errorf("备份清单格式 %d 不受支持", manifest.Format)
	}
	if manifest.Snapshot.Name != snapshotName || manifest.Files.Name != backupFilesName(snapshotName) {
		return nil, errors.New("备份清单与快照对不上")
	}
	return &manifest, nil
}

// removeIncompleteBackups 清理没发布完的备份：有文件包却没有清单的快照（发布到一半中断），
// 以及快照已经不在的文件包与清单。只有快照、没有文件包与清单的是旧格式备份，不动。
func removeIncompleteBackups(directory, protectedBackup string) error {
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取备份目录失败: %w", err)
	}
	type members struct {
		snapshot       string
		files, listing bool
	}
	groups := map[string]*members{}
	for _, entry := range entries {
		match := backupMemberPattern.FindStringSubmatch(entry.Name())
		if match == nil || !entry.Type().IsRegular() {
			continue
		}
		group := groups[match[1]]
		if group == nil {
			group = &members{}
			groups[match[1]] = group
		}
		switch match[2] {
		case backupFilesSuffix:
			group.files = true
		case backupManifestSuffix:
			group.listing = true
		default:
			group.snapshot = entry.Name()
		}
	}
	for stem, group := range groups {
		var remove []string
		switch {
		case group.snapshot == "":
			remove = []string{stem + backupManifestSuffix, stem + backupFilesSuffix}
		case group.files && !group.listing && group.snapshot != protectedBackup:
			remove = []string{stem + backupFilesSuffix, group.snapshot}
		}
		for _, name := range remove {
			if err := os.Remove(filepath.Join(directory, name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}
