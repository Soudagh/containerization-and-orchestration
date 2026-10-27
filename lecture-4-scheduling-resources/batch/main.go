// batch — заглушка «ночной аналитики»: бесконечно жрёт CPU и память, ничего полезного не делая.
//
// Переменные окружения:
//
//	CPU_WORKERS  — сколько горутин крутят пустой цикл (по умолчанию 1; 0 — не грузить CPU)
//	MEMORY_MB    — сколько МиБ занять и держать (по умолчанию 64)
//	RAMP_SECONDS — за сколько секунд плавно набрать MEMORY_MB (по умолчанию 0 — сразу)
//	PORT         — порт для /health и /metrics (по умолчанию 8080)
//
// /health нужен только потому, что гвардрейлы требуют пробы у каждого пода.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

	heldBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "batch_memory_held_bytes",
		Help: "Memory allocated and touched by batch.",
	})
	cpuWorkers = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "batch_cpu_workers",
		Help: "Goroutines spinning on CPU.",
	})
)

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		logger.Error("bad env", "key", key, "value", v)
		os.Exit(1)
	}
	return n
}

// eatMemory набирает mb мебибайт кусками по 1 МиБ, касаясь каждой страницы:
// без записи ядро не выдаёт реальные страницы и RSS не растёт.
func eatMemory(mb, rampSeconds int) [][]byte {
	held := make([][]byte, 0, mb)
	var pause time.Duration
	if rampSeconds > 0 && mb > 0 {
		pause = time.Duration(rampSeconds) * time.Second / time.Duration(mb)
	}
	page := os.Getpagesize()
	for i := 0; i < mb; i++ {
		chunk := make([]byte, 1<<20)
		for j := 0; j < len(chunk); j += page {
			chunk[j] = 1
		}
		held = append(held, chunk)
		heldBytes.Set(float64(len(held)) * (1 << 20))
		if pause > 0 {
			time.Sleep(pause)
		}
	}
	return held
}

func main() {
	workers := envInt("CPU_WORKERS", 1)
	memMB := envInt("MEMORY_MB", 64)
	ramp := envInt("RAMP_SECONDS", 0)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.Handle("GET /metrics", promhttp.Handler())
	go func() {
		addr := ":" + strconv.Itoa(envInt("PORT", 8080))
		if err := http.ListenAndServe(addr, mux); err != nil {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	for i := 0; i < workers; i++ {
		go func() {
			for {
			}
		}()
	}
	cpuWorkers.Set(float64(workers))
	logger.Info("batch started", "cpu_workers", workers, "memory_mb", memMB, "ramp_seconds", ramp)

	held := eatMemory(memMB, ramp)
	logger.Info("memory held", "mb", len(held))

	for {
		time.Sleep(time.Hour)
		runtime.KeepAlive(held)
	}
}
