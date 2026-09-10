package services

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Model an arbitrarily large remote file without allocating or reading one.
type countingSourceReader struct {
	bytesRead int
	overread  bool
}

func (r *countingSourceReader) Read(p []byte) (int, error) {
	if r.bytesRead+len(p) > typeScriptSampleLimit {
		r.overread = true
		return 0, errors.New("attempted to read beyond the sniffing budget")
	}
	for i := range p {
		p[i] = 'x'
	}
	r.bytesRead += len(p)
	return len(p), nil
}

func TestTypeScriptDetectionReadsOnlyBoundedPrefix(t *testing.T) {
	r := &countingSourceReader{}
	if isTypeScriptSource(r) {
		t.Fatal("video-like stream must not be excluded")
	}
	if r.overread || r.bytesRead != typeScriptSampleLimit {
		t.Fatalf("read %d bytes, want only %d", r.bytesRead, typeScriptSampleLimit)
	}
	// A marker outside the prefix must not cause a video to disappear from scans.
	path := filepath.Join(t.TempDir(), "recording.ts")
	data := append(bytes.Repeat([]byte{'x'}, typeScriptSampleLimit), []byte("export const metadata = 1")...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if isKnownNonVideoSourcePath(path) {
		t.Fatal("read beyond the bounded file header")
	}
}

func TestTypeScriptDetectionRetainsSourceFiltering(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		want       bool
	}{
		{"source", "// 中文注释\nexport const value = 1", true},
		{"interface", "interface Clip { name: string }", true},
		{"empty", "", false},
		{"video", "\x47\x00\x11\x10export const video payload", false},
		{"unknown", "plain video sample", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTypeScriptSource(strings.NewReader(tc.data)); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	for _, path := range []string{"missing.d.ts", "node_modules/pkg/source.ts"} {
		if !isKnownNonVideoSourcePath(path) {
			t.Fatalf("known source path was not excluded: %s", path)
		}
	}
}

func TestTypeScriptDetectionReadFailureDoesNotExcludeVideo(t *testing.T) {
	if isTypeScriptSource(failingSourceReader{}) {
		t.Fatal("failed reads must not classify a video as source code")
	}
}

type failingSourceReader struct{}

func (failingSourceReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
