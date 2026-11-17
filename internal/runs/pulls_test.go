package runs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/plugin"
)

const (
	headSHA  = "1111111111111111111111111111111111111111"
	olderSHA = "2222222222222222222222222222222222222222"
)

type listed struct {
	changes []plugin.Change
	asked   []plugin.ChangesArgs
}

func (l *listed) Changes(_ context.Context, a plugin.ChangesArgs) ([]plugin.Change, error) {
	l.asked = append(l.asked, a)

	return l.changes, nil
}

func pull(draft bool, labels ...string) plugin.Change {
	ready := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	return plugin.Change{
		ID: "pr-7", Number: 7, Base: "main", Head: headSHA, HeadBranch: "feat/notes", Draft: draft, Labels: labels,
		From: plugin.FromSame, ReadySince: &ready,
	}
}

func prConfig(pr config.PullRequests) *config.Config {
	pr.Base = "main"

	return &config.Config{Runs: map[string]config.Run{"pr": {Trigger: config.Trigger{PullRequests: &pr}}}}
}

var change = Change{Run: "pr", ID: "pr-7", Commit: headSHA, PR: 7}

func noneRan(time.Time) (string, error) { return "", nil }

func TestAdmitFilters(t *testing.T) {
	cases := []struct {
		name  string
		pr    config.PullRequests
		pull  plugin.Change
		admit bool
	}{
		{"by default ready only, a draft", config.PullRequests{}, pull(true), false},
		{"by default ready only, ready", config.PullRequests{}, pull(false), true},
		{"ready only, a draft", config.PullRequests{Draft: config.DraftsReady}, pull(true), false},
		{"drafts only, a draft", config.PullRequests{Draft: config.DraftsOnly}, pull(true), true},
		{"drafts only, ready", config.PullRequests{Draft: config.DraftsOnly}, pull(false), false},
		{"any, a draft", config.PullRequests{Draft: config.DraftsAny}, pull(true), true},
		{"any, ready", config.PullRequests{Draft: config.DraftsAny}, pull(false), true},
		{"needs a label", config.PullRequests{Labels: []string{"ci"}}, pull(false), false},
		{"has the label", config.PullRequests{Labels: []string{"ci"}}, pull(false, "ci"), true},
		{"label excluded", config.PullRequests{LabelsWithout: []string{"wip"}}, pull(false, "wip"), false},
		{"head matches", config.PullRequests{Head: "feat/*"}, pull(false), true},
		{"head doesn't", config.PullRequests{Head: "fix/*"}, pull(false), false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			host := &listed{changes: []plugin.Change{c.pull}}

			ok, why, err := Admit(t.Context(), host, prConfig(c.pr), change, noneRan)
			if err != nil {
				t.Fatal(err)
			}

			if ok != c.admit {
				t.Errorf("admitted %v (%s), want %v", ok, why, c.admit)
			}

			if a := host.asked[0]; a.Base != "main" || a.Filters.Head != c.pr.Head {
				t.Errorf("asked %+v", a)
			}
		})
	}
}

func TestAdmitSkipsAHeadThatMovedOn(t *testing.T) {
	p := pull(false)
	p.Head = olderSHA

	ok, why, err := Admit(t.Context(), &listed{changes: []plugin.Change{p}}, prConfig(config.PullRequests{}), change, noneRan)
	if err != nil || ok || !strings.Contains(why, "moved on") {
		t.Fatalf("admitted %v (%s) %v", ok, why, err)
	}
}

func TestAdmitSkipsAClosedPullRequest(t *testing.T) {
	ok, why, err := Admit(t.Context(), &listed{}, prConfig(config.PullRequests{}), change, noneRan)
	if err != nil || ok || !strings.Contains(why, "is closed") {
		t.Fatalf("admitted %v (%s) %v", ok, why, err)
	}
}

// A change the connector can't tell comes from the repository itself is a
// fork's, whatever else it says.
func TestAdmitNeverTakesAFork(t *testing.T) {
	for _, from := range []string{plugin.FromFork, plugin.FromUnknown, ""} {
		p := pull(false)
		p.From = from

		ok, why, err := Admit(t.Context(), &listed{changes: []plugin.Change{p}}, prConfig(config.PullRequests{}), change, noneRan)
		if err != nil || ok || !strings.Contains(why, "fork") {
			t.Fatalf("%q: admitted %v (%s) %v", from, ok, why, err)
		}

		heads, err := OpenHeads(t.Context(), &listed{changes: []plugin.Change{p}}, &config.PullRequests{Base: "main"})
		if err != nil || len(heads) != 0 {
			t.Fatalf("%q: heads %+v %v", from, heads, err)
		}
	}
}

func TestOncePerReady(t *testing.T) {
	ready := time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC)
	planned := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)

	ran := func(since time.Time) (string, error) {
		if planned.After(since) {
			return olderSHA, nil
		}

		return "", nil
	}

	cases := []struct {
		name  string
		ready time.Time
		admit bool
	}{
		// the older head was planned after the opening: a push to a ready pull request
		{"push after a run", time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), false},
		// marked ready again after the older head was planned
		{"ready again", ready, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := pull(false)
			p.ReadySince = &c.ready
			host := &listed{changes: []plugin.Change{p}}

			ok, why, err := Admit(t.Context(), host, prConfig(config.PullRequests{OncePerReady: true}), change, ran)
			if err != nil {
				t.Fatal(err)
			}

			if ok != c.admit || !host.asked[0].Filters.ReadySince {
				t.Errorf("admitted %v (%s), want %v; asked %+v", ok, why, c.admit, host.asked)
			}
		})
	}
}
