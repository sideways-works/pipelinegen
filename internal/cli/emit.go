package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/durations"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/piperun"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/plugin"
)

// planRecordFile, beside the emission, is the plan as piperun keeps it with the build, a run-level
// record of media type application/vnd.pipelinegen.plan+json.
const planRecordFile = "plan.json"

// defaultPools is where plan --emit writes the pools' slices and their sessions read them, unless a
// flag says otherwise; the flags default to nothing, so a step that never emits reads and writes none.
const defaultPools = "pools"

type emitFlags struct{ dir, pools string }

func (f *emitFlags) register(fs *flag.FlagSet) {
	pathVar(fs, &f.dir, "emit", "", "with --from, also write piperun's emission of the plan into this directory (emit.json), with the plan as the build's record (plan.json): per suite its prepare, the pool of its slices, its whole-suite checks and its end; a plan written before is emitted again, without what passed", writes)
	pathVar(fs, &f.pools, "pools", "", "with --emit, where each pool's slices go for its sessions (<suite>-slices.json), with the run's identity (run.json) (default pools)", writes)
}

func (f *emitFlags) emitter(cfg *config.Config, ch runs.Change, store *gitstore.Store, root string) *emitter {
	pools := f.pools
	if pools == "" {
		pools = defaultPools
	}

	return &emitter{dir: f.dir, pools: pools, cfg: cfg, ch: ch, store: store, root: root}
}

// emitter turns a run's plan into piperun's emission, and the slices of each pool into the file its
// sessions read them from, since a pool's items are only names.
type emitter struct {
	dir, pools string
	cfg        *config.Config
	ch         runs.Change
	store      *gitstore.Store
	root       string
}

type emission struct {
	data  []byte
	pools map[string][]piperun.Slice
	plan  *affected.Plan
	count int
}

// build is the plan's emission; again leaves out the slices that passed already, for a step that
// emits a plan written before.
func (e *emitter) build(ctx context.Context, plan *affected.Plan, again bool) (emission, error) {
	out := emission{pools: map[string][]piperun.Slice{}, plan: plan}

	suites := make([]piperun.Suite, 0, len(plan.Suites))

	for _, sp := range plan.Suites {
		s := e.cfg.Suites[sp.Suite]
		ps := piperun.Suite{Name: sp.Suite, Prepare: s.Prepare != "" || len(s.StepsOf(config.StepPrepare)) > 0, Whole: sp.WholeSuite}

		todo := sp.Selected

		if again {
			var err error
			if todo, ps.Whole, err = e.notPassed(ctx, sp); err != nil {
				return out, err
			}
		}

		ps.Slices = e.poolSlices(ctx, s, sp.Suite, todo)
		suites = append(suites, ps)

		if len(ps.Slices) > 0 {
			out.pools[plugin.PiperunSlices(sp.Suite)] = ps.Slices
			out.count += len(ps.Slices)
		}
	}

	em, err := piperun.Emit(suites)
	if err == nil {
		out.data, err = piperun.Encode(em)
	}

	if err != nil {
		return out, fmt.Errorf("plan --emit: %w", err)
	}

	return out, nil
}

// notPassed are the suite's selected slices and whole-suite checks that haven't passed.
func (e *emitter) notPassed(ctx context.Context, sp affected.SuitePlan) ([]bazel.Slice, []string, error) {
	states, err := report.States(ctx, e.store, reportID(e.ch), sp.Suite)
	if err != nil {
		return nil, nil, err
	}

	var (
		todo  []bazel.Slice
		whole []string
	)

	for _, sl := range sp.Selected {
		if states[sl.Name] != report.Passed {
			todo = append(todo, sl)
		}
	}

	for _, w := range sp.WholeSuite {
		if states[report.WholeName(w)] != report.Passed {
			whole = append(whole, w)
		}
	}

	return todo, whole, nil
}

// poolSlices are what a session needs of each slice, with its recorded duration for the pool's order
// and its predicted peak for admission; peaks that can't be read leave the sessions unchecked.
func (e *emitter) poolSlices(ctx context.Context, s config.Suite, suite string, todo []bazel.Slice) []piperun.Slice {
	if len(todo) == 0 {
		return nil
	}

	var file *durations.File

	if s.Durations != "" {
		if f, err := durations.Read(filepath.Join(e.root, s.Durations)); err == nil {
			file = f
		}
	}

	peaks, err := report.Peaks(ctx, e.store, suite)
	if err != nil {
		warnf(os.Stderr, "memory: %v; the sessions of %s start their slices unchecked\n", err, suite)
	}

	out := make([]piperun.Slice, 0, len(todo))

	for _, sl := range todo {
		ps := piperun.Slice{Name: sl.Name, Kind: sl.Kind, Sources: sl.Sources, Tests: sl.Tests}
		if !strings.HasPrefix(sl.Label, "@pipelinegen_deps//") {
			ps.Label, ps.Target = sl.Label, runTarget(sl)
		}

		for _, src := range sl.Sources {
			if file == nil {
				break
			}

			if v, ok := file.Seconds(src, nil); ok {
				ps.Seconds += v
				ps.Measured = true
			}
		}

		if p, ok := predict(peaks, []string{sl.Name}); ok {
			ps.PredictedMB = p.MB
		}

		out = append(out, ps)
	}

	return out
}

// write puts the pools' slices and the run's identity in the pools directory, the plan's record beside
// the emission, and the emission last, so a step that ends early leaves none.
func (e *emitter) write(em emission, stdout io.Writer) error {
	for name, list := range em.pools {
		if err := writeJSON(filepath.Join(e.pools, name+".json"), list); err != nil {
			return err
		}
	}

	if err := runs.WriteRunFile(e.pools, e.ch); err != nil {
		return err
	}

	if err := writeJSON(filepath.Join(e.dir, planRecordFile), em.plan); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(e.dir, plugin.EmitFile), em.data, 0o600); err != nil {
		return fmt.Errorf("plan --emit: %w", err)
	}

	return say(stdout, "%s %s: emitted %s in %s\n", e.ch.Run, e.ch.ID, plural(em.count, "slice"), plural(len(em.pools), "pool"))
}

// stored emits the plan already written for the change: this step's own, written before it was run
// again, or another build's; either way only what hasn't passed runs.
func (e *emitter) stored(ctx context.Context, stdout io.Writer) error {
	plan, ok, err := readPlan(ctx, e.store, reportID(e.ch))
	if err != nil {
		return err
	}

	if !ok {
		return say(stdout, "%s %s: no plan to emit\n", e.ch.Run, e.ch.ID)
	}

	em, err := e.build(ctx, plan, true)
	if err != nil {
		return err
	}

	if err := say(stdout, "%s %s: planned already; emitting what hasn't passed\n", e.ch.Run, e.ch.ID); err != nil {
		return err
	}

	return e.write(em, stdout)
}

func writeJSON(p string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("plan --emit: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return fmt.Errorf("plan --emit: %w", err)
	}

	if err := os.WriteFile(p, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("plan --emit: %w", err)
	}

	return nil
}

// readPool reads the slices plan --emit wrote for a suite's pool, in dir or else pools.
func readPool(dir, suite string) (map[string]piperun.Slice, error) {
	if dir == "" {
		dir = defaultPools
	}

	p := filepath.Join(dir, plugin.PiperunSlices(suite)+".json")

	data, err := os.ReadFile(p) //nolint:gosec // the run's own pool file
	if err != nil {
		return nil, fmt.Errorf("pool: %w", err)
	}

	var list []piperun.Slice
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("pool %s: %w", p, err)
	}

	out := make(map[string]piperun.Slice, len(list))
	for _, sl := range list {
		out[sl.Name] = sl
	}

	return out, nil
}
