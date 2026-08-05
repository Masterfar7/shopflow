package outbox

import (
	"context"
	"sort"
	"sync"
	"time"

	"shopflow/internal/platform/database"

	"github.com/google/uuid"
)

// MockRepository is an in-memory thread-safe implementation of Repository.
type MockRepository struct {
	mu          sync.Mutex
	messages    map[uuid.UUID]*OutboxMessage
	deadLetters []DeadLetterMessage
}

// NewMockRepository creates a new in-memory MockRepository.
func NewMockRepository() *MockRepository {
	return &MockRepository{
		messages: make(map[uuid.UUID]*OutboxMessage),
	}
}

// SaveMessage stores an outbox message in memory.
func (m *MockRepository) SaveMessage(ctx context.Context, dbtx database.DBTX, msg *OutboxMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if msg.ID == uuid.Nil {
		msg.ID = uuid.New()
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	if msg.Status == "" {
		msg.Status = StatusPending
	}

	cp := *msg
	if msg.Headers != nil {
		hCopy := make(map[string]string, len(msg.Headers))
		for k, v := range msg.Headers {
			hCopy[k] = v
		}
		cp.Headers = hCopy
	}
	m.messages[msg.ID] = &cp
	return nil
}

// LeaseMessages simulates FOR UPDATE SKIP LOCKED batch leasing atomically.
func (m *MockRepository) LeaseMessages(ctx context.Context, workerID string, batchSize int, leaseDuration time.Duration) ([]*OutboxMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	expires := now.Add(leaseDuration)

	// Collect candidate messages
	var candidates []*OutboxMessage
	for _, msg := range m.messages {
		if msg.Status != StatusPending && msg.Status != StatusFailed {
			continue
		}
		if msg.LeasedUntil == nil || msg.LeasedUntil.Before(now) {
			candidates = append(candidates, msg)
		}
	}

	// Sort deterministically by CreatedAt ASC, tie-breaking by ID
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].CreatedAt.Equal(candidates[j].CreatedAt) {
			return candidates[i].ID.String() < candidates[j].ID.String()
		}
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})

	limit := batchSize
	if limit > len(candidates) {
		limit = len(candidates)
	}

	var leased []*OutboxMessage
	for i := 0; i < limit; i++ {
		target := candidates[i]
		target.LeasedUntil = &expires
		w := workerID
		target.LeasedBy = &w

		cp := *target
		leased = append(leased, &cp)
	}

	return leased, nil
}

// ExtendLease extends lease duration for specified message IDs.
func (m *MockRepository) ExtendLease(ctx context.Context, workerID string, ids []uuid.UUID, leaseDuration time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	expires := time.Now().UTC().Add(leaseDuration)
	for _, id := range ids {
		if msg, ok := m.messages[id]; ok {
			if msg.LeasedBy != nil && *msg.LeasedBy == workerID {
				msg.LeasedUntil = &expires
			}
		}
	}
	return nil
}

// MarkPublished transitions message to PUBLISHED and clears lease fields.
func (m *MockRepository) MarkPublished(ctx context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	msg, ok := m.messages[id]
	if !ok {
		return ErrMessageNotFound
	}

	now := time.Now().UTC()
	msg.Status = StatusPublished
	msg.PublishedAt = &now
	msg.LeasedUntil = nil
	msg.LeasedBy = nil
	return nil
}

// MarkFailed increments retry count and schedules next attempt or dead letters.
func (m *MockRepository) MarkFailed(ctx context.Context, id uuid.UUID, errMsg string, nextRetryAt time.Time, deadLetter bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	msg, ok := m.messages[id]
	if !ok {
		return ErrMessageNotFound
	}

	msg.RetryCount++
	msg.LastError = &errMsg
	msg.LeasedBy = nil

	if deadLetter {
		msg.Status = StatusDeadLetter
		msg.LeasedUntil = nil
	} else {
		msg.Status = StatusFailed
		msg.LeasedUntil = &nextRetryAt
	}
	return nil
}

// SaveDeadLetter stores a dead letter record.
func (m *MockRepository) SaveDeadLetter(ctx context.Context, dlq *DeadLetterMessage) error {
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

// GetMessageByID retrieves a message by ID.
func (m *MockRepository) GetMessageByID(ctx context.Context, id uuid.UUID) (*OutboxMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	msg, ok := m.messages[id]
	if !ok {
		return nil, ErrMessageNotFound
	}
	cp := *msg
	return &cp, nil
}

// GetDeadLetterMessages returns recorded dead letter messages.
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

// GetPendingCount returns the count of messages in PENDING or FAILED status.
func (m *MockRepository) GetPendingCount(ctx context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var count int64
	for _, msg := range m.messages {
		if msg.Status == StatusPending || msg.Status == StatusFailed {
			count++
		}
	}
	return count, nil
}
