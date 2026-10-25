package main

import (
	"context"
	"log"
	"log/slog"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jiankailei/jkmeeting/server/internal/config"
	"github.com/jiankailei/jkmeeting/server/internal/embedding"
	"github.com/jiankailei/jkmeeting/server/internal/insights"
	"github.com/jiankailei/jkmeeting/server/internal/llm"
	"github.com/jiankailei/jkmeeting/server/internal/queue"
	"github.com/jiankailei/jkmeeting/server/internal/rag"
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

	llmClient := llm.NewClient(llm.Config{
		BaseURL: configValue.LLMBaseURL, Model: configValue.LLMModel, Timeout: configValue.LLMTimeout,
		MaxRetries: configValue.LLMMaxRetries, Concurrency: configValue.LLMConcurrency,
		MaxTokens: insights.DefaultOutputReserve,
	})
	extractor := insights.NewExtractor(llmClient, insights.ExtractorConfig{ContextTokens: configValue.LLMContextTokens})
	analyzer := insights.NewHandler(insights.NewRepository(pool), recordings, extractor, llmClient.Model())
	analysisServer := asynq.NewServer(redis, asynq.Config{
		Concurrency: configValue.LLMConcurrency,
		Queues:      map[string]int{queue.QueueAnalysis: 1},
	})
	analysisMux := asynq.NewServeMux()
	analysisMux.HandleFunc(queue.TypeAnalysis, analyzer.ProcessTask)
	if err := analysisServer.Start(analysisMux); err != nil {
		log.Fatal(err)
	}
	defer analysisServer.Shutdown()
	slog.Info("纪要 Worker 已启动", "llm_base_url", configValue.LLMBaseURL, "model", configValue.LLMModel,
		"concurrency", configValue.LLMConcurrency, "context_tokens", configValue.LLMContextTokens)

	embedder := embedding.NewClient(embedding.Config{BaseURL: configValue.EmbeddingBaseURL, Dim: configValue.EmbeddingDim,
		MaxRetries: 2})
	indexer := rag.NewIndexer(rag.NewRepository(pool), recordings, embedder)
	indexServer := asynq.NewServer(redis, asynq.Config{Concurrency: 1, Queues: map[string]int{queue.QueueIndex: 1}})
	indexMux := asynq.NewServeMux()
	indexMux.HandleFunc(queue.TypeIndex, indexer.ProcessTask)
	if err := indexServer.Start(indexMux); err != nil {
		log.Fatal(err)
	}
	defer indexServer.Shutdown()
	slog.Info("索引 Worker 已启动", "embedding_base_url", configValue.EmbeddingBaseURL, "dim", configValue.EmbeddingDim)

	asr := transcribe.NewClient(configValue.ASRBaseURL, configValue.ASRTimeout, 32<<20)
	handler := transcribe.NewHandler(recordings, s3, asr)
	server := asynq.NewServer(redis, asynq.Config{Concurrency: 1})
	mux := asynq.NewServeMux()
	mux.HandleFunc(queue.TypeTranscription, handler.ProcessTask)
	if err := server.Run(mux); err != nil {
		log.Print(err)
	}
}
