package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "ghs_tokenvalue" //nolint:gosec // a made-up token the tests look for

type recorded struct {
	method, path, auth, body string
}

func newAPI(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, n int)) (*Client, *[]recorded) {
	t.Helper()

	var (
		mu   sync.Mutex
		seen []recorded
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}

		mu.Lock()

		seen = append(seen, recorded{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), string(body)})
		n := len(seen)
		mu.Unlock()

		handle(w, r, n)
	}))
	t.Cleanup(srv.Close)

	return &Client{API: srv.URL, Repository: "o/r", Token: testToken, HTTP: srv.Client(), pause: time.Millisecond}, &seen
}

func TestClientSetsStatusesAndRetries(t *testing.T) {
	c, seen := newAPI(t, func(w http.ResponseWriter, _ *http.Request, n int) {
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)

			return
		}

		w.WriteHeader(http.StatusCreated)
	})

	err := c.SetStatus(t.Context(), "abc", CommitStatus{State: "pending", Context: "pipelinegen/pr/e2e", Description: "0 of 3"})
	if err != nil {
		t.Fatal(err)
	}

	if len(*seen) != 2 {
		t.Fatalf("%d requests", len(*seen))
	}

	got := (*seen)[1]
	if got.method != http.MethodPost || got.path != "/repos/o/r/statuses/abc" || got.auth != "Bearer "+testToken {
		t.Fatalf("%+v", got)
	}

	var st CommitStatus
	if err := json.Unmarshal([]byte(got.body), &st); err != nil || st.Context != "pipelinegen/pr/e2e" {
		t.Fatalf("%s %v", got.body, err)
	}
}

func TestClientPagesCommentsAndWrites(t *testing.T) {
	c, seen := newAPI(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusOK)

			return
		}

		n := 100
		if r.URL.Query().Get("page") == "2" {
			n = 1
		}

		out := make([]IssueComment, 0, n)
		for i := range n {
			out = append(out, IssueComment{ID: int64(i), Body: r.URL.Query().Get("page")})
		}

		if err := json.NewEncoder(w).Encode(out); err != nil {
			t.Error(err)
		}
	})

	all, err := c.Comments(t.Context(), 7)
	if err != nil || len(all) != 101 || all[100].Body != "2" {
		t.Fatalf("%d %v", len(all), err)
	}

	if err := c.CreateComment(t.Context(), 7, "hi"); err != nil {
		t.Fatal(err)
	}

	if err := c.UpdateComment(t.Context(), 55, "there"); err != nil {
		t.Fatal(err)
	}

	if err := c.DeleteComment(t.Context(), 56); err != nil {
		t.Fatal(err)
	}

	var calls []string
	for _, s := range (*seen)[2:] {
		calls = append(calls, s.method+" "+s.path)
	}

	want := "POST /repos/o/r/issues/7/comments,PATCH /repos/o/r/issues/comments/55,DELETE /repos/o/r/issues/comments/56"
	if strings.Join(calls, ",") != want {
		t.Fatal(calls)
	}
}

func TestClientErrorsSayWhatFailedWithoutTheToken(t *testing.T) {
	c, seen := newAPI(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.WriteHeader(http.StatusUnprocessableEntity)

		if _, err := fmt.Fprint(w, `{"message": "Validation Failed"}`); err != nil {
			t.Error(err)
		}
	})

	err := c.SetStatus(t.Context(), "abc", CommitStatus{State: "bogus"})
	if !errors.Is(err, ErrGitHub) || !strings.Contains(err.Error(), "422") || !strings.Contains(err.Error(), "Validation Failed") || strings.Contains(err.Error(), testToken) {
		t.Fatal(err)
	}

	if len(*seen) != 1 {
		t.Fatalf("%d requests", len(*seen))
	}
}

func TestClientWaitsOutSecondaryRateLimits(t *testing.T) {
	c, seen := newAPI(t, func(w http.ResponseWriter, _ *http.Request, n int) {
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusForbidden)

			if _, err := fmt.Fprint(w, `{"message": "You have exceeded a secondary rate limit"}`); err != nil {
				t.Error(err)
			}

			return
		}

		if _, err := fmt.Fprint(w, `{"commit": {"message": "Merge pull request #123 from x/y\n\nbody"}}`); err != nil {
			t.Error(err)
		}
	})

	start := time.Now()

	subject, err := c.CommitSubject(t.Context(), "abc")
	if err != nil || subject != "Merge pull request #123 from x/y" || len(*seen) != 2 {
		t.Fatalf("%q %v %d", subject, err, len(*seen))
	}

	if time.Since(start) < time.Second {
		t.Fatal("Retry-After not waited for")
	}
}
