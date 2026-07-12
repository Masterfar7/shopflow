// internal/domain/cart/handler.go
package cart

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

// Routes mounts all cart routes under /cart (e.g. /cart/carts or direct /cart).
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Mount("/carts", h.CartRoutes())
	// Also allow direct access under /cart
	r.Post("/", h.createCart)
	r.Get("/{cart_id}", h.getCart)
	r.Post("/{cart_id}/items", h.addItem)
	r.Put("/{cart_id}/items/{sku}", h.updateItemQuantity)
	r.Delete("/{cart_id}/items/{sku}", h.removeItem)
	r.Delete("/{cart_id}/clear", h.clearCart)
	return r
}

// CartRoutes mounts REST endpoints for carts under /carts.
func (h *Handler) CartRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.createCart)
	r.Get("/{cart_id}", h.getCart)
	r.Post("/{cart_id}/items", h.addItem)
	r.Put("/{cart_id}/items/{sku}", h.updateItemQuantity)
	r.Delete("/{cart_id}/items/{sku}", h.removeItem)
	r.Delete("/{cart_id}/clear", h.clearCart)
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

func (h *Handler) createCart(w http.ResponseWriter, r *http.Request) {
	var req CreateCartRequest
	_ = web.DecodeJSON(r, &req)

	customerID := req.CustomerID
	if customerID == uuid.Nil {
		var err error
		customerID, err = h.extractUserID(r)
		if err != nil || customerID == uuid.Nil {
			web.RespondProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "A valid customer_id in JSON body or X-User-ID header is required")
			return
		}
	}

	cart, err := h.service.CreateOrGetActiveCart(r.Context(), customerID)
	if err != nil {
		h.logger.Error("failed to create cart", "err", err, "customer_id", customerID)
		web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to create active cart")
		return
	}

	web.SetETag(w, cart.Version)
	web.RespondJSON(w, http.StatusOK, cart)
}

func (h *Handler) getCart(w http.ResponseWriter, r *http.Request) {
	cartIDStr := chi.URLParam(r, "cart_id")
	cartID, err := uuid.Parse(cartIDStr)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "Cart ID must be a valid UUID")
		return
	}

	customerID, err := h.extractUserID(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "A valid X-User-ID header is required")
		return
	}

	cart, err := h.service.GetCart(r.Context(), cartID, customerID)
	if err != nil {
		switch {
		case errors.Is(err, ErrCartNotFound):
			web.RespondProblem(w, r, http.StatusNotFound, "CART_NOT_FOUND", "Cart Not Found", fmt.Sprintf("Cart %s not found", cartIDStr))
		case errors.Is(err, ErrUnauthorized):
			web.RespondProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "A valid X-User-ID header is required")
		case errors.Is(err, ErrForbidden):
			web.RespondProblem(w, r, http.StatusForbidden, "FORBIDDEN", "Forbidden", "Customer does not own this cart")
		default:
			h.logger.Error("failed to get cart", "err", err, "cart_id", cartIDStr)
			web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to retrieve cart")
		}
		return
	}

	web.SetETag(w, cart.Version)
	web.RespondJSON(w, http.StatusOK, cart)
}

func (h *Handler) addItem(w http.ResponseWriter, r *http.Request) {
	cartIDStr := chi.URLParam(r, "cart_id")
	cartID, err := uuid.Parse(cartIDStr)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "Cart ID must be a valid UUID")
		return
	}

	customerID, err := h.extractUserID(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "A valid X-User-ID header is required")
		return
	}

	expectedVersion, err := web.ExtractIfMatch(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "PRECONDITION_REQUIRED", "Precondition Required", "A valid If-Match header is required")
		return
	}

	var req AddCartItemRequest
	if err := web.DecodeJSON(r, &req); err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid Request Body", err.Error())
		return
	}

	cart, err := h.service.AddItem(r.Context(), cartID, customerID, req.SKU, req.Quantity, expectedVersion)
	if err != nil {
		switch {
		case errors.Is(err, ErrOptimisticLockConflict), errors.Is(err, ErrInvalidVersion):
			web.RespondProblem(w, r, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "Precondition Failed", "Cart version does not match If-Match header")
		case errors.Is(err, ErrCartNotFound):
			web.RespondProblem(w, r, http.StatusNotFound, "CART_NOT_FOUND", "Cart Not Found", "Cart does not exist")
		case errors.Is(err, ErrUnauthorized):
			web.RespondProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "A valid X-User-ID header is required")
		case errors.Is(err, ErrForbidden):
			web.RespondProblem(w, r, http.StatusForbidden, "FORBIDDEN", "Forbidden", "Customer does not own this cart")
		case errors.Is(err, ErrCurrencyMismatch):
			web.RespondProblem(w, r, http.StatusBadRequest, "CURRENCY_MISMATCH", "Currency Mismatch", "Item price currency does not match cart currency")
		case errors.Is(err, ErrProductUnavailable):
			web.RespondProblem(w, r, http.StatusBadRequest, "PRODUCT_UNAVAILABLE", "Product Unavailable", "Product SKU is unavailable or inactive")
		case errors.Is(err, ErrInvalidQuantity):
			web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_QUANTITY", "Invalid Quantity", "Quantity must be greater than zero")
		default:
			h.logger.Error("failed to add item to cart", "err", err, "cart_id", cartIDStr)
			web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to add item to cart")
		}
		return
	}

	web.SetETag(w, cart.Version)
	web.RespondJSON(w, http.StatusOK, cart)
}

func (h *Handler) updateItemQuantity(w http.ResponseWriter, r *http.Request) {
	cartIDStr := chi.URLParam(r, "cart_id")
	cartID, err := uuid.Parse(cartIDStr)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "Cart ID must be a valid UUID")
		return
	}
	sku := chi.URLParam(r, "sku")

	customerID, err := h.extractUserID(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "A valid X-User-ID header is required")
		return
	}

	expectedVersion, err := web.ExtractIfMatch(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "PRECONDITION_REQUIRED", "Precondition Required", "A valid If-Match header is required")
		return
	}

	var req UpdateCartItemRequest
	if err := web.DecodeJSON(r, &req); err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid Request Body", err.Error())
		return
	}

	cart, err := h.service.UpdateQuantity(r.Context(), cartID, customerID, sku, req.Quantity, expectedVersion)
	if err != nil {
		switch {
		case errors.Is(err, ErrOptimisticLockConflict), errors.Is(err, ErrInvalidVersion):
			web.RespondProblem(w, r, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "Precondition Failed", "Cart version does not match If-Match header")
		case errors.Is(err, ErrCartNotFound):
			web.RespondProblem(w, r, http.StatusNotFound, "CART_NOT_FOUND", "Cart Not Found", "Cart does not exist")
		case errors.Is(err, ErrItemNotFound):
			web.RespondProblem(w, r, http.StatusNotFound, "ITEM_NOT_FOUND", "Item Not Found", fmt.Sprintf("Item with SKU %s not found in cart", sku))
		case errors.Is(err, ErrUnauthorized):
			web.RespondProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "A valid X-User-ID header is required")
		case errors.Is(err, ErrForbidden):
			web.RespondProblem(w, r, http.StatusForbidden, "FORBIDDEN", "Forbidden", "Customer does not own this cart")
		default:
			h.logger.Error("failed to update cart item quantity", "err", err, "cart_id", cartIDStr, "sku", sku)
			web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to update item quantity")
		}
		return
	}

	web.SetETag(w, cart.Version)
	web.RespondJSON(w, http.StatusOK, cart)
}

func (h *Handler) removeItem(w http.ResponseWriter, r *http.Request) {
	cartIDStr := chi.URLParam(r, "cart_id")
	cartID, err := uuid.Parse(cartIDStr)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "Cart ID must be a valid UUID")
		return
	}
	sku := chi.URLParam(r, "sku")

	customerID, err := h.extractUserID(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "A valid X-User-ID header is required")
		return
	}

	expectedVersion, err := web.ExtractIfMatch(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "PRECONDITION_REQUIRED", "Precondition Required", "A valid If-Match header is required")
		return
	}

	cart, err := h.service.RemoveItem(r.Context(), cartID, customerID, sku, expectedVersion)
	if err != nil {
		switch {
		case errors.Is(err, ErrOptimisticLockConflict), errors.Is(err, ErrInvalidVersion):
			web.RespondProblem(w, r, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "Precondition Failed", "Cart version does not match If-Match header")
		case errors.Is(err, ErrCartNotFound):
			web.RespondProblem(w, r, http.StatusNotFound, "CART_NOT_FOUND", "Cart Not Found", "Cart does not exist")
		case errors.Is(err, ErrItemNotFound):
			web.RespondProblem(w, r, http.StatusNotFound, "ITEM_NOT_FOUND", "Item Not Found", fmt.Sprintf("Item with SKU %s not found in cart", sku))
		case errors.Is(err, ErrUnauthorized):
			web.RespondProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "A valid X-User-ID header is required")
		case errors.Is(err, ErrForbidden):
			web.RespondProblem(w, r, http.StatusForbidden, "FORBIDDEN", "Forbidden", "Customer does not own this cart")
		default:
			h.logger.Error("failed to remove cart item", "err", err, "cart_id", cartIDStr, "sku", sku)
			web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to remove item")
		}
		return
	}

	web.SetETag(w, cart.Version)
	web.RespondJSON(w, http.StatusOK, cart)
}

func (h *Handler) clearCart(w http.ResponseWriter, r *http.Request) {
	cartIDStr := chi.URLParam(r, "cart_id")
	cartID, err := uuid.Parse(cartIDStr)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "INVALID_UUID", "Invalid UUID", "Cart ID must be a valid UUID")
		return
	}

	customerID, err := h.extractUserID(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "A valid X-User-ID header is required")
		return
	}

	expectedVersion, err := web.ExtractIfMatch(r)
	if err != nil {
		web.RespondProblem(w, r, http.StatusBadRequest, "PRECONDITION_REQUIRED", "Precondition Required", "A valid If-Match header is required")
		return
	}

	_, err = h.service.ClearCart(r.Context(), cartID, customerID, expectedVersion)
	if err != nil {
		switch {
		case errors.Is(err, ErrOptimisticLockConflict), errors.Is(err, ErrInvalidVersion):
			web.RespondProblem(w, r, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "Precondition Failed", "Cart version does not match If-Match header")
		case errors.Is(err, ErrCartNotFound):
			web.RespondProblem(w, r, http.StatusNotFound, "CART_NOT_FOUND", "Cart Not Found", "Cart does not exist")
		case errors.Is(err, ErrUnauthorized):
			web.RespondProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", "A valid X-User-ID header is required")
		case errors.Is(err, ErrForbidden):
			web.RespondProblem(w, r, http.StatusForbidden, "FORBIDDEN", "Forbidden", "Customer does not own this cart")
		default:
			h.logger.Error("failed to clear cart", "err", err, "cart_id", cartIDStr)
			web.RespondProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal Server Error", "Failed to clear cart")
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
