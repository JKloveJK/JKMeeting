#!/bin/bash
# P2-4 HLS 冒烟：上传 core 编码的 Ogg/Opus，等待转码 ready，校验签名 master、三档 variant 与转写互不阻塞。
set -euo pipefail

API_BASE_URL=${API_BASE_URL:-http://127.0.0.1:8082}
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
EXPECTED_VARIANTS=${EXPECTED_VARIANTS:-32,64,96}
MAX_WAIT_SECONDS=${MAX_WAIT_SECONDS:-60}
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

auth=(-H "Authorization: Bearer $API_BEARER_TOKEN")
psql_query() {
  docker exec -i jkmeeting-postgres-1 psql -U jkmeeting -d jkmeeting -At -c "$1" 2>/dev/null || true
}
dump_state() {
  printf -- '--- 诊断：recording_id=%s ---\n' "$recording_id" >&2
  psql_query "SELECT 'transcode_jobs', status, attempts, coalesce(error_message,'') FROM transcode_jobs WHERE recording_id='$recording_id'" >&2
  psql_query "SELECT 'transcode_outbox', status, claimed_at, dispatched_at FROM transcode_outbox WHERE recording_id='$recording_id'" >&2
  psql_query "SELECT 'recordings', status FROM recordings WHERE id='$recording_id'" >&2
  tail -5 /tmp/jk-worker.log >&2 2>/dev/null || true
}

# 约 20 秒语音，确保每档至少 3 个 6 秒分片。
aiff="$work_dir/sample.aiff"
say -o "$aiff" "This is a test meeting transcription. We are verifying adaptive bitrate streaming. \
The server transcodes the opus recording into three AAC renditions. Each rendition is split into six second segments. \
The client can switch between thirty two, sixty four and ninety six kilobits per second."
encoder="$ROOT_DIR/core/build/ogg_opus_encode"
cmake -S "$ROOT_DIR/core" -B "$ROOT_DIR/core/build" >/dev/null
cmake --build "$ROOT_DIR/core/build" --target ogg_opus_encode >/dev/null
# LOOP_SECONDS 可选：循环样本到指定时长，用于客户端档位切换等需要较长内容的验证。
loop_args=()
[ -n "${LOOP_SECONDS:-}" ] && loop_args=(-stream_loop -1 -t "$LOOP_SECONDS")
ffmpeg -hide_banner -loglevel error -y ${loop_args[@]+"${loop_args[@]}"} -i "$aiff" -ac 1 -ar 16000 -f s16le "$work_dir/sample.pcm"
audio="$work_dir/sample.ogg"
"$encoder" --rate 16000 --channels 1 --bitrate 24000 < "$work_dir/sample.pcm" > "$audio"
size_bytes=$(wc -c < "$audio" | tr -d ' ')
duration_ms=$(( $(wc -c < "$work_dir/sample.pcm") / 32 ))
printf 'audio=ogg size_bytes=%s duration_ms=%s\n' "$size_bytes" "$duration_ms"

create_response=$(curl -fsS -X POST "$API_BASE_URL/v1/recordings" "${auth[@]}" -H "Content-Type: application/json" \
  --data "{\"duration_ms\":$duration_ms,\"size_bytes\":$size_bytes,\"content_type\":\"audio/ogg\"}")
recording_id=$(jq -r '.recording_id' <<<"$create_response")
upload_url=$(jq -r '.upload_url' <<<"$create_response")
curl -fsS -X PUT "$upload_url" -H "Content-Type: audio/ogg" --data-binary "@$audio"
curl -fsS -X POST "$API_BASE_URL/v1/recordings/$recording_id/complete" "${auth[@]}" >/dev/null
started=$(date +%s)
printf 'recording_id=%s 已上传\n' "$recording_id"

last_state=""
while :; do
  playback=$(curl -fsS "$API_BASE_URL/v1/recordings/$recording_id/playback" "${auth[@]}")
  transcode_status=$(jq -r '.transcode_status' <<<"$playback")
  recording_status=$(curl -fsS "$API_BASE_URL/v1/recordings/$recording_id" "${auth[@]}" | jq -r '.status')
  elapsed=$(( $(date +%s) - started ))
  state="transcode=$transcode_status recording=$recording_status"
  if [ "$state" != "$last_state" ]; then
    printf 't=%ss %s\n' "$elapsed" "$state"
    last_state=$state
  fi
  if [ "$transcode_status" = "failed" ] || [ "$recording_status" = "failed" ]; then
    printf '%s\n' "$playback" >&2
    dump_state
    exit 1
  fi
  if [ "$transcode_status" = "ready" ] && [ "$recording_status" = "done" ]; then
    break
  fi
  if [ "$elapsed" -ge "$MAX_WAIT_SECONDS" ]; then
    printf '等待超时（%ss）\n' "$MAX_WAIT_SECONDS" >&2
    dump_state
    exit 1
  fi
  sleep 1
done

variants=$(jq -r '.hls_variants_kbps' <<<"$playback")
hls_url="$API_BASE_URL$(jq -r '.hls_url' <<<"$playback")"
[ "$variants" = "$EXPECTED_VARIANTS" ] || { printf 'variants=%s，期望 %s\n' "$variants" "$EXPECTED_VARIANTS" >&2; exit 1; }

# 未签名访问必须被拒绝。
unsigned_code=$(curl -s -o /dev/null -w '%{http_code}' "${hls_url%%\?*}")
[ "$unsigned_code" = "403" ] || { printf '未签名 master 返回 %s，期望 403\n' "$unsigned_code" >&2; exit 1; }

master="$work_dir/master.m3u8"
curl -fsS "$hls_url" -o "$master"
variant_count=$(grep -c '^#EXT-X-STREAM-INF' "$master")
printf 'master: variant_count=%s unsigned=403\n' "$variant_count"
[ "$variant_count" = "3" ] || { cat "$master" >&2; exit 1; }

base="${hls_url%%master.m3u8*}"
while read -r uri; do
  variant_url="$base$uri"
  media="$work_dir/media.m3u8"
  curl -fsS "$variant_url" -o "$media"
  segments=$(grep -c '\.m4s' "$media")
  probe=$(ffprobe -v error -select_streams a:0 -show_entries stream=codec_name,sample_rate,channels \
    -of csv=p=0 "$variant_url" | head -1)
  # ffprobe 对 playlist 给出的 format.bit_rate 不反映媒体码率，按分片实际字节数计算。
  segment_bytes=0
  while read -r segment_url; do
    segment_bytes=$(( segment_bytes + $(curl -fsS "$segment_url" | wc -c) ))
  done < <(grep '\.m4s' "$media")
  measured_kbps=$(( segment_bytes * 8 / duration_ms ))
  printf 'variant=%s segments=%s stream=%s measured_kbps=%s\n' "${uri%%/*}" "$segments" "$probe" "$measured_kbps"
  grep -q '^aac,' <<<"$probe" || { printf 'variant 非 AAC\n' >&2; exit 1; }
done < <(grep -v '^#' "$master" | grep -v '^$')

transcode_ms=$(psql_query "SELECT transcode_ms FROM transcode_jobs WHERE recording_id='$recording_id'")
printf 'HLS 冒烟成功：recording_id=%s duration_ms=%s transcode_ms=%s variants=%s\n' \
  "$recording_id" "$duration_ms" "$transcode_ms" "$variants"
