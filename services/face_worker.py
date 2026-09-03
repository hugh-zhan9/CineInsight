#!/usr/bin/env python3
"""CineInsight 人脸 sidecar（D-016）。

协议：stdin 每行一个 JSON 请求 {"id": "...", "path": "/tmp/x.jpg"}，
stdout 每行一个 JSON 响应：

    {"id": "...", "faces": [{"bbox": [x, y, w, h], "quality": 0.87,
                             "embedding": "<base64 float32*512>"}]}

bbox 是相对坐标（0–1，相对于解码后的图像），embedding 是 L2 归一化后的
512 维 float32 小端序 base64。没检出人脸时 faces 为空数组；这一条图出错时
返回 {"id": "...", "error": "..."}——单张失败不拖垮常驻进程；错误文案只给
不含路径的摘要（路径由 Go 侧按媒体 id 定位）。

模型加载完成后先打一行 {"ready": true}，Go 侧凭它区分"还在加载"与"卡死了"。

进程常驻，由 Go 侧负责超时与 kill。除本地模型文件外不访问网络：
insightface 的自动下载靠"模型已经在 root 下"来规避。
"""

import base64
import contextlib
import json
import os
import sys
import traceback

MODEL_ROOT_ENV = "CINEINSIGHT_FACE_MODEL_ROOT"
MODEL_PACK_ENV = "CINEINSIGHT_FACE_MODEL_PACK"
DET_SIZE_ENV = "CINEINSIGHT_FACE_DET_SIZE"
MAX_FACES_ENV = "CINEINSIGHT_FACE_MAX_FACES"

# 识别模型的输入边长：比这更小的脸是被放大后送进去的，质量分要打折。
RECOGNITION_INPUT_EDGE = 112.0


# 我们自己抛的 RuntimeError 文案里没有路径，可以原样回传；解释器抛的异常
# （FileNotFoundError 之类）会把输入路径写进 str(exc)，而这条文案会进 Go 侧的
# 日志与状态，因此只回一个不含路径的摘要（D-020）。
ERROR_SUMMARIES = {
    "FileNotFoundError": "input file missing",
    "IsADirectoryError": "input path is a directory",
    "PermissionError": "input file unreadable",
    "MemoryError": "out of memory",
    "OSError": "input file read failed",
    "ValueError": "unsupported image data",
}


def error_summary(exc):
    if isinstance(exc, RuntimeError):
        return str(exc)
    name = type(exc).__name__
    return ERROR_SUMMARIES.get(name, name)


def emit(payload):
    sys.stdout.write(json.dumps(payload, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def load_app():
    root = os.environ.get(MODEL_ROOT_ENV, "").strip()
    if not root:
        raise RuntimeError("%s 未设置" % MODEL_ROOT_ENV)
    pack = os.environ.get(MODEL_PACK_ENV, "buffalo_l").strip() or "buffalo_l"
    try:
        det_size = int(os.environ.get(DET_SIZE_ENV, "640"))
    except ValueError:
        det_size = 640
    # insightface 与 onnxruntime 都会往 stdout 打加载日志，而 stdout 是协议通道，
    # 混进去一行就把 JSON 流毁了。整个加载过程改道 stderr。
    with contextlib.redirect_stdout(sys.stderr):
        from insightface.app import FaceAnalysis

        app = FaceAnalysis(
            name=pack,
            root=root,
            allowed_modules=["detection", "recognition"],
            providers=["CPUExecutionProvider"],
        )
        app.prepare(ctx_id=-1, det_size=(det_size, det_size))
    return app


def read_image(path, cv2, np):
    # 不用 cv2.imread：它在非 ASCII 路径上会静默失败，而图库里全是中文目录名。
    with open(path, "rb") as handle:
        data = handle.read()
    if not data:
        raise RuntimeError("文件为空")
    image = cv2.imdecode(np.frombuffer(data, dtype=np.uint8), cv2.IMREAD_COLOR)
    if image is None:
        raise RuntimeError("无法解码图像")
    return image


def face_quality(bbox, width, height):
    x1, y1, x2, y2 = bbox
    face_w = max(0.0, float(x2) - float(x1))
    face_h = max(0.0, float(y2) - float(y1))
    if face_w <= 0 or face_h <= 0 or width <= 0 or height <= 0:
        return 0.0
    # 尺寸因子：短边不到识别输入边长的脸是插值放大来的，向量可信度更低。
    size_factor = min(1.0, min(face_w, face_h) / RECOGNITION_INPUT_EDGE)
    return size_factor


def relative_bbox(bbox, width, height):
    x1, y1, x2, y2 = [float(v) for v in bbox]
    x1 = max(0.0, min(x1, float(width)))
    x2 = max(0.0, min(x2, float(width)))
    y1 = max(0.0, min(y1, float(height)))
    y2 = max(0.0, min(y2, float(height)))
    if x2 < x1:
        x1, x2 = x2, x1
    if y2 < y1:
        y1, y2 = y2, y1
    return [x1 / width, y1 / height, (x2 - x1) / width, (y2 - y1) / height]


def analyze(app, path, cv2, np, max_faces):
    image = read_image(path, cv2, np)
    height, width = image.shape[0], image.shape[1]
    with contextlib.redirect_stdout(sys.stderr):
        detected = app.get(image)
    faces = []
    for face in detected:
        embedding = getattr(face, "normed_embedding", None)
        if embedding is None:
            continue
        vector = np.asarray(embedding, dtype=np.float32).reshape(-1)
        if vector.shape[0] != 512:
            continue
        norm = float(np.linalg.norm(vector))
        if norm <= 0:
            continue
        # 再归一化一次：Go 侧的相似度就是点积，向量必须严格是单位向量。
        vector = (vector / norm).astype(np.float32)
        score = float(getattr(face, "det_score", 0.0) or 0.0)
        quality = max(0.0, min(1.0, score * face_quality(face.bbox, width, height)))
        faces.append(
            {
                "bbox": [round(v, 6) for v in relative_bbox(face.bbox, width, height)],
                "quality": round(quality, 6),
                "embedding": base64.b64encode(vector.tobytes()).decode("ascii"),
            }
        )
    faces.sort(key=lambda item: item["quality"], reverse=True)
    if max_faces > 0:
        faces = faces[:max_faces]
    return faces


def main():
    try:
        max_faces = int(os.environ.get(MAX_FACES_ENV, "0"))
    except ValueError:
        max_faces = 0
    try:
        with contextlib.redirect_stdout(sys.stderr):
            import cv2
            import numpy as np
        app = load_app()
    except Exception as exc:  # noqa: BLE001 - 启动失败要让 Go 侧看到原因
        sys.stderr.write("face worker startup failed: %s\n%s\n" % (exc, traceback.format_exc()))
        return 1
    emit({"ready": True})

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            request = json.loads(line)
        except Exception as exc:  # noqa: BLE001
            emit({"id": "", "error": "request is not valid JSON: %s" % type(exc).__name__})
            continue
        request_id = str(request.get("id", ""))
        path = str(request.get("path", ""))
        if not path:
            emit({"id": request_id, "error": "缺少 path"})
            continue
        try:
            faces = analyze(app, path, cv2, np, max_faces)
        except Exception as exc:  # noqa: BLE001 - 单张失败只报这一张
            emit({"id": request_id, "error": error_summary(exc)})
            continue
        emit({"id": request_id, "faces": faces})
    return 0


if __name__ == "__main__":
    sys.exit(main())
