"""Private JSONL service owned by the Go worker. Diagnostics go to stderr."""
from __future__ import annotations

import argparse
import contextlib
import json
import os
import sys
from dataclasses import asdict
from pathlib import Path
from timestamp_fallback import needs_timestamp_fallback


def emit(payload):
    print(json.dumps(payload, ensure_ascii=False), flush=True)


def transcribe_with_timestamps(runner, audio, prompt, tokens):
    from moss_transcribe_diarize.transcript_parser import parse_transcript
    result = runner.transcribe(audio, prompt=prompt, max_new_tokens=tokens,
                               decoding="greedy", timed_output="auto")
    segments = [{**asdict(segment), "backend": "moss-constrained"} for segment in parse_transcript(result.text)]
    payload = {"text": result.text, "segments": segments,
               "generated_tokens": result.generated_tokens, "backend": "moss-constrained"}
    if needs_timestamp_fallback(result.text, segments, result.generated_tokens, tokens):
        payload["error"] = "MOSS constrained decoding produced unparseable text; not classifying as silence"
    return payload


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--model-dir", required=True)
    args = parser.parse_args()
    # Isolate custom model imports from other workers/projects on this machine.
    cache_root = Path(__file__).resolve().parent.parent / ".cache" / "huggingface"
    os.environ["HF_HOME"] = str(cache_root)
    os.environ["HF_MODULES_CACHE"] = str(cache_root / "modules")
    # Keep library progress/logging out of the machine-readable stdout pipe.
    with contextlib.redirect_stdout(sys.stderr):
        from moss_transcribe_diarize.app.model_runner import ModelRunner
        from moss_transcribe_diarize.inference_utils import DEFAULT_PROMPT
        import torch

        if not torch.cuda.is_available():
            raise RuntimeError("CUDA GPU is required; install matching PyTorch and NVIDIA driver")
        model_dir = Path(args.model_dir).resolve(strict=True)
        runner = ModelRunner(model_dir, device="cuda", dtype="bf16")
        runner._ensure_loaded()
    emit({"ready": True})
    for line in sys.stdin:
        try:
            request = json.loads(line)
            if request.get("op") == "ping":
                emit({"ready": runner.is_loaded})
                continue
            audio = Path(request["audio"]).resolve(strict=True)
            tokens = int(request["max_new_tokens"])
            if not 128 <= tokens <= 8192:
                raise ValueError("max_new_tokens must be 128-8192")
            with contextlib.redirect_stdout(sys.stderr):
                payload = transcribe_with_timestamps(runner, audio, (request.get("prompt") or "").strip() or DEFAULT_PROMPT, tokens)
            emit(payload)
        except Exception as exc:
            import traceback
            traceback.print_exc(file=sys.stderr)
            emit({"error": str(exc), "retryable": isinstance(exc, torch.cuda.OutOfMemoryError)})
        finally:
            with contextlib.redirect_stdout(sys.stderr):
                runner.release_chunk_memory()


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        import traceback
        traceback.print_exc(file=sys.stderr)
        emit({"ready": False, "error": str(exc)})
        sys.exit(1)
