package kit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/plugin"
)

const partsCheck = "collect and observe take a slice run twice in one batch"

// kitRecord is a fixture's recording command in .pipelinegen-kit.json. The
// kit runs it Parts times (at least two) for one slice in one batch, as a
// batch task runs: in a directory holding the tree as source/ and the
// coverage output as coverage/, with PIPELINEGEN_RECORD=true;
// PIPELINEGEN_SLICE and PIPELINEGEN_PART are the kit's own. Serve, when set,
// is the batch's stack, started before the setup's end and stopped after the
// last collect; Ready names the file (a glob) whose appearance says it
// answers.
type kitRecord struct {
	Slice  string         `json:"slice"`
	Run    []string       `json:"run"`
	Parts  int            `json:"parts"`
	Stack  *bool          `json:"stack"`
	Config map[string]any `json:"config"`
	Serve  []string       `json:"serve"`
	Ready  string         `json:"ready"`
}

type kitRecords []kitRecord

func (r *kitRecords) UnmarshalJSON(data []byte) error {
	var one kitRecord
	if err := json.Unmarshal(data, &one); err == nil {
		*r = kitRecords{one}

		return nil
	}

	var many []kitRecord
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("record: %w", err)
	}

	*r = many

	return nil
}

// kitFixture is what .pipelinegen-kit.json says of a collecting plugin: its
// recordings, and its entry's name in the fixture's config, which the core
// names its coverage directories after (default: the plugin file's name).
type kitFixture struct {
	Name   string     `json:"name"`
	Record kitRecords `json:"record"`
}

// parts records the fixture's slice in parts, once per recording entry, as
// the core does; every part's directory must hold the part's recording, and
// observe over all the parts must tell at least what each part alone tells.
func (k *kit) parts(ctx context.Context) {
	var f kitFixture

	data, err := os.ReadFile(filepath.Join(k.o.Tree, kitFile))
	if err == nil {
		err = json.Unmarshal(data, &f)
	}

	if err != nil || len(f.Record) == 0 {
		k.check(partsCheck, "the fixture names no recording command (%s: {\"record\": {\"slice\", \"run\", \"parts\"}}), so collect never gathered a slice run twice", kitFile)

		return
	}

	name := f.Name
	if name == "" {
		name = strings.TrimSuffix(k.p.Name, filepath.Ext(k.p.Name))
	}

	var problems []string

	for i, r := range f.Record {
		for _, p := range k.recordParts(ctx, name, r) {
			if len(f.Record) > 1 {
				p = fmt.Sprintf("record %d (stack %t): %s", i+1, r.Stack == nil || *r.Stack, p)
			}

			problems = append(problems, p)
		}
	}

	k.check(partsCheck, strings.Join(problems, "; "))
}

func (k *kit) recordParts(ctx context.Context, name string, r kitRecord) []string {
	if len(r.Run) == 0 || r.Slice == "" {
		return []string{"a record entry needs a slice and a run command"}
	}

	if r.Parts < 2 {
		r.Parts = 2
	}

	batch := k.work()
	tree, cov := filepath.Join(batch, plugin.ObserveTree), filepath.Join(batch, plugin.ObserveCoverage)

	if err := os.MkdirAll(cov, 0o750); err != nil {
		return []string{err.Error()}
	}

	if err := copyFixture(k.o.Tree, tree); err != nil {
		return []string{"copying the fixture: " + err.Error()}
	}

	// the plugin runs as the batch's steps run it: from the batch's
	// directory, with the config a pipeline gives it
	step := k.p
	step.Dir = batch

	if r.Config != nil {
		step.Config = r.Config
	}

	stack := r.Stack == nil || *r.Stack
	setup := filepath.Join(cov, "_setup")
	dirs := []string{filepath.Join(setup, name)}

	var srv *server

	if len(r.Serve) > 0 {
		var err error
		if srv, err = startServer(ctx, batch, r); err != nil {
			return []string{err.Error()}
		}
	}

	problems := k.collectAt(ctx, step, plugin.CollectSetupEnd, r.Slice, filepath.Join(setup, name), stack)

	for part := 1; part <= r.Parts; part++ {
		own := filepath.Join(report.PartDir(cov, r.Slice, part), name)

		problems = append(problems, k.collectAt(ctx, step, plugin.CollectSliceStart, r.Slice, own, stack)...)

		if err := runPart(ctx, batch, &r, part); err != nil {
			problems = append(problems, fmt.Sprintf("part %d: the recording command: %v", part, err))
		}

		problems = append(problems, k.collectAt(ctx, step, plugin.CollectSliceEnd, r.Slice, own, stack)...)

		if entries, err := os.ReadDir(own); err != nil || len(entries) == 0 {
			problems = append(problems, fmt.Sprintf("part %d left nothing in %s, the directory the core names for it", part, relTo(batch, own)))
		}

		dirs = append(dirs, own)
	}

	// the stack stops after the last collect, as a batch stops it
	if srv != nil {
		problems = append(problems, srv.stop()...)
	}

	if len(problems) == 0 {
		problems = k.observeParts(ctx, step, tree, r.Slice, dirs)
	}

	return problems
}

func (k *kit) collectAt(ctx context.Context, step plugin.Plugin, action, slice, dir string, stack bool) []string {
	if !k.desc.Handles(action) {
		return nil
	}

	req := plugin.CollectRequest{Request: step.NewRequest(k.work()), Action: action, Slice: slice, Dir: dir, Stack: stack}
	if err := k.callAs(ctx, step, plugin.CallCollect, req, nil); err != nil {
		return []string{action + ": " + err.Error()}
	}

	return nil
}

func (k *kit) observeParts(ctx context.Context, step plugin.Plugin, tree, slice string, dirs []string) []string {
	touched := func(dirs []string) (int, error) {
		work := k.work()

		var ans plugin.ObserveAnswer

		err := k.callAs(ctx, step, plugin.CallObserve, plugin.ObserveRequest{Request: step.NewRequest(work), Tree: tree, Slice: slice, Dirs: dirs}, &ans)
		if err == nil {
			ans.Touched, err = plugin.ReadLines(work, named(ans.Files, "touched"), ans.Touched)
		}

		return len(ans.Touched), err
	}

	all, err := touched(dirs)

	switch {
	case errors.Is(err, plugin.ErrCantTell):
		return []string{"observe can't tell from the parts together: " + err.Error()}
	case err != nil:
		return []string{"observe over the parts: " + err.Error()}
	}

	var problems []string

	for i := 1; i < len(dirs); i++ {
		alone, err := touched([]string{dirs[0], dirs[i]})
		if err == nil && alone > all {
			problems = append(problems, fmt.Sprintf("observe tells %d units from part %d alone but %d from every part together", alone, i, all))
		}
	}

	return problems
}

func (k *kit) callAs(ctx context.Context, p plugin.Plugin, call string, req, answer any) error {
	err := p.Call(ctx, call, req, answer)
	if err != nil {
		k.seen.WriteString(err.Error())
	}

	k.seen.WriteString(canonical(answer))

	return err
}

func runPart(ctx context.Context, batch string, r *kitRecord, part int) error {
	cmd := exec.CommandContext(ctx, r.Run[0], r.Run[1:]...) //nolint:gosec // the fixture's own command, run by its plugin's kit
	cmd.Dir = batch

	cmd.Env = append(os.Environ(), "PIPELINEGEN_RECORD=true", "PIPELINEGEN_SLICE="+r.Slice, "PIPELINEGEN_PART="+strconv.Itoa(part))

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

func relTo(root, p string) string {
	if rel, err := filepath.Rel(root, p); err == nil {
		return filepath.ToSlash(rel)
	}

	return p
}
