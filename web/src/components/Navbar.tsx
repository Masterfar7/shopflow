// web/src/components/Navbar.tsx
import React, { useState } from 'react';
import { ShoppingBag, ShoppingCart, User, RefreshCw, Clock, Check } from 'lucide-react';
import { Cart } from '../types';
import { generateUUID } from '../utils';

interface NavbarProps {
  cart: Cart | null;
  customerId: string;
  onCustomerChange: (newId: string) => void;
  onOpenCart: () => void;
  onOpenOrderHistory: () => void;
}

export const Navbar: React.FC<NavbarProps> = ({
  cart,
  customerId,
  onCustomerChange,
  onOpenCart,
  onOpenOrderHistory,
}) => {
  const [copied, setCopied] = useState(false);
  const [editingUser, setEditingUser] = useState(false);
  const [userInput, setUserInput] = useState(customerId);

  const totalQuantity = cart?.items?.reduce((sum, item) => sum + item.quantity, 0) || 0;

  const handleCopyId = () => {
    navigator.clipboard.writeText(customerId);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const handleNewRandomUser = () => {
    const newId = generateUUID();
    onCustomerChange(newId);
    setUserInput(newId);
  };

  const handleSaveCustomUser = (e: React.FormEvent) => {
    e.preventDefault();
    if (userInput.trim()) {
      onCustomerChange(userInput.trim());
      setEditingUser(false);
    }
  };

  return (
    <header className="sticky top-0 z-30 bg-white border-b border-slate-200 shadow-sm backdrop-blur-md bg-white/90">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 h-16 flex items-center justify-between gap-4">
        {/* Brand Logo */}
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-xl bg-gradient-to-tr from-indigo-600 to-indigo-400 flex items-center justify-center text-white shadow-md shadow-indigo-500/20">
            <ShoppingBag className="w-5 h-5" />
          </div>
          <div>
            <div className="font-bold text-lg text-slate-900 tracking-tight flex items-center gap-1.5">
              ShopFlow <span className="text-xs px-2 py-0.5 rounded-full bg-indigo-50 text-indigo-700 font-semibold border border-indigo-200">v0.1.0</span>
            </div>
            <div className="text-xs text-slate-500 font-medium">Distributed Order Processing</div>
          </div>
        </div>

        {/* Customer Context / Switcher & Cart */}
        <div className="flex items-center gap-3">
          {/* User ID Pill */}
          <div className="hidden md:flex items-center gap-2 bg-slate-100 hover:bg-slate-200/80 transition px-3 py-1.5 rounded-lg border border-slate-200/80 text-xs">
            <User className="w-3.5 h-3.5 text-slate-500" />
            <span className="text-slate-500 font-medium">Customer:</span>
            {editingUser ? (
              <form onSubmit={handleSaveCustomUser} className="flex items-center gap-1">
                <input
                  type="text"
                  value={userInput}
                  onChange={(e) => setUserInput(e.target.value)}
                  className="px-1.5 py-0.5 bg-white border border-indigo-400 rounded text-xs w-48 font-mono"
                  placeholder="UUID"
                  autoFocus
                />
                <button type="submit" className="text-xs text-indigo-600 hover:text-indigo-800 font-semibold px-1">
                  Save
                </button>
                <button
                  type="button"
                  onClick={() => {
                    setEditingUser(false);
                    setUserInput(customerId);
                  }}
                  className="text-xs text-slate-500 hover:text-slate-700 px-1"
                >
                  Cancel
                </button>
              </form>
            ) : (
              <div className="flex items-center gap-1.5">
                <span
                  onClick={() => setEditingUser(true)}
                  className="font-mono text-slate-700 hover:text-indigo-600 cursor-pointer font-semibold"
                  title="Click to edit customer UUID"
                >
                  {customerId.slice(0, 8)}...{customerId.slice(-4)}
                </span>
                <button
                  onClick={handleCopyId}
                  className="text-slate-400 hover:text-slate-600 p-0.5 rounded"
                  title="Copy customer UUID"
                >
                  {copied ? <Check className="w-3 h-3 text-emerald-600" /> : <span className="text-[10px]">📋</span>}
                </button>
                <button
                  onClick={handleNewRandomUser}
                  className="text-slate-400 hover:text-indigo-600 p-0.5 rounded"
                  title="Generate new customer ID"
                >
                  <RefreshCw className="w-3 h-3" />
                </button>
              </div>
            )}
          </div>

          {/* Orders Button */}
          <button
            onClick={onOpenOrderHistory}
            className="flex items-center gap-1.5 px-3 py-2 text-slate-700 hover:text-indigo-600 hover:bg-slate-100 rounded-lg text-sm font-medium transition"
            title="View Order History"
          >
            <Clock className="w-4 h-4" />
            <span className="hidden sm:inline">Orders</span>
          </button>

          {/* Cart Button */}
          <button
            onClick={onOpenCart}
            className="relative flex items-center gap-2 bg-indigo-600 hover:bg-indigo-700 text-white px-4 py-2 rounded-xl text-sm font-semibold shadow-sm shadow-indigo-600/30 transition active:scale-95"
          >
            <ShoppingCart className="w-4 h-4" />
            <span className="hidden sm:inline">Cart</span>
            {totalQuantity > 0 && (
              <span className="bg-amber-400 text-slate-900 text-xs font-bold px-1.5 py-0.5 rounded-full min-w-[20px] text-center">
                {totalQuantity}
              </span>
            )}
          </button>
        </div>
      </div>
    </header>
  );
};
