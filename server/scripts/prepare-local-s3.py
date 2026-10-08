#!/usr/bin/env python3
"""从本机环境生成不纳入版本控制的 SeaweedFS S3 配置。"""

import json
import os
from pathlib import Path


def main() -> None:
    values = {}
    for name in ("S3_ACCESS_KEY", "S3_SECRET_KEY"):
        value = os.environ.get(name)
        if not value:
            raise SystemExit(f"缺少本地环境变量：{name}")
        values[name] = value

    path = Path(__file__).resolve().parent.parent / "seaweedfs" / "s3.json"
    if path.exists():
        existing = json.loads(path.read_text(encoding="utf-8"))
        identities = existing.get("identities", [])
        credentials = identities[0].get("credentials", []) if identities else []
        current = credentials[0] if credentials else {}
        if current.get("accessKey") != values["S3_ACCESS_KEY"] or current.get("secretKey") != values["S3_SECRET_KEY"]:
            raise SystemExit("本机 s3.json 与环境变量不一致；请自行检查并备份后更新，脚本不会覆盖文件")
        print("本机 S3 配置已就绪（未输出凭据）")
        return

    config = {
        "identities": [{
            "name": "jkmeeting-local",
            "credentials": [{"accessKey": values["S3_ACCESS_KEY"], "secretKey": values["S3_SECRET_KEY"]}],
            "actions": ["Admin", "Read", "Write", "List", "Tagging"],
        }]
    }
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    fd = os.open(path, flags, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as output:
        json.dump(config, output, ensure_ascii=False, indent=2)
        output.write("\n")
    print("已生成仅本机使用的 S3 配置（未输出凭据）")


if __name__ == "__main__":
    main()
