package inventory

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"shopflow/internal/platform/database"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type InventoryService interface {
	GetStock(ctx context.Context, sku string) (*Item, error)
	ReplenishStock(ctx context.Context, sku string, quantity int, referenceID string) (*Item, error)
	ReserveStock(ctx context.Context, req ReserveStockRequest) (*ReserveStockResult, error)
	CommitStock(ctx context.Context, reservationID uuid.UUID) error
	CommitStockByOrderID(ctx context.Context, orderID uuid.UUID) error
	ReleaseStock(ctx context.Context, reservationID uuid.UUID, reason string) error
	ReleaseStockByOrderID(ctx context.Context, orderID uuid.UUID, reason string) error
}

type Service struct {
	repo     Repository
	beginner database.TxBeginner
	logger   *slog.Logger
}

func NewService(repo Repository, beginner database.TxBeginner, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		repo:     repo,
		beginner: beginner,
		logger:   logger,
	}
}

func (s *Service) GetStock(ctx context.Context, sku string) (*Item, error) {
	trimmed := strings.TrimSpace(sku)
	if trimmed == "" {
		return nil, ErrEmptySKU
	}
	return s.repo.GetStock(ctx, trimmed)
}

func (s *Service) ReplenishStock(ctx context.Context, sku string, quantity int, referenceID string) (*Item, error) {
	if quantity <= 0 {
		return nil, ErrInvalidQuantity
	}
	trimmed := strings.TrimSpace(sku)
	if trimmed == "" {
		return nil, ErrEmptySKU
	}

	item, err := s.repo.UpsertStock(ctx, trimmed, quantity)
	if err != nil {
		return nil, err
	}

	s.logger.Info("stock replenished", "sku", trimmed, "qty", quantity, "ref", referenceID)
	return item, nil
}

func (s *Service) ReserveStock(ctx context.Context, req ReserveStockRequest) (*ReserveStockResult, error) {
	if req.OrderID == uuid.Nil {
		return nil, ErrInvalidOrderID
	}
	if len(req.Items) == 0 {
		return nil, ErrEmptyItems
	}
	if req.ReservationID == uuid.Nil {
		req.ReservationID = uuid.New()
	}
	if req.TTLSeconds <= 0 {
		req.TTLSeconds = 900 // default 15 minutes
	}

	// 1. Input Normalization & SKU Consolidation
	consolidated := make(map[string]int)
	for _, item := range req.Items {
		sku := strings.TrimSpace(item.SKU)
		if sku == "" {
			return nil, ErrEmptySKU
		}
		if item.Quantity <= 0 {
			return nil, ErrInvalidQuantity
		}
		consolidated[sku] += item.Quantity
	}

	// 2. Extract unique SKUs and enforce deterministic ascending sort
	sortedSKUs := make([]string, 0, len(consolidated))
	for sku := range consolidated {
		sortedSKUs = append(sortedSKUs, sku)
	}
	sort.Strings(sortedSKUs) // Invariant 3.1: Lexicographical ascending sort

	var result *ReserveStockResult

	// 3. Atomic Transaction Execution
	err := database.ExecuteTx(ctx, s.beginner, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Step A: Deterministic Ascending Row Locking
		lockedItems, err := s.repo.LockSKUsForUpdate(ctx, tx, sortedSKUs)
		if err != nil {
			return err
		}

		// Step B: SKU Existence & Conservation Verification
		var failedSKUs []string
		shortages := make(map[string]StockShortage)
		for _, sku := range sortedSKUs {
			reqQty := consolidated[sku]
			item, exists := lockedItems[sku]
			if !exists {
				failedSKUs = append(failedSKUs, sku)
				shortages[sku] = StockShortage{
					Requested: reqQty,
					Available: 0,
					OnHand:    0,
					Reserved:  0,
				}
				continue
			}

			avail := item.OnHand - item.Reserved
			if avail < reqQty {
				failedSKUs = append(failedSKUs, sku)
				shortages[sku] = StockShortage{
					Requested: reqQty,
					Available: avail,
					OnHand:    item.OnHand,
					Reserved:  item.Reserved,
				}
			}
		}

		// Step C: All-or-Nothing Abort on Shortage
		if len(failedSKUs) > 0 {
			sort.Strings(failedSKUs)
			return &ErrInsufficientStock{
				FailedSKUs: failedSKUs,
				Details:    shortages,
			}
		}

		// Step D: Reserve Inventory Rows (in ascending order)
		for _, sku := range sortedSKUs {
			if err := s.repo.IncrementReserved(ctx, tx, sku, consolidated[sku]); err != nil {
				return err
			}
		}

		// Step E: Record Reservation Entity
		expiresAt := time.Now().Add(time.Duration(req.TTLSeconds) * time.Second)
		res := &Reservation{
			ID:        req.ReservationID,
			OrderID:   req.OrderID,
			Status:    ReservationStatusPending,
			ExpiresAt: expiresAt,
		}
		if err := s.repo.CreateReservation(ctx, tx, res); err != nil {
			return err
		}

		// Step F: Record Reservation Items (in sorted order)
		itemsToCreate := make([]StockItemRequest, 0, len(sortedSKUs))
		for _, sku := range sortedSKUs {
			itemsToCreate = append(itemsToCreate, StockItemRequest{
				SKU:      sku,
				Quantity: consolidated[sku],
			})
		}
		if err := s.repo.CreateReservationItems(ctx, tx, req.ReservationID, itemsToCreate); err != nil {
			return err
		}

		result = &ReserveStockResult{
			ReservationID: req.ReservationID,
			OrderID:       req.OrderID,
			Status:        ReservationStatusPending,
			ExpiresAt:     expiresAt,
			Items:         itemsToCreate,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return result, nil
}

func (s *Service) CommitStock(ctx context.Context, reservationID uuid.UUID) error {
	return database.ExecuteTx(ctx, s.beginner, pgx.TxOptions{}, func(tx pgx.Tx) error {
		res, err := s.repo.LockReservationForUpdate(ctx, tx, reservationID)
		if err != nil {
			return err
		}

		if res.Status == ReservationStatusCommitted {
			// Idempotent success
			return nil
		}
		if res.Status == ReservationStatusReleased {
			return ErrReservationAlreadyReleased
		}

		items, err := s.repo.GetReservationItemsTx(ctx, tx, reservationID)
		if err != nil {
			return err
		}

		// Extract and sort SKUs for deterministic ascending lock order
		skus := make([]string, len(items))
		for i, item := range items {
			skus[i] = item.SKU
		}
		sort.Strings(skus)

		if _, err := s.repo.LockSKUsForUpdate(ctx, tx, skus); err != nil {
			return err
		}

		for _, item := range items {
			if err := s.repo.CommitStockOnHand(ctx, tx, item.SKU, item.Quantity); err != nil {
				return err
			}
		}

		return s.repo.UpdateReservationStatus(ctx, tx, reservationID, ReservationStatusCommitted)
	})
}

func (s *Service) CommitStockByOrderID(ctx context.Context, orderID uuid.UUID) error {
	return database.ExecuteTx(ctx, s.beginner, pgx.TxOptions{}, func(tx pgx.Tx) error {
		res, err := s.repo.LockReservationByOrderForUpdate(ctx, tx, orderID)
		if err != nil {
			return err
		}

		if res.Status == ReservationStatusCommitted {
			// Idempotent success
			return nil
		}
		if res.Status == ReservationStatusReleased {
			return ErrReservationAlreadyReleased
		}

		items, err := s.repo.GetReservationItemsTx(ctx, tx, res.ID)
		if err != nil {
			return err
		}

		// Extract and sort SKUs for deterministic ascending lock order
		skus := make([]string, len(items))
		for i, item := range items {
			skus[i] = item.SKU
		}
		sort.Strings(skus)

		if _, err := s.repo.LockSKUsForUpdate(ctx, tx, skus); err != nil {
			return err
		}

		for _, item := range items {
			if err := s.repo.CommitStockOnHand(ctx, tx, item.SKU, item.Quantity); err != nil {
				return err
			}
		}

		return s.repo.UpdateReservationStatus(ctx, tx, res.ID, ReservationStatusCommitted)
	})
}

func (s *Service) ReleaseStock(ctx context.Context, reservationID uuid.UUID, reason string) error {
	return database.ExecuteTx(ctx, s.beginner, pgx.TxOptions{}, func(tx pgx.Tx) error {
		res, err := s.repo.LockReservationForUpdate(ctx, tx, reservationID)
		if err != nil {
			return err
		}

		if res.Status == ReservationStatusReleased {
			// Idempotent success
			return nil
		}
		if res.Status == ReservationStatusCommitted {
			return ErrReservationAlreadyCommitted
		}

		items, err := s.repo.GetReservationItemsTx(ctx, tx, reservationID)
		if err != nil {
			return err
		}

		// Extract and sort SKUs for deterministic ascending lock order
		skus := make([]string, len(items))
		for i, item := range items {
			skus[i] = item.SKU
		}
		sort.Strings(skus)

		if _, err := s.repo.LockSKUsForUpdate(ctx, tx, skus); err != nil {
			return err
		}

		for _, item := range items {
			if err := s.repo.DecrementReserved(ctx, tx, item.SKU, item.Quantity); err != nil {
				return err
			}
		}

		s.logger.Info("stock reservation released", "reservation_id", reservationID, "reason", reason)
		return s.repo.UpdateReservationStatus(ctx, tx, reservationID, ReservationStatusReleased)
	})
}

func (s *Service) ReleaseStockByOrderID(ctx context.Context, orderID uuid.UUID, reason string) error {
	return database.ExecuteTx(ctx, s.beginner, pgx.TxOptions{}, func(tx pgx.Tx) error {
		res, err := s.repo.LockReservationByOrderForUpdate(ctx, tx, orderID)
		if err != nil {
			return err
		}

		if res.Status == ReservationStatusReleased {
			// Idempotent success
			return nil
		}
		if res.Status == ReservationStatusCommitted {
			return ErrReservationAlreadyCommitted
		}

		items, err := s.repo.GetReservationItemsTx(ctx, tx, res.ID)
		if err != nil {
			return err
		}

		// Extract and sort SKUs for deterministic ascending lock order
		skus := make([]string, len(items))
		for i, item := range items {
			skus[i] = item.SKU
		}
		sort.Strings(skus)

		if _, err := s.repo.LockSKUsForUpdate(ctx, tx, skus); err != nil {
			return err
		}

		for _, item := range items {
			if err := s.repo.DecrementReserved(ctx, tx, item.SKU, item.Quantity); err != nil {
				return err
			}
		}

		s.logger.Info("stock reservation released by order id", "order_id", orderID, "reservation_id", res.ID, "reason", reason)
		return s.repo.UpdateReservationStatus(ctx, tx, res.ID, ReservationStatusReleased)
	})
}
