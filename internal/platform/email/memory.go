package email

import (
	"context"
	"sync"
)

// MemorySender provides a thread-safe in-memory email sender implementation for testing.
type MemorySender struct {
	mu       sync.RWMutex
	messages []EmailMessage
	failErr  error
}

// NewMemorySender instantiates an empty MemorySender.
func NewMemorySender() *MemorySender {
	return &MemorySender{
		messages: make([]EmailMessage, 0),
	}
}

// Send records the email in memory or returns the simulated error.
func (m *MemorySender) Send(ctx context.Context, msg EmailMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.failErr != nil {
		err := m.failErr
		m.failErr = nil // Single-shot error simulation
		return err
	}

	if len(msg.To) == 0 {
		return ErrInvalidRecipient
	}

	// Deep-copy To slice to prevent mutation
	recipients := make([]string, len(msg.To))
	copy(recipients, msg.To)
	msgCopy := msg
	msgCopy.To = recipients

	m.messages = append(m.messages, msgCopy)
	return nil
}

// GetMessages returns a snapshot of all sent messages.
func (m *MemorySender) GetMessages() []EmailMessage {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res := make([]EmailMessage, len(m.messages))
	for i, msg := range m.messages {
		to := make([]string, len(msg.To))
		copy(to, msg.To)
		msgCopy := msg
		msgCopy.To = to
		res[i] = msgCopy
	}
	return res
}

// Count returns the number of sent messages.
func (m *MemorySender) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.messages)
}

// Reset clears recorded messages and any pending failure.
func (m *MemorySender) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = m.messages[:0]
	m.failErr = nil
}

// SetFailNext instructs the sender to fail on the subsequent Send call.
func (m *MemorySender) SetFailNext(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failErr = err
}
