// worker — фоново обрабатывает заказы: берёт необработанные из postgres и помечает processed.
//
// Несколько реплик работают параллельно без двойной обработки: строки забираются
// через SELECT ... FOR UPDATE SKIP LOCKED — каждая реплика пропускает то, что уже
// взяла другая.
//
// Эндпоинты (порт PORT, по умолчанию 8080):
//
//	GET /health  — "ok"; при HEALTH_FAIL=true отвечает 500
//	GET /metrics — метрики Prometheus
//
// Переменные окружения: DATABASE_URL (без неё worker простаивает), POLL_INTERVAL (2s), BATCH_SIZE (10), HEALTH_FAIL.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

	ordersProcessed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "shop_orders_processed_total",
		Help: "Orders marked processed by this worker.",
	})

	batchErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "shop_worker_errors_total",
		Help: "Failed processing iterations (postgres unavailable, query errors).",
	})

	pending = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "shop_orders_pending",
		Help: "Unprocessed orders seen on the last poll.",
	})

	lastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "shop_worker_last_success_timestamp_seconds",
		Help: "Unix time of the last successful poll.",
	})
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// processBatch помечает до batch заказов обработанными в одной транзакции.
func processBatch(ctx context.Context, pool *pgxpool.Pool, batch int, me string) (int64, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE orders SET processed = true, processed_at = now(), processed_by = $2
		WHERE id IN (
			SELECT id FROM orders WHERE NOT processed
			ORDER BY id LIMIT $1
			FOR UPDATE SKIP LOCKED
		)`, batch, me)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func run(ctx context.Context, pool *pgxpool.Pool, interval time.Duration, batch int, me string) {
	for {
		qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		n, err := processBatch(qctx, pool, batch, me)
		if err == nil {
			var left int64
			err = pool.QueryRow(qctx, `SELECT count(*) FROM orders WHERE NOT processed`).Scan(&left)
			pending.Set(float64(left))
		}
		cancel()

		switch {
		case err != nil:
			// До первого запуска api таблицы ещё нет — это тоже просто ошибка итерации.
			batchErrors.Inc()
			logger.Warn("poll failed", "error", err)
		default:
			lastSuccess.SetToCurrentTime()
			if n > 0 {
				ordersProcessed.Add(float64(n))
				logger.Info("processed", "count", n)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv("DATABASE_URL")
	interval, err := time.ParseDuration(envOr("POLL_INTERVAL", "2s"))
	if err != nil {
		logger.Error("bad POLL_INTERVAL", "error", err)
		os.Exit(1)
	}
	batch, err := strconv.Atoi(envOr("BATCH_SIZE", "10"))
	if err != nil || batch <= 0 {
		logger.Error("bad BATCH_SIZE")
		os.Exit(1)
	}
	me, _ := os.Hostname() // имя пода — видно в processed_by, какая реплика взяла заказ

	var pool *pgxpool.Pool
	if dsn != "" {
		pool, err = pgxpool.New(ctx, dsn) // ленивое подключение
		if err != nil {
			logger.Error("bad DATABASE_URL", "error", err)
			os.Exit(1)
		}
		defer pool.Close()
	}

	healthFail := os.Getenv("HEALTH_FAIL") == "true"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		if healthFail {
			http.Error(w, "unhealthy (HEALTH_FAIL=true)", http.StatusInternalServerError)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	mux.Handle("GET /metrics", promhttp.Handler())

	srv := &http.Server{Addr: ":" + envOr("PORT", "8080"), Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	logger.Info("worker started", "id", me, "interval", interval, "batch", batch)
	if pool != nil {
		run(ctx, pool, interval, batch, me)
	} else {
		// Без базы worker живёт и отвечает на /health — база появится позже (оператор).
		logger.Warn("DATABASE_URL not set, idle until restarted with a database")
		<-ctx.Done()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
