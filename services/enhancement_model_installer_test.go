package services

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildFakeUpstreamArchive 造一个和上游同构的压缩包：模型放在 models/ 子目录下，
// 还夹带一堆我们不需要的文件。
func buildFakeUpstreamArchive(t *testing.T, contents map[string]string, extra map[string]string) []byte {
	t.Helper()
	buffer := &bytes.Buffer{}
	writer := zip.NewWriter(buffer)
	write := func(name, content string) {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range contents {
		write("realesrgan-ncnn-vulkan-20220424-macos/models/"+name, content)
	}
	for name, content := range extra {
		write("realesrgan-ncnn-vulkan-20220424-macos/"+name, content)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func manifestModelsFor(contents map[string]string) []enhancementManifestFile {
	models := make([]enhancementManifestFile, 0, len(contents))
	for name, content := range contents {
		digest := sha256.Sum256([]byte(content))
		models = append(models, enhancementManifestFile{Path: name, SHA256: hex.EncodeToString(digest[:])})
	}
	return models
}

func waitForInstaller(t *testing.T, installer *EnhancementModelInstaller) EnhancementModelStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := installer.Status()
		if !status.Running {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("下载没有在预期时间内结束")
	return EnhancementModelStatus{}
}

func newTestInstaller(t *testing.T, archive []byte) (*EnhancementModelInstaller, string) {
	t.Helper()
	installDir := filepath.Join(t.TempDir(), "models")
	installer := NewEnhancementModelInstaller(installDir)
	installer.fetch = func(ctx context.Context, url string) (io.ReadCloser, int64, error) {
		return io.NopCloser(bytes.NewReader(archive)), int64(len(archive)), nil
	}
	return installer, installDir
}

func TestEnhancementModelInstallerExtractsOnlyDeclaredModels(t *testing.T) {
	contents := enhancementModelFixtureContents()
	archive := buildFakeUpstreamArchive(t, contents, map[string]string{
		"realesrgan-ncnn-vulkan":             "上游自带的二进制，我们不用它",
		"models/realesrgan-x4plus-anime.bin": "用不到的模型",
	})
	installer, installDir := newTestInstaller(t, archive)

	if _, err := installer.Start(context.Background(), manifestModelsFor(contents)); err != nil {
		t.Fatalf("发起下载失败: %v", err)
	}
	status := waitForInstaller(t, installer)

	if !status.Completed || status.Error != "" {
		t.Fatalf("下载应当成功: %+v", status)
	}
	installed, err := os.ReadDir(installDir)
	if err != nil {
		t.Fatalf("读取安装目录失败: %v", err)
	}
	if len(installed) != len(contents) {
		names := make([]string, 0, len(installed))
		for _, entry := range installed {
			names = append(names, entry.Name())
		}
		t.Fatalf("只应安装清单声明的模型，实际: %v", names)
	}
	for name, want := range contents {
		got, err := os.ReadFile(filepath.Join(installDir, name))
		if err != nil || string(got) != want {
			t.Fatalf("模型内容不对 %s: %q err=%v", name, string(got), err)
		}
	}
}

func TestEnhancementModelInstallerRejectsHashMismatch(t *testing.T) {
	contents := enhancementModelFixtureContents()
	tampered := make(map[string]string, len(contents))
	for name := range contents {
		tampered[name] = "被换掉的权重"
	}
	installer, installDir := newTestInstaller(t, buildFakeUpstreamArchive(t, tampered, nil))

	if _, err := installer.Start(context.Background(), manifestModelsFor(contents)); err != nil {
		t.Fatalf("发起下载失败: %v", err)
	}
	status := waitForInstaller(t, installer)

	if status.Completed || status.Error == "" {
		t.Fatalf("哈希对不上必须失败: %+v", status)
	}
	if _, err := os.Stat(installDir); err == nil {
		entries, _ := os.ReadDir(installDir)
		if len(entries) > 0 {
			t.Fatalf("校验失败时不该往安装目录里留东西: %d 个文件", len(entries))
		}
	}
}

func TestEnhancementModelInstallerRejectsIncompleteArchive(t *testing.T) {
	contents := enhancementModelFixtureContents()
	partial := make(map[string]string)
	for name, content := range contents {
		partial[name] = content
		break // 只放一个，缺其余的
	}
	installer, _ := newTestInstaller(t, buildFakeUpstreamArchive(t, partial, nil))

	if _, err := installer.Start(context.Background(), manifestModelsFor(contents)); err != nil {
		t.Fatalf("发起下载失败: %v", err)
	}
	status := waitForInstaller(t, installer)

	if status.Completed || status.Error == "" {
		t.Fatalf("上游包缺文件时必须失败: %+v", status)
	}
}

func TestEnhancementModelInstallerRefusesWithoutManifestHashes(t *testing.T) {
	installer, _ := newTestInstaller(t, nil)

	if _, err := installer.Start(context.Background(), nil); err == nil {
		t.Fatal("没有校验信息时必须拒绝下载，而不是下一份无法验证的权重")
	}
}
