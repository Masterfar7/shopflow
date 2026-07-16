package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a test HTTP client for exercising ShopFlow APIs.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewClient constructs an API client targeting the specified baseURL.
func NewClient(baseURL string, timeoutSec int) *Client {
	if timeoutSec <= 0 {
		timeoutSec = 15
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{
			Timeout: time.Duration(timeoutSec) * time.Second,
		},
	}
}

// ProblemDetails models RFC 7807 problem details payloads.
type ProblemDetails struct {
	Type          string                `json:"type,omitempty"`
	Title         string                `json:"title,omitempty"`
	Status        int                   `json:"status,omitempty"`
	Detail        string                `json:"detail,omitempty"`
	Instance      string                `json:"instance,omitempty"`
	Code          string                `json:"code,omitempty"`
	InvalidParams []ProblemInvalidParam `json:"invalid_params,omitempty"`
}

type ProblemInvalidParam struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Product models
type CreateProductRequest struct {
	SKU         string `json:"sku"`
	Title       string `json:"title"`
	Description string `json:"description"`
	PriceMinor  int64  `json:"price_minor"`
	Currency    string `json:"currency"`
	CategoryID  string `json:"category_id,omitempty"`
}

type ProductResponse struct {
	ID          string    `json:"id"`
	CategoryID  *string   `json:"category_id"`
	SKU         string    `json:"sku"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	PriceMinor  int64     `json:"price_minor"`
	Currency    string    `json:"currency"`
	IsActive    bool      `json:"is_active"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ProductListResponse struct {
	Items      []ProductResponse `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
	HasMore    bool              `json:"has_more"`
}

// Cart models
type CartResponse struct {
	ID        string         `json:"id"`
	UserID    string         `json:"user_id"`
	Status    string         `json:"status"`
	Version   int64          `json:"version"`
	Items     []CartItemResp `json:"items"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type CartItemResp struct {
	ID             string    `json:"id"`
	SKU            string    `json:"sku"`
	Quantity       int       `json:"quantity"`
	UnitPriceMinor int64     `json:"unit_price_minor"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type AddCartItemRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

// Order models
type OrderItemRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

type CreateOrderRequest struct {
	Items    []OrderItemRequest `json:"items"`
	Currency string             `json:"currency"`
}

type OrderResponse struct {
	ID               string          `json:"id"`
	UserID           string          `json:"user_id"`
	IdempotencyKey   string          `json:"idempotency_key"`
	Status           string          `json:"status"`
	TotalAmountMinor int64           `json:"total_amount_minor"`
	Currency         string          `json:"currency"`
	Version          int64           `json:"version"`
	Items            []OrderItemResp `json:"items"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type OrderItemResp struct {
	ID             string `json:"id"`
	SKU            string `json:"sku"`
	TitleSnapshot  string `json:"title_snapshot"`
	UnitPriceMinor int64  `json:"unit_price_minor"`
	Quantity       int    `json:"quantity"`
	SubtotalMinor  int64  `json:"subtotal_minor"`
}

// Inventory models
type InventoryResponse struct {
	SKU       string    `json:"sku"`
	OnHand    int       `json:"on_hand"`
	Reserved  int       `json:"reserved"`
	Available int       `json:"available"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ReplenishStockRequest struct {
	Quantity int `json:"quantity"`
}

// Payment models
type PaymentSimulateRequest struct {
	OrderID     string `json:"order_id"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Trigger     string `json:"trigger,omitempty"` // "FORCE_SUCCESS", "FORCE_FAILURE", etc.
}

type PaymentResponse struct {
	ID             string    `json:"id"`
	OrderID        string    `json:"order_id"`
	UserID         string    `json:"user_id"`
	IdempotencyKey string    `json:"idempotency_key"`
	AmountMinor    int64     `json:"amount_minor"`
	Currency       string    `json:"currency"`
	Provider       string    `json:"provider"`
	Status         string    `json:"status"`
	ErrorCode      *string   `json:"error_code,omitempty"`
	FailureReason  *string   `json:"failure_reason,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type RefundPaymentRequest struct {
	AmountMinor int64  `json:"amount_minor"`
	Reason      string `json:"reason"`
}

type RefundResponse struct {
	ID          string    `json:"id"`
	PaymentID   string    `json:"payment_id"`
	OrderID     string    `json:"order_id"`
	AmountMinor int64     `json:"amount_minor"`
	Reason      string    `json:"reason"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Observability models
type HealthReadyResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// SendRaw dispatches an HTTP request with arbitrary payload, headers, and method.
func (c *Client) SendRaw(ctx context.Context, method, path string, body []byte, headers map[string]string) (int, []byte, http.Header, error) {
	endpoint := c.BaseURL + path
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, resp.Header, fmt.Errorf("failed to read response body: %w", err)
	}

	return resp.StatusCode, respBytes, resp.Header, nil
}

// CreateProduct creates a product SKU via POST /api/v1/products.
func (c *Client) CreateProduct(ctx context.Context, req CreateProductRequest, idemKey string) (*ProductResponse, *ProblemDetails, int, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, nil, 0, err
	}
	headers := map[string]string{
		"Content-Type": "application/json",
	}
	if idemKey != "" {
		headers["Idempotency-Key"] = idemKey
	}

	status, respBytes, _, err := c.SendRaw(ctx, http.MethodPost, "/api/v1/products", b, headers)
	if err != nil {
		return nil, nil, status, err
	}

	if status == http.StatusCreated || status == http.StatusOK {
		var prod ProductResponse
		if err := json.Unmarshal(respBytes, &prod); err != nil {
			return nil, nil, status, fmt.Errorf("failed to decode product: %w", err)
		}
		return &prod, nil, status, nil
	}

	var prob ProblemDetails
	_ = json.Unmarshal(respBytes, &prob)
	return nil, &prob, status, nil
}

// GetProduct retrieves a product by ID via GET /api/v1/products/{id}.
func (c *Client) GetProduct(ctx context.Context, id string) (*ProductResponse, *ProblemDetails, int, error) {
	status, respBytes, _, err := c.SendRaw(ctx, http.MethodGet, "/api/v1/products/"+id, nil, nil)
	if err != nil {
		return nil, nil, status, err
	}

	if status == http.StatusOK {
		var prod ProductResponse
		if err := json.Unmarshal(respBytes, &prod); err != nil {
			return nil, nil, status, fmt.Errorf("failed to decode product: %w", err)
		}
		return &prod, nil, status, nil
	}

	var prob ProblemDetails
	_ = json.Unmarshal(respBytes, &prob)
	return nil, &prob, status, nil
}

// ListProducts lists products via GET /api/v1/products.
func (c *Client) ListProducts(ctx context.Context, limit int, cursor string) (*ProductListResponse, *ProblemDetails, int, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}

	path := "/api/v1/products"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	status, respBytes, _, err := c.SendRaw(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, nil, status, err
	}

	if status == http.StatusOK {
		var list ProductListResponse
		if err := json.Unmarshal(respBytes, &list); err != nil {
			return nil, nil, status, fmt.Errorf("failed to decode product list: %w", err)
		}
		return &list, nil, status, nil
	}

	var prob ProblemDetails
	_ = json.Unmarshal(respBytes, &prob)
	return nil, &prob, status, nil
}

// CreateCart creates an active cart for a user via POST /api/v1/carts.
func (c *Client) CreateCart(ctx context.Context, userID string) (*CartResponse, *ProblemDetails, int, error) {
	headers := map[string]string{
		"Content-Type": "application/json",
		"X-User-ID":    userID,
	}
	status, respBytes, _, err := c.SendRaw(ctx, http.MethodPost, "/api/v1/carts", []byte("{}"), headers)
	if err != nil {
		return nil, nil, status, err
	}

	if status == http.StatusCreated || status == http.StatusOK {
		var cart CartResponse
		if err := json.Unmarshal(respBytes, &cart); err != nil {
			return nil, nil, status, fmt.Errorf("failed to decode cart: %w", err)
		}
		return &cart, nil, status, nil
	}

	var prob ProblemDetails
	_ = json.Unmarshal(respBytes, &prob)
	return nil, &prob, status, nil
}

// AddCartItem adds or updates a line item in a cart via POST /api/v1/carts/{id}/items.
func (c *Client) AddCartItem(ctx context.Context, cartID, userID string, req AddCartItemRequest, ifMatch string) (*CartResponse, *ProblemDetails, int, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, nil, 0, err
	}
	headers := map[string]string{
		"Content-Type": "application/json",
		"X-User-ID":    userID,
	}
	if ifMatch != "" {
		headers["If-Match"] = ifMatch
	}

	status, respBytes, _, err := c.SendRaw(ctx, http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/items", cartID), b, headers)
	if err != nil {
		return nil, nil, status, err
	}

	if status == http.StatusOK || status == http.StatusCreated {
		var cart CartResponse
		if err := json.Unmarshal(respBytes, &cart); err != nil {
			return nil, nil, status, fmt.Errorf("failed to decode cart: %w", err)
		}
		return &cart, nil, status, nil
	}

	var prob ProblemDetails
	_ = json.Unmarshal(respBytes, &prob)
	return nil, &prob, status, nil
}

// CheckoutCart converts an active cart into a pending order via POST /api/v1/carts/{id}/checkout.
func (c *Client) CheckoutCart(ctx context.Context, cartID, userID, idemKey string) (*OrderResponse, *ProblemDetails, int, error) {
	headers := map[string]string{
		"Content-Type": "application/json",
		"X-User-ID":    userID,
	}
	if idemKey != "" {
		headers["Idempotency-Key"] = idemKey
	}

	status, respBytes, _, err := c.SendRaw(ctx, http.MethodPost, fmt.Sprintf("/api/v1/carts/%s/checkout", cartID), []byte("{}"), headers)
	if err != nil {
		return nil, nil, status, err
	}

	if status == http.StatusCreated || status == http.StatusOK {
		var order OrderResponse
		if err := json.Unmarshal(respBytes, &order); err != nil {
			return nil, nil, status, fmt.Errorf("failed to decode order: %w", err)
		}
		return &order, nil, status, nil
	}

	var prob ProblemDetails
	_ = json.Unmarshal(respBytes, &prob)
	return nil, &prob, status, nil
}

// CreateOrder directly creates a multi-item order via POST /api/v1/orders.
func (c *Client) CreateOrder(ctx context.Context, userID string, req CreateOrderRequest, idemKey string) (*OrderResponse, *ProblemDetails, int, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, nil, 0, err
	}
	headers := map[string]string{
		"Content-Type": "application/json",
		"X-User-ID":    userID,
	}
	if idemKey != "" {
		headers["Idempotency-Key"] = idemKey
	}

	status, respBytes, _, err := c.SendRaw(ctx, http.MethodPost, "/api/v1/orders", b, headers)
	if err != nil {
		return nil, nil, status, err
	}

	if status == http.StatusCreated || status == http.StatusOK {
		var order OrderResponse
		if err := json.Unmarshal(respBytes, &order); err != nil {
			return nil, nil, status, fmt.Errorf("failed to decode order: %w", err)
		}
		return &order, nil, status, nil
	}

	var prob ProblemDetails
	_ = json.Unmarshal(respBytes, &prob)
	return nil, &prob, status, nil
}

// GetOrder retrieves an order by ID via GET /api/v1/orders/{id}.
func (c *Client) GetOrder(ctx context.Context, orderID, userID string) (*OrderResponse, *ProblemDetails, int, error) {
	headers := map[string]string{
		"X-User-ID": userID,
	}
	status, respBytes, _, err := c.SendRaw(ctx, http.MethodGet, "/api/v1/orders/"+orderID, nil, headers)
	if err != nil {
		return nil, nil, status, err
	}

	if status == http.StatusOK {
		var order OrderResponse
		if err := json.Unmarshal(respBytes, &order); err != nil {
			return nil, nil, status, fmt.Errorf("failed to decode order: %w", err)
		}
		return &order, nil, status, nil
	}

	var prob ProblemDetails
	_ = json.Unmarshal(respBytes, &prob)
	return nil, &prob, status, nil
}

// GetInventory queries current stock level for a SKU via GET /api/v1/inventory/{sku}.
func (c *Client) GetInventory(ctx context.Context, sku string) (*InventoryResponse, *ProblemDetails, int, error) {
	status, respBytes, _, err := c.SendRaw(ctx, http.MethodGet, "/api/v1/inventory/"+sku, nil, nil)
	if err != nil {
		return nil, nil, status, err
	}

	if status == http.StatusOK {
		var inv InventoryResponse
		if err := json.Unmarshal(respBytes, &inv); err != nil {
			return nil, nil, status, fmt.Errorf("failed to decode inventory: %w", err)
		}
		return &inv, nil, status, nil
	}

	var prob ProblemDetails
	_ = json.Unmarshal(respBytes, &prob)
	return nil, &prob, status, nil
}

// ReplenishStock replenishes on-hand stock for a SKU via POST /api/v1/inventory/{sku}/replenish.
func (c *Client) ReplenishStock(ctx context.Context, sku string, qty int) (*InventoryResponse, *ProblemDetails, int, error) {
	req := ReplenishStockRequest{Quantity: qty}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, nil, 0, err
	}
	headers := map[string]string{
		"Content-Type": "application/json",
	}

	status, respBytes, _, err := c.SendRaw(ctx, http.MethodPost, fmt.Sprintf("/api/v1/inventory/%s/replenish", sku), b, headers)
	if err != nil {
		return nil, nil, status, err
	}

	if status == http.StatusOK {
		var inv InventoryResponse
		if err := json.Unmarshal(respBytes, &inv); err != nil {
			return nil, nil, status, fmt.Errorf("failed to decode inventory: %w", err)
		}
		return &inv, nil, status, nil
	}

	var prob ProblemDetails
	_ = json.Unmarshal(respBytes, &prob)
	return nil, &prob, status, nil
}

// SimulatePayment executes payment simulation via POST /api/v1/payments/simulate.
func (c *Client) SimulatePayment(ctx context.Context, userID string, req PaymentSimulateRequest, idemKey string) (*PaymentResponse, *ProblemDetails, int, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, nil, 0, err
	}
	headers := map[string]string{
		"Content-Type": "application/json",
		"X-User-ID":    userID,
	}
	if idemKey != "" {
		headers["Idempotency-Key"] = idemKey
	}

	status, respBytes, _, err := c.SendRaw(ctx, http.MethodPost, "/api/v1/payments/simulate", b, headers)
	if err != nil {
		return nil, nil, status, err
	}

	if status == http.StatusOK || status == http.StatusCreated {
		var pay PaymentResponse
		if err := json.Unmarshal(respBytes, &pay); err != nil {
			return nil, nil, status, fmt.Errorf("failed to decode payment response: %w", err)
		}
		return &pay, nil, status, nil
	}

	var prob ProblemDetails
	_ = json.Unmarshal(respBytes, &prob)
	return nil, &prob, status, nil
}

// RefundPayment triggers a refund for a payment via POST /api/v1/payments/{id}/refund.
func (c *Client) RefundPayment(ctx context.Context, paymentID, userID string, req RefundPaymentRequest, idemKey string) (*RefundResponse, *ProblemDetails, int, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, nil, 0, err
	}
	headers := map[string]string{
		"Content-Type": "application/json",
		"X-User-ID":    userID,
	}
	if idemKey != "" {
		headers["Idempotency-Key"] = idemKey
	}

	status, respBytes, _, err := c.SendRaw(ctx, http.MethodPost, fmt.Sprintf("/api/v1/payments/%s/refund", paymentID), b, headers)
	if err != nil {
		return nil, nil, status, err
	}

	if status == http.StatusOK || status == http.StatusCreated {
		var ref RefundResponse
		if err := json.Unmarshal(respBytes, &ref); err != nil {
			return nil, nil, status, fmt.Errorf("failed to decode refund response: %w", err)
		}
		return &ref, nil, status, nil
	}

	var prob ProblemDetails
	_ = json.Unmarshal(respBytes, &prob)
	return nil, &prob, status, nil
}

// GetHealthLive queries GET /health/live.
func (c *Client) GetHealthLive(ctx context.Context) (int, []byte, error) {
	status, body, _, err := c.SendRaw(ctx, http.MethodGet, "/health/live", nil, nil)
	return status, body, err
}

// GetHealthReady queries GET /health/ready.
func (c *Client) GetHealthReady(ctx context.Context) (int, *HealthReadyResponse, error) {
	status, body, _, err := c.SendRaw(ctx, http.MethodGet, "/health/ready", nil, nil)
	if err != nil {
		return status, nil, err
	}
	var ready HealthReadyResponse
	_ = json.Unmarshal(body, &ready)
	return status, &ready, nil
}

// GetMetrics queries GET /metrics.
func (c *Client) GetMetrics(ctx context.Context) (int, string, error) {
	status, body, _, err := c.SendRaw(ctx, http.MethodGet, "/metrics", nil, nil)
	return status, string(body), err
}
