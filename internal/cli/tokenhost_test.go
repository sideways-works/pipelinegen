package cli

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/plugin"
)

type otherHost struct {
	*httptest.Server

	mu   sync.Mutex
	seen []string
}

func newOtherHost(t *testing.T) *otherHost {
	t.Helper()

	h := &otherHost{}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.seen = append(h.seen, r.Header.Get("Authorization"))
		h.mu.Unlock()

		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(h.Close)

	return h
}

func (h *otherHost) requests() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	withAuth := 0

	for _, a := range h.seen {
		if a != "" {
			withAuth++
		}
	}

	return len(h.seen), withAuth
}

func TestAChangeCannotSendTheTokenElsewhere(t *testing.T) {
	root, _, trigger, ch, _ := proposedChange(t)

	other := newOtherHost(t)
	t.Setenv("GITHUB_TOKEN", "the-adopters-token-value")

	writeFile(t, filepath.Join(trigger, "pipelinegen.yaml"), strings.Replace(readFile(t, filepath.Join(root, "pipelinegen.yaml")),
		"github: {repository: o/r, token: gh}", "github: {repository: o/r, token: gh}\ngit: {remote: \""+other.URL+"/elsewhere.git\"}", 1))

	out := t.TempDir()

	steps := map[string][]string{
		"plan, first step (read)":   {"plan", "-run", "pr", "-trigger", trigger, "-out", out, "-cache", t.TempDir()},
		"plan, second step (store)": {"plan", "-run", "pr", "-trigger", trigger, "-from", out, "-cache", t.TempDir()},
		"plan, one step (store)":    {"plan", "-run", "pr", "-trigger", trigger, "-cache", t.TempDir()},
		"tree (store)":              {"tree", "-run", "pr", "-suite", "e2e", "-trigger", trigger, "-out", t.TempDir(), "-gate", t.TempDir()},
		"report suite (store)":      {"report", "suite", "-root", trigger},
		"lock (store)":              {"lock", "acquire", "browsers", "-root", trigger},
	}

	t.Setenv(envTrigger, trigger)
	t.Setenv(envChange, ch.ID)
	t.Setenv(envCommit, ch.Commit)

	for name, args := range steps {
		before, _ := other.requests()

		_, stderr, _ := run(t, args...)

		all, withAuth := other.requests()
		if all == before {
			t.Errorf("%s never reached the change's remote: %s", name, stderr)
		}

		if withAuth != 0 {
			t.Fatalf("%s sent a credential to the change's remote", name)
		}
	}
}

func TestTheTrustedHostKeepsItsToken(t *testing.T) {
	root, _, _ := runEnv(t)
	t.Setenv("GITHUB_TOKEN", "the-adopters-token-value")

	cfg := loadConfig(t, root)

	if s := openStore(cfg); s.Token != "the-adopters-token-value" {
		t.Fatalf("the repository's own store: %q", s.Token)
	}

	if got := remoteToken(cfg, plugin.PurposeRead, repoURL(cfg.GitHub.Repository)); got != "the-adopters-token-value" {
		t.Fatalf("read: %q", got)
	}

	t.Setenv(envGitURL, "")

	for remote, trusted := range map[string]bool{
		"https://github.com/o/r.git":          true,
		"https://GitHub.com/o/r.git":          true,
		"https://github.com.evil.example/r":   false,
		"https://x-access-token@github.com/r": false,
		"http://github.com/o/r.git":           false,
		"https://evil.example/github.com/r":   false,
	} {
		if onTrustedHost(remote) != trusted {
			t.Errorf("%s: trusted %v", remote, !trusted)
		}
	}
}

func TestTheOtherWaysATokenLeavesStayOnTheirHost(t *testing.T) {
	root, _, _ := runEnv(t)
	cfg := loadConfig(t, root)
	cfg.Git.Remote = "https://evil.example/r.git"

	// fetches of the code: the token's scope is GitHub's git host, whatever
	// remote the config names.
	if a := readAuth(t.Context(), cfg, root); a.Scope != runs.GitHubScope {
		t.Errorf("fetch scope %q", a.Scope)
	}

	// statuses and comments: the API is the step's environment's, never the
	// config's.
	t.Setenv(envAPIURL, "https://api.example.test")

	if c, ok := host(cfg).(*report.Client); !ok || c.API != "https://api.example.test" {
		t.Errorf("report client %+v", host(cfg))
	}

	// the lock's store: on its own repository only with lock.repository,
	// which names a repository on the trusted host.
	if l, err := openLock(cfg, "browsers"); err != nil || l.Store.Token != "" {
		t.Errorf("lock store token %q (%v)", l.Store.Token, err)
	}
}
