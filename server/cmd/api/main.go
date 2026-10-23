package main

import (
	"context"
	"log"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jiankailei/jkmeeting/server/internal/config"
	"github.com/jiankailei/jkmeeting/server/internal/httpapi"
	"github.com/jiankailei/jkmeeting/server/internal/observability"
	"github.com/jiankailei/jkmeeting/server/internal/outbox"
	"github.com/jiankailei/jkmeeting/server/internal/queue"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
	"github.com/jiankailei/jkmeeting/server/internal/storage"
	"github.com/jiankailei/jkmeeting/server/internal/transcode"
)

func main() {
	configValue, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	metrics := observability.NewMetrics()
	poolConfig, err := pgxpool.ParseConfig(configValue.DatabaseURL)
	if err != nil {
		log.Fatal("解析 PostgreSQL 配置失败：", err)
	}
	poolConfig.MaxConns = configValue.DBMaxConns
	poolConfig.MinConns = configValue.DBMinConns
	poolConfig.ConnConfig.Tracer = observability.NewPGXQueryTracer(configValue.SlowQueryThreshold, metrics, slog.Default())

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		log.Fatal("连接 PostgreSQL 失败：", err)
	}
	defer pool.Close()

	metrics.SetPoolStatsProvider(func() observability.PoolStatsSnapshot {
		stat := pool.Stat()
		return observability.PoolStatsSnapshot{
			AcquiredConns:     stat.AcquiredConns(),
			IdleConns:         stat.IdleConns(),
			TotalConns:        stat.TotalConns(),
			MaxConns:          stat.MaxConns(),
			EmptyAcquireCount: stat.EmptyAcquireCount(),
		}
	})

	s3, err := storage.NewS3Client(configValue.S3Endpoint, configValue.S3AccessKey, configValue.S3SecretKey, configValue.S3Bucket)
	if err != nil {
		log.Fatal(err)
	}
	if err := s3.EnsureBucket(ctx); err != nil {
		log.Fatal(err)
	}

	asynqClient := asynq.NewClient(asynq.RedisClientOpt{
		Addr:         configValue.RedisAddr,
		DialTimeout:  500 * time.Millisecond,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
	})
	defer asynqClient.Close()

	outboxRepo := outbox.NewRepository(pool)
	metrics.SetOutboxPendingProvider(func() int64 {
		cnt, err := outboxRepo.CountPending(ctx)
		if err != nil {
			return -1
		}
		return cnt
	})

	dispatcher := queue.NewDispatcher(outboxRepo, asynqClient).WithBatchLimit(configValue.OutboxDispatchBatch)
	dispatcher.StartBackgroundLoop(ctx, 500*time.Millisecond)
	transcodeDispatcher := queue.NewTranscodeDispatcher(outbox.NewTranscodeRepository(pool), asynqClient).
		WithBatchLimit(configValue.OutboxDispatchBatch)
	transcodeDispatcher.StartBackgroundLoop(ctx, 500*time.Millisecond)

	handler := httpapi.NewRecordingAPI(
		recording.NewRepository(pool),
		s3,
		queue.Dispatchers{dispatcher, transcodeDispatcher},
		configValue.APIBearerToken,
	).
		WithHLS(transcode.NewRepository(pool), s3, httpapi.NewHLSSigner(configValue.APIBearerToken)).
		WithMetrics(metrics).
		WithRateLimit(configValue.RateLimitRPS, configValue.RateLimitBurst).
		WithBackpressure(outboxRepo.CountPending, configValue.MaxOutboxBacklog).
		WithGracefulOutboxFallback(true).
		WithHealthCheck(func(checkCtx context.Context) map[string]string {
			res := map[string]string{
				"postgres": "ok",
				"redis":    "ok",
				"s3":       "ok",
			}
			pingCtx, cancelPing := context.WithTimeout(checkCtx, 1500*time.Millisecond)
			defer cancelPing()
			if err := pool.Ping(pingCtx); err != nil {
				res["postgres"] = "down"
			}
			conn, err := net.DialTimeout("tcp", configValue.RedisAddr, 500*time.Millisecond)
			if err != nil {
				res["redis"] = "down"
			} else {
				_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
				_, writeErr := conn.Write([]byte("*1\r\n$4\r\nPING\r\n"))
				buf := make([]byte, 16)
				n, readErr := conn.Read(buf)
				_ = conn.Close()
				if writeErr != nil || readErr != nil || n < 5 || string(buf[:5]) != "+PONG" {
					res["redis"] = "down"
				}
			}
			if err := s3.EnsureBucket(pingCtx); err != nil {
				res["s3"] = "down"
			}
			return res
		})

	server := &http.Server{
		Addr:              valueOrDefault("API_ADDR", ":8080"),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	slog.Info("JKMeeting 录音 API 已启动",
		"addr", server.Addr,
		"db_max_conns", configValue.DBMaxConns,
		"rate_limit_rps", configValue.RateLimitRPS,
		"max_outbox_backlog", configValue.MaxOutboxBacklog,
		"slow_query_threshold_ms", configValue.SlowQueryThreshold.Milliseconds(),
	)
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
