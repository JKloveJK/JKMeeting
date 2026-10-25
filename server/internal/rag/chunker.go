// Package rag 负责转写切块、向量索引任务与基于检索结果的问答。
package rag

import (
	"strings"
	"unicode/utf8"
)

type Chunk struct {
	Index   int
	Content string
}

// Split 按句子累积到 maxRunes 以内，相邻块重叠最后一句；单句超长时按 maxRunes 硬切。
func Split(text string, maxRunes int) []Chunk {
	text = strings.TrimSpace(text)
	if text == "" || maxRunes <= 0 {
		return nil
	}
	var sentences []string
	for _, sentence := range splitSentences(text) {
		runes := []rune(sentence)
		for len(runes) > maxRunes {
			sentences = append(sentences, string(runes[:maxRunes]))
			runes = runes[maxRunes:]
		}
		if len(runes) > 0 {
			sentences = append(sentences, string(runes))
		}
	}

	var chunks []Chunk
	var current []string
	size := 0
	flush := func() {
		content := strings.TrimSpace(strings.Join(current, ""))
		if content != "" {
			chunks = append(chunks, Chunk{Index: len(chunks), Content: content})
		}
	}
	for _, sentence := range sentences {
		length := utf8.RuneCountInString(sentence)
		if size+length > maxRunes && len(current) > 0 {
			flush()
			overlap := current[len(current)-1]
			current, size = nil, 0
			if utf8.RuneCountInString(overlap)+length <= maxRunes {
				current, size = []string{overlap}, utf8.RuneCountInString(overlap)
			}
		}
		current = append(current, sentence)
		size += length
	}
	flush()
	return chunks
}

func splitSentences(text string) []string {
	var sentences []string
	var current strings.Builder
	for _, r := range text {
		current.WriteRune(r)
		if strings.ContainsRune("。！？；!?;\n", r) {
			sentences = append(sentences, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 {
		sentences = append(sentences, current.String())
	}
	return sentences
}
