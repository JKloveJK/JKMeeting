package rag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jiankailei/jkmeeting/server/internal/llm"
)

type Chatter interface {
	ChatJSON(context.Context, []llm.Message, json.RawMessage, func([]byte) error) (llm.Result, error)
}

type searcher interface {
	Search(context.Context, []float32, int) ([]Hit, error)
}

// AnswerMaxTokens 限制问答输出长度；正常答案远短于此，触顶即视为生成失控。
const AnswerMaxTokens = 512

// ErrEmbedding 包装问题向量化阶段的失败，便于 HTTP 层区分依赖故障。
var ErrEmbedding = errors.New("问题向量化失败")

type Citation struct {
	Hit
	Number int
}

type Answer struct {
	Text             string
	Insufficient     bool
	Citations        []Citation
	Retrieved        []Hit
	InvalidCitations int
	Usage            llm.Usage
	LLMCalled        bool
	RetrieveMS       int64
	Elapsed          time.Duration
}

type Answerer struct {
	embedder Embedder
	search   searcher
	chat     Chatter
}

func NewAnswerer(embedder Embedder, search searcher, chat Chatter) *Answerer {
	return &Answerer{embedder: embedder, search: search, chat: chat}
}

// maxItems 让约束解码在语法层面截断引用数组：实测模型曾无限重复 "1, 1, 1"，直到 max_tokens。
var answerSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["answer", "citations", "insufficient"],
  "properties": {
    "answer": {"type": "string"},
    "citations": {"type": "array", "items": {"type": "integer"}, "maxItems": 10},
    "insufficient": {"type": "boolean"}
  }
}`)

const answerPrompt = `你是会议知识库问答助手。只能依据用户给出的会议片段回答，不得使用片段以外的知识或猜测。
要求：
1. 用简体中文简洁作答，保留人名、数字、日期等原文细节。
2. citations 列出支撑答案的片段编号（方括号中的数字），只能使用给出的编号。
3. 如果片段不足以回答问题，insufficient 设为 true，answer 写“资料不足”，citations 为空数组。`

type answerOutput struct {
	Answer       string `json:"answer"`
	Citations    []int  `json:"citations"`
	Insufficient bool   `json:"insufficient"`
}

func parseAnswer(raw []byte) (answerOutput, error) {
	var output struct {
		Answer       *string `json:"answer"`
		Citations    *[]int  `json:"citations"`
		Insufficient *bool   `json:"insufficient"`
	}
	if err := json.Unmarshal(raw, &output); err != nil {
		return answerOutput{}, err
	}
	if output.Answer == nil || output.Citations == nil || output.Insufficient == nil {
		return answerOutput{}, errors.New("缺少必填字段")
	}
	return answerOutput{Answer: strings.TrimSpace(*output.Answer), Citations: *output.Citations, Insufficient: *output.Insufficient}, nil
}

// Ask 检索 top-k 块后让 LLM 仅据此作答；引用只保留本次检索结果中的编号，无合法引用时视为资料不足。
func (answerer *Answerer) Ask(ctx context.Context, question string, k int) (Answer, error) {
	started := time.Now()
	vectors, _, err := answerer.embedder.Embed(ctx, []string{question})
	if err != nil {
		return Answer{}, fmt.Errorf("%w：%v", ErrEmbedding, err)
	}
	hits, err := answerer.search.Search(ctx, vectors[0], k)
	if err != nil {
		return Answer{}, err
	}
	answer := Answer{Retrieved: hits, RetrieveMS: time.Since(started).Milliseconds()}
	if len(hits) == 0 {
		answer.Text, answer.Insufficient, answer.Elapsed = "资料不足", true, time.Since(started)
		return answer, nil
	}

	var excerpts strings.Builder
	for i, hit := range hits {
		fmt.Fprintf(&excerpts, "[%d] （会议 %s，片段 %d）\n%s\n\n", i+1, hit.RecordingID.String()[:8], hit.ChunkIndex, hit.Content)
	}
	messages := []llm.Message{
		{Role: "system", Content: answerPrompt},
		{Role: "user", Content: "会议片段：\n\n" + excerpts.String() + "问题：" + question},
	}
	result, err := answerer.chat.ChatJSON(ctx, messages, answerSchema, func(raw []byte) error {
		_, err := parseAnswer(raw)
		return err
	})
	answer.LLMCalled, answer.Usage = true, result.Usage
	if err != nil {
		answer.Elapsed = time.Since(started)
		return answer, err
	}
	output, _ := parseAnswer(result.Content)
	seen := map[int]bool{}
	for _, number := range output.Citations {
		if number < 1 || number > len(hits) {
			answer.InvalidCitations++
			continue
		}
		if !seen[number] {
			seen[number] = true
			answer.Citations = append(answer.Citations, Citation{Hit: hits[number-1], Number: number})
		}
	}
	if answer.InvalidCitations > 0 {
		slog.Warn("问答引用越界已丢弃", "invalid", answer.InvalidCitations, "retrieved", len(hits))
	}
	answer.Text, answer.Insufficient = output.Answer, output.Insufficient
	if !answer.Insufficient && len(answer.Citations) == 0 {
		answer.Text, answer.Insufficient = "资料不足", true
	}
	if answer.Insufficient {
		answer.Citations = nil
	}
	answer.Elapsed = time.Since(started)
	return answer, nil
}
