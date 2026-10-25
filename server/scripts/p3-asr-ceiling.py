#!/usr/bin/env python3
"""P3-1 音频模式误差分解：要点关键词在 ASR 转写里还剩多少（LLM 召回上限）。

用法：python3 server/scripts/p3-asr-ceiling.py output/p3-1-audio-<stamp>.transcripts.jsonl
繁简转换调用 macOS Foundation 的 ICU 变换 Hant-Hans（经 swift 执行），不引入第三方依赖。
"""
import json
import subprocess
import sys
import unicodedata
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CASES = json.loads((ROOT / "server/eval/p3/cases.json").read_text())


def to_simplified(texts):
    script = (
        "import Foundation\n"
        "let input = String(data: FileHandle.standardInput.readDataToEndOfFile(), encoding: .utf8)!\n"
        "print(input.applyingTransform(StringTransform(\"Hant-Hans\"), reverse: false)!, terminator: \"\")\n"
    )
    joined = "\u0001".join(texts)
    out = subprocess.run(["swift", "-e", script], input=joined.encode(), capture_output=True, check=True)
    return out.stdout.decode().split("\u0001")


def normalize(text):
    return "".join(ch.lower() for ch in text if not (ch.isspace() or unicodedata.category(ch).startswith("P")))


def contains_all(text, keywords):
    norm = normalize(text)
    return all(any(alt and normalize(alt) in norm for alt in kw.split("|")) for kw in keywords)


def present(text, point):
    person = point.get("owner") or point.get("speaker") or ""
    return contains_all(text, point["keywords"]) and (not person or normalize(person) in normalize(text))


def main():
    rows = [json.loads(line) for line in Path(sys.argv[1]).read_text().splitlines() if line.strip()]
    simplified = to_simplified([row["transcript"] for row in rows])
    totals = {"points": 0, "reference": 0, "raw": 0, "simplified": 0}
    for row, simple in zip(rows, simplified):
        case = next(c for c in CASES if c["id"] == row["case_id"])
        points = [p for cat in ("decisions", "action_items", "viewpoints") for p in case["reference"][cat]]
        counts = {
            "reference": sum(present(case["transcript"], p) for p in points),
            "raw": sum(present(row["transcript"], p) for p in points),
            "simplified": sum(present(simple, p) for p in points),
        }
        lost = [
            (p.get("owner") or p.get("speaker") or "") + ":" + "+".join(p["keywords"])
            for p in points
            if not present(simple, p)
        ]
        totals["points"] += len(points)
        for key in counts:
            totals[key] += counts[key]
        print(json.dumps({"case_id": row["case_id"], "points": len(points), **counts, "lost_after_simplify": lost},
                         ensure_ascii=False))
    print(json.dumps({"summary": totals}, ensure_ascii=False))


if __name__ == "__main__":
    main()
