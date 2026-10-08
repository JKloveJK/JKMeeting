package storage

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPresignedURLsUseConfiguredS3Endpoint(t *testing.T) {
	endpoint := os.Getenv("S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("S3_ENDPOINT 未设置")
	}

	client, err := NewS3Client(endpoint, os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY"), "recordings")
	if err != nil {
		t.Fatalf("NewS3Client() error = %v", err)
	}
	ctx := context.Background()
	if err := client.EnsureBucket(ctx); err != nil {
		t.Fatalf("EnsureBucket() error = %v", err)
	}

	putURL, err := client.PresignedPut(ctx, "recordings/test/audio.m4a", "audio/mp4", time.Minute)
	if err != nil {
		t.Fatalf("PresignedPut() error = %v", err)
	}
	parsed, err := url.Parse(putURL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsed.Host != "127.0.0.1:9000" {
		t.Fatalf("host = %q, want 127.0.0.1:9000", parsed.Host)
	}
	if !strings.Contains(parsed.Path, "/recordings/") {
		t.Fatalf("path = %q, want recordings bucket", parsed.Path)
	}
}
