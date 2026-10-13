package services

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Runs the Apple AI helper's pure Swift logic tests (snapshot accumulation and its logged replays, word timing,
// error classification, language detection decisions) so `go test ./...` covers them on a Mac with a toolchain.
func TestAppleAISwiftLogic(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the Swift logic tests need macOS")
	}
	// Check for a developer directory first: on a Mac without one, xcrun would open the install dialog.
	if err := exec.Command("/usr/bin/xcode-select", "-p").Run(); err != nil {
		t.Skip("no Xcode or Command Line Tools developer directory")
	}
	if err := exec.Command("/usr/bin/xcrun", "--find", "swiftc").Run(); err != nil {
		t.Skip("swiftc not available")
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source")
	}
	script := filepath.Join(filepath.Dir(source), "..", "scripts", "test_apple_ai_logic.sh")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "/bin/bash", script).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}
