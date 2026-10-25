package rag

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitKeepsShortTextAsOneChunk(t *testing.T) {
	chunks := Split("李雷：今天发布。韩梅梅：先灰度。", 300)
	if len(chunks) != 1 || chunks[0].Index != 0 || chunks[0].Content != "李雷：今天发布。韩梅梅：先灰度。" {
		t.Fatalf("chunks = %+v", chunks)
	}
	if Split("   ", 300) != nil {
		t.Fatal("空文本不应产生块")
	}
}

func TestSplitOverlapsOneSentenceAndRespectsLimit(t *testing.T) {
	sentences := []string{"第一句话讲发布计划。", "第二句话讲灰度比例。", "第三句话讲回滚阈值。", "第四句话讲值班安排。"}
	chunks := Split(strings.Join(sentences, ""), 20)
	if len(chunks) != 3 {
		t.Fatalf("chunks = %+v", chunks)
	}
	for i, chunk := range chunks {
		if chunk.Index != i || utf8.RuneCountInString(chunk.Content) > 20 {
			t.Fatalf("chunk %d = %+v", i, chunk)
		}
	}
	if !strings.HasPrefix(chunks[1].Content, sentences[1]) || !strings.HasPrefix(chunks[2].Content, sentences[2]) {
		t.Fatalf("相邻块应重叠上一块的最后一句：%+v", chunks)
	}
	if !strings.Contains(chunks[2].Content, sentences[3]) {
		t.Fatalf("不得丢失内容：%+v", chunks)
	}
}

func TestSplitHardCutsOverlongSentence(t *testing.T) {
	long := strings.Repeat("连", 25)
	chunks := Split(long, 10)
	var total int
	for _, chunk := range chunks {
		if utf8.RuneCountInString(chunk.Content) > 10 {
			t.Fatalf("chunk too long: %q", chunk.Content)
		}
		total += utf8.RuneCountInString(chunk.Content)
	}
	if total < 25 {
		t.Fatalf("内容丢失：%+v", chunks)
	}
}
