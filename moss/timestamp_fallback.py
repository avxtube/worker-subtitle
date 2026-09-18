"""Local timestamped ASR fallback; never invent timing for untimed MOSS text."""
from __future__ import annotations

import math
from pathlib import Path
import re
import sys

EMPTY_SEGMENTS = re.compile(r"^(\s*\[\d+(?:\.\d+)?\]\s*\[S\d+\]\s*\[\d+(?:\.\d+)?\]\s*)+$")


def needs_timestamp_fallback(text, segments, generated_tokens, token_limit):
    return (bool(text.strip()) and not segments and generated_tokens < token_limit
            and not EMPTY_SEGMENTS.fullmatch(text))


def normalize_segments(segments, duration):
    result = []
    for segment in segments:
        start, end, text = float(segment.start), float(segment.end), segment.text.strip()
        if not text:
            continue
        if (not math.isfinite(start) or not math.isfinite(end) or start < 0
                or end <= start or start >= duration or end > duration + 1):
            raise ValueError("Timestamp fallback returned invalid audio bounds")
        result.append({"start": start, "end": min(end, duration),
                       "speaker": "unknown", "text": text, "backend": "faster-whisper-small"})
    if not result:
        # A disagreement between recognizers is not proof that the clip is silent.
        raise ValueError("Timestamp fallback returned no timed speech; not classifying as no_speech")
    return result


class TimestampFallback:
    def __init__(self, runtime_root):
        self.model_dir = Path(runtime_root) / "models" / "faster-whisper-small"
        self.model = None

    def transcribe(self, audio):
        if self.model is None:
            from faster_whisper import WhisperModel
            print(f"[subtitle] Preparing timestamp fallback model: {self.model_dir} (CPU int8)", file=sys.stderr, flush=True)
            self.model_dir.mkdir(parents=True, exist_ok=True)
            self.model = WhisperModel("small", device="cpu", compute_type="int8",
                                      cpu_threads=4, download_root=str(self.model_dir))
        print("[subtitle] MOSS has no timed segments; transcribing this chunk with faster-whisper-small", file=sys.stderr, flush=True)
        segments, info = self.model.transcribe(
            str(audio), beam_size=5, temperature=0, condition_on_previous_text=False,
            word_timestamps=True, vad_filter=True,
        )
        timed = normalize_segments(segments, info.duration)
        return {"text": " ".join(segment["text"] for segment in timed),
                "segments": timed, "generated_tokens": 0,
                "backend": "faster-whisper-small", "language": info.language}


def with_timestamp_fallback(payload, audio, token_limit, fallback):
    if not needs_timestamp_fallback(payload["text"], payload["segments"],
                                    payload["generated_tokens"], token_limit):
        return payload
    raw = {"moss_text": payload["text"], "moss_generated_tokens": payload["generated_tokens"]}
    try:
        return {**fallback.transcribe(audio), **raw}
    except Exception as exc:
        return {**payload, **raw, "error": f"Timestamp fallback failed: {exc}",
                "retryable": False, "backend": "faster-whisper-small"}
