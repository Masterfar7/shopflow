// web/src/components/CartDrawer.tsx
import React, { useState } from 'react';
import { X, Trash2, Plus, Minus, ShoppingBag, ArrowRight, AlertTriangle, RefreshCw } from 'lucide-react';
import { Cart } from '../types';
import { formatMoney } from '../utils';
import { updateCartItemQuantity, removeCartItem, clearCart } from '../api';
import { Language, translations } from '../i18n';

interface CartDrawerProps {
  isOpen: boolean;
  onClose: () => void;
  cart: Cart | null;
  customerId: string;
  onCartUpdated: (updatedCart: Cart) => void;
  onRefreshCart: () => Promise<void>;
  onProceedToCheckout: () => void;
  lang?: Language;
}

export const CartDrawer: React.FC<CartDrawerProps> = ({
  isOpen,
  onClose,
  cart,
  customerId,
  onCartUpdated,
  onRefreshCart,
  onProceedToCheckout,
  lang = 'ru',
}) => {
  const t = translations[lang];
  const [updatingSku, setUpdatingSku] = useState<string | null>(null);
  const [errorNotice, setErrorNotice] = useState<string | null>(null);
  const [clearing, setClearing] = useState(false);

  if (!isOpen) return null;

  const items = cart?.items || [];
  const isEmpty = items.length === 0;

  const handleUpdateQuantity = async (sku: string, currentQty: number, delta: number) => {
    if (!cart) return;
    const newQty = currentQty + delta;
    setUpdatingSku(sku);
    setErrorNotice(null);

    try {
      if (newQty <= 0) {
        const updated = await removeCartItem(cart.cart_id, customerId, sku, cart.version);
        onCartUpdated(updated);
      } else {
        const updated = await updateCartItemQuantity(cart.cart_id, customerId, sku, newQty, cart.version);
        onCartUpdated(updated);
      }
    } catch (err: any) {
      if (err.status === 412) {
        setErrorNotice('Concurrency conflict: Cart was modified in another session. Refreshing...');
        await onRefreshCart();
      } else {
        setErrorNotice(err.message || 'Failed to update item quantity');
      }
    } finally {
      setUpdatingSku(null);
    }
  };

  const handleRemove = async (sku: string) => {
    if (!cart) return;
    setUpdatingSku(sku);
    setErrorNotice(null);

    try {
      const updated = await removeCartItem(cart.cart_id, customerId, sku, cart.version);
      onCartUpdated(updated);
    } catch (err: any) {
      if (err.status === 412) {
        setErrorNotice('Concurrency conflict: Cart was modified in another session. Refreshing...');
        await onRefreshCart();
      } else {
        setErrorNotice(err.message || 'Failed to remove item');
      }
    } finally {
      setUpdatingSku(null);
    }
  };

  const handleClear = async () => {
    if (!cart || isEmpty) return;
    setClearing(true);
    setErrorNotice(null);
    try {
      await clearCart(cart.cart_id, customerId, cart.version);
      await onRefreshCart();
    } catch (err: any) {
      if (err.status === 412) {
        setErrorNotice('Concurrency conflict: Cart was modified in another session. Refreshing...');
        await onRefreshCart();
      } else {
        setErrorNotice(err.message || 'Failed to clear cart');
      }
    } finally {
      setClearing(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 overflow-hidden">
      {/* Backdrop */}
      <div
        onClick={onClose}
        className="absolute inset-0 bg-slate-900/50 backdrop-blur-xs transition-opacity"
      />

      <div className="fixed inset-y-0 right-0 max-w-full flex pl-10">
        <div className="w-screen max-w-md bg-white shadow-2xl flex flex-col">
          {/* Header */}
          <div className="p-4 sm:p-6 border-b border-slate-200 flex items-center justify-between">
            <div className="flex items-center gap-2">
              <ShoppingBag className="w-5 h-5 text-indigo-600" />
              <h2 className="text-lg font-bold text-slate-900">{t.cartTitle}</h2>
              {cart && (
                <span
                  className="text-[11px] font-mono bg-slate-100 text-slate-600 px-2 py-0.5 rounded-full border border-slate-200"
                  title="Optimistic Concurrency Version"
                >
                  v{cart.version}
                </span>
              )}
            </div>
            <button
              onClick={onClose}
              className="p-1.5 rounded-xl text-slate-400 hover:text-slate-600 hover:bg-slate-100 transition"
              aria-label="Close cart"
            >
              <X className="w-5 h-5" />
            </button>
          </div>

          {/* Conflict or Error Notification Banner */}
          {errorNotice && (
            <div className="p-3 bg-amber-50 border-b border-amber-200 text-amber-900 text-xs flex items-center justify-between gap-2">
              <div className="flex items-center gap-2">
                <AlertTriangle className="w-4 h-4 text-amber-600 shrink-0" />
                <span>{errorNotice}</span>
              </div>
              <button
                onClick={() => onRefreshCart()}
                className="p-1 text-amber-700 hover:text-amber-900"
                title="Force refresh"
              >
                <RefreshCw className="w-3.5 h-3.5" />
              </button>
            </div>
          )}

          {/* Cart Items List */}
          <div className="flex-1 overflow-y-auto p-4 sm:p-6 space-y-4">
            {isEmpty ? (
              <div className="h-full flex flex-col items-center justify-center text-center text-slate-500 py-12">
                <div className="w-16 h-16 rounded-full bg-slate-100 flex items-center justify-center text-slate-400 mb-3">
                  <ShoppingBag className="w-8 h-8" />
                </div>
                <h3 className="text-base font-semibold text-slate-800">{t.emptyCartTitle}</h3>
                <p className="text-xs text-slate-500 mt-1 max-w-xs">
                  {t.emptyCartDesc}
                </p>
                <button
                  onClick={onClose}
                  className="mt-4 px-4 py-2 bg-indigo-50 hover:bg-indigo-100 text-indigo-700 rounded-xl text-xs font-semibold transition"
                >
                  {t.continueShopping}
                </button>
              </div>
            ) : (
              items.map((item) => {
                const isUpdating = updatingSku === item.sku;
                return (
                  <div
                    key={item.sku}
                    className="p-3.5 rounded-xl border border-slate-200/90 bg-slate-50/50 flex flex-col gap-2.5 transition"
                  >
                    <div className="flex items-start justify-between gap-3">
                      <div>
                        <h4 className="font-semibold text-sm text-slate-900 leading-snug">
                          {item.title || item.sku}
                        </h4>
                        <span className="font-mono text-[11px] text-slate-400">{t.sku}: {item.sku}</span>
                      </div>
                      <button
                        onClick={() => handleRemove(item.sku)}
                        disabled={isUpdating}
                        className="p-1 text-slate-400 hover:text-rose-600 transition disabled:opacity-30"
                        title={lang === 'ru' ? 'Удалить' : 'Remove item'}
                      >
                        <Trash2 className="w-4 h-4" />
                      </button>
                    </div>

                    <div className="flex items-center justify-between pt-2 border-t border-slate-200/60">
                      {/* Price breakdown */}
                      <div className="text-xs">
                        <span className="text-slate-500">
                          {formatMoney(item.unit_price.amount, item.unit_price.currency)} &times; {item.quantity}
                        </span>
                        <div className="font-bold text-slate-900 text-sm">
                          {formatMoney(item.line_total.amount, item.line_total.currency)}
                        </div>
                      </div>

                      {/* Quantity Controls */}
                      <div className="flex items-center border border-slate-200 rounded-lg bg-white px-1 py-0.5">
                        <button
                          onClick={() => handleUpdateQuantity(item.sku, item.quantity, -1)}
                          disabled={isUpdating}
                          className="p-1 text-slate-500 hover:text-slate-900 disabled:opacity-30"
                          aria-label="Decrease quantity"
                        >
                          <Minus className="w-3.5 h-3.5" />
                        </button>
                        <span className="w-7 text-center font-semibold text-xs text-slate-900 select-none">
                          {isUpdating ? '...' : item.quantity}
                        </span>
                        <button
                          onClick={() => handleUpdateQuantity(item.sku, item.quantity, 1)}
                          disabled={isUpdating}
                          className="p-1 text-slate-500 hover:text-slate-900 disabled:opacity-30"
                          aria-label="Increase quantity"
                        >
                          <Plus className="w-3.5 h-3.5" />
                        </button>
                      </div>
                    </div>
                  </div>
                );
              })
            )}
          </div>

          {/* Footer with Totals and Checkout */}
          {!isEmpty && cart && (
            <div className="p-4 sm:p-6 border-t border-slate-200 bg-slate-50/70 space-y-4">
              {/* Server-calculated totals in minor units */}
              <div className="space-y-1.5 text-sm">
                <div className="flex items-center justify-between text-slate-500 text-xs">
                  <span>{t.subtotal}</span>
                  <span>{formatMoney(cart.total_amount.amount, cart.currency)}</span>
                </div>
                <div className="flex items-center justify-between text-slate-500 text-xs">
                  <span>{t.shipping}</span>
                  <span className="text-emerald-600 font-medium">{t.free}</span>
                </div>
                <div className="flex items-center justify-between text-slate-900 font-bold text-base pt-2 border-t border-slate-200">
                  <span>{t.total}</span>
                  <span className="text-lg">
                    {formatMoney(cart.total_amount.amount, cart.currency)}
                  </span>
                </div>
              </div>

              {/* Action Buttons */}
              <div className="flex flex-col gap-2">
                <button
                  onClick={onProceedToCheckout}
                  className="w-full py-3 px-4 bg-indigo-600 hover:bg-indigo-700 text-white font-semibold rounded-xl shadow-md shadow-indigo-600/25 flex items-center justify-center gap-2 transition active:scale-[0.98]"
                >
                  <span>{t.checkoutBtn}</span>
                  <ArrowRight className="w-4 h-4" />
                </button>

                <button
                  onClick={handleClear}
                  disabled={clearing}
                  className="w-full py-2 text-xs font-semibold text-slate-500 hover:text-rose-600 transition disabled:opacity-40"
                >
                  {clearing ? (lang === 'ru' ? 'Очистка...' : 'Clearing cart...') : t.clearCart}
                </button>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
};
