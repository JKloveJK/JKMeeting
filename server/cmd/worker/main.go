package main

import (
	"context"
	"log"
	"log/slog"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jiankailei/jkmeeting/server/internal/config"
	"github.com/jiankailei/jkmeeting/server/internal/queue"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
	"github.com/jiankailei/jkmeeting/server/internal/storage"
	"github.com/jiankailei/jkmeeting/server/internal/transcode"
	"github.com/jiankailei/jkmeeting/server/internal/transcribe"
)

func main() {
	configValue, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ladder, err := transcode.ParseLadder(configValue.TranscodeBitrates)
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
	recordings := recording.NewRepository(pool)
	redis := asynq.RedisClientOpt{Addr: configValue.RedisAddr}

	// asynq 没有按队列的并发上限，因此转码使用独立 Server：并发互不占用，FFmpeg 再以 nice 降低 CPU 优先级。
	ffmpeg := transcode.FFmpeg{Path: configValue.FFmpegPath, Nice: configValue.TranscodeNice, SegmentSeconds: 6}
	transcoder := transcode.NewHandler(transcode.NewRepository(pool), recordings, s3, ffmpeg, ladder)
	transcodeServer := asynq.NewServer(redis, asynq.Config{
		Concurrency: configValue.TranscodeConcurrency,
		Queues:      map[string]int{queue.QueueTranscode: 1},
	})
	transcodeMux := asynq.NewServeMux()
	transcodeMux.HandleFunc(queue.TypeTranscode, transcoder.ProcessTask)
	if err := transcodeServer.Start(transcodeMux); err != nil {
		log.Fatal(err)
	}
	defer transcodeServer.Shutdown()
	slog.Info("转码 Worker 已启动", "concurrency", configValue.TranscodeConcurrency, "nice", configValue.TranscodeNice,
		"bitrates_kbps", transcode.LadderString(ladder))

	asr := transcribe.NewClient(configValue.ASRBaseURL, configValue.ASRTimeout, 32<<20)
	handler := transcribe.NewHandler(recordings, s3, asr)
	server := asynq.NewServer(redis, asynq.Config{Concurrency: 1})
	mux := asynq.NewServeMux()
	mux.HandleFunc(queue.TypeTranscription, handler.ProcessTask)
	if err := server.Run(mux); err != nil {
		log.Print(err)
	}
}
