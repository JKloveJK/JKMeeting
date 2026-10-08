package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL    string
	RedisAddr      string
	S3Endpoint     string
	S3AccessKey    string
	S3SecretKey    string
	S3Bucket       string
	APIBearerToken string
	UserID         string
	ASRBaseURL     string
	ASRTimeout     time.Duration
}

func Load() (Config, error) {
	config := Config{
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		RedisAddr:      os.Getenv("REDIS_ADDR"),
		S3Endpoint:     os.Getenv("S3_ENDPOINT"),
		S3AccessKey:    os.Getenv("S3_ACCESS_KEY"),
		S3SecretKey:    os.Getenv("S3_SECRET_KEY"),
		S3Bucket:       valueOrDefault("S3_BUCKET", "recordings"),
		APIBearerToken: os.Getenv("API_BEARER_TOKEN"),
		UserID:         "p0-local-user",
		ASRBaseURL:     valueOrDefault("ASR_BASE_URL", "http://127.0.0.1:8081"),
		ASRTimeout:     120 * time.Second,
	}

	for name, value := range map[string]string{
		"DATABASE_URL":    config.DatabaseURL,
		"REDIS_ADDR":      config.RedisAddr,
		"S3_ENDPOINT":     config.S3Endpoint,
		"S3_ACCESS_KEY":   config.S3AccessKey,
		"S3_SECRET_KEY":   config.S3SecretKey,
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
