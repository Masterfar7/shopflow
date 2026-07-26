package order

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"shopflow/internal/platform/web"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	service *Service
	logger  *slog.Logger
}

func NewHandler(service *Service, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		service: service,
		logger:  logger,
	}
}

// Routes mounts all order routes under /orders or at root.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.createOrder)
	r.Get("/", h.listOrders)
	r.Get("/{order_id}", h.getOrder)
	r.Get("/{id}", h.getOrder)
	r.Post("/{order_id}/cancel", h.cancelOrder)
	r.Post("/{id}/cancel", h.cancelOrder)

	// Also support sub-mount /orders if mounted at root
	r.Mount("/orders", h.OrderRoutes())
	return r
}

// OrderRoutes returns sub-routes for /orders.
func (h *Handler) OrderRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.createOrder)
	r.Get("/", h.listOrders)
	r.Get("/{order_id}", h.getOrder)
	r.Get("/{id}", h.getOrder)
	r.Post("/{order_id}/cancel", h.cancelOrder)
	r.Post("/{id}/cancel", h.cancelOrder)
	return r
}

func (h *Handler) extractUserID(r *http.Request) (uuid.UUID, error) {
	raw := strings.TrimSpace(r.Header.Get("X-User-ID"))
	if raw == "" {
		return uuid.Nil, ErrUnauthorized
	}
	u, err := uuid.Parse(raw)
	if err != nil || u == uuid.Nil {
		return uuid.Nil, ErrUnauthorized
	}
	return u, nil
}

func (h *Handler) extractOrderID(r *http.Request) (uuid.UUID, error) {
	idStr := chi.URLParam(r, "order_id")
	if idStr == "" {
		idStr = chi.URLParam(r, "id")
	}
	if idStr == "" {
		return uuid.Nil, errors.New("missing order ID in URL")
	}
	u, err := uuid.Parse(idStr)
	if err != nil || u == uuid.Nil {
		return uuid.Nil, errors.New("invalid order ID format")
	}
	return u, nil
}

func (h *Handler) createOrder(w http.ResponseWriter, r *http.Request) {
	userID, err := h.extractUserID(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "A valid X-User-ID header is required")
		return
	}

	idemKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idemKey == "" {
		web.RespondProblem(w, r, http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY", "Missing Idempotency-Key", "Idempotency-Key header is required for order creation")
		return
	}

	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid Request Body", "Failed to read request body")
		return
	}
	r.Body = io.NopCloser(bytes.NewBuffer(rawBody))

	var req CreateOrderRequest
	if err := web.DecodeJSON(r, &req); err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid Request Body", err.Error())
		return
	}

	order, isCached, err := h.service.CreateOrder(r.Context(), userID, idemKey, req, rawBody)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnauthorized):
			web.RespondProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", err.Error())
		case errors.Is(err, ErrMissingIdempotencyKey):
			web.RespondProblem(w, r, http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY", "Missing Idempotency-Key", err.Error())
		case errors.Is(err, ErrIdempotencyConflict):
			web.RespondProblem(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency Conflict", "Idempotency key was previously used with a different request payload")
		case errors.Is(err, ErrConcurrentProcessing):
			web.RespondProblem(w, r, http.StatusConflict, "CONCURRENT_REQUEST", "Concurrent Request In Progress", "A request with this idempotency key is currently processing")
		case errors.Is(err, ErrEmptyOrderItems):
			web.RespondProblem(w, r, http.StatusBadRequest, "EMPTY_ORDER_ITEMS", "Empty Order Items", "Order must contain at least one line item")
		case errors.Is(err, ErrMaxItemsExceeded):
			web.RespondProblem(w, r, http.StatusBadRequest, "MAX_ITEMS_EXCEEDED", "Max Items Exceeded", "Order exceeds maximum allowed line items (100)")
		case errors.Is(err, ErrInvalidQuantity):
			web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_QUANTITY", "Invalid Quantity", "Item quantity must be greater than zero")
		case errors.Is(err, ErrInvalidPrice):
			web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_PRICE", "Invalid Price", "Product unit price must be positive")
		case errors.Is(err, ErrCurrencyMismatch):
			web.RespondProblem(w, r, http.StatusBadRequest, "CURRENCY_MISMATCH", "Currency Mismatch", "Currency does not match catalog currency")
		case errors.Is(err, ErrProductNotFound):
			web.RespondProblem(w, r, http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product Not Found", "One or more requested SKUs were not found in catalog")
		case errors.Is(err, ErrProductUnavailable):
			web.RespondProblem(w, r, http.StatusBadRequest, "PRODUCT_UNAVAILABLE", "Product Unavailable", "One or more requested SKUs are inactive")
		case errors.Is(err, ErrArithmeticOverflow):
			web.RespondProblem(w, r, http.StatusBadRequest, "ARITHMETIC_OVERFLOW", "Arithmetic Overflow", "Monetary arithmetic overflow detected")
		default:
			h.logger.Error("failed to create order", "err", err, "user_id", userID)
			web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to place order")
		}
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/api/v1/orders/%s", order.ID))
	statusCode := http.StatusCreated
	if isCached {
		statusCode = http.StatusOK
	}
	web.RespondJSON(w, statusCode, order)
}

func (h *Handler) getOrder(w http.ResponseWriter, r *http.Request) {
	orderID, err := h.extractOrderID(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "Order ID must be a valid UUID")
		return
	}

	userID, err := h.extractUserID(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "A valid X-User-ID header is required")
		return
	}

	order, err := h.service.GetOrder(r.Context(), orderID, userID)
	if err != nil {
		switch {
		case errors.Is(err, ErrOrderNotFound):
			web.RespondProblem(w, r, http.StatusNotFound, "ORDER_NOT_FOUND", "Order Not Found", fmt.Sprintf("Order %s not found", orderID))
		case errors.Is(err, ErrForbidden):
			web.RespondProblem(w, r, http.StatusForbidden, "FORBIDDEN", "Forbidden", "Customer does not own this order")
		default:
			h.logger.Error("failed to get order", "err", err, "order_id", orderID)
			web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to retrieve order")
		}
		return
	}

	web.RespondJSON(w, http.StatusOK, order)
}

func (h *Handler) listOrders(w http.ResponseWriter, r *http.Request) {
	userID, err := h.extractUserID(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "A valid X-User-ID header is required")
		return
	}

	params := ListOrdersParams{UserID: userID}

	if statusQuery := strings.TrimSpace(r.URL.Query().Get("status")); statusQuery != "" {
		st := OrderStatus(statusQuery)
		params.Status = &st
	}

	if limitQuery := strings.TrimSpace(r.URL.Query().Get("limit")); limitQuery != "" {
		if l, err := strconv.Atoi(limitQuery); err == nil && l > 0 {
			params.Limit = l
		}
	}

	params.Cursor = strings.TrimSpace(r.URL.Query().Get("cursor"))

	resp, err := h.service.ListOrders(r.Context(), params)
	if err != nil {
		h.logger.Error("failed to list orders", "err", err, "user_id", userID)
		web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to list orders")
		return
	}

	web.RespondJSON(w, http.StatusOK, resp)
}

func (h *Handler) cancelOrder(w http.ResponseWriter, r *http.Request) {
	orderID, err := h.extractOrderID(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "Order ID must be a valid UUID")
		return
	}

	userID, err := h.extractUserID(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "A valid X-User-ID header is required")
		return
	}

	idemKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))

	reason := "Customer requested cancellation"
	if r.Body != nil {
		var req CancelOrderRequest
		if err := web.DecodeJSON(r, &req); err == nil && strings.TrimSpace(req.Reason) != "" {
			reason = req.Reason
		}
	}

	order, err := h.service.CancelOrder(r.Context(), orderID, userID, idemKey, reason)
	if err != nil {
		switch {
		case errors.Is(err, ErrOrderNotFound):
			web.RespondProblem(w, r, http.StatusNotFound, "ORDER_NOT_FOUND", "Order Not Found", "Order does not exist")
		case errors.Is(err, ErrForbidden):
			web.RespondProblem(w, r, http.StatusForbidden, "FORBIDDEN", "Forbidden", "Customer does not own this order")
		case errors.Is(err, ErrOrderTerminal):
			web.RespondProblem(w, r, http.StatusConflict, "ORDER_TERMINAL", "Order In Terminal State", "Order is already in a terminal state and cannot be cancelled")
		case errors.Is(err, ErrInvalidStateTransition):
			web.RespondProblem(w, r, http.StatusConflict, "INVALID_STATE_TRANSITION", "Invalid State Transition", err.Error())
		default:
			h.logger.Error("failed to cancel order", "err", err, "order_id", orderID)
			web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to cancel order")
		}
		return
	}

	web.RespondJSON(w, http.StatusOK, CancelOrderResponse{
		OrderID: order.ID,
		Status:  string(order.Status),
		Message: "Order cancelled successfully",
	})
}
