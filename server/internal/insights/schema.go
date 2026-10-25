// Package insights 把会议转写抽取为结构化纪要：摘要、决议、待办与参会人观点。
package insights

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type ActionItem struct {
	Task  string `json:"task"`
	Owner string `json:"owner"`
	Due   string `json:"due"`
}

type Viewpoint struct {
	Speaker string   `json:"speaker"`
	Points  []string `json:"points"`
}

type Insights struct {
	Summary     string       `json:"summary"`
	Decisions   []string     `json:"decisions"`
	ActionItems []ActionItem `json:"action_items"`
	Viewpoints  []Viewpoint  `json:"viewpoints"`
}

// Schema 交给 llama-server 转成语法约束解码；Validate 再兜住语法约束表达不了的语义规则。
var Schema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["summary", "decisions", "action_items", "viewpoints"],
  "properties": {
    "summary": {"type": "string"},
    "decisions": {"type": "array", "items": {"type": "string"}},
    "action_items": {"type": "array", "items": {
      "type": "object", "additionalProperties": false,
      "required": ["task", "owner", "due"],
      "properties": {"task": {"type": "string"}, "owner": {"type": "string"}, "due": {"type": "string"}}
    }},
    "viewpoints": {"type": "array", "items": {
      "type": "object", "additionalProperties": false,
      "required": ["speaker", "points"],
      "properties": {"speaker": {"type": "string"}, "points": {"type": "array", "items": {"type": "string"}}}
    }}
  }
}`)

// Empty 用于空转写：不调用模型，返回结构完整的空结果。
func Empty() Insights {
	return Insights{Summary: "（无语音内容）", Decisions: []string{}, ActionItems: []ActionItem{}, Viewpoints: []Viewpoint{}}
}

// Parse 解析并校验模型输出；四个字段必须存在（数组可为空），摘要、待办内容与发言人不能为空。
func Parse(raw []byte) (Insights, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Insights{}, err
	}
	for _, name := range []string{"summary", "decisions", "action_items", "viewpoints"} {
		if value, ok := fields[name]; !ok || string(value) == "null" {
			return Insights{}, fmt.Errorf("缺少字段 %s", name)
		}
	}
	var result Insights
	if err := json.Unmarshal(raw, &result); err != nil {
		return Insights{}, err
	}
	if strings.TrimSpace(result.Summary) == "" {
		return Insights{}, errors.New("summary 为空")
	}
	for _, item := range result.ActionItems {
		if strings.TrimSpace(item.Task) == "" {
			return Insights{}, errors.New("action_items[].task 为空")
		}
	}
	for _, viewpoint := range result.Viewpoints {
		if strings.TrimSpace(viewpoint.Speaker) == "" {
			return Insights{}, errors.New("viewpoints[].speaker 为空")
		}
	}
	return result, nil
}

func Validate(raw []byte) error {
	_, err := Parse(raw)
	return err
}
