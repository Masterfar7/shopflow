package inbox

import "errors"

var (
	// ErrDuplicateMessage indicates message has already been processed or is currently in flight.
	ErrDuplicateMessage = errors.New("inbox message already processed or currently processing")

	// ErrTerminalStateIgnored indicates aggregate is already in a terminal state, so late event is no-oped.
	ErrTerminalStateIgnored = errors.New("aggregate in terminal state; event ignored idempotently")

	// ErrPoisonPill indicates a message is structurally malformed or unrecoverably corrupted.
	ErrPoisonPill = errors.New("poison pill message cannot be processed")

	// ErrMessageNotFound indicates message was not found in inbox repository.
	ErrMessageNotFound = errors.New("inbox message not found")
)
