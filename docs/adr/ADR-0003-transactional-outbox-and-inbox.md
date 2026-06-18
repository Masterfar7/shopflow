# ADR-0003: Transactional Outbox with SKIP LOCKED Leasing and Idempotent Inbox

- **Status**: Accepted
- **Deciders**: Lead Architect, Messaging & Infrastructure Specialist
- **Date**: 2026-09-10
- **Consulted**: M0 Explorer 3, Adversarial Reviewer

## 1. Context and Problem Statement

ShopFlow relies on asynchronous event-driven messaging to coordinate workflows across bounded contexts (e.g. Order -> Inventory -> Payment). This introduces the classic **Dual-Write Problem**:
- If an order is saved to PostgreSQL, and the service immediately attempts to publish an event to Kafka:
  1. If the database transaction commits, but the Kafka broker is unreachable, the event is lost forever (inconsistent state).
  2. If the service attempts to publish to Kafka *before* committing the database transaction, a subsequent database commit failure results in a "phantom event" published to Kafka for an order that does not exist.
  3. Performing network calls to Kafka inside the database transaction holds database connection pool slots open, causes lock contention, and drastically impairs throughput.

Furthermore, network unreliability and Kafka consumer group rebalances mean messages are delivered with **at-least-once** semantics. Consumer services will inevitably receive duplicate messages or out-of-order events.

## 2. Decision

We adopt the **Transactional Outbox** pattern for reliable message publishing and the **Idempotent Inbox** pattern for resilient message consumption.

### 2.1 Transactional Outbox Specification
1. Every domain mutation that emits an event inserts the event record into the `outbox_messages` table within the **SAME local database transaction** as the business state mutation.
2. `outbox_messages` schema:
   ```sql
   CREATE TABLE outbox_messages (
       id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
       aggregate_type VARCHAR(100) NOT NULL,
       aggregate_id VARCHAR(255) NOT NULL,
       event_type VARCHAR(100) NOT NULL,
       payload JSONB NOT NULL,
       headers JSONB NOT NULL DEFAULT '{}'::jsonb,
       created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
       leased_until TIMESTAMPTZ,
       published_at TIMESTAMPTZ
   );
   CREATE INDEX idx_outbox_unprocessed ON outbox_messages (created_at ASC)
   WHERE published_at IS NULL;
   ```
3. **Outbox Poller Worker**:
   - An asynchronous background worker periodically polls unpublished messages using row leasing:
     ```sql
     SELECT id, aggregate_type, aggregate_id, event_type, payload, headers
     FROM outbox_messages
     WHERE published_at IS NULL AND (leased_until IS NULL OR leased_until < NOW())
     ORDER BY created_at ASC
     LIMIT $1
     FOR UPDATE SKIP LOCKED;
     ```
   - When claimed, `leased_until` is set to `NOW() + interval '30 seconds'`.
   - The worker publishes the event to Apache Kafka (`franz-go`).
   - Upon receiving Kafka broker ACK, the worker marks `published_at = NOW()` (or deletes the record in high-volume configurations).
   - If the worker crashes mid-publish, the lease expires and another worker safely reclaims the message.

### 2.2 Idempotent Inbox Specification
1. Every Kafka consumer records incoming events in the `inbox_messages` table:
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
2. Message processing occurs inside a local transaction:
   ```sql
   -- Insert into inbox:
   INSERT INTO inbox_messages (message_id, consumer_group, event_type, payload)
   VALUES ($1, $2, $3, $4)
   ON CONFLICT (message_id, consumer_group) DO NOTHING;
   ```
   If the insert returns 0 rows, the event has already been processed by this consumer group and is safely dropped as a duplicate.
3. If new, the domain handler executes business logic, and both the inbox record and domain mutation commit together atomically.
4. **Terminal State Protection**: Domain state machines reject backward transitions if out-of-order events arrive.

## 3. Consequences

### Positive
- **Guaranteed At-Least-Once Delivery**: Events cannot be lost even if Kafka is down when the order is placed.
- **Zero Network I/O in Business Transactions**: Database transactions commit in < 2ms without waiting for Kafka network ACKs.
- **Horizontal Scalability**: Using `FOR UPDATE SKIP LOCKED` allows multiple outbox poller instances to operate concurrently without lock contention or duplicate publishing.
- **Exactly-Once Business Semantics**: The Idempotent Inbox combined with terminal state protection guarantees that duplicate event deliveries have zero corruptive side effects.

### Negative / Trade-offs
- **Eventual Consistency Latency**: Outbox polling introduces a slight propagation delay (5–25ms depending on poll interval).
- **Database Storage Growth**: Outbox and Inbox tables accumulate records and require periodic retention purging (e.g. archiving records older than 7 days).

## 4. Invariants Enforced
- External network calls are strictly banned inside business database transactions.
- All domain events must be written to `outbox_messages` in the same transaction as the aggregate mutation.
- Consumers must enforce unique `(message_id, consumer_group)` deduplication.

## 5. Compliance Verification
- **Tier 2 Test**: `TestOutbox_WorkerCrash_LeaseExpiry` verifies lease reclamation after worker failure.
- **Tier 3 Test**: `TestChoreography_KafkaBrokerRestart_InboxDeduplication` injects Kafka broker failure, forces message redelivery, and verifies zero duplicate domain side-effects.
