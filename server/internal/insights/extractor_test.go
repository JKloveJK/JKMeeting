package insights

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jiankailei/jkmeeting/server/internal/llm"
)

type fakeChat struct {
	users   []string
	systems []string
	reply   func(call int) (string, error)
}

func (chat *fakeChat) ChatJSON(_ context.Context, messages []llm.Message, _ json.RawMessage, validate func([]byte) error) (llm.Result, error) {
	chat.systems = append(chat.systems, messages[0].Content)
	chat.users = append(chat.users, messages[1].Content)
	content, err := chat.reply(len(chat.users))
	if err != nil {
		return llm.Result{Attempts: 3, Usage: llm.Usage{PromptTokens: 10}}, err
	}
	if err := validate([]byte(content)); err != nil {
		return llm.Result{Attempts: 1, FormatFailures: 1}, err
	}
	return llm.Result{Content: json.RawMessage(content), Attempts: 1, Usage: llm.Usage{PromptTokens: 100, CompletionTokens: 20}}, nil
}

const validJSON = `{"summary":"讨论了发布计划。","decisions":["下周三发布"],"action_items":[{"task":"准备发布说明","owner":"李雷","due":"周二"}],"viewpoints":[{"speaker":"韩梅梅","points":["先灰度"]}]}`

func TestParseRequiresAllFieldsAndNonEmptyKeys(t *testing.T) {
	if _, err := Parse([]byte(validJSON)); err != nil {
		t.Fatalf("Parse(valid) error = %v", err)
	}
	for name, raw := range map[string]string{
		"missing decisions": `{"summary":"s","action_items":[],"viewpoints":[]}`,
		"null viewpoints":   `{"summary":"s","decisions":[],"action_items":[],"viewpoints":null}`,
		"empty summary":     `{"summary":" ","decisions":[],"action_items":[],"viewpoints":[]}`,
		"empty task":        `{"summary":"s","decisions":[],"action_items":[{"task":"","owner":"","due":""}],"viewpoints":[]}`,
		"empty speaker":     `{"summary":"s","decisions":[],"action_items":[],"viewpoints":[{"speaker":"","points":[]}]}`,
		"not json":          `summary: s`,
	} {
		if Validate([]byte(raw)) == nil {
			t.Errorf("%s: Validate() = nil, want error", name)
		}
	}
	if Validate([]byte(`{"summary":"s","decisions":[],"action_items":[],"viewpoints":[]}`)) != nil {
		t.Fatal("空数组应合法")
	}
}

func TestExtractShortTranscriptUsesSingleCall(t *testing.T) {
	chat := &fakeChat{reply: func(int) (string, error) { return validJSON, nil }}
	extraction, err := NewExtractor(chat, ExtractorConfig{}).Extract(context.Background(), "李雷：我负责发布说明。")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if extraction.Calls != 1 || extraction.Chunks != 1 || extraction.Insights.ActionItems[0].Owner != "李雷" ||
		extraction.Usage != (llm.Usage{PromptTokens: 100, CompletionTokens: 20}) || !strings.Contains(chat.users[0], "李雷：我负责发布说明。") {
		t.Fatalf("extraction = %+v users = %v", extraction, chat.users)
	}
}

func TestExtractEmptyTranscriptSkipsModel(t *testing.T) {
	chat := &fakeChat{reply: func(int) (string, error) { t.Fatal("空转写不应调用模型"); return "", nil }}
	extraction, err := NewExtractor(chat, ExtractorConfig{}).Extract(context.Background(), "  \n")
	if err != nil || extraction.Calls != 0 || Validate(mustJSON(t, extraction.Insights)) != nil {
		t.Fatalf("extraction = %+v err = %v", extraction, err)
	}
}

func TestExtractLongTranscriptMapsThenMerges(t *testing.T) {
	sentence := strings.Repeat("我们讨论发布节奏和灰度比例", 10) + "。"
	transcript := strings.Repeat(sentence, 120) // 约 15,600 个汉字
	chat := &fakeChat{reply: func(int) (string, error) { return validJSON, nil }}
	extractor := NewExtractor(chat, ExtractorConfig{ContextTokens: 8192, OutputReserve: 1536})

	extraction, err := extractor.Extract(context.Background(), transcript)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if extraction.Chunks < 3 || extraction.Calls != extraction.Chunks+1 {
		t.Fatalf("chunks = %d calls = %d", extraction.Chunks, extraction.Calls)
	}
	for index, user := range chat.users[:extraction.Chunks] {
		if EstimateTokens(user) > 8192-1536 {
			t.Fatalf("chunk %d 超出预算：%d tokens", index, EstimateTokens(user))
		}
	}
	if chat.systems[len(chat.systems)-1] != mergePrompt || !strings.HasPrefix(chat.users[len(chat.users)-1], "[") {
		t.Fatalf("最后一次调用应为合并：%q", chat.systems[len(chat.systems)-1])
	}
	var total int
	for _, user := range chat.users[:extraction.Chunks] {
		total += strings.Count(user, "我们讨论发布节奏和灰度比例")
	}
	if total != 1200 {
		t.Fatalf("分段丢失或重复内容：%d", total)
	}
}

func TestExtractPropagatesLLMFailureWithUsage(t *testing.T) {
	chat := &fakeChat{reply: func(int) (string, error) { return "", errors.New("llama-server 不可达") }}
	extraction, err := NewExtractor(chat, ExtractorConfig{}).Extract(context.Background(), "王芳：开始吧。")
	if err == nil || extraction.Attempts != 3 || extraction.Usage.PromptTokens != 10 {
		t.Fatalf("extraction = %+v err = %v", extraction, err)
	}
}

func TestSplitTranscriptHardCutsOverlongSentence(t *testing.T) {
	chunks := SplitTranscript(strings.Repeat("长", 2500), 1000)
	if len(chunks) != 3 || strings.Join(chunks, "") != strings.Repeat("长", 2500) {
		t.Fatalf("chunks = %d", len(chunks))
	}
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens("你好，世界"); got != 5 {
		t.Fatalf("CJK = %d", got)
	}
	if got := EstimateTokens("hello world, this is fine"); got < 6 || got > 9 {
		t.Fatalf("ASCII = %d", got)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
