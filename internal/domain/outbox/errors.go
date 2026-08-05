package outbox

import "errors"

var (
	ErrMessageNotFound    = errors.New("outbox message not found")
	ErrAlreadyLeased      = errors.New("outbox message already leased by another worker")
	ErrMaxRetriesExceeded = errors.New("outbox message exceeded maximum retry attempts")
	ErrWorkerInactive     = errors.New("outbox poller is not active")
)
