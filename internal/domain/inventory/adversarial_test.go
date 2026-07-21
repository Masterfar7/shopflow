package inventory_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"shopflow/internal/domain/inventory"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// VETO CHECK 1: Prohibition of floating-point types (float32, float64).
// ShopFlow Architecture Guidelines Constitution §4.1: float32 and float64 are strictly banned.
func TestAdversarial_VetoCheck1_ZeroFloats(t *testing.T) {
	dir := "."
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		// Inspect non-test production source files
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	require.NoError(t, err)

	var violations []string

	for _, pkg := range pkgs {
		for fileName, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				ident, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				if ident.Name == "float32" || ident.Name == "float64" {
					pos := fset.Position(ident.Pos())
					violations = append(violations, pos.String())
				}
				return true
			})
			_ = fileName
		}
	}

	assert.Empty(t, violations, "Zero floats rule violated in production files: %v", violations)
}

// VETO CHECK 2: Deterministic ascending SKU locking prior to row locking.
// ShopFlow Architecture Guidelines Constitution §3.1 and Binary Veto Checklist #2.
func TestAdversarial_VetoCheck2_LexicographicalAscendingSKUSort(t *testing.T) {
	svc, repo := setupInventoryService()
	ctx := context.Background()

	// Seed 5 SKUs in unsorted order
	unsortedSKUs := []string{"SKU-ZEBRA", "SKU-MONKEY", "SKU-BANANA", "SKU-APPLE", "SKU-KANGAROO"}
	for _, s := range unsortedSKUs {
		_, _ = repo.UpsertStock(ctx, s, 50)
	}

	// 1. Test in ReserveStock
	orderID := uuid.New()
	resID := uuid.New()
	var reqItems []inventory.StockItemRequest
	for _, s := range unsortedSKUs {
		reqItems = append(reqItems, inventory.StockItemRequest{SKU: s, Quantity: 2})
	}

	_, err := svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		ReservationID: resID,
		OrderID:       orderID,
		Items:         reqItems,
	})
	require.NoError(t, err)

	// Check that the last locked order in the repository was strictly sorted ascending
	repo.lockMu.Lock()
	require.NotEmpty(t, repo.lockOrderLog)
	lastLockOrder := repo.lockOrderLog[len(repo.lockOrderLog)-1]
	repo.lockMu.Unlock()

	expectedOrder := []string{"SKU-APPLE", "SKU-BANANA", "SKU-KANGAROO", "SKU-MONKEY", "SKU-ZEBRA"}
	assert.Equal(t, expectedOrder, lastLockOrder, "ReserveStock must sort SKUs lexicographically ascending before acquiring locks")

	// 2. Test in CommitStock
	err = svc.CommitStock(ctx, resID)
	require.NoError(t, err)

	repo.lockMu.Lock()
	commitLockOrder := repo.lockOrderLog[len(repo.lockOrderLog)-1]
	repo.lockMu.Unlock()

	assert.Equal(t, expectedOrder, commitLockOrder, "CommitStock must sort SKUs lexicographically ascending before acquiring locks")
}

// VETO CHECK 3: Strict domain table isolation and zero cross-domain queries.
// ShopFlow Architecture Guidelines Constitution §2.1 & Binary Veto Checklist #3.
func TestAdversarial_VetoCheck3_DomainTableIsolation(t *testing.T) {
	repoFile := "repository.go"
	data, err := os.ReadFile(repoFile)
	if err != nil {
		// Try relative to workspace
		data, err = os.ReadFile(filepath.Join("internal", "domain", "inventory", repoFile))
	}
	require.NoError(t, err)

	content := string(data)

	// Forbidden cross-domain tables
	forbiddenTables := []string{
		"catalog_products", "catalog_categories", "products", "categories",
		"carts", "cart_items",
		"orders", "order_items", "order_sagas",
		"payments", "payment_transactions",
		"outbox_messages", "inbox_messages",
	}

	for _, tbl := range forbiddenTables {
		assert.False(t, strings.Contains(strings.ToLower(content), tbl),
			"repository.go must NOT reference foreign table %q", tbl)
	}

	// Verify no SQL JOIN statements
	assert.False(t, strings.Contains(strings.ToUpper(content), " JOIN "),
		"repository.go must NOT contain cross-domain SQL JOIN statements")
}

// VETO CHECK 4: Universal Context Propagation.
// ShopFlow Architecture Guidelines Constitution §5.1 & Binary Veto Checklist #7.
func TestAdversarial_VetoCheck4_ContextPropagation(t *testing.T) {
	serviceType := reflect.TypeOf((*inventory.InventoryService)(nil)).Elem()
	contextType := reflect.TypeOf((*context.Context)(nil)).Elem()

	for i := 0; i < serviceType.NumMethod(); i++ {
		method := serviceType.Method(i)
		require.True(t, method.Type.NumIn() >= 1, "Method %s must accept at least one argument", method.Name)
		firstArg := method.Type.In(0)
		assert.Equal(t, contextType, firstArg,
			"Method %s on InventoryService must accept context.Context as its first parameter", method.Name)
	}

	repoType := reflect.TypeOf((*inventory.Repository)(nil)).Elem()
	for i := 0; i < repoType.NumMethod(); i++ {
		method := repoType.Method(i)
		require.True(t, method.Type.NumIn() >= 1, "Method %s must accept at least one argument", method.Name)
		firstArg := method.Type.In(0)
		assert.Equal(t, contextType, firstArg,
			"Method %s on Repository must accept context.Context as its first parameter", method.Name)
	}
}
