package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"shopflow/internal/domain/inbox"
	"shopflow/internal/platform/email"
	"shopflow/internal/platform/kafka"
)

// Service coordinates event-driven transactional notification delivery.
type Service struct {
	sender   email.Sender
	renderer *Renderer
	logger   *slog.Logger
}

// NewService instantiates a notification Service.
func NewService(sender email.Sender, renderer *Renderer, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		sender:   sender,
		renderer: renderer,
		logger:   logger,
	}
}

// HandleMessage processes incoming order lifecycle events from Kafka.
// STRICT INVARIANT: Sender.Send is called OUTSIDE ANY DATABASE TRANSACTION.
func (s *Service) HandleMessage(ctx context.Context, msg kafka.Message) error {
	eventType := strings.TrimSpace(msg.Headers["event_type"])
	if eventType == "" {
		// Attempt extraction from JSON payload
		var peek struct {
			EventType string `json:"event_type"`
			Type      string `json:"type"`
		}
		if err := json.Unmarshal(msg.Value, &peek); err == nil {
			if peek.EventType != "" {
				eventType = peek.EventType
			} else if peek.Type != "" {
				eventType = peek.Type
			}
		}
	}

	switch eventType {
	case "OrderConfirmed":
		return s.handleOrderConfirmed(ctx, msg.Value)
	case "OrderCancelled":
		return s.handleOrderCancelled(ctx, msg.Value)
	default:
		return nil
	}
}

func (s *Service) handleOrderConfirmed(ctx context.Context, data []byte) error {
	var payload OrderConfirmedPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		s.logger.WarnContext(ctx, "failed to unmarshal OrderConfirmed payload", "error", err)
		return inbox.ErrPoisonPill
	}

	if payload.CustomerEmail == "" {
		if payload.CustomerID != "" {
			payload.CustomerEmail = fmt.Sprintf("user-%s@shopflow.io", payload.CustomerID)
		} else {
			payload.CustomerEmail = fmt.Sprintf("customer-%s@shopflow.io", payload.OrderID.String()[:8])
		}
	}

	textBody, htmlBody, err := s.renderer.RenderOrderConfirmed(payload)
	if err != nil {
		s.logger.ErrorContext(ctx, "failed to render order confirmed email template",
			"order_id", payload.OrderID,
			"error", err,
		)
		return fmt.Errorf("render order confirmed: %w", err)
	}

	emailMsg := email.EmailMessage{
		To:       []string{payload.CustomerEmail},
		Subject:  fmt.Sprintf("Order Confirmed: %s", payload.OrderID),
		TextBody: textBody,
		HTMLBody: htmlBody,
	}

	// Strictly dispatched outside database transaction (Architecture Guidelines §6)
	if err := s.sender.Send(ctx, emailMsg); err != nil {
		s.logger.ErrorContext(ctx, "failed to dispatch order confirmed email",
			"order_id", payload.OrderID,
			"recipient", payload.CustomerEmail,
			"error", err,
		)
		return err
	}

	s.logger.InfoContext(ctx, "order confirmed email dispatched successfully",
		"order_id", payload.OrderID,
		"recipient", payload.CustomerEmail,
	)
	return nil
}

func (s *Service) handleOrderCancelled(ctx context.Context, data []byte) error {
	var payload OrderCancelledPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		s.logger.WarnContext(ctx, "failed to unmarshal OrderCancelled payload", "error", err)
		return inbox.ErrPoisonPill
	}

	if payload.CustomerEmail == "" {
		if payload.CustomerID != "" {
			payload.CustomerEmail = fmt.Sprintf("user-%s@shopflow.io", payload.CustomerID)
		} else {
			payload.CustomerEmail = fmt.Sprintf("customer-%s@shopflow.io", payload.OrderID.String()[:8])
		}
	}

	textBody, htmlBody, err := s.renderer.RenderOrderCancelled(payload)
	if err != nil {
		s.logger.ErrorContext(ctx, "failed to render order cancelled email template",
			"order_id", payload.OrderID,
			"error", err,
		)
		return fmt.Errorf("render order cancelled: %w", err)
	}

	emailMsg := email.EmailMessage{
		To:       []string{payload.CustomerEmail},
		Subject:  fmt.Sprintf("Order Cancelled: %s", payload.OrderID),
		TextBody: textBody,
		HTMLBody: htmlBody,
	}

	// Strictly dispatched outside database transaction (Architecture Guidelines §6)
	if err := s.sender.Send(ctx, emailMsg); err != nil {
		s.logger.ErrorContext(ctx, "failed to dispatch order cancelled email",
			"order_id", payload.OrderID,
			"recipient", payload.CustomerEmail,
			"error", err,
		)
		return err
	}

	s.logger.InfoContext(ctx, "order cancelled email dispatched successfully",
		"order_id", payload.OrderID,
		"recipient", payload.CustomerEmail,
	)
	return nil
}
