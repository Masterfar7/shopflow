package saga

// ValidTransitions defines the strict state transition table for the Order Saga FSM.
var ValidTransitions = map[SagaState][]SagaState{
	StateStarted: {
		StateReservingStock,
		StateCompensating,
		StateFailed,
		StateFailedCompensated,
	},
	StatePending: {
		StateReservingStock,
		StateStockReserved,
		StateCompensating,
		StateFailed,
		StateFailedCompensated,
	},
	StateReservingStock: {
		StateStockReserved,
		StateCompensating,
		StateFailed,
		StateFailedCompensated,
	},
	StateStockReserved: {
		StatePaying,
		StateCompensating,
		StateFailed,
		StateFailedCompensated,
	},
	StatePaying: {
		StatePaid,
		StateCompensating,
		StateFailed,
		StateFailedCompensated,
	},
	StatePaid: {
		StateConfirmed,
		StateCompensating,
		StateFailed,
		StateFailedCompensated,
	},
	StateCompensating: {
		StateFailed,
		StateFailedCompensated,
	},
	StateConfirmed:         {}, // Terminal success
	StateFailed:            {}, // Terminal failure
	StateFailedCompensated: {}, // Terminal failure
}

// CanTransition validates whether moving from 'from' to 'to' is allowed.
func CanTransition(from, to SagaState) bool {
	allowed, exists := ValidTransitions[from]
	if !exists {
		return false
	}
	for _, target := range allowed {
		if target == to {
			return true
		}
	}
	return false
}

// IsTerminal returns true if the state represents an irreversible final state.
func IsTerminal(state SagaState) bool {
	return state == StateConfirmed || state == StateFailed || state == StateFailedCompensated
}
