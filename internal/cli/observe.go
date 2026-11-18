package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/report"
)

// A recording batch observes its ended slices in its own task into observedDir, its end only what was
// left; observing never changes a slice's result, and stops in time for the task's limit.
const observedDir = "observed"

var errInterpreter = errors.New("interpreter missing")

func interpreter(path string) (string, bool) {
	f, err := os.Open(path) //nolint:gosec // the batch's own tools
	if err != nil {
		return "", false
	}

	defer f.Close() //nolint:errcheck // read only

	line, err := bufio.NewReader(io.LimitReader(f, 512)).ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}

	rest, ok := strings.CutPrefix(line, "#!")
	if !ok {
		return "", false
	}

	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", false
	}

	prog := fields[0]

	// env names the program it runs: the first word that isn't one of its
	// options.
	if filepath.Base(prog) == "env" {
		prog = ""

		for _, w := range fields[1:] {
			if !strings.HasPrefix(w, "-") && !strings.Contains(w, "=") {
				prog = w

				break
			}
		}

		if prog == "" {
			return "", false
		}
	}

	if filepath.IsAbs(prog) {
		_, err := os.Stat(prog)

		return prog, err != nil
	}

	_, err = exec.LookPath(prog)

	return prog, err != nil
}

func (b *batchPlugins) missingInterpreters() map[string]string {
	out := map[string]string{}

	if b == nil {
		return out
	}

	for _, c := range b.sets {
		if prog, missing := interpreter(filepath.Join(b.dir, c.name)); missing {
			out[c.name] = prog
		}
	}

	return out
}

// Without its interpreter a correlator can't describe itself or collect, and every slice would end
// errored after its tests ran.
func needInterpreters(b *batchPlugins, suite, task string) error {
	missing := b.missingInterpreters()
	if len(missing) == 0 {
		return nil
	}

	lines := make([]string, 0, len(missing))

	for _, c := range b.sets {
		if prog, ok := missing[c.name]; ok {
			lines = append(lines, interpreterLine(suite, c.name, prog, task))
		}
	}

	return fmt.Errorf("%w: %s", errInterpreter, strings.Join(lines, "; "))
}

func interpreterLine(suite, correlator, prog, task string) string {
	return fmt.Sprintf("suite %s records with %s, which needs %s; this task (%s) has none", suite, correlator, prog, task)
}

func (b *batchPlugins) keptPath(slice, correlator string) string {
	return filepath.Join(b.observed, url.PathEscape(slice), correlator+".json")
}

func (b *batchPlugins) kept(slice, correlator string) ([]report.Observation, bool) {
	if b.observed == "" {
		return nil, false
	}

	data, err := os.ReadFile(b.keptPath(slice, correlator))
	if err != nil {
		return nil, false
	}

	var kept []keptObservation
	if json.Unmarshal(data, &kept) != nil {
		return nil, false
	}

	obs := make([]report.Observation, 0, len(kept))
	for _, k := range kept {
		obs = append(obs, report.Observation(k))
	}

	return obs, true
}

type keptObservation struct {
	Plugin    string   `json:"plugin"`
	CantTell  bool     `json:"cant_tell,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Touched   []string `json:"touched"`
	SetupLike []string `json:"setup_like,omitempty"`
}

func (b *batchPlugins) keep(slice, correlator string, obs []report.Observation) error {
	p := b.keptPath(slice, correlator)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return fmt.Errorf("observed: %w", err)
	}

	kept := make([]keptObservation, 0, len(obs))
	for _, o := range obs {
		kept = append(kept, keptObservation(o))
	}

	data, err := json.Marshal(kept)
	if err != nil {
		return fmt.Errorf("observed: %w", err)
	}

	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("observed: %w", err)
	}

	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("observed: %w", err)
	}

	return nil
}

func observeOne(ctx context.Context, c correlatorSet, source, slice, setup string, parts []string) ([]report.Observation, error) {
	if c.err != nil {
		return nil, fmt.Errorf("observe: %w", c.err)
	}

	obs, err := c.set.Observe(ctx, source, slice, setup, parts)
	if err != nil {
		return nil, fmt.Errorf("observe: %w", err)
	}

	out := make([]report.Observation, 0, len(obs))
	for _, o := range obs {
		out = append(out, report.Observation{Plugin: o.Plugin, CantTell: o.CantTell, Reason: o.Reason, Touched: o.Touched, SetupLike: o.SetupLike})
	}

	return out, nil
}

// observeAhead stops at until, so the task ends within its limit; what it didn't observe is left to the
// batch's end.
func (b *batchPlugins) observeAhead(ctx context.Context, j *report.Journal, suite string, until time.Time, log io.Writer) {
	if b == nil || b.observed == "" {
		return
	}

	ends, err := j.Ends()
	if err != nil {
		warnf(log, "pipelinegen slice run: observe stays with the batch's end: %v\n", err)

		return
	}

	ctx, cancel := context.WithDeadline(ctx, until)
	defer cancel()

	left := map[string]bool{}
	missing := b.missingInterpreters()

	for _, ev := range ends {
		if ev.Suite != suite || ev.Unrecordable != "" || len(ev.Coverage) == 0 {
			continue
		}

		setup, parts := report.ObserveInputs(ev)

		for _, c := range b.sets {
			if left[c.name] {
				continue
			}

			var why string

			switch prog, lacks := missing[c.name]; {
			case lacks:
				why = "this task has no " + prog
			case ctx.Err() != nil:
				why = "the task's time ran out"
			default:
				obs, err := observeOne(ctx, c, ev.Source, ev.Slice, setup, parts)
				if err == nil {
					err = b.keep(ev.Slice, c.name, obs)
				}

				if err == nil {
					continue
				}

				why = err.Error()
				if ctx.Err() != nil {
					why = "the task's time ran out"
				}
			}

			left[c.name] = true

			warnf(log, "pipelinegen slice run: observe for %s stays with the batch's end: %s\n", c.name, why)
		}
	}
}

func (b *batchPlugins) sayLeftUnrunnable(suite string, evs []report.SliceEvent, log io.Writer) {
	missing := b.missingInterpreters()

	for _, c := range b.sets {
		prog, lacks := missing[c.name]
		if !lacks {
			continue
		}

		left := 0

		for _, ev := range evs {
			if _, ok := b.kept(ev.Slice, c.name); !ok {
				left++
			}
		}

		if left > 0 {
			warnf(log, "pipelinegen %s: %s: %s not observed in the batch fail\n", batchEndCommand, interpreterLine(suite, c.name, prog, batchEndCommand), plural(left, "slice"))
		}
	}
}
