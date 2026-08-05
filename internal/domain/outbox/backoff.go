package outbox

import (
	"crypto/rand"
	"math/big"
	"time"
)

// BackoffPolicy configures the exponential retry algorithm with jitter.
type BackoffPolicy struct {
	InitialInterval time.Duration
	MaxInterval     time.Duration
	MaxRetries      int
	EnableJitter    bool
}

// DefaultBackoffPolicy returns sensible production defaults for outbox retries.
func DefaultBackoffPolicy() BackoffPolicy {
	return BackoffPolicy{
		InitialInterval: 500 * time.Millisecond,
		MaxInterval:     30 * time.Second,
		MaxRetries:      5,
		EnableJitter:    true,
	}
}

// Calculate computes the next backoff duration for a given retry count.
// Uses integer arithmetic (bit shifts) and crypto/rand for jitter.
func (p BackoffPolicy) Calculate(retryCount int) time.Duration {
	if retryCount <= 0 {
		return p.InitialInterval
	}

	shift := retryCount
	if shift > 10 { // cap shift at 1024x initial interval
		shift = 10
	}

	multiplier := int64(1 << shift)
	delay := time.Duration(int64(p.InitialInterval) * multiplier)
	if delay > p.MaxInterval {
		delay = p.MaxInterval
	}

	if p.EnableJitter && delay > 0 {
		// Add random jitter up to 25% of delay
		maxJitter := int64(delay / 4)
		if maxJitter > 0 {
			n, err := rand.Int(rand.Reader, big.NewInt(maxJitter))
			if err == nil {
				delay += time.Duration(n.Int64())
			}
		}
	}

	return delay
}

// IsMaxRetriesExceeded reports whether retry count reached or exceeded MaxRetries.
func (p BackoffPolicy) IsMaxRetriesExceeded(retryCount int) bool {
	return retryCount >= p.MaxRetries
}
