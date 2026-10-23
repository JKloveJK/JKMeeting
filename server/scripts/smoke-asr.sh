#!/bin/sh
set -eu

API_BASE_URL=${API_BASE_URL:-http://127.0.0.1:8082}
API_BEARER_TOKEN=${API_BEARER_TOKEN:?需要设置 API_BEARER_TOKEN}
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

# AUDIO_FORMAT=m4a（默认，AAC）或 ogg（由 core 的 ogg_opus_encode 工具编码，覆盖自研 Ogg 封装）
AUDIO_FORMAT=${AUDIO_FORMAT:-m4a}
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)

aiff="$work_dir/sample.aiff"
say -o "$aiff" "This is a test meeting transcription."
case "$AUDIO_FORMAT" in
  m4a)
    audio="$work_dir/sample.m4a"
    content_type="audio/mp4"
    ffmpeg -hide_banner -loglevel error -y -i "$aiff" -c:a aac "$audio"
    ;;
  ogg)
    audio="$work_dir/sample.ogg"
    content_type="audio/ogg"
    encoder="$ROOT_DIR/core/build/ogg_opus_encode"
    cmake -S "$ROOT_DIR/core" -B "$ROOT_DIR/core/build" >/dev/null
    cmake --build "$ROOT_DIR/core/build" --target ogg_opus_encode >/dev/null
    ffmpeg -hide_banner -loglevel error -y -i "$aiff" -ac 1 -ar 16000 -f s16le "$work_dir/sample.pcm"
    "$encoder" --rate 16000 --channels 1 --bitrate 24000 < "$work_dir/sample.pcm" > "$audio"
    ;;
  *)
    printf 'AUDIO_FORMAT 仅支持 m4a 或 ogg：%s\n' "$AUDIO_FORMAT" >&2
    exit 2
    ;;
esac
size_bytes=$(wc -c < "$audio" | tr -d ' ')
printf 'audio_format=%s content_type=%s size_bytes=%s\n' "$AUDIO_FORMAT" "$content_type" "$size_bytes"

create_response=$(curl -fsS -X POST "$API_BASE_URL/v1/recordings" \
  -H "Authorization: Bearer $API_BEARER_TOKEN" \
  -H "Content-Type: application/json" \
  --data "{\"duration_ms\":3000,\"size_bytes\":$size_bytes,\"content_type\":\"$content_type\"}")
recording_id=$(printf '%s' "$create_response" | jq -r '.recording_id')
upload_url=$(printf '%s' "$create_response" | jq -r '.upload_url')

curl -fsS -X PUT "$upload_url" -H "Content-Type: $content_type" --data-binary "@$audio"
curl -fsS -X POST "$API_BASE_URL/v1/recordings/$recording_id/complete" \
  -H "Authorization: Bearer $API_BEARER_TOKEN" >/dev/null

attempt=1
while [ "$attempt" -le 30 ]; do
  response=$(curl -fsS "$API_BASE_URL/v1/recordings/$recording_id" -H "Authorization: Bearer $API_BEARER_TOKEN")
  status=$(printf '%s' "$response" | jq -r '.status')
  printf 'attempt=%s status=%s\n' "$attempt" "$status"
  if [ "$status" = "done" ]; then
    transcript=$(printf '%s' "$response" | jq -r '.transcript')
    printf 'ASR 冒烟成功（%s）：recording_id=%s transcript=%s\n' "$AUDIO_FORMAT" "$recording_id" "$transcript"
    exit 0
  fi
  if [ "$status" = "failed" ]; then
    printf '%s\n' "$response" >&2
    exit 1
  fi
  sleep 2
  attempt=$((attempt + 1))
done
printf '等待 ASR 超时：recording_id=%s\n' "$recording_id" >&2
exit 1
