#!/usr/bin/env python3
"""P3-3 MCP stdio 冒烟：initialize -> tools/list -> tools/call(list_recordings)。

需要 DATABASE_URL（make 会从 .env 注入）；embedding/LLM 只在检索/问答工具时才需要。
stdout 只打印结论，协议原文在失败时附带，便于定位是服务端还是客户端问题。
"""
import json
import subprocess
import sys


def send(proc, payload):
    proc.stdin.write(json.dumps(payload) + "\n")
    proc.stdin.flush()


def recv(proc):
    line = proc.stdout.readline()
    if not line:
        raise SystemExit("MCP 服务提前退出（stderr 见上方日志）")
    return json.loads(line)


def main() -> int:
    proc = subprocess.Popen(
        ["go", "run", "./cmd/mcp"],
        cwd="server",
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        text=True,
    )
    try:
        send(proc, {"jsonrpc": "2.0", "id": 1, "method": "initialize",
                    "params": {"protocolVersion": "2024-11-05",
                               "capabilities": {}, "clientInfo": {"name": "jkmeeting-smoke", "version": "0"}}})
        init = recv(proc)
        assert init["result"]["serverInfo"]["name"] == "jkmeeting-mcp", init
        send(proc, {"jsonrpc": "2.0", "method": "notifications/initialized"})
        send(proc, {"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
        tools = recv(proc)["result"]["tools"]
        names = [tool["name"] for tool in tools]
        expected = {"list_recordings", "get_transcript", "get_insights", "search_meetings", "ask_meetings"}
        assert expected.issubset(set(names)), names
        send(proc, {"jsonrpc": "2.0", "id": 3, "method": "tools/call",
                    "params": {"name": "list_recordings", "arguments": {"limit": 3}}})
        call = recv(proc)["result"]
        assert call.get("isError") is not True, call
        print(f"MCP 冒烟通过：工具 {names}；list_recordings 返回 {call['content'][0]['text'][:120]}")
        return 0
    finally:
        proc.kill()


if __name__ == "__main__":
    sys.exit(main())
