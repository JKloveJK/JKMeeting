package queue

import (
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"
)

const TypeTranscription = "transcription:run"

type TranscriptionPayload struct {
	RecordingID string `json:"recording_id"`
	RequestID   string `json:"request_id"`
}

func NewTranscriptionTask(recordingID, requestID string) (*asynq.Task, error) {
	payload, err := json.Marshal(TranscriptionPayload{
		RecordingID: recordingID,
		RequestID:   requestID,
	})
	if err != nil {
		return nil, fmt.Errorf("编码转写任务失败：%w", err)
	}
	return asynq.NewTask(TypeTranscription, payload), nil
}

func ParseTranscriptionPayload(task *asynq.Task) (TranscriptionPayload, error) {
	var payload TranscriptionPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return TranscriptionPayload{}, fmt.Errorf("解析转写任务失败：%w", err)
	}
	return payload, nil
}
