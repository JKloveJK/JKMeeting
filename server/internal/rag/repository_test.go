package rag

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
)

func unitVector(axis int) []float32 {
	vector := make([]float32, 1024)
	vector[axis] = 1
	return vector
}

func TestReplaceChunksIsIdempotentAndSearchOrdersByDistance(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL 未设置")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	recordingID := uuid.New()
	if err := recording.NewRepository(pool).Create(ctx, recording.Recording{ID: recordingID, UserID: "p0-local-user",
		ObjectKey: "recordings/test/rag.m4a", DurationMS: 1000, SizeBytes: 4, ContentType: "audio/mp4"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM recordings WHERE id = $1`, recordingID) })

	repository := NewRepository(pool)
	chunks := []Chunk{{Index: 0, Content: "rag-test-axis-1000"}, {Index: 1, Content: "rag-test-axis-1001"}}
	vectors := [][]float32{unitVector(1000), unitVector(1001)}
	for i := 0; i < 2; i++ {
		if err := repository.ReplaceChunks(ctx, recordingID, chunks, vectors); err != nil {
			t.Fatalf("ReplaceChunks() #%d error = %v", i+1, err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM transcript_chunks WHERE recording_id = $1`, recordingID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("重复执行后块数 = %d err = %v，期望 2", count, err)
	}

	query := unitVector(1001)
	query[1000] = 0.5
	hits, err := repository.Search(ctx, query, 50)
	if err != nil {
		t.Fatal(err)
	}
	var mine []Hit
	for _, hit := range hits {
		if hit.RecordingID == recordingID {
			mine = append(mine, hit)
		}
	}
	if len(mine) != 2 || mine[0].ChunkIndex != 1 || mine[0].Distance >= mine[1].Distance {
		t.Fatalf("hits = %+v", mine)
	}
}
