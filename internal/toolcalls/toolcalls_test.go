package toolcalls

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
)

func fixture() Commands {
	common := func(root string, more map[string]Flag) map[string]Flag {
		out := map[string]Flag{"root": {Default: root, Use: Reads}, "config": {Use: Reads | File}, "help": {Bool: true}}
		maps.Copy(out, more)

		return out
	}

	return Commands{
		"plan": {Flags: common(".", map[string]Flag{
			"run": {}, "trigger": {Default: "trigger", Use: Reads}, "cache": {Use: Reads | Writes},
		}), Needs: Needs{Reads: []string{"tools/plugins"}, ConfigFrom: "trigger", Bazel: true, Token: TokenRequired}},
		"report batch-end": {Flags: common(".", map[string]Flag{
			"batch": {}, "batches": {Default: "batches", Use: Reads}, "result": {Default: "result", Use: Reads},
		}), Needs: Needs{Reads: []string{"source"}, Recording: []string{"coverage"}, RunFromEnv: true, Token: TokenRequired}},
		"slice start": {Named: true, Flags: common(".", map[string]Flag{"batches": {}}), Needs: Needs{RunFromEnv: true, Token: TokenRequired}},
		"pipeline":    {Flags: common(".", map[string]Flag{"out": {Use: Writes | File}}), Needs: Needs{ConcourseURL: true}},
	}
}

func TestACallParsesAsTheToolReadsIt(t *testing.T) {
	cs := fixture()

	for _, bad := range [][]string{
		{"slice", "start", "--root", "source"},
		{"slice", "start", "a", "--minutes", "90"},
		{"report", "batch-end", "--root"},
		{"report", "nothing"},
		{"plan", "stray"},
		{"unknown"},
	} {
		if _, err := cs.Parse(bad); !errors.Is(err, ErrCall) {
			t.Errorf("%v parsed: %v", bad, err)
		}
	}

	c, err := cs.Parse([]string{"report", "batch-end", "--batch", "((.:batch))", "--result=result", "-root", "source", "-help", "2>err.txt", "--ignored"})
	if err != nil {
		t.Fatal(err)
	}

	if c.Command != "report batch-end" || c.Flags["root"] != "source" || c.Flags["result"] != "result" || c.Flags["help"] != "true" || len(c.Flags) != 4 {
		t.Fatalf("%+v", c)
	}

	if c, err := cs.Parse([]string{"slice", "start", "db", "--batches", "b"}); err != nil || c.Name != "db" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestACallReadsWhatItsFlagsDefaultsAndEnvironmentName(t *testing.T) {
	cs := fixture()

	mounts := func(args string, env map[string]string, recording bool) (string, string, string) {
		t.Helper()

		c, err := cs.Parse(strings.Fields(args))
		if err != nil {
			t.Fatal(err)
		}

		r, w, p := cs.Mounts(c, env, recording)

		return strings.Join(r, ","), strings.Join(w, ","), strings.Join(p, "; ")
	}

	step := map[string]string{EnvToken: "", EnvTrigger: "trigger", EnvBazelisk: "tools/bazelisk"}

	if r, w, p := mounts("report batch-end --result result --root source", step, true); r != "batches,coverage,result,source,trigger" || w != "" || p != "" {
		t.Errorf("batch-end recording: reads %s writes %s problems %s", r, w, p)
	}

	if r, _, _ := mounts("report batch-end --root source", map[string]string{EnvToken: "", EnvRunFile: "batches/run.json"}, false); r != "batches,result,source" {
		t.Errorf("batch-end from the run file reads %s", r)
	}

	// the config comes from the trigger, whatever --root says
	if r, w, p := mounts("plan --run main --cache plan-cache", step, false); r != "plan-cache,tools,trigger" || w != "plan-cache" || p != "" {
		t.Errorf("plan: reads %s writes %s problems %s", r, w, p)
	}

	if r, w, p := mounts("pipeline --root repo --out pipeline.yml", map[string]string{EnvConcourseURL: "x"}, false); r != "repo" || w != "" || p != "" {
		t.Errorf("pipeline: reads %s writes %s problems %s", r, w, p)
	}

	if _, w, _ := mounts("pipeline --root repo --out out/pipeline.yml", nil, false); w != "out" {
		t.Errorf("a pipeline written into a directory: %s", w)
	}

	for args, want := range map[string][]string{
		"slice start db":                    {"reads the build's own directory", "no way to learn its run", "without GITHUB_TOKEN"},
		"pipeline --root $REPO":             {"$REPO, which the step doesn't set"},
		"report batch-end --root source":    {"no way to learn its run"},
		"plan --run main --trigger ${TRIG}": {"$TRIG"},
	} {
		_, _, p := mounts(args, map[string]string{EnvConcourseURL: "x"}, false)
		for _, w := range want {
			if !strings.Contains(p, w) {
				t.Errorf("%s: problems %q, want %q", args, p, w)
			}
		}

		if strings.HasPrefix(args, "slice") && !strings.Contains(p, EnvConcourseURL) {
			t.Errorf("%s: an address it doesn't need went unnoticed: %q", args, p)
		}
	}

	// a step given the store's own token, the token split by purpose,
	// writes as one given the one token
	if _, _, p := mounts("slice start db --root source", map[string]string{PurposeEnv("store"): "", EnvRunFile: "batches/run.json"}, false); strings.Contains(p, "without") {
		t.Errorf("the store's own token: %s", p)
	}

	if got := PurposeEnv("read"); got != "PIPELINEGEN_READ_TOKEN" {
		t.Errorf("the read token's variable: %s", got)
	}

	if got := dedupe([]string{"b", "a", "b"}); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("dedupe %v", got)
	}
}

func TestPlanningAChangesTreeReadsWithTheReadToken(t *testing.T) {
	cs := Commands{"plan": {
		Flags: map[string]Flag{"out": {Use: Writes}, "root": {Default: ".", Use: Reads}},
		Needs: Needs{Token: TokenRequired, ReadOnlyWith: "out"},
	}}
	read := map[string]string{PurposeEnv("read"): "x"}

	for args, want := range map[string]bool{"plan --root trigger --out plans": true, "plan --root trigger": false} {
		c, err := cs.Parse(strings.Fields(args))
		if err != nil {
			t.Fatal(err)
		}

		_, _, problems := cs.Mounts(c, read, false)
		if ok := len(problems) == 0; ok != want {
			t.Errorf("%s with the read token: %v", args, problems)
		}
	}

	c, err := cs.Parse([]string{"plan", "--root", "trigger", "--out", "plans"})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, problems := cs.Mounts(c, map[string]string{}, false); len(problems) == 0 {
		t.Error("plan --out without any token")
	}
}
