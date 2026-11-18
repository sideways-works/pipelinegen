package kit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sideways-works/pipelinegen/internal/driver"
	"github.com/sideways-works/pipelinegen/plugin"
)

const kitSuite = "kit"

// kitDriver is what .pipelinegen-kit.json says of a driver's fixture: the slices to run, whether to
// record them, and the settings of a run step, a set-up and a prepare step.
type kitDriver struct {
	Slices  []plugin.DriverSlice `json:"slices"`
	Record  bool                 `json:"record"`
	Config  map[string]any       `json:"config"`
	Setup   map[string]any       `json:"setup"`
	Prepare map[string]any       `json:"prepare"`
}

func (k *kit) driverFixture() (kitDriver, bool) {
	var f struct {
		Driver *kitDriver `json:"driver"`
	}

	data, err := os.ReadFile(filepath.Join(k.o.Tree, kitFile))
	if err != nil || json.Unmarshal(data, &f) != nil || f.Driver == nil {
		return kitDriver{}, false
	}

	return *f.Driver, true
}

func describeDriver(d plugin.Description) []string {
	var problems []string

	if len(d.Verbs) == 0 {
		problems = append(problems, "a driver names its verbs")
	}

	for _, v := range d.Verbs {
		if !slices.Contains(plugin.DriverVerbs, v) {
			problems = append(problems, fmt.Sprintf("verb %q is none of %s", v, strings.Join(plugin.DriverVerbs, ", ")))
		}
	}

	types := []string{plugin.SettingString, plugin.SettingBool, plugin.SettingInt, plugin.SettingDuration, plugin.SettingList, plugin.SettingMap}

	for name, t := range d.Settings {
		if !slices.Contains(types, t) {
			problems = append(problems, fmt.Sprintf("setting %s has type %q, none of %s", name, t, strings.Join(types, ", ")))
		}
	}

	if d.Results != "" && d.Results != plugin.ResultsExit {
		problems = append(problems, fmt.Sprintf("results %q is neither empty nor %s", d.Results, plugin.ResultsExit))
	}

	if d.HasVerb(plugin.CallRun) && d.Routes == nil {
		problems = append(problems, "a driver that runs names the slices it can take (routes)")
	}

	return problems
}

// driverChecks run a driver on its fixture as the tool does: each slice alone, then as one bundle,
// recording and not, comparing results slice by slice; then marked bundles; then its set-up.
func (k *kit) driverChecks(ctx context.Context) {
	f, ok := k.driverFixture()
	if !ok {
		if k.desc.HasVerb(plugin.CallRun) || k.desc.HasVerb(plugin.CallSetup) || k.desc.HasVerb(plugin.CallPrepare) {
			k.check("the fixture exercises every verb the driver declares", "no driver section in %s of --tree, so its verbs never ran", kitFile)
		}

		return
	}

	if k.desc.Validates {
		k.driverValidate(ctx, f)
	}

	if k.desc.HasVerb(plugin.CallRun) {
		if len(f.Slices) < 2 {
			k.check("the fixture names slices to run", "it names %d; a bundle needs two", len(f.Slices))

			return
		}

		k.driverModes(ctx, f, false)

		if f.Record {
			k.driverModes(ctx, f, true)
			k.driverMarks(ctx, f)
		}
	}

	if k.desc.HasVerb(plugin.CallSetup) {
		k.driverSetup(ctx, f)
	}

	if k.desc.HasVerb(plugin.CallPrepare) {
		k.driverPrepare(ctx, f)
	}
}

type ends struct {
	mu    sync.Mutex
	got   map[string]string
	errs  []string
	marks []string
}

func (e *ends) Start(context.Context, plugin.DriverSlice) error        { return nil }
func (e *ends) Stop(context.Context, plugin.DriverSlice, string) error { return nil }

func (e *ends) End(_ context.Context, sl plugin.DriverSlice, ev *plugin.DriverEvent, why string, _ bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if ev == nil {
		e.got[sl.Name] = "no result: " + why

		return nil
	}

	var failed []string
	for _, f := range ev.Failures {
		failed = append(failed, f.Test)
	}

	tests := "?"
	if ev.Tests != nil {
		tests = strconv.Itoa(*ev.Tests)
	}

	files, problem := failureFiles(sl, ev)
	if problem != "" {
		e.errs = append(e.errs, sl.Name+": "+problem)
	}

	e.got[sl.Name] = fmt.Sprintf("passed %v, tests %s, failures [%s]%s", ev.Passed && len(failed) == 0, tests, strings.Join(failed, " "), files)

	return nil
}

func (k *kit) runFixture(ctx context.Context, f kitDriver, batch, record bool, marks *ends, stack bool) (*ends, string) {
	tree := k.work()
	if err := copyTree(k.o.Tree, tree); err != nil {
		return &ends{got: map[string]string{}, errs: []string{err.Error()}}, ""
	}

	coverage := k.work()

	var todo []plugin.DriverSlice

	for _, sl := range f.Slices {
		sl.Extras = filepath.Join(k.work(), "extras")
		sl.Files = filepath.Join(k.work(), "files")

		if record {
			sl.Recording = filepath.Join(coverage, filepath.FromSlash(sl.Name))
		}

		todo = append(todo, sl)
	}

	conf := k.o.Config
	if f.Config != nil {
		conf = f.Config
	}

	exe := &driver.Exec{Label: k.p.Name, Path: k.p.Path, Env: os.Environ(), Dir: tree, Log: io.Discard}

	req := plugin.DriverRequest{
		Protocol: plugin.Protocol, Work: k.work(), Cache: k.p.Cache, Config: conf,
		Tree: tree, Suite: kitSuite, Record: record, Stack: stack,
	}

	e := &ends{got: map[string]string{}}
	r := &driver.Runner{Hooks: e, Record: record, Log: io.Discard}

	if marks != nil {
		e = marks
		e.got = map[string]string{}
		dir := filepath.Join(k.work(), "marks")
		// a stack suite's bundle, or one of a suite whose correlators
		// collect at each slice's edges
		r = &driver.Runner{Hooks: &marking{ends: e}, Record: true, Stack: stack, Collects: !stack, Log: io.Discard,
			Marks: plugin.Marks{Command: markCommand(), Dir: dir}}
	}

	if err := r.Run(ctx, driver.Step{Driver: exe, Desc: k.desc, Batch: batch, Base: req}, todo); err != nil {
		e.errs = append(e.errs, err.Error())
	}

	return e, coverage
}

func markCommand() []string {
	exe, err := os.Executable()
	if err != nil {
		return []string{toolName, sliceCommand, "mark"}
	}

	return []string{exe, sliceCommand, "mark"}
}

type marking struct{ ends *ends }

func (m *marking) Start(_ context.Context, sl plugin.DriverSlice) error {
	m.ends.marks = append(m.ends.marks, "start "+sl.Name)

	return nil
}

func (m *marking) Stop(_ context.Context, sl plugin.DriverSlice, _ string) error {
	m.ends.marks = append(m.ends.marks, "stop "+sl.Name)

	return nil
}

func (m *marking) End(ctx context.Context, sl plugin.DriverSlice, ev *plugin.DriverEvent, why string, apart bool) error {
	return m.ends.End(ctx, sl, ev, why, apart)
}

func (k *kit) driverModes(ctx context.Context, f kitDriver, record bool) {
	what := "not recording"
	if record {
		what = "recording"
	}

	one, oneRec := k.runFixture(ctx, f, false, record, nil, false)
	all, allRec := k.runFixture(ctx, f, true, record, nil, false)

	k.check("run answers every slice, one by one and bundled, "+what, strings.Join(append(one.errs, all.errs...), "; "))

	var differ []string

	for _, sl := range f.Slices {
		a, b := one.got[sl.Name], all.got[sl.Name]

		switch {
		case a == "" || strings.HasPrefix(a, "no result"):
			differ = append(differ, fmt.Sprintf("%s got no result on its own: %s", sl.Name, a))
		case a != b:
			differ = append(differ, fmt.Sprintf("%s: on its own %s, bundled %s", sl.Name, a, b))
		}
	}

	k.check("one by one and bundled give each slice the same result, "+what, strings.Join(differ, "; "))

	if !record {
		return
	}

	var lost []string

	for _, sl := range f.Slices {
		a, errA := recording(filepath.Join(oneRec, filepath.FromSlash(sl.Name)))
		b, errB := recording(filepath.Join(allRec, filepath.FromSlash(sl.Name)))

		switch {
		case errA != nil || len(a) == 0:
			lost = append(lost, fmt.Sprintf("%s left no recording on its own (%v)", sl.Name, errA))
		case !k.desc.Apart:
			// the tool records such a driver slice by slice: its bundles
			// are never asked to keep recordings apart
		case errB != nil || !slices.Equal(a, b):
			lost = append(lost, sl.Name+": its bundled recording is not its recording on its own")
		}
	}

	k.check("one by one and bundled leave each slice the same recording", strings.Join(lost, "; "))
}

func recording(dir string) ([]string, error) {
	var out []string

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		data, err := os.ReadFile(p) //nolint:gosec // the kit's own run of the fixture
		if err != nil {
			return fmt.Errorf("recording: %w", err)
		}

		rel, _ := filepath.Rel(dir, p) //nolint:errcheck // p is under dir
		lines := strings.Split(string(data), "\n")
		sort.Strings(lines)

		for _, l := range lines {
			out = append(out, rel+": "+l)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("recording %s: %w", dir, err)
	}

	return out, nil
}

func (k *kit) driverMarks(ctx context.Context, f kitDriver) {
	one, _ := k.runFixture(ctx, f, false, true, nil, false)

	variants := []struct {
		stack bool
		what  string
	}{{true, "a recorded stack suite's bundle"}}

	// a driver that keeps no recording apart is called once per slice in a recording run of a suite
	// without a stack, as the tool calls it: no bundle of it is marked there
	if k.desc.Apart {
		variants = append(variants, struct {
			stack bool
			what  string
		}{false, "a recorded bundle of a suite with correlators"})
	}

	for _, v := range variants {
		marked, _ := k.runFixture(ctx, f, true, true, &ends{}, v.stack)

		want := make([]string, 0, 2*len(f.Slices))
		for _, sl := range f.Slices {
			want = append(want, "start "+sl.Name, "stop "+sl.Name)
		}

		got := slices.Clone(marked.marks)

		k.check(v.what+" marks each slice's start and stop, one slice at a time", ifNot(sameMarks(got, want),
			fmt.Sprintf("marks %v, want each slice's start then stop, in pairs", got)))

		var differ []string

		for _, sl := range f.Slices {
			if one.got[sl.Name] != marked.got[sl.Name] {
				differ = append(differ, fmt.Sprintf("%s: on its own %s, marked %s", sl.Name, one.got[sl.Name], marked.got[sl.Name]))
			}
		}

		k.check(v.what+", marked, gives each slice its result on its own", strings.Join(append(marked.errs, differ...), "; "))
	}
}

func sameMarks(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}

	for i := 0; i < len(got); i += 2 {
		start, stop := strings.TrimPrefix(got[i], "start "), strings.TrimPrefix(got[i+1], "stop ")
		if start == got[i] || stop == got[i+1] || start != stop {
			return false
		}
	}

	a, b := slices.Clone(got), slices.Clone(want)
	slices.Sort(a)
	slices.Sort(b)

	return slices.Equal(a, b)
}

// driverSetup: a set-up gets ready and stops when its stdin closes.
func (k *kit) driverSetup(ctx context.Context, f kitDriver) {
	tree := k.work()
	if err := copyTree(k.o.Tree, tree); err != nil {
		k.check("setup gets ready and stops", "%v", err)

		return
	}

	exe := &driver.Exec{Label: k.p.Name, Path: k.p.Path, Env: os.Environ(), Dir: tree, Log: io.Discard, StopWait: 10 * time.Second}
	req := plugin.DriverRequest{
		Protocol: plugin.Protocol, Work: k.work(), Cache: k.p.Cache, Config: f.Setup,
		Tree: tree, Suite: kitSuite,
	}

	sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	r, err := exe.Setup(sctx, req)
	if err != nil {
		k.check("setup gets ready and stops", "%v", err)

		return
	}

	k.check("setup gets ready and stops", ifNot(r.Stop(ctx) == nil, "it did not stop when its stdin closed"))
}

// driverPrepare: a prepare step, given the fixture's prepare settings,
// answers by exiting 0 and leaves something in the prepared directory.
func (k *kit) driverPrepare(ctx context.Context, f kitDriver) {
	const name = "prepare fills the prepared directory"

	if f.Prepare == nil {
		k.check(name, "the driver prepares, and the fixture's driver section has no prepare settings, so prepare never ran")

		return
	}

	tree := k.work()
	if err := copyTree(k.o.Tree, tree); err != nil {
		k.check(name, "%v", err)

		return
	}

	prepared := k.work()
	exe := &driver.Exec{Label: k.p.Name, Path: k.p.Path, Env: os.Environ(), Dir: tree, Log: io.Discard}
	req := plugin.DriverRequest{
		Protocol: plugin.Protocol, Work: k.work(), Cache: k.p.Cache, Config: f.Prepare,
		Tree: tree, Suite: kitSuite, Prepared: prepared,
	}

	pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	if err := exe.Prepare(pctx, req); err != nil {
		k.check(name, "%v", err)

		return
	}

	entries, err := os.ReadDir(prepared)
	k.check(name, ifNot(err == nil && len(entries) > 0, "it exited 0 and left the prepared directory empty"))
}

func copyTree(src, dst string) error {
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(src, p) //nolint:errcheck // p is under src
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}

		data, err := os.ReadFile(p) //nolint:gosec // the plugin's own fixture
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}

		return os.WriteFile(target, data, info.Mode().Perm()) //nolint:gosec // the kit's own copy
	})
	if err != nil {
		return fmt.Errorf("copy the fixture: %w", err)
	}

	return nil
}

// failureFiles checks the files an end names: only a slice that didn't pass names any, each a regular
// file inside its files directory. It says how many, for the modes to compare.
func failureFiles(sl plugin.DriverSlice, ev *plugin.DriverEvent) (string, string) {
	if len(ev.Files) == 0 {
		return "", ""
	}

	if ev.Passed && len(ev.Failures) == 0 {
		return "", fmt.Sprintf("a passed slice names %d failure files", len(ev.Files))
	}

	root, err := os.OpenRoot(sl.Files)
	if err != nil {
		return "", fmt.Sprintf("names failure files, but its files directory can't be read: %v", err)
	}

	defer func() { _ = root.Close() }() //nolint:errcheck // only read

	var bad []string

	for _, f := range ev.Files {
		fi, err := root.Lstat(filepath.FromSlash(f.Path))

		switch {
		case f.Path == "" || filepath.IsAbs(f.Path) || slices.Contains(strings.Split(f.Path, "/"), ".."):
			bad = append(bad, fmt.Sprintf("%q is no path inside the slice's files directory", f.Path))
		case err != nil:
			bad = append(bad, fmt.Sprintf("%s: %v", f.Path, err))
		case !fi.Mode().IsRegular():
			bad = append(bad, f.Path+" is no regular file")
		}
	}

	return fmt.Sprintf(", files %d", len(ev.Files)), strings.Join(bad, "; ")
}
