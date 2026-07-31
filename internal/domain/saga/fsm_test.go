package saga

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFSM_ValidTransitions(t *testing.T) {
	tests := []struct {
		name string
		from SagaState
		to   SagaState
		want bool
	}{
		// Happy path edges
		{"started to reserving_stock", StateStarted, StateReservingStock, true},
		{"reserving_stock to stock_reserved", StateReservingStock, StateStockReserved, true},
		{"stock_reserved to paying", StateStockReserved, StatePaying, true},
		{"paying to paid", StatePaying, StatePaid, true},
		{"paid to confirmed", StatePaid, StateConfirmed, true},

		// Failure / Compensation edges
		{"started to failed", StateStarted, StateFailed, true},
		{"reserving_stock to failed", StateReservingStock, StateFailed, true},
		{"stock_reserved to compensating", StateStockReserved, StateCompensating, true},
		{"paying to compensating", StatePaying, StateCompensating, true},
		{"paid to compensating", StatePaid, StateCompensating, true},
		{"compensating to failed", StateCompensating, StateFailed, true},
		{"compensating to failed_compensated", StateCompensating, StateFailedCompensated, true},

		// PENDING support
		{"pending to reserving_stock", StatePending, StateReservingStock, true},
		{"pending to stock_reserved", StatePending, StateStockReserved, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanTransition(tt.from, tt.to)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFSM_IllegalTransitions(t *testing.T) {
	illegalPairs := []struct {
		from SagaState
		to   SagaState
	}{
		{StateStarted, StateConfirmed},
		{StateStarted, StatePaid},
		{StateReservingStock, StatePaid},
		{StateReservingStock, StateConfirmed},
		{StatePaying, StateStarted},
		{StatePaid, StateStarted},
		{StateConfirmed, StateCompensating},
		{StateConfirmed, StateFailed},
		{StateConfirmed, StateStarted},
		{StateFailed, StateStarted},
		{StateFailed, StatePaying},
		{StateFailedCompensated, StateStarted},
		{StateFailedCompensated, StateConfirmed},
	}

	for _, p := range illegalPairs {
		t.Run(string(p.from)+"_to_"+string(p.to), func(t *testing.T) {
			assert.False(t, CanTransition(p.from, p.to), "should not allow transition from %s to %s", p.from, p.to)
		})
	}
}

func TestFSM_TerminalStateImmutability(t *testing.T) {
	allStates := []SagaState{
		StateStarted, StatePending, StateReservingStock, StateStockReserved,
		StatePaying, StatePaid, StateConfirmed, StateCompensating, StateFailed, StateFailedCompensated,
	}

	terminalStates := []SagaState{StateConfirmed, StateFailed, StateFailedCompensated}

	for _, term := range terminalStates {
		assert.True(t, IsTerminal(term), "state %s should be terminal", term)
		for _, target := range allStates {
			assert.False(t, CanTransition(term, target), "terminal state %s must not transition to %s", term, target)
		}
	}

	nonTerminalStates := []SagaState{
		StateStarted, StatePending, StateReservingStock, StateStockReserved, StatePaying, StatePaid, StateCompensating,
	}
	for _, nonTerm := range nonTerminalStates {
		assert.False(t, IsTerminal(nonTerm), "state %s should not be terminal", nonTerm)
	}
}

func TestFSM_PayloadSerialization(t *testing.T) {
	orderID := uuid.New()
	customerID := uuid.New()
	resID := uuid.New()
	payID := uuid.New()

	payload := SagaPayload{
		OrderID:          orderID,
		CustomerID:       customerID,
		TotalAmountMinor: 1499,
		Currency:         "USD",
		Items: []SagaItem{
			{SKU: "SKU-ABC", Quantity: 2, UnitPriceMinor: 500},
			{SKU: "SKU-XYZ", Quantity: 1, UnitPriceMinor: 499},
		},
		ReservationID:    &resID,
		PaymentID:        &payID,
		PaymentToken:     "tok_test_123",
		FailureReason:    "none",
		CompensatingStep: "",
	}

	data, err := payload.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored, err := UnmarshalPayload(data)
	require.NoError(t, err)
	require.NotNil(t, restored)

	assert.Equal(t, orderID, restored.OrderID)
	assert.Equal(t, customerID, restored.CustomerID)
	assert.Equal(t, int64(1499), restored.TotalAmountMinor)
	assert.Equal(t, "USD", restored.Currency)
	assert.Len(t, restored.Items, 2)
	assert.Equal(t, "SKU-ABC", restored.Items[0].SKU)
	assert.Equal(t, 2, restored.Items[0].Quantity)
	assert.Equal(t, int64(500), restored.Items[0].UnitPriceMinor)
	assert.Equal(t, resID, *restored.ReservationID)
	assert.Equal(t, payID, *restored.PaymentID)
	assert.Equal(t, "tok_test_123", restored.PaymentToken)
}

func TestFSM_PayloadSerialization_NilPointers(t *testing.T) {
	payload := SagaPayload{
		OrderID:          uuid.New(),
		CustomerID:       uuid.New(),
		TotalAmountMinor: 500,
		Currency:         "EUR",
		Items:            []SagaItem{},
	}

	data, err := payload.Marshal()
	require.NoError(t, err)

	restored, err := UnmarshalPayload(data)
	require.NoError(t, err)
	assert.Nil(t, restored.ReservationID)
	assert.Nil(t, restored.PaymentID)
	assert.Empty(t, restored.Items)
}
