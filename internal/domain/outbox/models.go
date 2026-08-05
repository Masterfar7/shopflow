package outbox

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// OutboxStatus defines the lifecycle states of an outbox message.
type OutboxStatus string

const (
	StatusPending    OutboxStatus = "PENDING"
	StatusPublished  OutboxStatus = "PUBLISHED"
	StatusFailed     OutboxStatus = "FAILED"
	StatusDeadLetter OutboxStatus = "DEAD_LETTER"
)

// OutboxMessage maps directly to the outbox_messages table.
type OutboxMessage struct {
	ID            uuid.UUID         `json:"id"`
	AggregateType string            `json:"aggregate_type"`
	AggregateID   string            `json:"aggregate_id"`
	EventType     string            `json:"event_type"`
	Payload       json.RawMessage   `json:"payload"`
	Headers       map[string]string `json:"headers"`
	Status        OutboxStatus      `json:"status"`
	RetryCount    int               `json:"retry_count"`
	LastError     *string           `json:"last_error,omitempty"`
	LeasedUntil   *time.Time        `json:"leased_until,omitempty"`
	LeasedBy      *string           `json:"leased_by,omitempty"`
	TraceContext  *string           `json:"trace_context,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	PublishedAt   *time.Time        `json:"published_at,omitempty"`
}

// DeadLetterMessage maps directly to the dead_letter_messages table.
type DeadLetterMessage struct {
	ID          uuid.UUID         `json:"id"`
	SourceType  string            `json:"source_type"` // 'OUTBOX' or 'INBOX'
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

// NewOutboxMessage creates a new PENDING outbox message.
func NewOutboxMessage(aggregateType, aggregateID, eventType string, payload any, headers map[string]string) (*OutboxMessage, error) {
	var payloadBytes []byte
	switch p := payload.(type) {
	case []byte:
		payloadBytes = p
	case string:
		payloadBytes = []byte(p)
	case json.RawMessage:
		payloadBytes = p
	default:
		var err error
		payloadBytes, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}

	if headers == nil {
		headers = make(map[string]string)
	}

	return &OutboxMessage{
		ID:            uuid.New(),
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		EventType:     eventType,
		Payload:       payloadBytes,
		Headers:       headers,
		Status:        StatusPending,
		RetryCount:    0,
		CreatedAt:     time.Now().UTC(),
	}, nil
}
