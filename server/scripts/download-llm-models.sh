#!/bin/sh
# P3 本地模型：对话模型 Qwen2.5-7B-Instruct（Q4_K_M）与 embedding 模型 bge-m3（Q8_0），均由 llama.cpp 加载。
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
MODEL_DIR="$ROOT_DIR/llm/models"
mkdir -p "$MODEL_DIR"

fetch() {
  name=$1
  url=$2
  sha256=$3
  path="$MODEL_DIR/$name"
  if [ ! -f "$path" ]; then
    curl -fL --retry 3 -C - --output "$path.part" "$url"
    mv "$path.part" "$path"
  fi
  printf '%s  %s\n' "$sha256" "$path" | shasum -a 256 -c -
}

fetch "Qwen2.5-7B-Instruct-Q4_K_M.gguf" \
  "https://huggingface.co/bartowski/Qwen2.5-7B-Instruct-GGUF/resolve/main/Qwen2.5-7B-Instruct-Q4_K_M.gguf" \
  "65b8fcd92af6b4fefa935c625d1ac27ea29dcb6ee14589c55a8f115ceaaa1423"
fetch "bge-m3-Q8_0.gguf" \
  "https://huggingface.co/gpustack/bge-m3-GGUF/resolve/main/bge-m3-Q8_0.gguf" \
  "950f4a8e5e19477a6d3c26d2f162233c20002c601f75e4b002e3239997821167"
printf 'LLM 模型已校验：%s\n' "$MODEL_DIR"
