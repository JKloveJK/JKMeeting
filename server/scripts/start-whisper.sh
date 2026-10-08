#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
MODEL_PATH="$ROOT_DIR/whisper/models/ggml-base.bin"
WHISPER_SERVER=${WHISPER_SERVER:-$(command -v whisper-server)}
PORT=${WHISPER_PORT:-8081}

if [ -z "$WHISPER_SERVER" ] || [ ! -x "$WHISPER_SERVER" ]; then
  printf '%s\n' "未找到 whisper-server，请先安装 whisper-cpp" >&2
  exit 1
fi
"$ROOT_DIR/scripts/download-whisper-model.sh"
printf 'whisper-server=%s\n' "$WHISPER_SERVER"
printf 'model=%s\n' "$MODEL_PATH"
printf 'port=%s\n' "$PORT"
exec "$WHISPER_SERVER" -m "$MODEL_PATH" --host 127.0.0.1 --port "$PORT" --convert
