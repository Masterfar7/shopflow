// web/src/i18n.ts
export type Language = 'ru' | 'en';

export const translations = {
  ru: {
    // Navbar
    appName: 'ShopFlow',
    appSubtitle: 'Распределенная платформа обработки заказов',
    customer: 'Покупатель',
    save: 'Сохр.',
    cancel: 'Отмена',
    copyTooltip: 'Скопировать UUID',
    copied: 'Скопировано!',
    newCustomer: 'Сгенерировать нового покупателя',
    orders: 'Мои заказы',
    cart: 'Корзина',

    // Catalog & Filters
    searchPlaceholder: 'Поиск товаров по названию, артикулу или описанию...',
    searchBtn: 'Найти',
    categoryLabel: 'Категория:',
    allCategories: 'Все категории',
    priceLabel: 'Цена ($):',
    minPrice: 'От',
    maxPrice: 'До',
    sortLabel: 'Сортировка:',
    sortNewest: 'Сначала новые',
    sortPriceAsc: 'Сначала дешевле',
    sortPriceDesc: 'Сначала дороже',
    resetAll: 'Сбросить фильтры',
    facets: 'Фильтры:',
    catalogTitle: 'Каталог товаров',
    itemsFound: 'найдено',
    itemOne: 'товар',
    itemFew: 'товара',
    itemMany: 'товаров',
    pageOf: (current: number, total: number) => `Страница ${current} из ${total}`,
    noProductsTitle: 'Товары не найдены',
    noProductsDesc: 'По вашему запросу или выбранным фильтрам ничего не найдено.',
    clearFilters: 'Очистить фильтры',
    inStockOnly: 'Только в наличии',

    // Product Card
    sku: 'Артикул',
    inStock: 'В наличии',
    outOfStock: 'Нет в наличии',
    addToCart: 'В корзину',
    adding: 'Добавляем...',
    added: 'Добавлено!',

    // Cart Drawer
    cartTitle: 'Ваша корзина',
    itemsInCart: (count: number) => `${count} ${count === 1 ? 'позиция' : count < 5 ? 'позиции' : 'позиций'}`,
    emptyCartTitle: 'Корзина пуста',
    emptyCartDesc: 'Выберите товары из каталога и добавьте их в корзину.',
    continueShopping: 'Перейти к покупкам',
    clearCart: 'Очистить корзину',
    subtotal: 'Подытог',
    shipping: 'Доставка',
    free: 'Бесплатно',
    total: 'Итого к оплате',
    checkoutBtn: 'Перейти к оформлению',

    // Checkout Modal
    checkoutTitle: 'Оформление заказа',
    checkoutSubtitle: 'Заполните данные доставки и способ оплаты',
    shippingAddress: 'Адрес доставки',
    fullName: 'ФИО получателя',
    fullNamePlaceholder: 'Иван Иванов',
    streetAddress: 'Улица, дом, квартира',
    streetPlaceholder: 'ул. Ленина, д. 10, кв. 25',
    city: 'Город',
    cityPlaceholder: 'Москва',
    postalCode: 'Индекс',
    postalPlaceholder: '101000',
    country: 'Страна',
    paymentMethod: 'Способ оплаты',
    cardHolder: 'Имя на карте',
    cardNumber: 'Номер карты',
    expiry: 'Срок действия',
    orderSummary: 'Состав заказа',
    placeOrderBtn: 'Подтвердить и оплатить',
    processingOrder: 'Обработка заказа...',

    // Order Status / Saga Tracker
    orderStatusTitle: 'Статус заказа',
    orderIdLabel: 'ID заказа',
    backToCatalog: 'Вернуться в каталог',
    sagaTrackerTitle: 'Оркестрация Saga & Outbox',
    sagaStepCreated: 'Заказ создан',
    sagaStepInventory: 'Резерв на складе',
    sagaStepPayment: 'Проведение оплаты',
    sagaStepNotification: 'Уведомление отправлено',
    sagaStepCompleted: 'Заказ успешно выполнен',
    sagaStepFailed: 'Компенсация (Откат заказа)',
    orderTotalLabel: 'Сумма заказа',
    orderStatusLabel: 'Текущий статус',

    // Order History Modal
    historyTitle: 'История заказов',
    noOrdersTitle: 'Заказов пока нет',
    noOrdersDesc: 'Вы еще не оформили ни одного заказа под текущим ID покупателя.',
    viewDetails: 'Подробнее',
  },
  en: {
    // Navbar
    appName: 'ShopFlow',
    appSubtitle: 'Distributed Order Processing Platform',
    customer: 'Customer',
    save: 'Save',
    cancel: 'Cancel',
    copyTooltip: 'Copy customer UUID',
    copied: 'Copied!',
    newCustomer: 'Generate new customer ID',
    orders: 'Orders',
    cart: 'Cart',

    // Catalog & Filters
    searchPlaceholder: 'Search products by title, SKU, or description...',
    searchBtn: 'Search',
    categoryLabel: 'Category:',
    allCategories: 'All Categories',
    priceLabel: 'Price ($):',
    minPrice: 'Min',
    maxPrice: 'Max',
    sortLabel: 'Sort by:',
    sortNewest: 'Newest',
    sortPriceAsc: 'Price: Low to High',
    sortPriceDesc: 'Price: High to Low',
    resetAll: 'Reset all',
    facets: 'Facets:',
    catalogTitle: 'Product Catalog',
    itemsFound: 'found',
    itemOne: 'item',
    itemFew: 'items',
    itemMany: 'items',
    pageOf: (current: number, total: number) => `Page ${current} of ${total}`,
    noProductsTitle: 'No products found',
    noProductsDesc: "We couldn't find any products matching your current search or filter criteria.",
    clearFilters: 'Clear Filters',
    inStockOnly: 'In stock only',

    // Product Card
    sku: 'SKU',
    inStock: 'In Stock',
    outOfStock: 'Out of stock',
    addToCart: 'Add to Cart',
    adding: 'Adding...',
    added: 'Added!',

    // Cart Drawer
    cartTitle: 'Your Cart',
    itemsInCart: (count: number) => `${count} ${count === 1 ? 'item' : 'items'}`,
    emptyCartTitle: 'Your cart is empty',
    emptyCartDesc: 'Explore our catalog and find something great!',
    continueShopping: 'Explore Catalog',
    clearCart: 'Clear Cart',
    subtotal: 'Subtotal',
    shipping: 'Shipping',
    free: 'Free',
    total: 'Total',
    checkoutBtn: 'Proceed to Checkout',

    // Checkout Modal
    checkoutTitle: 'Checkout',
    checkoutSubtitle: 'Provide shipping and payment information',
    shippingAddress: 'Shipping Address',
    fullName: 'Full Name',
    fullNamePlaceholder: 'John Doe',
    streetAddress: 'Street Address',
    streetPlaceholder: '123 Main St, Apt 4B',
    city: 'City',
    cityPlaceholder: 'New York',
    postalCode: 'Postal Code',
    postalPlaceholder: '10001',
    country: 'Country',
    paymentMethod: 'Payment Method',
    cardHolder: 'Cardholder Name',
    cardNumber: 'Card Number',
    expiry: 'Expiration',
    orderSummary: 'Order Summary',
    placeOrderBtn: 'Place Order',
    processingOrder: 'Processing order...',

    // Order Status / Saga Tracker
    orderStatusTitle: 'Order Status',
    orderIdLabel: 'Order ID',
    backToCatalog: 'Back to Catalog',
    sagaTrackerTitle: 'Saga & Outbox Orchestration',
    sagaStepCreated: 'Order Created',
    sagaStepInventory: 'Inventory Reserved',
    sagaStepPayment: 'Payment Processed',
    sagaStepNotification: 'Notification Dispatched',
    sagaStepCompleted: 'Order Completed',
    sagaStepFailed: 'Compensating Transaction',
    orderTotalLabel: 'Order Total',
    orderStatusLabel: 'Current Status',

    // Order History Modal
    historyTitle: 'Order History',
    noOrdersTitle: 'No orders yet',
    noOrdersDesc: 'You have not placed any orders with this customer ID yet.',
    viewDetails: 'View Details',
  },
};

const LANG_KEY = 'shopflow_language';

export function getLanguage(): Language {
  const saved = localStorage.getItem(LANG_KEY);
  if (saved === 'ru' || saved === 'en') {
    return saved;
  }
  return 'ru'; // Default to Russian as requested
}

export function setLanguage(lang: Language): void {
  localStorage.setItem(LANG_KEY, lang);
}
