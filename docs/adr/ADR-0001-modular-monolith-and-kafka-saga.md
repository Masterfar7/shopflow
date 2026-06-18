# ADR-0001: Modular Monolith and Kafka Event-Driven Saga

- **Status**: Accepted
- **Deciders**: Lead Architect, Orchestrator
- **Date**: 2026-09-10
- **Consulted**: Survey Explorer 1, M0 Explorer 3

## 1. Context and Problem Statement

ShopFlow is an e-commerce order processing platform requiring high concurrency, data integrity, and distributed resilience. Modern e-commerce architectures often default to distributed microservices. However, early-stage distributed microservices introduce massive operational overhead:
- Network latency across synchronous inter-service RPC hops.
- Complex orchestration and distributed failure modes (partial network partitions, cascading timeouts).
- Complicated local development and test automation (requiring dozens of containerized services to run a basic test).
- Distributed transactions across microservice boundaries leading to distributed data inconsistency.

Conversely, traditional monolithic applications often degenerate into "spaghetti code," where bounded contexts bleed into each other, cross-domain SQL joins create tight coupling, and extracting independent services becomes impossible.

How can ShopFlow combine the operational velocity and testing simplicity of a single deployable binary with the strict domain decoupling, scalability, and transactional guarantees of a distributed event-driven architecture?

## 2. Decision

We will structure ShopFlow as a **Modular Monolith** in Go with **Event-Driven Choreography and Saga Orchestration** over Apache Kafka (KRaft mode).

### Key Architectural Tenets:
1. **Single Deployment Binary**: The core application compiles into a single monolithic binary (`cmd/shopflow/main.go`).
2. **Strict Package Boundaries**: Each bounded context lives in its own isolated package under `internal/domain/<domain>` (`catalog`, `cart`, `order`, `inventory`, `payment`, `saga`, `outbox`, `inbox`).
3. **Database Schema Isolation**: Each domain strictly owns its database tables. Direct cross-domain SQL `JOIN` statements and cross-domain database foreign keys are strictly prohibited.
4. **Synchronous Inter-Domain Reads**: Cross-domain queries (e.g. Order verifying product price at cart checkout) occur in-process through explicit, narrow Go read-only interface contracts (e.g., `CatalogReader`).
5. **Asynchronous Cross-Domain Mutations (The Order Saga)**: Multi-step distributed state transitions (Order Placement -> Stock Reservation -> Payment Capture -> Order Confirmation) MUST NOT occur via synchronous HTTP/gRPC chains or distributed 2PC. Instead, they are orchestrated via an asynchronous Finite State Machine (FSM) persisted in PostgreSQL (`order_sagas`), driven by Kafka events emitted via the Transactional Outbox.

## 3. Consequences

### Positive
- **Rapid Development & Testing**: Integration tests run against a single process with real PostgreSQL and Kafka containers via Testcontainers in milliseconds.
- **Zero Inter-Service Network Overhead for Reads**: In-memory interface dispatch avoids serialization and network hops.
- **Clear Extraction Path**: Because domains share zero database tables and communicate via explicit interfaces and Kafka topics, any domain can be extracted into an independent microservice with zero changes to business logic.
- **Distributed Resilience**: Persistent Saga FSM handles network failures, payment declines, and inventory shortages via automated compensation workflows (releasing stock, triggering refunds).

### Negative / Trade-offs
- **Discipline Required**: Developers must resist the temptation to query tables across domain boundaries or import internal packages directly. Enforced via `Architecture Guidelines` and adversarial code reviews.
- **Eventual Consistency**: Asynchronous saga progression means order confirmation is eventually consistent (typically completed in 20–100ms).

## 4. Invariants Enforced
- Zero cross-service SQL queries or joins.
- Zero cross-domain foreign key constraints in database migrations.
- Saga state must be persisted in PostgreSQL at every step transition; ephemeral in-memory sagas are strictly forbidden.

## 5. Compliance Verification
- Static analysis and code review verify that domain packages never import other domain repositories or database transaction handles.
- Integration tests (`TestChoreography_HappyPath_FullOrderLifecycle`, `TestChoreography_PaymentFailure_TriggersCompensation`) verify asynchronous saga completion and compensation workflows.
