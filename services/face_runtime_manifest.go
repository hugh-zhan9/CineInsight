package services

import (
	"fmt"
	"strings"
)

// 人脸 sidecar 的身份清单（D-016）。
//
// 三件事在这里钉死，别处不得再出现第二份口径：
//   - Python 依赖的版本（pinned；升级是一次显式改动，不是 pip 的自由）；
//   - 模型包的来源 URL 与 sha256；
//   - 需要从包里取出的模型文件与各自的 sha256。
//
// 校验对不上就整批丢弃、状态置 download_failed：宁可不可用，也不拿一份来路不明
// 的权重去认人（与超分模型同一条口径）。
const (
	// faceRuntimeIdentity 出现在状态里，方便"我这台机器装的是哪一版"这个问题
	// 有一个确定答案。改依赖或改模型都要改它。
	faceRuntimeIdentity = "insightface-1.0.1-buffalo_l-onnxruntime-1.23.1"

	faceRuntimeDirName = "face-runtime"
	faceVenvDirName    = "venv"
	faceModelDirName   = "models"
	faceWorkerFileName = "face_worker.py"

	// 模型包来源。GitHub release 资产；FaceModelMirrorURL 非空时替换其前缀
	// （本机直连 GitHub 未必通，见计划 Goal）。
	faceModelArchiveURL     = "https://github.com/deepinsight/insightface/releases/download/v0.7/buffalo_l.zip"
	faceModelArchiveSHA256  = "80ffe37d8a5940d59a7384c201a2a38d4741f2f3c51eef46ebb28218a7b0ca2f"
	faceModelArchiveSize    = int64(288621354)
	faceModelArchiveMaxSize = int64(512) << 20
	faceModelFileMaxSize    = int64(256) << 20

	// 模型包解开后落在 models/buffalo_l/ 下：insightface 的 FaceAnalysis 按
	// <root>/models/<name>/ 找模型，目录名就是模型包名。
	faceModelPackName = "buffalo_l"
)

// faceRuntimePackages 是 venv 里安装的固定版本依赖。
//
// insightface 只装它自己（--no-deps）：它声明依赖 opencv-python（带 GUI 的那份），
// 而 opencv-python 与 opencv-python-headless 提供同一个 cv2 模块，两个都装会互相
// 覆盖文件。这里显式装齐它运行时真正 import 的几个包，再单独装 insightface。
var faceRuntimePackages = []string{
	"onnxruntime==1.23.1",
	"numpy==2.2.6",
	"opencv-python-headless==4.12.0.88",
	"scipy==1.15.3",
	"scikit-image==0.25.2",
	"tqdm==4.70.0",
	"requests==2.34.2",
	"onnx==1.22.0",
}

// faceRuntimeNoDepsPackages 是必须绕开依赖解析安装的包（理由见上）。
var faceRuntimeNoDepsPackages = []string{
	"insightface==1.0.1",
}

// facePythonMinMinor / facePythonMaxMinor 是可用的 CPython 3.x 区间。
// 上限来自 onnxruntime 1.23.1 的 wheel 只发到 cp313——本机的 Homebrew Python
// 3.14 装不上它，因此人脸这一侧不能沿用 WhisperX 的"3.10 及以上都行"。
const (
	facePythonMinMinor = 10
	facePythonMaxMinor = 13
)

// faceModelFile 是模型包里需要取出的一个文件。
type faceModelFile struct {
	Name   string
	SHA256 string
	Bytes  int64
}

// faceModelFiles 是检测与向量两个模型（D-016）。包里另外三个文件
// （2d106det / 1k3d68 / genderage）本切片用不到，不解压也不校验：
// allowed_modules 只开 detection 与 recognition。
var faceModelFiles = []faceModelFile{
	{Name: "det_10g.onnx", SHA256: "5838f7fe053675b1c7a08b633df49e7af5495cee0493c7dcf6697200b85b5b91", Bytes: 16923827},
	{Name: "w600k_r50.onnx", SHA256: "4c06341c33c2ca1f86781dab0e829f88ad5b64be9fba56e56bc9ebdefc619e43", Bytes: 174383860},
}

// faceModelDownloadURL 把镜像前缀拼到官方地址上。
//
// 约定与其他镜像开关一致：mirror 以官方 URL 整体为后缀（`https://镜像/https://github.com/...`，
// ghproxy 一类代理就是这个形状）。mirror 为空时用官方地址。
func faceModelDownloadURL(mirror string) string {
	mirror = strings.TrimSpace(mirror)
	if mirror == "" {
		return faceModelArchiveURL
	}
	return strings.TrimRight(mirror, "/") + "/" + faceModelArchiveURL
}

// faceModelTotalBytes 是两个模型文件解压后的字节数，供设置页说明下载量。
func faceModelTotalBytes() int64 {
	var total int64
	for _, file := range faceModelFiles {
		total += file.Bytes
	}
	return total
}

// describeFaceModelSize 给界面一句"要下多少"。
func describeFaceModelSize() string {
	return fmt.Sprintf("%.0f MB", float64(faceModelArchiveSize)/(1024*1024))
}
