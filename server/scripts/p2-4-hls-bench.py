#!/usr/bin/env python3
"""P2-4 HLS 转码实验：转码 RTF，以及 FFmpeg 并发负载对 Whisper 转写延迟的影响。

FFmpeg 参数与 server/internal/transcode/ffmpeg.go 的 FFmpeg.Args 保持一致（三档 AAC-LC、fMP4、6 秒分片）。
ASR 直接请求 whisper-server /inference，避免 asynq 转写队列串行排队把排队时间混入推理延迟。
原始数据逐行写入 JSONL，汇总打印到 stdout。
"""
import argparse
import json
import os
import platform
import shutil
import statistics
import subprocess
import tempfile
import threading
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
LADDER = [32, 64, 96]


def hls_args(input_path):
    args = ["-hide_banner", "-nostdin", "-v", "error", "-y", "-i", str(input_path)]
    for _ in LADDER:
        args += ["-map", "0:a"]
    args += ["-c:a", "aac", "-ac", "1", "-ar", "48000"]
    for index, kbps in enumerate(LADDER):
        args += [f"-b:a:{index}", f"{kbps}k"]
    stream_map = " ".join(f"a:{i},name:{kbps}k" for i, kbps in enumerate(LADDER))
    return args + [
        "-f", "hls", "-hls_time", "6", "-hls_playlist_type", "vod",
        "-hls_segment_type", "fmp4", "-hls_fmp4_init_filename", "init.mp4",
        "-hls_segment_filename", "%v/seg_%03d.m4s", "-master_pl_name", "master.m3u8",
        "-var_stream_map", stream_map, "%v/index.m3u8",
    ]


def run_transcode(input_path, nice):
    out_dir = tempfile.mkdtemp(prefix="jk-hls-bench-")
    command = ["ffmpeg"] + hls_args(input_path)
    if nice > 0:
        command = ["nice", "-n", str(nice)] + command
    started = time.perf_counter()
    subprocess.run(command, cwd=out_dir, check=True)
    elapsed_ms = (time.perf_counter() - started) * 1000
    shutil.rmtree(out_dir, ignore_errors=True)
    return elapsed_ms


def make_audio(work, speech_seconds_target):
    aiff = work / "speech.aiff"
    subprocess.run(["say", "-o", str(aiff),
                    "This is a test meeting transcription. We are measuring how transcoding load "
                    "affects speech recognition latency on the same machine."], check=True)
    clip_wav = work / "clip.wav"
    subprocess.run(["ffmpeg", "-v", "error", "-y", "-i", str(aiff), "-ac", "1", "-ar", "16000", str(clip_wav)], check=True)
    encoder = ROOT / "core/build/ogg_opus_encode"
    subprocess.run(["cmake", "--build", str(ROOT / "core/build"), "--target", "ogg_opus_encode"],
                   check=True, stdout=subprocess.DEVNULL)
    long_inputs = {}
    for seconds in speech_seconds_target:
        pcm = work / f"long_{seconds}.pcm"
        subprocess.run(["ffmpeg", "-v", "error", "-y", "-stream_loop", "-1", "-i", str(aiff), "-t", str(seconds),
                        "-ac", "1", "-ar", "16000", "-f", "s16le", str(pcm)], check=True)
        ogg = work / f"long_{seconds}.ogg"
        with open(pcm, "rb") as source, open(ogg, "wb") as sink:
            subprocess.run([str(encoder), "--rate", "16000", "--channels", "1", "--bitrate", "24000"],
                           stdin=source, stdout=sink, stderr=subprocess.DEVNULL, check=True)
        long_inputs[seconds] = ogg
    return clip_wav, long_inputs


def asr_once(asr_base_url, clip_wav):
    output = subprocess.run(
        ["curl", "-fsS", "-o", "/dev/null", "-w", "%{time_total}", f"{asr_base_url}/inference",
         "-F", f"file=@{clip_wav}", "-F", "response_format=json", "-F", "language=auto"],
        check=True, capture_output=True, text=True).stdout
    return float(output) * 1000


def percentile(values, q):
    ordered = sorted(values)
    index = min(len(ordered) - 1, max(0, round(q * (len(ordered) - 1))))
    return ordered[index]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", default=str(ROOT / "output/p2-4-hls-bench.jsonl"))
    parser.add_argument("--asr-base-url", default=os.environ.get("ASR_BASE_URL", "http://127.0.0.1:8081"))
    parser.add_argument("--asr-runs", type=int, default=15)
    parser.add_argument("--rtf-runs", type=int, default=3)
    args = parser.parse_args()

    work = Path(tempfile.mkdtemp(prefix="jk-p2-4-bench-"))
    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    records = []

    def emit(record):
        record["load_avg_1m"] = round(os.getloadavg()[0], 2)
        records.append(record)
        with open(output, "a") as sink:
            sink.write(json.dumps(record, ensure_ascii=False) + "\n")

    emit({"kind": "env", "machine": platform.machine(), "os": platform.platform(), "cpu_count": os.cpu_count(),
          "ffmpeg": subprocess.run(["ffmpeg", "-version"], capture_output=True, text=True).stdout.splitlines()[0]})
    clip_wav, long_inputs = make_audio(work, [60, 600])

    for seconds, path in long_inputs.items():
        for run in range(args.rtf_runs):
            elapsed = run_transcode(path, nice=10)
            emit({"kind": "transcode_rtf", "audio_s": seconds, "run": run, "nice": 10,
                  "transcode_ms": round(elapsed, 1), "rtf": round(elapsed / 1000 / seconds, 4)})

    scenarios = [("idle", 0, 0), ("ffmpeg_x1_nice10", 1, 10), ("ffmpeg_x1_nice0", 1, 0), ("ffmpeg_x3_nice0", 3, 0)]
    asr_once(args.asr_base_url, clip_wav)  # 预热模型
    for name, workers, nice in scenarios:
        stop = threading.Event()
        transcodes = []

        def load_loop():
            while not stop.is_set():
                transcodes.append(run_transcode(long_inputs[600], nice))

        threads = [threading.Thread(target=load_loop, daemon=True) for _ in range(workers)]
        for thread in threads:
            thread.start()
        if workers:
            time.sleep(1.0)
        for run in range(args.asr_runs):
            emit({"kind": "asr_latency", "scenario": name, "ffmpeg_workers": workers, "nice": nice, "run": run,
                  "asr_ms": round(asr_once(args.asr_base_url, clip_wav), 1)})
        stop.set()
        for thread in threads:
            thread.join()

    print("== 转码 RTF（nice 10，三档 AAC） ==")
    for seconds in long_inputs:
        rtfs = [r["rtf"] for r in records if r["kind"] == "transcode_rtf" and r["audio_s"] == seconds]
        print(f"audio={seconds}s runs={len(rtfs)} rtf_median={statistics.median(rtfs):.4f} rtf_max={max(rtfs):.4f}")
    print("== Whisper 推理延迟 ==")
    baseline = None
    for name, _, _ in scenarios:
        values = [r["asr_ms"] for r in records if r["kind"] == "asr_latency" and r["scenario"] == name]
        p50, p95 = percentile(values, 0.5), percentile(values, 0.95)
        baseline = baseline or p95
        print(f"{name:18s} n={len(values)} p50={p50:.0f}ms p95={p95:.0f}ms p95_vs_idle={p95 / baseline:.2f}x")
    print(f"原始数据：{output}")
    shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    main()
