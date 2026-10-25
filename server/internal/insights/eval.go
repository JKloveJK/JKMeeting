package insights

import (
	"strings"
	"unicode"
)

// KeyPoint 是评测集中的一个要点。Keywords 内每项都必须命中，单项可用 "|" 写同义词；
// Owner/Speaker 非空时还要求对应字段包含该人名。
type KeyPoint struct {
	Keywords []string `json:"keywords"`
	Owner    string   `json:"owner,omitempty"`
	Speaker  string   `json:"speaker,omitempty"`
}

type Reference struct {
	Decisions   []KeyPoint `json:"decisions"`
	ActionItems []KeyPoint `json:"action_items"`
	Viewpoints  []KeyPoint `json:"viewpoints"`
}

type CategoryScore struct {
	Total   int      `json:"total"`
	Matched int      `json:"matched"`
	Missed  []string `json:"missed,omitempty"`
}

type Score struct {
	Decisions   CategoryScore `json:"decisions"`
	ActionItems CategoryScore `json:"action_items"`
	Viewpoints  CategoryScore `json:"viewpoints"`
}

func (score Score) Total() int {
	return score.Decisions.Total + score.ActionItems.Total + score.Viewpoints.Total
}

func (score Score) Matched() int {
	return score.Decisions.Matched + score.ActionItems.Matched + score.Viewpoints.Matched
}

// ScoreInsights 用规则判定要点召回，不使用 LLM 打分，保证同一输出的分数可复现。
func ScoreInsights(got Insights, reference Reference) Score {
	var score Score
	for _, point := range reference.Decisions {
		score.Decisions.add(point, anyMatch(len(got.Decisions), func(i int) bool {
			return containsAll(got.Decisions[i], point.Keywords)
		}))
	}
	for _, point := range reference.ActionItems {
		score.ActionItems.add(point, anyMatch(len(got.ActionItems), func(i int) bool {
			item := got.ActionItems[i]
			return containsAll(item.Task, point.Keywords) && personMatches(item.Owner, point.Owner)
		}))
	}
	for _, point := range reference.Viewpoints {
		score.Viewpoints.add(point, anyMatch(len(got.Viewpoints), func(i int) bool {
			view := got.Viewpoints[i]
			return personMatches(view.Speaker, point.Speaker) && containsAll(strings.Join(view.Points, "；"), point.Keywords)
		}))
	}
	return score
}

func (category *CategoryScore) add(point KeyPoint, matched bool) {
	category.Total++
	if matched {
		category.Matched++
		return
	}
	label := strings.Join(point.Keywords, "+")
	if person := point.Owner + point.Speaker; person != "" {
		label = person + ":" + label
	}
	category.Missed = append(category.Missed, label)
}

func anyMatch(count int, match func(int) bool) bool {
	for i := 0; i < count; i++ {
		if match(i) {
			return true
		}
	}
	return false
}

// ContainsKeywords 与要点评分使用同一套规则，供问答评测复用。
func ContainsKeywords(text string, keywords []string) bool { return containsAll(text, keywords) }

func containsAll(text string, keywords []string) bool {
	normalized := normalize(text)
	for _, keyword := range keywords {
		hit := false
		for _, alternative := range strings.Split(keyword, "|") {
			if alternative = normalize(alternative); alternative != "" && strings.Contains(normalized, alternative) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

func personMatches(got, want string) bool {
	if want == "" {
		return true
	}
	return strings.Contains(normalize(got), normalize(want))
}

func normalize(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, text)
}
