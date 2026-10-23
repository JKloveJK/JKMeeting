#!/usr/bin/env python3
"""P1 服务端纵深六合一自动化实验脚本。

涵盖：
1. 100 -> 200 -> 500 -> 1000 并发压测曲线
2. pgxpool MaxConns (2, 5, 10, 25, 50, 80) 连接池吞吐拐点扫描
3. 100 万行 recordings 数据注入与 EXPLAIN (ANALYZE, BUFFERS) 慢查询/深分页对比
4. W3C traceparent 贯通、Prometheus /metrics 与 PGX 慢查询自动捕获验证
5. 故障注入（限流 429、Redis 暂停 Outbox 优雅降级与自愈、Postgres 暂停 /healthz 探针）
6. 10 万级队列积压背压保护（503）与有界批量派发防雪崩验证
"""

import json
import os
import re
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path


ROOT_DIR = Path(__file__).resolve().parent.parent.parent
SERVER_DIR = ROOT_DIR / "server"
OUTPUT_DIR = ROOT_DIR / "output"


def run_psql(sql: str) -> str:
    res = subprocess.run(
        [
            "docker",
            "compose",
            "exec",
            "-T",
            "postgres",
            "psql",
            "-U",
            "jkmeeting",
            "-d",
            "jkmeeting",
            "-X",
            "-q",
            "-t",
            "-A",
            "-c",
            sql,
        ],
        cwd=ROOT_DIR,
        capture_output=True,
        text=True,
        check=True,
    )
    return res.stdout.strip()


def http_req(
    url: str,
    method: str = "GET",
    headers: dict[str, str] | None = None,
    body: dict | None = None,
) -> tuple[int, dict[str, str], str]:
    req_headers = dict(headers or {})
    data = None
    if body is not None:
        req_headers["Content-Type"] = "application/json"
        data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(url, data=data, headers=req_headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            resp_headers = {k.lower(): v for k, v in resp.headers.items()}
            return resp.status, resp_headers, resp.read().decode("utf-8")
    except urllib.error.HTTPError as err:
        resp_headers = {k.lower(): v for k, v in err.headers.items()}
        return err.code, resp_headers, err.read().decode("utf-8", errors="ignore")


def wait_for_port(url: str, timeout_s: float = 15.0) -> None:
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        try:
            code, _, _ = http_req(url)
            if code in (200, 503):
                return
        except Exception:
            pass
        time.sleep(0.15)
    raise RuntimeError(f"等待服务就绪超时: {url}")


def parse_metric(prom_text: str, metric_prefix: str) -> float:
    for line in prom_text.splitlines():
        if line.startswith(metric_prefix + " "):
            return float(line.split()[-1])
    return 0.0


def main() -> None:
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    token = os.environ.get("API_BEARER_TOKEN")
    if not token:
        raise SystemExit("缺少环境变量 API_BEARER_TOKEN")

    report: dict = {"date": "2026-10-21", "experiments": {}}

    with tempfile.TemporaryDirectory() as tmp:
        tmp_dir = Path(tmp)
        api_bin = tmp_dir / "jkmeeting-api"
        bench_bin = tmp_dir / "loadbench"

        print("[1/6] 编译 Go API 与 loadbench 压测二进制...")
        subprocess.run(["go", "-C", str(SERVER_DIR), "build", "-o", str(api_bin), "./cmd/api"], check=True)
        subprocess.run(["go", "-C", str(SERVER_DIR), "build", "-o", str(bench_bin), "./cmd/loadbench"], check=True)

        # 服务端 content_type 白名单生效后，经 API 创建的压测行只能使用 audio/mp4，
        # 改为按“本次开始时间 + 压测行特征”精确匹配；压测行从不上传，真实录音不会同时满足这些条件。
        run_started_at = run_psql("SELECT now()::text;")
        bench_rows = (
            f"(created_at >= '{run_started_at}' AND status = 'created' AND duration_ms = 30000 "
            "AND size_bytes = 120000 AND content_type = 'audio/mp4')"
        )
        sql_rows = "content_type LIKE 'audio/mp4-p1%'"  # 直接 SQL 插入的实验行，不经过 API

        # 清理可能残留的压测数据
        run_psql(f"DELETE FROM transcription_outbox; DELETE FROM recordings WHERE {sql_rows};")

        # ------------------------------------------------------------------
        # 实验 1：并发阶梯压测 (100 -> 200 -> 500 -> 1000)
        # ------------------------------------------------------------------
        print("[2/6] 执行实验 1：100 -> 200 -> 500 -> 1000 并发阶梯压测...")
        env = os.environ.copy()
        env.update(
            {
                "API_ADDR": "127.0.0.1:18082",
                "DB_MAX_CONNS": "30",
                "RATE_LIMIT_RPS": "100000",
                "RATE_LIMIT_BURST": "100000",
                "MAX_OUTBOX_BACKLOG": "500000",
                "SLOW_QUERY_THRESHOLD_MS": "200",
            }
        )
        api_log_file = (OUTPUT_DIR / "p1-exp1-api.log").open("w", encoding="utf-8")
        proc = subprocess.Popen([str(api_bin)], env=env, stdout=api_log_file, stderr=subprocess.STDOUT)
        try:
            wait_for_port("http://127.0.0.1:18082/healthz")
            # 先创建一条基准记录用于 GET 压测，并验证 traceparent 透传
            trace_in = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
            status_code, resp_headers, resp_body = http_req(
                "http://127.0.0.1:18082/v1/recordings",
                method="POST",
                headers={
                    "Authorization": f"Bearer {token}",
                    "traceparent": trace_in,
                    "X-Request-ID": "p1-trace-verify-001",
                },
                body={"duration_ms": 30000, "size_bytes": 120000, "content_type": "audio/mp4"},
            )
            created_data = json.loads(resp_body)
            sample_id = created_data["recording_id"]

            report["experiments"]["observability_trace"] = {
                "status_code": status_code,
                "sent_traceparent": trace_in,
                "returned_traceparent": resp_headers.get("traceparent", ""),
                "returned_request_id": resp_headers.get("x-request-id", ""),
                "trace_id_preserved": resp_headers.get("traceparent", "").startswith(
                    "00-4bf92f3577b34da6a3ce929d0e0e4736-"
                ),
            }

            concurrency_steps = [100, 200, 500, 1000]
            get_curve = []
            post_curve = []
            for c in concurrency_steps:
                total_n = max(2000, c * 4)
                res_get = subprocess.run(
                    [
                        str(bench_bin),
                        "-url",
                        f"http://127.0.0.1:18082/v1/recordings/{sample_id}",
                        "-method",
                        "GET",
                        "-c",
                        str(c),
                        "-n",
                        str(total_n),
                    ],
                    env=env,
                    capture_output=True,
                    text=True,
                    check=True,
                )
                get_curve.append(json.loads(res_get.stdout))

                res_post = subprocess.run(
                    [
                        str(bench_bin),
                        "-url",
                        "http://127.0.0.1:18082/v1/recordings",
                        "-method",
                        "POST",
                        "-body",
                        '{"duration_ms":30000,"size_bytes":120000,"content_type":"audio/mp4"}',
                        "-c",
                        str(c),
                        "-n",
                        str(total_n),
                    ],
                    env=env,
                    capture_output=True,
                    text=True,
                    check=True,
                )
                post_curve.append(json.loads(res_post.stdout))

            report["experiments"]["concurrency_curve"] = {
                "get_recording": get_curve,
                "post_recording": post_curve,
            }
        finally:
            proc.terminate()
            proc.wait(timeout=5)
            api_log_file.close()

        run_psql(f"DELETE FROM recordings WHERE {sql_rows} OR {bench_rows};")

        # ------------------------------------------------------------------
        # 实验 2：pgxpool 连接池 MaxConns 扫描 (2, 5, 10, 25, 50, 80)
        # ------------------------------------------------------------------
        print("[3/6] 执行实验 2：pgxpool MaxConns (2 -> 5 -> 10 -> 25 -> 50 -> 80) 吞吐拐点扫描...")
        pool_curve = []
        for max_conns in [2, 5, 10, 25, 50, 80]:
            env_pool = os.environ.copy()
            env_pool.update(
                {
                    "API_ADDR": "127.0.0.1:18082",
                    "DB_MAX_CONNS": str(max_conns),
                    "DB_MIN_CONNS": str(min(2, max_conns)),
                    "RATE_LIMIT_RPS": "100000",
                    "RATE_LIMIT_BURST": "100000",
                    "SLOW_QUERY_THRESHOLD_MS": "1",
                }
            )
            p_pool = subprocess.Popen([str(api_bin)], env=env_pool, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                wait_for_port("http://127.0.0.1:18082/healthz")
                res = subprocess.run(
                    [
                        str(bench_bin),
                        "-url",
                        "http://127.0.0.1:18082/v1/recordings",
                        "-method",
                        "POST",
                        "-body",
                        '{"duration_ms":30000,"size_bytes":120000,"content_type":"audio/mp4"}',
                        "-c",
                        "200",
                        "-n",
                        "2000",
                    ],
                    env=env_pool,
                    capture_output=True,
                    text=True,
                    check=True,
                )
                bench_data = json.loads(res.stdout)
                _, _, prom_text = http_req("http://127.0.0.1:18082/metrics")
                bench_data["max_conns"] = max_conns
                bench_data["empty_acquire_total"] = int(parse_metric(prom_text, "jkmeeting_db_pool_empty_acquire_total"))
                bench_data["slow_queries_total"] = int(parse_metric(prom_text, "jkmeeting_db_slow_queries_total"))
                pool_curve.append(bench_data)
            finally:
                p_pool.terminate()
                p_pool.wait(timeout=5)
            run_psql(f"DELETE FROM recordings WHERE {sql_rows} OR {bench_rows};")

        report["experiments"]["pgxpool_scan"] = pool_curve

        # ------------------------------------------------------------------
        # 实验 3 & 4：100 万行数据量注入 + EXPLAIN (ANALYZE, BUFFERS) 对比
        # ------------------------------------------------------------------
        print("[4/6] 执行实验 3 & 4：注入 100 万条 recordings 数据并对比 EXPLAIN (ANALYZE, BUFFERS)...")
        t_insert0 = time.perf_counter()
        run_psql(
            """
            INSERT INTO recordings (id, user_id, status, object_key, duration_ms, size_bytes, content_type, created_at, updated_at)
            SELECT
                md5(gs::text || '-p1-1m')::uuid,
                CASE WHEN gs % 10 = 0 THEN 'p0-hot-user' ELSE 'bench-user-' || (gs % 1000)::text END,
                'done',
                'recordings/bench/' || gs::text || '.m4a',
                (gs % 300) * 1000 + 1000,
                120000,
                'audio/mp4-p1-1m',
                now() - (gs || ' seconds')::interval,
                now() - (gs || ' seconds')::interval
            FROM generate_series(1, 1000000) AS gs;
            ANALYZE recordings;
            """
        )
        insert_1m_seconds = round(time.perf_counter() - t_insert0, 2)
        total_rows = int(run_psql("SELECT count(*) FROM recordings;"))

        # 1) 全表扫描（无索引列过滤）
        explain_seq_scan = run_psql(
            "EXPLAIN (ANALYZE, BUFFERS) SELECT id, status, duration_ms, created_at FROM recordings WHERE duration_ms = 150000 ORDER BY created_at DESC LIMIT 20;"
        )
        # 2) 复合索引 (user_id, created_at DESC) 首页查询
        explain_index_first_page = run_psql(
            "EXPLAIN (ANALYZE, BUFFERS) SELECT id, status, duration_ms, created_at FROM recordings WHERE user_id = 'p0-hot-user' ORDER BY created_at DESC LIMIT 20;"
        )
        # 3) 复合索引下深分页 OFFSET 50000 退化
        explain_deep_offset = run_psql(
            "EXPLAIN (ANALYZE, BUFFERS) SELECT id, status, duration_ms, created_at FROM recordings WHERE user_id = 'p0-hot-user' ORDER BY created_at DESC OFFSET 50000 LIMIT 20;"
        )
        # 4) 游标分页（Keyset Pagination）消除深分页退化
        cursor_ts = run_psql(
            "SELECT created_at FROM recordings WHERE user_id = 'p0-hot-user' ORDER BY created_at DESC OFFSET 50000 LIMIT 1;"
        )
        explain_keyset_pagination = run_psql(
            f"EXPLAIN (ANALYZE, BUFFERS) SELECT id, status, duration_ms, created_at FROM recordings WHERE user_id = 'p0-hot-user' AND created_at < '{cursor_ts}'::timestamptz ORDER BY created_at DESC LIMIT 20;"
        )

        def extract_exec_time_ms(plan: str) -> float:
            m = re.search(r"Execution Time:\s*([0-9.]+)\s*ms", plan)
            return float(m.group(1)) if m else -1.0

        report["experiments"]["million_rows_explain"] = {
            "total_rows": total_rows,
            "insert_1m_seconds": insert_1m_seconds,
            "seq_scan_unindexed": {
                "execution_time_ms": extract_exec_time_ms(explain_seq_scan),
                "plan": explain_seq_scan.splitlines(),
            },
            "index_scan_first_page": {
                "execution_time_ms": extract_exec_time_ms(explain_index_first_page),
                "plan": explain_index_first_page.splitlines(),
            },
            "deep_offset_50000": {
                "execution_time_ms": extract_exec_time_ms(explain_deep_offset),
                "plan": explain_deep_offset.splitlines(),
            },
            "keyset_cursor_at_50000": {
                "execution_time_ms": extract_exec_time_ms(explain_keyset_pagination),
                "plan": explain_keyset_pagination.splitlines(),
            },
        }

        # 清理 100 万行压测数据并 VACUUM（VACUUM 不能在多语句隐式事务块中运行，需单独调用）
        run_psql("DELETE FROM recordings WHERE content_type = 'audio/mp4-p1-1m';")
        run_psql("VACUUM ANALYZE recordings;")

        # ------------------------------------------------------------------
        # 实验 5：故障注入与限流韧性（429 限流、Redis 暂停优雅降级与恢复、PG 暂停探针）
        # ------------------------------------------------------------------
        print("[5/6] 执行实验 5：故障注入（限流 429、Redis 暂停 Outbox 优雅降级与自愈、PG 暂停探针）...")
        env_fault = os.environ.copy()
        env_fault.update(
            {
                "API_ADDR": "127.0.0.1:18082",
                "RATE_LIMIT_RPS": "50",
                "RATE_LIMIT_BURST": "25",
                "MAX_OUTBOX_BACKLOG": "5000",
                "OUTBOX_BATCH_SIZE": "64",
            }
        )
        fault_log = (OUTPUT_DIR / "p1-fault-injection-api.log").open("w", encoding="utf-8")
        p_fault = subprocess.Popen([str(api_bin)], env=env_fault, stdout=fault_log, stderr=subprocess.STDOUT)
        try:
            wait_for_port("http://127.0.0.1:18082/healthz")

            # 5a) 触发令牌桶限流 (Burst=25, 瞬间打 100 个并发请求)
            res_rl = subprocess.run(
                [
                    str(bench_bin),
                    "-url",
                    "http://127.0.0.1:18082/v1/recordings",
                    "-method",
                    "POST",
                    "-body",
                    '{"duration_ms":30000,"size_bytes":120000,"content_type":"audio/mp4"}',
                    "-c",
                    "50",
                    "-n",
                    "100",
                ],
                env=env_fault,
                capture_output=True,
                text=True,
                check=True,
            )
            rl_result = json.loads(res_rl.stdout)

            time.sleep(1.0)  # 等待令牌桶恢复

            # 5b) Redis 故障注入：先创建并 PUT 音频对象，再 pause Redis，验证 complete 优雅降级返回 200 OK
            _, _, body_c = http_req(
                "http://127.0.0.1:18082/v1/recordings",
                method="POST",
                headers={"Authorization": f"Bearer {token}"},
                body={"duration_ms": 1000, "size_bytes": 4, "content_type": "audio/mp4"},
            )
            rec_info = json.loads(body_c)
            rec_id = rec_info["recording_id"]
            put_req = urllib.request.Request(
                rec_info["upload_url"],
                data=b"\x00\x01\x02\x03",
                headers={"Content-Type": "audio/mp4"},
                method="PUT",
            )
            with urllib.request.urlopen(put_req, timeout=10) as r_put:
                r_put.read()

            subprocess.run(["docker", "compose", "pause", "redis"], cwd=ROOT_DIR, check=True)
            try:
                hz_redis_down_code, _, hz_redis_down_body = http_req("http://127.0.0.1:18082/healthz")
                comp_code, _, comp_body = http_req(
                    f"http://127.0.0.1:18082/v1/recordings/{rec_id}/complete",
                    method="POST",
                    headers={"Authorization": f"Bearer {token}"},
                    body={},
                )
                outbox_while_redis_down = run_psql(
                    f"SELECT status FROM transcription_outbox WHERE recording_id = '{rec_id}';"
                )
            finally:
                subprocess.run(["docker", "compose", "unpause", "redis"], cwd=ROOT_DIR, check=True)

            # 等待后台 loop 自动自愈派发
            outbox_after_redis_up = outbox_while_redis_down
            for _ in range(20):
                outbox_after_redis_up = run_psql(
                    f"SELECT status FROM transcription_outbox WHERE recording_id = '{rec_id}';"
                )
                if outbox_after_redis_up == "dispatched":
                    break
                time.sleep(0.25)
            hz_redis_up_code, _, hz_redis_up_body = http_req("http://127.0.0.1:18082/healthz")

            # 5c) PostgreSQL 故障注入：pause Postgres -> 检查 /healthz 503 -> unpause -> 自愈 200
            subprocess.run(["docker", "compose", "pause", "postgres"], cwd=ROOT_DIR, check=True)
            try:
                hz_pg_down_code, _, hz_pg_down_body = http_req("http://127.0.0.1:18082/healthz")
            finally:
                subprocess.run(["docker", "compose", "unpause", "postgres"], cwd=ROOT_DIR, check=True)
            time.sleep(0.5)
            hz_pg_up_code, _, hz_pg_up_body = http_req("http://127.0.0.1:18082/healthz")

            # ------------------------------------------------------------------
            # 实验 6：10 万级队列堆积与背压保护 (MAX_OUTBOX_BACKLOG=5000)
            # ------------------------------------------------------------------
            print("[6/6] 执行实验 6：注入 10 万条 Outbox 积压任务，验证 503 背压保护与分批消化...")
            run_psql(
                """
                INSERT INTO recordings (id, user_id, status, object_key, duration_ms, size_bytes, content_type)
                SELECT
                    md5(gs::text || '-backlog')::uuid,
                    'p0-local-user',
                    'uploaded',
                    'recordings/backlog/' || gs::text || '.m4a',
                    1000,
                    4,
                    'audio/mp4-p1-backlog'
                FROM generate_series(1, 100000) AS gs;

                INSERT INTO transcription_outbox (id, recording_id, task_id, request_id, status)
                SELECT
                    md5(gs::text || '-outbox')::uuid,
                    md5(gs::text || '-backlog')::uuid,
                    'transcription:backlog-' || gs::text,
                    'req-backlog-' || gs::text,
                    'pending'
                FROM generate_series(1, 100000) AS gs;
                """
            )
            backlog_before = int(run_psql("SELECT count(*) FROM transcription_outbox WHERE status IN ('pending', 'dispatching');"))
            bp_code, _, bp_body = http_req(
                "http://127.0.0.1:18082/v1/recordings",
                method="POST",
                headers={"Authorization": f"Bearer {token}"},
                body={"duration_ms": 1000, "size_bytes": 4, "content_type": "audio/mp4"},
            )
            time.sleep(1.5)
            backlog_after_ticks = int(run_psql("SELECT count(*) FROM transcription_outbox WHERE status IN ('pending', 'dispatching');"))
            dispatched_by_ticks = 100000 - backlog_after_ticks
            _, _, prom_final = http_req("http://127.0.0.1:18082/metrics")

            report["experiments"]["fault_injection_and_resilience"] = {
                "rate_limit_burst_test": rl_result,
                "redis_outage_graceful_degradation": {
                    "healthz_during_outage_code": hz_redis_down_code,
                    "healthz_during_outage": json.loads(hz_redis_down_body),
                    "complete_status_during_redis_outage": comp_code,
                    "outbox_status_during_redis_outage": outbox_while_redis_down,
                    "outbox_status_after_redis_recovery": outbox_after_redis_up,
                    "healthz_after_recovery_code": hz_redis_up_code,
                    "healthz_after_recovery": json.loads(hz_redis_up_body),
                },
                "postgres_outage_probe": {
                    "healthz_during_outage_code": hz_pg_down_code,
                    "healthz_during_outage": json.loads(hz_pg_down_body),
                    "healthz_after_recovery_code": hz_pg_up_code,
                    "healthz_after_recovery": json.loads(hz_pg_up_body),
                },
            }

            report["experiments"]["queue_backlog_100k_and_backpressure"] = {
                "injected_outbox_events": 100000,
                "configured_max_backlog_threshold": 5000,
                "backlog_observed": backlog_before,
                "create_recording_status_under_backlog": bp_code,
                "create_recording_response_under_backlog": json.loads(bp_body),
                "dispatched_smoothly_in_1_5s_batches": dispatched_by_ticks,
                "metrics_backpressure_rejected_total": int(
                    parse_metric(prom_final, "jkmeeting_backpressure_rejected_total")
                ),
                "metrics_rate_limited_total": int(
                    parse_metric(prom_final, "jkmeeting_rate_limited_total")
                ),
            }
        finally:
            p_fault.terminate()
            p_fault.wait(timeout=5)
            fault_log.close()
            # 清理 10 万条积压测试数据与 Redis 队列中的测试任务
            run_psql(
                "DELETE FROM transcription_outbox WHERE task_id LIKE 'transcription:backlog-%'; "
                f"DELETE FROM recordings WHERE {sql_rows} OR {bench_rows};"
            )
            run_psql("VACUUM ANALYZE transcription_outbox;")
            run_psql("VACUUM ANALYZE recordings;")
            subprocess.run(
                ["docker", "compose", "exec", "-T", "redis", "redis-cli", "FLUSHALL"],
                cwd=ROOT_DIR,
                stdout=subprocess.DEVNULL,
                check=False,
            )

    out_path = OUTPUT_DIR / "p1-experiments-report.json"
    out_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"P1 全部 6 项实验完成，原始结果已写入: {out_path}")


if __name__ == "__main__":
    main()
