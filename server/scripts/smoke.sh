#!/bin/sh
set -eu

API_BASE_URL=${API_BASE_URL:-http://127.0.0.1:8082}
API_BEARER_TOKEN=${API_BEARER_TOKEN:?需要设置 API_BEARER_TOKEN}

create_response=$(curl -fsS -X POST "$API_BASE_URL/v1/recordings" \
  -H "Authorization: Bearer $API_BEARER_TOKEN" \
  -H "Content-Type: application/json" \
  --data '{"duration_ms":1000,"size_bytes":4,"content_type":"audio/mp4"}')

recording_id=$(printf '%s' "$create_response" | jq -r '.recording_id')
upload_url=$(printf '%s' "$create_response" | jq -r '.upload_url')

[ -n "$recording_id" ] && [ "$recording_id" != "null" ]
[ -n "$upload_url" ] && [ "$upload_url" != "null" ]

printf '\000\001\002\003' | curl -fsS -X PUT "$upload_url" -H "Content-Type: audio/mp4" --data-binary @-

curl -fsS -X POST "$API_BASE_URL/v1/recordings/$recording_id/complete" \
  -H "Authorization: Bearer $API_BEARER_TOKEN" >/dev/null

status_response=$(curl -fsS "$API_BASE_URL/v1/recordings/$recording_id" \
  -H "Authorization: Bearer $API_BEARER_TOKEN")

printf '%s' "$status_response" | jq -e '.status == "uploaded"' >/dev/null

playback_response=$(curl -fsS "$API_BASE_URL/v1/recordings/$recording_id/playback" \
  -H "Authorization: Bearer $API_BEARER_TOKEN")
playback_url=$(printf '%s' "$playback_response" | jq -r '.playback_url')
[ -n "$playback_url" ] && [ "$playback_url" != "null" ]

curl -fsS "$playback_url" >/dev/null
printf 'P0 服务端冒烟成功：recording_id=%s\n' "$recording_id"
