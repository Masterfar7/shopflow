import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

// Custom Metrics
export const successfulReservations = new Counter('successful_reservations');
export const rejectedReservations = new Counter('rejected_reservations');
export const oversoldAnomalies = new Counter('oversold_anomalies');
export const orderLatency = new Trend('order_placement_duration');
export const successRate = new Rate('order_success_rate');

// Test Configuration: 100 concurrent VUs executing exactly 1 order each
export const options = {
  scenarios: {
    atomic_overselling_race: {
      executor: 'per-vu-iterations',
      vus: 100,
      iterations: 1,
      maxDuration: '30s',
    },
  },
  thresholds: {
    // Exactly 10 reservations must succeed for 10 available items
    successful_reservations: ['count == 10'],
    // Exactly 90 reservations must be rejected
    rejected_reservations: ['count == 90'],
    // Zero overselling allowed (critical invariant)
    oversold_anomalies: ['count == 0'],
    // Order placement p95 latency must be under 1.5s under concurrency
    order_placement_duration: ['p(95) < 1500'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const TARGET_SKU = __ENV.TARGET_SKU || 'SKU-LIMITED-10';

export default function () {
  const vuId = __VU;
  const idempotencyKey = `k6-atomic-order-${vuId}-${Date.now()}`;

  const payload = JSON.stringify({
    customer_id: `00000000-0000-0000-0000-${String(vuId).padStart(12, '0')}`,
    items: [
      {
        sku: TARGET_SKU,
        quantity: 1,
      },
    ],
  });

  const params = {
    headers: {
      'Content-Type': 'application/json',
      'Idempotency-Key': idempotencyKey,
    },
  };

  const startTime = Date.now();
  const res = http.post(`${BASE_URL}/api/v1/orders`, payload, params);
  orderLatency.add(Date.now() - startTime);

  if (res.status === 201 || res.status === 200) {
    successfulReservations.add(1);
    successRate.add(1);
    check(res, {
      'order created successfully': (r) => r.status === 201 || r.status === 200,
      'order has valid ID': (r) => {
        try {
          const body = JSON.parse(r.body);
          return body.id !== undefined || body.order_id !== undefined;
        } catch (_) {
          return false;
        }
      },
    });
  } else if (res.status === 409 || res.status === 422 || res.status === 400) {
    rejectedReservations.add(1);
    successRate.add(0);
    check(res, {
      'insufficient stock cleanly rejected': (r) =>
        r.status === 409 || r.status === 422 || r.status === 400,
    });
  } else {
    // Unexpected response code
    oversoldAnomalies.add(1);
  }

  sleep(0.1);
}
