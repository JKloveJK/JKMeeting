package insights

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jiankailei/jkmeeting/server/internal/llm"
)

type Chatter interface {
	ChatJSON(ctx context.Context, messages []llm.Message, schema json.RawMessage, validate func([]byte) error) (llm.Result, error)
}

// Extraction 汇总一次纪要抽取的结果与开销；分段抽取时 Calls > 1。
type Extraction struct {
	Insights       Insights
	Usage          llm.Usage
	Calls          int
	Attempts       int
	FormatFailures int
	Chunks         int
	Elapsed        time.Duration
}

// DefaultOutputReserve 是为纪要输出预留的 token 数，同时作为 LLM 请求的 max_tokens。
const DefaultOutputReserve = 1536

type ExtractorConfig struct {
	ContextTokens int
	OutputReserve int
}

type Extractor struct {
	chat   Chatter
	config ExtractorConfig
}

func NewExtractor(chat Chatter, config ExtractorConfig) *Extractor {
	if config.ContextTokens <= 0 {
		config.ContextTokens = 8192
	}
	if config.OutputReserve <= 0 {
		config.OutputReserve = DefaultOutputReserve
	}
	return &Extractor{chat: chat, config: config}
}

const systemPrompt = `你是会议纪要助手。只依据用户提供的会议转写，输出符合 JSON Schema 的纪要，不得编造转写中没有的信息。
- summary：3～5 句中文，概括议题与结论。
- decisions：会上明确达成的决定，每条一句；没有就输出空数组。
- action_items：有明确执行动作的任务。owner 填转写中出现的负责人姓名，无法确定时填空字符串；due 填转写中出现的截止时间，没有则填空字符串。
- viewpoints：按发言人列出其主要观点；speaker 用转写中出现的人名，不要使用“发言人1”之类的编号。
- 转写由语音识别生成，可能有错别字，按上下文理解，但不要改写人名。`

const mergePrompt = `你是会议纪要助手。下面是同一场会议按顺序分段抽取的纪要（JSON 数组）。请合并为一份完整纪要：
重新概括 summary；合并并去重 decisions 与 action_items（保留负责人和截止时间）；按发言人合并 viewpoints。不得加入分段纪要之外的信息。`

// EstimateTokens 保守估算：CJK 字符按 1 token，其余字符约 3.5 个 1 token。
func EstimateTokens(text string) int {
	cjk, other := 0, 0
	for _, r := range text {
		if unicode.Is(unicode.Han, r) || unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Hangul) || unicode.IsPunct(r) && r > 0x2000 {
			cjk++
		} else {
			other++
		}
	}
	return cjk + (other*2+6)/7
}

func (extractor *Extractor) transcriptBudget() int {
	return extractor.config.ContextTokens - extractor.config.OutputReserve - EstimateTokens(systemPrompt) - 64
}

// SplitTranscript 按句子边界切分，使每段估算 token 不超过 budget；单句超长时按字符硬切。
func SplitTranscript(text string, budget int) []string {
	if EstimateTokens(text) <= budget {
		return []string{text}
	}
	var sentences []string
	var current strings.Builder
	for _, r := range text {
		current.WriteRune(r)
		if strings.ContainsRune("。！？!?\n", r) || (r == '.' && current.Len() > 1) {
			sentences = append(sentences, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 {
		sentences = append(sentences, current.String())
	}
	var chunks []string
	var chunk strings.Builder
	flush := func() {
		if strings.TrimSpace(chunk.String()) != "" {
			chunks = append(chunks, chunk.String())
		}
		chunk.Reset()
	}
	for _, sentence := range sentences {
		for EstimateTokens(sentence) > budget {
			flush()
			runes := []rune(sentence)
			cut := len(runes) * budget / EstimateTokens(sentence)
			if cut < 1 {
				cut = 1
			}
			chunks = append(chunks, string(runes[:cut]))
			sentence = string(runes[cut:])
		}
		if EstimateTokens(chunk.String()+sentence) > budget {
			flush()
		}
		chunk.WriteString(sentence)
	}
	flush()
	return chunks
}

func (extractor *Extractor) Extract(ctx context.Context, transcript string) (Extraction, error) {
	started := time.Now()
	if strings.TrimSpace(transcript) == "" {
		return Extraction{Insights: Empty(), Elapsed: time.Since(started)}, nil
	}
	chunks := SplitTranscript(transcript, extractor.transcriptBudget())
	extraction := Extraction{Chunks: len(chunks)}
	partials := make([]Insights, 0, len(chunks))
	for index, chunk := range chunks {
		user := "会议转写：\n" + chunk
		if len(chunks) > 1 {
			user = fmt.Sprintf("会议转写（第 %d/%d 段）：\n%s", index+1, len(chunks), chunk)
		}
		partial, err := extractor.call(ctx, &extraction, systemPrompt, user)
		if err != nil {
			extraction.Elapsed = time.Since(started)
			return extraction, err
		}
		partials = append(partials, partial)
	}
	if len(partials) == 1 {
		extraction.Insights = partials[0]
	} else {
		encoded, _ := json.Marshal(partials)
		merged, err := extractor.call(ctx, &extraction, mergePrompt, string(encoded))
		if err != nil {
			extraction.Elapsed = time.Since(started)
			return extraction, err
		}
		extraction.Insights = merged
	}
	extraction.Elapsed = time.Since(started)
	return extraction, nil
}

func (extractor *Extractor) call(ctx context.Context, extraction *Extraction, system, user string) (Insights, error) {
	result, err := extractor.chat.ChatJSON(ctx, []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}, Schema, Validate)
	extraction.Calls++
	extraction.Attempts += result.Attempts
	extraction.FormatFailures += result.FormatFailures
	extraction.Usage = extraction.Usage.Add(result.Usage)
	if err != nil {
		return Insights{}, err
	}
	return Parse(result.Content)
}
