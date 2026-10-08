package main

import (
	"context"
	"log"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jiankailei/jkmeeting/server/internal/config"
	"github.com/jiankailei/jkmeeting/server/internal/queue"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
	"github.com/jiankailei/jkmeeting/server/internal/storage"
	"github.com/jiankailei/jkmeeting/server/internal/transcribe"
)

func main() {
	configValue, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, configValue.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	s3, err := storage.NewS3Client(configValue.S3Endpoint, configValue.S3AccessKey, configValue.S3SecretKey, configValue.S3Bucket)
	if err != nil {
		log.Fatal(err)
	}
	asr := transcribe.NewClient(configValue.ASRBaseURL, configValue.ASRTimeout, 32<<20)
	handler := transcribe.NewHandler(recording.NewRepository(pool), s3, asr)

	server := asynq.NewServer(asynq.RedisClientOpt{Addr: configValue.RedisAddr}, asynq.Config{Concurrency: 1})
	mux := asynq.NewServeMux()
	mux.HandleFunc(queue.TypeTranscription, handler.ProcessTask)
	log.Fatal(server.Run(mux))
}
