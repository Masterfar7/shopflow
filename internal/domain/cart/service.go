// internal/domain/cart/service.go
package cart

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"

	"shopflow/internal/domain/catalog"
	"shopflow/internal/platform/database"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Service struct {
	repo          Repository
	catalogReader catalog.CatalogReader
	beginner      database.TxBeginner
	logger        *slog.Logger
}

func NewService(repo Repository, catalogReader catalog.CatalogReader, beginner database.TxBeginner, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		repo:          repo,
		catalogReader: catalogReader,
		beginner:      beginner,
		logger:        logger,
	}
}

func (s *Service) enrichItemTitles(ctx context.Context, items []CartItem) error {
	if len(items) == 0 {
		return nil
	}

	skus := make([]string, len(items))
	for i, item := range items {
		skus[i] = item.SKU
	}

	// Invariant: Sort SKUs lexicographically ascending before querying
	sort.Strings(skus)

	products, err := s.catalogReader.GetProductsBySKUs(ctx, skus)
	if err != nil {
		s.logger.Warn("failed to fetch product titles for cart enrichment", "err", err)
		return nil // Soft fail title enrichment to keep cart functional
	}

	titleMap := make(map[string]string, len(products))
	for _, p := range products {
		titleMap[p.SKU] = p.Title
	}

	for i := range items {
		if title, ok := titleMap[items[i].SKU]; ok {
			items[i].Title = title
		}
	}
	return nil
}

func (s *Service) CreateOrGetActiveCart(ctx context.Context, customerID uuid.UUID) (*Cart, error) {
	if customerID == uuid.Nil {
		return nil, ErrUnauthorized
	}

	cart := &Cart{
		ID:       uuid.New(),
		UserID:   customerID,
		Currency: "USD",
	}

	if err := s.repo.CreateCart(ctx, cart); err != nil {
		return nil, err
	}

	items, err := s.repo.GetCartItems(ctx, cart.ID)
	if err != nil {
		return nil, err
	}
	_ = s.enrichItemTitles(ctx, items)
	cart.Items = items

	if err := cart.CalculateTotals("USD"); err != nil {
		return nil, err
	}
	return cart, nil
}

func (s *Service) GetCart(ctx context.Context, cartID, customerID uuid.UUID) (*Cart, error) {
	if customerID == uuid.Nil {
		return nil, ErrUnauthorized
	}

	cart, err := s.repo.GetCartByID(ctx, cartID)
	if err != nil {
		return nil, err
	}

	if cart.UserID != customerID {
		return nil, ErrForbidden
	}

	items, err := s.repo.GetCartItems(ctx, cartID)
	if err != nil {
		return nil, err
	}
	_ = s.enrichItemTitles(ctx, items)
	cart.Items = items

	currency := cart.Currency
	if currency == "" {
		currency = "USD"
	}
	cart.Currency = currency

	if err := cart.CalculateTotals(currency); err != nil {
		return nil, err
	}
	return cart, nil
}

func (s *Service) AddItem(ctx context.Context, cartID, customerID uuid.UUID, sku string, quantity int, expectedVersion int64) (*Cart, error) {
	if customerID == uuid.Nil {
		return nil, ErrUnauthorized
	}
	if expectedVersion <= 0 {
		return nil, ErrInvalidVersion
	}
	if quantity <= 0 {
		return nil, ErrInvalidQuantity
	}
	sku = strings.TrimSpace(sku)
	if sku == "" {
		return nil, errors.New("sku cannot be empty")
	}

	cart, err := s.repo.GetCartByID(ctx, cartID)
	if err != nil {
		return nil, err
	}

	if cart.UserID != customerID {
		return nil, ErrForbidden
	}

	if cart.Status != CartStatusActive {
		return nil, ErrCartNotActive
	}

	if cart.Version != expectedVersion {
		return nil, ErrOptimisticLockConflict
	}

	// Strict Invariant: Zero network I/O inside database transactions!
	// CatalogReader product lookup and price resolution occurs OUTSIDE of WithTx.
	product, err := s.catalogReader.GetProductBySKU(ctx, sku)
	if err != nil || product == nil || !product.IsActive {
		return nil, ErrProductUnavailable
	}

	cartCurrency := cart.Currency
	if cartCurrency == "" {
		cartCurrency = "USD"
	}
	cart.Currency = cartCurrency

	productCurrency := product.Price.Currency
	if productCurrency == "" {
		productCurrency = "USD"
	}

	if productCurrency != cartCurrency {
		return nil, ErrCurrencyMismatch
	}

	priceMinor := product.Price.Amount
	if priceMinor <= 0 && product.PriceMinor > 0 {
		priceMinor = product.PriceMinor
	}
	if priceMinor <= 0 {
		return nil, ErrInvalidPrice
	}

	var newVer int64
	err = database.ExecuteTx(ctx, s.beginner, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var incErr error
		newVer, incErr = s.repo.IncrementCartVersion(ctx, tx, cartID, cart.Version)
		if incErr != nil {
			return incErr
		}

		return s.repo.UpsertCartItem(ctx, tx, cartID, sku, quantity, priceMinor)
	})
	if err != nil {
		return nil, err
	}

	cart.Version = newVer
	items, err := s.repo.GetCartItems(ctx, cartID)
	if err != nil {
		return nil, err
	}
	_ = s.enrichItemTitles(ctx, items)
	cart.Items = items

	if err := cart.CalculateTotals(cartCurrency); err != nil {
		return nil, err
	}

	return cart, nil
}

func (s *Service) UpdateQuantity(ctx context.Context, cartID, customerID uuid.UUID, sku string, quantity int, expectedVersion int64) (*Cart, error) {
	if customerID == uuid.Nil {
		return nil, ErrUnauthorized
	}
	if expectedVersion <= 0 {
		return nil, ErrInvalidVersion
	}
	if quantity <= 0 {
		return s.RemoveItem(ctx, cartID, customerID, sku, expectedVersion)
	}

	cart, err := s.repo.GetCartByID(ctx, cartID)
	if err != nil {
		return nil, err
	}

	if cart.UserID != customerID {
		return nil, ErrForbidden
	}

	if cart.Status != CartStatusActive {
		return nil, ErrCartNotActive
	}

	if cart.Version != expectedVersion {
		return nil, ErrOptimisticLockConflict
	}

	var newVer int64
	err = database.ExecuteTx(ctx, s.beginner, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var incErr error
		newVer, incErr = s.repo.IncrementCartVersion(ctx, tx, cartID, cart.Version)
		if incErr != nil {
			return incErr
		}

		return s.repo.UpdateCartItemQuantity(ctx, tx, cartID, sku, quantity)
	})
	if err != nil {
		return nil, err
	}

	cart.Version = newVer
	items, err := s.repo.GetCartItems(ctx, cartID)
	if err != nil {
		return nil, err
	}
	_ = s.enrichItemTitles(ctx, items)
	cart.Items = items

	currency := cart.Currency
	if currency == "" {
		currency = "USD"
	}
	cart.Currency = currency

	if err := cart.CalculateTotals(currency); err != nil {
		return nil, err
	}
	return cart, nil
}

func (s *Service) RemoveItem(ctx context.Context, cartID, customerID uuid.UUID, sku string, expectedVersion int64) (*Cart, error) {
	if customerID == uuid.Nil {
		return nil, ErrUnauthorized
	}
	if expectedVersion <= 0 {
		return nil, ErrInvalidVersion
	}

	cart, err := s.repo.GetCartByID(ctx, cartID)
	if err != nil {
		return nil, err
	}

	if cart.UserID != customerID {
		return nil, ErrForbidden
	}

	if cart.Status != CartStatusActive {
		return nil, ErrCartNotActive
	}

	if cart.Version != expectedVersion {
		return nil, ErrOptimisticLockConflict
	}

	var newVer int64
	err = database.ExecuteTx(ctx, s.beginner, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var incErr error
		newVer, incErr = s.repo.IncrementCartVersion(ctx, tx, cartID, cart.Version)
		if incErr != nil {
			return incErr
		}

		return s.repo.DeleteCartItem(ctx, tx, cartID, sku)
	})
	if err != nil {
		return nil, err
	}

	cart.Version = newVer
	items, err := s.repo.GetCartItems(ctx, cartID)
	if err != nil {
		return nil, err
	}
	_ = s.enrichItemTitles(ctx, items)
	cart.Items = items

	currency := cart.Currency
	if currency == "" {
		currency = "USD"
	}
	cart.Currency = currency

	if err := cart.CalculateTotals(currency); err != nil {
		return nil, err
	}
	return cart, nil
}

func (s *Service) ClearCart(ctx context.Context, cartID, customerID uuid.UUID, expectedVersion int64) (*Cart, error) {
	if customerID == uuid.Nil {
		return nil, ErrUnauthorized
	}
	if expectedVersion <= 0 {
		return nil, ErrInvalidVersion
	}

	cart, err := s.repo.GetCartByID(ctx, cartID)
	if err != nil {
		return nil, err
	}

	if cart.UserID != customerID {
		return nil, ErrForbidden
	}

	if cart.Status != CartStatusActive {
		return nil, ErrCartNotActive
	}

	if cart.Version != expectedVersion {
		return nil, ErrOptimisticLockConflict
	}

	var newVer int64
	err = database.ExecuteTx(ctx, s.beginner, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var incErr error
		newVer, incErr = s.repo.IncrementCartVersion(ctx, tx, cartID, cart.Version)
		if incErr != nil {
			return incErr
		}

		return s.repo.ClearCartItems(ctx, tx, cartID)
	})
	if err != nil {
		return nil, err
	}

	cart.Version = newVer
	cart.Items = []CartItem{}
	currency := cart.Currency
	if currency == "" {
		currency = "USD"
	}
	cart.Currency = currency
	_ = cart.CalculateTotals(currency)
	return cart, nil
}
