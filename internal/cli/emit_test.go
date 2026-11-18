package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/piperun"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/plugin"
)

type emitCase struct {
	cfg        *config.Config
	ch         runs.Change
	trigger    string
	em         *emitter
	api        *githubAPI
	head, base string
}

// emitChange is a proposed change of a suite with a prepare and a whole-suite check, planned by its
// first step into a directory of the test's, with an emitter for its second.
func emitChange(t *testing.T) emitCase {
	t.Helper()

	root, s, trigger, ch, api := proposedChange(t)
	cfg := loadConfig(t, root)

	e2e := cfg.Suites["e2e"]
	e2e.Prepare = "ci/tasks/e2e-prepare.yml"
	e2e.WholeSuite = []config.WholeSuiteCheck{{Name: "money", Task: "ci/tasks/money.yml"}}
	cfg.Suites["e2e"] = e2e

	abs, err := filepath.Abs(trigger)
	if err != nil {
		t.Fatal(err)
	}

	head, base, _, err := compareWith(t.Context(), cfg, ch, s, abs)
	if err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	em := &emitter{dir: filepath.Join(out, "emit"), pools: filepath.Join(out, "pools"), cfg: cfg, ch: ch, store: s, root: trigger}

	return emitCase{cfg: cfg, ch: ch, trigger: trigger, em: em, api: api, head: head, base: base}
}

func (c emitCase) firstStep(t *testing.T, names ...string) string {
	t.Helper()

	var sel []string
	for _, n := range names {
		sel = append(sel, fmt.Sprintf(`{"name": %q, "label": "//e2e:%s", "sources": ["e2e/%s.spec.ts"]}`, n, n, n))
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, planOutFile), fmt.Sprintf(`{"base": %q, "head": %q, "all": false, "reason": "", "suites": [{"suite": "e2e", "all": false, "reason": "", "selected": [%s], "untouched": [], "whole_suite": ["money"]}]}`,
		c.base, c.head, strings.Join(sel, ", ")))

	return dir
}

func readEmission(t *testing.T, dir string) plugin.Emission {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, plugin.EmitFile))
	if err != nil {
		t.Fatal(err)
	}

	var em plugin.Emission
	if err := json.Unmarshal(data, &em); err != nil {
		t.Fatal(err)
	}

	return em
}

func TestTheSecondStepEmitsThePlanItWrote(t *testing.T) {
	c := emitChange(t)
	s := c.em.store

	if err := planFromEmit(t.Context(), c.cfg, c.ch, s, c.trigger, c.firstStep(t, "a", "b"), c.em, io.Discard); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := readPlan(t.Context(), s, reportID(c.ch)); err != nil || !ok {
		t.Fatalf("the plan wasn't written: %v %v", ok, err)
	}

	em := readEmission(t, c.em.dir)

	var jobs []string
	for _, j := range em.Jobs {
		jobs = append(jobs, j.Name)
	}

	if !slices.Equal(jobs, []string{"e2e-prepare", "e2e-whole-money", "e2e-end"}) || len(em.Pools) != 1 || !slices.Equal(em.Pools[0].Items, []string{"a", "b"}) {
		t.Fatalf("emission: %+v", em)
	}

	pool, err := readPool(c.em.pools, "e2e")
	if err != nil || pool["a"].Label != "//e2e:a" || pool["b"].Sources[0] != "e2e/b.spec.ts" {
		t.Fatalf("the pool's slices: %+v %v", pool, err)
	}

	if ch, err := runs.ReadRunFile(filepath.Join(c.em.pools, runs.RunFile)); err != nil || ch.ID != "pr-7" || ch.Commit != c.ch.Commit {
		t.Fatalf("the run file: %+v %v", ch, err)
	}

	data, err := os.ReadFile(filepath.Join(c.em.dir, planRecordFile))
	if err != nil || !strings.Contains(string(data), `"head": "`+c.head+`"`) {
		t.Fatalf("the plan's record: %s %v", data, err)
	}
}

func TestARerunEmitsTheWrittenPlanWithoutWhatPassed(t *testing.T) {
	c := emitChange(t)
	s := c.em.store

	if err := planFromEmit(t.Context(), c.cfg, c.ch, s, c.trigger, c.firstStep(t, "a", "b"), c.em, io.Discard); err != nil {
		t.Fatal(err)
	}

	result := filepath.Join(t.TempDir(), "a.json")
	writeFile(t, result, `{"passed": true, "tests": 1, "failures": []}`)

	r := &report.Reporter{Config: c.cfg, ID: reportID(c.ch), Store: s, GitHub: report.Nowhere{}}
	if _, err := r.Slices(t.Context(), []report.SliceEvent{
		{Suite: "e2e", Slice: "a", Label: "//e2e:a", Result: result},
		{Suite: "e2e", Slice: report.WholeName("money"), Label: report.WholeName("money"), Result: result},
	}); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(c.em.dir); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder

	// the first step of a rerun finds the plan, and leaves nothing for the second
	if err := planFromEmit(t.Context(), c.cfg, c.ch, s, c.trigger, t.TempDir(), c.em, &out); err != nil {
		t.Fatal(err)
	}

	// the check passed too: the prepare and the end are left
	if em := readEmission(t, c.em.dir); len(em.Pools) != 1 || !slices.Equal(em.Pools[0].Items, []string{"b"}) || len(em.Jobs) != 2 || em.Jobs[1].Name != "e2e-end" {
		t.Fatalf("emitted again: %+v", em)
	}

	if !strings.Contains(out.String(), "planned already; emitting what hasn't passed") {
		t.Fatalf("%s", out.String())
	}
}

func TestAnEmissionOverALimitFailsThePlanBeforeItIsWritten(t *testing.T) {
	c := emitChange(t)

	names := make([]string, plugin.EmitMaxPoolItems+1)
	for i := range names {
		names[i] = fmt.Sprintf("s%d", i)
	}

	err := planFromEmit(t.Context(), c.cfg, c.ch, c.em.store, c.trigger, c.firstStep(t, names...), c.em, io.Discard)
	if !errors.Is(err, piperun.ErrLimit) || !strings.Contains(err.Error(), "pool e2e-slices holds 2001 slices") {
		t.Fatalf("%v", err)
	}

	if _, ok, err := readPlan(t.Context(), c.em.store, reportID(c.ch)); err != nil || ok {
		t.Fatalf("the plan was written: %v %v", ok, err)
	}

	if _, err := os.Stat(filepath.Join(c.em.dir, plugin.EmitFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an emission was written: %v", err)
	}

	if got := c.api.status("pipelinegen/pr"); !strings.HasPrefix(got, "error ") || !strings.Contains(got, "piperun") {
		t.Fatalf("run status: %q", got)
	}
}

func TestEmitGoesWithFromOnly(t *testing.T) {
	for _, args := range [][]string{
		{"plan", "--emit", "x"},
		{"plan", "--from", "p", "--emit", "x", "--gate", "g"},
	} {
		if _, stderr, code := run(t, args...); code != 2 || !strings.Contains(stderr, "--emit goes with --from, and without --gate") {
			t.Errorf("%q: %d %s", args, code, stderr)
		}
	}
}
