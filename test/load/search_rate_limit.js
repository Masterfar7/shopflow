import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

// Custom Metrics
export const totalSearchRequests = new Counter('total_search_requests');
export const allowedRequests = new Counter('allowed_search_requests');
export const rateLimitedRequests = new Counter('rate_limited_429_requests');
export const rateLimitRejectionRate = new Rate('rate_limit_rejection_rate');
export const searchDuration = new Trend('search_request_duration');

// Test Configuration: 50 VUs flooding the search endpoint to trigger rate limits (100 req/min limit)
export const options = {
  scenarios: {
    flood_catalog_search: {
      executor: 'constant-vus',
      vus: 30,
      duration: '15s',
    },
  },
  thresholds: {
    // Rate limit must trigger: at least some 429 Too Many Requests responses expected
    rate_limited_429_requests: ['count > 0'],
    // 95% of requests should complete within 500ms
    search_request_duration: ['p(95) < 500'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

export default function () {
  const queryTerms = ['pro', 'wireless', 'keyboard', 'laptop', 'camera', 'phone'];
  const term = queryTerms[Math.floor(Math.random() * queryTerms.length)];

  totalSearchRequests.add(1);

  const startTime = Date.now();
  const res = http.get(`${BASE_URL}/api/v1/products/search?q=${term}&limit=10`);
  searchDuration.add(Date.now() - startTime);

  if (res.status === 200) {
    allowedRequests.add(1);
    rateLimitRejectionRate.add(0);
    check(res, {
      'search status 200 OK': (r) => r.status === 200,
      'has rate limit remaining header': (r) => r.headers['X-Ratelimit-Remaining'] !== undefined,
    });
  } else if (res.status === 429) {
    rateLimitedRequests.add(1);
    rateLimitRejectionRate.add(1);
    check(res, {
      'status 429 Too Many Requests': (r) => r.status === 429,
      'has Retry-After header': (r) => r.headers['Retry-After'] !== undefined,
    });
  }

  // Small pacing interval
  sleep(0.05);
}
