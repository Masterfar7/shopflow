// web/src/components/ProductCard.tsx
import React, { useState, useEffect } from 'react';
import { ProductDocument, ProductImage } from '../types';
import { formatMoney } from '../utils';
import { getProductImages } from '../api';
import { ChevronLeft, ChevronRight, Check, Plus, Minus, ShoppingCart, Image as ImageIcon } from 'lucide-react';

interface ProductCardProps {
  product: ProductDocument;
  onAddToCart: (sku: string, quantity: number) => Promise<void>;
}

export const ProductCard: React.FC<ProductCardProps> = ({ product, onAddToCart }) => {
  const [images, setImages] = useState<ProductImage[]>([]);
  const [currentImageIndex, setCurrentImageIndex] = useState(0);
  const [quantity, setQuantity] = useState(1);
  const [adding, setAdding] = useState(false);
  const [added, setAdded] = useState(false);
  const [loadingImages, setLoadingImages] = useState(false);

  useEffect(() => {
    let active = true;
    if (product.id) {
      setLoadingImages(true);
      getProductImages(product.id)
        .then((imgs) => {
          if (active && imgs && imgs.length > 0) {
            setImages(imgs);
          }
        })
        .finally(() => {
          if (active) setLoadingImages(false);
        });
    }
    return () => {
      active = false;
    };
  }, [product.id]);

  const handlePrevImage = (e: React.MouseEvent) => {
    e.stopPropagation();
    setCurrentImageIndex((prev) => (prev > 0 ? prev - 1 : images.length - 1));
  };

  const handleNextImage = (e: React.MouseEvent) => {
    e.stopPropagation();
    setCurrentImageIndex((prev) => (prev < images.length - 1 ? prev + 1 : 0));
  };

  const handleAdd = async () => {
    if (!product.in_stock || quantity <= 0) return;
    setAdding(true);
    try {
      await onAddToCart(product.sku, quantity);
      setAdded(true);
      setTimeout(() => setAdded(false), 1500);
    } finally {
      setAdding(false);
    }
  };

  const hasMultipleImages = images.length > 1;
  const currentImage = images[currentImageIndex];

  return (
    <div className="bg-white rounded-2xl border border-slate-200/90 shadow-sm hover:shadow-md transition flex flex-col overflow-hidden group">
      {/* Image / Carousel Section */}
      <div className="relative aspect-[4/3] bg-slate-100 flex items-center justify-center overflow-hidden border-b border-slate-100">
        {currentImage?.url ? (
          <img
            src={currentImage.url}
            alt={product.title}
            className="w-full h-full object-cover object-center group-hover:scale-105 transition duration-300"
            onError={(e) => {
              // Hide broken image and fallback to placeholder
              (e.target as HTMLElement).style.display = 'none';
            }}
          />
        ) : (
          <div className="flex flex-col items-center justify-center text-slate-400 gap-1 p-4 text-center">
            <ImageIcon className="w-10 h-10 stroke-[1.5]" />
            <span className="text-[11px] font-medium text-slate-400">
              {loadingImages ? 'Loading image...' : 'ShopFlow Media'}
            </span>
          </div>
        )}

        {/* Carousel Prev/Next Buttons */}
        {hasMultipleImages && (
          <>
            <button
              onClick={handlePrevImage}
              aria-label="Previous image"
              className="absolute left-2 top-1/2 -translate-y-1/2 p-1.5 rounded-full bg-white/80 hover:bg-white text-slate-700 shadow-sm transition"
            >
              <ChevronLeft className="w-4 h-4" />
            </button>
            <button
              onClick={handleNextImage}
              aria-label="Next image"
              className="absolute right-2 top-1/2 -translate-y-1/2 p-1.5 rounded-full bg-white/80 hover:bg-white text-slate-700 shadow-sm transition"
            >
              <ChevronRight className="w-4 h-4" />
            </button>
            <div className="absolute bottom-2 left-1/2 -translate-x-1/2 flex items-center gap-1 bg-black/40 px-2 py-0.5 rounded-full">
              {images.map((_, idx) => (
                <span
                  key={idx}
                  className={`w-1.5 h-1.5 rounded-full transition ${
                    idx === currentImageIndex ? 'bg-white scale-125' : 'bg-white/50'
                  }`}
                />
              ))}
            </div>
          </>
        )}

        {/* Stock Status Badge */}
        <div className="absolute top-2.5 right-2.5">
          {product.in_stock ? (
            <span className="inline-flex items-center gap-1 px-2.5 py-1 rounded-full text-xs font-semibold bg-emerald-100 text-emerald-800 border border-emerald-200/80 shadow-xs">
              <span className="w-1.5 h-1.5 rounded-full bg-emerald-500 animate-pulse" />
              In Stock
            </span>
          ) : (
            <span className="inline-flex items-center px-2.5 py-1 rounded-full text-xs font-semibold bg-rose-100 text-rose-800 border border-rose-200/80">
              Out of Stock
            </span>
          )}
        </div>
      </div>

      {/* Product Content */}
      <div className="p-4 flex-1 flex flex-col justify-between">
        <div>
          {/* Category & SKU */}
          <div className="flex items-center justify-between text-xs text-slate-500 mb-1">
            <span className="font-medium text-indigo-600 bg-indigo-50 px-2 py-0.5 rounded">
              {product.category_name || 'General'}
            </span>
            <span className="font-mono text-[11px] text-slate-400">SKU: {product.sku}</span>
          </div>

          {/* Title */}
          <h3 className="font-semibold text-slate-900 text-base leading-snug line-clamp-2 hover:text-indigo-600 transition">
            {product.title}
          </h3>

          {/* Description */}
          {product.description && (
            <p className="text-xs text-slate-500 mt-1 line-clamp-2 leading-relaxed">
              {product.description}
            </p>
          )}
        </div>

        {/* Price and Cart Actions */}
        <div className="mt-4 pt-3 border-t border-slate-100 flex flex-col gap-3">
          <div className="flex items-baseline justify-between">
            <span className="text-xs text-slate-500 font-medium">Price</span>
            <span className="text-lg font-bold text-slate-900 tracking-tight">
              {formatMoney(product.price_minor, product.currency)}
            </span>
          </div>

          <div className="flex items-center gap-2">
            {/* Quantity Stepper */}
            <div className="flex items-center border border-slate-200 rounded-xl bg-slate-50 px-1.5 py-1">
              <button
                type="button"
                onClick={() => setQuantity((q) => Math.max(1, q - 1))}
                disabled={!product.in_stock || quantity <= 1 || adding}
                className="p-1 text-slate-500 hover:text-slate-800 disabled:opacity-30 disabled:cursor-not-allowed"
                aria-label="Decrease quantity"
              >
                <Minus className="w-3.5 h-3.5" />
              </button>
              <span className="w-7 text-center font-semibold text-xs text-slate-800 select-none">
                {quantity}
              </span>
              <button
                type="button"
                onClick={() => setQuantity((q) => Math.min(99, q + 1))}
                disabled={!product.in_stock || quantity >= 99 || adding}
                className="p-1 text-slate-500 hover:text-slate-800 disabled:opacity-30 disabled:cursor-not-allowed"
                aria-label="Increase quantity"
              >
                <Plus className="w-3.5 h-3.5" />
              </button>
            </div>

            {/* Add to Cart Button */}
            <button
              onClick={handleAdd}
              disabled={!product.in_stock || adding}
              className={`flex-1 flex items-center justify-center gap-1.5 py-2 px-3 rounded-xl text-xs font-bold transition duration-150 active:scale-95 shadow-sm ${
                added
                  ? 'bg-emerald-600 text-white'
                  : product.in_stock
                  ? 'bg-slate-900 hover:bg-indigo-600 text-white'
                  : 'bg-slate-200 text-slate-400 cursor-not-allowed shadow-none'
              }`}
            >
              {added ? (
                <>
                  <Check className="w-3.5 h-3.5" />
                  <span>Added!</span>
                </>
              ) : adding ? (
                <span>Adding...</span>
              ) : (
                <>
                  <ShoppingCart className="w-3.5 h-3.5" />
                  <span>Add to Cart</span>
                </>
              )}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
};
