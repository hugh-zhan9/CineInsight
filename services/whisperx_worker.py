#!/usr/bin/env python3
"""
Adapted from Caption-Trans's WhisperX pipeline for use inside this Wails/Go app.

This script runs a single WhisperX transcription request and writes a JSON payload
to stdout:
{
  "language": "ja",
  "duration_sec": 12.34,
  "segments": [{"start": 0.0, "end": 1.2, "text": "..."}, ...]
}
"""

import argparse
import contextlib
import json
import os
import sys
import wave
from typing import Any, Dict, List, Optional, Tuple

import numpy as np
import whisperx


SAMPLE_RATE = 16000
LANGUAGES_WITHOUT_SPACES = {"ja", "zh"}
SENTENCE_END_PUNCTUATION = frozenset("。！？!?")
SOFT_BREAK_PUNCTUATION = frozenset("、，,;；")
CLOSING_PUNCTUATION = frozenset("」』】》）〕〉〟'\"”’")
JOIN_WITHOUT_LEADING_SPACE = frozenset(".,!?;:%)]}。，、！？：；％）」』】》〕〉〟'\"”’")
JOIN_WITHOUT_TRAILING_SPACE = frozenset("([{「『【《（〔〈〝'\"“‘$")
DEFAULT_SEGMENTATION_OPTIONS: Dict[str, Any] = {
    "split_on_pause": True,
    "pause_threshold_sec": 0.8,
    "max_segment_duration_sec": 6.0,
    "max_segment_chars": 42,
    "min_split_chars": 10,
    "prefer_punctuation_split": True,
}
NO_SPACE_LANGUAGE_SEGMENTATION_OPTIONS: Dict[str, Any] = {
    "pause_threshold_sec": 0.65,
    "max_segment_duration_sec": 4.5,
    "max_segment_chars": 28,
    "min_split_chars": 6,
}
JAPANESE_SEGMENTATION_OPTIONS: Dict[str, Any] = {
    "pause_threshold_sec": 0.55,
    "max_segment_duration_sec": 4.0,
    "max_segment_chars": 24,
    "min_split_chars": 4,
}


def configure_stdio() -> None:
    for stream in (sys.stdout, sys.stderr):
        reconfigure = getattr(stream, "reconfigure", None)
        if callable(reconfigure):
            reconfigure(encoding="utf-8", errors="backslashreplace", line_buffering=True)


def to_float(value: Any, default: float = 0.0) -> float:
    try:
        return float(value)
    except (TypeError, ValueError):
        return default


def to_optional_float(value: Any) -> Optional[float]:
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def to_int(value: Any, default: int = 0) -> int:
    try:
        return int(value)
    except (TypeError, ValueError):
        return default


def to_bool(value: Any, default: bool = False) -> bool:
    if isinstance(value, bool):
        return value
    if isinstance(value, str):
        normalized = value.strip().lower()
        if normalized in {"1", "true", "yes", "on"}:
            return True
        if normalized in {"0", "false", "no", "off"}:
            return False
        return default
    if value is None:
        return default
    return bool(value)


def read_wav_pcm_s16le(path: str) -> np.ndarray:
    with wave.open(path, "rb") as wav_file:
        channels = wav_file.getnchannels()
        sample_width = wav_file.getsampwidth()
        sample_rate = wav_file.getframerate()
        frames = wav_file.readframes(wav_file.getnframes())

    if channels != 1:
        raise ValueError(f"Expected mono WAV, got channels={channels}")
    if sample_width != 2:
        raise ValueError(f"Expected 16-bit PCM WAV, got sample_width={sample_width}")
    if sample_rate != SAMPLE_RATE:
        raise ValueError(f"Expected {SAMPLE_RATE}Hz WAV, got sample_rate={sample_rate}")

    return np.frombuffer(frames, dtype=np.int16).astype(np.float32) / 32768.0


def normalize_segments(raw_segments: Any) -> List[Dict[str, Any]]:
    if not isinstance(raw_segments, list):
        return []

    segments: List[Dict[str, Any]] = []
    for item in raw_segments:
        if not isinstance(item, dict):
            continue

        start = to_float(item.get("start"), 0.0)
        end = to_float(item.get("end"), start)
        text = str(item.get("text") or "").strip()
        if not text:
            continue

        if end < start:
            end = start

        segments.append({"start": start, "end": end, "text": text})
    return segments


def normalize_word_entries(raw_words: Any, language: Optional[str]) -> List[Dict[str, Any]]:
    if not isinstance(raw_words, list):
        return []

    words: List[Dict[str, Any]] = []
    for item in raw_words:
        if not isinstance(item, dict):
            continue

        raw_text = str(item.get("word") or "")
        text = raw_text if language in LANGUAGES_WITHOUT_SPACES else raw_text.strip()
        if not text:
            continue

        words.append(
            {
                "word": text,
                "start": to_optional_float(item.get("start")),
                "end": to_optional_float(item.get("end")),
            }
        )
    return words


def join_words(words: List[Dict[str, Any]], language: Optional[str]) -> str:
    tokens = [str(item.get("word") or "") for item in words]
    if language in LANGUAGES_WITHOUT_SPACES:
        return "".join(tokens).strip()

    rendered = ""
    for token in tokens:
        text = token.strip()
        if not text:
            continue
        if not rendered:
            rendered = text
            continue
        if text[0] in JOIN_WITHOUT_LEADING_SPACE or rendered[-1] in JOIN_WITHOUT_TRAILING_SPACE:
            rendered += text
        else:
            rendered += f" {text}"
    return rendered.strip()


def merge_text_parts(left: str, right: str, language: Optional[str]) -> str:
    left_text = left.strip()
    right_text = right.strip()
    if not left_text:
        return right_text
    if not right_text:
        return left_text
    if language in LANGUAGES_WITHOUT_SPACES:
        return f"{left_text}{right_text}".strip()
    if right_text[0] in JOIN_WITHOUT_LEADING_SPACE or left_text[-1] in JOIN_WITHOUT_TRAILING_SPACE:
        return f"{left_text}{right_text}"
    return f"{left_text} {right_text}"


def effective_char_count(text: str, language: Optional[str]) -> int:
    if language in LANGUAGES_WITHOUT_SPACES:
        return len(text.replace(" ", "").replace("\u3000", ""))
    return len(text)


def ends_with_sentence_boundary(text: str) -> bool:
    trimmed = text.strip()
    while trimmed and trimmed[-1] in CLOSING_PUNCTUATION:
        trimmed = trimmed[:-1].rstrip()
    return bool(trimmed) and trimmed[-1] in SENTENCE_END_PUNCTUATION


def ends_with_soft_break(text: str) -> bool:
    trimmed = text.strip()
    while trimmed and trimmed[-1] in CLOSING_PUNCTUATION:
        trimmed = trimmed[:-1].rstrip()
    return bool(trimmed) and trimmed[-1] in SOFT_BREAK_PUNCTUATION


def is_punctuation_only(text: str) -> bool:
    stripped = text.strip()
    if not stripped:
        return False
    punctuation_chars = (
        SENTENCE_END_PUNCTUATION
        | SOFT_BREAK_PUNCTUATION
        | CLOSING_PUNCTUATION
        | JOIN_WITHOUT_LEADING_SPACE
        | JOIN_WITHOUT_TRAILING_SPACE
    )
    return all(ch in punctuation_chars for ch in stripped)


def is_closing_token(text: str) -> bool:
    stripped = text.strip()
    return bool(stripped) and all(ch in CLOSING_PUNCTUATION for ch in stripped)


def resolve_span(words: List[Dict[str, Any]], fallback_start: float, fallback_end: float) -> Tuple[float, float]:
    start = fallback_start
    end = fallback_end
    for item in words:
        word_start = item.get("start")
        if isinstance(word_start, float):
            start = word_start
            break
    for item in reversed(words):
        word_end = item.get("end")
        if isinstance(word_end, float):
            end = word_end
            break
    if end < start:
        end = start
    return start, end


def resolve_explicit_span(words: List[Dict[str, Any]]) -> Tuple[Optional[float], Optional[float]]:
    start: Optional[float] = None
    end: Optional[float] = None
    for item in words:
        word_start = item.get("start")
        if isinstance(word_start, float):
            start = word_start
            break
    for item in reversed(words):
        word_end = item.get("end")
        if isinstance(word_end, float):
            end = word_end
            break
    if start is not None and end is not None and end < start:
        end = start
    return start, end


def build_segment_from_words(
    words: List[Dict[str, Any]],
    language: Optional[str],
    fallback_start: float,
    fallback_end: float,
    allow_fallback: bool = True,
) -> Optional[Dict[str, Any]]:
    text = join_words(words, language)
    if not text:
        return None

    start, end = resolve_explicit_span(words)
    if start is None or end is None:
        if not allow_fallback:
            return None
        start, end = resolve_span(words, fallback_start, fallback_end)

    return {"start": start, "end": end, "text": text}


def gap_after_word(current_word: Dict[str, Any], next_word: Optional[Dict[str, Any]]) -> Optional[float]:
    if next_word is None:
        return None
    end = current_word.get("end")
    start = next_word.get("start")
    if not isinstance(end, float) or not isinstance(start, float):
        return None
    return max(0.0, start - end)


def classify_safe_break(
    text: str,
    current_word: Dict[str, Any],
    next_word: Optional[Dict[str, Any]],
    current_char_count: int,
    split_on_pause: bool,
    prefer_punctuation_split: bool,
    pause_threshold: float,
    min_split_chars: int,
) -> Optional[str]:
    if next_word is None or current_char_count < min_split_chars:
        return None
    next_text = str(next_word.get("word") or "")
    if prefer_punctuation_split and not is_closing_token(next_text) and ends_with_sentence_boundary(text):
        return "sentence"
    if not is_closing_token(next_text) and ends_with_soft_break(text):
        return "soft"
    if split_on_pause:
        gap = gap_after_word(current_word, next_word)
        if gap is not None and gap >= pause_threshold:
            return "pause"
    return None


def should_commit_immediately(break_reason: str) -> bool:
    return break_reason == "sentence"


def find_segment_end_index(
    words: List[Dict[str, Any]],
    segment_start: int,
    language: Optional[str],
    fallback_start: float,
    fallback_end: float,
    split_on_pause: bool,
    prefer_punctuation_split: bool,
    pause_threshold: float,
    max_duration: float,
    max_chars: int,
    min_split_chars: int,
) -> int:
    last_safe_break: Optional[int] = None
    for index in range(segment_start, len(words)):
        current_words = words[segment_start : index + 1]
        current_text = join_words(current_words, language)
        if not current_text:
            continue

        current_char_count = effective_char_count(current_text, language)
        next_word = words[index + 1] if index + 1 < len(words) else None
        break_reason = classify_safe_break(
            current_text,
            words[index],
            next_word,
            current_char_count,
            split_on_pause,
            prefer_punctuation_split,
            pause_threshold,
            min_split_chars,
        )
        if break_reason is not None:
            candidate = build_segment_from_words(
                current_words,
                language,
                fallback_start,
                fallback_end,
                allow_fallback=False,
            )
            if candidate is not None and not is_punctuation_only(candidate["text"]):
                last_safe_break = index + 1
                if should_commit_immediately(break_reason):
                    return index + 1

        current_start, current_end = resolve_span(current_words, fallback_start, fallback_end)
        current_duration = max(0.0, current_end - current_start)
        has_hit_limit = current_char_count >= max_chars or current_duration >= max_duration
        if has_hit_limit and last_safe_break is not None:
            return last_safe_break
    return len(words)


def segment_duration(segment: Dict[str, Any]) -> float:
    return max(0.0, to_float(segment.get("end"), 0.0) - to_float(segment.get("start"), 0.0))


def segment_gap(left: Dict[str, Any], right: Dict[str, Any]) -> float:
    return to_float(right.get("start"), 0.0) - to_float(left.get("end"), 0.0)


def has_suspicious_timing(segment: Dict[str, Any], language: Optional[str]) -> bool:
    text = str(segment.get("text") or "").strip()
    chars = effective_char_count(text, language)
    duration = segment_duration(segment)
    if chars <= 0:
        return True
    if is_punctuation_only(text):
        return duration >= 0.5
    if chars <= 2 and duration >= 2.5:
        return True
    return False


def merge_segments(
    base: Dict[str, Any],
    extra: Dict[str, Any],
    language: Optional[str],
    include_timing: bool,
) -> Dict[str, Any]:
    merged = {
        "start": to_float(base.get("start"), 0.0),
        "end": to_float(base.get("end"), 0.0),
        "text": merge_text_parts(str(base.get("text") or ""), str(extra.get("text") or ""), language),
    }
    if include_timing:
        merged["start"] = min(merged["start"], to_float(extra.get("start"), merged["start"]))
        merged["end"] = max(merged["end"], to_float(extra.get("end"), merged["end"]))
    return merged


def should_merge_into_previous(
    previous: Dict[str, Any],
    current: Dict[str, Any],
    language: Optional[str],
    min_split_chars: int,
) -> bool:
    text = str(current.get("text") or "").strip()
    if not text:
        return True

    chars = effective_char_count(text, language)
    duration = segment_duration(current)
    gap = segment_gap(previous, current)
    previous_text = str(previous.get("text") or "").strip()

    if is_punctuation_only(text):
        return True
    if has_suspicious_timing(current, language):
        return True
    if chars <= 2 and duration <= 0.2 and gap <= 0.2 and previous_text and not ends_with_sentence_boundary(previous_text):
        return True
    if chars < min_split_chars and gap <= 0.05 and previous_text and not ends_with_sentence_boundary(previous_text):
        return True
    if gap < -0.05 and chars <= max(2, min_split_chars):
        return True
    return False


def repair_split_segments(
    segments: List[Dict[str, Any]],
    language: Optional[str],
    segmentation_options: Dict[str, Any],
) -> List[Dict[str, Any]]:
    min_split_chars = max(1, to_int(segmentation_options.get("min_split_chars"), 4))
    repaired: List[Dict[str, Any]] = []
    pending_prefix: Optional[Dict[str, Any]] = None

    for raw_segment in normalize_segments(segments):
        segment = {
            "start": to_float(raw_segment.get("start"), 0.0),
            "end": to_float(raw_segment.get("end"), 0.0),
            "text": str(raw_segment.get("text") or "").strip(),
        }
        if not segment["text"]:
            continue

        if pending_prefix is not None:
            if has_suspicious_timing(pending_prefix, language):
                segment["text"] = merge_text_parts(str(pending_prefix.get("text") or ""), segment["text"], language)
            else:
                segment = merge_segments(pending_prefix, segment, language, include_timing=True)
            pending_prefix = None

        if not repaired:
            if is_punctuation_only(segment["text"]) or has_suspicious_timing(segment, language):
                pending_prefix = segment
                continue
            repaired.append(segment)
            continue

        previous = repaired[-1]
        if should_merge_into_previous(previous, segment, language, min_split_chars):
            repaired[-1] = merge_segments(
                previous,
                segment,
                language,
                include_timing=not has_suspicious_timing(segment, language),
            )
            continue

        repaired.append(segment)

    if pending_prefix is not None:
        if repaired:
            if has_suspicious_timing(pending_prefix, language):
                repaired[-1]["text"] = merge_text_parts(
                    repaired[-1]["text"], str(pending_prefix.get("text") or ""), language
                )
            else:
                repaired[-1] = merge_segments(repaired[-1], pending_prefix, language, include_timing=True)
        else:
            repaired.append(pending_prefix)

    return normalize_segments(repaired)


def split_segment_by_words(
    raw_segment: Dict[str, Any],
    language: Optional[str],
    segmentation_options: Dict[str, Any],
) -> List[Dict[str, Any]]:
    start = to_float(raw_segment.get("start"), 0.0)
    end = to_float(raw_segment.get("end"), start)
    text = str(raw_segment.get("text") or "").strip()
    if not text:
        return []

    words = normalize_word_entries(raw_segment.get("words"), language)
    if not words:
        return [{"start": start, "end": end if end >= start else start, "text": text}]

    split_on_pause = to_bool(segmentation_options.get("split_on_pause"), True)
    prefer_punctuation_split = to_bool(segmentation_options.get("prefer_punctuation_split"), True)
    pause_threshold = max(0.0, to_float(segmentation_options.get("pause_threshold_sec"), 0.8))
    max_duration = max(0.5, to_float(segmentation_options.get("max_segment_duration_sec"), 6.0))
    max_chars = max(1, to_int(segmentation_options.get("max_segment_chars"), 42))
    min_split_chars = max(
        1,
        min(max_chars, to_int(segmentation_options.get("min_split_chars"), min(max_chars, 10))),
    )

    split_segments: List[Dict[str, Any]] = []
    segment_start = 0
    while segment_start < len(words):
        segment_end = find_segment_end_index(
            words,
            segment_start,
            language,
            start,
            end,
            split_on_pause,
            prefer_punctuation_split,
            pause_threshold,
            max_duration,
            max_chars,
            min_split_chars,
        )
        if segment_end <= segment_start:
            segment_end = segment_start + 1

        allow_fallback = segment_start == 0 and segment_end == len(words)
        segment = build_segment_from_words(
            words[segment_start:segment_end],
            language,
            start,
            end,
            allow_fallback=allow_fallback,
        )
        if segment is not None:
            split_segments.append(segment)
        segment_start = segment_end

    return split_segments or [{"start": start, "end": end, "text": text}]


def normalize_transcript_segments(
    raw_segments: Any,
    language: Optional[str],
    segmentation_options: Dict[str, Any],
) -> List[Dict[str, Any]]:
    if not isinstance(raw_segments, list):
        return []

    split_segments: List[Dict[str, Any]] = []
    for item in raw_segments:
        if not isinstance(item, dict):
            continue
        split_segments.extend(split_segment_by_words(item, language, segmentation_options))
    return repair_split_segments(split_segments, language, segmentation_options)


def build_segmentation_options(language: Optional[str]) -> Dict[str, Any]:
    options: Dict[str, Any] = dict(DEFAULT_SEGMENTATION_OPTIONS)
    if language in LANGUAGES_WITHOUT_SPACES:
        options.update(NO_SPACE_LANGUAGE_SEGMENTATION_OPTIONS)
    if language == "ja":
        options.update(JAPANESE_SEGMENTATION_OPTIONS)
    return options


def should_apply_custom_segmentation(language: Optional[str]) -> bool:
    return language == "ja"


def build_asr_options(language: Optional[str]) -> Dict[str, Any]:
    if language == "ja":
        return {"initial_prompt": "句読点を含めて自然な文として書き起こしてください。"}
    return {}


def choose_align_device() -> str:
    return "cpu"


def get_env_path(name: str) -> Optional[str]:
    value = str(os.environ.get(name) or "").strip()
    return value or None


def transcribe(args: argparse.Namespace) -> Dict[str, Any]:
    language = None if args.language == "auto" else args.language
    audio = read_wav_pcm_s16le(args.wav_path)
    asr_model_dir = get_env_path("WHISPERX_ASR_MODEL_DIR")
    align_model_dir = get_env_path("WHISPERX_ALIGN_MODEL_DIR")

    with contextlib.redirect_stdout(sys.stderr), contextlib.redirect_stderr(sys.stderr):
        model = whisperx.load_model(
            args.model,
            args.asr_device,
            compute_type=args.compute_type,
            language=language,
            asr_options=build_asr_options(language),
            download_root=asr_model_dir,
        )
        result = model.transcribe(
            audio,
            batch_size=args.batch_size,
            print_progress=True,
            verbose=False,
        )

    detected_language = str(result.get("language") or language or "unknown")
    normalized_segments = normalize_segments(result.get("segments"))

    if not args.no_align and normalized_segments:
        try:
            align_device = args.align_device or choose_align_device()
            with contextlib.redirect_stdout(sys.stderr), contextlib.redirect_stderr(sys.stderr):
                align_model, align_metadata = whisperx.load_align_model(
                    language_code=detected_language,
                    device=align_device,
                    model_dir=align_model_dir,
                )
                aligned = whisperx.align(
                    result["segments"],
                    align_model,
                    align_metadata,
                    audio,
                    align_device,
                    return_char_alignments=False,
                    print_progress=True,
                )
            detected_language = str(aligned.get("language") or detected_language)
            if should_apply_custom_segmentation(detected_language):
                segmentation_options = build_segmentation_options(detected_language)
                normalized_segments = normalize_transcript_segments(
                    aligned.get("segments"),
                    detected_language,
                    segmentation_options,
                )
            else:
                normalized_segments = normalize_segments(aligned.get("segments"))
        except Exception as exc:
            print(f"[WhisperX] align skipped: {exc}", file=sys.stderr)

    return {
        "language": detected_language,
        "duration_sec": float(len(audio)) / float(SAMPLE_RATE),
        "segments": normalized_segments,
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--wav-path", required=True)
    parser.add_argument("--model", default="medium")
    parser.add_argument("--language", default="auto")
    parser.add_argument("--compute-type", default="int8")
    parser.add_argument("--batch-size", type=int, default=8)
    parser.add_argument("--asr-device", default="cpu")
    parser.add_argument("--align-device", default="cpu")
    parser.add_argument("--no-align", action="store_true")
    return parser.parse_args()


def main() -> int:
    configure_stdio()
    try:
        args = parse_args()
        payload = transcribe(args)
        print(json.dumps(payload, ensure_ascii=False))
        return 0
    except Exception as exc:
        print(str(exc), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
