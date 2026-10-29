// loadgen — постоянный клиент shop: шлёт POST /order с заданной частотой и считает,
// сколько запросов прошло, а сколько упало. Метрики отдаёт в Prometheus, чтобы
// доказать графиком «0 упавших запросов» на фоне хаоса подов.
//
// Переменные окружения:
//
//	TARGET     — адрес api, по имени сервиса (по умолчанию http://shop-api.shop.svc:8080)
//	RPS        — запросов в секунду (по умолчанию 20)
//	TIMEOUT    — таймаут одного запроса (по умолчанию 2s)
//	KEEPALIVE  — переиспользовать TCP-соединения (по умолчанию true; false — новое соединение на каждый запрос)
//	PORT       — порт для /health и /metrics (по умолчанию 8080)
//
// Метрики:
//
//	loadgen_requests_total{result="ok|http_error|conn_error"}
//	loadgen_request_duration_seconds — гистограмма задержки успешных и неуспешных запросов
//
// Каждая ошибка пишется в лог с причиной — по ней видно, на каком этапе запрос упал.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

	requests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "loadgen_requests_total",
		Help: "Requests sent by loadgen, by result.",
	}, []string{"result"})

	duration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "loadgen_request_duration_seconds",
		Help:    "Request latency as seen by the client.",
		Buckets: prometheus.DefBuckets,
	}, []string{"result"})
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	target := envOr("TARGET", "http://shop-api.shop.svc:8080") + "/order"
	rps, err := strconv.Atoi(envOr("RPS", "20"))
	if err != nil || rps <= 0 {
		logger.Error("bad RPS")
		os.Exit(1)
	}
	timeout, err := time.ParseDuration(envOr("TIMEOUT", "2s"))
	if err != nil {
		logger.Error("bad TIMEOUT", "error", err)
		os.Exit(1)
	}
	keepAlive := envOr("KEEPALIVE", "true") == "true"

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = !keepAlive
	client := &http.Client{Timeout: timeout, Transport: transport}

	// Заранее создаём все результаты, чтобы счётчики были видны с нулём ещё до первой ошибки.
	for _, r := range []string{"ok", "http_error", "conn_error"} {
		requests.WithLabelValues(r)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.Handle("GET /metrics", promhttp.Handler())
	srv := &http.Server{Addr: ":" + envOr("PORT", "8080"), Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	logger.Info("loadgen started", "target", target, "rps", rps, "timeout", timeout, "keepalive", keepAlive)

	ticker := time.NewTicker(time.Second / time.Duration(rps))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
			return
		case <-ticker.C:
			go send(client, target)
		}
	}
}

func send(client *http.Client, target string) {
	start := time.Now()
	resp, err := client.Post(target, "application/json", bytes.NewBufferString(`{"item":"book","qty":1}`))
	result := "ok"
	switch {
	case err != nil:
		result = "conn_error"
		logger.Warn("request failed", "error", err)
	default:
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			result = "http_error"
			logger.Warn("bad status", "code", resp.StatusCode)
		}
	}
	requests.WithLabelValues(result).Inc()
	duration.WithLabelValues(result).Observe(time.Since(start).Seconds())
}
