package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jiankailei/jkmeeting/server/internal/config"
	"github.com/jiankailei/jkmeeting/server/internal/httpapi"
	"github.com/jiankailei/jkmeeting/server/internal/outbox"
	"github.com/jiankailei/jkmeeting/server/internal/queue"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
	"github.com/jiankailei/jkmeeting/server/internal/storage"
)

func main() {
	configValue, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, configValue.DatabaseURL)
	if err != nil {
		log.Fatal("连接 PostgreSQL 失败：", err)
	}
	defer pool.Close()

	s3, err := storage.NewS3Client(configValue.S3Endpoint, configValue.S3AccessKey, configValue.S3SecretKey, configValue.S3Bucket)
	if err != nil {
		log.Fatal(err)
	}
	if err := s3.EnsureBucket(ctx); err != nil {
		log.Fatal(err)
	}

	asynqClient := asynq.NewClient(asynq.RedisClientOpt{Addr: configValue.RedisAddr})
	defer asynqClient.Close()

	handler := httpapi.NewRecordingAPI(
		recording.NewRepository(pool),
		s3,
		queue.NewDispatcher(outbox.NewRepository(pool), asynqClient),
		configValue.APIBearerToken,
	)

	server := &http.Server{
		Addr:              valueOrDefault("API_ADDR", ":8080"),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	slog.Info("P0 录音 API 已启动", "addr", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal("API 服务退出：", err)
	}
}

func valueOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
