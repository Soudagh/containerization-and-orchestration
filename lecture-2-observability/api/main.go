package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// RED-метрики: Rate (requests_total), Errors (errors_total), Duration (histogram).
var (
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests.",
	}, []string{"method", "route", "code"})

	errorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_errors_total",
		Help: "Total HTTP responses with 5xx status.",
	}, []string{"route"})

	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 1.5, 2, 2.5, 3, 5},
	}, []string{"method", "route"})
)

var (
	logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	tracer = otel.Tracer("api")
	port   = envOr("PORT", "8080")
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// initTracing всегда ставит SDK-провайдер (чтобы trace_id был в логах),
// а экспорт в OTLP включает, только если задан OTEL_EXPORTER_OTLP_ENDPOINT.
func initTracing(ctx context.Context) (func(context.Context) error, error) {
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(envOr("OTEL_SERVICE_NAME", "api")),
	))
	if err != nil {
		return nil, err
	}

	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		exp, err := otlptracehttp.New(ctx) // endpoint/протокол читаются из OTEL_* env
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	return tp.Shutdown, nil
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// instrument: otelhttp создаёт корневой спан, внутри — метрики и JSON-лог с trace_id.
func instrument(route string, h http.HandlerFunc) http.Handler {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h(rec, r)
		elapsed := time.Since(start)

		code := strconv.Itoa(rec.status)
		requestsTotal.WithLabelValues(r.Method, route, code).Inc()
		requestDuration.WithLabelValues(r.Method, route).Observe(elapsed.Seconds())
		if rec.status >= 500 {
			errorsTotal.WithLabelValues(route).Inc()
		}

		sc := trace.SpanContextFromContext(r.Context())
		level := slog.LevelInfo
		if rec.status >= 500 {
			level = slog.LevelError
		}
		logger.LogAttrs(r.Context(), level, "request",
			slog.String("method", r.Method),
			slog.String("route", route),
			slog.Int("status", rec.status),
			slog.Float64("duration_ms", float64(elapsed.Microseconds())/1000),
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	})
	return otelhttp.NewHandler(inner, route)
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	fmt.Fprintln(w, "ok")
}

// initMetrics заранее создаёт серии с нулём: иначе серия появляется только при
// первом Inc(), и rate()/increase() не видят первый прирост.
func initMetrics(routes map[string]string) {
	for route, code := range routes {
		requestsTotal.WithLabelValues(http.MethodGet, route, code)
		requestDuration.WithLabelValues(http.MethodGet, route)
		errorsTotal.WithLabelValues(route)
	}
}

func handleFail(w http.ResponseWriter, r *http.Request) {
	span := trace.SpanFromContext(r.Context())
	err := errors.New("simulated failure")
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func handleSlow(w http.ResponseWriter, r *http.Request) {
	delay := time.Duration(1000+rand.IntN(2000)) * time.Millisecond

	_, span := tracer.Start(r.Context(), "slow-op")
	span.SetAttributes(attribute.Int64("delay_ms", delay.Milliseconds()))
	time.Sleep(delay)
	span.End()

	fmt.Fprintf(w, "slept %s\n", delay)
}

// handleLoad: пачка параллельных запросов к себе же. Клиент обёрнут в otelhttp,
// поэтому дочерние запросы попадают в тот же трейс.
func handleLoad(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.URL.Query().Get("n"))
	if err != nil || n <= 0 {
		n = 100
	}
	target := r.URL.Query().Get("target")
	if target == "" {
		target = "health"
	}

	client := http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport), Timeout: 10 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%s/%s", port, target)

	var wg sync.WaitGroup
	sem := make(chan struct{}, 20)
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
			if resp, err := client.Do(req); err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	fmt.Fprintf(w, "sent %d requests to /%s\n", n, target)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := initTracing(ctx)
	if err != nil {
		logger.Error("tracing init failed", "error", err)
		os.Exit(1)
	}

	initMetrics(map[string]string{"/health": "200", "/fail": "500", "/slow": "200", "/load": "200"})

	mux := http.NewServeMux()
	// Пробы kubelet — без instrument: не попадают в RED-метрики, логи и трейсы.
	mux.HandleFunc("GET /livez", handleHealth)
	mux.HandleFunc("GET /readyz", handleHealth)
	mux.Handle("GET /health", instrument("/health", handleHealth))
	mux.Handle("GET /fail", instrument("/fail", handleFail))
	mux.Handle("GET /slow", instrument("/slow", handleSlow))
	mux.Handle("GET /load", instrument("/load", handleLoad))
	mux.Handle("GET /metrics", promhttp.Handler())

	srv := &http.Server{Addr: ":" + port, Handler: mux}
	go func() {
		logger.Info("listening", "port", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	_ = shutdownTracing(shutdownCtx)
}
