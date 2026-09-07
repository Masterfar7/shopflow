// web/src/components/CatalogView.tsx
import React, { useState, useEffect, useCallback } from 'react';
import { Search, SlidersHorizontal, RotateCcw, ChevronLeft, ChevronRight, AlertCircle, PackageSearch } from 'lucide-react';
import { Category, SearchResult } from '../types';
import { listCategories, searchProducts } from '../api';
import { ProductCard } from './ProductCard';
import { parseDollarsToMinor } from '../utils';

interface CatalogViewProps {
  onAddToCart: (sku: string, quantity: number) => Promise<void>;
}

export const CatalogView: React.FC<CatalogViewProps> = ({ onAddToCart }) => {
  // Filters state
  const [searchQuery, setSearchQuery] = useState('');
  const [selectedCategory, setSelectedCategory] = useState<string>('');
  const [minPriceInput, setMinPriceInput] = useState('');
  const [maxPriceInput, setMaxPriceInput] = useState('');
  const [inStockOnly, setInStockOnly] = useState(false);
  const [page, setPage] = useState(1);
  const pageSize = 12;

  // Data state
  const [categories, setCategories] = useState<Category[]>([]);
  const [searchResult, setSearchResult] = useState<SearchResult | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Price validation
  const minPriceMinor = parseDollarsToMinor(minPriceInput);
  const maxPriceMinor = parseDollarsToMinor(maxPriceInput);
  const priceRangeInvalid =
    minPriceMinor !== undefined &&
    maxPriceMinor !== undefined &&
    minPriceMinor > maxPriceMinor;

  // Load all categories on mount to enrich facet labels
  useEffect(() => {
    listCategories()
      .then((cats) => setCategories(cats || []))
      .catch((err) => console.warn('Failed to load categories', err));
  }, []);

  const categoryNameMap = React.useMemo(() => {
    const map = new Map<string, string>();
    categories.forEach((c) => map.set(c.id, c.name));
    return map;
  }, [categories]);

  // Execute search query against /api/v1/products/search
  const fetchProducts = useCallback(async () => {
    if (priceRangeInvalid) return;

    setLoading(true);
    setError(null);

    try {
      const result = await searchProducts({
        q: searchQuery.trim() || undefined,
        category_id: selectedCategory || undefined,
        min_price: minPriceMinor,
        max_price: maxPriceMinor,
        in_stock: inStockOnly ? true : undefined,
        page,
        page_size: pageSize,
      });
      setSearchResult(result);
    } catch (err: unknown) {
      console.error('Search failed', err);
      setError(err instanceof Error ? err.message : 'Failed to load catalog products');
    } finally {
      setLoading(false);
    }
  }, [searchQuery, selectedCategory, minPriceMinor, maxPriceMinor, inStockOnly, page, priceRangeInvalid]);

  useEffect(() => {
    fetchProducts();
  }, [fetchProducts]);

  const handleSearchSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    setPage(1);
    fetchProducts();
  };

  const handleResetFilters = () => {
    setSearchQuery('');
    setSelectedCategory('');
    setMinPriceInput('');
    setMaxPriceInput('');
    setInStockOnly(false);
    setPage(1);
  };

  const totalPages = searchResult?.total_pages || 1;
  const products = searchResult?.products || [];
  const facetCategories = searchResult?.facets?.categories || [];

  return (
    <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
      {/* Top Search & Filter Bar */}
      <div className="bg-white p-4 sm:p-6 rounded-2xl border border-slate-200/80 shadow-xs mb-8">
        <form onSubmit={handleSearchSubmit} className="flex flex-col md:flex-row gap-3">
          <div className="relative flex-1">
            <Search className="absolute left-3.5 top-1/2 -translate-y-1/2 w-5 h-5 text-slate-400" />
            <input
              type="text"
              value={searchQuery}
              onChange={(e) => {
                setSearchQuery(e.target.value);
                setPage(1);
              }}
              placeholder="Search products by title, SKU, or description..."
              className="w-full pl-11 pr-4 py-2.5 bg-slate-50 border border-slate-200 rounded-xl text-sm placeholder-slate-400 focus:bg-white focus:border-indigo-500 focus:ring-2 focus:ring-indigo-100 outline-none transition"
            />
          </div>

          <button
            type="submit"
            className="px-6 py-2.5 bg-indigo-600 hover:bg-indigo-700 text-white rounded-xl text-sm font-semibold shadow-sm shadow-indigo-600/20 transition active:scale-95 flex items-center justify-center gap-2"
          >
            <Search className="w-4 h-4" />
            Search
          </button>
        </form>

        {/* Filters Row */}
        <div className="mt-4 pt-4 border-t border-slate-100 flex flex-wrap items-center justify-between gap-4">
          <div className="flex flex-wrap items-center gap-4 text-xs">
            {/* Category Select */}
            <div className="flex items-center gap-1.5">
              <span className="font-semibold text-slate-600">Category:</span>
              <select
                value={selectedCategory}
                onChange={(e) => {
                  setSelectedCategory(e.target.value);
                  setPage(1);
                }}
                className="bg-slate-50 border border-slate-200 rounded-lg px-2.5 py-1.5 text-xs text-slate-700 focus:outline-none focus:border-indigo-500 font-medium"
              >
                <option value="">All Categories</option>
                {categories.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </div>

            {/* Price Range Filter */}
            <div className="flex items-center gap-1.5">
              <span className="font-semibold text-slate-600">Price ($):</span>
              <input
                type="number"
                step="0.01"
                min="0"
                placeholder="Min"
                value={minPriceInput}
                onChange={(e) => {
                  setMinPriceInput(e.target.value);
                  setPage(1);
                }}
                className="w-20 bg-slate-50 border border-slate-200 rounded-lg px-2 py-1.5 text-xs focus:outline-none focus:border-indigo-500"
              />
              <span className="text-slate-400">–</span>
              <input
                type="number"
                step="0.01"
                min="0"
                placeholder="Max"
                value={maxPriceInput}
                onChange={(e) => {
                  setMaxPriceInput(e.target.value);
                  setPage(1);
                }}
                className="w-20 bg-slate-50 border border-slate-200 rounded-lg px-2 py-1.5 text-xs focus:outline-none focus:border-indigo-500"
              />
              {priceRangeInvalid && (
                <span className="text-rose-600 font-medium flex items-center gap-1">
                  <AlertCircle className="w-3 h-3" /> Min &gt; Max
                </span>
              )}
            </div>

            {/* Stock Filter Toggle */}
            <label className="flex items-center gap-2 cursor-pointer select-none font-medium text-slate-700 hover:text-slate-900">
              <input
                type="checkbox"
                checked={inStockOnly}
                onChange={(e) => {
                  setInStockOnly(e.target.checked);
                  setPage(1);
                }}
                className="w-4 h-4 rounded text-indigo-600 border-slate-300 focus:ring-indigo-500 cursor-pointer"
              />
              In stock only
            </label>
          </div>

          {/* Reset Filters */}
          <button
            type="button"
            onClick={handleResetFilters}
            className="flex items-center gap-1 text-xs text-slate-500 hover:text-indigo-600 font-medium transition"
          >
            <RotateCcw className="w-3.5 h-3.5" />
            Reset all
          </button>
        </div>

        {/* Category Facets Chips from Search API */}
        {facetCategories.length > 0 && (
          <div className="mt-3 pt-3 border-t border-slate-100 flex flex-wrap items-center gap-2">
            <span className="text-xs font-semibold text-slate-500 flex items-center gap-1">
              <SlidersHorizontal className="w-3 h-3" /> Facets:
            </span>
            <button
              onClick={() => {
                setSelectedCategory('');
                setPage(1);
              }}
              className={`text-xs px-2.5 py-1 rounded-full border transition font-medium ${
                selectedCategory === ''
                  ? 'bg-indigo-600 text-white border-indigo-600'
                  : 'bg-slate-50 text-slate-700 border-slate-200 hover:bg-slate-100'
              }`}
            >
              All ({searchResult?.total_hits || 0})
            </button>
            {facetCategories.map((f) => {
              const label = categoryNameMap.get(f.key) || f.key.slice(0, 8);
              const isSelected = selectedCategory === f.key;
              return (
                <button
                  key={f.key}
                  onClick={() => {
                    setSelectedCategory(isSelected ? '' : f.key);
                    setPage(1);
                  }}
                  className={`text-xs px-2.5 py-1 rounded-full border transition font-medium flex items-center gap-1 ${
                    isSelected
                      ? 'bg-indigo-600 text-white border-indigo-600'
                      : 'bg-slate-50 text-slate-700 border-slate-200 hover:bg-slate-100'
                  }`}
                >
                  <span>{label}</span>
                  <span className={`text-[10px] px-1 py-0.2 rounded-full ${isSelected ? 'bg-indigo-500 text-white' : 'bg-slate-200 text-slate-600'}`}>
                    {f.count}
                  </span>
                </button>
              );
            })}
          </div>
        )}
      </div>

      {/* Results Header */}
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-xl font-bold text-slate-900 tracking-tight flex items-center gap-2">
          Product Catalog
          {searchResult && (
            <span className="text-xs font-normal text-slate-500 bg-slate-100 px-2 py-0.5 rounded-full border border-slate-200">
              {searchResult.total_hits} {searchResult.total_hits === 1 ? 'item' : 'items'} found
            </span>
          )}
        </h2>

        {totalPages > 1 && (
          <div className="text-xs text-slate-500 font-medium">
            Page {page} of {totalPages}
          </div>
        )}
      </div>

      {/* Error Message */}
      {error && (
        <div className="p-4 mb-6 rounded-xl bg-rose-50 border border-rose-200 text-rose-800 text-sm flex items-center gap-2">
          <AlertCircle className="w-5 h-5 shrink-0 text-rose-600" />
          <span>{error}</span>
        </div>
      )}

      {/* Loading Skeletons */}
      {loading ? (
        <div className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-6">
          {Array.from({ length: 8 }).map((_, i) => (
            <div key={i} className="bg-white rounded-2xl border border-slate-200 p-4 animate-pulse flex flex-col gap-4">
              <div className="aspect-[4/3] bg-slate-200 rounded-xl" />
              <div className="h-4 bg-slate-200 rounded w-3/4" />
              <div className="h-3 bg-slate-200 rounded w-1/2" />
              <div className="h-8 bg-slate-200 rounded mt-auto" />
            </div>
          ))}
        </div>
      ) : products.length === 0 ? (
        /* Empty State */
        <div className="bg-white rounded-2xl border border-dashed border-slate-300 p-12 text-center flex flex-col items-center justify-center">
          <div className="w-16 h-16 rounded-full bg-slate-100 flex items-center justify-center text-slate-400 mb-4">
            <PackageSearch className="w-8 h-8" />
          </div>
          <h3 className="text-base font-bold text-slate-800">No products found</h3>
          <p className="text-sm text-slate-500 mt-1 max-w-sm">
            We couldn&apos;t find any products matching your current search or filter criteria.
          </p>
          <button
            onClick={handleResetFilters}
            className="mt-5 px-4 py-2 bg-indigo-50 hover:bg-indigo-100 text-indigo-700 font-semibold text-xs rounded-xl border border-indigo-200 transition"
          >
            Clear Filters
          </button>
        </div>
      ) : (
        /* Products Grid */
        <div className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-6">
          {products.map((p) => (
            <ProductCard key={p.id || p.sku} product={p} onAddToCart={onAddToCart} />
          ))}
        </div>
      )}

      {/* Pagination Controls */}
      {totalPages > 1 && (
        <div className="mt-10 flex items-center justify-center gap-2">
          <button
            onClick={() => setPage((p) => Math.max(1, p - 1))}
            disabled={page <= 1 || loading}
            className="flex items-center gap-1 px-3.5 py-2 rounded-xl border border-slate-200 bg-white text-xs font-semibold text-slate-700 hover:bg-slate-50 disabled:opacity-40 disabled:cursor-not-allowed shadow-xs"
          >
            <ChevronLeft className="w-4 h-4" />
            Previous
          </button>
          <span className="text-xs font-semibold text-slate-600 px-3">
            {page} / {totalPages}
          </span>
          <button
            onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
            disabled={page >= totalPages || loading}
            className="flex items-center gap-1 px-3.5 py-2 rounded-xl border border-slate-200 bg-white text-xs font-semibold text-slate-700 hover:bg-slate-50 disabled:opacity-40 disabled:cursor-not-allowed shadow-xs"
          >
            Next
            <ChevronRight className="w-4 h-4" />
          </button>
        </div>
      )}
    </div>
  );
};
