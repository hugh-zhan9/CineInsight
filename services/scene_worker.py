#!/usr/bin/env python3
"""CineInsight 场景检索 sidecar（D-MW-SCENES，场景检索合同「本地运行时」）。

模型：Chinese-CLIP ViT-B/16 量化 ONNX（Xenova 转换件，固定提交），onnxruntime CPU 推理。
组合模型一次需要文本与图像两种输入：只取图像向量时给最短文本 [CLS][SEP]，
只取文本向量时给一张全零图，取对应输出并 L2 归一化。

协议：stdin 每行一个 JSON 请求，stdout 每行一个 JSON 响应。

    {"id": 1, "op": "embed_images", "paths": ["/tmp/f_000001.jpg", ...]}   # 至多 32 张
    {"id": 2, "op": "embed_text", "texts": ["夜晚下雨的街道", ...]}          # 至多 8 条
    -> {"id": 1, "embeddings": ["<base64 float32x512 小端>", ...]}
    -> {"id": 2, "error": "<code>"}

加载完成后先打一行 {"ready": true, "model": "cn-clip-vit-b16-q8@1"}。
stdout 只走协议；加载日志与诊断一律写 stderr。错误只回不含路径与文本的代码。
除本地模型文件外不访问网络。
"""

import base64
import contextlib
import json
import os
import sys

MODEL_ID = "cn-clip-vit-b16-q8@1"
MODEL_DIR_ENV = "CINEINSIGHT_SCENE_MODEL_DIR"
MAX_IMAGES = 32
MAX_TEXTS = 8
MAX_TOKENS = 77
EMBED_DIMS = 512


def emit(payload):
    sys.stdout.write(json.dumps(payload, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def log(message):
    sys.stderr.write("[scene_worker] %s\n" % message)
    sys.stderr.flush()


class SceneModel:
    def __init__(self, model_dir):
        with contextlib.redirect_stdout(sys.stderr):
            import numpy as np
            import onnxruntime as ort
            from PIL import Image
            from tokenizers import Tokenizer

        self.np = np
        self.Image = Image
        with open(os.path.join(model_dir, "preprocessor_config.json"), "r", encoding="utf-8") as handle:
            config = json.load(handle)
        size = config.get("size") or {}
        self.width = int(size.get("width") or 224)
        self.height = int(size.get("height") or 224)
        self.mean = np.array(config.get("image_mean") or [0.5, 0.5, 0.5], dtype=np.float32).reshape(3, 1, 1)
        self.std = np.array(config.get("image_std") or [0.5, 0.5, 0.5], dtype=np.float32).reshape(3, 1, 1)
        self.rescale = float(config.get("rescale_factor") or (1.0 / 255.0))

        self.tokenizer = Tokenizer.from_file(os.path.join(model_dir, "tokenizer.json"))
        self.tokenizer.enable_truncation(max_length=MAX_TOKENS)
        pad_id = self.tokenizer.token_to_id("[PAD]")
        self.tokenizer.enable_padding(pad_id=pad_id if pad_id is not None else 0, pad_token="[PAD]")

        options = ort.SessionOptions()
        options.log_severity_level = 3
        with contextlib.redirect_stdout(sys.stderr):
            self.session = ort.InferenceSession(
                os.path.join(model_dir, "model_quantized.onnx"),
                sess_options=options,
                providers=["CPUExecutionProvider"],
            )
        self.input_names = {item.name for item in self.session.get_inputs()}
        empty = self.tokenizer.encode("")
        self.empty_ids = np.array([empty.ids], dtype=np.int64)
        self.empty_mask = np.array([empty.attention_mask], dtype=np.int64)

    def _feeds(self, input_ids, attention_mask, pixel_values):
        feeds = {}
        if "input_ids" in self.input_names:
            feeds["input_ids"] = input_ids
        if "attention_mask" in self.input_names:
            feeds["attention_mask"] = attention_mask
        if "token_type_ids" in self.input_names:
            feeds["token_type_ids"] = self.np.zeros_like(input_ids)
        if "pixel_values" in self.input_names:
            feeds["pixel_values"] = pixel_values
        return feeds

    def _normalize(self, matrix):
        np = self.np
        matrix = np.asarray(matrix, dtype=np.float32)
        norms = np.linalg.norm(matrix, axis=1, keepdims=True)
        norms[norms == 0] = 1.0
        return matrix / norms

    def _load_image(self, path):
        image = self.Image.open(path)
        image = image.convert("RGB")
        if image.size != (self.width, self.height):
            image = image.resize((self.width, self.height), self.Image.BICUBIC)
        array = self.np.asarray(image, dtype=self.np.float32) * self.rescale
        array = array.transpose(2, 0, 1)
        return (array - self.mean) / self.std

    def embed_images(self, paths):
        np = self.np
        pixels = np.stack([self._load_image(path) for path in paths]).astype(np.float32)
        outputs = self.session.run(["image_embeds"], self._feeds(self.empty_ids, self.empty_mask, pixels))
        return self._normalize(outputs[0])

    def embed_texts(self, texts):
        np = self.np
        encodings = self.tokenizer.encode_batch([str(text) for text in texts])
        input_ids = np.array([item.ids for item in encodings], dtype=np.int64)
        attention_mask = np.array([item.attention_mask for item in encodings], dtype=np.int64)
        pixels = np.zeros((1, 3, self.height, self.width), dtype=np.float32)
        outputs = self.session.run(["text_embeds"], self._feeds(input_ids, attention_mask, pixels))
        return self._normalize(outputs[0])


def encode_vectors(matrix):
    encoded = []
    for row in matrix:
        data = row.astype("<f4").tobytes()
        if len(data) != EMBED_DIMS * 4:
            raise ValueError("unexpected embedding size")
        encoded.append(base64.b64encode(data).decode("ascii"))
    return encoded


def handle(model, request):
    request_id = request.get("id")
    op = request.get("op")
    if op == "embed_images":
        paths = request.get("paths")
        if not isinstance(paths, list) or not paths or not all(isinstance(p, str) and p for p in paths):
            return {"id": request_id, "error": "bad_request"}
        if len(paths) > MAX_IMAGES:
            return {"id": request_id, "error": "too_many_items"}
        try:
            matrix = model.embed_images(paths)
        except (OSError, ValueError):
            return {"id": request_id, "error": "image_unreadable"}
        return {"id": request_id, "embeddings": encode_vectors(matrix)}
    if op == "embed_text":
        texts = request.get("texts")
        if not isinstance(texts, list) or not texts or not all(isinstance(t, str) for t in texts):
            return {"id": request_id, "error": "bad_request"}
        if len(texts) > MAX_TEXTS:
            return {"id": request_id, "error": "too_many_items"}
        matrix = model.embed_texts(texts)
        return {"id": request_id, "embeddings": encode_vectors(matrix)}
    return {"id": request_id, "error": "unknown_op"}


def main():
    model_dir = os.environ.get(MODEL_DIR_ENV, "").strip()
    if not model_dir:
        log("%s is not set" % MODEL_DIR_ENV)
        return 2
    try:
        model = SceneModel(model_dir)
    except Exception as exc:  # noqa: BLE001 - 加载失败只报类型，不带路径
        log("model load failed: %s" % type(exc).__name__)
        return 3
    emit({"ready": True, "model": MODEL_ID})
    for raw in sys.stdin:
        line = raw.strip()
        if not line:
            continue
        try:
            request = json.loads(line)
        except ValueError:
            emit({"id": None, "error": "bad_request"})
            continue
        if not isinstance(request, dict):
            emit({"id": None, "error": "bad_request"})
            continue
        try:
            response = handle(model, request)
        except Exception as exc:  # noqa: BLE001 - 单条失败不拖垮常驻进程
            log("request failed: %s" % type(exc).__name__)
            response = {"id": request.get("id"), "error": "inference_failed"}
        emit(response)
    return 0


if __name__ == "__main__":
    sys.exit(main())
