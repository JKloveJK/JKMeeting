package config

import "testing"

func TestLoadRejectsMissingRequiredSettings(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("REDIS_ADDR", "")
	t.Setenv("S3_ENDPOINT", "")
	t.Setenv("S3_ACCESS_KEY", "")
	t.Setenv("S3_SECRET_KEY", "")
	t.Setenv("API_BEARER_TOKEN", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want missing required settings error")
	}
}

func TestLoadReadsLocalSettings(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:fixture@127.0.0.1:5432/test?sslmode=disable")
	t.Setenv("REDIS_ADDR", "127.0.0.1:6379")
	t.Setenv("S3_ENDPOINT", "http://127.0.0.1:9000")
	t.Setenv("S3_ACCESS_KEY", "fixture-access")
	t.Setenv("S3_SECRET_KEY", "fixture-secret")
	t.Setenv("API_BEARER_TOKEN", "fixture-token")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.S3Bucket != "recordings" {
		t.Fatalf("S3Bucket = %q, want recordings", got.S3Bucket)
	}
	if got.UserID != "p0-local-user" {
		t.Fatalf("UserID = %q, want p0-local-user", got.UserID)
	}
}
