package email

import (
	"context"
	"errors"
)

var (
	ErrInvalidRecipient = errors.New("recipient address cannot be empty")
	ErrEmptySubject     = errors.New("subject cannot be empty")
	ErrEmptyBody        = errors.New("both html and text body cannot be empty")
)

// EmailMessage models an outbound transactional email with plain-text and HTML alternatives.
type EmailMessage struct {
	From     string   `json:"from"`
	To       []string `json:"to"`
	Subject  string   `json:"subject"`
	TextBody string   `json:"text_body"`
	HTMLBody string   `json:"html_body"`
}

// Sender abstracts the transactional email dispatch mechanism.
type Sender interface {
	Send(ctx context.Context, msg EmailMessage) error
}
