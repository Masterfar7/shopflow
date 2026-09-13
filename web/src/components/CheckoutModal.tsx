// web/src/components/CheckoutModal.tsx
import React, { useState, useEffect } from 'react';
import { X, ShieldCheck, CreditCard, RefreshCw, AlertCircle, Loader2 } from 'lucide-react';
import { Cart, Order } from '../types';
import { formatMoney, generateUUID } from '../utils';
import { createOrder } from '../api';
import { Language, translations } from '../i18n';

interface CheckoutModalProps {
  isOpen: boolean;
  onClose: () => void;
  cart: Cart | null;
  customerId: string;
  onOrderPlaced: (order: Order) => void;
  lang?: Language;
}

export const CheckoutModal: React.FC<CheckoutModalProps> = ({
  isOpen,
  onClose,
  cart,
  customerId,
  onOrderPlaced,
  lang = 'ru',
}) => {
  const t = translations[lang];
  const [idempotencyKey, setIdempotencyKey] = useState(generateUUID());
  const [currency, setCurrency] = useState('USD');
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Generate a fresh idempotency key each time modal opens
  useEffect(() => {
    if (isOpen) {
      setIdempotencyKey(generateUUID());
      setError(null);
      if (cart?.currency) {
        setCurrency(cart.currency);
      }
    }
  }, [isOpen, cart]);

  if (!isOpen || !cart) return null;

  const handleRegenerateKey = () => {
    setIdempotencyKey(generateUUID());
  };

  const handlePlaceOrder = async (e: React.FormEvent) => {
    e.preventDefault();
    if (cart.items.length === 0) {
      setError('Cannot checkout with an empty cart');
      return;
    }

    setIsSubmitting(true);
    setError(null);

    const itemsPayload = cart.items.map((it) => ({
      sku: it.sku,
      quantity: it.quantity,
    }));

    try {
      const order = await createOrder(customerId, idempotencyKey, itemsPayload, currency);
      onOrderPlaced(order);
    } catch (err: any) {
      console.error('Order placement failed', err);
      setError(err.detail || err.message || 'Failed to place order. Please try again.');
    } finally {
      setIsSubmitting(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 overflow-y-auto flex items-center justify-center p-4">
      {/* Backdrop */}
      <div
        onClick={() => !isSubmitting && onClose()}
        className="fixed inset-0 bg-slate-900/60 backdrop-blur-xs transition-opacity"
      />

      {/* Dialog */}
      <div className="relative bg-white rounded-3xl shadow-2xl border border-slate-200 max-w-lg w-full overflow-hidden p-6 sm:p-8 flex flex-col gap-6 z-10 animate-in fade-in zoom-in-95 duration-150">
        {/* Header */}
        <div className="flex items-center justify-between border-b border-slate-100 pb-4">
          <div className="flex items-center gap-2.5">
            <div className="w-9 h-9 rounded-xl bg-indigo-50 text-indigo-600 flex items-center justify-center">
              <CreditCard className="w-5 h-5" />
            </div>
            <div>
              <h3 className="text-lg font-bold text-slate-900">{t.checkoutTitle}</h3>
              <p className="text-xs text-slate-500">{t.checkoutSubtitle}</p>
            </div>
          </div>
          <button
            onClick={onClose}
            disabled={isSubmitting}
            className="p-1.5 rounded-xl text-slate-400 hover:text-slate-600 hover:bg-slate-100 transition disabled:opacity-30"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* Error Alert */}
        {error && (
          <div className="p-3.5 rounded-xl bg-rose-50 border border-rose-200 text-rose-800 text-xs flex items-center gap-2.5">
            <AlertCircle className="w-4 h-4 text-rose-600 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        {/* Order Items Preview */}
        <div>
          <div className="text-xs font-bold text-slate-600 uppercase tracking-wider mb-2">{t.orderSummary}</div>
          <div className="max-h-48 overflow-y-auto divide-y divide-slate-100 border border-slate-200/80 rounded-xl bg-slate-50/50 p-2">
            {cart.items.map((item) => (
              <div key={item.sku} className="py-2 px-2 flex items-center justify-between text-xs">
                <div>
                  <div className="font-semibold text-slate-800">{item.title || item.sku}</div>
                  <div className="text-slate-400 font-mono">
                    {item.quantity} &times; {formatMoney(item.unit_price.amount, item.unit_price.currency)}
                  </div>
                </div>
                <div className="font-bold text-slate-900">
                  {formatMoney(item.line_total.amount, item.line_total.currency)}
                </div>
              </div>
            ))}
          </div>
        </div>

        {/* Order Form & Invariant Headers */}
        <form onSubmit={handlePlaceOrder} className="space-y-4">
          {/* Customer ID & Idempotency Key Info */}
          <div className="bg-slate-50 p-3.5 rounded-xl border border-slate-200/70 space-y-2 text-xs">
            <div className="flex items-center justify-between">
              <span className="text-slate-500 font-medium">{t.customer} ID:</span>
              <span className="font-mono text-slate-700 font-semibold">{customerId.slice(0, 8)}...</span>
            </div>

            <div className="flex items-center justify-between">
              <span className="text-slate-500 font-medium">{lang === 'ru' ? 'Валюта' : 'Currency'}:</span>
              <span className="font-bold text-slate-900">{currency}</span>
            </div>

            <div className="pt-2 border-t border-slate-200/60">
              <div className="flex items-center justify-between mb-1">
                <span className="text-slate-500 font-medium flex items-center gap-1">
                  <ShieldCheck className="w-3.5 h-3.5 text-emerald-600" />
                  Idempotency-Key (UUIDv4):
                </span>
                <button
                  type="button"
                  onClick={handleRegenerateKey}
                  disabled={isSubmitting}
                  className="text-indigo-600 hover:text-indigo-800 p-0.5"
                  title="Generate new UUID"
                >
                  <RefreshCw className="w-3 h-3" />
                </button>
              </div>
              <div className="font-mono text-[11px] text-slate-600 break-all bg-white p-1.5 rounded border border-slate-200">
                {idempotencyKey}
              </div>
            </div>
          </div>

          {/* Grand Total */}
          <div className="flex items-center justify-between text-base font-bold text-slate-900 px-1">
            <span>{t.total}:</span>
            <span className="text-xl text-indigo-700">
              {formatMoney(cart.total_amount.amount, cart.currency)}
            </span>
          </div>

          {/* Submit Button */}
          <button
            type="submit"
            disabled={isSubmitting || cart.items.length === 0}
            className="w-full py-3 px-4 bg-indigo-600 hover:bg-indigo-700 text-white font-bold rounded-xl shadow-lg shadow-indigo-600/25 flex items-center justify-center gap-2 transition active:scale-[0.98] disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {isSubmitting ? (
              <>
                <Loader2 className="w-4 h-4 animate-spin" />
                <span>{t.processingOrder}</span>
              </>
            ) : (
              <span>{t.placeOrderBtn}</span>
            )}
          </button>
        </form>
      </div>
    </div>
  );
};
