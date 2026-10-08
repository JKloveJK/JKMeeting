package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Client struct {
	baseURL  string
	http     *http.Client
	maxBytes int64
}

type inferenceResponse struct {
	Text string `json:"text"`
}

func NewClient(baseURL string, timeout time.Duration, maxBytes int64) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: timeout}, maxBytes: maxBytes}
}

func (client *Client) Transcribe(ctx context.Context, filePath, requestID string) (string, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return "", fmt.Errorf("读取待转写文件失败：%w", err)
	}
	if info.Size() > client.maxBytes {
		return "", fmt.Errorf("待转写文件超过大小上限")
	}

	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("打开待转写文件失败：%w", err)
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", fmt.Errorf("写入转写请求失败：%w", err)
	}
	_ = writer.WriteField("response_format", "json")
	_ = writer.WriteField("language", "auto")
	if err := writer.Close(); err != nil {
		return "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/inference", &body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-Request-ID", requestID)

	response, err := client.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("调用 ASR 服务失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("ASR 服务返回状态 %d", response.StatusCode)
	}

	var payload inferenceResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return "", fmt.Errorf("解析 ASR 响应失败：%w", err)
	}
	if strings.TrimSpace(payload.Text) == "" {
		return "", fmt.Errorf("ASR 服务返回空文本")
	}
	return payload.Text, nil
}
