package runs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
)

const (
	headSHA  = "1111111111111111111111111111111111111111"
	olderSHA = "2222222222222222222222222222222222222222"
)

func fakeGitHub(t *testing.T, answers map[string]any) *GitHub {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")

		body, ok := answers[path]
		if !ok {
			if strings.HasSuffix(path, "/statuses") || strings.HasSuffix(path, "/timeline") {
				body = []any{}
			} else {
				http.NotFound(w, r)

				return
			}
		}

		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)

	return &GitHub{API: srv.URL, Repository: "o/r"}
}

func pullJSON(draft bool, labels ...string) map[string]any {
	ls := []any{}
	for _, l := range labels {
		ls = append(ls, map[string]any{"name": l})
	}

	return map[string]any{
		"number": 7, "state": "open", "draft": draft, "created_at": "2026-01-01T10:00:00Z",
		"head":   map[string]any{"sha": headSHA, "ref": "feat/notes"},
		"labels": ls,
	}
}

func prConfig(pr config.PullRequests) *config.Config {
	return &config.Config{Runs: map[string]config.Run{"pr": {Trigger: config.Trigger{PullRequests: &pr}}}}
}

var change = Change{Run: "pr", ID: "pr-7", Commit: headSHA, PR: 7}

func TestAdmitFilters(t *testing.T) {
	cases := []struct {
		name  string
		pr    config.PullRequests
		pull  map[string]any
		admit bool
	}{
		{"by default ready only, a draft", config.PullRequests{}, pullJSON(true), false},
		{"by default ready only, ready", config.PullRequests{}, pullJSON(false), true},
		{"ready only, a draft", config.PullRequests{Draft: config.DraftsReady}, pullJSON(true), false},
		{"drafts only, a draft", config.PullRequests{Draft: config.DraftsOnly}, pullJSON(true), true},
		{"drafts only, ready", config.PullRequests{Draft: config.DraftsOnly}, pullJSON(false), false},
		{"any, a draft", config.PullRequests{Draft: config.DraftsAny}, pullJSON(true), true},
		{"any, ready", config.PullRequests{Draft: config.DraftsAny}, pullJSON(false), true},
		{"needs a label", config.PullRequests{Labels: []string{"ci"}}, pullJSON(false), false},
		{"has the label", config.PullRequests{Labels: []string{"ci"}}, pullJSON(false, "ci"), true},
		{"label excluded", config.PullRequests{LabelsWithout: []string{"wip"}}, pullJSON(false, "wip"), false},
		{"head matches", config.PullRequests{Head: "feat/*"}, pullJSON(false), true},
		{"head doesn't", config.PullRequests{Head: "fix/*"}, pullJSON(false), false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gh := fakeGitHub(t, map[string]any{"repos/o/r/pulls/7": c.pull})

			ok, why, err := Admit(t.Context(), gh, prConfig(c.pr), change, "ci/pr")
			if err != nil {
				t.Fatal(err)
			}

			if ok != c.admit {
				t.Errorf("admitted %v (%s), want %v", ok, why, c.admit)
			}
		})
	}
}

func TestAdmitSkipsAHeadThatMovedOn(t *testing.T) {
	p := pullJSON(false)
	p["head"] = map[string]any{"sha": olderSHA, "ref": "feat/notes"}
	gh := fakeGitHub(t, map[string]any{"repos/o/r/pulls/7": p})

	ok, why, err := Admit(t.Context(), gh, prConfig(config.PullRequests{}), change, "ci/pr")
	if err != nil || ok {
		t.Fatalf("admitted %v (%s) %v", ok, why, err)
	}
}

func TestOncePerReady(t *testing.T) {
	commits := []any{map[string]any{"sha": olderSHA}, map[string]any{"sha": headSHA}}
	ranOlder := []any{map[string]any{"context": "ci/pr", "created_at": "2026-01-02T10:00:00Z"}}

	cases := []struct {
		name     string
		timeline []any
		admit    bool
	}{
		// the older head ran after the opening: a push to a ready pull request
		{"push after a run", []any{}, false},
		// marked ready again after the older head ran
		{"ready again", []any{map[string]any{"event": "ready_for_review", "created_at": "2026-01-03T10:00:00Z"}}, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gh := fakeGitHub(t, map[string]any{
				"repos/o/r/pulls/7":                           pullJSON(false),
				"repos/o/r/pulls/7/commits":                   commits,
				"repos/o/r/issues/7/timeline":                 c.timeline,
				"repos/o/r/commits/" + olderSHA + "/statuses": ranOlder,
			})

			ok, why, err := Admit(t.Context(), gh, prConfig(config.PullRequests{OncePerReady: true}), change, "ci/pr")
			if err != nil {
				t.Fatal(err)
			}

			if ok != c.admit {
				t.Errorf("admitted %v (%s), want %v", ok, why, c.admit)
			}
		})
	}
}
