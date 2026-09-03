package appdata

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRenamesLegacyDirectoryOnce(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, LegacyDirName)
	if err := os.MkdirAll(filepath.Join(legacy, "thumbnails"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "library.db"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir, err := ResolveIn(home)
	if err != nil {
		t.Fatalf("解析数据目录失败: %v", err)
	}
	if dir != filepath.Join(home, DirName) {
		t.Fatalf("应当返回新目录: %s", dir)
	}
	// 数据必须整个搬过来，一个字节都不能丢
	if content, err := os.ReadFile(filepath.Join(dir, "library.db")); err != nil || string(content) != "db" {
		t.Fatalf("库文件没跟着搬过来: %q err=%v", string(content), err)
	}
	if _, err := os.Stat(filepath.Join(dir, "thumbnails")); err != nil {
		t.Fatalf("子目录没跟着搬过来: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("旧目录应当已经不存在: %v", err)
	}

	// 幂等：再解析一次不该有任何变化
	again, err := ResolveIn(home)
	if err != nil || again != dir {
		t.Fatalf("重复解析应当稳定: %s err=%v", again, err)
	}
}

func TestResolveKeepsExistingNewDirectoryAndLeavesLegacyAlone(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, DirName)
	legacy := filepath.Join(home, LegacyDirName)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "library.db"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "library.db"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir, err := ResolveIn(home)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if dir != target {
		t.Fatalf("应当用新目录: %s", dir)
	}
	// 两份都在时不合并、不覆盖、不删除，交给用户处置
	if content, _ := os.ReadFile(filepath.Join(target, "library.db")); string(content) != "new" {
		t.Fatalf("新目录的数据被覆盖了: %q", string(content))
	}
	if content, _ := os.ReadFile(filepath.Join(legacy, "library.db")); string(content) != "old" {
		t.Fatalf("旧目录不该被动: %q", string(content))
	}
}

func TestResolveOnFreshInstall(t *testing.T) {
	home := t.TempDir()

	dir, err := ResolveIn(home)
	if err != nil {
		t.Fatalf("全新安装不该报错: %v", err)
	}
	if dir != filepath.Join(home, DirName) {
		t.Fatalf("应当返回新目录: %s", dir)
	}
}

func TestResolveFailsLoudlyWhenTargetIsAFile(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, DirName), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, LegacyDirName), 0o755); err != nil {
		t.Fatal(err)
	}

	// 名字被文件占住时必须报错，而不是继续往下走用一个用不了的路径
	if _, err := ResolveIn(home); err == nil {
		t.Fatal("目标被同名文件占用时应当报错")
	}
}
