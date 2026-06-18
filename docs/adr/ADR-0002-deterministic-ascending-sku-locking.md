# ADR-0002: Deterministic Ascending SKU Locking for Deadlock Prevention

- **Status**: Accepted
- **Deciders**: Lead Architect, Inventory Domain Specialist
- **Date**: 2026-09-10
- **Consulted**: M0 Explorer 3, Adversarial Reviewer

## 1. Context and Problem Statement

In an e-commerce platform, multi-item customer orders frequently reserve inventory across overlapping sets of SKUs simultaneously.
Consider two concurrent orders placed at the exact same millisecond:
- **Customer 1**: Orders SKU-A (qty 2) and SKU-B (qty 1).
- **Customer 2**: Orders SKU-B (qty 1) and SKU-A (qty 3).

If Transaction 1 acquires an exclusive row lock on SKU-A (`SELECT ... FOR UPDATE`) and attempts to lock SKU-B, while Transaction 2 has already acquired an exclusive lock on SKU-B and attempts to lock SKU-A, a circular dependency occurs:
- Transaction 1 waits for Transaction 2 to release SKU-B.
- Transaction 2 waits for Transaction 1 to release SKU-A.

PostgreSQL detects this circular wait graph and aborts one of the transactions with error `40P01: deadlock detected`. Under high-concurrency flash sale conditions (e.g. 100 concurrent requests competing for shared items), lock contention causes frequent transaction aborts, degraded throughput, latency spikes, and customer checkout failures.

## 2. Decision

We mandate **Deterministic Ascending SKU Locking** for all multi-item inventory reservations across the entire platform.

### Implementation Rules:
1. **Total Order Pre-Sorting in Application Code**:
   Before dispatching any multi-item reservation request to the database layer, the list of SKUs must be sorted in lexicographical ascending order:
   ```go
   // Go Domain Layer
   sort.Slice(items, func(i, j int) bool {
       return items[i].SKU < items[j].SKU
   })
   ```
2. **Deterministic SQL Row Locking**:
   The reservation query MUST fetch and lock the target rows using explicit `ORDER BY sku ASC`:
   ```sql
   SELECT sku, on_hand, reserved
   FROM inventory_items
   WHERE sku = ANY($1)
   ORDER BY sku ASC
   FOR UPDATE;
   ```
3. **Atomic Evaluation & Zero Overselling**:
   While locks are held in ascending order, the inventory engine evaluates:
   ```go
   available := item.OnHand - item.Reserved
   if available < requestedQty {
       return ErrInsufficientStock // Triggers immediate ROLLBACK
   }
   ```
   If any requested item is insufficient, the entire reservation transaction aborts immediately. Zero partial reservations are permitted.

### Mathematical Proof of Deadlock Freedom:
By Dijkstra's Resource Hierarchy Theorem, a deadlock (circular wait) cannot occur if all concurrent processes request shared resources in the same global total order. Since all transactions request SKU locks in lexicographical ascending order ($SKU_1 < SKU_2 < \dots < SKU_n$), a cycle in the lock wait graph is mathematically impossible.

## 3. Consequences

### Positive
- **Zero Deadlocks (40P01 Elimination)**: High-concurrency transactions requesting identical or overlapping items queue cleanly on individual row locks rather than deadlocking and aborting.
- **Predictable Latency**: Elimination of deadlock detection timeouts reduces p99 tail latency during high-concurrency flash sales.
- **Physical Stock Conservation**: Guarantees `reserved <= on_hand`, `on_hand >= 0`, `reserved >= 0`, and zero overselling.

### Negative / Trade-offs
- **Minimal Sorting Overhead**: Slices of SKUs must be sorted in memory prior to querying (negligible $O(k \log k)$ overhead for cart sizes $k \le 100$).
- **Strict Implementation Discipline**: Any developer writing raw queries without `ORDER BY sku ASC FOR UPDATE` will re-introduce deadlock vulnerabilities. Enforced strictly by review guardrails.

## 4. Invariants Enforced
- Database check constraints on `inventory_items`:
  ```sql
  CONSTRAINT chk_inventory_on_hand_non_negative CHECK (on_hand >= 0),
  CONSTRAINT chk_inventory_reserved_non_negative CHECK (reserved >= 0),
  CONSTRAINT chk_inventory_stock_conservation CHECK (reserved <= on_hand)
  ```
- All multi-SKU reservations lock rows in ascending order.

## 5. Compliance Verification
- **Tier 4 Concurrency Test**: `TestLoad_100Concurrent_10Stock_ZeroOverselling` and `TestInventory_DeadlockAvoidance_Sorting` run 100 concurrent goroutines placing multi-item orders in randomized SKU orders. The test asserts zero deadlocks (0% `40P01` errors), exactly 10 successful reservations, and zero overselling.
