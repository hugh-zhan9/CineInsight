#!/usr/bin/env python3
import argparse
import json
import sys
import wave


def pick_device(torch_module):
    mps = getattr(torch_module.backends, "mps", None)
    if mps is not None:
        is_available = getattr(mps, "is_available", None)
        if callable(is_available) and is_available():
            return "mps", torch_module.float16
    return "cpu", torch_module.float32


def wav_duration_sec(path: str) -> float:
    with wave.open(path, "rb") as wav_file:
        frames = wav_file.getnframes()
        rate = wav_file.getframerate()
    if rate <= 0:
        return 0.0
    return float(frames) / float(rate)


def coerce_text(obj) -> str:
    if isinstance(obj, dict):
        for key in ("text", "token", "word", "label"):
            value = obj.get(key)
            if value:
                return str(value).strip()
    for key in ("text", "token", "word", "label"):
        value = getattr(obj, key, None)
        if value:
            return str(value).strip()
    return ""


def coerce_time(obj, key: str):
    if isinstance(obj, dict):
        value = obj.get(key)
        if value is None and key.endswith("_ms"):
            base = obj.get(key[:-3])
            if base is not None:
                return float(base) / 1000.0
        if value is None and key in {"start", "end"}:
            alt = obj.get(f"{key}_time")
            if alt is not None:
                return float(alt)
        if value is not None:
            return float(value)
        return None
    value = getattr(obj, key, None)
    if value is not None:
        return float(value)
    alt = getattr(obj, f"{key}_time", None)
    if alt is not None:
        return float(alt)
    return None


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--wav-path", required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--aligner", required=True)
    parser.add_argument("--language", default="auto")
    args = parser.parse_args()

    import torch
    from qwen_asr import Qwen3ASRModel

    device_map, dtype = pick_device(torch)
    language = None if args.language in {"", "auto"} else args.language

    model = Qwen3ASRModel.from_pretrained(
        args.model,
        dtype=dtype,
        device_map=device_map,
        forced_aligner=args.aligner,
        forced_aligner_kwargs={
            "dtype": dtype,
            "device_map": device_map,
        },
        max_new_tokens=1024,
    )

    results = model.transcribe(
        audio=args.wav_path,
        language=language,
        return_time_stamps=True,
    )
    if not results:
        raise RuntimeError("Qwen returned no results")
    result = results[0]
    detected_language = str(getattr(result, "language", None) or language or "unknown")
    timestamps = getattr(result, "time_stamps", None) or getattr(result, "timestamps", None) or []

    segments = []
    for item in timestamps:
        text = coerce_text(item)
        start = coerce_time(item, "start")
        end = coerce_time(item, "end")
        if not text or start is None or end is None:
            continue
        if end < start:
            end = start
        segments.append({"start": start, "end": end, "text": text})

    if not segments:
        full_text = str(getattr(result, "text", "") or "").strip()
        if full_text:
            segments = [{"start": 0.0, "end": max(wav_duration_sec(args.wav_path), 0.1), "text": full_text}]

    payload = {
        "language": detected_language,
        "segments": segments,
    }
    sys.stdout.write(json.dumps(payload, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
