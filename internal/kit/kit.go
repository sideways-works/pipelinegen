// Package kit checks that a plugin keeps the protocol: what every plugin shares, and per kind what
// the core relies on; a plugin repository runs it with `pipelinegen plugin test`.
package kit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/toolcalls"
	"github.com/sideways-works/pipelinegen/plugin"
	pkit "github.com/sideways-works/plugkit/kit"
)

type Result = pkit.Result

type Options struct {
	Path string
	// Tree is a fixture tree of the plugin's own, for sboms and correlators; without one only
	// describe and the calls' refusals are checked.
	Tree   string
	Config map[string]any
	Work   string
	// Commands are the tool's own commands and flags: a runner's pipeline
	// may call only those. Without them that check is left out.
	Commands Commands
	// Cache is the plugin's cache directory, filled as the planning job's prepare fills it (a Go
	// module cache in <cache>/gomodcache, say); default a fresh one under Work.
	Cache string
	// SBOM is an sbom plugin a correlator's links name units of, run beside it through the core's
	// cache with SBOMConfig: the checks that compare unit digests need it.
	SBOM       string
	SBOMConfig map[string]any
}

type kit struct {
	*pkit.Harness

	o       Options
	p       plugin.Plugin
	desc    plugin.Description
	copies  copies
	touched []string
}

// current calls the plugin as the kit has it now: describe tells the caches it gets.
type current struct{ k *kit }

func (c current) Call(ctx context.Context, call string, req, answer any) error {
	return c.k.p.Call(ctx, call, req, answer)
}

// Canary is what the kit puts in the token's variable: a plugin never gets
// the token, so no answer or message may carry it.
const Canary = "pipelinegen-kit-canary-0f8c2e"

func (k *kit) call(ctx context.Context, call string, req any, answer any) error {
	return k.Call(ctx, call, req, answer)
}

func (k *kit) can(call string) bool { return slices.Contains(k.desc.Capabilities, call) }

func Run(ctx context.Context, o Options) []Result {
	// the kit walks the fixture and hands it to the plugin by its real
	// path, as the core hands a tree to plugins
	if o.Tree != "" {
		if resolved, err := filepath.EvalSymlinks(o.Tree); err == nil {
			o.Tree = resolved
		}
	}

	// observe runs in a directory of its own, so every path the plugin
	// gets is whole
	o.Tree, o.Path, o.Work, o.Cache = absOrEmpty(o.Tree), absOrEmpty(o.Path), absOrEmpty(o.Work), absOrEmpty(o.Cache)

	k := &kit{o: o, p: plugin.Plugin{Name: filepath.Base(o.Path), Path: o.Path, Config: o.Config, Cache: o.Cache}}
	k.Harness = &pkit.Harness{Plugin: current{k}, Work: o.Work, Canary: Canary}

	if k.p.Cache == "" {
		k.p.Cache = filepath.Join(o.Work, "cache")
	}

	restore := pkit.SetEnv("GITHUB_TOKEN", Canary)
	defer restore()

	if err := pinToolchains(o.Tree, o.Work); err != nil {
		k.Check("the fixture's version files are copied to the work directory", "%v", err)

		return k.Results()
	}

	if !k.describe(ctx) {
		return k.Results()
	}

	k.p.Caches = k.desc.Caches

	k.refusals(ctx)
	k.fills(ctx)

	switch k.desc.Kind {
	case plugin.KindRunner:
		k.runner(ctx)
	case plugin.KindSBOM:
		k.sbom(ctx)
	case plugin.KindCorrelator:
		k.correlator(ctx)
	case plugin.KindConnector:
		k.connector(ctx)
	case plugin.KindDriver:
		k.driverChecks(ctx)
	}

	k.CheckCanary()

	return k.Results()
}

// unexercised fails a plugin whose fixture never let the kit call what it
// declares: a check that never ran passes nothing.
func (k *kit) unexercised() bool {
	if k.o.Tree != "" {
		return false
	}

	var calls []string

	for _, c := range []string{plugin.CallDetect, plugin.CallUnits, plugin.CallLinks, plugin.CallBind, plugin.CallObserve, plugin.CallCollect} {
		if k.can(c) || c == plugin.CallUnits && k.desc.Kind == plugin.KindSBOM {
			calls = append(calls, c)
		}
	}

	k.Check("the fixture exercises every call the plugin declares", "no --tree, so %s never ran", strings.Join(calls, ", "))

	return true
}

func (k *kit) request() plugin.Request { return k.p.NewRequest(k.WorkDir()) }

func (k *kit) describe(ctx context.Context) bool {
	if err := k.call(ctx, plugin.CallDescribe, k.request(), &k.desc); err != nil {
		k.Check("describe answers", "%v", err)

		return false
	}

	d := k.desc

	var problems []string

	if d.Protocol != plugin.Protocol {
		problems = append(problems, fmt.Sprintf("protocol %d, not %d", d.Protocol, plugin.Protocol))
	}

	if !slices.Contains([]string{plugin.KindRunner, plugin.KindSBOM, plugin.KindCorrelator, plugin.KindConnector, plugin.KindDriver}, d.Kind) {
		problems = append(problems, fmt.Sprintf("kind %q", d.Kind))
	}

	if d.Name == "" || d.Version == "" {
		problems = append(problems, "no name or version")
	}

	for _, n := range d.Narrowings {
		if n.Name == "" || n.Note == "" || n.OffAt < 2 || n.OffAt > config.MaxNarrowing {
			problems = append(problems, fmt.Sprintf("narrowing %q needs a note and off_at from 2 to %d", n.Name, config.MaxNarrowing))
		}
	}

	for call, how := range d.Inputs {
		if how != plugin.PerFile && how != plugin.PerSet {
			problems = append(problems, fmt.Sprintf("inputs of %s: %q is neither per-file nor per-set", call, how))
		}
	}

	if d.Kind == plugin.KindCorrelator && len(d.Namespaces) > 0 {
		problems = append(problems, "a correlator owns no namespace")
	}

	if d.Kind != plugin.KindCorrelator && len(d.ObserveReads) > 0 {
		problems = append(problems, "only a correlator observes, so only one names observe_reads")
	}

	if slices.Contains(d.ObserveReads, "") {
		problems = append(problems, "observe_reads names an empty key")
	}

	if d.Kind != plugin.KindDriver && len(d.Renders) > 0 {
		problems = append(problems, "only a driver renders")
	}

	if d.Kind == plugin.KindConnector {
		if len(d.Namespaces) > 0 {
			problems = append(problems, "a connector owns no namespace")
		}

		problems = append(problems, describeConnector(d)...)
	}

	if d.Kind == plugin.KindDriver {
		problems = append(problems, describeDriver(d)...)
	}

	k.Check("describe answers the protocol", strings.Join(problems, "; "))

	if slices.Contains(d.Capabilities, plugin.CallBind) && !plugins.ToolchainsTold(d) {
		k.Add(Result{
			Check: "bind answers can be cached", Note: true,
			Problem: fmt.Sprintf("it needs %s and reports no toolchains for them, so its bind is asked on every plan", strings.Join(d.Needs, ", ")),
		})
	}

	return len(problems) == 0
}

// refusals: a call the plugin doesn't know fails, and isn't "can't tell".
func (k *kit) refusals(ctx context.Context) {
	others := map[string]string{
		plugin.KindRunner:     plugin.CallUnits,
		plugin.KindSBOM:       plugin.CallPipeline,
		plugin.KindCorrelator: plugin.CallPipeline,
		plugin.KindConnector:  plugin.CallPipeline,
		plugin.KindDriver:     plugin.CallUnits,
	}

	k.Refuses(ctx, func() any { return k.request() }, "no-such-call", others[k.desc.Kind])
}

func (k *kit) runner(ctx context.Context) {
	root := k.WorkDir()

	var (
		texts   [][]byte
		notices []string
	)

	for range 2 {
		work := k.WorkDir()

		var ans plugin.PipelineAnswer

		req := plugin.PipelineRequest{Request: k.p.NewRequest(work), Spec: Spec(), Root: root}
		if err := k.call(ctx, plugin.CallPipeline, req, &ans); err != nil {
			k.Check("pipeline answers", "%v", err)

			return
		}

		if len(ans.Files) != 1 {
			k.Check("pipeline answers one file", "%d files", len(ans.Files))

			return
		}

		text, err := os.ReadFile(filepath.Join(work, filepath.Clean(ans.Files[0]))) //nolint:gosec // inside the call's work directory
		if err != nil || len(text) == 0 {
			k.Check("pipeline answers one file", "the file is missing or empty: %v", err)

			return
		}

		texts = append(texts, text)
		notices = append(notices, ans.Notices...)
	}

	k.Check("pipeline answers one file", "")
	k.Check("the pipeline's notices are one line each", brokenNotices(notices))
	k.Check("pipeline is the same every time", ifNot(bytes.Equal(texts[0], texts[1]), "two calls wrote different pipelines"))
	k.placement(ctx, root, texts[0])

	k.Check("the pipeline references only the token, the declared secrets and the repository's own vars", strayVars(texts[0], Spec()))

	if k.o.Commands == nil {
		k.Skip("the pipeline's steps call what the tool has and mount what it reads", "no command table given")

		return
	}

	k.steps(ctx, root, texts[0])
}

func brokenNotices(notices []string) string {
	var bad []string

	for _, n := range notices {
		if strings.TrimSpace(n) == "" || strings.ContainsAny(n, "\r\n") {
			bad = append(bad, fmt.Sprintf("%q", n))
		}
	}

	return strings.Join(firstFew(bad), "; ")
}

// steps checks every call of the tool in the pipelines written for the fixture spec and its variants:
// the tool has each command and flag, and each step mounts what it reads and keeps what it writes.
func (k *kit) steps(ctx context.Context, root string, plain []byte) {
	var (
		calls, mounts []string
		sh            shapes
		held          holdings
	)

	seen := map[string]bool{}

	for _, v := range specVariants() {
		// a runner that builds no connector's chain is refused such a run
		// by pipelinegen pipeline before it is asked
		if v.name == connectorRun && !k.can(plugin.CapConnectorChanges) {
			continue
		}

		// nor a continuous run, when it can't build one
		if !k.builds(v) {
			continue
		}

		text := plain

		if v.name != "" {
			work := k.WorkDir()

			var ans plugin.PipelineAnswer
			if err := k.call(ctx, plugin.CallPipeline, plugin.PipelineRequest{Request: k.p.NewRequest(work), Spec: v.spec, Root: root}, &ans); err != nil || len(ans.Files) != 1 {
				k.Check("pipeline answers for the spec "+v.name, "%v (%d files)", err, len(ans.Files))

				continue
			}

			var err error
			if text, err = os.ReadFile(filepath.Join(work, filepath.Clean(ans.Files[0]))); err != nil { //nolint:gosec // inside the call's work directory
				k.Check("pipeline answers for the spec "+v.name, "%v", err)

				continue
			}
		}

		c, m := callChecks(k.o.Commands, v.spec, text, seen)
		for _, p := range c {
			calls = appendNew(calls, p)
		}

		for _, p := range m {
			mounts = appendNew(mounts, v.label()+p)
		}

		held.add(v, text)

		sh.add(ctx, v, text)
	}

	held.report(k)
	k.shapeChecks(sh)

	var missing []string

	for _, step := range toolcalls.Steps {
		if !seen[step] {
			missing = append(missing, step)
		}
	}

	k.Check("the pipeline calls only commands and flags the tool has", strings.Join(firstFew(calls), "; "))
	k.Check("every step mounts what its pipelinegen command reads and keeps what it writes", strings.Join(firstFew(mounts), "; "))
	k.Check("the pipeline has every step the contracts name", ifNot(len(missing) == 0, "no step runs pipelinegen "+strings.Join(missing, ", pipelinegen ")))
}

type shapes struct {
	limits, queues, follows, handed, unrecorded, locks []string
}

func (sh *shapes) add(ctx context.Context, v specVariant, text []byte) {
	sh.limits = appendLabelled(sh.limits, v, unlimitedSteps(text))
	sh.queues = appendLabelled(sh.queues, v, serializedJobs(v.spec, text))
	sh.follows = appendLabelled(sh.follows, v, append(unplannedStarts(v.spec, text), undecidedPrepares(v.spec, text)...))
	sh.handed = appendLabelled(sh.handed, v, unhandedStarts(v.spec, text))
	sh.unrecorded = appendLabelled(sh.unrecorded, v, swallowedUnrecorded(ctx, text, installsOf(v.spec)...))
	sh.locks = appendLabelled(sh.locks, v, lockSteps(text))
}

func (k *kit) shapeChecks(sh shapes) {
	k.Check("no step takes a lock, even for an older tool's spec that holds one: none runs pipelinegen lock or unlock", strings.Join(firstFew(sh.locks), "; "))
	k.Check("every step the pipeline runs has a time limit", strings.Join(firstFew(sh.limits), "; "))
	k.Check("no job is serialized unless the spec asks, a planning job never", strings.Join(firstFew(sh.queues), "; "))

	if k.can(plugin.CapContinuous) {
		k.Check("a continuous run's suite jobs start only for a commit its planning job planned, which decides before its prepare", strings.Join(firstFew(sh.follows), "; "))
	}

	k.Check("a pull request run's suite jobs start only for a head its planning job or its sweep hands on, through the gate plan --from writes", strings.Join(firstFew(sh.handed), "; "))
	k.Check("no job passes when a report step could not write the run's records (exit 4)", strings.Join(firstFew(sh.unrecorded), "; "))
}

func appendLabelled(out []string, v specVariant, problems []string) []string {
	for _, p := range problems {
		out = appendNew(out, v.label()+p)
	}

	return out
}

type specVariant struct {
	name string
	spec plugin.Spec
}

func (v specVariant) label() string {
	if v.name == "" {
		return ""
	}

	return "(" + v.name + ") "
}

// specVariants are the fixture spec, set by itself, and the others below: together every kind of step a
// runner writes; an older tool's spec with locks must still be taken, writing no lock step.
func specVariants() []specVariant {
	self := Spec()
	self.CI["self_set"] = true

	return []specVariant{
		{"", Spec()}, {olderLocksRun, olderLocksSpec("")}, {"set by itself", self}, {splitTokens, splitSpec()},
		{connectorRun, connectorSpec()}, {continuousRun, continuousSpec()}, {olderLocksPerBatchRun, olderLocksSpec("batch")},
		{stepsRun, stepsSpec()}, {packagesRun, packagesSpec()},
	}
}

func (k *kit) builds(v specVariant) bool {
	switch v.name {
	case continuousRun:
		return k.can(plugin.CapContinuous)
	case stepsRun:
		return k.can(plugin.CapSteps)
	case packagesRun:
		return k.can(plugin.CapSteps) && k.can(plugin.CapPackages)
	}

	return true
}

const stepsRun = "suites run by drivers"

func stepsSpec() plugin.Spec {
	s := Spec()

	for i := range s.Suites {
		s.Suites[i].Prepare, s.Suites[i].RunSlice, s.Suites[i].Image = "", "", "planner"
		s.Suites[i].Steps = &plugin.SpecSteps{Prepare: i == 0, Drivers: []string{"go"}}
	}

	return s
}

const (
	olderLocksRun         = "an older tool's locks"
	olderLocksPerBatchRun = "an older tool's locks per batch"
)

func olderLocksSpec(per string) plugin.Spec {
	s := Spec()

	for i := range s.Suites {
		s.Suites[i].Lock = &plugin.SpecLock{Name: "lock-" + s.Suites[i].Name, Minutes: 30, Slots: 2, Per: per}
	}

	return s
}

const continuousRun = "a continuous run"

func continuousSpec() plugin.Spec {
	s := Spec()

	for i, r := range s.Runs {
		if r.Trigger.Kind == "timer" {
			s.Runs[i].Trigger = plugin.SpecTrigger{Kind: plugin.TriggerContinuous, Branch: r.Trigger.Branch}
		}
	}

	return s
}

const splitTokens = "tokens by purpose" //nolint:gosec // a variant's name, not a credential

const connectorRun = "a connector's changes"

func connectorSpec() plugin.Spec {
	s := splitSpec()
	s.SSHKeys = map[string]string{plugin.PurposeRead: "ssh_read_key", plugin.PurposeStore: "ssh_store_key"}

	for i, r := range s.Runs {
		if r.Trigger.Kind == plugin.TriggerPullRequests {
			s.Runs[i].Trigger.Connector, s.Runs[i].Trigger.SweepEvery = "git", "1m"
		}
	}

	return s
}

func splitSpec() plugin.Spec {
	s := Spec()
	s.GitHub.Tokens = map[string]string{}

	for _, p := range plugin.Purposes {
		s.GitHub.Tokens[p] = "github_" + p + "_token"
	}

	s.GitHub.TokenVar = s.GitHub.Tokens[plugin.PurposeStore]

	return s
}

// varRef is a var a pipeline references, ((name)); ((.:name)) is a var a
// step loads or an across sets.
var varRef = regexp.MustCompile(`\(\(([^().\s][^()\s]*)\)\)`)

func strayVars(text []byte, s plugin.Spec) string {
	allowed := map[string]bool{s.GitHub.TokenVar: true}
	for _, v := range s.GitHub.Tokens {
		allowed[v] = true
	}

	for _, v := range s.SSHKeys {
		allowed[v] = true
	}

	for _, r := range s.Runs {
		for _, rs := range r.Suites {
			for _, m := range []map[string]string{rs.Secrets.Prepare, rs.Secrets.Batch, rs.Secrets.Whole} {
				for _, v := range m {
					allowed[v] = true
				}
			}
		}
	}

	if s.Images != nil {
		for _, v := range append(varsOf(s.Images.Credentials), varsOf(s.Images.PullCredentials)...) {
			allowed[v] = true
		}
	}

	if planner, ok := s.CI["planner"].(map[string]any); ok {
		if sec, ok := planner["secrets"].(map[string]any); ok {
			for _, v := range sec {
				allowed[fmt.Sprint(v)] = true
			}
		}
	}

	ci, err := json.Marshal(s.CI)
	if err == nil {
		for _, m := range varRef.FindAllSubmatch(ci, -1) {
			allowed[string(m[1])] = true
		}
	}

	var stray []string

	for _, m := range varRef.FindAllSubmatch(text, -1) {
		if v := string(m[1]); !allowed[v] && !slices.Contains(stray, v) {
			stray = append(stray, v)
		}
	}

	if len(stray) == 0 {
		return ""
	}

	return "it references " + strings.Join(stray, ", ")
}

func (k *kit) sbom(ctx context.Context) {
	if k.unexercised() {
		return
	}

	claimed := k.detect(ctx)
	k.recordedClaims(ctx, claimed)

	full, ok := k.units(ctx, nil, claimed)
	if !ok {
		return
	}

	again, ok := k.units(ctx, nil, claimed)
	if ok {
		k.Check("units are the same every time", ifNot(canonical(full) == canonical(again), "two calls answered differently"))
	}

	k.checkUnits(full)
	k.checkComplete(full, claimed)
	k.checkAnchors(full, claimed)
	k.checkPaths(ctx, full, claimed)
	k.cached(ctx, claimed)
	k.checkCopies()
	k.neverExecutes(ctx)

	k.claimedOnly(claimed, full.Sets, func() string {
		other, ok := k.units(ctx, nil, claimed)
		if !ok {
			return "units failed when the files it didn't claim changed"
		}

		return ifNot(canonical(other) == canonical(full), "units changed with files it didn't claim")
	})
}

// claimedOnly runs answer on a fixture copy where every file not claimed or declared as input holds other
// bytes: a changed answer reads beyond the cache's key. Declared inputs must exist or be in absent.
func (k *kit) claimedOnly(claimed []plugin.Claim, sets []plugin.Set, answer func() string) {
	if len(claimed) == 0 || (!k.can(plugin.CallDetect) && len(k.desc.Candidates) == 0) {
		return
	}

	var (
		missing  []string
		declared []string
	)

	for _, set := range sets {
		for _, in := range set.Inputs {
			declared = append(declared, in)

			info, err := os.Stat(filepath.Join(k.o.Tree, filepath.FromSlash(in)))
			if (err != nil || info.IsDir()) && !slices.Contains(set.Absent, in) && !slices.Contains(missing, in) {
				missing = append(missing, in)
			}
		}
	}

	k.Check("a set's inputs are files of the tree", ifNot(len(missing) == 0,
		"no such file, and not listed as absent: "+strings.Join(firstFew(missing), ", ")))

	keep := map[string]bool{kitFile: true}

	for _, in := range declared {
		keep[in] = true
	}

	for _, c := range claimed {
		keep[c.Path] = true

		for _, d := range c.Depends {
			keep[d] = true
		}
	}

	copyDir := k.WorkDir()

	err := filepath.WalkDir(k.o.Tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(k.o.Tree, p) //nolint:errcheck // p is under the tree
		rel = filepath.ToSlash(rel)

		if d.IsDir() {
			if d.Name() == gitDir {
				return filepath.SkipDir
			}

			return os.MkdirAll(filepath.Join(copyDir, rel), 0o750)
		}

		data := []byte("not claimed: the plugin must not read this\n")
		if keep[rel] {
			if data, err = os.ReadFile(p); err != nil { //nolint:gosec // the fixture's own file
				return err //nolint:wrapcheck // reported as the check's problem
			}
		}

		return os.WriteFile(filepath.Join(copyDir, rel), data, 0o600) //nolint:gosec // rel is a path the walk found under the fixture
	})
	if err != nil {
		k.Check("answers depend only on claimed files", "copying the fixture: %v", err)

		return
	}

	tree := k.o.Tree
	k.o.Tree = copyDir

	defer func() { k.o.Tree = tree }()

	k.Check("answers depend only on claimed files", answer())
}

func (k *kit) detect(ctx context.Context) []plugin.Claim {
	if !k.can(plugin.CallDetect) && len(k.desc.Candidates) == 0 {
		return nil
	}

	paths := k.candidates()
	work := k.WorkDir()

	var ans plugin.DetectAnswer

	err := k.call(ctx, plugin.CallDetect, plugin.DetectRequest{Request: k.p.NewRequest(work), Tree: k.o.Tree, Paths: paths}, &ans)
	if err == nil {
		ans.Claims, err = plugin.ReadLines(work, named(ans.Files, "claims"), ans.Claims)
	}

	if err != nil {
		k.Check("detect answers", "%v", err)

		return nil
	}

	var problems []string

	for _, c := range ans.Claims {
		if !slices.Contains(paths, c.Path) {
			problems = append(problems, c.Path+" was not asked about")
		}

		if c.Role == "" {
			problems = append(problems, c.Path+" has no role")
		}
	}

	k.Check("detect claims only what it was asked about, each in a role", strings.Join(problems, "; "))
	k.Check("detect claims something in the fixture", ifNot(len(ans.Claims) > 0, "it claimed nothing"))

	return ans.Claims
}

func (k *kit) candidates() []string {
	var out []string

	_ = filepath.WalkDir(k.o.Tree, func(p string, d fs.DirEntry, err error) error { //nolint:errcheck // an unreadable fixture shows as no candidates
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}

			return nil
		}

		rel, _ := filepath.Rel(k.o.Tree, p) //nolint:errcheck // p is under the tree
		rel = filepath.ToSlash(rel)

		if len(k.desc.Candidates) == 0 || slices.ContainsFunc(k.desc.Candidates, func(g string) bool { return config.MatchGlob(g, rel) }) {
			out = append(out, rel)
		}

		return nil
	})

	sort.Strings(out)

	return out
}

func (k *kit) units(ctx context.Context, paths []string, claimed []plugin.Claim) (plugin.UnitsAnswer, bool) {
	var ans plugin.UnitsAnswer

	work := k.WorkDir()

	req := plugin.TreeRequest{Request: k.p.NewRequest(work), Tree: k.o.Tree, Paths: paths, Claimed: claimed}

	err := k.call(ctx, plugin.CallUnits, req, &ans)
	if err == nil {
		ans.Units, err = plugin.ReadLines(work, named(ans.Files, "units"), ans.Units)
	}

	if err == nil {
		ans.Links, err = plugin.ReadLines(work, named(ans.Files, "links"), ans.Links)
	}

	if err == nil {
		ans.Sets, err = plugin.ReadLines(work, named(ans.Files, "sets"), ans.Sets)
	}

	if err != nil {
		k.Check("units answers", "%v", err)

		return ans, false
	}

	ans.Files = nil

	return ans, true
}

func allUnits(a plugin.UnitsAnswer) ([]plugin.Unit, []plugin.Link) {
	units, links := slices.Clone(a.Units), slices.Clone(a.Links)
	for _, s := range a.Sets {
		units = append(units, s.Units...)
		links = append(links, s.Links...)
	}

	return units, links
}

func (k *kit) checkUnits(a plugin.UnitsAnswer) {
	units, links := allUnits(a)
	ids := map[string]bool{}

	var problems []string

	for _, u := range units {
		switch {
		case ids[u.ID]:
			problems = append(problems, "unit "+u.ID+" twice")
		case !slices.ContainsFunc(k.desc.Namespaces, func(ns string) bool { return strings.HasPrefix(u.ID, ns+":") }):
			problems = append(problems, "unit "+u.ID+" outside the declared namespaces")
		case u.Digest == "" && len(u.DigestFrom) == 0:
			problems = append(problems, "unit "+u.ID+" has no digest")
		}

		ids[u.ID] = true
	}

	for _, u := range units {
		for _, from := range u.DigestFrom {
			if !ids[from] {
				problems = append(problems, "unit "+u.ID+" takes its digest from "+from+", which isn't there")
			}
		}
	}

	problems = append(problems, k.linkProblems(links, ids)...)

	k.Check("units are well formed", strings.Join(firstFew(problems), "; "))
	k.Check("units describe something in the fixture", ifNot(len(units) > 0, "no units"))
}

func (k *kit) linkProblems(links []plugin.Link, ids map[string]bool) []string {
	var problems []string

	for _, l := range links {
		if ids != nil && !ids[l.From] && !strings.HasPrefix(l.From, "file:") && !plugin.CoreOwned(l.From) {
			problems = append(problems, "link from "+l.From+", which isn't a unit")
		}

		// a link may carry another plugin's narrowing: the core checks
		// that some plugin declares it
		if p := l.Problem(); p != "" {
			problems = append(problems, p)
		}
	}

	return problems
}

func (k *kit) checkComplete(a plugin.UnitsAnswer, claimed []plugin.Claim) {
	complete := slices.Clone(a.Complete)
	for _, s := range a.Sets {
		complete = append(complete, s.Complete...)
	}

	if len(complete) == 0 {
		return
	}

	var problems []string

	for _, f := range complete {
		switch {
		case k.desc.Kind != plugin.KindSBOM:
			problems = append(problems, "a "+k.desc.Kind+" says "+f+" is covered whole by units it doesn't list")
		case !slices.ContainsFunc(claimed, func(c plugin.Claim) bool { return c.Path == f }):
			problems = append(problems, f+" is said to be covered whole, but wasn't claimed")
		}
	}

	k.Check("files said to be covered whole are claimed ones", strings.Join(firstFew(problems), "; "))
}

func (k *kit) checkAnchors(a plugin.UnitsAnswer, claimed []plugin.Claim) {
	units, _ := allUnits(a)

	var problems []string

	for _, c := range claimed {
		switch {
		case c.Anchor == "":
		case c.ByUnits == "":
			problems = append(problems, c.Path+" names the anchor "+c.Anchor+" without by_units")
		case !slices.ContainsFunc(units, func(u plugin.Unit) bool { return u.ID == c.Anchor }):
			problems = append(problems, c.Path+" names the anchor "+c.Anchor+", which no unit is")
		}
	}

	k.Check("a claim's anchor is one of the plugin's units", strings.Join(firstFew(problems), "; "))
}

// checkPaths: an answer for some paths agrees with the whole answer, which
// is what makes caching by content safe.
func (k *kit) checkPaths(ctx context.Context, full plugin.UnitsAnswer, claimed []plugin.Claim) {
	whole := map[string]string{}

	units, _ := allUnits(full)
	for _, u := range units {
		whole[u.ID] = canonical(u)
	}

	var paths []string

	if k.desc.Inputs[plugin.CallUnits] == plugin.PerSet {
		for _, s := range full.Sets {
			if len(s.Inputs) > 0 {
				paths = append(paths, s.Inputs[0])
			}
		}
	} else {
		for _, u := range units {
			if u.File != "" && !slices.Contains(paths, u.File) {
				paths = append(paths, u.File)
			}
		}
	}

	var problems []string

	for _, p := range firstFew(paths) {
		part, ok := k.units(ctx, []string{p}, claimed)
		if !ok {
			return
		}

		got, _ := allUnits(part)
		for _, u := range got {
			if whole[u.ID] != canonical(u) {
				problems = append(problems, fmt.Sprintf("asked for %s, unit %s differs from the whole tree's answer", p, u.ID))
			}
		}

		if len(got) == 0 {
			problems = append(problems, "asked for "+p+", no units")
		}
	}

	k.Check("an answer for some paths agrees with the whole tree's", strings.Join(firstFew(problems), "; "))
}

func (k *kit) correlator(ctx context.Context) {
	if k.unexercised() {
		return
	}

	claimed := k.detect(ctx)

	if k.can(plugin.CallLinks) {
		k.links(ctx, claimed)
		k.cached(ctx, claimed)
		k.inputUnits(ctx, k.answerSets(ctx, claimed))
		k.checkCopies()
	} else {
		// the core caches units and links only
		k.Skip(cachedCheck, "it answers no links")
		k.Skip(heldCheck, "it answers no links")
		k.Skip(unitsCheck, "it answers no links")
	}

	if k.can(plugin.CallBind) {
		k.bind(ctx, claimed)
	}

	if k.can(plugin.CallObserve) {
		k.observe(ctx)
	}

	k.recordedClaims(ctx, claimed)

	if k.can(plugin.CallCollect) {
		k.collect(ctx)

		if k.can(plugin.CallObserve) {
			k.parts(ctx)
		}
	}

	k.neverExecutes(ctx)
}

func (k *kit) links(ctx context.Context, claimed []plugin.Claim) {
	var answers [2]plugin.LinksAnswer

	for i := range answers {
		work := k.WorkDir()

		req := plugin.TreeRequest{Request: k.p.NewRequest(work), Tree: k.o.Tree, Claimed: claimed}

		err := k.call(ctx, plugin.CallLinks, req, &answers[i])
		if err == nil {
			answers[i].Links, err = plugin.ReadLines(work, named(answers[i].Files, "links"), answers[i].Links)
		}

		if err == nil {
			answers[i].Sets, err = plugin.ReadLines(work, named(answers[i].Files, "sets"), answers[i].Sets)
		}

		if err != nil {
			k.Check("links answers", "%v", err)

			return
		}

		answers[i].Files = nil
	}

	k.Check("links are the same every time", ifNot(canonical(answers[0]) == canonical(answers[1]), "two calls answered differently"))

	first := answers[0]

	k.claimedOnly(claimed, first.Sets, func() string {
		work := k.WorkDir()

		var other plugin.LinksAnswer

		err := k.call(ctx, plugin.CallLinks, plugin.TreeRequest{Request: k.p.NewRequest(work), Tree: k.o.Tree, Claimed: claimed}, &other)
		if err == nil {
			other.Links, err = plugin.ReadLines(work, named(other.Files, "links"), other.Links)
		}

		if err == nil {
			other.Sets, err = plugin.ReadLines(work, named(other.Files, "sets"), other.Sets)
		}

		if err != nil {
			return "links failed when the files it didn't claim changed: " + err.Error()
		}

		other.Files = nil

		return ifNot(canonical(other) == canonical(first), "links changed with files it didn't claim")
	})

	links := slices.Clone(answers[0].Links)
	for _, s := range answers[0].Sets {
		links = append(links, s.Links...)
	}

	k.Check("links are well formed", strings.Join(firstFew(k.linkProblems(links, nil)), "; "))
}

type bindRecord struct {
	Slice string            `json:"slice"`
	Width string            `json:"width"`
	Units []json.RawMessage `json:"units"`
}

func (k *kit) bind(ctx context.Context, claimed []plugin.Claim) {
	var answers [2]string

	for i := range answers {
		work := k.WorkDir()

		req := map[string]any{
			"protocol": plugin.Protocol, "work": work, "cache": k.p.Cache, "config": k.o.Config,
			"tree": k.o.Tree, "claimed": claimed, "widths": []string{"executed", "dependents"},
			"slices": k.fixtureSlices(),
		}

		var ans plugin.Answer
		if err := k.call(ctx, plugin.CallBind, req, &ans); err != nil {
			k.Check("bind answers", "%v", err)

			return
		}

		var problems []string

		ids, err := plugin.ReadLines[json.RawMessage](work, named(ans.Files, "units"), nil)
		if err != nil {
			problems = append(problems, err.Error())
		}

		records, err := plugin.ReadLines[bindRecord](work, named(ans.Files, "bind"), nil)
		if err != nil {
			problems = append(problems, err.Error())
		}

		for _, r := range records {
			if r.Slice == "" || r.Width == "" {
				problems = append(problems, "a bind record without a slice or a width")
			}
		}

		if _, err := plugin.ReadLines[json.RawMessage](work, named(ans.Files, "unbound"), nil); err != nil {
			problems = append(problems, err.Error())
		}

		k.Check("bind's files are well formed", strings.Join(firstFew(problems), "; "))

		answers[i] = canonical(ids) + canonical(records)
	}

	k.Check("bind is the same every time", ifNot(answers[0] == answers[1], "two calls answered differently"))
}

const kitFile = ".pipelinegen-kit.json"

func (k *kit) fixtureSlices() []any {
	data, err := os.ReadFile(filepath.Join(k.o.Tree, kitFile))
	if err != nil {
		return []any{}
	}

	var f struct {
		Slices []any `json:"slices"`
	}

	if json.Unmarshal(data, &f) != nil || f.Slices == nil {
		return []any{}
	}

	return f.Slices
}

// observe: missing data can't tell, and existing empty data is an empty answer; each runs as the
// observing step does, in a directory holding only the tree and the coverage output.
func (k *kit) observe(ctx context.Context) {
	reads := k.desc.ObserveReadsOutside(k.o.Config)
	outside := make([]string, 0, len(reads))

	for _, r := range reads {
		outside = append(outside, fmt.Sprintf("config.%s = %q is under neither %s/ nor %s/", r.Key, r.Value, plugin.ObserveTree, plugin.ObserveCoverage))
	}

	k.Check("observe reads only what the step that observes sees (the tree and the coverage output)", strings.Join(outside, "; "))

	// dirs[0] is the setup's, the rest the slice's parts
	setup, missing := k.WorkDir(), filepath.Join(k.WorkDir(), "missing")

	err := k.observeCall(ctx, plugin.ObserveRequest{Request: k.request(), Tree: k.o.Tree, Slice: "s", Dirs: []string{setup, missing}}, &plugin.ObserveAnswer{})
	k.Check("observe can't tell from a missing part", ifNot(errors.Is(err, plugin.ErrCantTell), fmt.Sprintf("it answered %v", err)))

	empty := k.WorkDir()

	var ans plugin.ObserveAnswer

	err = k.observeCall(ctx, plugin.ObserveRequest{Request: k.request(), Tree: k.o.Tree, Slice: "s", Dirs: []string{setup, empty}}, &ans)
	k.Check("observe can't tell from an empty directory", ifNot(errors.Is(err, plugin.ErrCantTell), fmt.Sprintf("it answered %v with %d touched: an empty directory is no evidence of a recording", err, len(ans.Touched))))

	k.observeFixture(ctx)
}

type kitObserve struct {
	Slice string   `json:"slice"`
	Dirs  []string `json:"dirs"`
}

// observeFixture: a recording of the fixture's tree is attributed to some of its units; against an
// empty tree it can't be told, but is never an empty answer, which would empty the slice's list.
func (k *kit) observeFixture(ctx context.Context) {
	var f struct {
		Observe *kitObserve `json:"observe"`
	}

	data, err := os.ReadFile(filepath.Join(k.o.Tree, kitFile))
	if err != nil || json.Unmarshal(data, &f) != nil || f.Observe == nil || len(f.Observe.Dirs) < 2 {
		k.Check("observe attributes the fixture's recording", "the fixture names no recording (%s: {\"observe\": {\"slice\", \"dirs\"}}), so observe never saw one", kitFile)

		return
	}

	dirs := make([]string, 0, len(f.Observe.Dirs))
	for _, d := range f.Observe.Dirs {
		dirs = append(dirs, filepath.Join(k.o.Tree, filepath.FromSlash(d)))
	}

	observe := func(tree string, dirs []string) (plugin.ObserveAnswer, error) {
		work := k.WorkDir()

		var ans plugin.ObserveAnswer

		err := k.observeCall(ctx, plugin.ObserveRequest{Request: k.p.NewRequest(work), Tree: tree, Slice: f.Observe.Slice, Dirs: dirs}, &ans)
		if err == nil {
			ans.Touched, err = plugin.ReadLines(work, named(ans.Files, "touched"), ans.Touched)
		}

		return ans, err
	}

	ans, err := observe(k.o.Tree, dirs)

	k.touched = ans.Touched
	if err != nil {
		k.Check("observe attributes the fixture's recording", "%v", err)
	} else {
		k.Check("observe attributes the fixture's recording", ifNot(len(ans.Touched) > 0, "it touched nothing"))
	}

	// the setup is told by its place, first, whatever its name, and may
	// have left nothing: the slice's own parts still count
	noSetup := append([]string{filepath.Join(k.WorkDir(), "not-the-setup-name")}, dirs[1:]...)

	ans, err = observe(k.o.Tree, noSetup)
	if err != nil {
		k.Check("observe takes the first directory as the setup, which may be missing", "%v", err)
	} else {
		k.Check("observe takes the first directory as the setup, which may be missing", ifNot(len(ans.Touched) > 0, "with no setup, the slice's parts touched nothing"))
	}

	ans, err = observe(k.WorkDir(), dirs)

	problem := ""

	switch {
	case errors.Is(err, plugin.ErrCantTell):
	case err != nil:
		problem = err.Error()
	case len(ans.Touched) == 0:
		problem = "it answered an empty list for a recording it couldn't map back to the tree"
	}

	k.Check("observe can't tell from a recording it can't attribute", problem)
}

func (k *kit) collect(ctx context.Context) {
	dir := k.WorkDir()

	var problems []string

	for _, a := range k.desc.Collects {
		if !slices.Contains(plugin.CollectActions, a) {
			problems = append(problems, "describe names the collect action "+a+", which isn't one")
		}
	}

	// only the actions it declares: the core never calls the others
	for _, action := range plugin.CollectActions {
		if !k.desc.Handles(action) {
			continue
		}

		req := plugin.CollectRequest{Request: k.request(), Action: action, Slice: "s", Dir: dir}
		if err := k.call(ctx, plugin.CallCollect, req, nil); err != nil {
			problems = append(problems, action+": "+err.Error())
		}
	}

	k.Check("collect with nothing running succeeds", strings.Join(problems, "; "))
}

func named(files []string, name string) []string {
	var out []string

	for _, f := range files {
		if filepath.Base(f) == name+".jsonl" {
			out = append(out, f)
		}
	}

	return out
}

func canonical(v any) string { return pkit.Canonical(v) }

func ifNot(ok bool, problem string) string {
	if ok {
		return ""
	}

	return problem
}

func firstFew[T any](s []T) []T {
	if len(s) > 5 {
		return s[:5]
	}

	return s
}

// observeCall calls observe as the observing step runs it, in a directory of only the tree and the
// coverage output, so a plugin that reads what another step made fails here as in the pipeline.
func (k *kit) observeCall(ctx context.Context, req plugin.ObserveRequest, answer any) error {
	step := k.WorkDir()
	coverage := filepath.Join(step, plugin.ObserveCoverage)

	if err := os.MkdirAll(coverage, 0o750); err != nil {
		return fmt.Errorf("observe step: %w", err)
	}

	if err := os.Symlink(req.Tree, filepath.Join(step, plugin.ObserveTree)); err != nil {
		return fmt.Errorf("observe step: %w", err)
	}

	dirs := make([]string, 0, len(req.Dirs))

	for i, d := range req.Dirs {
		name := "_setup"
		if i > 0 {
			name = fmt.Sprintf("part-%d", i)
		}

		link := filepath.Join(coverage, name)
		if err := os.Symlink(d, link); err != nil {
			return fmt.Errorf("observe step: %w", err)
		}

		dirs = append(dirs, link)
	}

	req.Dirs = dirs

	p := k.p
	p.Dir = step

	err := p.Call(ctx, plugin.CallObserve, req, answer)
	k.Saw(err, answer)

	return err
}

func absOrEmpty(p string) string {
	if p == "" {
		return ""
	}

	if a, err := filepath.Abs(p); err == nil {
		return a
	}

	return p
}

// recordedClaims: only an observing correlator may mark a claim recorded, each file of it in the recording;
// a claim marked erased is not recorded too, and the companion sbom lists no unit of it that could run.
func (k *kit) recordedClaims(ctx context.Context, claimed []plugin.Claim) {
	var problems, erased []string

	observes := k.desc.Kind == plugin.KindCorrelator && k.can(plugin.CallObserve)

	for _, c := range claimed {
		switch {
		case c.Recorded && c.Erased:
			problems = append(problems, c.Path+" is marked both recorded and erased")
		case (c.Recorded || c.Erased) && !observes:
			problems = append(problems, c.Path+" is marked recorded or erased by a plugin that observes nothing")
		case c.Recorded && !slices.ContainsFunc(k.touched, func(id string) bool { return unitOfFile(id, c.Path) }):
			problems = append(problems, c.Path+" is marked recorded, and the fixture's recording holds no unit of it")
		case c.Erased:
			erased = append(erased, c.Path)
		}
	}

	problems = append(problems, k.erasedRuns(ctx, erased)...)

	k.Check("a claim marked recorded is a file the recording holds", strings.Join(problems, "; "))
}

func (k *kit) erasedRuns(ctx context.Context, files []string) []string {
	if len(files) == 0 {
		return nil
	}

	if k.o.SBOM == "" {
		return []string{"files are marked erased, and no companion sbom (--sbom) can say that they hold no unit that runs"}
	}

	sbom := plugin.Plugin{Name: kitSBOM, Path: k.o.SBOM, Config: k.o.SBOMConfig, Cache: k.p.Cache}
	work := k.WorkDir()

	var ans plugin.UnitsAnswer

	err := sbom.Call(ctx, plugin.CallUnits, plugin.TreeRequest{Request: sbom.NewRequest(work), Tree: k.o.Tree, Paths: files}, &ans)
	if err == nil {
		ans.Units, err = plugin.ReadLines(work, named(ans.Files, "units"), ans.Units)
	}

	if err != nil {
		return []string{"the companion sbom's units of the files marked erased: " + err.Error()}
	}

	units, _ := allUnits(ans)

	var out []string

	for _, u := range units {
		if u.Executable && slices.Contains(files, u.File) {
			out = append(out, u.File+" is marked erased, and the sbom answers "+u.ID+", which could run")
		}
	}

	return out
}

// unitOfFile tells whether a unit id is of a file: <namespace>:<path>,
// whole or followed by its part (#...).
func unitOfFile(id, path string) bool {
	_, rest, ok := strings.Cut(id, ":")

	return ok && (rest == path || strings.HasPrefix(rest, path+"#"))
}
