package inbox

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// InboxStatus defines the processing status of an inbox message.
type InboxStatus string

const (
	StatusProcessing InboxStatus = "PROCESSING"
	StatusCompleted  InboxStatus = "COMPLETED"
	StatusFailed     InboxStatus = "FAILED"
)

// InboxMessage maps directly to the inbox_messages table.
type InboxMessage struct {
	MessageID     string          `json:"message_id"`
	ConsumerGroup string          `json:"consumer_group"`
	EventType     string          `json:"event_type"`
	Payload       json.RawMessage `json:"payload"`
	Status        InboxStatus     `json:"status"`
	ProcessedAt   time.Time       `json:"processed_at"`
}

// DeadLetterMessage maps to dead_letter_messages table for failed inbox events.
type DeadLetterMessage struct {
	ID          uuid.UUID         `json:"id"`
	SourceType  string            `json:"source_type"` // 'INBOX' or 'OUTBOX'
	SourceID    string            `json:"source_id"`
	Topic       string            `json:"topic"`
	Partition   int               `json:"partition"`
	OffsetVal   int64             `json:"offset_val"`
	ErrorReason string            `json:"error_reason"`
	Payload     json.RawMessage   `json:"payload"`
	Headers     map[string]string `json:"headers"`
	RetryCount  int               `json:"retry_count"`
	CreatedAt   time.Time         `json:"created_at"`
}
