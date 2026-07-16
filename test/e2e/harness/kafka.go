package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kgo"
)

// KafkaClient manages Kafka producer and consumer interactions for tests.
type KafkaClient struct {
	Client  *kgo.Client
	Brokers []string
}

// NewKafkaClient constructs a new Franz-Go Kafka client.
func NewKafkaClient(brokers []string, group string) (*KafkaClient, error) {
	if group == "" {
		group = fmt.Sprintf("test-group-%d", time.Now().UnixNano())
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics("orders", "inventory", "payments", "dlq"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create kafka client: %w", err)
	}

	return &KafkaClient{
		Client:  client,
		Brokers: brokers,
	}, nil
}

// Close closes the Franz-Go Kafka client.
func (kc *KafkaClient) Close() {
	if kc.Client != nil {
		kc.Client.Close()
	}
}

// Ping verifies connectivity to Kafka brokers.
func (kc *KafkaClient) Ping(ctx context.Context) error {
	return kc.Client.Ping(ctx)
}

// PublishEvent sends an event record to a specified Kafka topic.
func (kc *KafkaClient) PublishEvent(ctx context.Context, topic, key string, payload []byte, headers map[string]string) error {
	var kHeaders []kgo.RecordHeader
	for k, v := range headers {
		kHeaders = append(kHeaders, kgo.RecordHeader{Key: k, Value: []byte(v)})
	}

	record := &kgo.Record{
		Topic:   topic,
		Key:     []byte(key),
		Value:   payload,
		Headers: kHeaders,
	}

	res := kc.Client.ProduceSync(ctx, record)
	return res.FirstErr()
}

// ConsumeEvents polls for up to maxEvents on a topic within the specified timeout.
func (kc *KafkaClient) ConsumeEvents(ctx context.Context, topic string, maxEvents int, timeout time.Duration) ([]*kgo.Record, error) {
	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var records []*kgo.Record
	for {
		select {
		case <-ctxTimeout.Done():
			return records, nil
		default:
			fetches := kc.Client.PollFetches(ctxTimeout)
			if fetches.IsClientClosed() {
				return records, fmt.Errorf("client closed")
			}
			iter := fetches.RecordIter()
			for !iter.Done() {
				rec := iter.Next()
				if rec.Topic == topic {
					records = append(records, rec)
					if len(records) >= maxEvents {
						return records, nil
					}
				}
			}
		}
	}
}

// AssertEventPublished asserts that an event matching matchFn appears in topic before timeout.
func (kc *KafkaClient) AssertEventPublished(ctx context.Context, t testing.TB, topic string, matchFn func(rec *kgo.Record) bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pollCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		fetches := kc.Client.PollFetches(pollCtx)
		cancel()

		iter := fetches.RecordIter()
		for !iter.Done() {
			rec := iter.Next()
			if rec.Topic == topic && matchFn(rec) {
				return // Found matching event
			}
		}
	}
	require.Fail(t, fmt.Sprintf("expected event matching predicate not found in topic %s within %v", topic, timeout))
}

// Synthetic Event Builders

type CloudEventEnvelope struct {
	SpecVersion string          `json:"specversion"`
	ID          string          `json:"id"`
	Source      string          `json:"source"`
	Type        string          `json:"type"`
	Time        string          `json:"time"`
	Data        json.RawMessage `json:"data"`
}

// BuildOrderCreatedEvent constructs a CloudEvent payload for OrderCreated.
func BuildOrderCreatedEvent(eventID, orderID, userID string, totalMinor int64, currency string) ([]byte, error) {
	data := map[string]interface{}{
		"order_id":           orderID,
		"user_id":            userID,
		"total_amount_minor": totalMinor,
		"currency":           currency,
		"status":             "PENDING",
	}
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	ce := CloudEventEnvelope{
		SpecVersion: "1.0",
		ID:          eventID,
		Source:      "shopflow.orders",
		Type:        "com.shopflow.orders.created",
		Time:        time.Now().UTC().Format(time.RFC3339),
		Data:        dataBytes,
	}
	return json.Marshal(ce)
}

// BuildStockReservedEvent constructs a CloudEvent payload for StockReserved.
func BuildStockReservedEvent(eventID, orderID, reservationID string, skus []string) ([]byte, error) {
	data := map[string]interface{}{
		"order_id":       orderID,
		"reservation_id": reservationID,
		"skus":           skus,
		"status":         "COMMITTED",
	}
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	ce := CloudEventEnvelope{
		SpecVersion: "1.0",
		ID:          eventID,
		Source:      "shopflow.inventory",
		Type:        "com.shopflow.inventory.stock_reserved",
		Time:        time.Now().UTC().Format(time.RFC3339),
		Data:        dataBytes,
	}
	return json.Marshal(ce)
}
