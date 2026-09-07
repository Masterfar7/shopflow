// web/src/utils.test.ts
import { describe, it, expect } from 'vitest';
import { formatMoney, parseDollarsToMinor, generateUUID } from './utils';

describe('Financial Arithmetic & Formatting (Zero Floats Invariant)', () => {
  it('formats positive minor units correctly', () => {
    expect(formatMoney(1999, 'USD')).toBe('$19.99');
    expect(formatMoney(100, 'USD')).toBe('$1.00');
    expect(formatMoney(5, 'USD')).toBe('$0.05');
    expect(formatMoney(0, 'USD')).toBe('$0.00');
    expect(formatMoney(1000000, 'USD')).toBe('$10,000.00');
  });

  it('formats other currencies with symbols', () => {
    expect(formatMoney(2550, 'EUR')).toBe('€25.50');
    expect(formatMoney(1500, 'GBP')).toBe('£15.00');
    expect(formatMoney(999, 'CAD')).toBe('CAD 9.99');
  });

  it('formats negative minor amounts with minus sign', () => {
    expect(formatMoney(-500, 'USD')).toBe('-$5.00');
    expect(formatMoney(-1999, 'USD')).toBe('-$19.99');
  });

  it('parses dollar input strings to integer minor units', () => {
    expect(parseDollarsToMinor('19.99')).toBe(1999);
    expect(parseDollarsToMinor('19')).toBe(1900);
    expect(parseDollarsToMinor('0.5')).toBe(50);
    expect(parseDollarsToMinor('0.05')).toBe(5);
    expect(parseDollarsToMinor('100.00')).toBe(10000);
  });

  it('rejects invalid or negative price strings', () => {
    expect(parseDollarsToMinor('')).toBeUndefined();
    expect(parseDollarsToMinor('abc')).toBeUndefined();
    expect(parseDollarsToMinor('-10')).toBeUndefined();
    expect(parseDollarsToMinor('10.999')).toBeUndefined();
  });
});

describe('UUID & Idempotency Key Generation', () => {
  it('generates valid RFC 4122 v4 UUIDs', () => {
    const uuid1 = generateUUID();
    const uuid2 = generateUUID();

    const uuidRegex = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

    expect(uuid1).toMatch(uuidRegex);
    expect(uuid2).toMatch(uuidRegex);
    expect(uuid1).not.toBe(uuid2);
  });
});
