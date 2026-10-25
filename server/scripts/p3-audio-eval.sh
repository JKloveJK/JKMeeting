#!/usr/bin/env bash
# P3-1 音频模式评测：评测集文字稿 -> say 合成语音 -> 上传 -> Whisper 转写 -> LLM 纪要 -> 规则评分。
# 需要 API、Worker、whisper-server 与 llama-server 均已启动。
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "$0")/../.." && pwd)
API_BASE_URL=${API_BASE_URL:-http://127.0.0.1:8082}
API_BEARER_TOKEN=${API_BEARER_TOKEN:?需要设置 API_BEARER_TOKEN}
CASES_FILE=${CASES_FILE:-$ROOT_DIR/server/eval/p3/cases.json}
AUDIO_CASES=${AUDIO_CASES:-c01-release,c02-incident,c05-design,c08-migration,c14-appstore}
VOICE=${VOICE:-Tingting}
MAX_WAIT_SECONDS=${MAX_WAIT_SECONDS:-600}
STALL_SECONDS=${STALL_SECONDS:-180}
OUT_DIR=${OUT_DIR:-$ROOT_DIR/output}
stamp=$(date +%Y%m%d-%H%M%S)
mapping="$OUT_DIR/p3-1-audio-$stamp.recordings"
transcripts="$OUT_DIR/p3-1-audio-$stamp.transcripts.jsonl"
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
mkdir -p "$OUT_DIR"
: > "$mapping"
: > "$transcripts"

auth=(-H "Authorization: Bearer $API_BEARER_TOKEN")

dump_state() {
  local id=$1
  printf '状态未推进，收集诊断：recording_id=%s\n' "$id" >&2
  docker exec jkmeeting-postgres-1 psql -U jkmeeting -d jkmeeting -c \
    "SELECT r.status, i.status AS insights, i.attempts, i.error_message, o.status AS outbox
     FROM recordings r LEFT JOIN meeting_insights i ON i.recording_id = r.id
     LEFT JOIN analysis_outbox o ON o.recording_id = r.id WHERE r.id = '$id'" >&2 || true
  tail -n 5 /tmp/jk-worker.log >&2 2>/dev/null || true
}

IFS=',' read -r -a case_ids <<< "$AUDIO_CASES"
for case_id in "${case_ids[@]}"; do
  # 把 "姓名：" 改写为 "姓名说，"，让单音色合成后的转写仍保留说话人线索。
  speech=$(jq -r --arg id "$case_id" '.[] | select(.id == $id) | .transcript' "$CASES_FILE" | sed -E 's/^([^：]+)：/\1说，/')
  if [ -z "$speech" ]; then
    printf '评测集中没有用例：%s\n' "$case_id" >&2
    exit 2
  fi
  aiff="$work_dir/$case_id.aiff"
  audio="$work_dir/$case_id.m4a"
  say -v "$VOICE" -o "$aiff" "$speech"
  ffmpeg -hide_banner -loglevel error -y -i "$aiff" -ac 1 -c:a aac -b:a 64k "$audio"
  duration_ms=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$audio" | awk '{printf "%d", $1 * 1000}')
  size_bytes=$(wc -c < "$audio" | tr -d ' ')
  create=$(curl -fsS -X POST "$API_BASE_URL/v1/recordings" "${auth[@]}" -H "Content-Type: application/json" \
    --data "{\"duration_ms\":$duration_ms,\"size_bytes\":$size_bytes,\"content_type\":\"audio/mp4\"}")
  recording_id=$(jq -r '.recording_id' <<< "$create")
  curl -fsS -X PUT "$(jq -r '.upload_url' <<< "$create")" -H "Content-Type: audio/mp4" --data-binary "@$audio"
  curl -fsS -X POST "$API_BASE_URL/v1/recordings/$recording_id/complete" "${auth[@]}" >/dev/null
  printf '%s %s\n' "$case_id" "$recording_id" >> "$mapping"
  printf '已上传 case=%s recording_id=%s duration_ms=%s\n' "$case_id" "$recording_id" "$duration_ms"
done

while read -r case_id recording_id; do
  started=$(date +%s)
  last_state=""
  last_change=$started
  while :; do
    recording=$(curl -fsS "$API_BASE_URL/v1/recordings/$recording_id" "${auth[@]}")
    insights=$(curl -fsS "$API_BASE_URL/v1/recordings/$recording_id/insights" "${auth[@]}")
    state="$(jq -r '.status' <<< "$recording")/$(jq -r '.status' <<< "$insights")"
    now=$(date +%s)
    if [ "$state" != "$last_state" ]; then
      printf 'case=%s t=%ss recording/insights=%s\n' "$case_id" "$((now - started))" "$state"
      last_state=$state
      last_change=$now
    fi
    case "$state" in
      done/ready|done/failed|failed/*) break ;;
    esac
    if [ $((now - last_change)) -ge "$STALL_SECONDS" ] || [ $((now - started)) -ge "$MAX_WAIT_SECONDS" ]; then
      dump_state "$recording_id"
      exit 1
    fi
    sleep 2
  done
  jq -c --arg id "$case_id" '{case_id: $id, recording_id: .recording_id, transcript: .transcript}' <<< "$recording" >> "$transcripts"
done < "$mapping"

cd "$ROOT_DIR/server"
go run ./cmd/insighteval -mode api -cases "$CASES_FILE" -recordings "$mapping" \
  -api-base-url "$API_BASE_URL" -out "$OUT_DIR/p3-1-audio-$stamp.jsonl"
printf '映射：%s\n转写：%s\n' "$mapping" "$transcripts"
