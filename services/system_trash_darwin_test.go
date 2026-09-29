//go:build darwin && cgo

package services

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// 真实的系统废纸篓用例：只操作 t.TempDir() 里的临时文件，并且无论成败，测试结束时都把它
// 从废纸篓里删掉（用 trashItemAtURL 返回的实际路径），不在用户的废纸篓里留垃圾。
func TestLIB05RealSystemTrashMovesTempFileAndReturnsTrashPath(t *testing.T) {
	source := filepath.Join(t.TempDir(), "cineinsight-p010-real-trash.txt")
	if err := os.WriteFile(source, []byte("temp file for the real system trash test"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}

	trashed, err := moveToSystemTrash(source)
	if trashed != "" {
		t.Cleanup(func() { _ = os.Remove(trashed) })
	}
	if err != nil {
		t.Fatalf("移入系统废纸篓失败: %v", err)
	}
	if _, statErr := os.Stat(source); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("原文件应已移走: %v", statErr)
	}
	after, err := os.Stat(trashed)
	if err != nil {
		t.Fatalf("返回的废纸篓路径上应有文件: %v", err)
	}
	// 同卷移动是重命名：大小、mtime、稳定身份都不变——这正是恢复前核对身份所依赖的性质。
	id := trashFileIDOf(before)
	if !id.strictMatch(after) {
		t.Fatalf("移入废纸篓后文件身份应不变: before=%+v after=%+v", id, trashFileIDOf(after))
	}
	// 崩溃恢复分支 2：按身份在用户废纸篓顶层能找回它（这里用真实的查找目录，不用测试替身）。
	// macOS 的隐私保护（TCC）会拒绝没有「完全磁盘访问」授权的进程列出 ~/.Trash；列不出来时
	// findInUserTrash 只能返回空，崩溃恢复随即落到「文件位置未知」分支。此时跳过这一段断言，
	// 并把它记成需要真机确认的点（在已授权的应用里验证）。
	if _, listErr := os.ReadDir(filepath.Dir(trashed)); listErr != nil {
		t.Skipf("当前进程无权列出用户废纸篓（%v），跳过按身份查找的断言", listErr)
	}
	previousLookup := trashLookupDirs
	trashLookupDirs = defaultTrashLookupDirs
	t.Cleanup(func() { trashLookupDirs = previousLookup })
	if found := findInUserTrash(source, id); found != trashed {
		t.Fatalf("按身份应能在废纸篓里找回: got %q want %q", found, trashed)
	}
}

func TestLIB05RealSystemTrashMissingFileMapsToNotExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.txt")
	trashed, err := moveToSystemTrash(missing)
	if trashed != "" {
		t.Cleanup(func() { _ = os.Remove(trashed) })
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("不存在的文件应映射为 os.ErrNotExist: %v", err)
	}
}
