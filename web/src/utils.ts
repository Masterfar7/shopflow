// web/src/utils.ts

/**
 * Format integer minor units (e.g. 1999 cents) to currency string (e.g. "$19.99").
 * Strictly adheres to Zero Floats for Money invariant.
 */
export function formatMoney(amountMinor: number, currency = 'USD'): string {
  if (isNaN(amountMinor)) {
    return '$0.00';
  }
  const isNegative = amountMinor < 0;
  const abs = Math.abs(amountMinor);
  const dollars = Math.floor(abs / 100);
  const cents = (abs % 100).toString().padStart(2, '0');
  const formattedDigits = `${dollars.toLocaleString('en-US')}.${cents}`;
  const sign = isNegative ? '-' : '';

  switch (currency.toUpperCase()) {
    case 'USD':
      return `${sign}$${formattedDigits}`;
    case 'EUR':
      return `${sign}€${formattedDigits}`;
    case 'GBP':
      return `${sign}£${formattedDigits}`;
    default:
      return `${sign}${currency.toUpperCase()} ${formattedDigits}`;
  }
}

/**
 * Converts user-entered dollar amount string (e.g. "19.99" or "50") to integer minor units (e.g. 1999 or 5000).
 * Returns undefined if invalid or empty.
 */
export function parseDollarsToMinor(val: string): number | undefined {
  const trimmed = val.trim();
  if (!trimmed) return undefined;

  // Validate decimal number pattern
  if (!/^\d+(\.\d{1,2})?$/.test(trimmed)) {
    return undefined;
  }

  const parts = trimmed.split('.');
  const dollars = parseInt(parts[0], 10);
  let cents = 0;
  if (parts.length > 1) {
    cents = parseInt(parts[1].padEnd(2, '0').slice(0, 2), 10);
  }

  const totalMinor = dollars * 100 + cents;
  if (totalMinor < 0 || isNaN(totalMinor)) {
    return undefined;
  }
  return totalMinor;
}

/**
 * Generates a standard RFC 4122 v4 UUID.
 */
export function generateUUID(): string {
  if (typeof crypto !== 'undefined' && crypto.randomUUID) {
    return crypto.randomUUID();
  }
  // Fallback RFC4122 v4 UUID generator
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    const v = c === 'x' ? r : (r & 0x3) | 0x8;
    return v.toString(16);
  });
}

const CUSTOMER_ID_KEY = 'shopflow_customer_id';

/**
 * Retrieves the persistent customer UUID from localStorage, or generates and persists a new one.
 */
export function getCustomerId(): string {
  if (typeof window === 'undefined') return generateUUID();
  let id = localStorage.getItem(CUSTOMER_ID_KEY);
  if (!id || id.trim() === '') {
    id = generateUUID();
    localStorage.setItem(CUSTOMER_ID_KEY, id);
  }
  return id;
}

/**
 * Sets a new customer UUID in localStorage.
 */
export function setCustomerId(id: string): void {
  if (typeof window !== 'undefined') {
    localStorage.setItem(CUSTOMER_ID_KEY, id.trim());
  }
}
