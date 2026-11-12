// Command words is a small HTTP server: /upper?s=WORDS answers WORDS in
// capitals, /count?s=WORDS how many words there are, and /healthz nothing,
// once it is up. It listens on ADDR until it is stopped.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func upper(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, Upper(r.URL.Query().Get("s")))
}

func count(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, strconv.Itoa(Count(r.URL.Query().Get("s"))))
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /upper", upper)
	mux.HandleFunc("GET /count", count)
	mux.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})

	srv := &http.Server{Addr: os.Getenv("ADDR"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	// a clean stop on SIGTERM lets a coverage build write its last counters
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
