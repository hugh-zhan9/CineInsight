package services

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// 文件包（D-DH-01）：不压缩的 tar，包内路径相对数据目录、用正斜杠；只收普通文件，
// 不跟随符号链接；点开头的文件与目录是发布中的临时文件，跳过。

// walkBackupFiles 按固定顺序遍历收录目录里的普通文件。目录不存在不算错误（还没有内容）；
// 遍历中途消失的文件跳过——文件包是这一刻目录内容的快照。
func walkBackupFiles(dataDir string, visit func(rel, abs string) error) error {
	for _, dir := range backupFilesDirectories {
		root := filepath.Join(dataDir, filepath.FromSlash(dir))
		info, err := os.Lstat(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", dir, pathlessError(err))
		}
		if !info.IsDir() {
			continue
		}
		err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if os.IsNotExist(walkErr) {
					return nil
				}
				return walkErr
			}
			if current != root && strings.HasPrefix(entry.Name(), ".") {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() || !entry.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(dataDir, current)
			if err != nil {
				return pathlessError(err)
			}
			return visit(filepath.ToSlash(rel), current)
		})
		if err != nil {
			return fmt.Errorf("遍历 %s 失败: %w", dir, pathlessError(err))
		}
	}
	return nil
}

// estimateBackupFilesPack 预估文件包大小：每个文件一个 512 字节头加按 512 对齐的内容，末尾两个空块。
func estimateBackupFilesPack(dataDir string) (int64, error) {
	total := int64(1024)
	err := walkBackupFiles(dataDir, func(_, abs string) error {
		info, err := os.Lstat(abs)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return pathlessError(err)
		}
		total += 512 + (info.Size()+511)/512*512
		return nil
	})
	return total, err
}

// writeBackupFilesPack 把收录目录写成 tar，同时算出每个条目的大小与哈希（清单条目）。
// 文件在打包时变短视为失败；变长只收打包开始那一刻的长度。
func writeBackupFilesPack(ctx context.Context, dataDir string, out io.Writer) ([]BackupManifestEntry, error) {
	writer := tar.NewWriter(out)
	entries := []BackupManifestEntry{}
	err := walkBackupFiles(dataDir, func(rel, abs string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		file, err := os.Open(abs)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", rel, pathlessError(err))
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", rel, pathlessError(err))
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		header := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     rel,
			Mode:     int64(info.Mode().Perm()),
			Size:     info.Size(),
			ModTime:  info.ModTime(),
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		hash := sha256.New()
		copied, err := io.Copy(writer, io.TeeReader(io.LimitReader(file, info.Size()), hash))
		if err != nil {
			return fmt.Errorf("打包 %s 失败: %w", rel, pathlessError(err))
		}
		if copied != info.Size() {
			return fmt.Errorf("%s 在打包时被改动", rel)
		}
		entries = append(entries, BackupManifestEntry{Path: rel, Size: info.Size(), SHA256: fmt.Sprintf("%x", hash.Sum(nil))})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return entries, nil
}

// validBackupPackPath 判断包内路径能否安全还原：相对、规整、不越界，且落在某个收录目录之下。
func validBackupPackPath(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || path.Clean(name) != name {
		return false
	}
	for _, dir := range backupFilesDirectories {
		if strings.HasPrefix(name, dir+"/") {
			return true
		}
	}
	return false
}

// readBackupFilesPack 逐条读出文件包，核对条目路径、大小与哈希都与清单一致，返回整个文件的哈希
// 与第一个条目问题。extractTo 非空时同时把每个条目写到它给出的位置（恢复用的暂存目录）；只有通过
// validBackupPackPath 且列在清单里的条目才会被写出。
// 条目有问题时仍读完整个文件，好让调用方分别报告「哈希」与「条目」两项结论；
// 读文件本身失败（ioErr）时哈希不可用。
func readBackupFilesPack(packPath string, files BackupManifestFiles, extractTo func(name string) string) (fileHash string, entryErr error, ioErr error) {
	file, err := os.Open(packPath)
	if err != nil {
		return "", nil, errors.New("文件包不存在或无法读取")
	}
	defer file.Close()
	hash := sha256.New()
	source := io.TeeReader(file, hash)
	expected := make(map[string]BackupManifestEntry, len(files.Entries))
	for _, entry := range files.Entries {
		expected[entry.Path] = entry
	}
	seen := make(map[string]bool, len(files.Entries))
	reader := tar.NewReader(source)
	for entryErr == nil {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			entryErr = errors.New("文件包格式损坏")
			break
		}
		entryErr = readBackupPackEntry(reader, header, expected, seen, extractTo)
	}
	if entryErr == nil && len(seen) != len(expected) {
		entryErr = errors.New("文件包缺少清单里的条目")
	}
	if _, err := io.Copy(io.Discard, source); err != nil {
		return "", entryErr, errors.New("文件包读取失败")
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), entryErr, nil
}

func readBackupPackEntry(reader io.Reader, header *tar.Header, expected map[string]BackupManifestEntry, seen map[string]bool, extractTo func(name string) string) error {
	want, listed := expected[header.Name]
	if header.Typeflag != tar.TypeReg || !validBackupPackPath(header.Name) || !listed || seen[header.Name] {
		return errors.New("文件包里有清单之外的条目")
	}
	seen[header.Name] = true
	if header.Size != want.Size {
		return errors.New("文件包条目大小与清单不一致")
	}
	hash := sha256.New()
	sink := io.Writer(hash)
	var out *os.File
	if extractTo != nil {
		target := extractTo(header.Name)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return fmt.Errorf("创建暂存目录失败: %w", pathlessError(err))
		}
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode).Perm()&0755|0600)
		if err != nil {
			return fmt.Errorf("写入暂存文件失败: %w", pathlessError(err))
		}
		out = file
		sink = io.MultiWriter(out, hash)
	}
	copied, err := io.Copy(sink, reader)
	if out != nil {
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil || copied != want.Size {
		return errors.New("文件包条目读取失败")
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != want.SHA256 {
		return errors.New("文件包条目内容与清单不一致")
	}
	return nil
}
