package saga

import "errors"

var (
	ErrSagaNotFound           = errors.New("order saga not found")
	ErrSagaAlreadyExists      = errors.New("order saga already exists for this order")
	ErrInvalidStateTransition = errors.New("invalid saga state transition")
	ErrSagaAlreadyTerminal    = errors.New("cannot transition from terminal state")
	ErrTerminalStateImmutable = ErrSagaAlreadyTerminal
	ErrSagaTimeout            = errors.New("order saga timed out")
	ErrSagaTimedOut           = ErrSagaTimeout
	ErrCompensationFailed     = errors.New("saga compensation workflow failed")
	ErrOptimisticLockConflict = errors.New("saga modified by concurrent transaction")
)
