package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type BenchResult struct {
	Concurrency int            `json:"concurrency"`
	Requests    int            `json:"requests"`
	DurationMs  float64        `json:"duration_ms"`
	RPS         float64        `json:"rps"`
	P50Ms       float64        `json:"p50_ms"`
	P95Ms       float64        `json:"p95_ms"`
	P99Ms       float64        `json:"p99_ms"`
	StatusCodes map[string]int `json:"status_codes"`
	Errors      int64          `json:"errors"`
	ErrorRate   float64        `json:"error_rate"`
}

func main() {
	urlFlag := flag.String("url", "http://127.0.0.1:8082/v1/recordings", "Target URL")
	methodFlag := flag.String("method", "POST", "HTTP method")
	concurrencyFlag := flag.Int("c", 100, "Concurrency level")
	totalReqFlag := flag.Int("n", 2000, "Total number of requests")
	bodyFlag := flag.String("body", `{"duration_ms":30000,"size_bytes":120000,"content_type":"audio/mp4"}`, "Request JSON body")
	flag.Parse()

	token := os.Getenv("API_BEARER_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "缺少环境变量 API_BEARER_TOKEN")
		os.Exit(1)
	}

	transport := &http.Transport{
		MaxIdleConns:        *concurrencyFlag * 2,
		MaxIdleConnsPerHost: *concurrencyFlag * 2,
		MaxConnsPerHost:     *concurrencyFlag * 2,
		IdleConnTimeout:     30 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	jobs := make(chan int, *totalReqFlag)
	for i := 0; i < *totalReqFlag; i++ {
		jobs <- i
	}
	close(jobs)

	latencies := make([]float64, *totalReqFlag)
	var statusMu sync.Mutex
	statusCodes := make(map[string]int)
	var errCount int64

	payloadBytes := []byte(*bodyFlag)
	started := time.Now()

	var wg sync.WaitGroup
	for w := 0; w < *concurrencyFlag; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				var bodyReader io.Reader
				if *methodFlag == http.MethodPost || *methodFlag == http.MethodPut {
					bodyReader = bytes.NewReader(payloadBytes)
				}
				req, err := http.NewRequest(*methodFlag, *urlFlag, bodyReader)
				if err != nil {
					atomic.AddInt64(&errCount, 1)
					continue
				}
				req.Header.Set("Authorization", "Bearer "+token)
				if bodyReader != nil {
					req.Header.Set("Content-Type", "application/json")
				}

				t0 := time.Now()
				resp, err := client.Do(req)
				elapsedMs := float64(time.Since(t0).Microseconds()) / 1000.0
				latencies[idx] = elapsedMs

				if err != nil {
					atomic.AddInt64(&errCount, 1)
					statusMu.Lock()
					statusCodes["network_error"]++
					statusMu.Unlock()
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()

				codeKey := fmt.Sprintf("%d", resp.StatusCode)
				statusMu.Lock()
				statusCodes[codeKey]++
				statusMu.Unlock()
				if resp.StatusCode >= 500 {
					atomic.AddInt64(&errCount, 1)
				}
			}
		}()
	}
	wg.Wait()

	totalDurationMs := float64(time.Since(started).Microseconds()) / 1000.0
	sort.Float64s(latencies)

	res := BenchResult{
		Concurrency: *concurrencyFlag,
		Requests:    *totalReqFlag,
		DurationMs:  math.Round(totalDurationMs*100) / 100,
		RPS:         math.Round((float64(*totalReqFlag)/(totalDurationMs/1000.0))*100) / 100,
		P50Ms:       math.Round(percentile(latencies, 0.50)*100) / 100,
		P95Ms:       math.Round(percentile(latencies, 0.95)*100) / 100,
		P99Ms:       math.Round(percentile(latencies, 0.99)*100) / 100,
		StatusCodes: statusCodes,
		Errors:      errCount,
		ErrorRate:   math.Round((float64(errCount)/float64(*totalReqFlag))*10000) / 10000,
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res)
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
