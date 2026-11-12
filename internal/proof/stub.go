package proof

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// stub is a stand-in for the GitHub API: it takes every status and
// comment a run posts, keeps none, and answers reads with nothing. It
// logs each request to its log file.
type stub struct {
	srv *http.Server
	ln  net.Listener
	log *os.File
	mu  sync.Mutex
}

func startStub(ctx context.Context, logFile string) (*stub, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("the API stand-in did not start: %w", err)
	}

	f, err := os.Create(logFile) //nolint:gosec // in the proof's work directory
	if err != nil {
		_ = ln.Close() //nolint:errcheck // not started

		return nil, fmt.Errorf("the API stand-in did not start: %w", err)
	}

	s := &stub{ln: ln, log: f}
	s.srv = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 10 * time.Second}

	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.say(err.Error())
		}
	}()

	return s, nil
}

func (s *stub) Addr() string { return s.ln.Addr().String() }

func (s *stub) say(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, _ = fmt.Fprintln(s.log, line) //nolint:errcheck // a log
}

func (s *stub) serve(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body) //nolint:errcheck // the body is not kept
	s.say(r.Method + " " + r.URL.Path)

	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/status"):
		_, _ = io.WriteString(w, `{"state": "pending", "statuses": []}`) //nolint:errcheck // the client's to read
	case r.Method == http.MethodGet:
		_, _ = io.WriteString(w, `[]`) //nolint:errcheck // the client's to read
	default:
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id": 1}`) //nolint:errcheck // the client's to read
	}
}

func (s *stub) Close() {
	_ = s.srv.Close() //nolint:errcheck // stopping
	_ = s.log.Close() //nolint:errcheck // a log
}
