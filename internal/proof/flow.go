package proof

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func exitCode(err error) (int, bool) {
	if err == nil {
		return 0, true
	}

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return 0, false
	}

	if code := exit.ExitCode(); code >= 0 {
		return code, true
	}

	if ws, ok := exit.Sys().(syscall.WaitStatus); ok {
		return 128 + int(ws.Signal()), true
	}

	return -1, true
}

func (r *run) main() *planner {
	return &planner{r: r, fixture: filepath.Join(r.work, "fixture"), cache: filepath.Join(r.work, "cache"), env: r.env}
}

// record plans every slice of the commit under run "full", records each
// batch through the correlator as a CI worker would, and attaches the lists.
// FIXTURE_LOSE_COVERAGE names a slice whose coverage is lost;
// FIXTURE_SYMLINK_SOURCE gives the batches their tree as a symlink.
func (r *run) record(ctx context.Context, name, commit, cache string, env []string) error {
	rec := filepath.Join(r.work, name)
	gitURL := strings.TrimPrefix(envOf(env, "PIPELINEGEN_GIT_URL"), "file://")

	steps := [][]string{
		{gitCmd, "clone", "-q", filepath.Join(gitURL, "example", "fixture.git"), filepath.Join(rec, "source")},
		{gitCmd, "-C", filepath.Join(rec, "source"), "checkout", "-q", commit},
		{"mkdir", "-p", filepath.Join(rec, "tools")},
		{"cp", r.tool, filepath.Join(rec, "tools", "pipelinegen")},
		{"cp", "-r", r.plugins, filepath.Join(rec, "tools", "plugins")},
	}

	for _, s := range steps {
		if err := r.sh(ctx, r.work, env, s[0], s[1:]...); err != nil {
			return err
		}
	}

	env = setEnv(env, "PIPELINEGEN_RUN=full", "PIPELINEGEN_CHANGE=main", "PIPELINEGEN_COMMIT="+commit, "PIPELINEGEN_SUITE=go")
	tool := filepath.Join("tools", "pipelinegen")

	if err := r.sh(ctx, rec, env, tool, append([]string{"plan", "--run", "full", "--trigger", "source", flagCache, cache}, r.bd...)...); err != nil {
		return err
	}

	if err := r.sh(ctx, rec, env, tool, "batches", "--run", "full", "--suite", "go", "--trigger", "source", "-root", "source", "--out", "batches"); err != nil {
		return err
	}

	batches, err := filepath.Glob(filepath.Join(rec, "batches", "*", ""))
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}

	for _, b := range batches {
		if info, err := os.Stat(b); err != nil || !info.IsDir() {
			continue
		}

		if err := r.recordBatch(ctx, rec, filepath.Base(b), env); err != nil {
			return err
		}
	}

	if err := r.sh(ctx, rec, env, tool, "lists", "attach", "--run", "full", "--change", mainBranch, "--commit", commit, "--to", commit, "-root", "source"); err != nil {
		return err
	}

	return r.sh(ctx, rec, env, tool, "lists", "show", "--commit", commit, "-root", "source")
}

func (r *run) recordBatch(ctx context.Context, rec, n string, env []string) error {
	dir := filepath.Join(rec, "batch-"+n)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("record: %w", err)
	}

	if envOf(env, "FIXTURE_SYMLINK_SOURCE") != "" {
		if err := r.sh(ctx, rec, env, "cp", "-a", "batches", "tools", dir+"/"); err != nil {
			return err
		}

		if err := os.Symlink(filepath.Join("..", "source"), filepath.Join(dir, "source")); err != nil {
			return fmt.Errorf("record: %w", err)
		}
	} else if err := r.sh(ctx, rec, env, "cp", "-a", "source", "batches", "tools", dir+"/"); err != nil {
		return err
	}

	env = setEnv(env, "PIPELINEGEN_BATCH="+n)

	if err := r.sh(ctx, dir, env, "sh", "source/ci/go-batch.sh"); err != nil {
		return err
	}

	return r.sh(ctx, dir, env, filepath.Join("tools", "pipelinegen"), "slice", "flush")
}

func envOf(env []string, name string) string {
	for _, kv := range slices.Backward(env) {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v
		}
	}

	return ""
}

type job struct {
	label, base, head string
	why               bool
}

func (j job) line() string {
	s := j.label + " " + j.base + " " + j.head
	if j.why {
		s += " why"
	}

	return s + "\n"
}

func (r *run) prove(ctx context.Context) error {
	m := r.main()

	if err := r.record(ctx, "record", r.c0, m.cache, r.env); err != nil {
		return err
	}

	r.logf("recorded")

	for _, c := range cases {
		if err := m.commitChange(ctx, c, c); err != nil {
			return err
		}
	}

	for _, s := range settings {
		if err := m.setting(ctx, s.name, s.narrowing); err != nil {
			return err
		}
	}

	jobs, err := r.jobs(ctx, m)
	if err != nil {
		return err
	}

	if err := r.planAll(ctx, m, jobs); err != nil {
		return err
	}

	r.logf("planned, by %d workers", r.o.Workers)

	if err := r.table(); err != nil {
		return err
	}

	moved, err := r.safety(ctx, m)
	if err != nil {
		return err
	}

	if err := r.safetyTable(moved); err != nil {
		return err
	}

	return r.summary()
}

// jobs are every plan of every case, with their branches made up front: the
// fixture's repository is only read while the workers plan.
func (r *run) jobs(ctx context.Context, m *planner) ([]job, error) {
	var (
		out  []job
		text strings.Builder
	)

	for _, c := range cases {
		out = append(out, job{c + "-L2", r.c0, "change/" + c, true})

		// the levels that switch the narrowings off come last, for the
		// check of what each narrowing left out
		for _, s := range []struct {
			setting string
			why     bool
		}{{"L3", true}, {levelDependents, true}, {"L5", false}, {"L4", false}, {levelDependentsL3, false}} {
			// a plugin's entry alone at the level that switches
			// plugin-entry off
			if s.setting == "L4" && !strings.HasSuffix(c, "entry") {
				continue
			}

			branch, err := m.on(ctx, s.setting, c)
			if err != nil {
				return nil, err
			}

			out = append(out, job{c + "-" + s.setting, "setting/" + s.setting, branch, s.why})
		}
	}

	for _, j := range out {
		text.WriteString(j.line())
	}

	return out, writeFile(filepath.Join(r.work, "jobs"), text.String())
}

// planAll plans the first job, which warms the cache; each worker then plans
// every Nth of the rest in a worktree of the fixture and a copy of that
// cache of its own. A worker that fails stops only itself, and the run then
// fails with each failed worker's log.
func (r *run) planAll(ctx context.Context, m *planner, jobs []job) error {
	if err := m.plan(ctx, jobs[0].label, jobs[0].base, jobs[0].head, jobs[0].why); err != nil {
		return err
	}

	// a branch checked out in one worktree can't be in another
	if err := m.fx(ctx, "checkout", "-q", "--detach"); err != nil {
		return err
	}

	workers := make([]*planner, r.o.Workers)

	for w := range workers {
		p, err := r.worker(ctx, m, w+1)
		if err != nil {
			return err
		}

		workers[w] = p
	}

	errs := make([]error, len(workers))

	var wg sync.WaitGroup

	for w, p := range workers {
		wg.Go(func() {
			defer p.closeLog()

			for i, j := range jobs[1:] {
				if i%len(workers) != w {
					continue
				}

				if err := p.plan(ctx, j.label, j.base, j.head, j.why); err != nil {
					_, _ = fmt.Fprintln(p.out, err) //nolint:errcheck // the worker's own log
					errs[w] = err

					return
				}
			}
		})
	}

	wg.Wait()

	broken := false

	for w, err := range errs {
		if err == nil {
			continue
		}

		broken = true

		_, _ = fmt.Fprintf(r.o.Stderr, "worker %d failed:\n", w+1) //nolint:errcheck // the log is for people

		if data, err := os.ReadFile(filepath.Join(r.work, fmt.Sprintf("w%d.log", w+1))); err == nil {
			_, _ = r.o.Stderr.Write(data) //nolint:errcheck // the log is for people
		}
	}

	if broken {
		return ErrDiffers
	}

	return nil
}

func (r *run) worker(ctx context.Context, m *planner, w int) (*planner, error) {
	wd := filepath.Join(r.work, fmt.Sprintf("w%d", w))

	for _, d := range []string{"tmp", "gotmp"} {
		if err := os.MkdirAll(filepath.Join(wd, d), 0o750); err != nil {
			return nil, fmt.Errorf("worker: %w", err)
		}
	}

	if err := m.fx(ctx, "worktree", "add", "-q", "--detach", filepath.Join(wd, "fixture"), r.c0); err != nil {
		return nil, err
	}

	for _, suffix := range []string{"", ".walk"} {
		c := m.cache + suffix

		if info, err := os.Stat(c); err != nil || !info.IsDir() {
			continue
		}

		dest := filepath.Join(wd, "cache"+suffix)
		if err := os.MkdirAll(dest, 0o750); err != nil {
			return nil, fmt.Errorf("worker: %w", err)
		}

		entries, err := os.ReadDir(c)
		if err != nil {
			return nil, fmt.Errorf("worker: %w", err)
		}

		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") || e.Name() == "caches" || e.Name() == "tree" {
				continue
			}

			if err := r.sh(ctx, r.work, r.env, "cp", "-a", filepath.Join(c, e.Name()), dest+"/"); err != nil {
				return nil, err
			}
		}

		if err := os.Symlink(filepath.Join(m.cache, "caches"), filepath.Join(dest, "caches")); err != nil {
			return nil, fmt.Errorf("worker: %w", err)
		}
	}

	log, err := os.Create(filepath.Join(r.work, fmt.Sprintf("w%d.log", w)))
	if err != nil {
		return nil, fmt.Errorf("worker: %w", err)
	}

	env := setEnv(r.env, "TMPDIR="+filepath.Join(wd, "tmp"), "GOTMPDIR="+filepath.Join(wd, "gotmp"))

	return &planner{r: r, fixture: filepath.Join(wd, "fixture"), cache: filepath.Join(wd, "cache"), env: env, out: log}, nil
}

func (r *run) table() error {
	var t strings.Builder

	t.WriteString("| Plan | Selected | Narrowed | Warnings | Selection | Dropped matches its level | Walk |\n")
	t.WriteString("|---|---|---|---|---|---|---|\n")

	for _, c := range cases {
		for _, l := range levels {
			label := c + "-" + l
			f := filepath.Join(r.work, "plans", label+".json")

			if l == "L4" && !exists(f) {
				continue
			}

			p := r.readPlan(f)

			fmt.Fprintf(&t, "| %s | %s (of %d) | %s | %d | %s | %s | %s |\n", label, p.Shown(), p.Total(), p.NarrowedShown(), len(p.Warnings()),
				r.verdict(f, expected(label), label), r.dropped(f), readText(strings.TrimSuffix(f, ".json")+".walk"))
		}
	}

	if _, err := io.WriteString(r.o.Stdout, t.String()); err != nil {
		return fmt.Errorf("table: %w", err)
	}

	return writeFile(filepath.Join(r.work, "table.md"), t.String())
}

type movedResult struct{ cached int }

func (r *run) safety(ctx context.Context, m *planner) (movedResult, error) {
	var res movedResult

	// Safety 1: a slice whose coverage is gone can't be told; it is bound
	// to everything, with a warning, until a recording tells again.
	c1, err := r.untold(ctx, m)
	if err != nil {
		return res, err
	}

	_ = c1

	// Safety 2: a change that edits the config can't deselect its own
	// tests: it drops app/greet_test.go from the suite and changes Greet,
	// which the greet and report tests ran. Then the same where the config
	// names itself under everything_on.
	if err := m.commitChange(ctx, "selfdrop", "selfdrop"); err != nil {
		return res, err
	}

	if err := m.plan(ctx, "safety-config", r.c0, "change/selfdrop", true); err != nil {
		return res, err
	}

	if err := m.setting(ctx, "guard", "narrowing: 2\neverything_on: [pipelinegen.yaml]"); err != nil {
		return res, err
	}

	branch, err := m.on(ctx, "guard", "selfdrop")
	if err != nil {
		return res, err
	}

	if err := m.plan(ctx, "safety-config-guarded", "setting/guard", branch, true); err != nil {
		return res, err
	}

	// Safety 2b: a change of the config itself is planned by what it
	// means: a step's slices, a driver's, nothing for a comment, and the
	// config as a changed file where a part isn't understood.
	if err := r.configRows(ctx, m); err != nil {
		return res, err
	}

	// Safety 3: a plan waits while another holds the cache, and goes on as
	// soon as the holder is gone, even when it dies holding it.
	if err := r.lockWaits(ctx, m); err != nil {
		return res, err
	}

	// Safety 4: a work tree changed underneath the plan while bazel-diff
	// hashed it fails the plan and caches nothing of it; the next plan is
	// right.
	if res.cached, err = r.movedTree(ctx, m); err != nil {
		return res, err
	}

	// Safety 5: a recording that says nothing about a slice is never an
	// empty list: the price slice keeps its units, or is marked and the
	// plan warns, and a change to the helper selects it. The batches get
	// their tree as a symlink; then the price slice's coverage names a
	// module the tree doesn't hold; then its tests run and its coverage
	// counts nothing.
	for _, a := range []struct{ name, setting, message string }{
		{"symlink", "FIXTURE_SYMLINK_SOURCE=1", "a link"},
		{"foreign", "FIXTURE_FOREIGN_COVERAGE=app/price_test.go", "another module"},
		{nothing, "FIXTURE_ZERO_COVERAGE=app/price_test.go", "nothing counted"},
	} {
		if err := r.alone(ctx, m, a.name, a.setting, a.message); err != nil {
			return res, err
		}
	}

	return res, nil
}

func (r *run) untold(ctx context.Context, m *planner) (string, error) {
	if err := m.fx(ctx, "checkout", "-q", mainBranch); err != nil {
		return "", err
	}

	if err := m.edit("unused/unused.go", sub("turns a string around", "turns a string back to front")); err != nil {
		return "", err
	}

	if err := m.fx(ctx, "commit", "-qam", "a comment"); err != nil {
		return "", err
	}

	c1, err := r.output(ctx, m.fixture, m.env, gitCmd, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}

	if err := m.fx(ctx, "push", "-q", "origin", mainBranch); err != nil {
		return "", err
	}

	if err := r.record(ctx, "record-untold", c1, m.cache, setEnv(r.env, "FIXTURE_LOSE_COVERAGE=app/greet_test.go")); err != nil {
		return "", err
	}

	if err := m.fx(ctx, "checkout", "-q", "-B", "change/untold", c1); err != nil {
		return "", err
	}

	if err := m.change(ctx, "helper"); err != nil {
		return "", err
	}

	if err := m.fx(ctx, "commit", "-qam", "untold"); err != nil {
		return "", err
	}

	return c1, m.plan(ctx, "safety-untold", c1, "change/untold", true)
}

func (r *run) lockplan(ctx context.Context, m *planner, name string) (*exec.Cmd, error) {
	out := filepath.Join(r.work, "plans", name)

	o, err := os.Create(out + ".json") //nolint:gosec // in the proof's work directory
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}

	e, err := os.Create(out + ".err") //nolint:gosec // in the proof's work directory
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}

	cmd := r.command(ctx, m.fixture, m.person(), o, e, r.tool, m.affectedArgs(m.cache, r.bd, "--base", r.c0, "--head", "change/waits", "--json")...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}

	return cmd, nil
}

func (r *run) holder(ctx context.Context, m *planner, seconds string) (*exec.Cmd, error) {
	cmd := r.command(ctx, m.fixture, r.env, r.o.Stdout, r.o.Stderr, "flock", "-o", filepath.Join(m.cache, "lock"), "sleep", seconds)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}

	return cmd, nil
}

func (r *run) tee(file, line string) error {
	if _, err := io.WriteString(r.o.Stdout, line+"\n"); err != nil {
		return fmt.Errorf("tee: %w", err)
	}

	return writeFile(file, line+"\n")
}

func (r *run) lockWaits(ctx context.Context, m *planner) error {
	if err := m.commitChange(ctx, "waits", "testfile"); err != nil {
		return err
	}

	if err := m.fx(ctx, "checkout", "-q", "change/waits"); err != nil {
		return err
	}

	holder, err := r.holder(ctx, m, "15")
	if err != nil {
		return err
	}

	time.Sleep(time.Second)

	planner, err := r.lockplan(ctx, m, "safety-lock-wait")
	if err != nil {
		return err
	}

	_ = holder.Wait() //nolint:errcheck // only that it let go counts
	freed := time.Now()

	if err := planner.Wait(); err != nil {
		return fmt.Errorf("lock: %w", err)
	}

	ended := time.Now()

	if err := r.tee(filepath.Join(r.work, "plans", "safety-lock-wait.time"),
		fmt.Sprintf("a plan started behind a 15 s holder ended %.1f s after the holder let go", ended.Sub(freed).Seconds())); err != nil {
		return err
	}

	if err := r.lockDied(ctx, m); err != nil {
		return err
	}

	if err := m.walkplan(ctx, "safety-lock-wait", r.c0, "change/waits", ""); err != nil {
		return err
	}

	return m.walkplan(ctx, "safety-lock-died", r.c0, "change/waits", "")
}

func (r *run) lockDied(ctx context.Context, m *planner) error {
	holder, err := r.holder(ctx, m, "600")
	if err != nil {
		return err
	}

	time.Sleep(time.Second)

	planner, err := r.lockplan(ctx, m, "safety-lock-died")
	if err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() { done <- planner.Wait() }()

	time.Sleep(5 * time.Second)

	alive := "no"

	select {
	case err := <-done:
		done <- err
	default:
		alive = "yes"
	}

	sleeper := ""
	if out, err := r.output(ctx, r.work, r.env, "ps", "-o", "pid=", "--ppid", strconv.Itoa(holder.Process.Pid)); err == nil {
		sleeper = strings.TrimSpace(out)
	}

	_ = holder.Process.Kill() //nolint:errcheck // a holder gone already let go
	_ = holder.Wait()         //nolint:errcheck // killed

	t := time.Now()

	if err := <-done; err != nil {
		return fmt.Errorf("lock: %w", err)
	}

	if err := r.tee(filepath.Join(r.work, "plans", "safety-lock-died.time"),
		fmt.Sprintf("after 5 s behind the holder the plan was still waiting: %s; it ended %d s after the holder was killed", alive, int(time.Since(t).Seconds()))); err != nil {
		return err
	}

	if sleeper != "" {
		_ = r.command(ctx, r.work, r.env, io.Discard, io.Discard, "kill", sleeper).Run() //nolint:errcheck // a sleeper gone already is stopped
	}

	return nil
}

// movingScript is bazel-diff as it is, except that once it has hashed the
// head, the work tree is moved to the head's parent, as another process
// might.
const movingScript = `#!/bin/sh
"%[1]s" "$@"
status=$?
case " $* " in *" generate-hashes "*)
	if [ "$(git -C "%[2]s" rev-parse HEAD)" = "%[3]s" ]; then git -C "%[2]s" checkout -q --detach HEAD~1; fi ;;
esac
exit $status
`

func (r *run) movedTree(ctx context.Context, m *planner) (int, error) {
	if err := m.commitChange(ctx, "moved", "moved"); err != nil {
		return 0, err
	}

	if err := m.fx(ctx, "checkout", "-q", "change/moved"); err != nil {
		return 0, err
	}

	head, err := r.output(ctx, m.fixture, m.env, gitCmd, "rev-parse", "HEAD")
	if err != nil {
		return 0, err
	}

	bazelDiff := r.o.BazelDiff
	if bazelDiff == "" {
		bazelDiff = findFile(filepath.Join(m.cache, "bazel-diff"), func(_ string, d os.DirEntry) bool { return !d.IsDir() && d.Name() == "bazel-diff" })
	}

	for _, w := range []struct{ tree, script string }{
		{filepath.Join(m.cache, "tree"), filepath.Join(r.work, "bin", "bazel-diff-moving")},
		{filepath.Join(m.cache+".walk", "tree"), filepath.Join(r.work, "bin", "bazel-diff-moving-walk")},
	} {
		if err := os.WriteFile(w.script, fmt.Appendf(nil, movingScript, bazelDiff, w.tree, head), 0o755); err != nil { //nolint:gosec // an executable
			return 0, fmt.Errorf("moved: %w", err)
		}
	}

	out := filepath.Join(r.work, "plans", "safety-moved")

	code, err := m.toFiles(ctx, out+".json", out+".err", r.tool, m.affectedArgs(m.cache, []string{flagBazelDiff, filepath.Join(r.work, "bin", "bazel-diff-moving")}, "--base", r.c0, "--head", head, "--json")...)
	if err != nil {
		return 0, err
	}

	if err := writeFile(out+".exit", fmt.Sprintf("%d\n", code)); err != nil {
		return 0, err
	}

	if err := m.walkplan(ctx, "safety-moved", r.c0, head, filepath.Join(r.work, "bin", "bazel-diff-moving-walk")); err != nil {
		return 0, err
	}

	cached := 0

	_ = filepath.WalkDir(filepath.Join(m.cache, "snapshots"), func(_ string, d os.DirEntry, err error) error { //nolint:errcheck // what can't be read holds nothing
		if err == nil && strings.HasPrefix(d.Name(), head) {
			cached++
		}

		return nil
	})

	return cached, m.plan(ctx, "safety-moved-again", r.c0, "change/moved", false)
}

func findFile(dir string, match func(string, os.DirEntry) bool) string {
	found := ""

	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error { //nolint:errcheck // nothing found is an answer
		if err != nil || found != "" {
			return nil //nolint:nilerr // keep looking
		}

		if match(p, d) {
			found = p
		}

		return nil
	})

	return found
}

func (r *run) alone(ctx context.Context, m *planner, name, setting, message string) error {
	sym := filepath.Join(r.work, name)

	if err := m.fx(ctx, "checkout", "-q", mainBranch); err != nil {
		return err
	}

	reverse := regexp.MustCompile(`^// Reverse .*`)

	if err := m.edit("unused/unused.go", func(l string) string {
		return reverse.ReplaceAllLiteralString(l, "// Reverse turns a string around ("+message+").")
	}); err != nil {
		return err
	}

	if err := m.fx(ctx, "commit", "-qam", message); err != nil {
		return err
	}

	commit, err := r.output(ctx, m.fixture, m.env, gitCmd, "rev-parse", "HEAD")
	if err != nil {
		return err
	}

	if err := m.fx(ctx, "push", "-q", "origin", mainBranch); err != nil {
		return err
	}

	for _, d := range []string{filepath.Join(sym, "remote", "example"), filepath.Join(sym, "cache")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("alone: %w", err)
		}
	}

	if err := os.Symlink(filepath.Join(m.cache, "caches"), filepath.Join(sym, "cache", "caches")); err != nil {
		return fmt.Errorf("alone: %w", err)
	}

	bare := filepath.Join(sym, "remote", "example", "fixture.git")

	if err := r.sh(ctx, r.work, r.env, gitCmd, "clone", "-q", "--bare", filepath.Join(r.work, "remote", "example", "fixture.git"), bare); err != nil {
		return err
	}

	if err := m.fx(ctx, "checkout", "-q", "-B", "change/"+name, commit); err != nil {
		return err
	}

	if err := m.change(ctx, "helper"); err != nil {
		return err
	}

	if err := m.fx(ctx, "commit", "-qam", name); err != nil {
		return err
	}

	env := setEnv(r.env, "PIPELINEGEN_GIT_URL=file://"+filepath.Join(sym, "remote"), setting)
	p := &planner{r: r, fixture: m.fixture, cache: filepath.Join(sym, "cache"), env: env}

	if err := r.record(ctx, "record-"+name, commit, p.cache, env); err != nil {
		return err
	}

	if err := p.plan(ctx, "safety-"+name, commit, "change/"+name, true); err != nil {
		return err
	}

	return r.aloneLists(ctx, name, commit, bare)
}

func (r *run) aloneLists(ctx context.Context, name, commit, bare string) error {
	var lines strings.Builder

	for _, s := range []string{greetTest, "app/price_test.go", "report/report_test.go"} {
		ref := "refs/pipelinegen/lists/" + commit + ":go/" + s
		git := func(args ...string) (string, error) {
			var out strings.Builder

			err := r.command(ctx, r.work, r.env, &out, io.Discard, gitCmd, append([]string{"--git-dir", bare}, args...)...).Run()

			return out.String(), err
		}

		units := "no list"

		if _, err := git("cat-file", "-e", ref+".txt"); err == nil {
			text, _ := git("show", ref+".txt") //nolint:errcheck // shown after cat-file found it
			units = fmt.Sprintf("%d units", countUnits(text))
		}

		mark := ""
		if _, err := git("cat-file", "-e", ref+".cannot-tell"); err == nil {
			mark = " marked"
		}

		fmt.Fprintf(&lines, "%s %s%s\n", s, units, mark)
	}

	return writeFile(filepath.Join(r.work, "plans", "safety-"+name+".lists"), lines.String())
}

func countUnits(text string) int {
	n := 0

	for line := range strings.SplitSeq(strings.TrimSuffix(text, "\n"), "\n") {
		if text != "" && !strings.HasPrefix(line, "#") {
			n++
		}
	}

	return n
}

func has(list, item string) bool { return strings.Contains(","+list+",", ","+item+",") }

func (r *run) warned(file, slice string) bool {
	if !r.readPlan(file).Warned(slice) {
		return false
	}

	re := regexp.MustCompile(`(?m)^pipelinegen affected: .*` + regexp.QuoteMeta(slice))

	return re.MatchString(readText(strings.TrimSuffix(file, ".json") + ".err"))
}

var (
	observeDetail = regexp.MustCompile(`: go-correlator observe:.*; bound`)
	unitsLine     = regexp.MustCompile(`(?m)^app/price_test.go [1-9][0-9]* units$`)
	markedLine    = regexp.MustCompile(`(?m)^app/price_test.go .* marked$`)
)

func (r *run) check(t *strings.Builder, name, what string, ok bool) {
	known := slices.Contains(knownChecks, name)

	switch {
	case ok && known:
		fmt.Fprintf(t, "| %s | %s | as expected, though listed as known: drop it from the list |\n", name, what)
	case ok:
		fmt.Fprintf(t, "| %s | %s | as expected |\n", name, what)
	case known:
		fmt.Fprintf(t, "| %s | %s | KNOWN WRONG |\n", name, what)
	default:
		fmt.Fprintf(t, "| %s | %s | DIFFERS |\n", name, what)
		r.mark("failed")
	}
}

func (r *run) safetyTable(moved movedResult) error {
	p := func(name string) string { return filepath.Join(r.work, "plans", name) }
	sel := func(name string) string { return r.readPlan(p(name + ".json")).Selected() }

	var t strings.Builder

	t.WriteString("\n| Safety | What happened | Result |\n|---|---|---|\n")

	// the moved plan fails either way, with the same message
	if readText(p("safety-moved.walk.exit")) != "0" && strings.Contains(readText(p("safety-moved.walk.err")), "the work tree changed underneath the plan") {
		if err := writeFile(p("safety-moved.walk"), "same: fails, the work tree changed underneath the plan\n"); err != nil {
			return err
		}
	}

	var warnLines []string
	for _, w := range r.readPlan(p("safety-untold.json")).Warnings() {
		warnLines = append(warnLines, observeDetail.ReplaceAllString(w, " ...; bound"))
	}

	r.check(&t, "untold", "selected "+sel("safety-untold")+"; "+strings.Join(warnLines, "\n"),
		has(sel("safety-untold"), greetTest) && r.warned(p("safety-untold.json"), greetTest))

	config := r.readPlan(p("safety-config.json"))
	r.check(&t, caseConfig, "selected "+config.Selected()+"; app/greet_test.go: "+strings.Join(config.Reasons(greetTest), "\n"),
		config.HasReasonKind(greetTest, "base-config"))

	guarded := r.readPlan(p("safety-config-guarded.json"))
	r.check(&t, "config-guarded", "selected "+guarded.Selected()+"; "+guarded.Reason(),
		guarded.Selected() == "app/greet_test.go,app/price_test.go,report/report_test.go")

	r.configTable(&t)

	waitTime := readText(p("safety-lock-wait.time"))
	r.check(&t, "lock-wait", waitTime+"; selected "+sel("safety-lock-wait"),
		sel("safety-lock-wait") == greetTest && !strings.Contains(waitTime, "ended -"))

	diedTime := readText(p("safety-lock-died.time"))
	r.check(&t, "lock-died", diedTime+"; selected "+sel("safety-lock-died"),
		sel("safety-lock-died") == greetTest && strings.Contains(diedTime, "waiting: yes"))

	movedErr := readText(p("safety-moved.err"))
	r.check(&t, "moved", fmt.Sprintf("exit %s: %s; snapshots of the head kept: %d; the next plan selected %s", readText(p("safety-moved.exit")), movedErr, moved.cached, sel("safety-moved-again")),
		readText(p("safety-moved.exit")) != "0" && strings.Contains(movedErr, "the work tree changed underneath the plan") && moved.cached == 0 &&
			sel("safety-moved-again") == "app/greet_test.go,report/report_test.go")

	for _, c := range []string{"symlink", "foreign", nothing} {
		lists := readText(p("safety-" + c + ".lists"))
		selected := sel("safety-" + c)
		plan := r.readPlan(p("safety-" + c + ".json"))

		r.check(&t, c, fmt.Sprintf("units recorded per slice: %s; a helper change selected %s; %d warnings", strings.ReplaceAll(lists, "\n", ","), or(selected, nothing), len(plan.Warnings())),
			has(selected, "app/price_test.go") && (unitsLine.MatchString(lists) || (markedLine.MatchString(lists) && r.warned(p("safety-"+c+".json"), "app/price_test.go"))))
	}

	if _, err := io.WriteString(r.o.Stdout, t.String()); err != nil {
		return fmt.Errorf("table: %w", err)
	}

	f, err := os.OpenFile(filepath.Join(r.work, "table.md"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("table: %w", err)
	}

	_, err = f.WriteString(t.String())

	return errors.Join(err, f.Close())
}

func (r *run) walkRows() ([]string, []string) {
	walks, _ := filepath.Glob(filepath.Join(r.work, "plans", "*.walk")) //nolint:errcheck // a valid pattern
	sort.Strings(walks)

	var rows []string

	for _, f := range walks {
		label := strings.TrimSuffix(filepath.Base(f), ".walk")

		for line := range strings.SplitSeq(readText(f), "\n") {
			if strings.Contains(line, "DIFFERS") {
				rows = append(rows, label+" "+line)
			}
		}
	}

	return walks, rows
}

func (r *run) differingRows() []string {
	var rows []string

	for line := range strings.SplitSeq(readText(filepath.Join(r.work, "table.md")), "\n") {
		if strings.Contains(line, "| DIFFERS") || strings.Contains(line, ": WRONG (says") {
			rows = append(rows, line)
		}
	}

	return rows
}

func countLines(path string) int {
	n := 0

	for line := range strings.SplitSeq(readText(path), "\n") {
		if line != "" {
			n++
		}
	}

	return n
}

func (r *run) summary() error {
	n := countLines(filepath.Join(r.work, "failed"))
	_, _ = fmt.Fprintf(r.o.Stderr, "%d differences\n", n) //nolint:errcheck // the log is for people

	walks, walkrows := r.walkRows()

	var out strings.Builder

	if len(walkrows) > 0 {
		fmt.Fprintf(&out, "plans the forward binding (built from this checkout) plans differently:\n%s\n", strings.Join(walkrows, "\n"))
	}

	walkline := fmt.Sprintf("; binding forward, built from this checkout, differs in %d of %d plans", len(walkrows), len(walks))
	if !r.walking {
		walkline = "; the walk not compared: go-correlator " + r.pins[pieceCorrelator].version + " offers no links"
	}

	rows := r.differingRows()

	used := fmt.Sprintf("go-sbom %s, go-correlator %s", r.pins[pieceSBOM].version, r.pins[pieceCorrelator].version)

	switch {
	case r.o.Source:
		if len(rows) > 0 {
			fmt.Fprintf(&out, "the tool built from this commit differs from the table in %d rows; the change, not a release, breaks them:\n%s\n", len(rows), strings.Join(rows, "\n"))
		}

		fmt.Fprintf(&out, "the tool built from this commit (%s), with the pinned %s: %d rows differ from the table%s\n", r.built, used, len(rows), walkline)
	case r.o.Newest:
		if len(rows) > 0 {
			fmt.Fprintf(&out, "rows that differ:\n%s\n", strings.Join(rows, "\n"))
		}

		moved := strings.TrimSuffix(r.moved, ", ")
		if moved != "" && n == 0 {
			fmt.Fprintf(&out, "newer releases than the pins hold the table (%s): bump ci/proof/pins\n", moved)
		}

		newer := "none newer than the pins"
		if moved != "" {
			newer = moved + " from the pins"
		}

		fmt.Fprintf(&out, "newest releases: pipelinegen %s, %s (%s): %d rows differ from the table%s\n", r.pins["pipelinegen"].version, used, newer, len(rows), walkline)
	default:
		if len(rows) > 0 {
			fmt.Fprintf(&out, "rows that differ:\n%s\n", strings.Join(rows, "\n"))
		}

		fmt.Fprintf(&out, "pinned releases: pipelinegen %s, %s: %d rows differ from the table%s\n", r.pins["pipelinegen"].version, used, len(rows), walkline)
	}

	if _, err := io.WriteString(r.o.Stdout, out.String()); err != nil {
		return fmt.Errorf("summary: %w", err)
	}

	if n != 0 || len(walkrows) != 0 {
		return ErrDiffers
	}

	return nil
}

func exists(p string) bool {
	_, err := os.Stat(p)

	return err == nil
}
