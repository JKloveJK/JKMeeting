package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL          string
	RedisAddr            string
	S3Endpoint           string
	S3AccessKey          string
	S3SecretKey          string
	S3Bucket             string
	APIBearerToken       string
	UserID               string
	ASRBaseURL           string
	ASRTimeout           time.Duration
	DBMaxConns           int32
	DBMinConns           int32
	DBAcquireTimeout     time.Duration
	SlowQueryThreshold   time.Duration
	RateLimitRPS         float64
	RateLimitBurst       int
	MaxOutboxBacklog     int64
	OutboxDispatchBatch  int
	FFmpegPath           string
	TranscodeConcurrency int
	TranscodeNice        int
	TranscodeBitrates    string
	LLMBaseURL           string
	LLMModel             string
	LLMTimeout           time.Duration
	LLMMaxRetries        int
	LLMConcurrency       int
	LLMContextTokens     int
	EmbeddingBaseURL     string
	EmbeddingDim         int
	RAGTopK              int
}

func Load() (Config, error) {
	config := Config{
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		RedisAddr:           os.Getenv("REDIS_ADDR"),
		S3Endpoint:          os.Getenv("S3_ENDPOINT"),
		S3AccessKey:         os.Getenv("S3_ACCESS_KEY"),
		S3SecretKey:         os.Getenv("S3_SECRET_KEY"),
		S3Bucket:            valueOrDefault("S3_BUCKET", "recordings"),
		APIBearerToken:      os.Getenv("API_BEARER_TOKEN"),
		UserID:              "p0-local-user",
		ASRBaseURL:          valueOrDefault("ASR_BASE_URL", "http://127.0.0.1:8081"),
		ASRTimeout:          120 * time.Second,
		DBMaxConns:          int32(intOrDefault("DB_MAX_CONNS", 25)),
		DBMinConns:          int32(intOrDefault("DB_MIN_CONNS", 2)),
		DBAcquireTimeout:    time.Duration(intOrDefault("DB_ACQUIRE_TIMEOUT_MS", 3000)) * time.Millisecond,
		SlowQueryThreshold:  time.Duration(intOrDefault("SLOW_QUERY_THRESHOLD_MS", 50)) * time.Millisecond,
		RateLimitRPS:        floatOrDefault("RATE_LIMIT_RPS", 2000),
		RateLimitBurst:      intOrDefault("RATE_LIMIT_BURST", 2000),
		MaxOutboxBacklog:    int64(intOrDefault("MAX_OUTBOX_BACKLOG", 50000)),
		OutboxDispatchBatch: intOrDefault("OUTBOX_BATCH_SIZE", 64),
		// 转码与转写分队列消费；转码并发上限与 nice 值用于防止 FFmpeg 挤占 Whisper 的 CPU。
		FFmpegPath:           valueOrDefault("FFMPEG_PATH", "ffmpeg"),
		TranscodeConcurrency: intOrDefault("TRANSCODE_CONCURRENCY", 1),
		TranscodeNice:        intOrDefault("TRANSCODE_NICE", 10),
		TranscodeBitrates:    valueOrDefault("TRANSCODE_BITRATES_KBPS", "32,64,96"),
		// LLM_CONTEXT_TOKENS 必须与 llama-server 的 -c 一致，否则长转写会被服务端截断或拒绝。
		LLMBaseURL:       valueOrDefault("LLM_BASE_URL", "http://127.0.0.1:8083"),
		LLMModel:         valueOrDefault("LLM_MODEL", "qwen2.5-7b-instruct-q4_k_m"),
		LLMTimeout:       time.Duration(intOrDefault("LLM_TIMEOUT_SECONDS", 90)) * time.Second,
		LLMMaxRetries:    nonNegativeIntOrDefault("LLM_MAX_RETRIES", 2),
		LLMConcurrency:   intOrDefault("LLM_CONCURRENCY", 1),
		LLMContextTokens: intOrDefault("LLM_CONTEXT_TOKENS", 8192),
		// EMBEDDING_DIM 必须与迁移 0005 中 vector(1024) 的列维度一致。
		EmbeddingBaseURL: valueOrDefault("EMBEDDING_BASE_URL", "http://127.0.0.1:8084"),
		EmbeddingDim:     intOrDefault("EMBEDDING_DIM", 1024),
		RAGTopK:          intOrDefault("RAG_TOP_K", 5),
	}

	for name, value := range map[string]string{
		"DATABASE_URL":     config.DatabaseURL,
		"REDIS_ADDR":       config.RedisAddr,
		"S3_ENDPOINT":      config.S3Endpoint,
		"S3_ACCESS_KEY":    config.S3AccessKey,
		"S3_SECRET_KEY":    config.S3SecretKey,
		"API_BEARER_TOKEN": config.APIBearerToken,
	} {
		if strings.TrimSpace(value) == "" {
			return Config{}, fmt.Errorf("缺少必填配置：%s", name)
		}
	}
	return config, nil
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func intOrDefault(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func nonNegativeIntOrDefault(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func floatOrDefault(name string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
