// api — HTTP-сервис магазина: принимает заказы и отдаёт их список из postgres.
//
// Эндпоинты:
//
//	GET  /health  — "ok <version>"; при HEALTH_FAIL=true отвечает 500 (для сломанного rollout)
//	POST /order   — создаёт заказ: {"item": "...", "qty": N}
//	GET  /orders  — список заказов (последние 100)
//	GET  /metrics — метрики Prometheus
//
// Переменные окружения: PORT (8080), DATABASE_URL, HEALTH_FAIL, APP_VERSION.
// /health не зависит от базы: под жив и готов, даже если postgres недоступен.
package main

import (
	"context"
	"encoding/json"
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
	logger  = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	version = envOr("APP_VERSION", "dev")

	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests.",
	}, []string{"method", "route", "code"})

	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})

	ordersCreated = promauto.NewCounter(prometheus.CounterOpts{
		Name: "shop_orders_created_total",
		Help: "Orders written to postgres.",
	})

	dbUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "shop_db_up",
		Help: "1 if the last postgres ping succeeded.",
	})
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

const schema = `
CREATE TABLE IF NOT EXISTS orders (
	id           BIGSERIAL PRIMARY KEY,
	item         TEXT        NOT NULL,
	qty          INT         NOT NULL,
	created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	processed    BOOLEAN     NOT NULL DEFAULT false,
	processed_at TIMESTAMPTZ,
	processed_by TEXT
)`

type order struct {
	ID          int64      `json:"id"`
	Item        string     `json:"item"`
	Qty         int        `json:"qty"`
	CreatedAt   time.Time  `json:"created_at"`
	Processed   bool       `json:"processed"`
	ProcessedAt *time.Time `json:"processed_at,omitempty"`
	ProcessedBy *string    `json:"processed_by,omitempty"`
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func instrument(route string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h(rec, r)
		requestsTotal.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
		requestDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	}
}

// watchDB держит схему и метрику shop_db_up в актуальном состоянии.
// Работает в фоне, поэтому api стартует и без базы.
func watchDB(ctx context.Context, pool *pgxpool.Pool) {
	schemaReady := false
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := pool.Ping(pingCtx)
		if err == nil && !schemaReady {
			if _, err = pool.Exec(pingCtx, schema); err == nil {
				schemaReady = true
				logger.Info("schema ready")
			}
		}
		cancel()
		if err != nil {
			dbUp.Set(0)
			logger.Warn("postgres unavailable", "error", err)
		} else {
			dbUp.Set(1)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var pool *pgxpool.Pool
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		var err error
		pool, err = pgxpool.New(ctx, dsn) // ленивое подключение, не падает без базы
		if err != nil {
			logger.Error("bad DATABASE_URL", "error", err)
			os.Exit(1)
		}
		defer pool.Close()
		go watchDB(ctx, pool)
	} else {
		logger.Warn("DATABASE_URL not set, /order and /orders will return 503")
	}

	healthFail := os.Getenv("HEALTH_FAIL") == "true"

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", instrument("/health", func(w http.ResponseWriter, _ *http.Request) {
		if healthFail {
			http.Error(w, "unhealthy (HEALTH_FAIL=true)", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "ok %s\n", version)
	}))

	mux.HandleFunc("POST /order", instrument("/order", func(w http.ResponseWriter, r *http.Request) {
		if pool == nil {
			http.Error(w, "database not configured", http.StatusServiceUnavailable)
			return
		}
		var in struct {
			Item string `json:"item"`
			Qty  int    `json:"qty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Item == "" || in.Qty <= 0 {
			http.Error(w, `usage: {"item": "name", "qty": N}`, http.StatusBadRequest)
			return
		}
		var o order
		err := pool.QueryRow(r.Context(),
			`INSERT INTO orders (item, qty) VALUES ($1, $2)
			 RETURNING id, item, qty, created_at, processed`, in.Item, in.Qty).
			Scan(&o.ID, &o.Item, &o.Qty, &o.CreatedAt, &o.Processed)
		if err != nil {
			logger.Error("insert order", "error", err)
			http.Error(w, "database error", http.StatusServiceUnavailable)
			return
		}
		ordersCreated.Inc()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(o)
	}))

	mux.HandleFunc("GET /orders", instrument("/orders", func(w http.ResponseWriter, r *http.Request) {
		if pool == nil {
			http.Error(w, "database not configured", http.StatusServiceUnavailable)
			return
		}
		rows, err := pool.Query(r.Context(),
			`SELECT id, item, qty, created_at, processed, processed_at, processed_by
			 FROM orders ORDER BY id DESC LIMIT 100`)
		if err != nil {
			logger.Error("select orders", "error", err)
			http.Error(w, "database error", http.StatusServiceUnavailable)
			return
		}
		defer rows.Close()
		list := []order{}
		for rows.Next() {
			var o order
			if err := rows.Scan(&o.ID, &o.Item, &o.Qty, &o.CreatedAt, &o.Processed, &o.ProcessedAt, &o.ProcessedBy); err != nil {
				http.Error(w, "database error", http.StatusServiceUnavailable)
				return
			}
			list = append(list, o)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
	}))

	mux.Handle("GET /metrics", promhttp.Handler())

	srv := &http.Server{Addr: ":" + envOr("PORT", "8080"), Handler: mux}
	go func() {
		logger.Info("listening", "addr", srv.Addr, "version", version, "health_fail", healthFail)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
