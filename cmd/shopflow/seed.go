package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type seedCategory struct {
	id          string
	slug        string
	name        string
	description string
}

type seedProduct struct {
	id          string
	categoryID  string
	sku         string
	title       string
	description string
	priceMinor  int64
	stock       int
	imageURL    string
}

func seedInitialData(ctx context.Context, db *pgxpool.Pool, logger *slog.Logger) error {
	var count int
	err := db.QueryRow(ctx, "SELECT COUNT(*) FROM products").Scan(&count)
	if err != nil {
		return fmt.Errorf("check products count: %w", err)
	}

	if count > 0 {
		logger.Info("catalog already contains data, skipping seed", "product_count", count)
		return nil
	}

	logger.Info("catalog is empty, seeding demo products and categories...")

	categories := []seedCategory{
		{
			id:          "11111111-1111-1111-1111-111111111101",
			slug:        "electronics",
			name:        "Электроника (Electronics)",
			description: "Гаджеты, смартфоны, ноутбуки и аксессуары",
		},
		{
			id:          "11111111-1111-1111-1111-111111111102",
			slug:        "audio",
			name:        "Аудио & Звук (Audio)",
			description: "Беспроводные наушники, акустика и микрофоны",
		},
		{
			id:          "11111111-1111-1111-1111-111111111103",
			slug:        "accessories",
			name:        "Аксессуары (Accessories)",
			description: "Чехлы, кабели, зарядные станции и рюкзаки",
		},
		{
			id:          "11111111-1111-1111-1111-111111111104",
			slug:        "smart-home",
			name:        "Умный дом (Smart Home)",
			description: "Устройства автоматизации и комфорта",
		},
	}

	for _, c := range categories {
		_, err := db.Exec(ctx, `
			INSERT INTO categories (id, slug, name, description, created_at, updated_at)
			VALUES ($1, $2, $3, $4, NOW(), NOW())
			ON CONFLICT (slug) DO NOTHING
		`, c.id, c.slug, c.name, c.description)
		if err != nil {
			return fmt.Errorf("insert category %s: %w", c.slug, err)
		}
	}

	products := []seedProduct{
		{
			id:          "22222222-2222-2222-2222-222222222201",
			categoryID:  "11111111-1111-1111-1111-111111111101",
			sku:         "SKU-MACBOOK-PRO-16",
			title:       "Ноутбук Pro 16 M3 Max",
			description: "Флагманский производительный ноутбук для разработки, 3D-графики и высоких нагрузок. 36GB RAM, 1TB SSD.",
			priceMinor:  249999, // $2,499.99
			stock:       15,
			imageURL:    "https://images.unsplash.com/photo-1517336714731-489689fd1ca8?w=800&auto=format&fit=crop&q=60",
		},
		{
			id:          "22222222-2222-2222-2222-222222222202",
			categoryID:  "11111111-1111-1111-1111-111111111102",
			sku:         "SKU-HEADPHONES-ANC",
			title:       "Беспроводные наушники Pro ANC",
			description: "Премиальное активное шумоподавление, Hi-Res аудиокодеки и до 40 часов непрерывного воспроизведения.",
			priceMinor:  34999, // $349.99
			stock:       45,
			imageURL:    "https://images.unsplash.com/photo-1505740420928-5e560c06d30e?w=800&auto=format&fit=crop&q=60",
		},
		{
			id:          "22222222-2222-2222-2222-222222222203",
			categoryID:  "11111111-1111-1111-1111-111111111101",
			sku:         "SKU-KEYCHRON-Q1",
			title:       "Механическая клавиатура Custom RGB",
			description: "Алюминиевый корпус, Hot-Swap переключатели, настраиваемая подсветка и подключение по Type-C/Bluetooth.",
			priceMinor:  18950, // $189.50
			stock:       28,
			imageURL:    "https://images.unsplash.com/photo-1587829741301-dc798b83add3?w=800&auto=format&fit=crop&q=60",
		},
		{
			id:          "22222222-2222-2222-2222-222222222204",
			categoryID:  "11111111-1111-1111-1111-111111111103",
			sku:         "SKU-BACKPACK-URBAN",
			title:       "Городской водонепроницаемый рюкзак",
			description: "Отделение для ноутбука 16 дюймов, скрытые карманы против кражи, USB-порт для быстрой подзарядки.",
			priceMinor:  8990, // $89.90
			stock:       60,
			imageURL:    "https://images.unsplash.com/photo-1553062407-98eeb64c6a62?w=800&auto=format&fit=crop&q=60",
		},
		{
			id:          "22222222-2222-2222-2222-222222222205",
			categoryID:  "11111111-1111-1111-1111-111111111101",
			sku:         "SKU-MONITOR-4K-32",
			title:       "Монитор UltraWide 4K 32\" IPS",
			description: "Калиброванная цветопередача 99% DCI-P3, 144Hz частота обновления, встроенный KVM-переключатель.",
			priceMinor:  69900, // $699.00
			stock:       12,
			imageURL:    "https://images.unsplash.com/photo-1527443224154-c4a3942d3acf?w=800&auto=format&fit=crop&q=60",
		},
		{
			id:          "22222222-2222-2222-2222-222222222206",
			categoryID:  "11111111-1111-1111-1111-111111111104",
			sku:         "SKU-SMART-SPEAKER",
			title:       "Умная колонка с голосовым ассистентом",
			description: "Чистый стереозвук, управление умным домом по Zigbee и Wi-Fi, адаптивная акустика помещения.",
			priceMinor:  12999, // $129.99
			stock:       35,
			imageURL:    "https://images.unsplash.com/photo-1543512214-318c7553f230?w=800&auto=format&fit=crop&q=60",
		},
		{
			id:          "22222222-2222-2222-2222-222222222207",
			categoryID:  "11111111-1111-1111-1111-111111111103",
			sku:         "SKU-WIRELESS-CHARGER",
			title:       "Беспроводная зарядная станция 3-в-1",
			description: "Одновременная быстрая зарядка MagSafe для смартфона, смарт-часов и беспроводных наушников.",
			priceMinor:  5990, // $59.90
			stock:       80,
			imageURL:    "https://images.unsplash.com/photo-1622445262464-84b1456045b6?w=800&auto=format&fit=crop&q=60",
		},
		{
			id:          "22222222-2222-2222-2222-222222222208",
			categoryID:  "11111111-1111-1111-1111-111111111102",
			sku:         "SKU-STUDIO-MIC",
			title:       "Конденсаторный USB-микрофон Studio Pro",
			description: "Кардиоидная диаграмма направленности, встроенный поп-фильтр и нулевая задержка мониторинга звука.",
			priceMinor:  14900, // $149.00
			stock:       20,
			imageURL:    "https://images.unsplash.com/photo-1590658268037-6bf12165a8df?w=800&auto=format&fit=crop&q=60",
		},
	}

	for _, p := range products {
		catUUID := uuid.MustParse(p.categoryID)
		prodUUID := uuid.MustParse(p.id)

		_, err := db.Exec(ctx, `
			INSERT INTO products (id, category_id, sku, title, description, price_minor, currency, is_active, version, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'USD', TRUE, 1, NOW(), NOW())
			ON CONFLICT (sku) DO NOTHING
		`, prodUUID, catUUID, p.sku, p.title, p.description, p.priceMinor)
		if err != nil {
			return fmt.Errorf("insert product %s: %w", p.sku, err)
		}

		// Seed Inventory stock
		_, err = db.Exec(ctx, `
			INSERT INTO inventory (sku, on_hand, reserved, version, updated_at)
			VALUES ($1, $2, 0, 1, NOW())
			ON CONFLICT (sku) DO NOTHING
		`, p.sku, p.stock)
		if err != nil {
			return fmt.Errorf("insert inventory %s: %w", p.sku, err)
		}

		// Seed Product Image
		if p.imageURL != "" {
			_, err = db.Exec(ctx, `
				INSERT INTO product_images (id, product_id, storage_key, url, content_type, size_bytes, is_primary, sort_order, created_at)
				VALUES ($1, $2, $3, $4, 'image/jpeg', 102400, TRUE, 0, NOW())
				ON CONFLICT (storage_key) DO NOTHING
			`, uuid.New(), prodUUID, p.sku+"-primary", p.imageURL)
			if err != nil {
				return fmt.Errorf("insert product image %s: %w", p.sku, err)
			}
		}
	}

	logger.Info("catalog successfully seeded with demo products and stock", "count", len(products))
	return nil
}
