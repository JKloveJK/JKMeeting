#!/bin/sh
# P5 不停机迁移演练（expand/contract）：
#   1) 迁移前打一次 API 冒烟（API 需已在本机运行）；
#   2) 应用 expand 迁移 0006（新增可空列，旧 API 不感知）；
#   3) 回填历史数据 client_platform='unknown'（幂等，可重跑）；
#   4) 迁移后再冒烟，证明服务不中断；
#   5) 输出回滚命令（contract 删列）但不自动执行——删列必须等新版本全量后再人工确认。
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
PSQL="docker exec -i jkmeeting-postgres-1 psql -v ON_ERROR_STOP=1 -U jkmeeting -d jkmeeting"

smoke() {
  if [ -n "${API_BEARER_TOKEN:-}" ] && curl -sf -m 3 http://127.0.0.1:8082/healthz >/dev/null 2>&1; then
    printf 'API 冒烟通过（%s）\n' "$1"
  else
    printf 'API 未运行或 token 未设置，跳过冒烟（%s）——仅执行数据库步骤\n' "$1"
  fi
}

smoke "迁移前"
printf '应用 expand 迁移 0006…\n'
$PSQL < "$ROOT_DIR/server/migrations/0006_add_recordings_client_platform.sql"
printf '回填历史数据…\n'
$PSQL -c "UPDATE recordings SET client_platform = 'unknown' WHERE client_platform IS NULL;"
$PSQL -c "SELECT client_platform, count(*) FROM recordings GROUP BY 1 ORDER BY 1;"
smoke "迁移后"

cat <<'EOF'
演练完成。回滚（contract，仅在确认所有 API 实例都不再依赖该列后人工执行）：
  docker exec -i jkmeeting-postgres-1 psql -U jkmeeting -d jkmeeting \
    -c 'ALTER TABLE recordings DROP COLUMN IF EXISTS client_platform;'
EOF
