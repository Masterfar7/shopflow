# Project: ShopFlow Distributed Order Processing Platform

## Architecture
ShopFlow is designed as a high-performance **Modular Monolith** in Go with **Event-Driven Choreography and Saga Orchestration** via Kafka.
The architecture enforces strict domain isolation:
- No cross-domain direct database queries or domain package leaks.
- Cross-domain interactions occur strictly via explicit interface contracts or asynchronous Kafka events.
- Strict transaction boundaries: zero external network I/O inside database transactions.
- Zero floats: all monetary values are represented strictly in `int64` minor units.
- Deterministic locking: multi-SKU reservations sort SKUs in ascending order (`ORDER BY sku ASC FOR UPDATE`) to guarantee zero deadlocks.
- Resilience: Transactional Outbox with `FOR UPDATE SKIP LOCKED` leasing and Idempotent Inbox with unique `(message_id, consumer_group)` deduplication.

```
                  ┌──────────────────────────────────────────────┐
                  │                 API Gateway / HTTP           │
                  │             (chi router, auth, tracing)      │
                  └──────┬────────────────────┬───────────┬──────┘
                         │                    │           │
                         ▼                    ▼           ▼
                  ┌──────────────┐     ┌──────────────┐  ┌──────────────┐
                  │   Catalog    │     │     Cart     │  │    Order     │
                  │   Domain     │     │    Domain    │  │    Domain    │
                  └──────┬───────┘     └──────┬───────┘  └──────┬───────┘
                         │                    │                 │
                         │             Idempotency Keys         ▼
                         │                    │          ┌──────────────┐
                         │                    │          │  Order Saga  │
                         │                    │          │  (Postgres)  │
                         │                    │          └──────┬───────┘
                         ▼                    ▼                 │
                  ┌──────────────┐     ┌──────────────┐         │
                  │  Inventory   │     │   Payment    │         │
                  │  (Atomic)    │     │ (Simulation) │         │
                  └──────┬───────┘     └──────┬───────┘         │
                         │                    │                 │
                         ▼                    ▼                 ▼
                  ┌──────────────────────────────────────────────┐
                  │       Transactional Outbox / Inbox           │
                  └──────────────────────┬───────────────────────┘
                                         │
                                         ▼
                  ┌──────────────────────────────────────────────┐
                  │            Apache Kafka (KRaft)              │
                  │   Topics: orders, inventory, payments, dlq   │
                  └──────────────────────────────────────────────┘
```

## Feature Inventory
| # | Feature ID | Feature Description | Milestone | Source |
|---|------------|---------------------|-----------|--------|
| 1 | FEAT-INF-01 | Docker Compose Local Infrastructure (PostgreSQL, Kafka KRaft, Redis, Mailpit) | M0 | REQ-R6 |
| 2 | FEAT-INF-02 | API & Event Contracts (OpenAPI 3.1, Proto3 Buf, CloudEvents/Kafka JSON Schemas, ADRs, ARCHITECTURE.md) | M0 | REQ-R1 |
| 3 | FEAT-CAT-01 | SKU & Product Management (Catalog CRUD & queries) | M1 | REQ-R2 |
| 4 | FEAT-CAT-02 | Price & Currency Management (Strict `int64` minor units, zero floats) | M1 | REQ-R2, Invariant |
| 5 | FEAT-CAT-03 | Catalog Optimistic Concurrency Control (Version checks / ETag) | M1 | REQ-R2 |
| 6 | FEAT-CRT-01 | Cart Creation & Ownership-Based Authorization | M1 | REQ-R2 |
| 7 | FEAT-CRT-02 | Cart Item Add/Update/Remove with Price Snapshotting | M1 | REQ-R2 |
| 8 | FEAT-CRT-03 | Cart Optimistic Concurrency & Item Lifecycle | M1 | REQ-R2 |
| 9 | FEAT-ORD-01 | Atomic Multi-Item Order Creation with Server-Side Totals | M2 | REQ-R3 |
| 10 | FEAT-ORD-02 | Immutable Price & Title Snapshotting on Line Items | M2 | REQ-R3, Invariant |
| 11 | FEAT-ORD-03 | Order State Machine Lifecycle & Invariant Guards | M2 | REQ-R3 |
| 12 | FEAT-ORD-04 | REST Idempotency Key Handling with DB Locking & Response Caching | M2 | REQ-R3 |
| 13 | FEAT-INV-01 | SKU Stock Initialization & Inventory Tracking | M2 | REQ-R3 |
| 14 | FEAT-INV-02 | Multi-SKU Atomic Stock Reservation with Deterministic Ascending Locking (`ORDER BY sku ASC`) | M2 | REQ-R3, Invariant |
| 15 | FEAT-INV-03 | Physical Stock Conservation (`on_hand >= 0`, `reserved <= on_hand`, zero overselling) | M2 | REQ-R3, Invariant |
| 16 | FEAT-INV-04 | Stock Reservation Release & Stock Reconciliation (Compensation) | M2 | REQ-R3 |
| 17 | FEAT-SGA-01 | Order Saga Orchestration FSM Persisted in PostgreSQL (Never in ephemeral goroutines) | M2 | REQ-R3 |
| 18 | FEAT-SGA-02 | Saga Event Handlers, Step Transitions & Timeout Watchdogs | M2 | REQ-R3 |
| 19 | FEAT-SGA-03 | Automated Multi-Step Compensation Workflows (Release stock, trigger refund) | M2 | REQ-R3, §R4 |
| 20 | FEAT-TXO-01 | Transactional Outbox Schema & Insertion within Local DB Transactions | M3 | REQ-R4 |
| 21 | FEAT-TXO-02 | Outbox Polling Publisher with `FOR UPDATE SKIP LOCKED` and Lease Renewal | M3 | REQ-R4 |
| 22 | FEAT-TXO-03 | Kafka Acknowledgement Handling & Outbox Message Deletion / Archival | M3 | REQ-R4 |
| 23 | FEAT-INB-01 | Idempotent Inbox Schema & Deduplication via Unique `(message_id, consumer_group)` | M3 | REQ-R4 |
| 24 | FEAT-INB-02 | Atomic Message Processing with Domain State Mutation | M3 | REQ-R4 |
| 25 | FEAT-INB-03 | Terminal State Protection against Out-of-Order Message Resurrection | M3 | REQ-R4 |
| 26 | FEAT-RSZ-01 | Exponential Backoff Retry Engine with Jitter | M3 | REQ-R4 |
| 27 | FEAT-RSZ-02 | Dead Letter Queue (DLQ) Routing for Poison Pill Events | M3 | REQ-R4 |
| 28 | FEAT-RSZ-03 | Automated Failure Recovery & Redelivery Crash-Safety | M3 | REQ-R4 |
| 29 | FEAT-PAY-01 | Idempotent Payment Simulator with Configurable Test Triggers | M4 | REQ-R2, §R4 |
| 30 | FEAT-PAY-02 | Payment Capture, Confirmation & Refund Execution | M4 | REQ-R4 |
| 31 | FEAT-PAY-03 | Asynchronous Payment Reconciliation Routine | M4 | REQ-R2 |
| 32 | FEAT-OBS-01 | W3C OpenTelemetry Distributed Tracing across HTTP & Kafka Boundaries | M4 | REQ-R6 |
| 33 | FEAT-OBS-02 | Prometheus Technical & Business Metrics Exporter | M4 | REQ-R6 |
| 34 | FEAT-OBS-03 | Health Probes (`/health/live`, `/health/ready`) with Dependency Checks | M4 | REQ-R6 |
| 35 | FEAT-M7-01 | BlobStorage abstraction & S3/MinIO/InMemory implementations in internal/platform/storage/ | M7 | Milestone M7 |
| 36 | FEAT-M7-02 | Goose migration 00008_create_product_images_tables.sql with unique storage_key & BIGINT size | M7 | Milestone M7 |
| 37 | FEAT-M7-03 | Product Image domain model, repository, and service in internal/domain/catalog | M7 | Milestone M7 |
| 38 | FEAT-M7-04 | REST endpoints /api/v1/products/{id}/images (POST multipart, GET list, DELETE) | M7 | Milestone M7 |
| 39 | FEAT-M7-05 | Image MIME magic byte sniffing, <= 5MB boundary enforcement, and orphaned upload compensation | M7 | Milestone M7 |
| 40 | FEAT-M8-01 | OpenSearch client abstraction & implementations (internal/platform/search/) with OpenSearch client & InMemory fallback | M8 | Milestone M8 |
| 41 | FEAT-M8-02 | Product Search Index schema definition & document mapping (title with weighting, description, category_id, price_minor int64, tags) | M8 | Milestone M8 |
| 42 | FEAT-M8-03 | Catalog to Search synchronization service / event consumer | M8 | Milestone M8 |
| 43 | FEAT-M8-04 | Search domain & query builder (text query, price range in int64, category filter/aggregation, offset/limit pagination) | M8 | Milestone M8 |
| 44 | FEAT-M8-05 | REST endpoints /api/v1/products/search and /api/v1/catalog/search | M8 | Milestone M8 |
| 45 | FEAT-M9-01 | Email client abstraction & implementations (internal/platform/email/) with SMTP (Mailpit) & InMemory fallback | M9 | Milestone M9 |
| 46 | FEAT-M9-02 | Transactional HTML & plain text email templates for OrderConfirmed and OrderCancelled with integer minor money formatting | M9 | Milestone M9 |
| 47 | FEAT-M9-03 | Notification domain service & Kafka event consumer for order lifecycle events | M9 | Milestone M9 |
| 48 | FEAT-M9-04 | Idempotent email dispatch via inbox_messages deduplication to prevent duplicate emails | M9 | Milestone M9 |
| 49 | FEAT-M9-05 | Configuration parameters for SMTP and wiring in cmd/shopflow/main.go | M9 | Milestone M9 |

## Milestones
| # | Name | Scope | Dependencies | Status |
|---|------|-------|-------------|--------|
| M0 | Architecture, Contracts & Foundation | OpenAPI specs, Proto3, Kafka schemas, ADRs, ARCHITECTURE.md, Go module scaffolding, Goose migration runner, Docker Compose | none | DONE |
| M1 | Core Engine, Catalog & Cart | Configuration, logging (`slog`), DB connection pool (`pgx`), Catalog & Cart domain logic, optimistic concurrency, `int64` money | M0 | DONE |
| M2 | Atomic Orders, Stock Reservations & Saga FSM | Multi-item atomic orders, price snapshots, idempotency keys, deterministic SKU locking (`ORDER BY sku ASC`), zero overselling, PostgreSQL Saga FSM | M1 | DONE |
| M3 | Transactional Outbox, Inbox & Messaging Resilience | Outbox publisher (`SKIP LOCKED`), Idempotent Inbox consumer, Kafka integration (`franz-go`), backoff retry, DLQ, compensation workflows | M2 | DONE |
| M4 | Payment Simulation, Observability & Recovery | Payment simulator & refund adapter, OpenTelemetry tracing, Prometheus metrics, health probes, crash recovery | M3 | DONE |
| M5 | Final E2E Test Pass & Adversarial Hardening | Pass 100% of E2E test suite (Tiers 1–4) + Adversarial coverage hardening (Tier 5) | M4, TEST_READY.md | DONE |
| M6 | Redis Integration (Catalog Cache & Rate Limiting) | Cache-aside catalog layer (singleflight, jitter TTL, negative cache), Token bucket rate limiting (Lua script, per-route limits, FailOpen/FailClosed) | M1, M4 | DONE |
| M7 | S3/MinIO Object Storage & Product Images | BlobStorage adapter, image metadata migration, product image REST API, MIME magic sniffing, 5MB boundary, orphaned compensation | M1 | DONE |
| M8 | OpenSearch Product Search & Faceting | Search platform adapter (OpenSearch & InMemory), product index mapping, catalog sync post-commit hooks, full-text search with title boost, category faceting, int64 minor price filter, REST search route | M1 | DONE |
| M9 | Notifications Service & Mailpit Integration | Email platform adapter (SMTP & InMemory), transactional HTML/text templates, notification domain service, Kafka order event consumer, inbox deduplication, order outbox payload enrichment | M1 | DONE |
| M10 | Minimal Web Frontend | React 18 + Vite + TypeScript SPA, catalog search & category faceting, cart optimistic concurrency control (If-Match), live Saga order state tracking, Go static embed | M8 | DONE |
| M11 | Kubernetes Deployment & CI/CD Pipelines | Helm chart (deploy/helm/shopflow/) with Deployment, Service, HPA, PDB, ConfigMap, Secret, Ingress; minimal multi-stage Dockerfile (<50MB); GitHub Actions CI pipeline (golangci-lint, matrix race tests, Helm/Docker lint) | M0 | DONE |
| M12 | Load Testing & Failure Invariance Verification | k6 load tests (overselling_stress.js for 100 concurrent requests competing for 10 items, search_rate_limit.js), benchmark analysis & BENCHMARK.md | M2, M3, M6 | DONE |

## Interface Contracts

### Domain Money Representation
All domains represent money using integer minor units (e.g. cents) with currency code:
```go
type Money struct {
    Amount   int64  `json:"amount"`   // e.g. 1999 for $19.99
    Currency string `json:"currency"` // ISO-4217, e.g. "USD"
}
```
Floats are strictly forbidden in domain models and persistence DDLs.

### Catalog ↔ Cart / Order
- `CatalogReader`:
  - `GetProduct(ctx context.Context, id uuid.UUID) (*Product, error)`
  - `GetProductsBySKUs(ctx context.Context, skus []string) ([]Product, error)`
- Read-only contract. Orders snapshot `title` and `unit_price_minor` at placement time.

### Order ↔ Inventory
- `InventoryService`:
  - `ReserveStock(ctx context.Context, req ReserveStockRequest) (*ReservationResult, error)`
    - Algorithm: `SELECT ... FROM inventory WHERE sku = ANY($1) ORDER BY sku ASC FOR UPDATE;`
    - Checks: `(on_hand - reserved) >= requested_qty` for all items.
    - Transaction aborts if any item is insufficient (zero partial reservations, zero overselling).
  - `ReleaseStock(ctx context.Context, reservationID uuid.UUID) error`
  - `CommitStock(ctx context.Context, reservationID uuid.UUID) error`

### Order ↔ Payment (Asynchronous via Saga / Outbox)
- Events:
  - `OrderCreated` → triggers Inventory Reservation.
  - `StockReserved` → triggers Payment Authorization/Capture.
  - `PaymentProcessed` → triggers Order Confirmation.
  - `PaymentFailed` → triggers Compensation (Release Stock, Cancel Order).
  - `StockReservationFailed` → triggers Order Cancellation.

### Outbox ↔ Kafka Publisher
- Transactional outbox polling query:
```sql
SELECT id, aggregate_type, aggregate_id, event_type, payload, headers
FROM outbox_messages
WHERE published_at IS NULL AND (leased_until IS NULL OR leased_until < NOW())
ORDER BY created_at ASC
LIMIT $1
FOR UPDATE SKIP LOCKED;
```

### Inbox ↔ Kafka Consumer
- Inbox deduplication table:
```sql
CREATE TABLE inbox_messages (
    message_id VARCHAR(255) NOT NULL,
    consumer_group VARCHAR(100) NOT NULL,
    event_type VARCHAR(100) NOT NULL,
    payload JSONB NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (message_id, consumer_group)
);
```

### BlobStorage (Object Storage Abstraction)
Unified media and blob storage interface implemented by S3/MinIO (`S3Storage`) and in-memory (`MemoryStorage`) adapters:
```go
type BlobStorage interface {
    Put(ctx context.Context, key string, data io.Reader, sizeBytes int64, contentType string) (*StoredObject, error)
    Get(ctx context.Context, key string) (io.ReadCloser, *StoredObject, error)
    Delete(ctx context.Context, key string) error
    GetURL(key string) string
}

type StoredObject struct {
    Key         string
    ContentType string
    SizeBytes   int64
    URL         string
    Data        []byte
}
```
- Invariants & Validation:
  - MIME magic byte sniffing: `DetectImageContentType(data []byte)` inspects payload magic bytes via `http.DetectContentType` with explicit support for WebP (`RIFF....WEBP`).
  - Size boundary: Max upload size is strictly 5MB (`MaxFileSizeBytes = 5 * 1024 * 1024`).
  - Allowed MIME content types: `image/jpeg`, `image/png`, `image/webp`, `image/gif`.
  - Deterministic collision-free key generation: `products/{product_id}/{image_id}.{ext}`.
  - Orphaned upload compensation: If metadata persistence fails after binary upload, the uploaded blob is immediately deleted from storage.

### Search Client (Search Engine Abstraction)
Unified product search and indexing engine abstraction implemented by OpenSearch (`OpenSearchClient`) and thread-safe in-memory (`MemoryClient`) fallback adapters in `internal/platform/search/`:
```go
// Client defines the contract for product search and indexing engines.
type Client interface {
	Ping(ctx context.Context) error
	EnsureIndex(ctx context.Context) error
	IndexProduct(ctx context.Context, doc ProductDocument) error
	DeleteProduct(ctx context.Context, id string) error
	BulkIndex(ctx context.Context, docs []ProductDocument) error
	Search(ctx context.Context, q Query) (*SearchResult, error)
}

// ProductDocument represents an indexed product in the search engine.
// Strictly adheres to Zero Floats for Money invariant by using int64 PriceMinor.
type ProductDocument struct {
	ID           string  `json:"id"`
	SKU          string  `json:"sku"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	CategoryID   *string `json:"category_id,omitempty"`
	CategoryName string  `json:"category_name"`
	PriceMinor   int64   `json:"price_minor"`
	Currency     string  `json:"currency"`
	InStock      bool    `json:"in_stock"`
}

// Query specifies search criteria, filters, and pagination parameters.
type Query struct {
	Text          string  // Free-text search query (boosted on title)
	CategoryID    *string // Exact match filter on category UUID
	MinPriceMinor *int64  // Filter price_minor >= MinPriceMinor
	MaxPriceMinor *int64  // Filter price_minor <= MaxPriceMinor
	InStockOnly   *bool   // Filter in_stock == true
	Page          int     // 1-based page index (default 1)
	PageSize      int     // Number of items per page (default 20, max 100)
}

// FacetBucket represents an aggregation bucket for a facet value.
type FacetBucket struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

// Facets represents faceted search aggregations across categories.
type Facets struct {
	Categories []FacetBucket `json:"categories"`
}

// SearchResult contains the search query results, pagination metadata, and facets.
type SearchResult struct {
	TotalHits  int64             `json:"total_hits"`
	Page       int               `json:"page"`
	PageSize   int               `json:"page_size"`
	TotalPages int               `json:"total_pages"`
	Products   []ProductDocument `json:"products"`
	Facets     Facets            `json:"facets"`
}
```
- Invariants & Validation:
  - Zero floats for money: `price_minor` represented strictly as `int64` minor units across index documents, search queries, and responses.
  - Full-text relevance ranking: Multi-match query with title boost (`title^3`) over SKU (`sku^2`) and description (`description`).
  - Category faceting: Category aggregation buckets returning counts per category.
  - Price filtering: Exact boundary matching (`gte` and `lte`) using integer minor units.
  - Query parameter validation: Rejects negative prices (`min_price < 0` or `max_price < 0`), inverted price ranges (`min_price > max_price`), non-integer prices, and invalid category UUID formats with HTTP 400 Bad Request.
  - Dual REST routing: Exposes search via `/api/v1/products/search` and canonical alias `/api/v1/catalog/search`.
  - Catalog synchronization: Automatic post-commit synchronization hooks in catalog service keep OpenSearch/InMemory index up-to-date on product creation, update, and price changes, and prune deleted products.

### Email Platform (Transactional Email Abstraction)
Unified transactional email dispatch abstraction implemented by SMTP (`SMTPSender` connecting to Mailpit or upstream SMTP relay) and thread-safe in-memory (`MemorySender`) fallback adapters in `internal/platform/email/`:
```go
// EmailMessage models an outbound transactional email with plain-text and HTML alternatives.
type EmailMessage struct {
	From     string   `json:"from"`
	To       []string `json:"to"`
	Subject  string   `json:"subject"`
	TextBody string   `json:"text_body"`
	HTMLBody string   `json:"html_body"`
}

// Sender abstracts the transactional email dispatch mechanism.
type Sender interface {
	Send(ctx context.Context, msg EmailMessage) error
}
```
- Invariants & Implementations:
  - `SMTPSender`: RFC 2046 `multipart/alternative` MIME messages with plain text and HTML parts, socket deadline propagation via `ctx.Deadline()`, and optional plain authentication.
  - `MemorySender`: Concurrency-safe in-memory store utilizing `sync.RWMutex`, deep copying of recipient slices, message inspection helpers (`GetMessages()`, `Count()`), and single-shot fault injection (`SetFailNext(err)`).
  - Provider selection: `NewSenderFromConfig` instantiates `MemorySender` when `EMAIL_PROVIDER=memory` or `SMTP_HOST` is blank, and `SMTPSender` otherwise.

### Notification Service & Kafka Event Consumer
Notification domain service and background consumer orchestrating event-driven customer communications in `internal/domain/notification/`:
```go
// Service coordinates event-driven transactional notification delivery.
type Service struct {
	sender   email.Sender
	renderer *Renderer
	logger   *slog.Logger
}

func NewService(sender email.Sender, renderer *Renderer, logger *slog.Logger) *Service
func (s *Service) HandleMessage(ctx context.Context, msg kafka.Message) error

// Consumer coordinates consumption of order events from Kafka via idempotent inbox.
type Consumer struct {
	processor *inbox.Processor
	logger    *slog.Logger
}

func NewConsumer(inboxRepo inbox.Repository, kafkaConsumer kafka.Consumer, svc *Service, logger *slog.Logger) *Consumer
func (c *Consumer) Start(ctx context.Context) error
func (c *Consumer) Stop(ctx context.Context) error
func (c *Consumer) IsActive() bool
```
- Invariants & Validation:
  - Zero floats for money: All financial fields (`UnitPriceMinor`, `SubtotalMinor`, `TotalAmountMinor`) are strictly 64-bit signed integers. Monetary formatting uses pure integer division and modulo (`/ 100`, `% 100`) via `FormatMinorMoney` with bounds guard for `math.MinInt64`.
  - Strict Transaction Boundary: `Service` holds zero database handles, connection pools, or transactions. All SMTP and network I/O operations execute strictly outside database transactions (ARCHITECTURE.md §6).
  - Event Filtering & Deduplication: Subscribes to `shopflow.orders` under `consumer_group = "notification-service"`. Filters strictly for canonical customer-facing events (`OrderConfirmed`, `OrderCancelled`), ignoring internal aggregate transition events to eliminate duplicate notifications.
  - Idempotent Inbox: Uses `inbox.Processor` to guarantee at-most-once email dispatch per Kafka `message_id`. Replayed messages are safely skipped.
  - Poison Pill Handling: Malformed payloads return `inbox.ErrPoisonPill` to route invalid messages directly to the dead-letter queue (`shopflow.deadletter`).
  - Contextual HTML Escaping: Email rendering uses standard library `html/template` with contextual auto-escaping to eliminate XSS hazards.

## Code Layout
```
shopflow/
├── cmd/
│   ├── shopflow/         # Main monolithic binary
│   └── migrate/          # Standalone Goose migration CLI
├── api/
│   ├── openapi/          # OpenAPI 3.1 specifications
│   ├── proto/            # Protobuf definitions
│   └── events/           # Kafka event schemas (JSON Schema / CloudEvents)
├── docs/
│   ├── adr/              # Architecture Decision Records
│   └── ARCHITECTURE.md         # Domain boundary & concurrency guardrails
├── internal/
│   ├── platform/         # Shared technical infrastructure
│   │   ├── config/       # Environment & configuration loading
│   │   ├── database/     # pgx connection pool & transaction management
│   │   ├── email/        # Email sender abstraction, SMTP & InMemory adapters
│   │   ├── kafka/        # twmb/franz-go client, producer & consumer loop
│   │   ├── search/       # Search client, OpenSearch & InMemory adapters
│   │   ├── storage/      # BlobStorage abstraction, S3/MinIO & Memory adapters
│   │   ├── telemetry/    # OpenTelemetry tracing & Prometheus metrics
│   │   └── web/          # HTTP helpers, middleware, error handling
│   ├── domain/           # Core business domain packages
│   │   ├── catalog/      # Products, prices, categories
│   │   ├── cart/         # Shopping carts, line items, TTL
│   │   ├── order/        # Orders, price snapshots, totals calculation
│   │   ├── inventory/    # Stock levels, atomic reservations, SKU locking
│   │   ├── notification/ # Notification service, HTML/text templates, consumer
│   │   ├── payment/      # Payment simulation, refunds, reconciliation
│   │   ├── saga/         # Order Saga FSM coordinator & state persistence
│   │   ├── outbox/       # Outbox table, polling publisher, leasing
│   │   └── inbox/        # Inbox table, deduplication, consumer handler
│   └── testutil/         # Testcontainers harness, fixtures, mock generators
├── migrations/           # Goose SQL migration scripts
├── test/
│   └── e2e/              # Dual Track E2E Test Suite (Tiers 1-5)
├── docker-compose.yml    # PostgreSQL, Kafka KRaft, Redis, Mailpit
├── Makefile              # Build, test, migrate, lint targets
├── go.mod
└── go.sum
```
