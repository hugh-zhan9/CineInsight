package services

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeRuntimeRejectsMissingAndForgedManifest(t *testing.T) {
	root := t.TempDir()
	checker := &nativeRuntime{dir: root}
	capability, path := checker.probe()
	if capability.Available || path != "" {
		t.Fatal("missing runtime accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(`{"runtime_id":"libmpv-0.41.0-iina-1.5.0-arm64","entry":"../../outside.dylib","source_materials_complete":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	checker = &nativeRuntime{dir: root}
	capability, path = checker.probe()
	if capability.Available || path != "" {
		t.Fatal("forged manifest accepted")
	}
}
func TestNativeRuntimeVerifiesPinnedLocalFixture(t *testing.T) {
	root := os.Getenv("CINEINSIGHT_TEST_NATIVE_RUNTIME")
	if root == "" {
		t.Skip("explicit pinned runtime fixture required")
	}
	checker := &nativeRuntime{dir: root}
	capability, path := checker.probe()
	if !capability.Available || path != filepath.Join(root, "lib", "libmpv.2.dylib") {
		t.Fatalf("pinned runtime: %+v %s", capability, path)
	}
	// Do not modify a shared runtime fixture. A synthetic stamp proves later replacement is rejected.
	checker.stamps = append(checker.stamps, nativeRuntimeStamp{path: filepath.Join(t.TempDir(), "missing"), info: checker.stamps[0].info})
	capability, path = checker.probe()
	if capability.Available || path != "" || capability.ReasonCode != "native_runtime_mismatch" {
		t.Fatal("changed runtime remained usable")
	}
}

func TestNativeReleaseRuntimeNeverFallsBackOutsideItsBundle(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "CineInsight.app", "Contents", "MacOS", "CineInsight")
	outside := filepath.Join(t.TempDir(), "build", "native-playback", "runtime")
	if err := os.MkdirAll(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "manifest.json"), []byte(`{"runtime_id":"libmpv-0.41.0-iina-1.5.0-arm64"}`), 0600); err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(filepath.Dir(filepath.Dir(executable)), "Resources", "native-playback")
	if got := nativeRuntimeDirFor(executable, outside); got != expected {
		t.Fatalf("bundle loaded outside runtime: %s", got)
	}
	if got := nativeRuntimeDirFor(filepath.Join(t.TempDir(), "development-binary"), outside); got != outside {
		t.Fatal("explicit development fixture was ignored")
	}
}
