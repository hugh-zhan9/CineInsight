package services

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"video-master/models"
)

const (
	localMLRuntimeDirName = "local_ml_sidecar"
	localMLVenvDirName    = "venv"
	localMLWorkerFileName = "local_ml_worker.py"
	localMLModelDefault   = defaultLocalMLModel
)

var ErrLocalMLRuntimeNotRunning = errors.New("local ML runtime is not running")

//go:embed local_ml_worker.py
var localMLWorkerScript string

// LocalMLVideoEmbeddingRequest describes a video embedding request for local ML.
type LocalMLVideoEmbeddingRequest struct {
	VideoID      uint             `json:"video_id"`
	VideoName    string           `json:"video_name"`
	VideoPath    string           `json:"video_path"`
	SubtitleText string           `json:"subtitle_text"`
	Frames       []AITaggingFrame `json:"frames"`
}

// LocalMLEmbeddingResult stores a normalized embedding produced by local ML.
type LocalMLEmbeddingResult struct {
	Model      string      `json:"model"`
	Source     string      `json:"source"`
	Embedding  []float32   `json:"embedding,omitempty"`
	Embeddings [][]float32 `json:"embeddings,omitempty"`
	Dimension  int         `json:"dimension,omitempty"`
}

// LocalMLRuntimeConfig describes the local model runtime requested by settings.
type LocalMLRuntimeConfig struct {
	Model  string `json:"model"`
	Device string `json:"device"`
}

// LocalMLRuntimeStatus is exposed to the UI for the managed local ML runtime.
type LocalMLRuntimeStatus struct {
	Running      bool      `json:"running"`
	State        string    `json:"state"`
	Model        string    `json:"model"`
	Device       string    `json:"device"`
	Engine       string    `json:"engine"`
	Managed      bool      `json:"managed"`
	StartupError string    `json:"startup_error,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty" ts_type:"string"`
}

// LocalMLRuntime is the boundary for the in-app local ML backend.
type LocalMLRuntime interface {
	EnsureStarted(ctx context.Context, config LocalMLRuntimeConfig) error
	Stop(ctx context.Context) error
	Status() LocalMLRuntimeStatus
	EmbedText(ctx context.Context, text string) (*LocalMLEmbeddingResult, error)
	EmbedTexts(ctx context.Context, texts []string) (*LocalMLEmbeddingResult, error)
	EmbedVideo(ctx context.Context, req LocalMLVideoEmbeddingRequest) (*LocalMLEmbeddingResult, error)
	AnalyzeTags(ctx context.Context, req AITaggingRequest) ([]AITagSuggestion, error)
}

// InProcessLocalMLRuntime keeps the model runtime owned by the desktop app.
type InProcessLocalMLRuntime struct {
	mu           sync.Mutex
	running      bool
	prepared     bool
	modelSpec    string
	modelName    string
	pretrained   string
	device       string
	startedAt    time.Time
	startupError string
	workerMu     sync.Mutex
	workerCmd    *exec.Cmd
	workerStdin  io.WriteCloser
	workerStdout *bufio.Reader
	workerLog    *localMLProcessLog
}

func NewInProcessLocalMLRuntime() *InProcessLocalMLRuntime {
	return &InProcessLocalMLRuntime{}
}

type localMLProcessLog struct {
	mu   sync.Mutex
	data []byte
}

func (b *localMLProcessLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	const maxLocalMLProcessLogBytes = 8192
	if len(b.data) > maxLocalMLProcessLogBytes {
		b.data = append([]byte(nil), b.data[len(b.data)-maxLocalMLProcessLogBytes:]...)
	}
	return len(p), nil
}

func (b *localMLProcessLog) String() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

func (r *InProcessLocalMLRuntime) EnsureStarted(ctx context.Context, config LocalMLRuntimeConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.ensureWorkerScript(); err != nil {
		return err
	}

	spec := strings.TrimSpace(config.Model)
	if spec == "" {
		spec = localMLModelDefault
	}
	device := normalizeLocalMLDevice(config.Device)
	modelName, pretrained := parseLocalMLModelSpec(spec)
	r.mu.Lock()
	if r.running && r.modelSpec == spec && r.device == device {
		r.startupError = ""
		if r.startedAt.IsZero() {
			r.startedAt = time.Now()
		}
		r.mu.Unlock()
		return nil
	}
	r.modelSpec = spec
	r.modelName = modelName
	r.pretrained = pretrained
	r.device = device
	r.running = true
	r.prepared = false
	r.startupError = ""
	if r.startedAt.IsZero() {
		r.startedAt = time.Now()
	}
	r.mu.Unlock()
	r.stopWorker()
	return nil
}

func (r *InProcessLocalMLRuntime) Stop(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.stopWorker()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running = false
	r.prepared = false
	r.startedAt = time.Time{}
	return nil
}

func (r *InProcessLocalMLRuntime) Status() LocalMLRuntimeStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := "stopped"
	if r.running {
		if r.prepared {
			state = "ready"
		} else {
			state = "starting"
		}
	}
	if r.startupError != "" {
		state = "failed"
	}
	model := r.modelSpec
	if model == "" {
		model = localMLModelDefault
	}
	device := r.device
	if device == "" {
		device = defaultLocalMLDevice
	}
	return LocalMLRuntimeStatus{
		Running:      r.running,
		State:        state,
		Model:        model,
		Device:       device,
		Engine:       "local-clip",
		Managed:      true,
		StartupError: r.startupError,
		StartedAt:    r.startedAt,
	}
}

func (r *InProcessLocalMLRuntime) EmbedText(ctx context.Context, text string) (*LocalMLEmbeddingResult, error) {
	result, err := r.EmbedTexts(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(result.Embeddings) == 0 {
		return nil, fmt.Errorf("local ML text embedding returned no vector")
	}
	return &LocalMLEmbeddingResult{
		Model:     result.Model,
		Source:    result.Source,
		Embedding: result.Embeddings[0],
		Dimension: len(result.Embeddings[0]),
	}, nil
}

func (r *InProcessLocalMLRuntime) EmbedTexts(ctx context.Context, texts []string) (*LocalMLEmbeddingResult, error) {
	if err := r.ensurePrepared(ctx); err != nil {
		return nil, err
	}
	texts = filterNonEmptyStrings(texts)
	if len(texts) == 0 {
		return &LocalMLEmbeddingResult{Model: r.modelSpec, Source: "local-clip", Embeddings: [][]float32{}}, nil
	}
	var payload struct {
		Model string   `json:"model"`
		Texts []string `json:"texts"`
	}
	payload.Model = r.modelSpec
	payload.Texts = texts
	var response struct {
		Model      string      `json:"model"`
		Source     string      `json:"source"`
		Embeddings [][]float32 `json:"embeddings"`
		Dimension  int         `json:"dimension"`
	}
	if err := r.runWorker(ctx, "embed-texts", payload, &response); err != nil {
		return nil, err
	}
	return &LocalMLEmbeddingResult{
		Model:      response.Model,
		Source:     response.Source,
		Embeddings: response.Embeddings,
		Dimension:  response.Dimension,
	}, nil
}

func (r *InProcessLocalMLRuntime) EmbedVideo(ctx context.Context, req LocalMLVideoEmbeddingRequest) (*LocalMLEmbeddingResult, error) {
	if err := r.ensurePrepared(ctx); err != nil {
		return nil, err
	}
	var payload struct {
		Model        string           `json:"model"`
		VideoID      uint             `json:"video_id"`
		VideoName    string           `json:"video_name"`
		VideoPath    string           `json:"video_path"`
		SubtitleText string           `json:"subtitle_text"`
		Frames       []AITaggingFrame `json:"frames"`
	}
	payload.Model = r.modelSpec
	payload.VideoID = req.VideoID
	payload.VideoName = req.VideoName
	payload.VideoPath = req.VideoPath
	payload.SubtitleText = req.SubtitleText
	payload.Frames = req.Frames
	var response struct {
		Model     string    `json:"model"`
		Source    string    `json:"source"`
		Embedding []float32 `json:"embedding"`
		Dimension int       `json:"dimension"`
	}
	if err := r.runWorker(ctx, "embed-video", payload, &response); err != nil {
		return nil, err
	}
	return &LocalMLEmbeddingResult{
		Model:     response.Model,
		Source:    response.Source,
		Embedding: response.Embedding,
		Dimension: response.Dimension,
	}, nil
}

func (r *InProcessLocalMLRuntime) AnalyzeTags(ctx context.Context, req AITaggingRequest) ([]AITagSuggestion, error) {
	if err := r.ensurePrepared(ctx); err != nil {
		return nil, err
	}
	existing := make([]models.Tag, 0, len(req.ExistingTags))
	for _, tag := range req.ExistingTags {
		if strings.TrimSpace(tag.Name) == "" {
			continue
		}
		existing = append(existing, tag)
	}
	if len(existing) == 0 {
		return nil, nil
	}
	videoEmbedding, err := r.EmbedVideo(ctx, LocalMLVideoEmbeddingRequest{
		VideoID:      req.Video.ID,
		VideoName:    req.Video.Name,
		VideoPath:    req.Video.Path,
		SubtitleText: req.Evidence.SubtitleText,
		Frames:       req.Evidence.Frames,
	})
	if err != nil {
		return nil, err
	}
	candidates, prompts := buildLocalMLTagPromptCandidates(existing)
	tagEmbeddings, err := r.EmbedTexts(ctx, prompts)
	if err != nil {
		return nil, err
	}
	return buildLocalMLTagSuggestions(req, candidates, videoEmbedding.Embedding, tagEmbeddings.Embeddings)
}

type localMLTagPromptCandidate struct {
	tag   models.Tag
	start int
	end   int
}

func buildLocalMLTagPromptCandidates(tags []models.Tag) ([]localMLTagPromptCandidate, []string) {
	candidates := make([]localMLTagPromptCandidate, 0, len(tags))
	prompts := make([]string, 0, len(tags)*4)
	for _, tag := range tags {
		name := strings.TrimSpace(tag.Name)
		if name == "" {
			continue
		}
		start := len(prompts)
		prompts = append(prompts, localMLTagPrompts(name)...)
		candidates = append(candidates, localMLTagPromptCandidate{
			tag:   tag,
			start: start,
			end:   len(prompts),
		})
	}
	return candidates, prompts
}

func localMLTagPrompts(name string) []string {
	return []string{
		name,
		"视频标签：" + name,
		"这段视频的主题是" + name,
		"画面内容包含" + name,
		"字幕或文件名提到" + name,
	}
}

func buildLocalMLTagSuggestions(req AITaggingRequest, candidates []localMLTagPromptCandidate, videoEmbedding []float32, tagEmbeddings [][]float32) ([]AITagSuggestion, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	if len(tagEmbeddings) == 0 {
		return nil, nil
	}
	type scoredTag struct {
		tag   models.Tag
		score float64
		match string
	}
	scored := make([]scoredTag, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.start < 0 || candidate.end > len(tagEmbeddings) || candidate.start >= candidate.end {
			return nil, fmt.Errorf("local ML tag embedding count mismatch")
		}
		score := 0.0
		for idx := candidate.start; idx < candidate.end; idx++ {
			if promptScore := cosineSimilarity(videoEmbedding, tagEmbeddings[idx]); promptScore > score {
				score = promptScore
			}
		}
		matchType := "existing_semantic"
		if evidenceScore := localMLTagEvidenceScore(req, candidate.tag.Name); evidenceScore > 0 {
			if evidenceScore > score {
				score = evidenceScore
			} else {
				score = math.Min(1.0, score+0.08)
			}
			matchType = "existing_evidence"
		}
		if score <= 0 {
			continue
		}
		scored = append(scored, scoredTag{tag: candidate.tag, score: score, match: matchType})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].tag.ID < scored[j].tag.ID
		}
		return scored[i].score > scored[j].score
	})

	suggestions := make([]AITagSuggestion, 0, len(scored))
	for _, item := range scored {
		confidence := models.AITagConfidenceLow
		switch {
		case item.score >= 0.52:
			confidence = models.AITagConfidenceHigh
		case item.score >= 0.32:
			confidence = models.AITagConfidenceMedium
		case item.score >= 0.20:
			confidence = models.AITagConfidenceLow
		default:
			continue
		}
		suggestions = append(suggestions, AITagSuggestion{
			Label:               item.tag.Name,
			Confidence:          confidence,
			MatchType:           item.match,
			MatchedExistingName: item.tag.Name,
			Reasoning:           fmt.Sprintf("本地多语言 CLIP 候选词表相似度 %.3f", item.score),
		})
		if len(suggestions) >= 8 {
			break
		}
	}
	return suggestions, nil
}

func localMLTagEvidenceScore(req AITaggingRequest, tagName string) float64 {
	tagName = strings.TrimSpace(tagName)
	if tagName == "" {
		return 0
	}
	tagKey := strings.ToLower(tagName)
	for _, text := range []string{req.Video.Name, req.Video.Path, req.Video.Directory, req.Evidence.SubtitleText} {
		if strings.Contains(strings.ToLower(text), tagKey) {
			return 0.62
		}
	}
	lowerTag := strings.ToLower(tagName)
	if req.Evidence.DetectedFaceCount > 0 && containsAny(lowerTag, "人", "人物", "人脸", "face", "person", "people") {
		return 0.56
	}
	if req.Video.Height > req.Video.Width && containsAny(lowerTag, "竖屏", "纵向", "portrait") {
		return 0.54
	}
	if req.Video.Width >= req.Video.Height && req.Video.Width > 0 && req.Video.Height > 0 && containsAny(lowerTag, "横屏", "横向", "landscape") {
		return 0.54
	}
	if req.Video.Height >= 2160 && containsAny(lowerTag, "4k", "超清", "2160") {
		return 0.54
	}
	if req.Video.Height >= 1080 && containsAny(lowerTag, "高清", "1080") {
		return 0.50
	}
	return 0
}

func (r *InProcessLocalMLRuntime) ensurePrepared(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	prepared := r.prepared
	spec := r.modelSpec
	running := r.running
	r.mu.Unlock()
	if !running {
		return ErrLocalMLRuntimeNotRunning
	}
	if prepared {
		return nil
	}

	if err := r.prepareEnvironment(ctx, spec); err != nil {
		r.mu.Lock()
		r.startupError = err.Error()
		r.mu.Unlock()
		return err
	}

	r.mu.Lock()
	r.prepared = true
	r.startupError = ""
	r.mu.Unlock()
	return nil
}

func (r *InProcessLocalMLRuntime) prepareEnvironment(ctx context.Context, spec string) error {
	basePython, err := findLocalMLBasePython()
	if err != nil {
		return err
	}
	venvPython, err := r.ensureVenv(basePython)
	if err != nil {
		return err
	}
	if err := r.ensureDependencies(ctx, venvPython); err != nil {
		return err
	}
	if spec == "" {
		spec = localMLModelDefault
	}
	return nil
}

func (r *InProcessLocalMLRuntime) ensureDependencies(ctx context.Context, venvPython string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	env, err := r.workerEnvironment()
	if err != nil {
		return err
	}
	if r.localMLIsInstalled(venvPython, env) {
		return nil
	}
	upgrade := exec.CommandContext(ctx, venvPython, "-m", "pip", "install", "--upgrade", "pip", "setuptools", "wheel")
	upgrade.Env = env
	if output, err := upgrade.CombinedOutput(); err != nil {
		return fmt.Errorf("升级本地 ML pip 依赖失败: %s", strings.TrimSpace(string(output)))
	}
	install := exec.CommandContext(ctx, venvPython, "-m", "pip", "install", "torch", "torchvision", "open_clip_torch", "Pillow", "numpy")
	install.Env = env
	if output, err := install.CombinedOutput(); err != nil {
		return fmt.Errorf("安装本地 ML 依赖失败: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func (r *InProcessLocalMLRuntime) localMLIsInstalled(venvPython string, env []string) bool {
	cmd := exec.Command(venvPython, "-c", `import open_clip, torch, PIL, numpy; print("ok")`)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	return err == nil && strings.TrimSpace(string(output)) == "ok"
}

func (r *InProcessLocalMLRuntime) ensureVenv(basePython string) (string, error) {
	if err := os.MkdirAll(r.runtimeDir(), 0755); err != nil {
		return "", err
	}
	venvPython := r.venvPythonPath()
	if _, err := os.Stat(venvPython); err == nil {
		return venvPython, nil
	}
	cmd := exec.Command(basePython, "-m", "venv", r.venvDir())
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("创建本地 ML 虚拟环境失败: %s", strings.TrimSpace(string(output)))
	}
	return venvPython, nil
}

func (r *InProcessLocalMLRuntime) runtimeDir() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".video-master", localMLRuntimeDirName)
}

func (r *InProcessLocalMLRuntime) venvDir() string {
	return filepath.Join(r.runtimeDir(), localMLVenvDirName)
}

func (r *InProcessLocalMLRuntime) venvPythonPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(r.venvDir(), "Scripts", "python.exe")
	}
	return filepath.Join(r.venvDir(), "bin", "python3")
}

func (r *InProcessLocalMLRuntime) workerPath() string {
	return filepath.Join(r.runtimeDir(), localMLWorkerFileName)
}

func (r *InProcessLocalMLRuntime) ensureWorkerScript() error {
	if err := os.MkdirAll(r.runtimeDir(), 0755); err != nil {
		return err
	}
	path := r.workerPath()
	if data, err := os.ReadFile(path); err == nil && string(data) == localMLWorkerScript {
		return nil
	}
	return os.WriteFile(path, []byte(localMLWorkerScript), 0644)
}

func (r *InProcessLocalMLRuntime) runWorker(ctx context.Context, mode string, payload any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	r.workerMu.Lock()
	defer r.workerMu.Unlock()
	if err := r.ensurePersistentWorkerLocked(ctx); err != nil {
		return err
	}

	requestBody, err := json.Marshal(map[string]json.RawMessage{
		"mode":    json.RawMessage(strconv.Quote(mode)),
		"payload": payloadBytes,
	})
	if err != nil {
		return err
	}
	if _, err := r.workerStdin.Write(append(requestBody, '\n')); err != nil {
		detail := r.workerErrorDetail(err.Error())
		r.stopWorkerLocked()
		return fmt.Errorf("本地 ML 推理失败: %s", detail)
	}

	type readResult struct {
		line []byte
		err  error
	}
	readCh := make(chan readResult, 1)
	go func() {
		line, err := r.workerStdout.ReadBytes('\n')
		readCh <- readResult{line: line, err: err}
	}()

	select {
	case <-ctx.Done():
		r.stopWorkerLocked()
		return ctx.Err()
	case result := <-readCh:
		if result.err != nil {
			detail := r.workerErrorDetail(result.err.Error())
			r.stopWorkerLocked()
			return fmt.Errorf("本地 ML 推理失败: %s", detail)
		}
		var response struct {
			OK     bool            `json:"ok"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		if err := json.Unmarshal(result.line, &response); err != nil {
			detail := strings.TrimSpace(string(result.line))
			if detail == "" {
				detail = err.Error()
			}
			r.stopWorkerLocked()
			return fmt.Errorf("本地 ML 输出解析失败: %s", detail)
		}
		if !response.OK {
			detail := strings.TrimSpace(response.Error)
			if detail == "" {
				detail = r.workerErrorDetail("unknown worker error")
			}
			return fmt.Errorf("本地 ML 推理失败: %s", detail)
		}
		if err := json.Unmarshal(response.Result, out); err != nil {
			return fmt.Errorf("本地 ML 输出解析失败: %w", err)
		}
		return nil
	}
}

func (r *InProcessLocalMLRuntime) ensurePersistentWorkerLocked(ctx context.Context) error {
	if r.workerCmd != nil && r.workerStdin != nil && r.workerStdout != nil {
		return nil
	}
	venvPython, err := r.ensureVenvPython(ctx)
	if err != nil {
		return err
	}
	env, err := r.workerEnvironment()
	if err != nil {
		return err
	}
	r.mu.Lock()
	device := normalizeLocalMLDevice(r.device)
	r.mu.Unlock()
	cmd := exec.Command(venvPython, r.workerPath(), "--mode", "serve", "--device", device)
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	logBuffer := &localMLProcessLog{}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动本地 ML worker 失败: %w", err)
	}
	go func() {
		_, _ = io.Copy(logBuffer, stderr)
	}()
	r.workerCmd = cmd
	r.workerStdin = stdin
	r.workerStdout = bufio.NewReader(stdout)
	r.workerLog = logBuffer
	return nil
}

func (r *InProcessLocalMLRuntime) stopWorker() {
	r.workerMu.Lock()
	defer r.workerMu.Unlock()
	r.stopWorkerLocked()
}

func (r *InProcessLocalMLRuntime) stopWorkerLocked() {
	if r.workerStdin != nil {
		_ = r.workerStdin.Close()
	}
	if r.workerCmd != nil && r.workerCmd.Process != nil {
		_ = r.workerCmd.Process.Kill()
		_ = r.workerCmd.Wait()
	}
	r.workerCmd = nil
	r.workerStdin = nil
	r.workerStdout = nil
	r.workerLog = nil
}

func (r *InProcessLocalMLRuntime) workerErrorDetail(fallback string) string {
	detail := strings.TrimSpace(r.workerLog.String())
	if detail == "" {
		detail = strings.TrimSpace(fallback)
	}
	if detail == "" {
		detail = "unknown worker error"
	}
	return detail
}

func (r *InProcessLocalMLRuntime) ensureVenvPython(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.mu.Lock()
	prepared := r.prepared
	r.mu.Unlock()
	if !prepared {
		if err := r.ensurePrepared(ctx); err != nil {
			return "", err
		}
	}
	venvPython := r.venvPythonPath()
	if _, err := os.Stat(venvPython); err != nil {
		return "", err
	}
	return venvPython, nil
}

func (r *InProcessLocalMLRuntime) workerEnvironment() ([]string, error) {
	dirs := []string{
		r.runtimeDir(),
		filepath.Join(r.runtimeDir(), "hf"),
		filepath.Join(r.runtimeDir(), "hf", "hub"),
		filepath.Join(r.runtimeDir(), "torch"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	model := r.modelSpec
	device := r.device
	r.mu.Unlock()
	if model == "" {
		model = localMLModelDefault
	}
	device = normalizeLocalMLDevice(device)
	return append(os.Environ(),
		"PYTHONUNBUFFERED=1",
		"PIP_DISABLE_PIP_VERSION_CHECK=1",
		"PIP_PROGRESS_BAR=off",
		"HF_HUB_DISABLE_XET=1",
		"HF_HOME="+filepath.Join(r.runtimeDir(), "hf"),
		"HF_HUB_CACHE="+filepath.Join(r.runtimeDir(), "hf", "hub"),
		"TORCH_HOME="+filepath.Join(r.runtimeDir(), "torch"),
		"LOCAL_ML_MODEL="+model,
		"LOCAL_ML_DEVICE="+device,
	), nil
}

func parseLocalMLModelSpec(spec string) (model string, pretrained string) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		spec = localMLModelDefault
	}
	defaultModel, defaultPretrained := splitLocalMLModelSpec(localMLModelDefault)
	if strings.Contains(spec, "::") {
		parts := strings.SplitN(spec, "::", 2)
		model = strings.TrimSpace(parts[0])
		pretrained = strings.TrimSpace(parts[1])
		if model == "" {
			model = defaultModel
		}
		if pretrained == "" {
			pretrained = defaultPretrained
		}
		return model, pretrained
	}
	return spec, defaultPretrained
}

func splitLocalMLModelSpec(spec string) (model string, pretrained string) {
	parts := strings.SplitN(spec, "::", 2)
	model = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		pretrained = strings.TrimSpace(parts[1])
	}
	if model == "" {
		model = "xlm-roberta-base-ViT-B-32"
	}
	if pretrained == "" {
		pretrained = "laion5b_s13b_b90k"
	}
	return model, pretrained
}

func findLocalMLBasePython() (string, error) {
	candidates := []string{}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/opt/homebrew/bin/python3",
			"/usr/local/bin/python3",
			"/Library/Frameworks/Python.framework/Versions/3.12/bin/python3",
			"/Library/Frameworks/Python.framework/Versions/3.11/bin/python3",
			"/Library/Frameworks/Python.framework/Versions/3.10/bin/python3",
			"/usr/bin/python3",
		)
	} else if runtime.GOOS == "windows" {
		candidates = append(candidates, "python.exe", "python3.exe")
	} else {
		candidates = append(candidates, "/usr/bin/python3", "/usr/local/bin/python3")
	}
	candidates = append(candidates, "python3", "python")
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		path := candidate
		if !strings.Contains(candidate, string(os.PathSeparator)) {
			if lookedUp, err := exec.LookPath(candidate); err == nil {
				path = lookedUp
			} else {
				continue
			}
		}
		if pythonMeetsMinimumVersion(path) {
			return path, nil
		}
	}
	return "", fmt.Errorf("未找到可用的 Python 3.10+ 解释器")
}

func pythonMeetsMinimumVersion(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	cmd := exec.Command(path, "-c", `import sys; print(f"{sys.version_info[0]}.{sys.version_info[1]}")`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	version := strings.TrimSpace(string(output))
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return major > 3 || (major == 3 && minor >= 10)
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, aa, bb float64
	for i := 0; i < n; i++ {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		aa += av * av
		bb += bv * bv
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return dot / (math.Sqrt(aa) * math.Sqrt(bb))
}

func filterNonEmptyStrings(values []string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered
}
