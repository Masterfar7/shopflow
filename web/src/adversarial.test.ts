// web/src/adversarial.test.ts
import { describe, it, expect } from 'vitest';
import { formatMoney, parseDollarsToMinor, generateUUID } from './utils';
import {
  addCartItem,
  updateCartItemQuantity,
  removeCartItem,
  clearCart,
  createOrder,
} from './api';

describe('Adversarial Money Stress Testing (Zero Floats)', () => {
  it('handles boundary minor amounts precisely without penny leakage', () => {
    // 0 cents
    expect(formatMoney(0, 'USD')).toBe('$0.00');

    // 1 cent
    expect(formatMoney(1, 'USD')).toBe('$0.01');

    // 9 cents
    expect(formatMoney(9, 'USD')).toBe('$0.09');

    // 10 cents
    expect(formatMoney(10, 'USD')).toBe('$0.10');

    // 99 cents
    expect(formatMoney(99, 'USD')).toBe('$0.99');

    // 100 cents ($1.00)
    expect(formatMoney(100, 'USD')).toBe('$1.00');

    // 105 cents ($1.05)
    expect(formatMoney(105, 'USD')).toBe('$1.05');

    // Negative boundary amounts
    expect(formatMoney(-1, 'USD')).toBe('-$0.01');
    expect(formatMoney(-9, 'USD')).toBe('-$0.09');
    expect(formatMoney(-10, 'USD')).toBe('-$0.10');
    expect(formatMoney(-99, 'USD')).toBe('-$0.99');
    expect(formatMoney(-100, 'USD')).toBe('-$1.00');
    expect(formatMoney(-105, 'USD')).toBe('-$1.05');

    // Large amounts
    expect(formatMoney(123456789, 'USD')).toBe('$1,234,567.89');
    expect(formatMoney(-123456789, 'USD')).toBe('-$1,234,567.89');
  });

  it('proves zero floating-point accumulator drift across 10,000 cents', () => {
    // In IEEE-754: 0.01 + 0.01 + ... 10,000 times drifts.
    // In ShopFlow int64 minor units: strict integer arithmetic has zero drift.
    let minorTotal = 0;
    for (let i = 0; i < 10000; i++) {
      minorTotal += 1;
    }
    expect(minorTotal).toBe(10000);
    expect(formatMoney(minorTotal, 'USD')).toBe('$100.00');
  });

  it('stress-tests parseDollarsToMinor against adversarial and tricky inputs', () => {
    // Normal valid cases
    expect(parseDollarsToMinor('0')).toBe(0);
    expect(parseDollarsToMinor('0.0')).toBe(0);
    expect(parseDollarsToMinor('0.00')).toBe(0);
    expect(parseDollarsToMinor('0.01')).toBe(1);
    expect(parseDollarsToMinor('0.1')).toBe(10);
    expect(parseDollarsToMinor('0.10')).toBe(10);
    expect(parseDollarsToMinor('1.5')).toBe(150);
    expect(parseDollarsToMinor('1.05')).toBe(105);
    expect(parseDollarsToMinor('1.50')).toBe(150);
    expect(parseDollarsToMinor('99999.99')).toBe(9999999);

    // Adversarial / Invalid inputs
    expect(parseDollarsToMinor('')).toBeUndefined();
    expect(parseDollarsToMinor('   ')).toBeUndefined();
    expect(parseDollarsToMinor('0.001')).toBeUndefined(); // 3 decimal places rejected
    expect(parseDollarsToMinor('1.999')).toBeUndefined();
    expect(parseDollarsToMinor('-0.01')).toBeUndefined(); // negative rejected
    expect(parseDollarsToMinor('-10')).toBeUndefined();
    expect(parseDollarsToMinor('1e5')).toBeUndefined(); // scientific notation rejected
    expect(parseDollarsToMinor('NaN')).toBeUndefined();
    expect(parseDollarsToMinor('Infinity')).toBeUndefined();
    expect(parseDollarsToMinor('1,000.00')).toBeUndefined(); // commas rejected
    expect(parseDollarsToMinor('$$10.00')).toBeUndefined();
    expect(parseDollarsToMinor('.50')).toBeUndefined(); // leading dot rejected
    expect(parseDollarsToMinor('50.')).toBeUndefined(); // trailing dot rejected
  });
});

describe('Adversarial UUID & Idempotency Key Stress Testing', () => {
  it('generates 1000 collision-free RFC4122 v4 UUIDs', () => {
    const seen = new Set<string>();
    const uuidRegex = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

    for (let i = 0; i < 1000; i++) {
      const u = generateUUID();
      expect(u).toMatch(uuidRegex);
      expect(seen.has(u)).toBe(false);
      seen.add(u);
    }
    expect(seen.size).toBe(1000);
  });
});

describe('Adversarial Header Verification (If-Match & Idempotency-Key)', () => {
  it('verifies that cart operations attach If-Match with quotes', async () => {
    let capturedHeaders: Headers | null = null;

    // Intercept global fetch
    const originalFetch = global.fetch;
    global.fetch = async (_input: RequestInfo | URL, init?: RequestInit) => {
      capturedHeaders = new Headers(init?.headers);
      return new Response(
        JSON.stringify({
          cart_id: 'c-123',
          customer_id: 'cust-1',
          version: 3,
          items: [],
          total_amount: { amount: 0, currency: 'USD' },
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } }
      );
    };

    try {
      // Test addCartItem
      await addCartItem('c-123', 'cust-1', 'SKU-TEST', 2, 2);
      expect(capturedHeaders).not.toBeNull();
      expect(capturedHeaders!.get('If-Match')).toBe('"2"');
      expect(capturedHeaders!.get('X-User-ID')).toBe('cust-1');

      // Test updateCartItemQuantity
      await updateCartItemQuantity('c-123', 'cust-1', 'SKU-TEST', 5, 3);
      expect(capturedHeaders!.get('If-Match')).toBe('"3"');
      expect(capturedHeaders!.get('X-User-ID')).toBe('cust-1');

      // Test removeCartItem
      await removeCartItem('c-123', 'cust-1', 'SKU-TEST', 4);
      expect(capturedHeaders!.get('If-Match')).toBe('"4"');
      expect(capturedHeaders!.get('X-User-ID')).toBe('cust-1');

      // Test clearCart
      global.fetch = async (_input: RequestInfo | URL, init?: RequestInit) => {
        capturedHeaders = new Headers(init?.headers);
        return new Response(null, { status: 204 });
      };
      await clearCart('c-123', 'cust-1', 5);
      expect(capturedHeaders!.get('If-Match')).toBe('"5"');
      expect(capturedHeaders!.get('X-User-ID')).toBe('cust-1');
    } finally {
      global.fetch = originalFetch;
    }
  });

  it('verifies that order placement attaches Idempotency-Key and customer ID', async () => {
    let capturedHeaders: Headers | null = null;
    let capturedBody: any = null;

    const originalFetch = global.fetch;
    global.fetch = async (_input: RequestInfo | URL, init?: RequestInit) => {
      capturedHeaders = new Headers(init?.headers);
      capturedBody = JSON.parse(init?.body as string);
      return new Response(
        JSON.stringify({
          id: 'ord-123',
          user_id: 'cust-1',
          idempotency_key: capturedHeaders!.get('Idempotency-Key'),
          status: 'PENDING',
          total_amount_minor: 1999,
          currency: 'USD',
          version: 1,
          items: [],
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        }),
        { status: 201, headers: { 'Content-Type': 'application/json' } }
      );
    };

    try {
      const idemKey = generateUUID();
      const order = await createOrder('cust-1', idemKey, [{ sku: 'SKU-1', quantity: 1 }], 'USD');

      expect(capturedHeaders).not.toBeNull();
      expect(capturedHeaders!.get('Idempotency-Key')).toBe(idemKey);
      expect(capturedHeaders!.get('X-User-ID')).toBe('cust-1');
      expect(capturedHeaders!.get('Content-Type')).toBe('application/json');
      expect(capturedBody).toEqual({
        items: [{ sku: 'SKU-1', quantity: 1 }],
        currency: 'USD',
      });
      expect(order.idempotency_key).toBe(idemKey);
    } finally {
      global.fetch = originalFetch;
    }
  });
});
