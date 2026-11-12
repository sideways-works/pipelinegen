// Command notes serves the notes page and API.
//
//	notes [-addr :8080] [-seed notes.json]
//
// It stops cleanly on SIGINT or SIGTERM, which lets a coverage build write
// its counters. With NOTES_REQUEST_LOG set, it appends the route of every
// request it serves to that file, one "METHOD /pattern" line each.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"example.com/notes/api"
	"example.com/notes/notes"
	"example.com/notes/web"
)

func main() {
	addr := flag.String("addr", ":8080", "the address to listen on")
	seed := flag.String("seed", "", "a JSON file of notes to start with")
	flag.Parse()

	store := notes.NewStore()
	if *seed != "" {
		if err := load(store, *seed); err != nil {
			log.Fatal(err)
		}
	}

	mux := http.NewServeMux()
	mux.Handle("/notes", api.Handler(store))
	mux.Handle("/", http.FileServerFS(web.Files))

	var handler http.Handler = mux
	if path := os.Getenv("NOTES_REQUEST_LOG"); path != "" {
		handler = logRoutes(mux, path)
	}

	srv := &http.Server{Addr: *addr, Handler: handler}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()

	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// logRoutes appends the route pattern of each request to the file at path.
// The API's routes are registered on a mux of their own, so the pattern is
// the inner one: the generated handler sets it on the request.
func logRoutes(next http.Handler, path string) http.Handler {
	var mu sync.Mutex

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)

		pattern := r.Pattern
		if !strings.Contains(pattern, " ") {
			pattern = r.Method + " " + pattern
		}

		mu.Lock()
		defer mu.Unlock()

		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			log.Print(err)
			return
		}

		defer f.Close()

		fmt.Fprintln(f, pattern)
	})
}

// load adds the notes of a JSON file to the store.
func load(store *notes.Store, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var seed []notes.Note
	if err := json.Unmarshal(data, &seed); err != nil {
		return err
	}

	for _, n := range seed {
		store.Add(n.Title, n.Body)
	}

	return nil
}
