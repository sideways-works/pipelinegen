package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

// TestReplayWalkedAgainstSelectors runs on the clone PIPELINEGEN_TEST_REPLAY_REPO names, never changing
// it; _PAIRS bounds the commits (15), _REF names the ref (main), _TABLE a file for the table.
func TestReplayWalkedAgainstSelectors(t *testing.T) {
	src := os.Getenv("PIPELINEGEN_TEST_REPLAY_REPO")
	if src == "" {
		t.Skip("PIPELINEGEN_TEST_REPLAY_REPO names no repository to replay")
	}

	pairs := 15
	if n, err := strconv.Atoi(os.Getenv("PIPELINEGEN_TEST_REPLAY_PAIRS")); err == nil && n > 0 {
		pairs = n
	}

	before := forward.Load()

	t.Cleanup(func() { forward.Store(before) })

	r := &testrepo.Repo{Dir: filepath.Join(t.TempDir(), "repo")}
	(&testrepo.Repo{Dir: t.TempDir()}).GitIn(t, "clone", "-q", "--no-local", src, r.Dir)

	cache := t.TempDir()
	bz := testrepo.Bazel(t, filepath.Join(cache, "tree"))
	differ := bazeldifftest.Tool(t).Path

	at := func(head string) *config.Config {
		t.Helper()

		r.GitIn(t, "checkout", "-q", "--detach", head)

		cfg, err := config.Load(filepath.Join(r.Dir, "pipelinegen.yaml"))
		if err != nil {
			t.Fatal(err)
		}

		cfg.Bazel.StartupFlags = append(slices.Clone(bz.StartupFlags), cfg.Bazel.StartupFlags...)

		return cfg
	}

	var table strings.Builder

	fmt.Fprintln(&table, "commit\tsuite\tselectors\twalked\tsame")

	ref := os.Getenv("PIPELINEGEN_TEST_REPLAY_REF")
	if ref == "" {
		ref = "main"
	}

	r.GitIn(t, "fetch", "-q", src, ref)
	commits := strings.Fields(r.GitIn(t, "rev-list", "--first-parent", "-n", strconv.Itoa(pairs*4), "FETCH_HEAD"))

	compare := func(base, head string, cfg *config.Config) {
		t.Helper()

		sides := [2]map[string]string{}

		// Slices only the walk selects because their own file changed: no selector can name a file no Bazel
		// package holds.
		ownOnly := map[string][]string{}

		for i, walking := range []bool{false, true} {
			forward.Store(!walking)

			p, err := affectedPlan(t.Context(), cfg, r.Dir, base, head, false, cache, differ, listsNone, filepath.Join(r.Dir, "tools", "plugins"))
			if err != nil {
				t.Fatalf("%s walking %v: %v", head[:12], walking, err)
			}

			sides[i] = map[string]string{}

			for _, sp := range p.Suites {
				if walking {
					sp, ownOnly[sp.Suite] = withoutOwnChanges(sp, sides[0][sp.Suite])
				}

				sides[i][sp.Suite] = selectedNames(sp)
				if len(sp.Removed) > 0 {
					sides[i][sp.Suite] += " removed " + strings.Join(sp.Removed, ",")
				}
			}

			if p.All {
				sides[i]["(all)"] = p.Reason
			}
		}

		for _, suite := range append(cfg.SuiteNames(), "(all)") {
			b, w := sides[0][suite], sides[1][suite]

			same := strconv.FormatBool(b == w)
			if len(ownOnly[suite]) > 0 {
				same += ", and the walk runs " + strings.Join(ownOnly[suite], ",") + " for its own change"
			}

			fmt.Fprintf(&table, "%s\t%s\t%s\t%s\t%s\n", head[:12], suite, count(b), count(w), same)

			if b != w {
				t.Errorf("%s %s: selectors select %q, the walk %q", head[:12], suite, b, w)
			}
		}
	}

	done := 0

	for _, head := range commits {
		if done == pairs {
			break
		}

		base := r.GitIn(t, "rev-parse", head+"^")

		cfg := at(head)
		if everythingOn(cfg, strings.Fields(r.GitIn(t, "diff", "--name-only", base, head))) {
			continue
		}

		done++

		compare(base, head, cfg)
	}

	edits := []struct{ file, text string }{
		{"example/calc/lib.sh", "\n# changed\n"},
		{"example/calc/add_test.sh", "\n# changed\n"},
		{"example/words/words.go", "\n// changed\n"},
		{"internal/affected/walked.go", "\n// changed\n"},
		{"internal/graph/zz_replay_test.go", "package graph\n\nimport \"testing\"\n\nfunc TestReplayed(t *testing.T) {}\n"},
	}

	head := commits[0]

	for _, e := range edits {
		r.GitIn(t, "checkout", "-q", "--detach", head)

		path := filepath.Join(r.Dir, filepath.FromSlash(e.file))

		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, append(data, e.text...), 0o600); err != nil { //nolint:gosec // a file of the replay's own clone
			t.Fatal(err)
		}

		r.GitIn(t, "add", e.file)
		r.GitIn(t, "-c", "user.name=replay", "-c", "user.email=replay@example.com", "commit", "-q", "-m", "replay: "+e.file)

		next := r.GitIn(t, "rev-parse", "HEAD")
		compare(head, next, at(next))
		head = next
	}

	t.Log("\n" + table.String())

	if out := os.Getenv("PIPELINEGEN_TEST_REPLAY_TABLE"); out != "" {
		if err := os.WriteFile(out, []byte(table.String()), 0o600); err != nil { //nolint:gosec // the file the person running the replay named
			t.Fatal(err)
		}
	}
}

// withoutOwnChanges drops the slices only the walk selects because their own test file changed or is new.
func withoutOwnChanges(sp affected.SuitePlan, bound string) (affected.SuitePlan, []string) {
	in := map[string]bool{}
	for n := range strings.SplitSeq(strings.SplitN(bound, " removed ", 2)[0], ",") {
		in[n] = true
	}

	var own []string

	kept := sp.Selected[:0:0]

	for _, sl := range sp.Selected {
		rs := sp.Reasons[sl.Label]
		if !in[sl.Name] && len(rs) > 0 && !slices.ContainsFunc(rs, func(r affected.Reason) bool {
			return r.Kind != affected.KindTestChanged && r.Kind != affected.KindNewSlice
		}) {
			own = append(own, sl.Name)

			continue
		}

		kept = append(kept, sl)
	}

	sp.Selected = kept

	return sp, own
}

func everythingOn(cfg *config.Config, files []string) bool {
	for _, f := range files {
		if _, ok := cfg.MatchesEverythingOn(f); ok {
			return true
		}
	}

	return false
}

func count(names string) string {
	if names == "" {
		return "0"
	}

	n := strings.Count(strings.SplitN(names, " removed ", 2)[0], ",") + 1
	if n <= 3 || strings.Contains(names, " removed ") {
		return fmt.Sprintf("%d (%s)", n, names)
	}

	return strconv.Itoa(n)
}
