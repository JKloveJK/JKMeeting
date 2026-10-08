#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
for migration in "$ROOT_DIR"/server/migrations/*.sql; do
  docker exec -i jkmeeting-postgres-1 psql -v ON_ERROR_STOP=1 -U jkmeeting -d jkmeeting < "$migration"
done
