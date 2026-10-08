#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
MODEL_DIR="$ROOT_DIR/whisper/models"
MODEL_PATH="$MODEL_DIR/ggml-base.bin"
MODEL_URL="https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.bin"
MODEL_SHA256="60ed5bc3dd14eea856493d334349b405782ddcaf0028d4b5df4088345fba2efe"

mkdir -p "$MODEL_DIR"
if [ ! -f "$MODEL_PATH" ]; then
  curl -fL --retry 3 --output "$MODEL_PATH.part" "$MODEL_URL"
  mv "$MODEL_PATH.part" "$MODEL_PATH"
fi

printf '%s  %s\n' "$MODEL_SHA256" "$MODEL_PATH" | shasum -a 256 -c -
printf 'Whisper base model verified: %s\n' "$MODEL_PATH"
