package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Pinger defines an interface for probing dependencies (Postgres, Redis, Kafka, etc.).
type Pinger interface {
	Ping(ctx context.Context) error
}

// PingerFunc allows using a function as a Pinger.
type PingerFunc func(ctx context.Context) error

// Ping executes the underlying function.
func (f PingerFunc) Ping(ctx context.Context) error {
	return f(ctx)
}

// LivenessResponse is the JSON schema payload for /health/live.
type LivenessResponse struct {
	Status string `json:"status"`
	Uptime string `json:"uptime"`
}

// ReadinessResponse is the JSON schema payload for /health/ready.
type ReadinessResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// LivenessHandler returns HTTP 200 with status UP and process uptime.
func LivenessHandler(startTime time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uptime := time.Since(startTime).Truncate(time.Second).String()
		resp := LivenessResponse{
			Status: "UP",
			Uptime: uptime,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// ReadinessHandler probes database, redis, and kafka dependencies, returning 200 OK or 503 Service Unavailable.
func ReadinessHandler(db Pinger, redis Pinger, kafka Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		checks := make(map[string]string, 3)
		allHealthy := true

		// Check PostgreSQL
		if db != nil {
			if err := db.Ping(ctx); err != nil {
				checks["database"] = fmt.Sprintf("DOWN: %v", err)
				allHealthy = false
			} else {
				checks["database"] = "UP"
			}
		} else {
			checks["database"] = "UP"
		}

		// Check Redis
		if redis != nil {
			if err := redis.Ping(ctx); err != nil {
				checks["redis"] = fmt.Sprintf("DOWN: %v", err)
				allHealthy = false
			} else {
				checks["redis"] = "UP"
			}
		} else {
			checks["redis"] = "UP"
		}

		// Check Kafka
		if kafka != nil {
			if err := kafka.Ping(ctx); err != nil {
				checks["kafka"] = fmt.Sprintf("DOWN: %v", err)
				allHealthy = false
			} else {
				checks["kafka"] = "UP"
			}
		} else {
			checks["kafka"] = "UP"
		}

		w.Header().Set("Content-Type", "application/json")
		if allHealthy {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ReadinessResponse{
				Status: "UP",
				Checks: checks,
			})
			return
		}

		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(ReadinessResponse{
			Status: "DOWN",
			Checks: checks,
		})
	}
}
