// web/src/components/OrderStatusView.tsx
import React, { useState, useEffect, useCallback, useRef } from 'react';
import {
  CheckCircle2,
  Clock,
  Layers,
  XCircle,
  RotateCw,
  ArrowLeft,
  ShieldCheck,
  Package,
  AlertTriangle,
} from 'lucide-react';
import { Order, OrderStatusType } from '../types';
import { formatMoney, generateUUID } from '../utils';
import { getOrder, cancelOrder } from '../api';

interface OrderStatusViewProps {
  orderId: string;
  customerId: string;
  onBackToCatalog: () => void;
}

export const OrderStatusView: React.FC<OrderStatusViewProps> = ({
  orderId,
  customerId,
  onBackToCatalog,
}) => {
  const [order, setOrder] = useState<Order | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [cancelling, setCancelling] = useState(false);
  const [cancelReason, setCancelReason] = useState('Customer cancellation request');
  const [showCancelDialog, setShowCancelDialog] = useState(false);
  const pollTimerRef = useRef<NodeJS.Timeout | null>(null);

  const isTerminal = (status?: OrderStatusType): boolean => {
    return status === 'CONFIRMED' || status === 'CANCELLED' || status === 'REFUNDED';
  };

  const fetchOrderDetails = useCallback(async () => {
    try {
      const data = await getOrder(orderId, customerId);
      setOrder(data);
      setError(null);
      return data;
    } catch (err: any) {
      console.error('Failed to poll order status', err);
      setError(err.detail || err.message || 'Failed to retrieve order status');
      return null;
    } finally {
      setLoading(false);
    }
  }, [orderId, customerId]);

  // Polling loop
  useEffect(() => {
    let mounted = true;

    const poll = async () => {
      const current = await fetchOrderDetails();
      if (!mounted) return;

      if (current && !isTerminal(current.status)) {
        pollTimerRef.current = setTimeout(poll, 1500);
      }
    };

    poll();

    return () => {
      mounted = false;
      if (pollTimerRef.current) {
        clearTimeout(pollTimerRef.current);
      }
    };
  }, [fetchOrderDetails]);

  const handleCancel = async () => {
    if (!order) return;
    setCancelling(true);
    try {
      const cancelIdemKey = generateUUID();
      await cancelOrder(order.id, customerId, cancelIdemKey, cancelReason);
      setShowCancelDialog(false);
      await fetchOrderDetails();
    } catch (err: any) {
      setError(err.detail || err.message || 'Failed to cancel order');
    } finally {
      setCancelling(false);
    }
  };

  const status = order?.status;

  // Determine stage progression for Saga visualization:
  // Step 1: PENDING
  // Step 2: RESERVED (RESERVING_STOCK, STOCK_RESERVED, PAYING, PAID)
  // Step 3: CONFIRMED (or CANCELLED)
  const isCancelled = status === 'CANCELLED' || status === 'REFUNDED';
  const isPending = status === 'PENDING';
  const isReserved =
    status === 'RESERVING_STOCK' ||
    status === 'STOCK_RESERVED' ||
    status === 'PAYING' ||
    status === 'PAID' ||
    status === 'CONFIRMED';
  const isConfirmed = status === 'CONFIRMED';

  return (
    <div className="max-w-4xl mx-auto px-4 sm:px-6 lg:px-8 py-10">
      {/* Top Navigation */}
      <button
        onClick={onBackToCatalog}
        className="flex items-center gap-2 text-xs font-semibold text-slate-500 hover:text-indigo-600 transition mb-6"
      >
        <ArrowLeft className="w-4 h-4" />
        Back to Catalog
      </button>

      {/* Main Status Card */}
      <div className="bg-white rounded-3xl border border-slate-200 shadow-sm overflow-hidden p-6 sm:p-8 space-y-8">
        {/* Header */}
        <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 pb-6 border-b border-slate-100">
          <div>
            <div className="flex items-center gap-2">
              <Package className="w-6 h-6 text-indigo-600" />
              <h1 className="text-2xl font-black text-slate-900 tracking-tight">Order Status</h1>
            </div>
            <div className="text-xs text-slate-500 mt-1 font-mono">ID: {orderId}</div>
          </div>

          <div className="flex items-center gap-2">
            {!isTerminal(status) && (
              <span className="flex items-center gap-1.5 text-xs font-medium text-indigo-700 bg-indigo-50 px-3 py-1.5 rounded-full border border-indigo-200/80">
                <RotateCw className="w-3.5 h-3.5 animate-spin text-indigo-600" />
                Live Polling Saga
              </span>
            )}
            <button
              onClick={() => fetchOrderDetails()}
              disabled={loading}
              className="p-2 rounded-xl text-slate-500 hover:text-slate-800 hover:bg-slate-100 transition border border-slate-200 text-xs font-semibold"
              title="Refresh now"
            >
              <RotateCw className="w-3.5 h-3.5" />
            </button>
          </div>
        </div>

        {/* Error Notification */}
        {error && (
          <div className="p-4 rounded-xl bg-rose-50 border border-rose-200 text-rose-800 text-xs flex items-center gap-2">
            <AlertTriangle className="w-4 h-4 text-rose-600 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        {/* Saga State Machine Stepper Visualization */}
        <div className="bg-slate-50/80 rounded-2xl p-6 border border-slate-200/70">
          <div className="text-xs font-bold uppercase tracking-wider text-slate-500 mb-6 flex items-center gap-1.5">
            <Layers className="w-4 h-4 text-indigo-600" />
            Saga State Machine Transitions
          </div>

          <div className="relative flex flex-col md:flex-row items-start md:items-center justify-between gap-6">
            {/* Step 1: PENDING */}
            <div className="flex items-center gap-3.5 z-10">
              <div
                className={`w-10 h-10 rounded-2xl flex items-center justify-center transition shadow-sm ${
                  isPending || isReserved || isConfirmed
                    ? 'bg-indigo-600 text-white shadow-indigo-500/25'
                    : 'bg-slate-200 text-slate-500'
                }`}
              >
                <Clock className="w-5 h-5" />
              </div>
              <div>
                <div className="font-bold text-sm text-slate-900">PENDING</div>
                <div className="text-[11px] text-slate-500">Order placement registered</div>
              </div>
            </div>

            {/* Connector 1 */}
            <div
              className={`hidden md:block flex-1 h-1 transition-colors ${
                isReserved || isConfirmed ? 'bg-indigo-600' : 'bg-slate-200'
              }`}
            />

            {/* Step 2: RESERVED */}
            <div className="flex items-center gap-3.5 z-10">
              <div
                className={`w-10 h-10 rounded-2xl flex items-center justify-center transition shadow-sm ${
                  isReserved
                    ? 'bg-indigo-600 text-white shadow-indigo-500/25'
                    : isCancelled
                    ? 'bg-slate-200 text-slate-400'
                    : 'bg-slate-200 text-slate-500'
                }`}
              >
                <Layers className="w-5 h-5" />
              </div>
              <div>
                <div className="font-bold text-sm text-slate-900">RESERVED</div>
                <div className="text-[11px] text-slate-500">Atomic inventory locking</div>
              </div>
            </div>

            {/* Connector 2 */}
            <div
              className={`hidden md:block flex-1 h-1 transition-colors ${
                isConfirmed
                  ? 'bg-indigo-600'
                  : isCancelled
                  ? 'bg-rose-500'
                  : 'bg-slate-200'
              }`}
            />

            {/* Step 3: CONFIRMED or CANCELLED */}
            <div className="flex items-center gap-3.5 z-10">
              <div
                className={`w-10 h-10 rounded-2xl flex items-center justify-center transition shadow-sm ${
                  isConfirmed
                    ? 'bg-emerald-600 text-white shadow-emerald-500/25'
                    : isCancelled
                    ? 'bg-rose-600 text-white shadow-rose-500/25'
                    : 'bg-slate-200 text-slate-400'
                }`}
              >
                {isConfirmed ? (
                  <CheckCircle2 className="w-5 h-5" />
                ) : isCancelled ? (
                  <XCircle className="w-5 h-5" />
                ) : (
                  <CheckCircle2 className="w-5 h-5 text-slate-400" />
                )}
              </div>
              <div>
                <div
                  className={`font-bold text-sm ${
                    isConfirmed
                      ? 'text-emerald-700'
                      : isCancelled
                      ? 'text-rose-700'
                      : 'text-slate-400'
                  }`}
                >
                  {isCancelled ? 'CANCELLED' : 'CONFIRMED'}
                </div>
                <div className="text-[11px] text-slate-500">
                  {isConfirmed
                    ? 'Payment settled & completed'
                    : isCancelled
                    ? 'Stock released & refunded'
                    : 'Awaiting payment confirmation'}
                </div>
              </div>
            </div>
          </div>

          {/* Current Raw State Badge */}
          <div className="mt-6 pt-4 border-t border-slate-200/60 flex items-center justify-between text-xs">
            <span className="text-slate-500 font-medium">Detailed Aggregate State:</span>
            <span
              className={`font-mono font-bold px-2.5 py-1 rounded-md text-xs ${
                status === 'CONFIRMED'
                  ? 'bg-emerald-100 text-emerald-800'
                  : status === 'CANCELLED' || status === 'REFUNDED'
                  ? 'bg-rose-100 text-rose-800'
                  : 'bg-indigo-100 text-indigo-800 animate-pulse'
              }`}
            >
              {status || 'LOADING...'}
            </span>
          </div>
        </div>

        {/* Order Details & Line Items */}
        {order && (
          <div className="space-y-4">
            <h3 className="text-sm font-bold uppercase tracking-wider text-slate-700">
              Snapshot Line Items
            </h3>

            <div className="border border-slate-200 rounded-2xl overflow-hidden divide-y divide-slate-100">
              {order.items.map((it) => (
                <div key={it.id || it.sku} className="p-4 flex items-center justify-between text-sm">
                  <div>
                    <div className="font-semibold text-slate-900">{it.title_snapshot || it.sku}</div>
                    <div className="text-xs text-slate-400 font-mono">
                      SKU: {it.sku} &bull; Qty: {it.quantity} &bull; Unit:{' '}
                      {formatMoney(it.unit_price_minor, order.currency)}
                    </div>
                  </div>
                  <div className="font-bold text-slate-900">
                    {formatMoney(it.subtotal_minor, order.currency)}
                  </div>
                </div>
              ))}
            </div>

            {/* Total and Metadata */}
            <div className="bg-slate-50 p-4 rounded-2xl border border-slate-200/70 flex flex-col sm:flex-row sm:items-center justify-between gap-4 text-xs">
              <div className="space-y-1">
                <div className="text-slate-500 flex items-center gap-1.5">
                  <ShieldCheck className="w-3.5 h-3.5 text-indigo-600" />
                  <span className="font-medium">Idempotency Key:</span>
                  <span className="font-mono text-slate-700">{order.idempotency_key}</span>
                </div>
                <div className="text-slate-500">
                  <span className="font-medium">Created:</span>{' '}
                  {new Date(order.created_at).toLocaleString()}
                </div>
              </div>

              <div className="text-right">
                <div className="text-slate-500 font-medium">Grand Total</div>
                <div className="text-xl font-black text-slate-900">
                  {formatMoney(order.total_amount_minor, order.currency)}
                </div>
              </div>
            </div>
          </div>
        )}

        {/* Action Buttons */}
        <div className="flex flex-wrap items-center justify-between gap-4 pt-4 border-t border-slate-100">
          <button
            onClick={onBackToCatalog}
            className="px-5 py-2.5 bg-slate-900 hover:bg-slate-800 text-white text-xs font-semibold rounded-xl transition active:scale-95"
          >
            Continue Shopping
          </button>

          {order && !isTerminal(order.status) && (
            <button
              onClick={() => setShowCancelDialog(true)}
              className="px-4 py-2.5 border border-rose-200 text-rose-700 hover:bg-rose-50 text-xs font-semibold rounded-xl transition"
            >
              Cancel Order
            </button>
          )}
        </div>
      </div>

      {/* Cancel Order Confirmation Dialog */}
      {showCancelDialog && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-slate-900/60 backdrop-blur-xs">
          <div className="bg-white rounded-2xl max-w-sm w-full p-6 space-y-4 border border-slate-200 shadow-2xl">
            <h4 className="font-bold text-base text-slate-900">Cancel Order</h4>
            <p className="text-xs text-slate-500">
              Are you sure you want to cancel this order? This will release reserved inventory stock and trigger saga compensation.
            </p>
            <div>
              <label className="text-xs font-semibold text-slate-700 block mb-1">Reason</label>
              <input
                type="text"
                value={cancelReason}
                onChange={(e) => setCancelReason(e.target.value)}
                className="w-full text-xs p-2 border border-slate-200 rounded-lg outline-none focus:border-indigo-500"
              />
            </div>
            <div className="flex items-center justify-end gap-2 pt-2">
              <button
                onClick={() => setShowCancelDialog(false)}
                disabled={cancelling}
                className="px-3 py-1.5 text-xs text-slate-600 hover:bg-slate-100 rounded-lg"
              >
                Keep Order
              </button>
              <button
                onClick={handleCancel}
                disabled={cancelling}
                className="px-4 py-1.5 text-xs font-semibold bg-rose-600 hover:bg-rose-700 text-white rounded-lg transition"
              >
                {cancelling ? 'Cancelling...' : 'Confirm Cancel'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};
