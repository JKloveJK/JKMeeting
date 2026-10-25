// rageval 对 P3-2 检索问答做离线评测：把评测集文字稿以确定性 ID 写入数据库并建索引，
// 再用与 API 相同的 Answerer 逐题提问，统计检索命中、引用合法性、答案关键词与拒答。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jiankailei/jkmeeting/server/internal/embedding"
	"github.com/jiankailei/jkmeeting/server/internal/insights"
	"github.com/jiankailei/jkmeeting/server/internal/llm"
	"github.com/jiankailei/jkmeeting/server/internal/rag"
)

var evalNamespace = uuid.MustParse("6f1c4f7e-3b1a-4d6e-9a51-2f3c0b7d8e10")

type evalCase struct {
	ID         string `json:"id"`
	Transcript string `json:"transcript"`
}

type question struct {
	ID             string   `json:"id"`
	Question       string   `json:"question"`
	ExpectedCase   string   `json:"expected_case"`
	AnswerKeywords []string `json:"answer_keywords"`
}

type record struct {
	ID               string   `json:"id"`
	Question         string   `json:"question"`
	ExpectedCase     string   `json:"expected_case"`
	Answerable       bool     `json:"answerable"`
	OK               bool     `json:"ok"`
	Error            string   `json:"error,omitempty"`
	RetrievedCases   []string `json:"retrieved_cases"`
	RetrievalHit     bool     `json:"retrieval_hit"`
	RetrievalRank    int      `json:"retrieval_rank"`
	CitedCases       []string `json:"cited_cases"`
	CitationHit      bool     `json:"citation_hit"`
	InvalidCitations int      `json:"invalid_citations"`
	Answer           string   `json:"answer"`
	Insufficient     bool     `json:"insufficient"`
	KeywordHit       bool     `json:"keyword_hit"`
	LatencyMS        int64    `json:"latency_ms"`
	RetrieveMS       int64    `json:"retrieve_ms"`
	PromptTokens     int      `json:"prompt_tokens"`
	CompletionTokens int      `json:"completion_tokens"`
}

func main() {
	casesPath := flag.String("cases", "eval/p3/cases.json", "会议文字稿")
	questionsPath := flag.String("questions", "eval/p3/rag-questions.json", "问题集")
	k := flag.Int("k", 5, "检索 top-k")
	out := flag.String("out", "", "逐题 JSONL 输出")
	skipIngest := flag.Bool("skip-ingest", false, "跳过写库与建索引")
	chunkRunes := flag.Int("chunk-runes", rag.ChunkRunes, "切块上限（字），调小可增加块数、提高检索难度")
	flag.Parse()

	ctx := context.Background()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("需要 DATABASE_URL")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	var cases []evalCase
	var questions []question
	mustLoad(*casesPath, &cases)
	mustLoad(*questionsPath, &questions)

	embedder := embedding.NewClient(embedding.Config{BaseURL: envOr("EMBEDDING_BASE_URL", "http://127.0.0.1:8084"), Dim: 1024, MaxRetries: 2})
	chat := llm.NewClient(llm.Config{BaseURL: envOr("LLM_BASE_URL", "http://127.0.0.1:8083"), Model: envOr("LLM_MODEL", "qwen2.5-7b-instruct-q4_k_m"),
		Timeout: 90 * time.Second, MaxRetries: 1, Concurrency: 1, MaxTokens: rag.AnswerMaxTokens})
	repository := rag.NewRepository(pool)

	caseByRecording := map[uuid.UUID]string{}
	var embedTimes []int64
	totalChunks := 0
	for _, testCase := range cases {
		id := uuid.NewSHA1(evalNamespace, []byte(testCase.ID))
		caseByRecording[id] = testCase.ID
		if *skipIngest {
			continue
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO recordings (id, user_id, status, object_key, duration_ms, size_bytes, content_type, transcript)
			VALUES ($1, 'p0-local-user', 'done', $2, 1, 0, 'text/plain', $3)
			ON CONFLICT (id) DO UPDATE SET transcript = EXCLUDED.transcript, updated_at = now()`,
			id, "eval/p3-2/"+testCase.ID, testCase.Transcript); err != nil {
			log.Fatal(err)
		}
		chunks, vectors, elapsed, err := rag.EmbedChunks(ctx, embedder, testCase.Transcript, *chunkRunes)
		if err != nil {
			log.Fatalf("索引 %s 失败：%v", testCase.ID, err)
		}
		if err := repository.ReplaceChunks(ctx, id, chunks, vectors); err != nil {
			log.Fatal(err)
		}
		embedTimes = append(embedTimes, elapsed.Milliseconds())
		totalChunks += len(chunks)
	}
	if !*skipIngest {
		fmt.Fprintf(os.Stderr, "已索引 %d 段会议，%d 个块，单段 embedding 耗时 P50=%dms P95=%dms\n",
			len(cases), totalChunks, percentile(embedTimes, 0.5), percentile(embedTimes, 0.95))
	}

	var sink io.Writer = io.Discard
	if *out != "" {
		_ = os.MkdirAll(filepath.Dir(*out), 0o755)
		file, err := os.Create(*out)
		if err != nil {
			log.Fatal(err)
		}
		defer file.Close()
		sink = file
	}
	encoder := json.NewEncoder(sink)
	answerer := rag.NewAnswerer(embedder, repository, chat)

	var records []record
	for i, item := range questions {
		answer, err := answerer.Ask(ctx, item.Question, *k)
		row := record{ID: item.ID, Question: item.Question, ExpectedCase: item.ExpectedCase, Answerable: item.ExpectedCase != "",
			LatencyMS: answer.Elapsed.Milliseconds(), RetrieveMS: answer.RetrieveMS,
			PromptTokens: answer.Usage.PromptTokens, CompletionTokens: answer.Usage.CompletionTokens}
		if err != nil {
			row.Error = err.Error()
		} else {
			row.OK = true
			row.Answer, row.Insufficient, row.InvalidCitations = answer.Text, answer.Insufficient, answer.InvalidCitations
			for rank, hit := range answer.Retrieved {
				name := caseByRecording[hit.RecordingID]
				if name == "" {
					name = "other:" + hit.RecordingID.String()[:8]
				}
				row.RetrievedCases = append(row.RetrievedCases, name)
				if name == item.ExpectedCase && !row.RetrievalHit {
					row.RetrievalHit, row.RetrievalRank = true, rank+1
				}
			}
			for _, citation := range answer.Citations {
				name := caseByRecording[citation.RecordingID]
				row.CitedCases = append(row.CitedCases, name)
				if name == item.ExpectedCase {
					row.CitationHit = true
				}
			}
			row.KeywordHit = row.Answerable && !row.Insufficient && insights.ContainsKeywords(answer.Text, item.AnswerKeywords)
		}
		records = append(records, row)
		_ = encoder.Encode(row)
		fmt.Fprintf(os.Stderr, "[%d/%d] %s latency_ms=%d retrieval_hit=%v rank=%d keyword_hit=%v insufficient=%v %s\n",
			i+1, len(questions), item.ID, row.LatencyMS, row.RetrievalHit, row.RetrievalRank, row.KeywordHit, row.Insufficient, row.Error)
	}
	result := summarize(records, *k, totalChunks)
	result["chunk_runes"] = *chunkRunes
	output, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(output))
}

func summarize(records []record, k, chunks int) map[string]any {
	var answerable, retrievalHits, citationHits, keywordHits, unanswerable, refused, falseRefusals, invalid, errors int
	var latencies []int64
	var promptTokens, completionTokens int
	for _, row := range records {
		if !row.OK {
			errors++
			continue
		}
		latencies = append(latencies, row.LatencyMS)
		promptTokens += row.PromptTokens
		completionTokens += row.CompletionTokens
		invalid += row.InvalidCitations
		if row.Answerable {
			answerable++
			if row.RetrievalHit {
				retrievalHits++
			}
			if row.CitationHit {
				citationHits++
			}
			if row.KeywordHit {
				keywordHits++
			}
			if row.Insufficient {
				falseRefusals++
			}
		} else {
			unanswerable++
			if row.Insufficient {
				refused++
			}
		}
	}
	ratio := func(a, b int) float64 {
		if b == 0 {
			return 0
		}
		return math.Round(float64(a)/float64(b)*10000) / 10000
	}
	count := len(latencies)
	return map[string]any{
		"questions": len(records), "errors": errors, "top_k": k, "indexed_chunks": chunks,
		"retrieval_hit_rate": ratio(retrievalHits, answerable), "citation_hit_rate": ratio(citationHits, answerable),
		"keyword_hit_rate": ratio(keywordHits, answerable), "false_refusals": falseRefusals,
		"unanswerable": unanswerable, "refused_unanswerable": refused, "model_invalid_citations": invalid,
		"latency_p50_ms": percentile(latencies, 0.5), "latency_p95_ms": percentile(latencies, 0.95),
		"avg_prompt_tokens": ratio(promptTokens, max(count, 1)), "avg_completion_tokens": ratio(completionTokens, max(count, 1)),
	}
}

// percentile 使用最近秩法，不插值。
func percentile(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(math.Ceil(p*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}

func mustLoad(path string, target any) {
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		log.Fatalf("解析 %s 失败：%v", path, err)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
