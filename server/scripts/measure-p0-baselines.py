#!/usr/bin/env python3
"""测量 P0 服务端与端到端性能基线（上传 P95、ASR RTF、松手到文本）。

不会打印或落盘任何 Bearer token 或预签名 URL query。
"""

import json
import math
import os
import re
import subprocess
import tempfile
import time
import urllib.request
from pathlib import Path


def percentile(sorted_values: list[float], p: float) -> float:
    if not sorted_values:
        return 0.0
    idx = min(len(sorted_values) - 1, max(0, math.ceil(p * len(sorted_values)) - 1))
    return sorted_values[idx]


def http_json(url: str, method: str, token: str, payload: dict | None = None) -> tuple[int, dict]:
    headers = {"Authorization": f"Bearer {token}"}
    data = None
    if payload is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    with urllib.request.urlopen(req, timeout=30) as resp:
        body = resp.read().decode("utf-8")
        return resp.status, (json.loads(body) if body else {})


def http_put_bytes(url: str, content: bytes) -> int:
    req = urllib.request.Request(
        url,
        data=content,
        headers={"Content-Type": "audio/mp4"},
        method="PUT",
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        resp.read()
        return resp.status


def build_30s_speech_m4a(work_dir: Path) -> tuple[Path, int]:
    aiff_path = work_dir / "speech-30s.aiff"
    m4a_path = work_dir / "speech-30s.m4a"
    text = (
        "Welcome to the JKMeeting architecture review. "
        "Today we are validating the end to end audio recording pipeline, "
        "including C plus plus core resampling, presigned object storage upload, "
        "transactional outbox dispatch, and asynchronous speech transcription with whisper. "
        "All metrics are recorded with reproducible evidence."
    )
    subprocess.run(["say", "-r", "150", "-o", str(aiff_path), text], check=True)
    subprocess.run(
        [
            "ffmpeg",
            "-hide_banner",
            "-loglevel",
            "error",
            "-y",
            "-i",
            str(aiff_path),
            "-af",
            "apad=whole_dur=30",
            "-t",
            "30",
            "-ar",
            "16000",
            "-ac",
            "1",
            "-c:a",
            "aac",
            str(m4a_path),
        ],
        check=True,
    )
    return m4a_path, 30_000


def parse_worker_asr_ms(worker_log: Path, recording_id: str) -> int | None:
    if not worker_log.exists():
        return None
    pattern = re.compile(
        r"recording_id=" + re.escape(recording_id) + r"\b.*?asr_ms=(\d+)"
    )
    for line in reversed(worker_log.read_text(encoding="utf-8", errors="ignore").splitlines()):
        match = pattern.search(line)
        if match:
            return int(match.group(1))
    return None


def main() -> None:
    root_dir = Path(__file__).resolve().parent.parent.parent
    output_dir = root_dir / "output"
    output_dir.mkdir(parents=True, exist_ok=True)

    api_base = os.environ.get("API_BASE_URL") or f"http://{os.environ.get('API_ADDR', '127.0.0.1:8082')}"
    if not api_base.startswith("http"):
        api_base = f"http://{api_base}"
    token = os.environ.get("API_BEARER_TOKEN")
    if not token:
        raise SystemExit("缺少环境变量 API_BEARER_TOKEN")

    worker_log = output_dir / "p0-baseline-worker.log"
    upload_jsonl = output_dir / "p0-upload-baseline.jsonl"
    e2e_jsonl = output_dir / "p0-e2e-baseline.jsonl"

    # 1. 上传 P95 基线：1.2 MB (1,258,291 bytes)，N=30
    target_upload_bytes = 1_258_291
    upload_samples = 30
    upload_latencies_ms: list[float] = []
    upload_errors = 0

    with tempfile.TemporaryDirectory() as tmp:
        work_dir = Path(tmp)
        m4a_30s_path, duration_ms = build_30s_speech_m4a(work_dir)
        base_m4a_bytes = m4a_30s_path.read_bytes()
        if len(base_m4a_bytes) < target_upload_bytes:
            payload_1_2mb = base_m4a_bytes + b"\x00" * (target_upload_bytes - len(base_m4a_bytes))
        else:
            payload_1_2mb = base_m4a_bytes[:target_upload_bytes]

        with upload_jsonl.open("w", encoding="utf-8") as f_up:
            for i in range(1, upload_samples + 1):
                _, created = http_json(
                    f"{api_base}/v1/recordings",
                    "POST",
                    token,
                    {
                        "duration_ms": 300000,
                        "size_bytes": len(payload_1_2mb),
                        "content_type": "audio/mp4",
                    },
                )
                upload_url = created["upload_url"]
                rec_id = created["recording_id"]
                t0 = time.perf_counter()
                try:
                    status_code = http_put_bytes(upload_url, payload_1_2mb)
                    elapsed_ms = (time.perf_counter() - t0) * 1000.0
                    if 200 <= status_code < 300:
                        upload_latencies_ms.append(elapsed_ms)
                        record = {
                            "sample": i,
                            "recording_id": rec_id,
                            "size_bytes": len(payload_1_2mb),
                            "put_ms": round(elapsed_ms, 3),
                            "status": status_code,
                        }
                    else:
                        upload_errors += 1
                        record = {"sample": i, "recording_id": rec_id, "error_status": status_code}
                except Exception as exc:
                    upload_errors += 1
                    record = {"sample": i, "recording_id": rec_id, "error": type(exc).__name__}
                f_up.write(json.dumps(record, ensure_ascii=False) + "\n")

        # 2. 端到端松手到文本 & ASR RTF 基线：30s 真实语音，N=10
        e2e_samples = 10
        e2e_exact_ms_list: list[float] = []
        e2e_client_2s_ms_list: list[float] = []
        asr_ms_list: list[float] = []
        rtf_std_list: list[float] = []
        speedup_list: list[float] = []
        e2e_errors = 0

        with e2e_jsonl.open("w", encoding="utf-8") as f_e2e:
            for i in range(1, e2e_samples + 1):
                t_start = time.perf_counter()
                t_create0 = time.perf_counter()
                _, created = http_json(
                    f"{api_base}/v1/recordings",
                    "POST",
                    token,
                    {
                        "duration_ms": duration_ms,
                        "size_bytes": len(base_m4a_bytes),
                        "content_type": "audio/mp4",
                    },
                )
                create_ms = (time.perf_counter() - t_create0) * 1000.0
                rec_id = created["recording_id"]
                upload_url = created["upload_url"]

                t_put0 = time.perf_counter()
                http_put_bytes(upload_url, base_m4a_bytes)
                put_ms = (time.perf_counter() - t_put0) * 1000.0

                t_comp0 = time.perf_counter()
                http_json(f"{api_base}/v1/recordings/{rec_id}/complete", "POST", token, {})
                complete_ms = (time.perf_counter() - t_comp0) * 1000.0

                t_poll0 = time.perf_counter()
                status_val = "uploaded"
                transcript_len = 0
                while (time.perf_counter() - t_start) < 60.0:
                    _, state = http_json(f"{api_base}/v1/recordings/{rec_id}", "GET", token)
                    status_val = state.get("status", "")
                    if status_val == "done":
                        transcript_len = len(state.get("transcript") or "")
                        break
                    if status_val == "failed":
                        break
                    time.sleep(0.05)

                total_exact_ms = (time.perf_counter() - t_start) * 1000.0
                poll_wait_ms = (time.perf_counter() - t_poll0) * 1000.0
                # 模拟客户端 2s 轮询周期下的感知总耗时
                polls_2s = math.ceil(poll_wait_ms / 2000.0)
                client_2s_total_ms = create_ms + put_ms + complete_ms + (polls_2s * 2000.0)

                time.sleep(0.1)
                worker_asr_ms = parse_worker_asr_ms(worker_log, rec_id)
                if status_val == "done" and worker_asr_ms and worker_asr_ms > 0:
                    e2e_exact_ms_list.append(total_exact_ms)
                    e2e_client_2s_ms_list.append(client_2s_total_ms)
                    asr_ms_list.append(float(worker_asr_ms))
                    rtf_std = worker_asr_ms / float(duration_ms)
                    speedup = float(duration_ms) / worker_asr_ms
                    rtf_std_list.append(rtf_std)
                    speedup_list.append(speedup)
                    record = {
                        "sample": i,
                        "recording_id": rec_id,
                        "audio_duration_ms": duration_ms,
                        "audio_size_bytes": len(base_m4a_bytes),
                        "create_ms": round(create_ms, 2),
                        "put_ms": round(put_ms, 2),
                        "complete_ms": round(complete_ms, 2),
                        "worker_asr_ms": worker_asr_ms,
                        "e2e_exact_ms": round(total_exact_ms, 2),
                        "e2e_client_poll_2s_ms": round(client_2s_total_ms, 2),
                        "rtf_processing_over_audio": round(rtf_std, 5),
                        "speedup_audio_over_processing": round(speedup, 2),
                        "transcript_chars": transcript_len,
                        "status": "done",
                    }
                else:
                    e2e_errors += 1
                    record = {
                        "sample": i,
                        "recording_id": rec_id,
                        "status": status_val,
                        "worker_asr_ms": worker_asr_ms,
                    }
                f_e2e.write(json.dumps(record, ensure_ascii=False) + "\n")

    up_sorted = sorted(upload_latencies_ms)
    e2e_sorted = sorted(e2e_exact_ms_list)
    e2e_2s_sorted = sorted(e2e_client_2s_ms_list)
    asr_sorted = sorted(asr_ms_list)
    rtf_sorted = sorted(rtf_std_list)
    spd_sorted = sorted(speedup_list)

    summary = {
        "upload_1_2mb": {
            "samples": upload_samples,
            "errors": upload_errors,
            "p50_ms": round(percentile(up_sorted, 0.50), 2),
            "p95_ms": round(percentile(up_sorted, 0.95), 2),
            "p99_ms": round(percentile(up_sorted, 0.99), 2),
        },
        "asr_30s": {
            "samples": e2e_samples,
            "errors": e2e_errors,
            "asr_ms_p50": round(percentile(asr_sorted, 0.50), 2),
            "asr_ms_p95": round(percentile(asr_sorted, 0.95), 2),
            "asr_ms_p99": round(percentile(asr_sorted, 0.99), 2),
            "rtf_std_p50": round(percentile(rtf_sorted, 0.50), 5),
            "rtf_std_p95": round(percentile(rtf_sorted, 0.95), 5),
            "rtf_std_p99": round(percentile(rtf_sorted, 0.99), 5),
            "audio_over_asr_p50": round(percentile(spd_sorted, 0.50), 2),
            "audio_over_asr_p95": round(percentile(spd_sorted, 0.95), 2),
        },
        "stop_to_text_30s": {
            "samples": e2e_samples,
            "errors": e2e_errors,
            "exact_p50_ms": round(percentile(e2e_sorted, 0.50), 2),
            "exact_p95_ms": round(percentile(e2e_sorted, 0.95), 2),
            "exact_p99_ms": round(percentile(e2e_sorted, 0.99), 2),
            "client_2s_poll_p50_ms": round(percentile(e2e_2s_sorted, 0.50), 2),
            "client_2s_poll_p95_ms": round(percentile(e2e_2s_sorted, 0.95), 2),
            "client_2s_poll_p99_ms": round(percentile(e2e_2s_sorted, 0.99), 2),
        },
    }
    print(json.dumps(summary, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
