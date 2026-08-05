package inbox

import (
	"context"
	"sync"
	"time"

	"shopflow/internal/platform/database"

	"github.com/google/uuid"
)

// MockRepository is a thread-safe in-memory implementation of inbox Repository.
type MockRepository struct {
	mu          sync.Mutex
	messages    map[string]*InboxMessage
	deadLetters []DeadLetterMessage
}

// NewMockRepository creates an empty in-memory inbox repository.
func NewMockRepository() *MockRepository {
	return &MockRepository{
		messages: make(map[string]*InboxMessage),
	}
}

func (m *MockRepository) makeKey(messageID, consumerGroup string) string {
	return messageID + "::" + consumerGroup
}

// TryStartProcessing simulates atomic deduplication via primary key constraint.
func (m *MockRepository) TryStartProcessing(ctx context.Context, dbtx database.DBTX, msg *InboxMessage) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := m.makeKey(msg.MessageID, msg.ConsumerGroup)
	if existing, ok := m.messages[key]; ok {
		if existing.Status == StatusCompleted {
			return false, nil // Already processed idempotently
		}
		if existing.Status == StatusFailed {
			now := time.Now().UTC()
			existing.Status = StatusProcessing
			existing.ProcessedAt = now
			msg.Status = StatusProcessing
			msg.ProcessedAt = now
			return true, nil
		}
		if existing.Status == StatusProcessing {
			return false, ErrDuplicateMessage
		}
		return false, ErrDuplicateMessage
	}

	cp := *msg
	if cp.Status == "" {
		cp.Status = StatusProcessing
	}
	cp.ProcessedAt = time.Now().UTC()
	m.messages[key] = &cp
	return true, nil
}

// MarkCompleted transitions status to COMPLETED.
func (m *MockRepository) MarkCompleted(ctx context.Context, dbtx database.DBTX, messageID, consumerGroup string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := m.makeKey(messageID, consumerGroup)
	msg, ok := m.messages[key]
	if !ok {
		return ErrMessageNotFound
	}
	msg.Status = StatusCompleted
	msg.ProcessedAt = time.Now().UTC()
	return nil
}

// MarkFailed transitions status to FAILED.
func (m *MockRepository) MarkFailed(ctx context.Context, dbtx database.DBTX, messageID, consumerGroup string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := m.makeKey(messageID, consumerGroup)
	msg, ok := m.messages[key]
	if !ok {
		return ErrMessageNotFound
	}
	msg.Status = StatusFailed
	msg.ProcessedAt = time.Now().UTC()
	return nil
}

// SaveDeadLetter writes to dead letter storage.
func (m *MockRepository) SaveDeadLetter(ctx context.Context, dbtx database.DBTX, dlq *DeadLetterMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if dlq.ID == uuid.Nil {
		dlq.ID = uuid.New()
	}
	if dlq.CreatedAt.IsZero() {
		dlq.CreatedAt = time.Now().UTC()
	}

	cp := *dlq
	m.deadLetters = append(m.deadLetters, cp)
	return nil
}

// GetMessage retrieves an inbox record.
func (m *MockRepository) GetMessage(ctx context.Context, messageID, consumerGroup string) (*InboxMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := m.makeKey(messageID, consumerGroup)
	msg, ok := m.messages[key]
	if !ok {
		return nil, ErrMessageNotFound
	}
	cp := *msg
	return &cp, nil
}

// GetDeadLetterMessages retrieves recorded dead letters.
func (m *MockRepository) GetDeadLetterMessages(ctx context.Context, limit int) ([]DeadLetterMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if limit <= 0 || limit > len(m.deadLetters) {
		limit = len(m.deadLetters)
	}

	result := make([]DeadLetterMessage, limit)
	copy(result, m.deadLetters[:limit])
	return result, nil
}
