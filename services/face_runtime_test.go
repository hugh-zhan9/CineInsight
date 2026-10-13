package services

import (
	"context"
	"strings"
	"testing"
)

func newFaceRuntimeForTest(t *testing.T) *FaceRuntime {
	t.Helper()
	r := NewFaceRuntime(t.TempDir(), nil)
	r.probe = func(context.Context, appleAIRequest) (appleAIResponse, error) {
		return appleAIResponse{Supported: true, ModelID: faceRuntimeIdentity}, nil
	}
	return r
}
func TestFaceAppleRuntime(t *testing.T) {
	r := newFaceRuntimeForTest(t)
	if !r.Available() {
		t.Fatal(r.Status())
	}
	if _, err := r.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.probe = func(context.Context, appleAIRequest) (appleAIResponse, error) {
		return appleAIResponse{Supported: true, ModelID: "wrong"}, nil
	}
	if r.Available() {
		t.Fatal("accepted different model")
	}
}

// An older or newer macOS without the pinned faceprint revision is reported as clearly unavailable.
func TestFaceAppleRuntimeReportsMissingSystemRevision(t *testing.T) {
	r := newFaceRuntimeForTest(t)
	for _, code := range []string{"apple_face_unsupported", "apple_face_version_changed"} {
		r.probe = func(context.Context, appleAIRequest) (appleAIResponse, error) {
			return appleAIResponse{}, appleAIError(code)
		}
		status := r.Status()
		if status.State != FaceRuntimeStateIncompatible || !strings.Contains(status.Reason, "人脸识别不可用") || strings.Contains(status.Reason, code) {
			t.Fatalf("%s: %+v", code, status)
		}
	}
}
