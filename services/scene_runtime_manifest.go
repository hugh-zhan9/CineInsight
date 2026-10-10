package services

import (
	"fmt"
	"strings"
)

// 场景检索 sidecar 的身份清单（场景检索合同「选型与实测」「本地运行时」）。
//
// 依赖版本、模型来源（固定提交）与每个模型文件的 sha256 只在这里出现。校验对不上
// 就整批丢弃、状态置 download_failed：宁可不可用，也不拿来路不明的权重去建索引。
const (
	// SceneLocalModelID 是本地画面向量的模型标识，写进 scene_index_states.model_id 与
	// scene_visual_segments.model_id；换模型或改预处理必须改它，旧段随即不再参与查询。
	SceneLocalModelID = "cn-clip-vit-b16-q8@1"

	// sceneRuntimeIdentity 记在 venv 与模型标记文件里，回答"这台机器装的是哪一版"。
	// 改依赖或改模型都要改它。
	sceneRuntimeIdentity = "scene-cn-clip-vit-b16-q8@1-onnxruntime-1.23.2"

	sceneRuntimeDirName = "scene-runtime"
	sceneVenvDirName    = "venv"
	sceneModelsDirName  = "models"
	sceneModelPackName  = "cn-clip-vit-b16-q8"
	sceneWorkerFileName = "scene_worker.py"
	sceneTempDirName    = ".tmp"

	// 模型来源：Hugging Face 上 Xenova 的 ONNX 转换件，按提交固定。
	// SceneModelMirrorURL 非空时替换主机部分（例如 https://hf-mirror.com）。
	sceneModelDefaultHost = "https://huggingface.co"
	sceneModelRepo        = "Xenova/chinese-clip-vit-base-patch16"
	sceneModelRevision    = "f26904860903e70e050b8f48255e5f48401816e9"
	sceneModelFileMaxSize = int64(256) << 20
)

// sceneRuntimePackages 是 venv 里逐个固定版本的依赖。
//
// 版本以"托管 Python 3.10 能装上"为准：托管解释器是 3.10，onnxruntime 1.24 起、
// numpy 2.3 起都不再发 cp310 的 wheel（合同里的本机实测用的是 3.14 + onnxruntime 1.31，
// 那一版在 3.10 上装不上）。onnxruntime / numpy 与人脸运行时同代。
var sceneRuntimePackages = []string{
	"onnxruntime==1.23.2",
	"numpy==2.2.6",
	"pillow==12.3.0",
	"tokenizers==0.23.3",
}

// scenePythonMinMinor / scenePythonMaxMinor：可用的 CPython 3.x 区间，与人脸运行时同口径。
const (
	scenePythonMinMinor = 10
	scenePythonMaxMinor = 13
)

// sceneModelFile 是需要下载并校验的一个模型文件。Remote 是仓库内路径，Name 是本地文件名。
type sceneModelFile struct {
	Remote string
	Name   string
	SHA256 string
	Bytes  int64
}

var sceneModelFiles = []sceneModelFile{
	{Remote: "onnx/model_quantized.onnx", Name: "model_quantized.onnx", SHA256: "a2c2849329eafb24100acec5a9421a39f8cb445296444c5c98855b69e5e7a751", Bytes: 190842404},
	{Remote: "tokenizer.json", Name: "tokenizer.json", SHA256: "7dfbf1966ebf99d471c3796e9b457329d2b2182b817e144f1e904b957745c839", Bytes: 439124},
	{Remote: "preprocessor_config.json", Name: "preprocessor_config.json", SHA256: "61a78fdd2c7ac17b54b6190c0f4cb23423192c535003d52528d01e318a47608b", Bytes: 546},
}

// sceneModelHost 返回实际下载用的主机：镜像设置非空时用镜像，否则用官方主机。
func sceneModelHost(mirror string) string {
	mirror = strings.TrimRight(strings.TrimSpace(mirror), "/")
	if mirror == "" {
		return sceneModelDefaultHost
	}
	return mirror
}

// sceneModelFileURL 拼出一个模型文件的固定提交下载地址。
func sceneModelFileURL(mirror string, file sceneModelFile) string {
	return fmt.Sprintf("%s/%s/resolve/%s/%s", sceneModelHost(mirror), sceneModelRepo, sceneModelRevision, file.Remote)
}

func sceneModelTotalBytes(files []sceneModelFile) int64 {
	var total int64
	for _, file := range files {
		total += file.Bytes
	}
	return total
}

func describeSceneModelSize(files []sceneModelFile) string {
	return fmt.Sprintf("%.0f MB", float64(sceneModelTotalBytes(files))/(1024*1024))
}
