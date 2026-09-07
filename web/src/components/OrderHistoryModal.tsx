// web/src/components/OrderHistoryModal.tsx
import React, { useState, useEffect } from 'react';
import { X, Clock, ChevronRight, AlertCircle, RefreshCw } from 'lucide-react';
import { Order } from '../types';
import { formatMoney } from '../utils';
import { listOrders } from '../api';

interface OrderHistoryModalProps {
  isOpen: boolean;
  onClose: () => void;
  customerId: string;
  onSelectOrder: (orderId: string) => void;
}

export const OrderHistoryModal: React.FC<OrderHistoryModalProps> = ({
  isOpen,
  onClose,
  customerId,
  onSelectOrder,
}) => {
  const [orders, setOrders] = useState<Order[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchOrders = async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await listOrders(customerId);
      setOrders(res?.items || []);
    } catch (err: any) {
      setError(err.message || 'Failed to load order history');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (isOpen) {
      fetchOrders();
    }
  }, [isOpen, customerId]);

  if (!isOpen) return null;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
      <div
        onClick={onClose}
        className="fixed inset-0 bg-slate-900/60 backdrop-blur-xs transition-opacity"
      />

      <div className="relative bg-white rounded-3xl shadow-2xl border border-slate-200 max-w-lg w-full overflow-hidden p-6 z-10 flex flex-col max-h-[85vh]">
        {/* Header */}
        <div className="flex items-center justify-between pb-4 border-b border-slate-100">
          <div className="flex items-center gap-2">
            <Clock className="w-5 h-5 text-indigo-600" />
            <h3 className="font-bold text-lg text-slate-900">Order History</h3>
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={fetchOrders}
              className="p-1.5 rounded-lg text-slate-400 hover:text-slate-600 hover:bg-slate-100"
              title="Refresh orders"
            >
              <RefreshCw className="w-4 h-4" />
            </button>
            <button
              onClick={onClose}
              className="p-1.5 rounded-lg text-slate-400 hover:text-slate-600 hover:bg-slate-100"
            >
              <X className="w-5 h-5" />
            </button>
          </div>
        </div>

        {/* Content */}
        <div className="flex-1 overflow-y-auto py-4 space-y-3">
          {loading ? (
            <div className="py-8 text-center text-xs text-slate-500">Loading orders...</div>
          ) : error ? (
            <div className="p-3 bg-rose-50 rounded-xl text-rose-800 text-xs flex items-center gap-2">
              <AlertCircle className="w-4 h-4 shrink-0" />
              <span>{error}</span>
            </div>
          ) : orders.length === 0 ? (
            <div className="py-12 text-center text-slate-400 text-xs">
              No orders found for this customer.
            </div>
          ) : (
            orders.map((ord) => (
              <div
                key={ord.id}
                onClick={() => {
                  onSelectOrder(ord.id);
                  onClose();
                }}
                className="p-4 rounded-xl border border-slate-200 hover:border-indigo-400 hover:bg-indigo-50/30 transition cursor-pointer flex items-center justify-between"
              >
                <div>
                  <div className="flex items-center gap-2 mb-1">
                    <span className="font-mono text-xs font-bold text-slate-800">
                      #{ord.id.slice(0, 8)}
                    </span>
                    <span
                      className={`text-[10px] font-semibold px-2 py-0.5 rounded-full ${
                        ord.status === 'CONFIRMED'
                          ? 'bg-emerald-100 text-emerald-800'
                          : ord.status === 'CANCELLED'
                          ? 'bg-rose-100 text-rose-800'
                          : 'bg-indigo-100 text-indigo-800'
                      }`}
                    >
                      {ord.status}
                    </span>
                  </div>
                  <div className="text-[11px] text-slate-400">
                    {new Date(ord.created_at).toLocaleString()} &bull; {ord.items.length}{' '}
                    {ord.items.length === 1 ? 'item' : 'items'}
                  </div>
                </div>

                <div className="flex items-center gap-2 text-right">
                  <div className="font-bold text-slate-900 text-sm">
                    {formatMoney(ord.total_amount_minor, ord.currency)}
                  </div>
                  <ChevronRight className="w-4 h-4 text-slate-400" />
                </div>
              </div>
            ))
          )}
        </div>
      </div>
    </div>
  );
};
