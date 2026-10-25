package queue

import (
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"
)

const (
	TypeIndex  = "index:transcript"
	QueueIndex = "index"
)

type IndexPayload struct {
	RecordingID string `json:"recording_id"`
	RequestID   string `json:"request_id"`
}

func IndexEnqueueOptions() []asynq.Option {
	return []asynq.Option{asynq.Queue(QueueIndex), asynq.MaxRetry(3)}
}

func NewIndexTask(recordingID, requestID string) (*asynq.Task, error) {
	payload, err := json.Marshal(IndexPayload{RecordingID: recordingID, RequestID: requestID})
	if err != nil {
		return nil, fmt.Errorf("编码索引任务失败：%w", err)
	}
	return asynq.NewTask(TypeIndex, payload), nil
}

func ParseIndexPayload(task *asynq.Task) (IndexPayload, error) {
	var payload IndexPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return IndexPayload{}, fmt.Errorf("解析索引任务失败：%w", err)
	}
	return payload, nil
}
