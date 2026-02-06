package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

const (
	// Типы задач
	TypeWorkflowDelay    = "workflow:delay"
	TypeWorkflowSchedule = "workflow:schedule"
	TypeSendMessage      = "telegram:send"
)

// DelayWorkflowPayload - данные для отложенного выполнения workflow
type DelayWorkflowPayload struct {
	WorkflowID  uuid.UUID `json:"workflow_id"`
	ProfileID   uuid.UUID `json:"profile_id"`
	ChatID      int64     `json:"chat_id"`
	UserID      int64     `json:"user_id"`
	DelaySeconds int      `json:"delay_seconds"`
}

// NewDelayWorkflowTask создает задачу для отложенного выполнения
func NewDelayWorkflowTask(payload DelayWorkflowPayload) (*asynq.Task, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	// Задача будет выполнена через delay_seconds
	return asynq.NewTask(
		TypeWorkflowDelay,
		data,
		asynq.ProcessIn(time.Duration(payload.DelaySeconds)*time.Second),
	), nil
}

// HandleDelayWorkflow обрабатывает отложенное выполнение workflow
func HandleDelayWorkflow(ctx context.Context, t *asynq.Task) error {
	var p DelayWorkflowPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("json.Unmarshal failed: %w", err)
	}

	log.Printf("⏱️ Выполнение отложенного workflow %s для profile %s", p.WorkflowID, p.ProfileID)

	// TODO: запустить workflow через engine
	
	return nil
}

// ScheduleWorkflowPayload - данные для workflow по расписанию
type ScheduleWorkflowPayload struct {
	WorkflowID uuid.UUID `json:"workflow_id"`
	ProfileID  uuid.UUID `json:"profile_id"`
	Cron       string    `json:"cron"`
}

// NewScheduleWorkflowTask создает периодическую задачу
func NewScheduleWorkflowTask(payload ScheduleWorkflowPayload) (*asynq.Task, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	return asynq.NewTask(TypeWorkflowSchedule, data), nil
}

// HandleScheduleWorkflow обрабатывает workflow по расписанию
func HandleScheduleWorkflow(ctx context.Context, t *asynq.Task) error {
	var p ScheduleWorkflowPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("json.Unmarshal failed: %w", err)
	}

	log.Printf("📅 Выполнение workflow по расписанию %s для profile %s", p.WorkflowID, p.ProfileID)

	// TODO: запустить workflow через engine

	return nil
}
