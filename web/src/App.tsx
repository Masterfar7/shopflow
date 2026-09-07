// web/src/App.tsx
import React, { useState, useEffect, useCallback } from 'react';
import { Navbar } from './components/Navbar';
import { CatalogView } from './components/CatalogView';
import { CartDrawer } from './components/CartDrawer';
import { CheckoutModal } from './components/CheckoutModal';
import { OrderStatusView } from './components/OrderStatusView';
import { OrderHistoryModal } from './components/OrderHistoryModal';
import { Cart, Order } from './types';
import { getCustomerId, setCustomerId } from './utils';
import { getOrInitActiveCart, addCartItem } from './api';

export const App: React.FC = () => {
  const [customerId, setLocalCustomerId] = useState<string>(getCustomerId());
  const [cart, setCart] = useState<Cart | null>(null);

  // Routing state
  const [activeView, setActiveView] = useState<'catalog' | 'order'>('catalog');
  const [activeOrderId, setActiveOrderId] = useState<string | null>(null);

  // Modals state
  const [cartOpen, setCartOpen] = useState(false);
  const [checkoutOpen, setCheckoutOpen] = useState(false);
  const [orderHistoryOpen, setOrderHistoryOpen] = useState(false);

  // Handle client-side URL routing (e.g. /orders/:orderId)
  useEffect(() => {
    const handlePopState = () => {
      const path = window.location.pathname;
      if (path.startsWith('/orders/')) {
        const id = path.replace('/orders/', '').trim();
        if (id) {
          setActiveOrderId(id);
          setActiveView('order');
          return;
        }
      }
      setActiveView('catalog');
      setActiveOrderId(null);
    };

    handlePopState();
    window.addEventListener('popstate', handlePopState);
    return () => window.removeEventListener('popstate', handlePopState);
  }, []);

  const navigateToOrder = (orderId: string) => {
    setActiveOrderId(orderId);
    setActiveView('order');
    if (window.location.pathname !== `/orders/${orderId}`) {
      window.history.pushState(null, '', `/orders/${orderId}`);
    }
  };

  const navigateToCatalog = () => {
    setActiveView('catalog');
    setActiveOrderId(null);
    if (window.location.pathname !== '/') {
      window.history.pushState(null, '', '/');
    }
  };

  // Load or initialize cart for the active customer
  const refreshCart = useCallback(async () => {
    try {
      const active = await getOrInitActiveCart(customerId);
      setCart(active);
    } catch (err) {
      console.warn('Could not initialize cart', err);
    }
  }, [customerId]);

  useEffect(() => {
    refreshCart();
  }, [refreshCart]);

  const handleCustomerChange = (newId: string) => {
    setCustomerId(newId);
    setLocalCustomerId(newId);
  };

  const handleAddToCart = async (sku: string, quantity: number) => {
    let currentCart = cart;
    if (!currentCart) {
      currentCart = await getOrInitActiveCart(customerId);
      setCart(currentCart);
    }

    try {
      const updated = await addCartItem(
        currentCart.cart_id,
        customerId,
        sku,
        quantity,
        currentCart.version
      );
      setCart(updated);
    } catch (err: any) {
      if (err.status === 412) {
        // Optimistic concurrency conflict: refresh cart and retry
        const refreshed = await getOrInitActiveCart(customerId);
        setCart(refreshed);
        const retried = await addCartItem(
          refreshed.cart_id,
          customerId,
          sku,
          quantity,
          refreshed.version
        );
        setCart(retried);
      } else {
        throw err;
      }
    }
  };

  const handleOrderPlaced = (order: Order) => {
    setCheckoutOpen(false);
    setCartOpen(false);
    // Reload active cart for customer (will be fresh after checkout)
    refreshCart();
    // Navigate to live Saga order tracking view
    navigateToOrder(order.id);
  };

  return (
    <div className="min-h-screen bg-slate-50 text-slate-900 flex flex-col font-sans">
      {/* Top Navigation */}
      <Navbar
        cart={cart}
        customerId={customerId}
        onCustomerChange={handleCustomerChange}
        onOpenCart={() => setCartOpen(true)}
        onOpenOrderHistory={() => setOrderHistoryOpen(true)}
      />

      {/* Main Content Body */}
      <main className="flex-1">
        {activeView === 'catalog' ? (
          <CatalogView onAddToCart={handleAddToCart} />
        ) : activeOrderId ? (
          <OrderStatusView
            orderId={activeOrderId}
            customerId={customerId}
            onBackToCatalog={navigateToCatalog}
          />
        ) : (
          <CatalogView onAddToCart={handleAddToCart} />
        )}
      </main>

      {/* Cart Drawer */}
      <CartDrawer
        isOpen={cartOpen}
        onClose={() => setCartOpen(false)}
        cart={cart}
        customerId={customerId}
        onCartUpdated={(updated) => setCart(updated)}
        onRefreshCart={refreshCart}
        onProceedToCheckout={() => {
          setCartOpen(false);
          setCheckoutOpen(true);
        }}
      />

      {/* Checkout Modal */}
      <CheckoutModal
        isOpen={checkoutOpen}
        onClose={() => setCheckoutOpen(false)}
        cart={cart}
        customerId={customerId}
        onOrderPlaced={handleOrderPlaced}
      />

      {/* Order History Modal */}
      <OrderHistoryModal
        isOpen={orderHistoryOpen}
        onClose={() => setOrderHistoryOpen(false)}
        customerId={customerId}
        onSelectOrder={(id) => navigateToOrder(id)}
      />

      {/* Footer */}
      <footer className="bg-white border-t border-slate-200 py-6 text-center text-xs text-slate-500">
        <div className="max-w-7xl mx-auto px-4 flex flex-col sm:flex-row items-center justify-between gap-2">
          <span>ShopFlow &copy; 2026. High-Performance Distributed Order Platform.</span>
          <span className="font-mono text-slate-400">Strict int64 Minor Money &bull; Zero Overselling</span>
        </div>
      </footer>
    </div>
  );
};

export default App;
