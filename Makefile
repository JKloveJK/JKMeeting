SHELL := /bin/bash
.DEFAULT_GOAL := help

-include .env
export POSTGRES_PASSWORD DATABASE_URL REDIS_ADDR S3_ENDPOINT S3_ACCESS_KEY S3_SECRET_KEY API_BEARER_TOKEN API_ADDR ASR_BASE_URL API_BASE_URL

.PHONY: help doctor verify test-core bench-core opus-xcframework test-apple-bridge test-apple-ios test-apple-macos prepare-local-config up-local migrate-server test-server run-server run-server-lan run-worker run-worker-lan run-whisper smoke-server smoke-asr smoke-hls smoke load e2e-local

help: ## 列出可用的项目验证入口
	@printf "JKMeeting verification targets:\n"
	@printf "  make doctor       检查 Harness 文件与当前前置条件\n"
	@printf "  make verify       P0 前置阶段的汇总验证；模块落地后扩展为全量验证\n"
	@printf "  make test-core    运行 C++ core 的 CMake/CTest\n"
	@printf "  make bench-core   以 Release 运行 core 会话与 Opus 编码耗时基准\n"
	@printf "  make opus-xcframework  构建 Apple 用 Opus.xcframework\n"
	@printf "  make test-apple-bridge  运行 macOS ObjC++ 桥接验证\n"
	@printf "  make test-apple-ios   在指定 iOS Simulator 上运行 XCTest\n"
	@printf "  make test-apple-macos 运行 macOS XCTest\n"
	@printf "  make prepare-local-config 生成忽略的本机 S3 配置\n"
	@printf "  make up-local       准备配置并启动本机容器\n"
	@printf "  make migrate-server  应用 PostgreSQL recording migration\n"
	@printf "  make test-server     运行 Go 服务端测试\n"
	@printf "  make run-server      启动本地 Go API\n"
	@printf "  make run-server-lan  从私有环境变量启动局域网 Go API\n"
	@printf "  make run-whisper     启动本机 Whisper HTTP 服务\n"
	@printf "  make run-worker      启动 Redis 转写任务 Worker\n"
	@printf "  make run-worker-lan  从私有环境变量启动局域网 Worker\n"
	@printf "  make smoke-server    运行 API curl 冒烟测试\n"
	@printf "  make smoke-asr       运行异步转写冒烟测试\n"
	@printf "  make smoke-hls       运行 HLS 多码率转码冒烟测试\n"
	@printf "  make load         server/ 落地后运行固定负载基线\n"
	@printf "  make e2e-local    iOS 与本地服务落地后运行端到端闭环\n"

doctor: ## 验证 Harness 与当前已落地模块的前置条件
	@test -f AGENTS.md
	@test -f docs/metrics.md
	@test -f docs/decisions/README.md
	@test -f docs/decisions/0001-ai-harness-and-documentation-governance.md
	@test -f docs/decisions/0002-core-audio-frame-ownership.md
	@test -f core/CMakeLists.txt
	@test -f apple/CMakeLists.txt
	@test -f apple/Bridge/JKMeetingCore.h
	@test -f server/go.mod
	@test -f server/migrations/0001_create_recordings.sql
	@printf "Harness, Apple, and Go service test entries are ready.\n"

verify: doctor test-core test-apple-bridge test-server ## 汇总当前已落地模块的真实验证
	@printf "Verification passed for all currently implemented modules.\n"

test-core: ## 配置、构建并运行 C++ core 的 CTest
	cmake -S core -B core/build
	cmake --build core/build
	ctest --test-dir core/build --output-on-failure

BENCH_CORE_OUTPUT ?= output/p2-3-opus-bench.jsonl

bench-core: ## 以 Release 构建运行 core 会话与 Ogg Opus 编解码基准，结果追加到 $(BENCH_CORE_OUTPUT)
	cmake -S core -B core/build-release -DCMAKE_BUILD_TYPE=Release
	cmake --build core/build-release --target audio_session_bench
	@mkdir -p output
	core/build-release/audio_session_bench | tee -a $(BENCH_CORE_OUTPUT)

test-apple-bridge: ## 配置、构建并运行 macOS ObjC++ 与 C++ core 桥接测试
	cmake -S apple -B apple/cmake-build
	cmake --build apple/cmake-build
	ctest --test-dir apple/cmake-build --output-on-failure

opus-xcframework: ## 构建 Apple 用 Opus.xcframework（版本戳一致时跳过）
	apple/scripts/build-opus-xcframework.sh

test-apple-ios: opus-xcframework ## 在指定 Simulator 上运行 iOS XCTest
	@: "$${IOS_SIMULATOR_ID:?请设置 IOS_SIMULATOR_ID 为 iOS Simulator UUID}"
	xcodegen generate --spec apple/project.yml --project apple
	xcodebuild test -project apple/JKMeeting.xcodeproj -scheme JKMeeting-iOS -destination "platform=iOS Simulator,id=$${IOS_SIMULATOR_ID}" CODE_SIGNING_ALLOWED=NO

test-apple-macos: opus-xcframework ## 运行 macOS XCTest
	xcodegen generate --spec apple/project.yml --project apple
	xcodebuild test -project apple/JKMeeting.xcodeproj -scheme JKMeeting-macOS -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO

prepare-local-config: ## 按私有环境生成本机 SeaweedFS 凭据文件
	@python3 server/scripts/prepare-local-s3.py

up-local: prepare-local-config ## 准备本机配置并启动容器
	@docker compose up -d --no-build

migrate-server: ## 应用 PostgreSQL recording migration
	@server/scripts/migrate.sh

test-server: ## 运行 Go 服务端测试
	@: "$${DATABASE_URL:?请设置 DATABASE_URL}" "$${S3_ENDPOINT:?请设置 S3_ENDPOINT}" "$${S3_ACCESS_KEY:?请设置 S3_ACCESS_KEY}" "$${S3_SECRET_KEY:?请设置 S3_SECRET_KEY}"
	@go -C server test ./...

run-server: ## 从本机环境启动 Go API
	@: "$${DATABASE_URL:?请设置 DATABASE_URL}" "$${REDIS_ADDR:?请设置 REDIS_ADDR}" "$${S3_ENDPOINT:?请设置 S3_ENDPOINT}" "$${S3_ACCESS_KEY:?请设置 S3_ACCESS_KEY}" "$${S3_SECRET_KEY:?请设置 S3_SECRET_KEY}" "$${API_BEARER_TOKEN:?请设置 API_BEARER_TOKEN}" "$${API_ADDR:?请设置 API_ADDR}"
	@go -C server run ./cmd/api

run-server-lan: run-server ## 使用调用方提供的局域网地址启动同一 API

run-whisper: ## 启动本机 Whisper HTTP 服务
	server/scripts/start-whisper.sh

run-worker: ## 从本机环境启动 Redis 转写任务 Worker
	@: "$${DATABASE_URL:?请设置 DATABASE_URL}" "$${REDIS_ADDR:?请设置 REDIS_ADDR}" "$${S3_ENDPOINT:?请设置 S3_ENDPOINT}" "$${S3_ACCESS_KEY:?请设置 S3_ACCESS_KEY}" "$${S3_SECRET_KEY:?请设置 S3_SECRET_KEY}" "$${API_BEARER_TOKEN:?请设置 API_BEARER_TOKEN}"
	@go -C server run ./cmd/worker

run-worker-lan: run-worker ## 使用调用方提供的局域网 S3 地址启动同一 Worker

smoke-server: ## 运行 API curl 冒烟测试
	@: "$${API_BEARER_TOKEN:?请设置 API_BEARER_TOKEN}"
	@server/scripts/smoke.sh

smoke-asr: ## 运行异步转写冒烟测试
	@: "$${API_BEARER_TOKEN:?请设置 API_BEARER_TOKEN}"
	@server/scripts/smoke-asr.sh

smoke-hls: ## 运行 P2-4 HLS 多码率转码冒烟测试（需 API、Worker 与宿主机 FFmpeg）
	@: "$${API_BEARER_TOKEN:?请设置 API_BEARER_TOKEN}"
	@server/scripts/smoke-hls.sh

smoke: smoke-server ## 兼容 P0 API 冒烟入口

load: ## 运行 P1 服务端固定并发压测基线（100 并发 / 2000 请求）
	@: "$${API_BEARER_TOKEN:?请设置 API_BEARER_TOKEN}"
	@go -C server run ./cmd/loadbench -url "$${API_BASE_URL:-http://127.0.0.1:8082}/v1/recordings" -method POST -c 100 -n 2000

p1-experiments: ## 运行 P1 服务端纵深全套加压、连接池、100万数据、故障注入与背压实验
	@: "$${API_BEARER_TOKEN:?请设置 API_BEARER_TOKEN}"
	@python3 server/scripts/p1-experiments.py

e2e-local: ## 预留：iOS、服务端与 ASR 落地后执行本地端到端闭环
	@printf "Unavailable: add the iOS app, local services, and ASR setup before running this target.\n" >&2
	@exit 2
