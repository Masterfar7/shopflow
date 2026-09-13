package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"shopflow/internal/domain/cart"
	"shopflow/internal/domain/catalog"
	"shopflow/internal/domain/inbox"
	"shopflow/internal/domain/inventory"
	"shopflow/internal/domain/notification"
	"shopflow/internal/domain/order"
	"shopflow/internal/domain/outbox"
	"shopflow/internal/domain/payment"
	"shopflow/internal/domain/saga"
	"shopflow/internal/platform/cache"
	"shopflow/internal/platform/config"
	"shopflow/internal/platform/database"
	"shopflow/internal/platform/email"
	"shopflow/internal/platform/kafka"
	"shopflow/internal/platform/ratelimit"
	"shopflow/internal/platform/search"
	"shopflow/internal/platform/storage"
	"shopflow/internal/platform/telemetry"
	"shopflow/internal/platform/web"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
)

func main() {
	serverStartTime := time.Now().UTC()

	// 1. Root Context listening for OS interrupt signals
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 2. Load Configuration
	cfg := config.Load()

	// 3. Structured Logging Setup (log/slog)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))
	slog.SetDefault(logger)
	logger.Info("starting shopflow server",
		"http_port", cfg.HTTPPort,
		"database_url", cfg.DatabaseURL,
		"kafka_brokers", cfg.KafkaBrokers,
		"redis_addr", cfg.RedisAddr,
	)

	// 4. PostgreSQL Connection Pool via platform/database
	dbCfg := database.Config{
		URL:             cfg.DatabaseURL,
		MaxConns:        25,
		MinConns:        5,
		MaxConnLifetime: 1 * time.Hour,
		MaxConnIdleTime: 15 * time.Minute,
		HealthCheckTime: 1 * time.Minute,
	}
	dbPool, err := database.NewPool(ctx, dbCfg)
	if err != nil {
		logger.Error("failed to initialize postgres pool", "err", err)
		os.Exit(1)
	}
	defer dbPool.Close()
	logger.Info("postgres pool connected successfully")

	// Automatically seed initial demo products if catalog is empty
	if err := seedInitialData(ctx, dbPool, logger); err != nil {
		logger.Warn("failed to seed initial catalog data", "err", err)
	}

	// Initialize Telemetry
	metrics := telemetry.DefaultMetrics()

	// Initialize Blob Storage
	blobStorage, err := storage.NewStorageFromConfig(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize blob storage", "err", err)
		os.Exit(1)
	}

	// Initialize Search Client
	searchClient, err := search.NewClientFromConfig(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize search client", "err", err)
		os.Exit(1)
	}

	// Initialize Catalog Domain
	catalogRepo := catalog.NewPostgresRepository(dbPool)
	catalogService := catalog.NewServiceWithSearch(catalogRepo, searchClient, logger)
	catalogHandler := catalog.NewHandlerWithStorageAndSearch(catalogService, blobStorage, searchClient, logger)

	// Initialize Cart Domain (CatalogReader injected cleanly)
	cartRepo := cart.NewPostgresRepository(dbPool)
	cartService := cart.NewService(cartRepo, catalogService, dbPool, logger)
	cartHandler := cart.NewHandler(cartService, logger)

	// Initialize Inventory Domain
	inventoryRepo := inventory.NewPostgresRepository(dbPool)
	inventoryService := inventory.NewService(inventoryRepo, dbPool, logger)
	inventoryHandler := inventory.NewHandler(inventoryService, logger)

	// Initialize Order Domain
	orderRepo := order.NewPostgresRepository(dbPool)
	orderService := order.NewService(orderRepo, catalogService, dbPool, logger)
	orderHandler := order.NewHandler(orderService, logger)

	// Initialize Payment Domain
	paymentRepo := payment.NewPostgresRepository(dbPool)
	paymentService := payment.NewService(paymentRepo, logger)
	paymentHandler := payment.NewHandler(paymentService, logger)
	paymentCommander := payment.NewSagaAdapter(paymentService)

	// Initialize Saga Domain & Adapters
	sagaRepo := saga.NewPostgresRepository(dbPool)
	invCommander := &inventoryCommanderAdapter{svc: inventoryService}
	ordCommander := &orderCommanderAdapter{repo: orderRepo, svc: orderService}
	sagaCoordinator := saga.NewCoordinator(sagaRepo, dbPool, invCommander, ordCommander, paymentCommander, saga.DefaultConfig(), logger)
	sagaWatchdog := saga.NewWatchdog(sagaCoordinator, sagaRepo, saga.DefaultWatchdogConfig(), logger)
	_ = sagaCoordinator

	// 5. Redis Client Initialization
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer rdb.Close()

	// Initialize Catalog Cache layer (wrapping catalogService)
	catalogCache := cache.NewCatalogCache(catalogService, rdb, logger)
	_ = catalogCache

	// Initialize Rate Limiter
	rateLimiter := ratelimit.NewLimiter(rdb, logger)

	// Rate limit configs per route category
	publicReadLimit := ratelimit.Config{
		Requests:      100,
		Window:        time.Minute,
		FailurePolicy: ratelimit.FailOpen,
		KeyFunc:       ratelimit.IPKeyFunc,
	}
	orderWriteLimit := ratelimit.Config{
		Requests:      10,
		Window:        time.Minute,
		FailurePolicy: ratelimit.FailClosed,
		KeyFunc:       ratelimit.UserIDKeyFunc,
	}

	// 6. Kafka Client Initialization (twmb/franz-go)
	kafkaClient, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.KafkaBrokers...),
		kgo.ConsumerGroup("shopflow-inbox-group"),
	)
	if err != nil {
		logger.Error("failed to initialize kafka client", "err", err)
		os.Exit(1)
	}
	defer kafkaClient.Close()

	// 7. Chi HTTP Router & Middleware Tree
	r := chi.NewRouter()

	// Observability & Telemetry Middleware (Tracing + Prometheus Metrics)
	r.Use(telemetry.TracingMiddleware())
	r.Use(telemetry.MetricsMiddleware(metrics))

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(httpLoggingMiddleware(logger))

	// Observability & Health Probes
	r.Get("/health/live", telemetry.LivenessHandler(serverStartTime))
	r.Get("/health/ready", telemetry.ReadinessHandler(dbPool, telemetry.PingerFunc(func(ctx context.Context) error {
		return rdb.Ping(ctx).Err()
	}), kafkaClient))
	r.Handle("/metrics", telemetry.Handler())

	// API V1 Route Mounts
	r.Route("/api/v1", func(api chi.Router) {
		api.Get("/info", func(w http.ResponseWriter, r *http.Request) {
			web.RespondJSON(w, http.StatusOK, map[string]string{
				"app":     "shopflow",
				"version": "0.1.0",
				"status":  "healthy",
			})
		})

		// Sub-domain mounts matching USER_REQUEST with rate limiting:
		api.Group(func(catRouter chi.Router) {
			catRouter.Use(rateLimiter.Middleware("catalog", publicReadLimit))
			catRouter.Mount("/catalog", catalogHandler.Routes())
			catRouter.Mount("/products", catalogHandler.ProductRoutes())
			catRouter.Mount("/categories", catalogHandler.CategoryRoutes())
		})

		api.Group(func(ordRouter chi.Router) {
			ordRouter.Use(rateLimiter.Middleware("order", orderWriteLimit))
			ordRouter.Mount("/orders", orderHandler.Routes())
		})

		api.Mount("/cart", cartHandler.Routes())
		api.Mount("/inventory", inventoryHandler.Routes())
		api.Mount("/payments", paymentHandler.Routes())

		// Direct resource mounts matching OpenAPI specs:
		api.Mount("/carts", cartHandler.CartRoutes())
		api.Mount("/reservations", inventoryHandler.ReservationRoutes())
	})

	// Static Web SPA Serving (serves web/dist if present, with SPA fallback to index.html)
	webDistDir := os.Getenv("WEB_DIST_PATH")
	if webDistDir == "" {
		webDistDir = "web/dist"
	}
	web.AttachSPAFallback(r, webDistDir, logger)

	httpServer := &http.Server{
		Addr:         ":" + cfg.HTTPPort,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// 8. Background Workers Coordination
	var wg sync.WaitGroup
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()

	// 8a. Outbox Publisher Worker
	outboxRepo := outbox.NewPostgresRepository(dbPool)
	kafkaPublisher := kafka.NewFranzPublisher(kafkaClient, logger)
	outboxPoller := outbox.NewPoller(outboxRepo, kafkaPublisher, outbox.DefaultPollerConfig(), logger)
	if err := outboxPoller.Start(workerCtx); err != nil {
		logger.Error("failed to start outbox poller", "err", err)
	}

	// 8b. Saga Timeout Watchdog Worker
	if err := sagaWatchdog.Start(workerCtx); err != nil {
		logger.Error("failed to start saga watchdog", "err", err)
	}

	// 8c. Notification Event Consumer
	emailSender, err := email.NewSenderFromConfig(cfg, logger)
	if err != nil {
		logger.Error("failed to initialize email sender", "err", err)
		os.Exit(1)
	}

	notificationRenderer, err := notification.NewRenderer()
	if err != nil {
		logger.Error("failed to initialize notification renderer", "err", err)
		os.Exit(1)
	}

	notificationService := notification.NewService(emailSender, notificationRenderer, logger)
	notificationInboxRepo := inbox.NewPostgresRepository(dbPool)

	notificationKafkaConsumer, err := kafka.NewFranzConsumerFromConfig(kafka.ConsumerConfig{
		Brokers: cfg.KafkaBrokers,
		Group:   "notification-service",
		Topics:  []string{"shopflow.orders"},
	}, logger)
	if err != nil {
		logger.Error("failed to initialize notification kafka consumer", "err", err)
		os.Exit(1)
	}

	notificationConsumer := notification.NewConsumer(notificationInboxRepo, notificationKafkaConsumer, notificationService, logger)
	if err := notificationConsumer.Start(workerCtx); err != nil {
		logger.Error("failed to start notification consumer", "err", err)
	}

	// 9. Start HTTP Server
	go func() {
		logger.Info("http server listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "err", err)
			stop()
		}
	}()

	// 10. Wait for Shutdown Signal
	<-ctx.Done()
	logger.Info("shutdown signal received, initiating graceful shutdown")

	// 11. Graceful Shutdown Routine
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelShutdown()

	// 11a. Stop HTTP server first (cease new ingress)
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("http server graceful shutdown error", "err", err)
	} else {
		logger.Info("http server stopped cleanly")
	}

	// 11b. Stop background workers and wait for completion
	if err := outboxPoller.Stop(shutdownCtx); err != nil {
		logger.Warn("outbox poller graceful stop warning", "err", err)
	}

	if err := sagaWatchdog.Stop(shutdownCtx); err != nil {
		logger.Warn("saga watchdog graceful stop warning", "err", err)
	}

	if err := notificationConsumer.Stop(shutdownCtx); err != nil {
		logger.Warn("notification consumer graceful stop warning", "err", err)
	}

	cancelWorkers()
	workersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(workersDone)
	}()

	select {
	case <-workersDone:
		logger.Info("all background workers drained successfully")
	case <-shutdownCtx.Done():
		logger.Warn("shutdown deadline exceeded waiting for background workers")
	}

	logger.Info("shopflow process exited successfully")
}

// inventoryCommanderAdapter adapts inventory.Service to saga.InventoryCommander.
type inventoryCommanderAdapter struct {
	svc inventory.InventoryService
}

func (a *inventoryCommanderAdapter) ReserveStock(ctx context.Context, orderID uuid.UUID, items []saga.ReservationItem) (*saga.ReservationResult, error) {
	stockItems := make([]inventory.StockItemRequest, len(items))
	for i, it := range items {
		stockItems[i] = inventory.StockItemRequest{
			SKU:      it.SKU,
			Quantity: it.Quantity,
		}
	}
	res, err := a.svc.ReserveStock(ctx, inventory.ReserveStockRequest{
		OrderID: orderID,
		Items:   stockItems,
	})
	if err != nil {
		return nil, err
	}
	return &saga.ReservationResult{
		ReservationID: res.ReservationID,
		Success:       res.Status == inventory.ReservationStatusPending,
	}, nil
}

func (a *inventoryCommanderAdapter) ReleaseStock(ctx context.Context, reservationID uuid.UUID, reason string) error {
	return a.svc.ReleaseStock(ctx, reservationID, reason)
}

func (a *inventoryCommanderAdapter) CommitStock(ctx context.Context, reservationID uuid.UUID) error {
	return a.svc.CommitStock(ctx, reservationID)
}

// orderCommanderAdapter adapts order domain to saga.OrderCommander.
type orderCommanderAdapter struct {
	repo order.Repository
	svc  *order.Service
}

func (a *orderCommanderAdapter) UpdateStatus(ctx context.Context, orderID uuid.UUID, status string) error {
	ord, err := a.repo.GetOrderByID(ctx, orderID)
	if err != nil {
		return err
	}
	_, err = a.svc.TransitionStatus(ctx, orderID, ord.Status, order.OrderStatus(status), ord.Version)
	return err
}

func (a *orderCommanderAdapter) CancelOrder(ctx context.Context, orderID uuid.UUID, reason string) error {
	ord, err := a.repo.GetOrderByID(ctx, orderID)
	if err != nil {
		return err
	}
	if ord.Status == order.StatusCancelled {
		return nil
	}
	_, err = a.svc.CancelOrder(ctx, orderID, ord.UserID, "saga-cancel", reason)
	return err
}

func (a *orderCommanderAdapter) ConfirmOrder(ctx context.Context, orderID uuid.UUID) error {
	ord, err := a.repo.GetOrderByID(ctx, orderID)
	if err != nil {
		return err
	}
	if ord.Status == order.StatusConfirmed {
		return nil
	}
	_, err = a.svc.ConfirmOrder(ctx, orderID, ord.Version)
	return err
}

// httpLoggingMiddleware logs request path, method, status and latency using slog.
func httpLoggingMiddleware(logger *slog.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Info("http_request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}
