// cmd/mcp 是 P3-3 的 MCP Server 入口（stdio 传输）。
// 只依赖 DATABASE_URL 与本机 embedding / LLM 服务；凭据经环境变量注入，不写进仓库。
// stdout 专供 JSON-RPC，日志全部写 stderr。
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jiankailei/jkmeeting/server/internal/embedding"
	"github.com/jiankailei/jkmeeting/server/internal/llm"
	"github.com/jiankailei/jkmeeting/server/internal/mcp"
	"github.com/jiankailei/jkmeeting/server/internal/rag"
)

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		logger.Error("缺少 DATABASE_URL")
		os.Exit(1)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		logger.Error("连接数据库失败", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	embedder := embedding.NewClient(embedding.Config{
		BaseURL: envOr("EMBEDDING_BASE_URL", "http://127.0.0.1:8084"), Dim: 1024, MaxRetries: 2,
	})
	chat := llm.NewClient(llm.Config{
		BaseURL:     envOr("LLM_BASE_URL", "http://127.0.0.1:8083"),
		Model:       envOr("LLM_MODEL", "qwen2.5-7b-instruct-q4_k_m"),
		Timeout:     90 * time.Second,
		MaxRetries:  1,
		Concurrency: 1,
		MaxTokens:   rag.AnswerMaxTokens,
	})
	repository := rag.NewRepository(pool)
	answerer := rag.NewAnswerer(embedder, repository, chat)
	service := mcp.NewService(pool, embedder, answerer)

	logger.Info("jkmeeting-mcp 已启动（stdio）")
	if err := mcp.NewServer(service, logger).Serve(ctx, os.Stdin, os.Stdout); err != nil {
		logger.Error("mcp 服务退出", "error", err)
		os.Exit(1)
	}
}
