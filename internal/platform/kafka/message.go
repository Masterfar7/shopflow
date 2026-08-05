package kafka

import (
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Message encapsulates a Kafka message with structured headers, key, value, and metadata.
type Message struct {
	Topic     string            `json:"topic"`
	Key       []byte            `json:"key"`
	Value     []byte            `json:"value"`
	Headers   map[string]string `json:"headers,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
	Partition int32             `json:"partition"`
	Offset    int64             `json:"offset"`
}

// ToRecord converts Message to a franz-go *kgo.Record.
func (m Message) ToRecord() *kgo.Record {
	var headers []kgo.RecordHeader
	if len(m.Headers) > 0 {
		headers = make([]kgo.RecordHeader, 0, len(m.Headers))
		for k, v := range m.Headers {
			headers = append(headers, kgo.RecordHeader{
				Key:   k,
				Value: []byte(v),
			})
		}
	}

	timestamp := m.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	return &kgo.Record{
		Topic:     m.Topic,
		Key:       m.Key,
		Value:     m.Value,
		Headers:   headers,
		Timestamp: timestamp,
		Partition: m.Partition,
		Offset:    m.Offset,
	}
}

// FromRecord converts a franz-go *kgo.Record into Message.
func FromRecord(r *kgo.Record) Message {
	var headers map[string]string
	if len(r.Headers) > 0 {
		headers = make(map[string]string, len(r.Headers))
		for _, h := range r.Headers {
			headers[h.Key] = string(h.Value)
		}
	}

	return Message{
		Topic:     r.Topic,
		Key:       r.Key,
		Value:     r.Value,
		Headers:   headers,
		Timestamp: r.Timestamp,
		Partition: r.Partition,
		Offset:    r.Offset,
	}
}
