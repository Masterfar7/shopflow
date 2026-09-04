package email_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"shopflow/internal/platform/config"
	"shopflow/internal/platform/email"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestMemorySender_Basic(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	assert.Equal(t, 0, sender.Count())
	assert.Empty(t, sender.GetMessages())

	ctx := context.Background()
	msg := email.EmailMessage{
		From:     "sender@shopflow.io",
		To:       []string{"customer@example.com"},
		Subject:  "Test Email",
		TextBody: "Hello world text",
		HTMLBody: "<p>Hello world html</p>",
	}

	err := sender.Send(ctx, msg)
	require.NoError(t, err)
	assert.Equal(t, 1, sender.Count())

	msgs := sender.GetMessages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "customer@example.com", msgs[0].To[0])
	assert.Equal(t, "Test Email", msgs[0].Subject)
	assert.Equal(t, "Hello world text", msgs[0].TextBody)
	assert.Equal(t, "<p>Hello world html</p>", msgs[0].HTMLBody)

	// Ensure snapshot immutability
	msgs[0].To[0] = "mutated@example.com"
	assert.Equal(t, "customer@example.com", sender.GetMessages()[0].To[0])

	sender.Reset()
	assert.Equal(t, 0, sender.Count())
	assert.Empty(t, sender.GetMessages())
}

func TestMemorySender_Concurrency(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	ctx := context.Background()
	const workers = 50

	var wg sync.WaitGroup
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			err := sender.Send(ctx, email.EmailMessage{
				From:     "noreply@shopflow.io",
				To:       []string{fmt.Sprintf("user-%d@example.com", id)},
				Subject:  fmt.Sprintf("Order %d", id),
				TextBody: "Your order details",
			})
			assert.NoError(t, err)
		}(i)
	}

	wg.Wait()
	assert.Equal(t, workers, sender.Count())
	assert.Len(t, sender.GetMessages(), workers)
}

func TestMemorySender_FailNext(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	ctx := context.Background()

	expectedErr := errors.New("simulated smtp network timeout")
	sender.SetFailNext(expectedErr)

	msg := email.EmailMessage{
		To:       []string{"test@example.com"},
		Subject:  "Hi",
		TextBody: "Text",
	}

	// First attempt must fail with simulated error
	err := sender.Send(ctx, msg)
	assert.ErrorIs(t, err, expectedErr)
	assert.Equal(t, 0, sender.Count())

	// Next attempt must succeed automatically (single-shot behavior)
	err = sender.Send(ctx, msg)
	require.NoError(t, err)
	assert.Equal(t, 1, sender.Count())
}

func TestMemorySender_Validation(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	ctx := context.Background()

	err := sender.Send(ctx, email.EmailMessage{
		To:       nil,
		Subject:  "No Recipient",
		TextBody: "Text",
	})
	assert.ErrorIs(t, err, email.ErrInvalidRecipient)
	assert.Equal(t, 0, sender.Count())
}

func TestMemorySender_ContextCancelled(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewMemorySender()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sender.Send(ctx, email.EmailMessage{
		To:       []string{"test@example.com"},
		Subject:  "Test",
		TextBody: "Text",
	})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, sender.Count())
}

func TestSMTPSender_Validation(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewSMTPSender(email.SMTPConfig{
		Host: "localhost",
		Port: 1025,
	}, slog.Default())
	ctx := context.Background()

	// Empty To
	err := sender.Send(ctx, email.EmailMessage{
		To:       nil,
		Subject:  "Test",
		TextBody: "Body",
	})
	assert.ErrorIs(t, err, email.ErrInvalidRecipient)

	// Blank recipient
	err = sender.Send(ctx, email.EmailMessage{
		To:       []string{"   "},
		Subject:  "Test",
		TextBody: "Body",
	})
	assert.ErrorIs(t, err, email.ErrInvalidRecipient)

	// Empty subject
	err = sender.Send(ctx, email.EmailMessage{
		To:       []string{"user@example.com"},
		Subject:  "   ",
		TextBody: "Body",
	})
	assert.ErrorIs(t, err, email.ErrEmptySubject)

	// Empty body (both html and text blank)
	err = sender.Send(ctx, email.EmailMessage{
		To:       []string{"user@example.com"},
		Subject:  "Test",
		TextBody: "",
		HTMLBody: "",
	})
	assert.ErrorIs(t, err, email.ErrEmptyBody)
}

func TestSMTPSender_ContextCancelled(t *testing.T) {
	defer goleak.VerifyNone(t)

	sender := email.NewSMTPSender(email.SMTPConfig{
		Host: "127.0.0.1",
		Port: 65530,
	}, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sender.Send(ctx, email.EmailMessage{
		To:       []string{"user@example.com"},
		Subject:  "Test",
		TextBody: "Hello",
	})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestSMTPSender_DialFailure(t *testing.T) {
	defer goleak.VerifyNone(t)

	// Use an unreachable port with short timeout
	sender := email.NewSMTPSender(email.SMTPConfig{
		Host: "127.0.0.1",
		Port: 54321, // No SMTP server listening
	}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := sender.Send(ctx, email.EmailMessage{
		To:       []string{"user@example.com"},
		Subject:  "Test",
		TextBody: "Hello",
	})
	assert.Error(t, err)
}

func TestSenderFactory(t *testing.T) {
	defer goleak.VerifyNone(t)

	cfgMem := config.Config{EmailProvider: "memory"}
	sMem, err := email.NewSenderFromConfig(cfgMem, slog.Default())
	require.NoError(t, err)
	_, isMem := sMem.(*email.MemorySender)
	assert.True(t, isMem)

	cfgSMTP := config.Config{
		EmailProvider: "smtp",
		SMTPHost:      "localhost",
		SMTPPort:      1025,
		EmailFrom:     "shop@example.com",
	}
	sSMTP, err := email.NewSenderFromConfig(cfgSMTP, slog.Default())
	require.NoError(t, err)
	_, isSMTP := sSMTP.(*email.SMTPSender)
	assert.True(t, isSMTP)
}
