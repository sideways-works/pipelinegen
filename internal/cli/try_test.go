package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestTryPlansTheLastCommits(t *testing.T) {
	testrepo.Parallel(t)

	bd := bazeldifftest.Tool(t).Path
	r := testrepo.New(t)

	conf := strings.Replace(readText(t, filepath.Join(r.Dir, "pipelinegen.yaml")), "  - pipelinegen.yaml\n", "", 1) +
		"untested:\n  - {paths: [pipelinegen.yaml], reason: the plans here take one config}\n"

	r.Write("pipelinegen.yaml", "pipeline: [\n")
	broken := r.Commit("an old config")

	r.Write("pipelinegen.yaml", conf)
	r.Write("api/money/money.go", "package money // changed\n")
	money := r.Commit("money")

	r.Write("stray.txt", "no target owns me\n")
	stray := r.Commit("stray")

	cfgFile := filepath.Join(t.TempDir(), "pipelinegen.yaml")
	if err := os.WriteFile(cfgFile, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}

	cache := t.TempDir()
	testrepo.Bazel(t, filepath.Join(cache, "tree"))

	stdout, stderr, code := run(t, "affected", "-root", r.Dir, "-config", cfgFile, "-base", broken, "-head", money, "-cache", cache, "-bazel-diff", bd, "-no-lists")
	if code != 0 || !strings.HasPrefix(stdout, "every slice: the base's configuration can't be read") {
		t.Fatalf("without --same-config: exit %d\n%s\n%s", code, stdout, stderr)
	}

	stdout, stderr, code = run(t, "affected", "-root", r.Dir, "-config", cfgFile, "-base", broken, "-head", money, "-cache", cache, "-bazel-diff", bd, "-no-lists", "-same-config", "-json")
	if code != 0 || !strings.Contains(stdout, `"all": false`) || !strings.Contains(stdout, "planned under one config for both trees (--same-config)") {
		t.Fatalf("--same-config: exit %d\n%s\n%s", code, stdout, stderr)
	}

	stdout, stderr, code = run(t, "try", "-root", r.Dir, "-config", cfgFile, "-last", "2", "-cache", cache, "-bazel-diff", bd)
	if code != 0 {
		t.Fatalf("try: exit %d\n%s", code, stderr)
	}

	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 3 ||
		!strings.HasPrefix(lines[0], stray[:12]+"  backend 1/1  e2e 2/2  specs 2/2  everything: stray.txt changed and no Bazel target owns it") ||
		!strings.HasPrefix(lines[1], money[:12]+"  backend 1/1  e2e 1/2  specs 1/2  ") ||
		lines[2] != "total: 2 commits, backend median 100% worst 100%, e2e median 50% worst 100%, specs median 50% worst 100%, 1 ran everything: stray.txt changed and no Bazel target owns it (1)" {
		t.Fatalf("try:\n%s", stdout)
	}
}

func TestTryNamesTargetsTheTreesLackOnce(t *testing.T) {
	var tally tryTally

	lacking := &affected.Plan{Suites: []affected.SuitePlan{{
		Suite: "browser",
		Reasons: map[string][]affected.Reason{
			"//e2e:a": {{Kind: affected.KindInputUnknown, What: "//apps/web:e2e_shared"}},
			"//e2e:b": {{Kind: affected.KindInputUnknown, What: "//apps/web:e2e_shared"}},
			"//e2e:c": {{Kind: affected.KindInputUnknown, What: "@ext//x:y", Through: []string{"no pin"}}},
		},
	}}}

	tally.add("1111111111111111111111111111111111111111", lacking)
	tally.add("2222222222222222222222222222222222222222", lacking)
	tally.add("3333333333333333333333333333333333333333", &affected.Plan{})

	line := tally.lacking()
	if !strings.HasPrefix(line, "2 of 3 commits depend on targets their trees lack or bazel-diff doesn't hash: //apps/web:e2e_shared; ") ||
		strings.Contains(line, "@ext") || !strings.Contains(line, "replay the commits onto a branch that has the BUILD changes") {
		t.Fatalf("%q", line)
	}

	if (&tryTally{}).lacking() != "" {
		t.Fatal("a line with nothing lacking")
	}
}

func TestAffectedPlansUnderTheHeadsOwnConfig(t *testing.T) {
	r := testrepo.New(t)
	head := r.Git("rev-parse", "HEAD")
	committed := readFile(t, filepath.Join(r.Dir, "pipelinegen.yaml"))

	r.Write("pipelinegen.yaml", committed+"\nexplain_limit: 7\n")
	r.Commit("a limit")
	limited := r.Git("rev-parse", "HEAD")

	r.Write("pipelinegen.yaml", committed+"\nexplain_limit: 9\n")

	c := &common{root: r.Dir}

	for commit, want := range map[string]int{head: config.DefaultExplainLimit, limited: 7} {
		cfg, err := affectedConfig(t.Context(), c, r.Dir, commit, false)
		if err != nil || cfg.ExplainLimitOrDefault() != want {
			t.Fatalf("%s: %+v %v", commit, cfg, err)
		}
	}

	if cfg, err := affectedConfig(t.Context(), c, r.Dir, head, true); err != nil || cfg.ExplainLimitOrDefault() != 9 {
		t.Fatalf("--same-config: %v", err)
	}
}

func TestAffectedNamesARevisionByItsCommit(t *testing.T) {
	r := testrepo.New(t)
	first := r.Git("rev-parse", "HEAD")
	r.Write("docs/note.md", "note\n")
	r.Commit("a note")

	if got, err := resolveRev(t.Context(), r.Dir, "HEAD~1"); err != nil || got != first {
		t.Fatalf("HEAD~1: %q %v", got, err)
	}

	if _, stderr, code := run(t, "affected", "-root", r.Dir, "-base", "no-such-rev"); code != 2 || !strings.Contains(stderr, "no commit no-such-rev") {
		t.Fatalf("%d %s", code, stderr)
	}
}
