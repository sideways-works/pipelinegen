package forgeplugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/plugin"
)

type script struct {
	Rounds [][]plugin.HTTPRequest `json:"rounds"`
	Both   bool                   `json:"both,omitempty"`
	Empty  bool                   `json:"empty,omitempty"`
	Remote bool                   `json:"remote,omitempty"`
}

type seen struct {
	ID      string            `json:"id"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"`
}

// TestMain lets the test binary act as a forge when asked, so the
// exchange calls a real executable, round by round, keeping no state.
func TestMain(m *testing.M) {
	if os.Getenv("FORGE_TEST_SERVE") == "" {
		os.Exit(m.Run())
	}

	handle := func(in []byte) (any, error) {
		var req plugin.ExchangeRequest
		if err := json.Unmarshal(in, &req); err != nil {
			return nil, fmt.Errorf("request: %w", err)
		}

		var s script
		if err := json.Unmarshal(req.Args, &s); err != nil {
			return nil, fmt.Errorf("args: %w", err)
		}

		// the round to answer is the one whose requests have no
		// responses yet
		next, sum := 0, 0
		for next < len(s.Rounds) && sum+len(s.Rounds[next]) <= len(req.Responses) {
			sum += len(s.Rounds[next])
			next++
		}

		if next < len(s.Rounds) {
			return plugin.Round{Requests: s.Rounds[next]}, nil
		}

		var out []seen

		if s.Remote {
			remote, _ := req.Config["remote"].(string) //nolint:errcheck // absent is the answer

			out = append(out, seen{ID: "remote", Body: remote})
		}

		for _, r := range req.Responses {
			body, err := os.ReadFile(filepath.Join(req.Work, r.BodyFile))
			if err != nil {
				return nil, fmt.Errorf("body: %w", err)
			}

			out = append(out, seen{ID: r.ID, Status: r.Status, Headers: r.Headers, Body: string(body)})
		}

		done, err := json.Marshal(out)
		if err != nil {
			return nil, fmt.Errorf("answer: %w", err)
		}

		switch {
		case s.Both:
			return plugin.Round{Done: done, Requests: []plugin.HTTPRequest{{ID: "late", Method: "GET", URL: "http://x"}}}, nil
		case s.Empty:
			return plugin.Round{}, nil
		}

		return plugin.Round{Done: done}, nil
	}

	handlers := map[string]plugin.Handler{}
	for _, c := range plugin.ForgeCalls {
		handlers[c] = handle
	}

	testenv.ExitChild(m, plugin.Serve(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, handlers))
}

const (
	changesToken = "changes-token-value-5d1c"
	reportToken  = "report-token-value-77ab"
)

func fakeForge(t *testing.T) plugin.Plugin {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "fake-forge")
	if err := testenv.WriteExecutable(t.Context(), path, []byte("#!/bin/sh\nFORGE_TEST_SERVE=1 exec '"+self+"' \"$@\"\n")); err != nil {
		t.Fatal(err)
	}

	return plugin.Plugin{Name: "fake-forge", Path: path}
}

type host struct {
	*httptest.Server

	mu   sync.Mutex
	got  []*http.Request
	answ func(w http.ResponseWriter, r *http.Request, n int)
}

func newHost(t *testing.T, answer func(w http.ResponseWriter, r *http.Request, n int)) *host {
	t.Helper()

	h := &host{answ: answer}
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.Close)

	return h
}

func (h *host) serve(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.got = append(h.got, r.Clone(r.Context()))
	n := len(h.got)
	h.mu.Unlock()

	if h.answ != nil {
		h.answ(w, r, n)

		return
	}

	w.Header().Set("Link", "<next>")
	w.Header().Set("X-Other", "dropped")
	write(w, "body of "+r.URL.Path)
}

func write(w http.ResponseWriter, body string) {
	if _, err := io.WriteString(w, body); err != nil { //nolint:gosec // a test server's own text
		return
	}
}

func (h *host) hits() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return len(h.got)
}

func (h *host) request(i int) *http.Request {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.got[i]
}

func newExchange(t *testing.T, h *host) *Exchange {
	t.Helper()

	hosts, err := Allowed(h.URL, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	desc := plugin.Description{
		Kind: plugin.KindForge, Auth: &plugin.ForgeAuth{Header: "Authorization", Scheme: "Bearer"},
		RequestHeaders: []string{"Accept"}, ResponseHeaders: []string{"Link"},
	}

	return &Exchange{
		Plugin: fakeForge(t), Desc: desc, Hosts: hosts,
		Tokens: map[string]string{plugin.PurposeChanges: changesToken, plugin.PurposeReport: reportToken},
		pause:  time.Millisecond, maxWait: 20 * time.Millisecond,
	}
}

func run(t *testing.T, e *Exchange, call string, s script) ([]seen, error) {
	t.Helper()

	var out []seen

	err := e.Call(t.Context(), call, t.TempDir(), s, &out)

	return out, err
}

func get(id, u string, auth string) plugin.HTTPRequest {
	r := plugin.HTTPRequest{ID: id, Method: "GET", URL: u}
	if auth != "" {
		r.Auth = &plugin.HTTPAuth{Purpose: auth}
	}

	return r
}

func TestExchangeRounds(t *testing.T) {
	t.Parallel()

	h := newHost(t, nil)
	e := newExchange(t, h)

	first := get("page-1", h.URL+"/pulls?page=1", plugin.PurposeChanges)
	first.Headers = map[string]string{"accept": "application/json"}

	out, err := run(t, e, plugin.CallChanges, script{Rounds: [][]plugin.HTTPRequest{
		{first, get("anon", h.URL+"/open", "")},
		{get("page-2", h.URL+"/pulls?page=2", plugin.PurposeChanges)},
	}})
	if err != nil {
		t.Fatal(err)
	}

	if len(out) != 3 || out[0].ID != "page-1" || out[2].ID != "page-2" || out[2].Body != "body of /pulls" {
		t.Fatalf("responses = %+v", out)
	}

	if out[0].Headers["Link"] != "<next>" || out[0].Headers["X-Other"] != "" || len(out[0].Headers) != 1 {
		t.Errorf("headers = %v, want only Link", out[0].Headers)
	}

	if got := h.request(0).Header.Get("Authorization"); got != "Bearer "+changesToken {
		t.Errorf("page-1 Authorization = %q", got)
	}

	if got := h.request(0).Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q", got)
	}

	if got := h.request(1).Header.Get("Authorization"); got != "" {
		t.Errorf("a request without auth carried %q", got)
	}
}

func TestExchangeCustomAuthHeader(t *testing.T) {
	t.Parallel()

	h := newHost(t, nil)
	e := newExchange(t, h)
	e.Desc.Auth = &plugin.ForgeAuth{Header: "PRIVATE-TOKEN"}

	if _, err := run(t, e, plugin.CallStatus, script{Rounds: [][]plugin.HTTPRequest{{get("s", h.URL+"/s", plugin.PurposeReport)}}}); err != nil {
		t.Fatal(err)
	}

	r := h.request(0)
	if r.Header.Get("Private-Token") != reportToken || r.Header.Get("Authorization") != "" {
		t.Errorf("headers = %v", r.Header)
	}
}

func TestAllowedAPIHosts(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name                string
		declared, confirmed bool
	}{
		{name: "declared only", declared: true},
		{name: "confirmed only", confirmed: true},
		{name: "both", declared: true, confirmed: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			h, api := newHost(t, nil), newHost(t, nil)

			var declared, confirmed []string
			if c.declared {
				declared = []string{api.URL}
			}

			if c.confirmed {
				confirmed = []string{api.URL + "/"}
			}

			hosts, err := Allowed(h.URL, declared, confirmed)
			if err != nil {
				t.Fatal(err)
			}

			e := newExchange(t, h)
			e.Hosts = hosts
			sent := c.declared && c.confirmed

			_, err = run(t, e, plugin.CallChanges, script{Rounds: [][]plugin.HTTPRequest{{get("a", api.URL+"/x", plugin.PurposeChanges)}}})

			switch {
			case sent && (err != nil || api.hits() != 1):
				t.Fatalf("err = %v, hits %d", err, api.hits())
			case !sent && (!errors.Is(err, ErrRefused) || api.hits() != 0):
				t.Fatalf("err = %v, hits %d, want refused", err, api.hits())
			}
		})
	}
}

func TestForgeHoldsNoToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", changesToken)

	h := newHost(t, nil)
	e := newExchange(t, h)
	work := t.TempDir()

	var out []seen
	if err := e.Call(t.Context(), plugin.CallChanges, work, script{Rounds: [][]plugin.HTTPRequest{{get("a", h.URL+"/a", plugin.PurposeChanges)}}}, &out); err != nil {
		t.Fatal(err)
	}

	err := filepath.WalkDir(work, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		data, err := os.ReadFile(path) //nolint:gosec // the test's own directory
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		if strings.Contains(string(data), changesToken) {
			t.Errorf("%s holds the token", path)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOrigin(t *testing.T) {
	t.Parallel()

	for remote, want := range map[string]string{
		"https://GitHub.com/o/r.git":         "https://github.com",
		"https://git.example:443/o/r":        "https://git.example",
		"http://127.0.0.1:3000/o/r":          "http://127.0.0.1:3000",
		"ssh://git@git.example:2222/o/r.git": "https://git.example",
		"git@git.example:o/r.git":            "https://git.example",
		"git.example:o/r":                    "https://git.example",
		"git://git.example/o/r":              "https://git.example",
		"https://[::1]:8443/o/r":             "https://[::1]:8443",
		"file:///srv/git/r.git":              "",
		"/srv/git/r.git":                     "",
		"./r":                                "",
	} {
		got, err := Origin(remote)
		if (err != nil) != (want == "") || got != want {
			t.Errorf("Origin(%q) = %q, %v; want %q", remote, got, err, want)
		}
	}
}

func TestEveryCallKnowsTheRemote(t *testing.T) {
	t.Parallel()

	h := newHost(t, nil)
	e := newExchange(t, h)
	e.Remote = "https://git.example.test/team/app.git"
	e.Plugin.Config = map[string]any{"api": "x"}

	out, err := run(t, e, plugin.CallLinks, script{Remote: true})
	if err != nil || len(out) != 1 || out[0].Body != e.Remote {
		t.Fatalf("%+v %v", out, err)
	}

	if _, ok := e.Plugin.Config["remote"]; ok {
		t.Error("the forge's own block was changed")
	}
}

// A refusal reads as it always has, naming the forge.
func TestARefusalNamesTheForge(t *testing.T) {
	t.Parallel()

	h := newHost(t, nil)

	_, err := run(t, newExchange(t, h), plugin.CallChanges, script{Rounds: [][]plugin.HTTPRequest{{get("a", "file:///etc/passwd", "")}}})
	if !errors.Is(err, ErrRefused) || err.Error() != `forge request refused: fake-forge changes: request "a": scheme "file"` {
		t.Fatalf("%v", err)
	}

	r := get("h", h.URL+"/h", "")
	r.Headers = map[string]string{"Cookie": "a=b"}

	e := newExchange(t, h)
	e.Desc.RequestHeaders = []string{"Cookie"}

	_, err = run(t, e, plugin.CallChanges, script{Rounds: [][]plugin.HTTPRequest{{r}}})
	if err == nil || err.Error() != `forge request refused: fake-forge changes: request "h": header Cookie is never set by a forge` {
		t.Fatalf("%v", err)
	}

	if h.hits() != 0 {
		t.Fatalf("%d refused requests went out", h.hits())
	}
}
