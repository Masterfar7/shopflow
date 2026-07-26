package order

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"shopflow/internal/domain/catalog"
	"shopflow/internal/platform/database"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Service struct {
	repo           Repository
	catalogReader  catalog.CatalogReader
	beginner       database.TxBeginner
	logger         *slog.Logger
	idempotencyTTL time.Duration
}

func NewService(repo Repository, catalogReader catalog.CatalogReader, beginner database.TxBeginner, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		repo:           repo,
		catalogReader:  catalogReader,
		beginner:       beginner,
		logger:         logger,
		idempotencyTTL: 24 * time.Hour,
	}
}

// SetIdempotencyTTL sets the duration for idempotency key validity.
func (s *Service) SetIdempotencyTTL(ttl time.Duration) {
	s.idempotencyTTL = ttl
}

// ComputeRequestHash generates a deterministic SHA-256 hash of the incoming request body.
func ComputeRequestHash(rawBody []byte) string {
	sum := sha256.Sum256(rawBody)
	return hex.EncodeToString(sum[:])
}

// CreateOrder handles atomic multi-item order placement with price snapshotting and idempotency caching.
// Returns (order, isCached, error).
func (s *Service) CreateOrder(ctx context.Context, userID uuid.UUID, idemKey string, req CreateOrderRequest, rawPayload []byte) (*Order, bool, error) {
	if userID == uuid.Nil {
		return nil, false, ErrUnauthorized
	}
	idemKey = strings.TrimSpace(idemKey)
	if idemKey == "" {
		return nil, false, ErrMissingIdempotencyKey
	}
	if len(req.Items) == 0 {
		return nil, false, ErrEmptyOrderItems
	}
	if len(req.Items) > 100 {
		return nil, false, ErrMaxItemsExceeded
	}

	currency := strings.TrimSpace(req.Currency)
	if currency == "" {
		currency = "USD"
	}

	// 1. Validate items and collect SKUs
	skuMap := make(map[string]int, len(req.Items))
	for _, it := range req.Items {
		sku := strings.TrimSpace(it.SKU)
		if sku == "" {
			return nil, false, errors.New("sku cannot be empty")
		}
		if it.Quantity <= 0 {
			return nil, false, ErrInvalidQuantity
		}
		skuMap[sku] += it.Quantity
	}

	skus := make([]string, 0, len(skuMap))
	for sku := range skuMap {
		skus = append(skus, sku)
	}
	// Deterministic ascending SKU sort prior to querying (Invariant Architecture Guidelines §3.1)
	sort.Strings(skus)

	// 2. Resolve Product Price & Title Snapshots from CatalogReader
	// CARDINAL RULE: Must occur BEFORE starting the DB transaction!
	products, err := s.catalogReader.GetProductsBySKUs(ctx, skus)
	if err != nil {
		return nil, false, fmt.Errorf("catalog reader error: %w", err)
	}

	prodMap := make(map[string]catalog.Product, len(products))
	for _, p := range products {
		prodMap[p.SKU] = p
	}

	// Verify all requested SKUs are active and available
	lineItems := make([]OrderItem, 0, len(skus))
	for _, sku := range skus {
		prod, ok := prodMap[sku]
		if !ok {
			return nil, false, ErrProductNotFound
		}
		if !prod.IsActive {
			return nil, false, ErrProductUnavailable
		}
		if prod.Currency != "" && prod.Currency != currency {
			return nil, false, ErrCurrencyMismatch
		}
		unitPrice := prod.PriceMinor
		if unitPrice <= 0 && prod.Price.Amount > 0 {
			unitPrice = prod.Price.Amount
		}
		if unitPrice <= 0 {
			return nil, false, ErrInvalidPrice
		}

		lineItems = append(lineItems, OrderItem{
			ID:             uuid.New(),
			SKU:            sku,
			TitleSnapshot:  prod.Title,
			UnitPriceMinor: unitPrice,
			Quantity:       skuMap[sku],
		})
	}

	// 3. Compute Server-Side Line Subtotals and Grand Total
	order := &Order{
		ID:             uuid.New(),
		UserID:         userID,
		IdempotencyKey: idemKey,
		Status:         StatusPending,
		Currency:       currency,
		Items:          lineItems,
	}
	if err := order.CalculateTotals(); err != nil {
		return nil, false, err
	}
	for i := range order.Items {
		order.Items[i].OrderID = order.ID
	}

	if len(rawPayload) == 0 {
		rawPayload, _ = json.Marshal(req)
	}
	reqHash := ComputeRequestHash(rawPayload)

	// 4. Atomic Database Transaction (ACID)
	var finalOrder *Order
	var isCached bool

	runInTx := func(fn func(tx pgx.Tx) error) error {
		if s.beginner == nil {
			return fn(nil)
		}
		return database.ExecuteTx(ctx, s.beginner, pgx.TxOptions{}, fn)
	}

	err = runInTx(func(tx pgx.Tx) error {
		// a. Lock & check idempotency key
		idemRec, isNew, lockErr := s.repo.LockIdempotencyKey(ctx, tx, idemKey, userID, reqHash, s.idempotencyTTL)
		if lockErr != nil {
			return lockErr
		}

		// b. If already completed with identical payload: return cached response!
		if !isNew {
			if idemRec.Status == IdempotencyStatusCompleted && len(idemRec.ResponseBody) > 0 {
				var cached Order
				if unmarshalErr := json.Unmarshal(idemRec.ResponseBody, &cached); unmarshalErr == nil {
					finalOrder = &cached
					isCached = true
					return nil
				}
			}
			return ErrConcurrentProcessing
		}

		// c. Prepare Outbox payload
		eventItems := make([]map[string]any, len(order.Items))
		for i, item := range order.Items {
			eventItems[i] = map[string]any{
				"sku":              item.SKU,
				"title_snapshot":   item.TitleSnapshot,
				"unit_price_minor": item.UnitPriceMinor,
				"quantity":         item.Quantity,
				"subtotal_minor":   item.SubtotalMinor,
				"line_total_minor": item.SubtotalMinor,
			}
		}
		outboxPayload := map[string]any{
			"order_id":           order.ID,
			"order_number":       fmt.Sprintf("ORD-%s", order.ID.String()[:8]),
			"customer_id":        order.UserID.String(),
			"saga_id":            order.ID.String(),
			"status":             string(order.Status),
			"total_amount_minor": order.TotalAmountMinor,
			"currency":           order.Currency,
			"items":              eventItems,
			"created_at":         order.CreatedAt,
		}

		// d. Create order, order items and outbox message
		if insErr := s.repo.CreateOrderWithItemsAndOutbox(ctx, tx, order, outboxPayload); insErr != nil {
			return insErr
		}

		// e. Cache response in idempotency_keys
		orderJSON, marshalErr := json.Marshal(order)
		if marshalErr != nil {
			return marshalErr
		}

		if completeErr := s.repo.CompleteIdempotencyKey(ctx, tx, idemKey, userID, 201, orderJSON, order.ID); completeErr != nil {
			return completeErr
		}

		finalOrder = order
		return nil
	})

	if err != nil {
		return nil, false, err
	}

	return finalOrder, isCached, nil
}

func (s *Service) GetOrder(ctx context.Context, orderID, userID uuid.UUID) (*Order, error) {
	if userID == uuid.Nil {
		return nil, ErrUnauthorized
	}
	order, err := s.GetOrderByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.UserID != userID {
		return nil, ErrForbidden
	}
	return order, nil
}

func (s *Service) GetOrderByID(ctx context.Context, orderID uuid.UUID) (*Order, error) {
	order, err := s.repo.GetOrderByID(ctx, orderID)
	if err != nil {
		return nil, err
	}

	items, err := s.repo.GetOrderItems(ctx, orderID)
	if err != nil {
		return nil, err
	}
	order.Items = items
	return order, nil
}

func (s *Service) ListOrders(ctx context.Context, params ListOrdersParams) (*OrderListResponse, error) {
	if params.UserID == uuid.Nil {
		return nil, ErrUnauthorized
	}
	orders, nextCursor, hasMore, err := s.repo.ListOrders(ctx, params)
	if err != nil {
		return nil, err
	}
	return &OrderListResponse{
		Items:      orders,
		NextCursor: nextCursor,
		HasMore:    hasMore,
	}, nil
}

func (s *Service) CancelOrder(ctx context.Context, orderID, userID uuid.UUID, idemKey string, reason string) (*Order, error) {
	if userID == uuid.Nil {
		return nil, ErrUnauthorized
	}
	if strings.TrimSpace(reason) == "" {
		return nil, errors.New("cancellation reason cannot be empty")
	}

	order, err := s.GetOrder(ctx, orderID, userID)
	if err != nil {
		return nil, err
	}

	if !order.Status.CanTransitionTo(StatusCancelled) {
		if order.Status == StatusCancelled {
			return order, nil // Idempotent
		}
		return nil, ErrOrderTerminal
	}

	var updatedOrder *Order
	runInTx := func(fn func(tx pgx.Tx) error) error {
		if s.beginner == nil {
			return fn(nil)
		}
		return database.ExecuteTx(ctx, s.beginner, pgx.TxOptions{}, fn)
	}

	err = runInTx(func(tx pgx.Tx) error {
		newVer, transErr := s.repo.UpdateOrderStatus(ctx, tx, orderID, order.Status, StatusCancelled, order.Version)
		if transErr != nil {
			return transErr
		}
		order.Status = StatusCancelled
		order.Version = newVer

		// Outbox notification
		itemsPayload := make([]map[string]any, 0, len(order.Items))
		for _, it := range order.Items {
			itemsPayload = append(itemsPayload, map[string]any{
				"sku":              it.SKU,
				"title":            it.TitleSnapshot,
				"unit_price_minor": it.UnitPriceMinor,
				"quantity":         it.Quantity,
				"subtotal_minor":   it.SubtotalMinor,
			})
		}

		outboxPayload := map[string]any{
			"order_id":           orderID,
			"customer_id":        userID.String(),
			"customer_email":     fmt.Sprintf("user-%s@shopflow.io", userID.String()),
			"total_amount_minor": order.TotalAmountMinor,
			"currency":           order.Currency,
			"reason":             reason,
			"line_items":         itemsPayload,
			"cancelled_at":       time.Now(),
		}
		if outboxErr := s.repo.InsertOutboxMessage(ctx, tx, orderID, "OrderCancelled", outboxPayload); outboxErr != nil {
			return outboxErr
		}

		updatedOrder = order
		return nil
	})
	if err != nil {
		return nil, err
	}

	return updatedOrder, nil
}

func (s *Service) ConfirmOrder(ctx context.Context, orderID uuid.UUID, expectedVersion int64) (*Order, error) {
	order, err := s.GetOrderByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.Status == StatusConfirmed {
		return order, nil // Idempotent
	}
	if !order.Status.CanTransitionTo(StatusConfirmed) {
		return nil, ErrInvalidStateTransition
	}

	var newVer int64
	runInTx := func(fn func(tx pgx.Tx) error) error {
		if s.beginner == nil {
			return fn(nil)
		}
		return database.ExecuteTx(ctx, s.beginner, pgx.TxOptions{}, fn)
	}

	err = runInTx(func(tx pgx.Tx) error {
		var transErr error
		newVer, transErr = s.repo.UpdateOrderStatus(ctx, tx, orderID, order.Status, StatusConfirmed, expectedVersion)
		if transErr != nil {
			return transErr
		}

		statusPayload := map[string]any{
			"order_id":    orderID,
			"from_status": string(order.Status),
			"to_status":   string(StatusConfirmed),
			"version":     newVer,
			"updated_at":  time.Now(),
		}
		if outboxErr := s.repo.InsertOutboxMessage(ctx, tx, orderID, fmt.Sprintf("OrderStatusChanged_%s_to_%s", order.Status, StatusConfirmed), statusPayload); outboxErr != nil {
			return outboxErr
		}

		itemsPayload := make([]map[string]any, 0, len(order.Items))
		for _, it := range order.Items {
			itemsPayload = append(itemsPayload, map[string]any{
				"sku":              it.SKU,
				"title":            it.TitleSnapshot,
				"unit_price_minor": it.UnitPriceMinor,
				"quantity":         it.Quantity,
				"subtotal_minor":   it.SubtotalMinor,
			})
		}

		confirmedPayload := map[string]any{
			"order_id":           orderID,
			"customer_id":        order.UserID.String(),
			"customer_email":     fmt.Sprintf("user-%s@shopflow.io", order.UserID.String()),
			"total_amount_minor": order.TotalAmountMinor,
			"currency":           order.Currency,
			"line_items":         itemsPayload,
			"confirmed_at":       time.Now(),
		}
		return s.repo.InsertOutboxMessage(ctx, tx, orderID, "OrderConfirmed", confirmedPayload)
	})
	if err != nil {
		return nil, err
	}

	order.Status = StatusConfirmed
	order.Version = newVer
	return order, nil
}

func (s *Service) TransitionStatus(ctx context.Context, orderID uuid.UUID, fromStatus, toStatus OrderStatus, expectedVersion int64) (*Order, error) {
	if !fromStatus.CanTransitionTo(toStatus) {
		return nil, ErrInvalidStateTransition
	}

	var newVer int64
	runInTx := func(fn func(tx pgx.Tx) error) error {
		if s.beginner == nil {
			return fn(nil)
		}
		return database.ExecuteTx(ctx, s.beginner, pgx.TxOptions{}, fn)
	}

	err := runInTx(func(tx pgx.Tx) error {
		var transErr error
		newVer, transErr = s.repo.UpdateOrderStatus(ctx, tx, orderID, fromStatus, toStatus, expectedVersion)
		if transErr != nil {
			return transErr
		}

		outboxPayload := map[string]any{
			"order_id":    orderID,
			"from_status": string(fromStatus),
			"to_status":   string(toStatus),
			"version":     newVer,
			"updated_at":  time.Now(),
		}
		return s.repo.InsertOutboxMessage(ctx, tx, orderID, fmt.Sprintf("OrderStatusChanged_%s_to_%s", fromStatus, toStatus), outboxPayload)
	})
	if err != nil {
		return nil, err
	}

	order, err := s.GetOrderByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	order.Status = toStatus
	order.Version = newVer
	return order, nil
}
