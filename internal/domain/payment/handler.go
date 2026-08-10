package payment

import (
	"errors"
	"log/slog"
	"net/http"

	"shopflow/internal/platform/web"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Handler provides HTTP endpoints for payment simulation and refunds.
type Handler struct {
	service *Service
	logger  *slog.Logger
}

// NewHandler constructs a new payment Handler.
func NewHandler(service *Service, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		service: service,
		logger:  logger,
	}
}

// Routes returns the Chi router for payment operations mounted under /payments.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Post("/simulate", h.simulatePayment)
	r.Post("/{payment_id}/refund", h.refundPayment)
	r.Post("/{id}/refund", h.refundPayment)
	r.Get("/{payment_id}", h.getPayment)
	r.Get("/{id}", h.getPayment)

	return r
}

func (h *Handler) simulatePayment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req SimulatePaymentRequest
	if err := web.DecodeJSON(r, &req); err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Bad Request", err.Error())
		return
	}

	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey == "" {
		idemKey = uuid.New().String()
	}

	rawUserID := r.Header.Get("X-User-ID")
	userUUID, err := uuid.Parse(rawUserID)
	if err != nil || userUUID == uuid.Nil {
		userUUID = uuid.New()
	}

	p, err := h.service.AuthorizeAndCapture(
		ctx,
		req.OrderID,
		userUUID,
		req.AmountMinor,
		req.Currency,
		req.Trigger,
		idemKey,
	)

	if err != nil {
		if errors.Is(err, ErrIdempotencyConflict) {
			web.RespondProblem(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Conflict", err.Error())
			return
		}
		if errors.Is(err, ErrInvalidPaymentAmount) {
			web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_AMOUNT", "Bad Request", err.Error())
			return
		}

		// In case of simulated decline or timeout with an aggregate record created:
		if p != nil {
			// Return 200 OK with FAILED status payload so clients can inspect the failure details
			web.RespondJSON(w, http.StatusOK, p.ToResponse())
			return
		}

		web.RespondProblem(w, r, http.StatusInternalServerError, "PAYMENT_ERROR", "Internal Server Error", err.Error())
		return
	}

	web.RespondJSON(w, http.StatusOK, p.ToResponse())
}

func (h *Handler) refundPayment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rawID := chi.URLParam(r, "payment_id")
	if rawID == "" {
		rawID = chi.URLParam(r, "id")
	}

	paymentID, err := uuid.Parse(rawID)
	if err != nil || paymentID == uuid.Nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_ID", "Bad Request", "Invalid payment ID")
		return
	}

	var req RefundPaymentRequest
	if err := web.DecodeJSON(r, &req); err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Bad Request", err.Error())
		return
	}

	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey == "" {
		idemKey = uuid.New().String()
	}

	rawUserID := r.Header.Get("X-User-ID")
	userUUID, err := uuid.Parse(rawUserID)
	if err != nil {
		userUUID = uuid.Nil
	}

	refund, err := h.service.Refund(ctx, paymentID, userUUID, req.AmountMinor, req.Reason, idemKey)
	if err != nil {
		if errors.Is(err, ErrPaymentNotFound) {
			web.RespondProblem(w, r, http.StatusNotFound, "PAYMENT_NOT_FOUND", "Not Found", err.Error())
			return
		}
		if errors.Is(err, ErrPaymentAlreadyRefunded) {
			web.RespondProblem(w, r, http.StatusConflict, "ALREADY_REFUNDED", "Conflict", err.Error())
			return
		}
		if errors.Is(err, ErrInvalidRefundAmount) || errors.Is(err, ErrPaymentCannotBeRefunded) {
			web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_REFUND", "Bad Request", err.Error())
			return
		}
		web.RespondProblem(w, r, http.StatusInternalServerError, "REFUND_ERROR", "Internal Server Error", err.Error())
		return
	}

	web.RespondJSON(w, http.StatusOK, refund.ToResponse())
}

func (h *Handler) getPayment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rawID := chi.URLParam(r, "payment_id")
	if rawID == "" {
		rawID = chi.URLParam(r, "id")
	}

	paymentID, err := uuid.Parse(rawID)
	if err != nil || paymentID == uuid.Nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_ID", "Bad Request", "Invalid payment ID")
		return
	}

	payment, err := h.service.GetPaymentByID(ctx, paymentID)
	if err != nil {
		if errors.Is(err, ErrPaymentNotFound) {
			web.RespondProblem(w, r, http.StatusNotFound, "PAYMENT_NOT_FOUND", "Not Found", "Payment not found")
			return
		}
		web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", err.Error())
		return
	}

	web.RespondJSON(w, http.StatusOK, payment.ToResponse())
}
