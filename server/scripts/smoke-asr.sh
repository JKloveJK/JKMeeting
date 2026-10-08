#!/bin/sh
set -eu

API_BASE_URL=${API_BASE_URL:-http://127.0.0.1:8082}
API_BEARER_TOKEN=${API_BEARER_TOKEN:?需要设置 API_BEARER_TOKEN}
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

aiff="$work_dir/sample.aiff"
m4a="$work_dir/sample.m4a"
say -o "$aiff" "This is a test meeting transcription."
ffmpeg -hide_banner -loglevel error -y -i "$aiff" -c:a aac "$m4a"
size_bytes=$(wc -c < "$m4a" | tr -d ' ')

create_response=$(curl -fsS -X POST "$API_BASE_URL/v1/recordings" \
  -H "Authorization: Bearer $API_BEARER_TOKEN" \
  -H "Content-Type: application/json" \
  --data "{\"duration_ms\":3000,\"size_bytes\":$size_bytes,\"content_type\":\"audio/mp4\"}")
recording_id=$(printf '%s' "$create_response" | jq -r '.recording_id')
upload_url=$(printf '%s' "$create_response" | jq -r '.upload_url')

curl -fsS -X PUT "$upload_url" -H "Content-Type: audio/mp4" --data-binary "@$m4a"
curl -fsS -X POST "$API_BASE_URL/v1/recordings/$recording_id/complete" \
  -H "Authorization: Bearer $API_BEARER_TOKEN" >/dev/null

attempt=1
while [ "$attempt" -le 30 ]; do
  response=$(curl -fsS "$API_BASE_URL/v1/recordings/$recording_id" -H "Authorization: Bearer $API_BEARER_TOKEN")
  status=$(printf '%s' "$response" | jq -r '.status')
  if [ "$status" = "done" ]; then
    transcript=$(printf '%s' "$response" | jq -r '.transcript')
    printf 'P0 ASR 冒烟成功：recording_id=%s transcript=%s\n' "$recording_id" "$transcript"
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
