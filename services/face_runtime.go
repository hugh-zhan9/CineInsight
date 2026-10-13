package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	FaceRuntimeStateAvailable    = "available"
	FaceRuntimeStateIncompatible = "incompatible"
	faceRuntimeIdentity          = "apple-faceprint-cpu-r3737841669-pad25-v1"
	faceRuntimeDirName           = "face-runtime"
)

var ErrFaceRuntimeUnsupported = errors.New("苹果人脸能力不可用")
var ErrFaceRuntimeUnavailable = errors.New("苹果人脸能力未就绪")
var ErrFaceDataDirUnavailable = errors.New("人脸数据目录不可用：应用数据目录未解析")

type FaceRuntimeStatus struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
	// Identity 固定系统接口版本与预处理口径。
	Identity   string `json:"identity"`
	RuntimeDir string `json:"runtime_dir"`
	Preparing  bool   `json:"preparing"`
}

// FaceRuntime 仅检查随包的系统能力，不下载解释器或模型。
type FaceRuntime struct {
	baseDir        string
	mu             sync.Mutex
	emitter        func(FaceRuntimeStatus)
	observedMu     sync.Mutex
	observedStatus *FaceRuntimeStatus
	observedAt     time.Time
	probe          appleAIRunner
}

func NewFaceRuntime(dataDir string, _ func() string) *FaceRuntime {
	return &FaceRuntime{baseDir: dataDir, probe: runAppleAI}
}
func (r *FaceRuntime) RuntimeDir() string {
	if r == nil || strings.TrimSpace(r.baseDir) == "" {
		return ""
	}
	return filepath.Join(r.baseDir, faceRuntimeDirName)
}
func (r *FaceRuntime) DirAvailable() bool { return r != nil && r.RuntimeDir() != "" }
func (r *FaceRuntime) SetEventEmitter(emitter func(FaceRuntimeStatus)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.emitter = emitter
	r.mu.Unlock()
}
func (r *FaceRuntime) check(ctx context.Context) FaceRuntimeStatus {
	status := FaceRuntimeStatus{State: FaceRuntimeStateIncompatible, Identity: faceRuntimeIdentity}
	if !r.DirAvailable() {
		status.Reason = ErrFaceDataDirUnavailable.Error()
		return status
	}
	status.RuntimeDir = r.RuntimeDir()
	response, err := r.probe(ctx, appleAIRequest{Operation: "face_status"})
	if err != nil {
		status.Reason = appleAIReason(err)
		return status
	}
	if !response.Supported || response.ModelID != faceRuntimeIdentity {
		status.Reason = "系统人脸特征接口不可用或版本不匹配"
		return status
	}
	status.State = FaceRuntimeStateAvailable
	return status
}
func (r *FaceRuntime) Status() FaceRuntimeStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	status := r.check(ctx)
	if r != nil {
		r.observedMu.Lock()
		r.observedStatus = &status
		r.observedAt = time.Now()
		r.observedMu.Unlock()
	}
	return status
}
func (r *FaceRuntime) Available() bool {
	return r != nil && r.Status().State == FaceRuntimeStateAvailable
}

// Prepare 保留重新检查入口；不会安装任何内容。
func (r *FaceRuntime) Prepare(ctx context.Context) (FaceRuntimeStatus, error) {
	if !r.DirAvailable() {
		return r.Status(), ErrFaceDataDirUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	status := r.check(ctx)
	r.mu.Lock()
	emit := r.emitter
	r.mu.Unlock()
	if emit != nil {
		emit(status)
	}
	if status.State != FaceRuntimeStateAvailable {
		return status, fmt.Errorf("%w: %s", ErrFaceRuntimeUnavailable, status.Reason)
	}
	return status, nil
}
func (r *FaceRuntime) StopAndWait() {}
func (r *FaceRuntime) WorkerEnvironment() (string, string, []string, error) {
	if !r.DirAvailable() {
		return "", "", nil, ErrFaceDataDirUnavailable
	}
	status := r.Status()
	if status.State != FaceRuntimeStateAvailable {
		return "", "", nil, fmt.Errorf("%w: %s", ErrFaceRuntimeUnavailable, status.Reason)
	}
	return appleAIExecutable(), "--face-worker", os.Environ(), nil
}
