package queue

import (
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"
)

const (
	TypeAnalysis  = "analysis:insights"
	QueueAnalysis = "analysis"
)

type AnalysisPayload struct {
	RecordingID string `json:"recording_id"`
	RequestID   string `json:"request_id"`
}

// AnalysisEnqueueOptions 把纪要任务放进独立队列，由单独的 asynq.Server 按 LLM 并发上限消费。
func AnalysisEnqueueOptions() []asynq.Option {
	return []asynq.Option{asynq.Queue(QueueAnalysis), asynq.MaxRetry(3)}
}

func NewAnalysisTask(recordingID, requestID string) (*asynq.Task, error) {
	payload, err := json.Marshal(AnalysisPayload{RecordingID: recordingID, RequestID: requestID})
	if err != nil {
		return nil, fmt.Errorf("编码纪要任务失败：%w", err)
	}
	return asynq.NewTask(TypeAnalysis, payload), nil
}

func ParseAnalysisPayload(task *asynq.Task) (AnalysisPayload, error) {
	var payload AnalysisPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return AnalysisPayload{}, fmt.Errorf("解析纪要任务失败：%w", err)
	}
	return payload, nil
}
