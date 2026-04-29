#!/usr/bin/env python3
import argparse
import gc
import json
import os
import re
import sys
import wave

os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")


LOW_MEMORY_MAX_INFERENCE_BATCH_SIZE = 1
LOW_MEMORY_MAX_NEW_TOKENS = 256
LOW_MEMORY_CONV_CHUNKSIZE = 128
ALIGNED_SEGMENT_MAX_CHARS = 42
ALIGNED_SEGMENT_MAX_DURATION = 8.0
ALIGNED_SEGMENT_MAX_GAP = 0.8
SENTENCE_ENDINGS = "。！？!?；;,.，"
QWEN_LANGUAGE_MAP = {
    "ar": "Arabic",
    "arabic": "Arabic",
    "ca": "Cantonese",
    "cantonese": "Cantonese",
    "cs": "Czech",
    "czech": "Czech",
    "da": "Danish",
    "danish": "Danish",
    "de": "German",
    "german": "German",
    "el": "Greek",
    "greek": "Greek",
    "en": "English",
    "english": "English",
    "es": "Spanish",
    "spanish": "Spanish",
    "fa": "Persian",
    "persian": "Persian",
    "fi": "Finnish",
    "finnish": "Finnish",
    "fil": "Filipino",
    "filipino": "Filipino",
    "fr": "French",
    "french": "French",
    "hi": "Hindi",
    "hindi": "Hindi",
    "hu": "Hungarian",
    "hungarian": "Hungarian",
    "id": "Indonesian",
    "indonesian": "Indonesian",
    "it": "Italian",
    "italian": "Italian",
    "ja": "Japanese",
    "japanese": "Japanese",
    "ko": "Korean",
    "korean": "Korean",
    "ms": "Malay",
    "malay": "Malay",
    "nl": "Dutch",
    "dutch": "Dutch",
    "pl": "Polish",
    "polish": "Polish",
    "pt": "Portuguese",
    "portuguese": "Portuguese",
    "ro": "Romanian",
    "romanian": "Romanian",
    "ru": "Russian",
    "russian": "Russian",
    "sv": "Swedish",
    "swedish": "Swedish",
    "th": "Thai",
    "thai": "Thai",
    "tr": "Turkish",
    "turkish": "Turkish",
    "vi": "Vietnamese",
    "vietnamese": "Vietnamese",
    "zh": "Chinese",
    "chinese": "Chinese",
}


def log_stage(message: str):
    sys.stderr.write(f"[qwen-worker] {message}\n")
    sys.stderr.flush()


def normalize_qwen_language(language: str):
    normalized = (language or "").strip()
    if not normalized or normalized.lower() == "auto":
        return None
    mapped = QWEN_LANGUAGE_MAP.get(normalized.lower())
    if mapped:
        return mapped
    return normalized


def pick_device(torch_module, requested_device: str):
    requested_device = (requested_device or "auto").lower()
    if requested_device == "cpu":
        return "cpu", torch_module.float32
    if requested_device == "mps":
        mps = getattr(torch_module.backends, "mps", None)
        is_available = getattr(mps, "is_available", None) if mps is not None else None
        if callable(is_available) and is_available():
            return "mps", torch_module.float16
        raise RuntimeError("Requested MPS device but it is not available")
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


def release_torch_resources(torch_module, *objs):
    for obj in objs:
        try:
            del obj
        except Exception:
            pass
    gc.collect()
    mps_module = getattr(torch_module, "mps", None)
    empty_cache = getattr(mps_module, "empty_cache", None) if mps_module is not None else None
    if callable(empty_cache):
        try:
            empty_cache()
        except Exception:
            pass


def sentence_chunks(text: str):
    cleaned = re.sub(r"\s+", " ", (text or "").strip())
    if not cleaned:
        return []
    parts = re.split(r"(?<=[。！？!?；;,.，])", cleaned)
    sentences = []
    buffer = ""
    for part in parts:
        piece = (part or "").strip()
        if not piece:
            continue
        buffer += piece
        if len(buffer) >= 24 or piece[-1:] in "。！？!?；;,.，":
            sentences.append(buffer.strip())
            buffer = ""
    if buffer.strip():
        sentences.append(buffer.strip())
    if sentences:
        return sentences
    return [cleaned[i:i + 24] for i in range(0, len(cleaned), 24)]


def build_heuristic_segments(text: str, duration_sec: float):
    sentences = sentence_chunks(text)
    if not sentences:
        return []
    total_units = sum(max(1, len(item)) for item in sentences)
    if total_units <= 0:
        total_units = len(sentences)
    total_duration = max(duration_sec, 0.1)
    segments = []
    current_start = 0.0
    for index, sentence in enumerate(sentences):
        units = max(1, len(sentence))
        current_end = total_duration if index == len(sentences) - 1 else current_start + (total_duration * units / total_units)
        if current_end < current_start:
            current_end = current_start
        segments.append({
            "start": round(current_start, 3),
            "end": round(current_end, 3),
            "text": sentence,
        })
        current_start = current_end
    return segments


def is_cjk_char(value: str) -> bool:
    if not value:
        return False
    codepoint = ord(value[0])
    return (
        0x3400 <= codepoint <= 0x4DBF
        or 0x4E00 <= codepoint <= 0x9FFF
        or 0xF900 <= codepoint <= 0xFAFF
    )


def join_aligned_text(current: str, token: str) -> str:
    token = (token or "").strip()
    if not current:
        return token
    if not token:
        return current
    prev = current[-1]
    first = token[0]
    if first in SENTENCE_ENDINGS or first in "，。！？；：、,.!?;:)）】」』":
        return current + token
    if prev in "([{（【「『":
        return current + token
    if is_cjk_char(prev) or is_cjk_char(first):
        return current + token
    return current + " " + token


def normalized_text_len(text: str) -> int:
    return len(re.sub(r"\s+", "", text or ""))


def collect_aligned_items(timestamps):
    raw = []
    for item in timestamps:
        text = coerce_text(item)
        start = coerce_time(item, "start")
        end = coerce_time(item, "end")
        if not text or start is None or end is None:
            continue
        start = max(0.0, float(start))
        end = max(0.0, float(end))
        if end < start:
            end = start
        raw.append({"start": start, "end": end, "text": text})

    raw.sort(key=lambda item: (item["start"], item["end"]))
    for index, item in enumerate(raw):
        if item["end"] <= item["start"] and index + 1 < len(raw):
            next_start = raw[index + 1]["start"]
            if next_start > item["start"]:
                item["end"] = next_start
    return raw


def aligned_items_are_collapsed(raw_items, duration_sec: float, full_text: str) -> bool:
    if len(raw_items) < 20:
        return False
    starts = [item["start"] for item in raw_items]
    ends = [max(item["start"], item["end"]) for item in raw_items]
    coverage = max(ends) - min(starts)
    unique_start_count = len({round(item["start"], 3) for item in raw_items})
    unique_start_ratio = unique_start_count / max(1, len(raw_items))
    zero_ratio = sum(1 for item in raw_items if item["end"] <= item["start"]) / max(1, len(raw_items))
    char_count = max(1, normalized_text_len(full_text))
    chars_per_second = char_count / max(coverage, 0.1)

    if unique_start_ratio < 0.05 and zero_ratio > 0.70:
        return True
    if duration_sec >= 60.0 and coverage < min(duration_sec * 0.05, 30.0) and chars_per_second > 25.0:
        return True
    return False


def build_segments_from_aligned_items(raw_items):
    segments = []
    current_text = ""
    current_start = None
    current_end = None
    previous_end = None

    def flush_current():
        nonlocal current_text, current_start, current_end, previous_end
        text = re.sub(r"\s+", " ", current_text).strip()
        if text and current_start is not None and current_end is not None:
            end = max(current_end, current_start)
            segments.append({
                "start": round(current_start, 3),
                "end": round(end, 3),
                "text": text,
            })
        current_text = ""
        current_start = None
        current_end = None
        previous_end = None

    for item in raw_items:
        token = item["text"]
        start = item["start"]
        end = item["end"]
        gap = 0.0 if previous_end is None else max(0.0, start - previous_end)
        if current_text and gap > ALIGNED_SEGMENT_MAX_GAP:
            flush_current()

        if current_start is None:
            current_start = start
            current_end = end
            current_text = token.strip()
        else:
            current_text = join_aligned_text(current_text, token)
            current_end = max(current_end or end, end)
        previous_end = max(end, start)

        duration = 0.0 if current_start is None or current_end is None else current_end - current_start
        if (
            normalized_text_len(current_text) >= ALIGNED_SEGMENT_MAX_CHARS
            or duration >= ALIGNED_SEGMENT_MAX_DURATION
            or current_text[-1:] in SENTENCE_ENDINGS
        ):
            flush_current()

    flush_current()
    return segments


def segments_are_suspicious(segments, duration_sec: float, full_text: str) -> bool:
    if not segments:
        return True
    if len(segments) < 20:
        return False
    zero_ratio = sum(1 for item in segments if item["end"] <= item["start"]) / len(segments)
    short_ratio = sum(1 for item in segments if normalized_text_len(item["text"]) <= 2) / len(segments)
    starts = [item["start"] for item in segments]
    ends = [max(item["start"], item["end"]) for item in segments]
    coverage = max(ends) - min(starts)
    text_len = normalized_text_len(full_text)

    if short_ratio > 0.85 and zero_ratio > 0.50:
        return True
    if text_len > 120 and duration_sec >= 60.0 and coverage < min(duration_sec * 0.05, 30.0):
        return True
    return False


def low_memory_model_kwargs(device_map, dtype):
    return {
        "dtype": dtype,
        "device_map": device_map,
        "low_cpu_mem_usage": True,
        "max_inference_batch_size": LOW_MEMORY_MAX_INFERENCE_BATCH_SIZE,
        "max_new_tokens": LOW_MEMORY_MAX_NEW_TOKENS,
    }


def low_memory_aligner_kwargs(device_map, dtype):
    return {
        "dtype": dtype,
        "device_map": device_map,
        "low_cpu_mem_usage": True,
    }


def apply_low_memory_conv_chunksize(obj):
    if obj is None:
        return
    config = getattr(obj, "config", None)
    if config is not None and hasattr(config, "conv_chunksize"):
        setattr(config, "conv_chunksize", LOW_MEMORY_CONV_CHUNKSIZE)
    named_modules = getattr(obj, "named_modules", None)
    if callable(named_modules):
        for _, module in named_modules():
            if hasattr(module, "conv_chunksize"):
                setattr(module, "conv_chunksize", LOW_MEMORY_CONV_CHUNKSIZE)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--wav-path", required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--aligner", required=True)
    parser.add_argument("--language", default="auto")
    parser.add_argument("--device", default="auto", choices=("auto", "mps", "cpu"))
    args = parser.parse_args()

    import torch
    from qwen_asr import Qwen3ASRModel, Qwen3ForcedAligner

    device_map, dtype = pick_device(torch, args.device)
    language = normalize_qwen_language(args.language)

    asr_model = None
    aligner = None

    log_stage(
        f"device={device_map} dtype={getattr(dtype, '__name__', str(dtype))} "
        f"batch={LOW_MEMORY_MAX_INFERENCE_BATCH_SIZE} max_new_tokens={LOW_MEMORY_MAX_NEW_TOKENS} "
        f"conv_chunksize={LOW_MEMORY_CONV_CHUNKSIZE}"
    )
    log_stage("loading asr model")
    asr_model = Qwen3ASRModel.from_pretrained(
        args.model,
        **low_memory_model_kwargs(device_map, dtype),
    )
    apply_low_memory_conv_chunksize(getattr(asr_model, "model", None))

    log_stage("running asr transcription")
    with torch.inference_mode():
        results = asr_model.transcribe(
            audio=args.wav_path,
            language=language,
            return_time_stamps=False,
        )
    if not results:
        raise RuntimeError("Qwen returned no results")
    result = results[0]
    detected_language = str(getattr(result, "language", None) or language or "unknown")
    full_text = str(getattr(result, "text", "") or "").strip()
    duration_sec = max(wav_duration_sec(args.wav_path), 0.1)

    log_stage(f"asr transcription completed language={detected_language} text_len={len(full_text)}")
    log_stage("releasing asr model")
    release_torch_resources(torch, asr_model)
    asr_model = None

    timestamps = []
    if full_text:
        try:
            log_stage("loading forced aligner")
            aligner = Qwen3ForcedAligner.from_pretrained(
                args.aligner,
                **low_memory_aligner_kwargs(device_map, dtype),
            )
            apply_low_memory_conv_chunksize(getattr(aligner, "model", None))
            log_stage("running forced alignment")
            with torch.inference_mode():
                aligned = aligner.align(
                    audio=args.wav_path,
                    text=full_text,
                    language=detected_language,
                )
            if aligned:
                timestamps = aligned[0]
            log_stage(f"forced alignment completed items={len(timestamps)}")
        except Exception as exc:
            log_stage(f"forced alignment failed, falling back to heuristic segmentation: {exc}")
            timestamps = []
        finally:
            log_stage("releasing forced aligner")
            release_torch_resources(torch, aligner)
            aligner = None

    segments = []
    raw_aligned_items = collect_aligned_items(timestamps)
    if raw_aligned_items:
        if aligned_items_are_collapsed(raw_aligned_items, duration_sec, full_text):
            log_stage("forced alignment collapsed, falling back to heuristic segmentation")
        else:
            segments = build_segments_from_aligned_items(raw_aligned_items)
            if segments_are_suspicious(segments, duration_sec, full_text):
                log_stage("forced alignment segments look suspicious, falling back to heuristic segmentation")
                segments = []

    if not segments:
        if full_text:
            log_stage("using heuristic subtitle segmentation")
            segments = build_heuristic_segments(full_text, duration_sec)

    payload = {
        "language": detected_language,
        "segments": segments,
    }
    sys.stdout.write(json.dumps(payload, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
