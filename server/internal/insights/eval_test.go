package insights

import "testing"

func TestScoreInsightsMatchesKeywordsOwnersAndSpeakers(t *testing.T) {
	got := Insights{
		Summary:   "讨论发布",
		Decisions: []string{"下周三（10 月 28 日）正式发布 v2.1"},
		ActionItems: []ActionItem{
			{Task: "准备发布说明", Owner: "李雷", Due: "周二"},
			{Task: "压测网关", Owner: "王五"},
		},
		Viewpoints: []Viewpoint{{Speaker: "韩梅梅", Points: []string{"建议先灰度 10% 用户"}}},
	}
	reference := Reference{
		Decisions: []KeyPoint{{Keywords: []string{"周三|星期三", "发布"}}, {Keywords: []string{"回滚"}}},
		ActionItems: []KeyPoint{
			{Keywords: []string{"发布说明"}, Owner: "李雷"},
			{Keywords: []string{"压测"}, Owner: "张三"},
		},
		Viewpoints: []KeyPoint{{Keywords: []string{"灰度"}, Speaker: "韩梅梅"}},
	}

	score := ScoreInsights(got, reference)
	if score.Total() != 5 || score.Matched() != 3 {
		t.Fatalf("score = %+v", score)
	}
	if len(score.Decisions.Missed) != 1 || score.Decisions.Missed[0] != "回滚" {
		t.Fatalf("decision missed = %v", score.Decisions.Missed)
	}
	if len(score.ActionItems.Missed) != 1 || score.ActionItems.Missed[0] != "张三:压测" {
		t.Fatalf("负责人不符时不得计为命中：%v", score.ActionItems.Missed)
	}
}

func TestContainsAllIgnoresPunctuationAndCase(t *testing.T) {
	if !containsAll("上线 API，v2！", []string{"api", "V2"}) {
		t.Fatal("应忽略大小写与标点")
	}
	if containsAll("只有灰度", []string{"灰度", "回滚|撤回"}) {
		t.Fatal("缺任一关键词都不应命中")
	}
}
