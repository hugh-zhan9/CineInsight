package services

import (
	"context"
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
