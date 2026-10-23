package queue

import (
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"
)

const (
	TypeTranscode  = "transcode:hls"
	QueueTranscode = "transcode"
)

type TranscodePayload struct {
	RecordingID string `json:"recording_id"`
	RequestID   string `json:"request_id"`
}

// TranscodeEnqueueOptions 把任务放进独立队列，由单独的 asynq.Server 按自己的并发上限消费。
func TranscodeEnqueueOptions() []asynq.Option {
	return []asynq.Option{asynq.Queue(QueueTranscode), asynq.MaxRetry(3)}
}

func NewTranscodeTask(recordingID, requestID string) (*asynq.Task, error) {
	payload, err := json.Marshal(TranscodePayload{RecordingID: recordingID, RequestID: requestID})
	if err != nil {
		return nil, fmt.Errorf("编码转码任务失败：%w", err)
	}
	return asynq.NewTask(TypeTranscode, payload), nil
}

func ParseTranscodePayload(task *asynq.Task) (TranscodePayload, error) {
	var payload TranscodePayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return TranscodePayload{}, fmt.Errorf("解析转码任务失败：%w", err)
	}
	return payload, nil
}
