package proof

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const (
	gitCmd            = "git"
	mainBranch        = "main"
	flagCache         = "--cache"
	flagBazelDiff     = "--bazel-diff"
	levelDependents   = "dependents"
	levelDependentsL3 = "dependents-L3"
	nothing           = "nothing"
	expectNone        = "none"
	expectAll         = "all"
	greetTest         = "app/greet_test.go"
	caseConfig        = "config"
	caseCorrEntry     = "correntry"
	caseSBOMEntry     = "sbomentry"
	pieceSBOM         = "go-sbom"
	pieceCorrelator   = "go-correlator"
)

var (
	errNoChange = errors.New("no change")
	errPlan     = errors.New("a plan failed")
)

var cases = []string{"helper", caseConfig, "start", "initialiser", "unused", "bump", "testfile", "both", "newdecl", "unrun", "signature", "width", "breaks", caseCorrEntry, caseSBOMEntry}

var levels = []string{"L2", "L3", "L4", "L5", levelDependents, levelDependentsL3}

func (p *planner) fx(ctx context.Context, args ...string) error {
	return p.r.shOut(ctx, p.fixture, p.env, p.stdout(), p.stderr(), gitCmd, args...)
}

func (p *planner) closeLog() {
	if c, ok := p.out.(io.Closer); ok {
		_ = c.Close() //nolint:errcheck // a log written whole by now
	}
}

func (p *planner) stdout() io.Writer {
	if p.out != nil {
		return p.out
	}

	return p.r.o.Stdout
}

func (p *planner) stderr() io.Writer {
	if p.out != nil {
		return p.out
	}

	return p.r.o.Stderr
}

func (r *run) shOut(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error {
	if err := r.command(ctx, dir, env, stdout, stderr, name, args...).Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}

	return nil
}

func (p *planner) edit(file string, line func(string) string) error {
	path := filepath.Join(p.fixture, filepath.FromSlash(file))

	data, err := os.ReadFile(path) //nolint:gosec // the fixture's own file
	if err != nil {
		return fmt.Errorf("edit: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	for i := range lines {
		lines[i] = line(lines[i])
	}

	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil { //nolint:gosec // a fixture's source file
		return fmt.Errorf("edit: %w", err)
	}

	return nil
}

func sub(old, repl string) func(string) string {
	return func(l string) string { return strings.Replace(l, old, repl, 1) }
}

func whole(old, repl string) func(string) string {
	return func(l string) string {
		if l == old {
			return repl
		}

		return l
	}
}

func both(fs ...func(string) string) func(string) string {
	return func(l string) string {
		for _, f := range fs {
			l = f(l)
		}

		return l
	}
}

func (p *planner) entryChange(name string) error {
	piece := map[string]string{caseCorrEntry: pieceCorrelator, caseSBOMEntry: pieceSBOM}[name]
	v := p.r.pins[piece].version

	return p.edit("pipelinegen.yaml", sub("version: "+v+",", "version: "+v+"-entry,"))
}

func (p *planner) change(ctx context.Context, name string) error {
	switch name {
	case "helper":
		return p.edit("app/price.go", sub("total - total/10", "total - total/5"))
	case caseConfig:
		return p.edit("app/config.go", both(whole("\tPort int", "\tPort int\n\tHost string"), sub("defaultPort = 8080", "defaultPort = 8081")))
	case "start":
		return p.edit("app/start.go", sub("Config{Name: name, Port: defaultPort}", `Config{Name: "app-" + name, Port: defaultPort}`))
	case "initialiser":
		return p.edit("app/registry.go", whole("var registry = load()", `var registry = append(load(), "role")`))
	case "unused":
		return p.edit("unused/unused.go", sub("return string(r)", `return string(r) + ""`))
	case "bump":
		if err := p.r.shOut(ctx, p.fixture, p.env, io.Discard, io.Discard, "go", "get", "github.com/google/go-cmp@v0.7.0"); err != nil {
			return err
		}

		return p.r.shOut(ctx, p.fixture, p.env, p.stdout(), p.stderr(), "go", "mod", "download", expectAll)
	case "testfile":
		return p.edit(greetTest, sub(`Start("greet")`, `Start("greeting")`))
	case "both":
		if err := p.change(ctx, "helper"); err != nil {
			return err
		}

		return p.change(ctx, "start")
	case "unrun":
		return p.edit("app/price.go", sub("return n / 10 * 10", "return n / 5 * 5"))
	case "signature":
		return p.edit("app/price.go", whole("type Opt int", "type Opt int64"))
	case "width":
		return p.edit("limits/limits.go", whole("const Width = 1000", "const Width = 900"))
	case "breaks":
		// a change to Opt that breaks the build: hook(0) no longer compiles
		return p.edit("app/price.go", whole("type Opt int", "type Opt string"))
	case caseCorrEntry, caseSBOMEntry:
		return p.entryChange(name)
	case "newdecl":
		return writeFile(filepath.Join(p.fixture, "app", "shout.go"), newDecl)
	case "selfdrop":
		if err := p.edit("pipelinegen.yaml", whole(`    tests: ["**/*_test.go"]`, "    tests: [\"**/*_test.go\"]\n    exclude: [\"app/greet_test.go\"]")); err != nil {
			return err
		}

		return p.edit("app/greet.go", sub(`"hello, "`, `"hello there, "`))
	case "moved":
		return p.edit("app/greet.go", sub(`"hello, "`, `"hi, "`))
	}

	return fmt.Errorf("%w %q", errNoChange, name)
}

const newDecl = `package app

import "strings"

// Shout says something loudly. Nothing calls it yet.
func Shout(s string) string {
	return strings.ToUpper(s) + "!"
}
`

func writeFile(path, text string) error {
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil { //nolint:gosec // a fixture's source file
		return fmt.Errorf("write: %w", err)
	}

	return nil
}

func (p *planner) commitChange(ctx context.Context, name, what string) error {
	if err := p.fx(ctx, "checkout", "-q", "-B", "change/"+name, p.r.c0); err != nil {
		return err
	}

	if err := p.change(ctx, what); err != nil {
		return err
	}

	if err := p.fx(ctx, "add", "-A"); err != nil {
		return err
	}

	return p.fx(ctx, "commit", "-qm", name)
}

var settings = []struct{ name, narrowing string }{
	{"L3", "narrowing: 3"},
	{"L4", "narrowing: 4"},
	{"L5", "narrowing: 5"},
	{levelDependents, "narrowing: 2\nfollow: dependents"},
	{levelDependentsL3, "narrowing: 3\nfollow: dependents"},
}

func (p *planner) setting(ctx context.Context, name, narrowing string) error {
	if err := p.fx(ctx, "checkout", "-q", "-B", "setting/"+name, p.r.c0); err != nil {
		return err
	}

	if err := p.edit("pipelinegen.yaml", whole("narrowing: 2", narrowing)); err != nil {
		return err
	}

	return p.fx(ctx, "commit", "-qam", name)
}

func (p *planner) on(ctx context.Context, setting, change string) (string, error) {
	branch := "on/" + setting + "/" + change

	if err := p.fx(ctx, "checkout", "-q", "-B", branch, "setting/"+setting); err != nil {
		return "", err
	}

	if err := p.r.shOut(ctx, p.fixture, p.env, io.Discard, p.stderr(), gitCmd, "cherry-pick", "--allow-empty", "-x", "change/"+change); err != nil {
		return "", err
	}

	return branch, nil
}

// person runs the proof's plans as a person's commands, without PIPELINEGEN_JOB_URL: with it bazelisk's
// downloads would go into each cache and a plan would read a pipeline step's environment.
func (p *planner) person() []string { return unsetEnv(p.env, "PIPELINEGEN_JOB_URL") }

func (p *planner) toFiles(ctx context.Context, outFile, errFile string, name string, args ...string) (int, error) {
	o, err := os.Create(outFile) //nolint:gosec // in the proof's work directory
	if err != nil {
		return 0, fmt.Errorf("plan: %w", err)
	}

	defer func() { _ = o.Close() }() //nolint:errcheck // written whole by now

	e, err := os.Create(errFile) //nolint:gosec // in the proof's work directory
	if err != nil {
		return 0, fmt.Errorf("plan: %w", err)
	}

	defer func() { _ = e.Close() }() //nolint:errcheck // written whole by now

	runErr := p.r.command(ctx, p.fixture, p.person(), o, e, name, args...).Run()

	code, ok := exitCode(runErr)
	if !ok {
		return 0, fmt.Errorf("%s: %w", name, runErr)
	}

	return code, nil
}

func (p *planner) affectedArgs(cache string, bd []string, rest ...string) []string {
	args := append([]string{"affected", "-root", p.fixture, flagCache, cache, "--plugins", p.r.plugins}, bd...)

	return append(args, rest...)
}

func (p *planner) plan(ctx context.Context, label, base, head string, why bool) error {
	out := filepath.Join(p.r.work, "plans", label)

	if err := p.fx(ctx, "checkout", "-q", "--detach", head); err != nil {
		return err
	}

	code, err := p.toFiles(ctx, out+".json", out+".err", p.r.tool, p.affectedArgs(p.cache, p.r.bd, "--base", base, "--head", head, "--json")...)
	if err != nil {
		return err
	}

	if code != 0 {
		return fmt.Errorf("%w: affected %s: exit status %d%s", errPlan, label, code, lastLines(out+".err", 3))
	}

	if err := p.walkplan(ctx, label, base, head, ""); err != nil {
		return err
	}

	if err := p.fx(ctx, "checkout", "-q", "--detach", head); err != nil {
		return err
	}

	if !why {
		return nil
	}

	pl := p.r.readPlan(out + ".json")

	var text strings.Builder

	for _, s := range []string{pl.FirstSelected(), pl.FirstUntouched()} {
		if s == "" {
			continue
		}

		var got strings.Builder

		args := append([]string{"why", "-root", p.fixture, flagCache, p.cache, "--plugins", p.r.plugins}, p.r.bd...)
		if err := p.r.command(ctx, p.fixture, p.person(), &got, &got, p.r.tool, append(args, "--slice", s, "--base", base, "--head", head)...).Run(); err != nil {
			return fmt.Errorf("why %s %s: %w", label, s, err)
		}

		for line := range strings.SplitSeq(strings.TrimSuffix(got.String(), "\n"), "\n") {
			if strings.HasPrefix(line, "plugins:") || strings.HasPrefix(line, "  go-") {
				continue
			}

			text.WriteString(line + "\n")
		}
	}

	return writeFile(out+".why", text.String())
}

// walkplan plans the same by the walk on a cache of its own, so neither tool reads what the other stored,
// and records whether it selects and leaves out what the plan did.
func (p *planner) walkplan(ctx context.Context, label, base, head, differ string) error {
	out := filepath.Join(p.r.work, "plans", label)

	if !p.r.walking {
		return writeFile(out+".walk", "not compared\n")
	}

	wcache := p.cache + ".walk"
	if err := os.MkdirAll(wcache, 0o750); err != nil {
		return fmt.Errorf("walk: %w", err)
	}

	if _, err := os.Lstat(filepath.Join(wcache, "caches")); err != nil {
		if err := os.Symlink(filepath.Join(p.cache, "caches"), filepath.Join(wcache, "caches")); err != nil {
			return fmt.Errorf("walk: %w", err)
		}
	}

	bd := p.r.bd
	if differ != "" {
		bd = []string{flagBazelDiff, differ}
	}

	if err := p.fx(ctx, "checkout", "-q", "--detach", head); err != nil {
		return err
	}

	code, err := p.toFiles(ctx, out+".walk.json", out+".walk.err", p.r.walk, p.affectedArgs(wcache, bd, "--base", base, "--head", head, "--json")...)
	if err != nil {
		return err
	}

	if err := writeFile(out+".walk.exit", fmt.Sprintf("%d\n", code)); err != nil {
		return err
	}

	return writeFile(out+".walk", p.r.walkverdict(label)+"\n")
}

func nonEmpty(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Size() > 0
}

func readText(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // in the proof's work directory
	if err != nil {
		return ""
	}

	return strings.TrimRight(string(data), "\n")
}

func (r *run) readPlan(file string) *Plan {
	p, err := ReadPlan(file)
	if err != nil {
		r.mu.Lock()
		_, _ = fmt.Fprintln(r.o.Stderr, "kit:", err) //nolint:errcheck // the log is for people
		r.mu.Unlock()

		return &Plan{}
	}

	return p
}

func (r *run) mark(tally string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	f, err := os.OpenFile(filepath.Join(r.work, tally), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // in the proof's work directory
	if err != nil {
		return
	}

	_, _ = f.WriteString("x\n") //nolint:errcheck // a tally
	_ = f.Close()               //nolint:errcheck // a tally
}

var trailingSemicolon = regexp.MustCompile(`; *$`)

func sortedLines(p *Plan) string {
	narrowed := p.Narrowed()
	lines := make([]string, 0, len(narrowed))

	for _, n := range narrowed {
		lines = append(lines, n.Line())
	}

	sort.Strings(lines)

	return strings.Join(lines, "\n")
}

func (r *run) walkverdict(label string) string {
	f := filepath.Join(r.work, "plans", label)

	if !nonEmpty(f+".json") || !nonEmpty(f+".walk.json") {
		if !nonEmpty(f+".json") && !nonEmpty(f+".walk.json") && readText(f+".walk.exit") != "0" {
			return "same: no plan either way"
		}

		r.mark("walkfailed")

		return "DIFFERS: one tool made no plan (walk exit " + readText(f+".walk.exit") + ")"
	}

	plan, walk := r.readPlan(f+".json"), r.readPlan(f+".walk.json")

	if a, b := plan.Selected(), walk.Selected(); a != b {
		r.mark("walkfailed")

		if b == "" {
			b = nothing
		}

		return "DIFFERS: binding forward selects " + b
	}

	if a, b := sortedLines(plan), sortedLines(walk); a != b {
		r.mark("walkfailed")

		b = strings.NewReplacer("\t", " ", "\n", ";").Replace(b)

		return "DIFFERS: binding forward leaves out " + trailingSemicolon.ReplaceAllString(b, "")
	}

	return "same"
}

// expected is what each plan should select: exact:<slices>, none, all, sample:<n>:<narrowing> (n sampled
// by it), or own+sample:<slices>:<n>:<narrowing> (those, plus at most n sampled).
func expected(label string) string {
	c, level, _ := strings.Cut(label, "-")
	is := func(cs ...string) bool { return slices.Contains(cs, c) }

	switch {
	case is("helper"):
		return "exact:app/price_test.go"
	case is("unrun") && (level == "L2" || level == "L3"):
		return expectNone
	// go-correlator binds the types a bound declaration names: hook is
	// bound (Total calls it), so Opt, in its signature, is too
	case is("unrun", "signature"):
		return "exact:app/price_test.go"
	case is("breaks"):
		return expectAll
	case is(caseCorrEntry, caseSBOMEntry):
		return entryExpected(level)
	case is("width") && (level == "L2" || level == "L3"):
		return expectNone
	case is("width"):
		return "exact:report/report_test.go"
	case (strings.HasSuffix(label, "-L3") || strings.HasSuffix(label, "-L5")) && is(caseConfig, "start", "initialiser", "both"):
		return expectAll
	case label == "bump-L5":
		return expectAll
	case is(caseConfig, "initialiser"):
		return "sample:1:sample-bound"
	case is("start"):
		return "sample:1:sample"
	case is("unused"):
		return expectNone
	case is("bump"):
		return "exact:report/report_test.go"
	case is("testfile"):
		return "exact:app/greet_test.go"
	case is("both"):
		return "own+sample:app/price_test.go:1:sample"
	case label == "newdecl-L2" || label == "newdecl-dependents":
		return expectNone
	case is("newdecl"):
		return expectAll
	}

	return ""
}

// entryExpected is what a changed plugin entry selects: below level 4 the suite's sample, with the reached
// slices named under plugin-entry; from level 4 what the entry reaches, on its own.
func entryExpected(level string) string {
	if level == "L4" || level == "L5" {
		return expectAll
	}

	return "named:1:plugin-entry"
}

var knownSelections []string

var knownDropped []string

var knownChecks []string

func (r *run) verdict(file, expectation, label string) string {
	p := r.readPlan(file)
	got := p.Selected()
	n := p.Count()

	parts := strings.SplitN(expectation, ":", 4)
	for len(parts) < 4 {
		parts = append(parts, "")
	}

	kind, a, b, c := parts[0], parts[1], parts[2], parts[3]

	var ok bool

	switch kind {
	case "exact":
		ok = got == a
	case expectNone:
		ok = got == ""
	case expectAll:
		ok = n == p.Total()
	case "sample":
		ok = strconv.Itoa(n) == a && p.Sampled(b) > 0
	case "named":
		ok = strconv.Itoa(n) == a && p.Named(b) > 0
	case "own+sample":
		most, _ := strconv.Atoi(b) //nolint:errcheck // the table's own number

		ok = strings.Contains(","+got+",", ","+a+",") && n <= 1+most && p.Sampled(c) > 0
	}

	known := slices.Contains(knownSelections, label)

	switch {
	case ok && known:
		return "as expected, though listed as known: drop it from the list"
	case ok:
		return "as expected"
	case known:
		return "KNOWN WRONG: expected " + expectation
	}

	r.mark("failed")

	return "DIFFERS: expected " + expectation
}

func minus(a, b string) string {
	drop := map[string]bool{}
	for s := range strings.SplitSeq(b, ",") {
		drop[s] = true
	}

	var out []string

	for s := range strings.SplitSeq(a, ",") {
		if s != "" && !drop[s] {
			out = append(out, s)
		}
	}

	return strings.Join(out, ",")
}

func (r *run) dropped(file string) string {
	label := strings.TrimSuffix(filepath.Base(file), ".json")

	width := ""
	if strings.Contains(label, "-dependents") {
		width = "dependents-"
	}

	c, _, _ := strings.Cut(label, "-")
	p := r.readPlan(file)

	var out strings.Builder

	for _, n := range p.Narrowed() {
		if n.Name == "" {
			continue
		}

		other := filepath.Join(r.work, "plans", fmt.Sprintf("%s-L%d.json", c, n.OffAt))

		switch n.OffAt {
		case 3:
			other = filepath.Join(r.work, "plans", c+"-"+width+"L3.json")
		case 4:
			// no plan is made at level 4: level 5 switches off what 4 does
			// too
			other = filepath.Join(r.work, "plans", c+"-L5.json")
		}

		want := minus(r.readPlan(other).Selected(), p.Selected())
		known := slices.Contains(knownDropped, n.Name)

		switch {
		case n.Dropped == want && known:
			out.WriteString(n.Name + ": holds, though listed as known: drop it from the list; ")
		case n.Dropped == want:
			out.WriteString(n.Name + ": holds; ")
		case known:
			fmt.Fprintf(&out, "%s: KNOWN WRONG (says %s, level %d adds %s); ", n.Name, or(n.Dropped, expectNone), n.OffAt, or(want, expectNone))
		default:
			fmt.Fprintf(&out, "%s: WRONG (says %s, level %d adds %s); ", n.Name, or(n.Dropped, expectNone), n.OffAt, or(want, expectNone))
			r.mark("failed")
		}
	}

	return or(out.String(), "-")
}

func or(s, otherwise string) string {
	if s == "" {
		return otherwise
	}

	return s
}

func lastLines(path string, n int) string {
	b, err := os.ReadFile(path) //nolint:gosec // the proof's own work file
	if err != nil {
		return ""
	}

	var keep []string

	for l := range strings.SplitSeq(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			keep = append(keep, l)
		}
	}

	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}

	if len(keep) == 0 {
		return ""
	}

	return "\n  " + strings.Join(keep, "\n  ")
}
