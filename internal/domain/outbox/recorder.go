package outbox

import (
	"context"

	"shopflow/internal/platform/database"
)

// Recorder defines an interface for recording domain events into the outbox within a local transaction.
type Recorder interface {
	RecordEvent(ctx context.Context, dbtx database.DBTX, aggregateType string, aggregateID string, eventType string, payload any) error
}

// EventRecorder implements Recorder using outbox.Repository.
type EventRecorder struct {
	repo Repository
}

// NewEventRecorder creates an EventRecorder.
func NewEventRecorder(repo Repository) *EventRecorder {
	return &EventRecorder{repo: repo}
}

// RecordEvent constructs and saves an OutboxMessage within the supplied database transaction.
func (r *EventRecorder) RecordEvent(ctx context.Context, dbtx database.DBTX, aggregateType string, aggregateID string, eventType string, payload any) error {
	msg, err := NewOutboxMessage(aggregateType, aggregateID, eventType, payload, nil)
	if err != nil {
		return err
	}
	return r.repo.SaveMessage(ctx, dbtx, msg)
}
