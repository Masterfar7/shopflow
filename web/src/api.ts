// web/src/api.ts
import {
  Cart,
  Category,
  Order,
  ProductImage,
  SearchResult,
} from './types';

export class ApiError extends Error {
  status: number;
  code?: string;
  detail?: string;

  constructor(status: number, message: string, code?: string, detail?: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.detail = detail;
  }
}

async function handleResponse<T>(res: Response): Promise<T> {
  if (res.status === 204) {
    return {} as T;
  }

  const contentType = res.headers.get('content-type') || '';
  const isJson = contentType.includes('application/json') || contentType.includes('application/problem+json');

  if (!res.ok) {
    let errorDetail = res.statusText;
    let errorCode: string | undefined;

    if (isJson) {
      try {
        const problem = await res.json();
        errorCode = problem.code;
        errorDetail = problem.detail || problem.title || res.statusText;
      } catch {
        // Fallback to status text
      }
    } else {
      const text = await res.text();
      if (text) errorDetail = text;
    }

    throw new ApiError(res.status, errorDetail, errorCode, errorDetail);
  }

  return (await res.json()) as T;
}

export interface SearchParams {
  q?: string;
  category_id?: string;
  min_price?: number; // minor units
  max_price?: number; // minor units
  in_stock?: boolean;
  page?: number;
  page_size?: number;
}

export async function searchProducts(params: SearchParams = {}): Promise<SearchResult> {
  const q = new URLSearchParams();
  if (params.q) q.set('q', params.q);
  if (params.category_id) q.set('category_id', params.category_id);
  if (params.min_price !== undefined) q.set('min_price', params.min_price.toString());
  if (params.max_price !== undefined) q.set('max_price', params.max_price.toString());
  if (params.in_stock !== undefined) q.set('in_stock', params.in_stock.toString());
  if (params.page !== undefined) q.set('page', params.page.toString());
  if (params.page_size !== undefined) q.set('page_size', params.page_size.toString());

  const res = await fetch(`/api/v1/products/search?${q.toString()}`);
  return handleResponse<SearchResult>(res);
}

export async function listCategories(): Promise<Category[]> {
  const res = await fetch('/api/v1/categories');
  return handleResponse<Category[]>(res);
}

export async function getProductImages(productId: string): Promise<ProductImage[]> {
  try {
    const res = await fetch(`/api/v1/products/${productId}/images`);
    if (!res.ok) return [];
    return handleResponse<ProductImage[]>(res);
  } catch {
    return [];
  }
}

export async function getOrInitActiveCart(customerId: string): Promise<Cart> {
  const res = await fetch('/api/v1/carts', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-User-ID': customerId,
    },
    body: JSON.stringify({ customer_id: customerId }),
  });
  return handleResponse<Cart>(res);
}

export async function getCart(cartId: string, customerId: string): Promise<Cart> {
  const res = await fetch(`/api/v1/carts/${cartId}`, {
    headers: {
      'X-User-ID': customerId,
    },
  });
  return handleResponse<Cart>(res);
}

export async function addCartItem(
  cartId: string,
  customerId: string,
  sku: string,
  quantity: number,
  currentVersion: number
): Promise<Cart> {
  const res = await fetch(`/api/v1/carts/${cartId}/items`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-User-ID': customerId,
      'If-Match': `"${currentVersion}"`,
    },
    body: JSON.stringify({ sku, quantity }),
  });
  return handleResponse<Cart>(res);
}

export async function updateCartItemQuantity(
  cartId: string,
  customerId: string,
  sku: string,
  quantity: number,
  currentVersion: number
): Promise<Cart> {
  const res = await fetch(`/api/v1/carts/${cartId}/items/${encodeURIComponent(sku)}`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      'X-User-ID': customerId,
      'If-Match': `"${currentVersion}"`,
    },
    body: JSON.stringify({ quantity }),
  });
  return handleResponse<Cart>(res);
}

export async function removeCartItem(
  cartId: string,
  customerId: string,
  sku: string,
  currentVersion: number
): Promise<Cart> {
  const res = await fetch(`/api/v1/carts/${cartId}/items/${encodeURIComponent(sku)}`, {
    method: 'DELETE',
    headers: {
      'X-User-ID': customerId,
      'If-Match': `"${currentVersion}"`,
    },
  });
  return handleResponse<Cart>(res);
}

export async function clearCart(
  cartId: string,
  customerId: string,
  currentVersion: number
): Promise<void> {
  const res = await fetch(`/api/v1/carts/${cartId}/clear`, {
    method: 'DELETE',
    headers: {
      'X-User-ID': customerId,
      'If-Match': `"${currentVersion}"`,
    },
  });
  await handleResponse<void>(res);
}

export interface CreateOrderItemPayload {
  sku: string;
  quantity: number;
}

export async function createOrder(
  customerId: string,
  idempotencyKey: string,
  items: CreateOrderItemPayload[],
  currency = 'USD'
): Promise<Order> {
  const res = await fetch('/api/v1/orders', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-User-ID': customerId,
      'Idempotency-Key': idempotencyKey,
    },
    body: JSON.stringify({
      items,
      currency,
    }),
  });
  return handleResponse<Order>(res);
}

export async function getOrder(orderId: string, customerId: string): Promise<Order> {
  const res = await fetch(`/api/v1/orders/${orderId}`, {
    headers: {
      'X-User-ID': customerId,
    },
  });
  return handleResponse<Order>(res);
}

export async function listOrders(customerId: string): Promise<{ items: Order[]; has_more: boolean }> {
  const res = await fetch('/api/v1/orders', {
    headers: {
      'X-User-ID': customerId,
    },
  });
  return handleResponse<{ items: Order[]; has_more: boolean }>(res);
}

export async function cancelOrder(
  orderId: string,
  customerId: string,
  idempotencyKey: string,
  reason = 'Customer requested cancellation'
): Promise<void> {
  const res = await fetch(`/api/v1/orders/${orderId}/cancel`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-User-ID': customerId,
      'Idempotency-Key': idempotencyKey,
    },
    body: JSON.stringify({ reason }),
  });
  await handleResponse<void>(res);
}
