// api — a tiny HTTP service for Lab 1. Endpoints:
//   GET /health      -> "ok"
//   GET /eat?mb=N    -> allocate N MiB, touch every page, keep it forever
//   GET /burn        -> spin one goroutine in an infinite loop (loads one core)
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"sync"
)

var (
	mu    sync.Mutex
	held  [][]byte // memory we never release, so RSS actually grows
	burns int
)

func health(w http.ResponseWriter, _ *http.Request) {
	fmt.Fprintln(w, "ok v2")
}

func eat(w http.ResponseWriter, r *http.Request) {
	mb, err := strconv.Atoi(r.URL.Query().Get("mb"))
	if err != nil || mb <= 0 {
		http.Error(w, "usage: /eat?mb=N", http.StatusBadRequest)
		return
	}
	buf := make([]byte, mb<<20)
	// Touch every page: a plain make() only reserves virtual memory;
	// the kernel hands out real pages on first write.
	for i := 0; i < len(buf); i += os.Getpagesize() {
		buf[i] = 1
	}
	mu.Lock()
	held = append(held, buf)
	total := 0
	for _, b := range held {
		total += len(b)
	}
	mu.Unlock()
	fmt.Fprintf(w, "ate %d MiB, holding %d MiB total\n", mb, total>>20)
}

func burn(w http.ResponseWriter, _ *http.Request) {
	go func() {
		runtime.LockOSThread() // pin the spin to one OS thread = one core
		for {
		}
	}()
	mu.Lock()
	burns++
	n := burns
	mu.Unlock()
	fmt.Fprintf(w, "burning: %d core(s) busy\n", n)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("/health", health)
	http.HandleFunc("/eat", eat)
	http.HandleFunc("/burn", burn)
	log.Printf("api listening on :%s (pid %d)", port, os.Getpid())
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
