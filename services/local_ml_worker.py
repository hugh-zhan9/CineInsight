#!/usr/bin/env python3
import argparse
import base64
import io
import json
import os
import sys
from typing import List

import numpy as np
import torch
from PIL import Image
import open_clip


def configure_stdio() -> None:
    for stream in (sys.stdout, sys.stderr):
        reconfigure = getattr(stream, "reconfigure", None)
        if callable(reconfigure):
            reconfigure(encoding="utf-8", errors="backslashreplace", line_buffering=True)


def parse_model_spec(spec: str):
    spec = (spec or "").strip() or os.environ.get("LOCAL_ML_MODEL", "xlm-roberta-base-ViT-B-32::laion5b_s13b_b90k")
    if "::" in spec:
        model, pretrained = spec.split("::", 1)
        model = model.strip() or "xlm-roberta-base-ViT-B-32"
        pretrained = pretrained.strip() or "laion5b_s13b_b90k"
        return model, pretrained
    return spec, "laion5b_s13b_b90k"


def decode_data_url(data_url: str) -> bytes:
    data_url = (data_url or "").strip()
    if not data_url:
        return b""
    if data_url.startswith("data:") and "," in data_url:
        data_url = data_url.split(",", 1)[1]
    return base64.b64decode(data_url)


def select_device(requested: str) -> str:
    value = (requested or os.environ.get("LOCAL_ML_DEVICE", "auto")).strip().lower() or "auto"
    if value not in {"auto", "cpu", "cuda", "mps"}:
        value = "auto"
    if value == "cuda":
        if torch.cuda.is_available():
            return "cuda"
        raise RuntimeError("CUDA was requested but is not available")
    if value == "mps":
        if getattr(torch.backends, "mps", None) and torch.backends.mps.is_available():
            return "mps"
        raise RuntimeError("MPS was requested but is not available")
    if value == "auto":
        if torch.cuda.is_available():
            return "cuda"
        if getattr(torch.backends, "mps", None) and torch.backends.mps.is_available():
            return "mps"
    return "cpu"


def load_model(spec: str, requested_device: str):
    model_name, pretrained = parse_model_spec(spec)
    device = select_device(requested_device)
    model, _, preprocess = open_clip.create_model_and_transforms(model_name, pretrained=pretrained, device=device)
    tokenizer = open_clip.get_tokenizer(model_name)
    model.eval()
    return model, preprocess, tokenizer, model_name, pretrained, device


def normalize_tensor(tensor: torch.Tensor) -> torch.Tensor:
    return torch.nn.functional.normalize(tensor.float(), dim=-1)


def embed_texts(model, tokenizer, texts: List[str], device: str):
    filtered = [text.strip() for text in texts if text and text.strip()]
    if not filtered:
        return []
    tokens = tokenizer(filtered).to(device)
    with torch.no_grad():
        encoded = model.encode_text(tokens)
    encoded = normalize_tensor(encoded)
    return encoded.cpu().numpy().astype(np.float32).tolist()


def embed_images(model, preprocess, data_urls: List[str], device: str):
    images = []
    for data_url in data_urls:
        raw = decode_data_url(data_url)
        if not raw:
            continue
        image = Image.open(io.BytesIO(raw)).convert("RGB")
        images.append(preprocess(image))
    if not images:
        return []
    batch = torch.stack(images).to(device)
    with torch.no_grad():
        encoded = model.encode_image(batch)
    encoded = normalize_tensor(encoded)
    return encoded.cpu().numpy().astype(np.float32).tolist()


def combine_embeddings(image_vectors, text_vectors):
    vectors = []
    for vector in image_vectors:
        if vector:
            vectors.append(np.asarray(vector, dtype=np.float32))
    for vector in text_vectors:
        if vector:
            vectors.append(np.asarray(vector, dtype=np.float32))
    if not vectors:
        return []
    combined = np.mean(np.stack(vectors, axis=0), axis=0)
    norm = np.linalg.norm(combined)
    if norm > 0:
        combined = combined / norm
    return combined.astype(np.float32).tolist()


def run_payload(mode: str, payload: dict, model, preprocess, tokenizer, model_name: str, pretrained: str, device: str) -> dict:
    source = "open_clip"
    if mode == "embed-texts":
        texts = payload.get("texts") or []
        embeddings = embed_texts(model, tokenizer, texts, device)
        return {
            "model": f"{model_name}::{pretrained}",
            "source": source,
            "device": device,
            "embeddings": embeddings,
            "dimension": len(embeddings[0]) if embeddings else 0,
        }

    if mode != "embed-video":
        raise ValueError(f"unsupported mode: {mode}")

    video_text_parts = [
        str(payload.get("video_name") or "").strip(),
        str(payload.get("subtitle_text") or "").strip(),
    ]
    text_embedding = embed_texts(model, tokenizer, ["\n".join([part for part in video_text_parts if part])], device)
    image_embedding = embed_images(model, preprocess, [frame.get("data_url", "") for frame in (payload.get("frames") or [])], device)
    combined = combine_embeddings(image_embedding, text_embedding[:1])
    return {
        "model": f"{model_name}::{pretrained}",
        "source": source,
        "device": device,
        "embedding": combined,
        "dimension": len(combined),
    }


def serve(args) -> int:
    model, preprocess, tokenizer, model_name, pretrained, device = load_model(os.environ.get("LOCAL_ML_MODEL", ""), args.device)
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            request = json.loads(line)
            result = run_payload(
                request.get("mode", ""),
                request.get("payload") or {},
                model,
                preprocess,
                tokenizer,
                model_name,
                pretrained,
                device,
            )
            json.dump({"ok": True, "result": result}, sys.stdout)
        except Exception as exc:
            print(f"local ML worker request failed: {exc}", file=sys.stderr)
            json.dump({"ok": False, "error": str(exc)}, sys.stdout)
        sys.stdout.write("\n")
        sys.stdout.flush()
    return 0


def main():
    configure_stdio()
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=["embed-texts", "embed-video", "serve"])
    parser.add_argument("--device", default=os.environ.get("LOCAL_ML_DEVICE", "auto"))
    args = parser.parse_args()

    if args.mode == "serve":
        return serve(args)

    payload = json.load(sys.stdin)
    model, preprocess, tokenizer, model_name, pretrained, device = load_model(payload.get("model", ""), args.device)
    result = run_payload(args.mode, payload, model, preprocess, tokenizer, model_name, pretrained, device)
    json.dump(result, sys.stdout)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
