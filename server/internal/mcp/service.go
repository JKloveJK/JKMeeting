package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jiankailei/jkmeeting/server/internal/insights"
	"github.com/jiankailei/jkmeeting/server/internal/rag"
)

// Embedder 与 rag.Embedder 保持一致（含耗时返回），避免 MCP 层反向依赖具体客户端类型。
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, int, error)
}

type businessService struct {
	pool         *pgxpool.Pool
	ragRepo      *rag.Repository
	insightsRepo *insights.Repository
	embedder     Embedder
	answerer     *rag.Answerer
}

func NewService(pool *pgxpool.Pool, embedder Embedder, answerer *rag.Answerer) Service {
	return &businessService{
		pool:         pool,
		ragRepo:      rag.NewRepository(pool),
		insightsRepo: insights.NewRepository(pool),
		embedder:     embedder,
		answerer:     answerer,
	}
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func (service *businessService) ListRecordings(ctx context.Context, limit int) (any, error) {
	limit = clamp(limit, 1, 50)
	rows, err := service.pool.Query(ctx, `
		SELECT id, status, duration_ms, created_at FROM recordings
		ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type row struct {
		ID         string `json:"id"`
		Status     string `json:"status"`
		DurationMS int    `json:"duration_ms"`
		CreatedAt  string `json:"created_at"`
	}
	var result []row
	for rows.Next() {
		var item row
		var createdAt time.Time
		if err := rows.Scan(&item.ID, &item.Status, &item.DurationMS, &createdAt); err != nil {
			return nil, err
		}
		item.CreatedAt = createdAt.Format(time.RFC3339)
		result = append(result, item)
	}
	if result == nil {
		result = []row{}
	}
	return map[string]any{"recordings": result}, rows.Err()
}

func parseID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, fmt.Errorf("recording_id 不是合法 UUID：%s", raw)
	}
	return id, nil
}

func (service *businessService) GetTranscript(ctx context.Context, recordingID string) (any, error) {
	id, err := parseID(recordingID)
	if err != nil {
		return nil, err
	}
	var status string
	var transcript *string
	err = service.pool.QueryRow(ctx, `SELECT status, transcript FROM recordings WHERE id = $1`, id).Scan(&status, &transcript)
	if err != nil {
		return nil, fmt.Errorf("录音不存在或查询失败：%w", err)
	}
	text := ""
	if transcript != nil {
		text = *transcript
	}
	return map[string]any{"recording_id": id.String(), "status": status, "transcript": text}, nil
}

func (service *businessService) GetInsights(ctx context.Context, recordingID string) (any, error) {
	id, err := parseID(recordingID)
	if err != nil {
		return nil, err
	}
	record, err := service.insightsRepo.FindInsights(ctx, id)
	if err != nil {
		// 没有纪要记录与“录音不存在”都以状态 none 表达，与 HTTP 接口语义保持一致。
		return map[string]any{"recording_id": id.String(), "status": "none"}, nil
	}
	result := map[string]any{"recording_id": id.String(), "status": string(record.Status)}
	if record.Status == insights.StatusReady && len(record.Result) > 0 {
		result["result"] = record.Result
	}
	if record.ErrorMessage != nil {
		result["error_message"] = *record.ErrorMessage
	}
	return result, nil
}

func (service *businessService) SearchMeetings(ctx context.Context, query string, topK int) (any, error) {
	topK = clamp(topK, 1, 10)
	vectors, _, err := service.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("问题向量化失败：%w", err)
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("问题向量化返回 %d 个向量，期望 1", len(vectors))
	}
	hits, err := service.ragRepo.Search(ctx, vectors[0], topK)
	if err != nil {
		return nil, err
	}
	return map[string]any{"hits": hits}, nil
}

func (service *businessService) AskMeetings(ctx context.Context, question string, topK int) (any, error) {
	topK = clamp(topK, 1, 10)
	answer, err := service.answerer.Ask(ctx, question, topK)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"answer":       answer.Text,
		"insufficient": answer.Insufficient,
		"citations":    answer.Citations,
		"retrieved":    answer.Retrieved,
	}, nil
}
