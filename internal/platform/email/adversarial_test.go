package email_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"shopflow/internal/platform/email"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// 1. ADVERSARIAL TEST: Concurrent Read/Write/Reset on MemorySender
func TestAdversarial_MemorySender_ConcurrentStress(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	ctx := context.Background()
	const numWriters = 30
	const numReaders = 20

	var wg sync.WaitGroup
	wg.Add(numWriters + numReaders)

	stopReaders := make(chan struct{})

	// Writers
	for i := 0; i < numWriters; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = sender.Send(ctx, email.EmailMessage{
					From:     "stress@shopflow.io",
					To:       []string{fmt.Sprintf("user-%d-%d@example.com", id, j)},
					Subject:  fmt.Sprintf("Subject %d-%d", id, j),
					TextBody: "Text body content",
					HTMLBody: "<p>HTML body content</p>",
				})
			}
		}(i)
	}

	// Readers
	for i := 0; i < numReaders; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopReaders:
					return
				default:
					_ = sender.Count()
					msgs := sender.GetMessages()
					for _, m := range msgs {
						_ = len(m.To)
					}
					time.Sleep(1 * time.Millisecond)
				}
			}
		}()
	}

	// Wait for writers to complete
	time.Sleep(100 * time.Millisecond)
	close(stopReaders)
	wg.Wait()

	assert.Positive(t, sender.Count())
	assert.Equal(t, numWriters*20, sender.Count())

	// Test Reset under non-active state
	sender.Reset()
	assert.Equal(t, 0, sender.Count())
}

// 2. ADVERSARIAL TEST: Large Email Payloads
func TestAdversarial_LargePayloads(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	ctx := context.Background()

	largeText := strings.Repeat("Line of receipt details.\n", 2000) // ~50KB
	largeHTML := strings.Repeat("<div><span>Item Row</span></div>", 2000)

	msg := email.EmailMessage{
		From:     "noreply@shopflow.io",
		To:       []string{"bulk@example.com"},
		Subject:  "Large Order Confirmation",
		TextBody: largeText,
		HTMLBody: largeHTML,
	}

	err := sender.Send(ctx, msg)
	require.NoError(t, err)
	require.Equal(t, 1, sender.Count())

	sent := sender.GetMessages()[0]
	assert.Equal(t, len(largeText), len(sent.TextBody))
	assert.Equal(t, len(largeHTML), len(sent.HTMLBody))
}

// 3. ADVERSARIAL TEST: SMTPSender Input Validation Boundaries
func TestAdversarial_SMTPSender_BoundaryValidations(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewSMTPSender(email.SMTPConfig{
		Host: "localhost",
		Port: 1025,
	}, slog.Default())
	ctx := context.Background()

	testCases := []struct {
		name        string
		msg         email.EmailMessage
		expectedErr error
	}{
		{
			name: "all whitespace recipients",
			msg: email.EmailMessage{
				To:       []string{"  ", "\t", "\n"},
				Subject:  "Subject",
				TextBody: "Text",
			},
			expectedErr: email.ErrInvalidRecipient,
		},
		{
			name: "empty subject with whitespace",
			msg: email.EmailMessage{
				To:       []string{"user@example.com"},
				Subject:  "   \n\t  ",
				TextBody: "Text",
			},
			expectedErr: email.ErrEmptySubject,
		},
		{
			name: "empty body with whitespace only",
			msg: email.EmailMessage{
				To:       []string{"user@example.com"},
				Subject:  "Valid Subject",
				TextBody: "  \n  ",
				HTMLBody: "   \t  ",
			},
			expectedErr: email.ErrEmptyBody,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := sender.Send(ctx, tc.msg)
			assert.ErrorIs(t, err, tc.expectedErr)
		})
	}
}
