import argparse
import json
from pathlib import Path
from huggingface_hub import HfApi, snapshot_download

parser = argparse.ArgumentParser()
parser.add_argument("--output", required=True)
parser.add_argument("--revision", default="main")
parser.add_argument("--check", action="store_true")
args = parser.parse_args()
root = Path(args.output)
def complete():
    required = ["config.json", "preprocessor_config.json", "processor_config.json",
                "tokenizer_config.json", "tokenizer.json", "chat_template.jinja",
                "modeling_moss_transcribe_diarize.py", "processing_moss_transcribe_diarize.py",
                "configuration_moss_transcribe_diarize.py"]
    if not all((root / name).is_file() and (root / name).stat().st_size for name in required):
        return False
    index = root / "model.safetensors.index.json"
    try:
        weights = set(json.loads(index.read_text())["weight_map"].values()) if index.is_file() else {"model.safetensors"}
        return bool(weights) and all((root / name).is_file() and (root / name).stat().st_size > 0 for name in weights)
    except (ValueError, KeyError, OSError):
        return False
if args.check:
    if not complete():
        raise SystemExit("Model files missing or incomplete; download required")
    print("Model files present (offline check)", flush=True)
    raise SystemExit(0)
repo = "OpenMOSS-Team/MOSS-Transcribe-Diarize"
revision = HfApi().model_info(repo, revision=args.revision).sha
snapshot_download(repo, revision=revision, local_dir=args.output)
if not complete():
    raise RuntimeError("Model download incomplete; retry to resume")
Path(args.output, "installed-revision.json").write_text(
    json.dumps({"repository": repo, "revision": revision}, indent=2), encoding="utf-8"
)
