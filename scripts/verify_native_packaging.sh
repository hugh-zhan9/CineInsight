#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
artifact_root="$repo_root/target/native-package-smoke"

rm -rf "$artifact_root"
mkdir -p "$artifact_root/bin" "$artifact_root/contracts" "$artifact_root/sidecars" "$artifact_root/runtime"

cd "$repo_root/rust"
cargo build --bin cine-daemon

cd "$repo_root/macos/CineInsightNative"
swift build --product CineInsightNative

cp "$repo_root/rust/target/debug/cine-daemon" "$artifact_root/bin/cine-daemon"
cp "$repo_root/contracts/native-api.yaml" "$artifact_root/contracts/native-api.yaml"
if [[ ! -s "$repo_root/frontend/dist/short.html" ]]; then
  (cd "$repo_root/frontend" && npm run build)
fi
mkdir -p "$artifact_root/short-feed"
cp "$repo_root/frontend/dist/short.html" "$artifact_root/short-feed/short.html"
cp -R "$repo_root/frontend/dist/assets" "$artifact_root/short-feed/assets"
cp "$repo_root/services/whisperx_worker.py" "$artifact_root/sidecars/whisperx_worker.py"
cp "$repo_root/services/qwen_asr_worker.py" "$artifact_root/sidecars/qwen_asr_worker.py"
mkdir -p "$artifact_root/runtime/whisperx_sidecar/venv/bin" "$artifact_root/runtime/whisperx_sidecar/hf" "$artifact_root/runtime/whisperx_sidecar/torch"
mkdir -p "$artifact_root/runtime/qwen_asr_sidecar/venv/bin" "$artifact_root/runtime/qwen_asr_sidecar/hf" "$artifact_root/runtime/qwen_asr_sidecar/torch"
cp "$repo_root/services/whisperx_worker.py" "$artifact_root/runtime/whisperx_sidecar/whisperx_worker.py"
cp "$repo_root/services/qwen_asr_worker.py" "$artifact_root/runtime/qwen_asr_sidecar/qwen_asr_worker.py"
python_bin="$(command -v python3 || true)"
if [[ -n "$python_bin" ]]; then
  cat > "$artifact_root/runtime/whisperx_sidecar/venv/bin/python3" <<PYTHON
#!/usr/bin/env sh
exec "$python_bin" "\$@"
PYTHON
  cat > "$artifact_root/runtime/qwen_asr_sidecar/venv/bin/python3" <<PYTHON
#!/usr/bin/env sh
exec "$python_bin" "\$@"
PYTHON
fi
cat > "$artifact_root/runtime/manifest.json" <<'MANIFEST'
{"schema":1}
MANIFEST
cat > "$artifact_root/runtime/README.txt" <<'RUNTIME'
CineInsight ASR runtime cache
RUNTIME
chmod +x "$artifact_root/runtime/whisperx_sidecar/venv/bin/python3" "$artifact_root/runtime/qwen_asr_sidecar/venv/bin/python3"

test -x "$artifact_root/bin/cine-daemon"
test -s "$artifact_root/contracts/native-api.yaml"
test -s "$artifact_root/short-feed/short.html"
test -d "$artifact_root/short-feed/assets"
test -s "$artifact_root/sidecars/whisperx_worker.py"
test -s "$artifact_root/sidecars/qwen_asr_worker.py"
test -d "$artifact_root/runtime"
test -s "$artifact_root/runtime/manifest.json"
test -s "$artifact_root/runtime/whisperx_sidecar/whisperx_worker.py"
test -s "$artifact_root/runtime/qwen_asr_sidecar/qwen_asr_worker.py"
test -d "$artifact_root/runtime/whisperx_sidecar/venv/bin"
test -d "$artifact_root/runtime/qwen_asr_sidecar/venv/bin"
test -x "$artifact_root/runtime/whisperx_sidecar/venv/bin/python3"
test -x "$artifact_root/runtime/qwen_asr_sidecar/venv/bin/python3"

echo "native packaging smoke passed"
