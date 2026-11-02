package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
	"github.com/sideways-works/pipelinegen/plugin"
)

// The sneaky one links t:a to t:c while texts/flag.txt says "link": its answer depends on a file it
// doesn't declare.
func fakeSneaky(describe func(plugin.Description) plugin.Handler, sneaky bool) map[string]plugin.Handler {
	name := "tidy"
	if sneaky {
		name = "sneaky"
	}

	return map[string]plugin.Handler{
		plugin.CallDescribe: describe(plugin.Description{
			Kind: plugin.KindCorrelator, Name: name,
			Capabilities: []string{plugin.CallLinks},
			Inputs:       map[string]string{plugin.CallLinks: plugin.PerSet},
		}),
		plugin.CallLinks: func(in []byte) (any, error) {
			var req plugin.TreeRequest
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, fmt.Errorf("request: %w", err)
			}

			flag, err := os.ReadFile(filepath.Join(req.Tree, "texts/flag.txt"))
			if err != nil {
				return nil, fmt.Errorf("links: %w", err)
			}

			var a plugin.LinksAnswer

			for _, c := range req.Claimed {
				if len(req.Paths) > 0 && !slices.Contains(req.Paths, c.Path) {
					continue
				}

				set := plugin.Set{Group: c.Path, Inputs: []string{c.Path}}
				if sneaky && c.Path == "texts/a.txt" && strings.Contains(string(flag), "link") {
					set.Links = []plugin.Link{{From: "t:a", To: "t:c", Kind: plugin.LinkUses}}
				}

				a.Sets = append(a.Sets, set)
			}

			return a, nil
		},
	}
}

type nightly struct {
	r       *testrepo.Repo
	cfg     *config.Config
	remote  string
	plugins string
}

func newNightly(t *testing.T, correlator, check string) *nightly {
	t.Helper()

	t.Setenv(envJobURL, "https://ci/jobs/plan/1")

	r := testrepo.New(t)
	for _, f := range []string{"a", "b", "c"} {
		r.Write("texts/"+f+".txt", f+"\n")
	}

	r.Write("texts/flag.txt", "quiet\n")
	r.Commit("texts")

	remote := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	rel := &config.Release{Version: "v0", SHA256: strings.Repeat("a", 64)}

	return &nightly{
		r: r, remote: remote, plugins: pluginsDirFor(t),
		cfg: &config.Config{Narrowing: 3, RebuildCheck: check, Plugins: []config.Plugin{
			{Name: "texts", Kind: config.KindSBOM, Repository: "o/texts", Release: rel, Include: []string{"texts/*.txt"}},
			{Name: correlator, Kind: config.KindCorrelator, Repository: "o/" + correlator, Release: rel, Include: []string{"texts/*.txt"}},
		}},
	}
}

func (n *nightly) store(t *testing.T) *gitstore.Store {
	t.Helper()

	s := &gitstore.Store{Remote: n.remote, Dir: filepath.Join(t.TempDir(), "scratch")}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})

	return s
}

func (n *nightly) load(t *testing.T, cache string) *plugins.Set {
	t.Helper()

	set, err := plugins.Load(t.Context(), plugins.Options{Config: n.cfg, Dir: n.plugins, Cache: cache, Answers: answersCache(cache, n.store(t)), Work: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	return set
}

func (n *nightly) at(t *testing.T, commit string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "tree")
	if err := (gitx.Repo{Dir: n.r.Dir}).Checkout(t.Context(), dir, commit); err != nil {
		t.Fatal(err)
	}

	return dir
}

func (n *nightly) warm(t *testing.T, cache, commit string) (*deps.Tree, []string) {
	t.Helper()

	set := n.load(t, cache)

	tree, err := set.Tree(t.Context(), n.at(t, commit), commit, 3)
	if err != nil {
		t.Fatal(err)
	}

	if err := conflictWarnings(set, set.Flush(t.Context())); err != nil {
		t.Fatal(err)
	}

	return tree, set.Report().Warnings
}

func (n *nightly) refs(t *testing.T) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "git", "-C", n.remote, "for-each-ref", "--format=%(refname) %(objectname)").CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	return string(out)
}

func runWith(t *testing.T, rb *rebuild) report.Run {
	t.Helper()

	data, err := json.Marshal(rb.Found)
	if err != nil {
		t.Fatal(err)
	}

	var got report.Rebuild
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	return report.Run{
		Name: "nightly", Commit: strings.Repeat("c", 40), Rebuild: &got,
		Suites: []report.Suite{{Name: "e2e", Slices: []report.Slice{{Label: "a.spec.ts", State: report.Passed}}}},
	}
}

func hasLink(t *deps.Tree, from, to string) bool {
	return slices.ContainsFunc(t.Links, func(l plugin.Link) bool { return l.From == from && l.To == to })
}

func TestTheNightlyRebuildFindsAnAnswerThatReadsAnUndeclaredFile(t *testing.T) {
	for _, check := range []string{config.RebuildError, config.RebuildWarning} {
		t.Run(check, func(t *testing.T) {
			n := newNightly(t, "sneaky", check)

			first := n.r.Commit("nothing yet")
			if tree, _ := n.warm(t, t.TempDir(), first); hasLink(tree, "t:a", "t:c") {
				t.Fatal("a links c before the flag says so")
			}

			n.r.Write("texts/flag.txt", "link\n")
			head := n.r.Commit("the flag says link")

			before := t.TempDir()
			if tree, _ := n.warm(t, before, head); hasLink(tree, "t:a", "t:c") {
				t.Fatal("warm, the cache was expected to serve a's old set")
			}

			rb := rebuildGraph(t.Context(), n.cfg, n.store(t), n.r.Dir, head, t.TempDir(), n.plugins)
			if rb.Found.Error != "" {
				t.Fatal(rb.Found.Error)
			}

			d := rb.Found.Differences
			if len(d) != 1 || d[0].Plugin != "sneaky" || d[0].Call != plugin.CallLinks || !slices.Equal(d[0].Inputs, []string{"texts/a.txt"}) ||
				len(d[0].What) != 1 || !strings.Contains(d[0].What[0], `"from":"t:a","to":"t:c"`) {
				t.Fatalf("differences %+v", d)
			}

			run := runWith(t, rb)
			state, desc := run.RunStatus()

			switch check {
			case config.RebuildError:
				if state != "failure" || !strings.Contains(desc, "(sneaky)") {
					t.Errorf("error: %s: %s", state, desc)
				}
			case config.RebuildWarning:
				if state != "success" || !strings.Contains(desc, "1 warning") {
					t.Errorf("warning: %s: %s", state, desc)
				}
			}

			var log strings.Builder
			rb.say(&log)

			if !strings.Contains(log.String(), check+": the cold rebuild differs from the cache: sneaky links, inputs [texts/a.txt]") {
				t.Errorf("plan log:\n%s", log.String())
			}

			log.Reset()

			if err := rb.write(t.Context(), &log); err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(log.String(), "replaces sneaky's stored answer") {
				t.Errorf("replace log:\n%s", log.String())
			}

			if tree, ws := n.warm(t, t.TempDir(), head); !hasLink(tree, "t:a", "t:c") || len(ws) > 0 {
				t.Errorf("after the rebuild, warm: link %t, warnings %v", hasLink(tree, "t:a", "t:c"), ws)
			}

			t.Run("old local level", func(t *testing.T) {
				if tree, _ := n.warm(t, before, head); !hasLink(tree, "t:a", "t:c") {
					t.Error("the old local level still serves the stale set")
				}
			})
		})
	}
}

func TestTheNightlyRebuildOfAWellBehavedPlugin(t *testing.T) {
	n := newNightly(t, "tidy", config.RebuildError)

	first := n.r.Commit("nothing yet")
	n.warm(t, t.TempDir(), first)

	stored := n.refs(t)

	rb := rebuildGraph(t.Context(), n.cfg, n.store(t), n.r.Dir, first, t.TempDir(), n.plugins)
	if rb.Found.Error != "" || len(rb.Found.Differences) > 0 {
		t.Fatalf("unchanged tree: %+v", rb.Found)
	}

	var log strings.Builder
	if err := rb.write(t.Context(), &log); err != nil || log.Len() > 0 {
		t.Fatalf("write: %v\n%s", err, log.String())
	}

	if got := n.refs(t); got != stored {
		t.Errorf("a rebuild that found nothing wrote:\nbefore\n%s\nafter\n%s", stored, got)
	}

	n.r.Write("texts/a.txt", "a, changed\n")
	head := n.r.Commit("a changed")

	if _, ws := n.warm(t, t.TempDir(), head); len(ws) > 0 {
		t.Errorf("an ordinary plan of a well-behaved plugin warned: %v", ws)
	}

	rb = rebuildGraph(t.Context(), n.cfg, n.store(t), n.r.Dir, head, t.TempDir(), n.plugins)
	if rb.Found.Error != "" || len(rb.Found.Differences) > 0 {
		t.Fatalf("after a change: %+v", rb.Found)
	}

	if state, desc := runWith(t, rb).RunStatus(); state != "success" || desc != "1 suite passed" {
		t.Errorf("%s: %s", state, desc)
	}

	log.Reset()

	if err := rb.write(t.Context(), &log); err != nil || strings.Contains(log.String(), "replaces") {
		t.Errorf("write: %v\n%s", err, log.String())
	}
}

func TestAnOrdinaryPlanThatMeetsAConflictWarnsAndKeepsTheStoredAnswer(t *testing.T) {
	n := newNightly(t, "sneaky", config.RebuildError)

	quiet := n.r.Commit("nothing yet")
	n.r.Write("texts/flag.txt", "link\n")
	linked := n.r.Commit("the flag says link")

	// the quiet plan asks before the linked one stores, as two plans at
	// once do.
	slow := n.load(t, t.TempDir())

	tree, err := slow.Tree(t.Context(), n.at(t, quiet), quiet, 3)
	if err != nil {
		t.Fatal(err)
	}

	if hasLink(tree, "t:a", "t:c") {
		t.Fatal("the quiet tree links")
	}

	if _, ws := n.warm(t, t.TempDir(), linked); len(ws) > 0 {
		t.Fatalf("the first to store warned: %v", ws)
	}

	err = slow.Flush(t.Context())

	var ce *plugincache.ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("flush: %v", err)
	}

	if err := conflictWarnings(slow, err); err != nil {
		t.Fatal(err)
	}

	ws := slow.Report().Warnings
	if len(ws) != len(ce.Conflicts) {
		t.Fatalf("%d warnings for %d conflicts: %v", len(ws), len(ce.Conflicts), ws)
	}

	for _, c := range ce.Conflicts {
		if c.Build.Plugin != "sneaky" {
			t.Errorf("a conflict of %s: stored %s, got %s", c.Build.Plugin, c.Stored, c.Got)
		}

		if !slices.ContainsFunc(ws, func(w string) bool {
			return strings.HasPrefix(w, "sneaky gave an answer that differs from the stored one for the same key "+c.Hash)
		}) {
			t.Errorf("no warning names %s: %v", c.Hash, ws)
		}
	}

	if tree, _ := n.warm(t, t.TempDir(), quiet); !hasLink(tree, "t:a", "t:c") {
		t.Error("the stored answer was overwritten")
	}
}
