package transcribe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClientParsesInferenceText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/inference" {
			t.Fatalf("path = %q, want /inference", request.URL.Path)
		}
		if err := request.ParseMultipartForm(32 << 20); err != nil {
			t.Fatalf("ParseMultipartForm() error = %v", err)
		}
		if _, _, err := request.FormFile("file"); err != nil {
			t.Fatalf("FormFile(file) error = %v", err)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"text":"测试转写"}`))
	}))
	defer server.Close()

	filePath := filepath.Join(t.TempDir(), "audio.m4a")
	if err := os.WriteFile(filePath, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}

	client := NewClient(server.URL, 5*time.Second, 32<<20)
	text, err := client.Transcribe(context.Background(), filePath, "request-test")
	if err != nil {
		t.Fatalf("Transcribe() error = %v", err)
	}
	if text != "测试转写" {
		t.Fatalf("text = %q, want 测试转写", text)
	}
}
