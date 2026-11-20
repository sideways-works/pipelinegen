package cli

import (
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

// prRunConfig is a pull request's run; its store is a remote the test names.
const prRunConfig = `
pipeline: shop
suites:
  e2e:
    tag: slice-e2e
    run_slice: ci/tasks/e2e-slice.yml
git: {remote: "file:///REMOTE"}
concourse: {url: "https://ci", repository: {uri: x}, planner: {image: {repository: p}, digest: "sha256:x", bootstrap: {release: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}, bazelisk: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}}}}
runs:
  pr:
    trigger: {pull_requests: {base: main}}
    compare: merged_target
    select: affected
    suites: {e2e: {}}
`

// gitRepo is a repository whose commits hold the given files, one commit per map: their ids, oldest
// first.
func gitRepo(t *testing.T, commits ...map[string]string) (string, []string) {
	t.Helper()

	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()

		out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.test"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}

		return strings.TrimSpace(string(out))
	}

	git("init", "-q", "-b", "main")

	var ids []string

	for _, files := range commits {
		for path, text := range files {
			writeFile(t, filepath.Join(repo, path), text)
		}

		git("add", "-A")
		git("commit", "-q", "--allow-empty", "-m", "c")
		ids = append(ids, git("rev-parse", "HEAD"))
	}

	return repo, ids
}

func withLevel(text string, level int) string {
	if level == 0 {
		return text
	}

	return text + "narrowing: " + strconv.Itoa(level) + "\n"
}

func TestCarryingOverTakesTheStricterOfTheTwoConfigs(t *testing.T) {
	for _, c := range []struct {
		name       string
		base, head int
		on         bool
	}{
		{"both at 3", 3, 3, true},
		{"the change lowers the level to 3", 4, 3, false},
		{"the change raises it to 4", 3, 4, false},
		{"both at 4", 4, 4, false},
		{"both at 5", 5, 5, false},
		{"the default", 0, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo, ids := gitRepo(t,
				map[string]string{config.FileName: withLevel(prRunConfig, c.base)},
				map[string]string{config.FileName: withLevel(prRunConfig, c.head), "a.txt": "a\n"})

			cfg := loadConfigText(t, withLevel(prRunConfig, c.head))

			if offAt, on := passedSinceOn(t.Context(), cfg, repo, ids[0], ids[1]); on != c.on || offAt != 4 {
				t.Fatalf("on %v, off from %d", on, offAt)
			}
		})
	}
}

func TestFromItsLevelNothingIsCarriedOverAndNoRecordRead(t *testing.T) {
	text := strings.Replace(prRunConfig, "/REMOTE", filepath.Join(t.TempDir(), "none.git"), 1)

	for _, level := range []int{3, 4, 5} {
		repo, ids := gitRepo(t,
			map[string]string{config.FileName: withLevel(text, level), "a.txt": "a\n"},
			map[string]string{"a.txt": "b\n"})

		cfg := loadConfigText(t, withLevel(text, level))
		store := openStore(cfg)

		t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // nothing written

		ch := runs.Change{Run: "pr", ID: "pr-7", Commit: ids[1], PR: 7, Base: "main"}

		rebase, notes := passedSinceBase(t.Context(), cfg, ch, store, repo, ids[0], ids[1], t.TempDir(), "")

		// below the level the store is read, and the unreachable one says so in a note
		if read := len(notes) == 1 && strings.Contains(notes[0], "the last build of pr-7"); rebase != nil || read != (level < 4) {
			t.Fatalf("at level %d: %v %q", level, rebase != nil, notes)
		}
	}
}

func TestTheTestedTreeIsMadeAgainOrNotAtAll(t *testing.T) {
	repo, ids := gitRepo(t, map[string]string{"a.txt": "a\n"}, map[string]string{"b.txt": "b\n"})
	gitT(t, repo, "branch", "topic", ids[1])
	gitT(t, repo, "reset", "-q", "--hard", ids[0])
	gitT(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "the target moves")

	tip := revParse(t, repo, "HEAD")
	cfg := loadConfigText(t, prRunConfig)

	tested, err := runs.Merge(t.Context(), repo, ids[1], tip)
	if err != nil {
		t.Fatal(err)
	}

	// a newer build's tree under test, which making the old one again leaves alone
	current, err := runs.Merge(t.Context(), repo, ids[1], ids[0])
	if err != nil {
		t.Fatal(err)
	}

	b := report.Build{Commit: ids[1][:12], Merged: ids[1], Base: tip, Head: tested}

	if got, err := testedTree(t.Context(), cfg, repo, b); err != nil || got != tested {
		t.Fatalf("made again: %s %v", got, err)
	}

	if got := revParse(t, repo, "refs/pipelinegen/tree"); got != current {
		t.Fatalf("the tree under test moved to %s", got)
	}

	for name, broken := range map[string]report.Build{
		"a plan without the change's commit": {Commit: b.Commit, Base: b.Base, Head: b.Head},
		"another commit than the build's":    {Commit: ids[0][:12], Merged: ids[1], Base: b.Base, Head: b.Head},
		"a merge that isn't the one tested":  {Commit: b.Commit, Merged: b.Merged, Base: ids[0], Head: b.Head},
		"a commit no remote has":             {Commit: "dead00000000", Merged: "dead" + strings.Repeat("0", 36), Base: b.Base, Head: b.Head},
	} {
		if got, err := testedTree(t.Context(), cfg, repo, broken); !errors.Is(err, errNotTested) || got != "" {
			t.Errorf("%s: %s %v", name, got, err)
		}
	}
}

func TestTheSecondStepRecordsTheChangesOwnCommit(t *testing.T) {
	root, s, trigger, ch, _ := proposedChange(t)
	cfg := loadConfig(t, root)

	abs, err := filepath.Abs(trigger)
	if err != nil {
		t.Fatal(err)
	}

	head, base, _, err := compareWith(t.Context(), cfg, ch, s, abs)
	if err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(map[string]any{"base": base, "head": head, "merged": strings.Repeat("f", 40), "suites": []any{}})
	if err != nil {
		t.Fatal(err)
	}

	plan, err := ownPlan(t.Context(), cfg, ch, s, trigger, data)
	if err != nil || plan.Merged != ch.Commit {
		t.Fatalf("the plan records %q, want the trigger's %s: %v", plan.Merged, ch.Commit, err)
	}
}

func TestAConfigChangedSinceTheLastBuildCarriesNothingOver(t *testing.T) {
	text := withLevel(prRunConfig, 3)

	// a plugin's pin, its config block or any other key: the config is the one file that holds them
	repo, ids := gitRepo(t,
		map[string]string{config.FileName: text},
		map[string]string{config.FileName: text + "# a pin moved\n"})

	reaches, err := reachFrom(t.Context(), loadConfigText(t, text), nil, repo, "pr", ids[0], ids[1], t.TempDir(), "", true)
	if reaches != nil || !errors.Is(err, errReachesAll) {
		t.Fatalf("a changed config: %v %v", reaches, err)
	}
}

// TestBothPathsHonourPassedSincesSwitch plans a branch's run and a pull request's against a store no
// read reaches: a note says the records were read, so the narrowing was on.
func TestBothPathsHonourPassedSincesSwitch(t *testing.T) {
	branchRun := strings.Replace(noHostConfig, "REMOTE", "file:///REMOTE", 1)
	override := "narrowings:\n  passed-since: {off_at: 3, note: the runs' records are not to be trusted here}\n"

	for _, path := range []struct {
		name, config, read string
		change             func(head string) runs.Change
	}{
		{"a branch's run", branchRun, "the runs' records", func(head string) runs.Change {
			return runs.Change{Run: "main", ID: "main", Commit: head, Branch: "main"}
		}},
		{"a pull request's run", prRunConfig, "the last build of pr-7", func(head string) runs.Change {
			return runs.Change{Run: "pr", ID: "pr-7", Commit: head, PR: 7, Base: "main"}
		}},
	} {
		for _, c := range []struct {
			name, extra string
			on          bool
		}{
			{"on at the default level", "", true},
			{"on below the config's own level", "narrowing: 2\n" + strings.Replace(override, "off_at: 3", "off_at: 4", 1), true},
			{"off from level 4", "narrowing: 4\n", false},
			{"off by narrowings.passed-since", override, false},
		} {
			t.Run(path.name+", "+c.name, func(t *testing.T) {
				text := strings.Replace(path.config, "/REMOTE", filepath.Join(t.TempDir(), "none.git"), 1) + c.extra
				// a commit between the base and the head, at which a branch's slice may have passed
				repo, ids := gitRepo(t,
					map[string]string{config.FileName: text, "a.txt": "a\n"},
					map[string]string{"a.txt": "b\n"},
					map[string]string{"a.txt": "c\n"})

				cfg := loadConfigText(t, text)
				store := openStore(cfg)

				t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // nothing written

				rebase, notes := passedSinceBase(t.Context(), cfg, path.change(ids[2]), store, repo, ids[0], ids[2], t.TempDir(), "")

				read := len(notes) == 1 && strings.Contains(notes[0], path.read)
				if rebase != nil || read != c.on {
					t.Fatalf("on %v: %v %q", c.on, rebase != nil, notes)
				}
			})
		}
	}
}
