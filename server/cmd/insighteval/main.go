// insighteval 对 P3-1 会议纪要抽取做离线评测。
//
// text 模式直接把评测集文字稿送入 Extractor（与 Worker 同一实现）；
// api 模式读取 "case_id recording_id" 映射，从 /v1/recordings/{id}/insights 取全链路结果评分。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/jiankailei/jkmeeting/server/internal/insights"
	"github.com/jiankailei/jkmeeting/server/internal/llm"
)

type evalCase struct {
	ID         string             `json:"id"`
	Title      string             `json:"title"`
	Transcript string             `json:"transcript"`
	Reference  insights.Reference `json:"reference"`
}

type runRecord struct {
	Mode             string             `json:"mode"`
	CaseID           string             `json:"case_id"`
	Run              int                `json:"run"`
	RecordingID      string             `json:"recording_id,omitempty"`
	OK               bool               `json:"ok"`
	Error            string             `json:"error,omitempty"`
	FinalFormatError bool               `json:"final_format_error"`
	LatencyMS        int64              `json:"latency_ms"`
	LLMCalls         int                `json:"llm_calls"`
	LLMAttempts      int                `json:"llm_attempts"`
	FormatFailures   int                `json:"format_failures"`
	PromptTokens     int                `json:"prompt_tokens"`
	CompletionTokens int                `json:"completion_tokens"`
	Chunks           int                `json:"chunks"`
	Score            insights.Score     `json:"score"`
	Insights         *insights.Insights `json:"insights,omitempty"`
}

type summary struct {
	Mode                string              `json:"mode"`
	Model               string              `json:"model"`
	Runs                int                 `json:"runs"`
	Errors              int                 `json:"errors"`
	FinalFormatErrors   int                 `json:"final_format_errors"`
	RawFormatFailures   int                 `json:"raw_format_failures"`
	KeyPoints           int                 `json:"key_points"`
	Matched             int                 `json:"matched"`
	Recall              float64             `json:"recall"`
	CategoryRecall      map[string]float64  `json:"category_recall"`
	LatencyP50MS        int64               `json:"latency_p50_ms"`
	LatencyP95MS        int64               `json:"latency_p95_ms"`
	LatencyMaxMS        int64               `json:"latency_max_ms"`
	AvgPromptTokens     float64             `json:"avg_prompt_tokens"`
	AvgCompletionTokens float64             `json:"avg_completion_tokens"`
	Missed              map[string][]string `json:"missed,omitempty"`
	Host                string              `json:"host"`
	StartedAt           string              `json:"started_at"`
}

func main() {
	casesPath := flag.String("cases", "server/eval/p3/cases.json", "评测集 JSON")
	mode := flag.String("mode", "text", "text 或 api")
	baseURL := flag.String("llm-base-url", envOr("LLM_BASE_URL", "http://127.0.0.1:8083"), "llama-server 地址")
	model := flag.String("model", envOr("LLM_MODEL", "qwen2.5-7b-instruct-q4_k_m"), "模型名（记录用）")
	contextTokens := flag.Int("context-tokens", 8192, "须与 llama-server -c 一致")
	timeout := flag.Duration("timeout", 90*time.Second, "单次 LLM 调用超时")
	maxRetries := flag.Int("max-retries", 2, "LLM 重试次数")
	repeat := flag.Int("repeat", 0, "text 模式总调用次数，0 表示每个用例一次；超过用例数时循环")
	only := flag.String("case", "", "仅运行指定用例 ID（逗号分隔）")
	out := flag.String("out", "", "逐次结果 JSONL 输出路径")
	recordings := flag.String("recordings", "", "api 模式的 case_id recording_id 映射文件")
	apiBaseURL := flag.String("api-base-url", envOr("API_BASE_URL", "http://127.0.0.1:8082"), "api 模式 API 地址")
	flag.Parse()

	cases, err := loadCases(*casesPath, *only)
	if err != nil {
		log.Fatal(err)
	}
	var sink io.Writer = io.Discard
	if *out != "" {
		if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
			log.Fatal(err)
		}
		file, err := os.Create(*out)
		if err != nil {
			log.Fatal(err)
		}
		defer file.Close()
		sink = file
	}
	encoder := json.NewEncoder(sink)
	started := time.Now()

	var records []runRecord
	switch *mode {
	case "text":
		client := llm.NewClient(llm.Config{BaseURL: *baseURL, Model: *model, Timeout: *timeout, MaxRetries: *maxRetries, Concurrency: 1,
			MaxTokens: insights.DefaultOutputReserve})
		extractor := insights.NewExtractor(client, insights.ExtractorConfig{ContextTokens: *contextTokens})
		total := *repeat
		if total <= 0 {
			total = len(cases)
		}
		for run := 0; run < total; run++ {
			record := runText(extractor, cases[run%len(cases)], run+1)
			records = append(records, record)
			_ = encoder.Encode(record)
			progress(record, run+1, total)
		}
	case "api":
		mapping, err := loadMapping(*recordings)
		if err != nil {
			log.Fatal(err)
		}
		token := os.Getenv("API_BEARER_TOKEN")
		run := 0
		for _, testCase := range cases {
			recordingID, ok := mapping[testCase.ID]
			if !ok {
				continue
			}
			run++
			record := runAPI(*apiBaseURL, token, testCase, recordingID, run)
			records = append(records, record)
			_ = encoder.Encode(record)
			progress(record, run, len(mapping))
		}
	default:
		log.Fatalf("未知模式：%s", *mode)
	}

	result := summarize(*mode, *model, records, started)
	output, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(output))
}

func runText(extractor *insights.Extractor, testCase evalCase, run int) runRecord {
	started := time.Now()
	extraction, err := extractor.Extract(context.Background(), testCase.Transcript)
	record := runRecord{
		Mode: "text", CaseID: testCase.ID, Run: run, LatencyMS: time.Since(started).Milliseconds(),
		LLMCalls: extraction.Calls, LLMAttempts: extraction.Attempts, FormatFailures: extraction.FormatFailures,
		PromptTokens: extraction.Usage.PromptTokens, CompletionTokens: extraction.Usage.CompletionTokens, Chunks: extraction.Chunks,
	}
	if err != nil {
		record.Error = err.Error()
		record.FinalFormatError = errors.Is(err, llm.ErrInvalidFormat)
		record.Score = insights.ScoreInsights(insights.Insights{}, testCase.Reference)
		return record
	}
	record.OK = true
	record.Score = insights.ScoreInsights(extraction.Insights, testCase.Reference)
	record.Insights = &extraction.Insights
	return record
}

func runAPI(baseURL, token string, testCase evalCase, recordingID string, run int) runRecord {
	record := runRecord{Mode: "api", CaseID: testCase.ID, Run: run, RecordingID: recordingID}
	request, _ := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/v1/recordings/"+recordingID+"/insights", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		record.Error = err.Error()
		return record
	}
	defer response.Body.Close()
	var body struct {
		Status           string          `json:"status"`
		Result           json.RawMessage `json:"result"`
		PromptTokens     *int            `json:"prompt_tokens"`
		CompletionTokens *int            `json:"completion_tokens"`
		LLMCalls         *int            `json:"llm_calls"`
		AnalysisMS       *int            `json:"analysis_ms"`
		ErrorMessage     *string         `json:"error_message"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&body); err != nil {
		record.Error = err.Error()
		return record
	}
	record.Score = insights.ScoreInsights(insights.Insights{}, testCase.Reference)
	if body.Status != "ready" {
		record.Error = "status=" + body.Status
		if body.ErrorMessage != nil {
			record.Error += " " + *body.ErrorMessage
		}
		return record
	}
	parsed, err := insights.Parse(body.Result)
	if err != nil {
		record.Error = err.Error()
		record.FinalFormatError = true
		return record
	}
	record.OK = true
	record.Insights = &parsed
	record.Score = insights.ScoreInsights(parsed, testCase.Reference)
	record.PromptTokens, record.CompletionTokens = deref(body.PromptTokens), deref(body.CompletionTokens)
	record.LLMCalls, record.LatencyMS = deref(body.LLMCalls), int64(deref(body.AnalysisMS))
	return record
}

func summarize(mode, model string, records []runRecord, started time.Time) summary {
	result := summary{Mode: mode, Model: model, Runs: len(records), CategoryRecall: map[string]float64{},
		Missed: map[string][]string{}, Host: runtime.GOOS + "/" + runtime.GOARCH, StartedAt: started.Format(time.RFC3339)}
	var latencies []int64
	categoryTotal, categoryMatched := map[string]int{}, map[string]int{}
	for _, record := range records {
		if !record.OK {
			result.Errors++
		}
		if record.FinalFormatError {
			result.FinalFormatErrors++
		}
		result.RawFormatFailures += record.FormatFailures
		result.KeyPoints += record.Score.Total()
		result.Matched += record.Score.Matched()
		for name, category := range map[string]insights.CategoryScore{
			"decisions": record.Score.Decisions, "action_items": record.Score.ActionItems, "viewpoints": record.Score.Viewpoints,
		} {
			categoryTotal[name] += category.Total
			categoryMatched[name] += category.Matched
			for _, missed := range category.Missed {
				result.Missed[record.CaseID] = append(result.Missed[record.CaseID], name+"/"+missed)
			}
		}
		if record.OK {
			latencies = append(latencies, record.LatencyMS)
			result.AvgPromptTokens += float64(record.PromptTokens)
			result.AvgCompletionTokens += float64(record.CompletionTokens)
		}
	}
	if result.KeyPoints > 0 {
		result.Recall = round(float64(result.Matched) / float64(result.KeyPoints))
	}
	for name, total := range categoryTotal {
		if total > 0 {
			result.CategoryRecall[name] = round(float64(categoryMatched[name]) / float64(total))
		}
	}
	if count := len(latencies); count > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		result.LatencyP50MS = percentile(latencies, 0.50)
		result.LatencyP95MS = percentile(latencies, 0.95)
		result.LatencyMaxMS = latencies[count-1]
		result.AvgPromptTokens = round(result.AvgPromptTokens / float64(count))
		result.AvgCompletionTokens = round(result.AvgCompletionTokens / float64(count))
	}
	return result
}

// percentile 使用最近秩法：P95 取第 ceil(0.95*n) 个样本，不做插值。
func percentile(sorted []int64, p float64) int64 {
	index := int(math.Ceil(p*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}

func progress(record runRecord, done, total int) {
	status := "ok"
	if !record.OK {
		status = "error: " + record.Error
	}
	fmt.Fprintf(os.Stderr, "[%d/%d] %s latency_ms=%d matched=%d/%d format_failures=%d %s\n",
		done, total, record.CaseID, record.LatencyMS, record.Score.Matched(), record.Score.Total(), record.FormatFailures, status)
}

func loadCases(path, only string) ([]evalCase, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []evalCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		return nil, fmt.Errorf("解析评测集失败：%w", err)
	}
	if only != "" {
		wanted := map[string]bool{}
		for _, id := range strings.Split(only, ",") {
			wanted[strings.TrimSpace(id)] = true
		}
		filtered := cases[:0]
		for _, testCase := range cases {
			if wanted[testCase.ID] {
				filtered = append(filtered, testCase)
			}
		}
		cases = filtered
	}
	if len(cases) == 0 {
		return nil, errors.New("评测集为空")
	}
	return cases, nil
}

func loadMapping(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	mapping := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 {
			mapping[fields[0]] = fields[1]
		}
	}
	return mapping, scanner.Err()
}

func deref(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func round(value float64) float64 { return math.Round(value*10000) / 10000 }

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
