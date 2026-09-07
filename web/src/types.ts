// web/src/types.ts

export interface Money {
  amount: number; // int64 minor units (e.g. 1999 for $19.99)
  currency: string; // ISO-4217, e.g. "USD"
}

export interface Category {
  id: string;
  slug: string;
  name: string;
  description: string;
  created_at?: string;
  updated_at?: string;
}

export interface ProductImage {
  id: string;
  product_id: string;
  storage_key: string;
  url: string;
  content_type: string;
  size_bytes: number;
  is_primary: boolean;
  sort_order: number;
  created_at?: string;
}

export interface ProductDocument {
  id: string;
  sku: string;
  title: string;
  description: string;
  category_id?: string;
  category_name: string;
  price_minor: number;
  currency: string;
  in_stock: boolean;
}

export interface FacetBucket {
  key: string;
  count: number;
}

export interface Facets {
  categories: FacetBucket[];
}

export interface SearchResult {
  total_hits: number;
  page: number;
  page_size: number;
  total_pages: number;
  products: ProductDocument[];
  facets: Facets;
}

export interface CartItem {
  id: string;
  cart_id: string;
  sku: string;
  title: string;
  quantity: number;
  unit_price: Money;
  line_total: Money;
  created_at?: string;
  updated_at?: string;
}

export interface Cart {
  cart_id: string;
  customer_id: string;
  status: string;
  version: number;
  currency: string;
  items: CartItem[];
  total_amount: Money;
  created_at?: string;
  updated_at?: string;
}

export interface OrderItem {
  id: string;
  order_id: string;
  sku: string;
  title_snapshot: string;
  unit_price_minor: number;
  quantity: number;
  subtotal_minor: number;
  created_at: string;
}

export type OrderStatusType =
  | 'PENDING'
  | 'RESERVING_STOCK'
  | 'STOCK_RESERVED'
  | 'PAYING'
  | 'PAID'
  | 'CONFIRMED'
  | 'CANCELLED'
  | 'REFUNDED';

export interface Order {
  id: string;
  user_id: string;
  idempotency_key: string;
  status: OrderStatusType;
  total_amount_minor: number;
  currency: string;
  version: number;
  items: OrderItem[];
  created_at: string;
  updated_at: string;
}

export interface ProblemDetails {
  type: string;
  title: string;
  status: number;
  detail: string;
  instance?: string;
  code: string;
  invalid_params?: { name: string; reason: string }[];
}
