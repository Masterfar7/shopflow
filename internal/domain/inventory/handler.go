package inventory

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"shopflow/internal/platform/web"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	service InventoryService
	logger  *slog.Logger
}

func NewHandler(service InventoryService, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		service: service,
		logger:  logger,
	}
}

// Routes returns Chi router mounted under /inventory or /api/v1/inventory
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()

	// Reservation management endpoints
	r.Post("/reserve", h.reserveStock)
	r.Mount("/reservations", h.ReservationRoutes())

	// Stock and replenishment endpoints
	r.Get("/{sku}", h.getInventory)
	r.Post("/{sku}/replenish", h.replenishInventory)
	r.Post("/{sku}/restock", h.replenishInventory)

	return r
}

// ReservationRoutes mounts endpoints under /reservations
func (h *Handler) ReservationRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/reserve", h.reserveStock)
	r.Post("/{id}/commit", h.commitStock)
	r.Post("/{id}/release", h.releaseStock)
	return r
}

func (h *Handler) getInventory(w http.ResponseWriter, r *http.Request) {
	sku := strings.TrimSpace(chi.URLParam(r, "sku"))
	if sku == "" {
		web.RespondProblem(w, r, http.StatusBadRequest, "EMPTY_SKU", "Empty SKU", "SKU cannot be empty")
		return
	}

	item, err := h.service.GetStock(r.Context(), sku)
	if err != nil {
		if errors.Is(err, ErrSKUNotFound) {
			web.RespondProblem(w, r, http.StatusNotFound, "SKU_NOT_FOUND", "SKU Not Found", fmt.Sprintf("SKU %q does not exist", sku))
			return
		}
		h.logger.Error("failed to get inventory", "err", err, "sku", sku)
		web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to retrieve stock")
		return
	}

	web.SetETag(w, item.Version)
	web.RespondJSON(w, http.StatusOK, item)
}

func (h *Handler) replenishInventory(w http.ResponseWriter, r *http.Request) {
	sku := strings.TrimSpace(chi.URLParam(r, "sku"))
	if sku == "" {
		web.RespondProblem(w, r, http.StatusBadRequest, "EMPTY_SKU", "Empty SKU", "SKU cannot be empty")
		return
	}

	var req ReplenishStockRequest
	if err := web.DecodeJSON(r, &req); err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid Request", err.Error())
		return
	}

	if req.Quantity <= 0 {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_QUANTITY", "Invalid Quantity", "Quantity must be greater than zero")
		return
	}

	item, err := h.service.ReplenishStock(r.Context(), sku, req.Quantity, req.ReferenceID)
	if err != nil {
		if errors.Is(err, ErrInvalidQuantity) {
			web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_QUANTITY", "Invalid Quantity", err.Error())
			return
		}
		if errors.Is(err, ErrEmptySKU) {
			web.RespondProblem(w, r, http.StatusBadRequest, "EMPTY_SKU", "Empty SKU", err.Error())
			return
		}
		h.logger.Error("failed to replenish stock", "err", err, "sku", sku)
		web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to replenish stock")
		return
	}

	web.SetETag(w, item.Version)
	web.RespondJSON(w, http.StatusOK, item)
}

func (h *Handler) reserveStock(w http.ResponseWriter, r *http.Request) {
	var req ReserveStockRequest
	if err := web.DecodeJSON(r, &req); err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid Request", err.Error())
		return
	}

	result, err := h.service.ReserveStock(r.Context(), req)
	if err != nil {
		var insufficientErr *ErrInsufficientStock
		if errors.As(err, &insufficientErr) {
			web.RespondProblem(w, r, http.StatusConflict, "INSUFFICIENT_STOCK", "Insufficient Stock", err.Error())
			return
		}
		if errors.Is(err, ErrInvalidQuantity) {
			web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_QUANTITY", "Invalid Quantity", err.Error())
			return
		}
		if errors.Is(err, ErrEmptySKU) {
			web.RespondProblem(w, r, http.StatusBadRequest, "EMPTY_SKU", "Empty SKU", err.Error())
			return
		}
		if errors.Is(err, ErrEmptyItems) {
			web.RespondProblem(w, r, http.StatusBadRequest, "EMPTY_ITEMS", "Empty Items", err.Error())
			return
		}
		if errors.Is(err, ErrInvalidOrderID) {
			web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_ORDER_ID", "Invalid Order ID", err.Error())
			return
		}
		if errors.Is(err, ErrDuplicateReservationID) {
			web.RespondProblem(w, r, http.StatusConflict, "DUPLICATE_RESERVATION", "Duplicate Reservation", err.Error())
			return
		}
		if errors.Is(err, ErrSKUNotFound) {
			web.RespondProblem(w, r, http.StatusNotFound, "SKU_NOT_FOUND", "SKU Not Found", err.Error())
			return
		}
		h.logger.Error("failed to reserve stock", "err", err, "order_id", req.OrderID)
		web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to reserve stock")
		return
	}

	web.RespondJSON(w, http.StatusOK, result)
}

func (h *Handler) commitStock(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	resID, err := uuid.Parse(idStr)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "Reservation ID must be a valid UUID")
		return
	}

	err = h.service.CommitStock(r.Context(), resID)
	if err != nil {
		if errors.Is(err, ErrReservationNotFound) {
			web.RespondProblem(w, r, http.StatusNotFound, "RESERVATION_NOT_FOUND", "Reservation Not Found", err.Error())
			return
		}
		if errors.Is(err, ErrReservationAlreadyReleased) {
			web.RespondProblem(w, r, http.StatusConflict, "RESERVATION_ALREADY_RELEASED", "Reservation Already Released", err.Error())
			return
		}
		h.logger.Error("failed to commit stock", "err", err, "reservation_id", resID)
		web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to commit stock")
		return
	}

	web.RespondJSON(w, http.StatusOK, StatusResponse{Status: ReservationStatusCommitted})
}

func (h *Handler) releaseStock(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	resID, err := uuid.Parse(idStr)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "Reservation ID must be a valid UUID")
		return
	}

	var req ReleaseStockRequest
	if r.Body != nil && r.ContentLength > 0 {
		_ = web.DecodeJSON(r, &req)
	}

	err = h.service.ReleaseStock(r.Context(), resID, req.Reason)
	if err != nil {
		if errors.Is(err, ErrReservationNotFound) {
			web.RespondProblem(w, r, http.StatusNotFound, "RESERVATION_NOT_FOUND", "Reservation Not Found", err.Error())
			return
		}
		if errors.Is(err, ErrReservationAlreadyCommitted) {
			web.RespondProblem(w, r, http.StatusConflict, "RESERVATION_ALREADY_COMMITTED", "Reservation Already Committed", err.Error())
			return
		}
		h.logger.Error("failed to release stock", "err", err, "reservation_id", resID)
		web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to release stock")
		return
	}

	web.RespondJSON(w, http.StatusOK, StatusResponse{Status: ReservationStatusReleased})
}
