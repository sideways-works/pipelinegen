package cli

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/attached"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/branchstore"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

const seedScript = `set -u
tools/pipelinegen slice setup-end --root source
tools/pipelinegen slice list | cut -f1 | while read -r name; do
	tools/pipelinegen slice start "$name" --root source
	mkdir -p "coverage/$name/fake"
	echo "unit:$name" > "coverage/$name/fake/touched.txt"
	mkdir -p "$(dirname "result/$name.json")"
	case "$name" in
	*pay*) echo '{"passed": false}' > "result/$name.json" ;;
	*) echo '{"passed": true}' > "result/$name.json" ;;
	esac
	tools/pipelinegen slice end "$name" --root source || true
done
`

func seedRepo(t *testing.T) (string, *gitstore.Store, map[string]string) {
	t.Helper()

	root := t.TempDir()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cfg := recordConfig
	if onBranchStore {
		cfg += "store: {backend: branch}\n"
	}

	for p, body := range map[string]string{
		config.FileName:      cfg,
		"ci/e2e-slice.yml":   recordTask,
		"ci/e2e-slice.sh":    "test \"$GREETING\" = hello\n" + seedScript,
		"e2e/cart.spec.ts":   "test('cart')\n",
		"e2e/pay.spec.ts":    "test('pay')\n",
		"tools/plugins/fake": "#!/bin/sh\nFAKE=1 exec '" + exe + "' \"$@\"\n",
	} {
		writeFile(t, filepath.Join(root, p), body)
	}

	if err := os.Chmod(filepath.Join(root, "tools/plugins/fake"), 0o700); err != nil { //nolint:gosec // the test's own plugin
		t.Fatal(err)
	}

	gitT(t, root, "init", "-q", "-b", "main")
	gitT(t, root, "add", "-A", ":!tools")
	gitT(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-m", "one")

	env := map[string]string{envGitURL: "", envToken: "", envJobURL: "", envPipelineStep: "", envRun: "", envConfig: "", envPR: "", envChange: ""}

	if _, stderr, code := runIn(t, root, env, "record", "--suite", "e2e", "--root", root); code == 0 {
		t.Fatalf("a recording with a failed slice passed: %s", stderr)
	}

	bare := storetest.Bare(t)
	gitT(t, root, "remote", "add", "origin", bare)

	shared := &gitstore.Store{Remote: bare, Dir: filepath.Join(t.TempDir(), "scratch")}
	if onBranchStore {
		shared.Branch = &branchstore.Store{Remote: bare, Branch: config.DefaultStoreBranch, Dir: filepath.Join(t.TempDir(), "branch")}
	}

	t.Cleanup(func() {
		if err := shared.Close(); err != nil {
			t.Error(err)
		}
	})

	for k, v := range env {
		t.Setenv(k, v)
	}

	return root, shared, env
}

func listFiles(t *testing.T, s *gitstore.Store, commit string) map[string][]byte {
	t.Helper()

	ref, ok, err := s.First(t.Context(), []string{lists.Ref(commit)})
	if err != nil {
		t.Fatal(err)
	}

	if !ok {
		return nil
	}

	files, err := s.Files(t.Context(), ref.Commit)
	if err != nil {
		t.Fatal(err)
	}

	maps.DeleteFunc(files, func(name string, _ []byte) bool { return strings.HasPrefix(name, "_attached/") })

	return files
}

func TestSeedWritesWhatATrustedAttachWould(t *testing.T) { seedWritesWhatATrustedAttachWould(t) }

func TestSeedWritesWhatATrustedAttachWouldOnABranch(t *testing.T) {
	onBranch(t, seedWritesWhatATrustedAttachWould)
}

func seedWritesWhatATrustedAttachWould(t *testing.T) {
	t.Helper()

	root, shared, _ := seedRepo(t)
	head := gitT(t, root, "rev-parse", "HEAD")

	stdout, stderr, code := run(t, "seed", "--root", root, "--dry-run")
	if code != 0 || !strings.Contains(stdout, "a new list for 1 slice") ||
		!strings.Contains(stdout, "no list for 1 slice that failed locally: e2e/e2e/pay.spec.ts") {
		t.Fatalf("dry run: %d\n%s\n%s", code, stdout, stderr)
	}

	if got := listFiles(t, shared, head); got != nil {
		t.Fatalf("a dry run wrote lists: %v", got)
	}

	t.Setenv(envToken, "store-credential")

	stdout, stderr, code = run(t, "seed", "--root", root)
	if code != 0 || !strings.Contains(stdout, "seeded "+head[:12]) {
		t.Fatalf("seed: %d\n%s\n%s", code, stdout, stderr)
	}

	seeded := listFiles(t, shared, head)
	if len(seeded) != 1 || string(seeded["e2e/e2e/cart.spec.ts.txt"]) == "" {
		t.Fatalf("seeded lists: %q", seeded)
	}

	localPath, err := localStorePath(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}

	local := &gitstore.Store{Remote: "file://" + localPath, Dir: filepath.Join(t.TempDir(), "local")}
	t.Cleanup(func() { _ = local.Close() }) //nolint:errcheck // a scratch directory

	ids, err := localRuns(t.Context(), local, head)
	if err != nil || len(ids) != 1 {
		t.Fatalf("local runs: %v %v", ids, err)
	}

	recs, err := report.Recordings(t.Context(), local, ids[0])
	if err != nil {
		t.Fatal(err)
	}

	states, err := report.States(t.Context(), local, ids[0], "e2e")
	if err != nil {
		t.Fatal(err)
	}

	other := &gitstore.Store{Remote: storetest.Bare(t), Dir: filepath.Join(t.TempDir(), "other")}
	t.Cleanup(func() { _ = other.Close() }) //nolint:errcheck // a scratch directory

	trusted := report.ID{Run: "main", Change: "main", Commit: head}
	if err := report.WritePlan(t.Context(), other, trusted, []byte(`{"all": true, "suites": [{"suite": "e2e", "all": true, "selected": [
		{"name": "e2e/cart.spec.ts"}, {"name": "e2e/pay.spec.ts"}], "untouched": []}]}`)); err != nil {
		t.Fatal(err)
	}

	for key, data := range recs {
		slice := strings.TrimSuffix(strings.TrimPrefix(key, "e2e/"), ".txt")
		recorded(t, other, trusted, "e2e", slice, states[slice], string(data))
	}

	plan, _, err := readPlan(t.Context(), other, trusted)
	if err != nil {
		t.Fatal(err)
	}

	r, marks, err := runRecordings(t.Context(), other, trusted)
	if err != nil {
		t.Fatal(err)
	}

	passed, err := passedSlices(t.Context(), other, trusted)
	if err != nil {
		t.Fatal(err)
	}

	shrink, whole := judged(r, passed, plan.All)

	if _, err := (lists.Attach{
		Repo: root, Run: "main", Change: "main", Commit: head, To: head,
		Recordings: r, CannotTell: passedMarks(marks, passed), Shrinkable: shrink, Whole: whole, Trusted: true,
	}).Do(t.Context(), other); err != nil {
		t.Fatal(err)
	}

	want := listFiles(t, other, head)

	const replacedFile = "_replaced.json"
	if _, ok := seeded[replacedFile]; ok || !strings.Contains(string(want[replacedFile]), "e2e/e2e/cart.spec.ts") {
		t.Fatalf("which lists were replaced: the seed says %q, the run of everything %q", seeded[replacedFile], want[replacedFile])
	}

	delete(want, replacedFile)

	if !maps.EqualFunc(seeded, want, func(a, b []byte) bool { return string(a) == string(b) }) {
		t.Fatalf("seeded lists\n%q\nwant a trusted attach's\n%q", seeded, want)
	}

	ref, _, err := shared.First(t.Context(), []string{lists.Ref(head)})
	if err != nil {
		t.Fatal(err)
	}

	notes, err := lists.Seeds(t.Context(), shared, ref)
	if err != nil || len(notes) != 1 || notes[0].Seed.From != head || notes[0].Seed.Tool != Version ||
		len(notes[0].Seed.Plugins) != 2 || notes[0].Seed.Plugins[1].Pin != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("seed notes: %+v %v", notes, err)
	}

	cfgPath := filepath.Join(t.TempDir(), config.FileName)

	showCfg := recordConfig + "git: {remote: \"" + shared.Remote + "\"}\n"
	if onBranchStore {
		showCfg += "store: {backend: branch}\n"
	}

	writeFile(t, cfgPath, showCfg)

	stdout, _, code = run(t, "lists", "show", "--root", root, "--config", cfgPath)
	if code != 0 || !strings.Contains(stdout, "seeded from a local recording of "+head[:12]+" ("+ids[0].Change+", pipelinegen "+Version+", plugins units commit, fake commit), not a CI run\n") {
		t.Fatalf("lists show: %d\n%s", code, stdout)
	}

	stdout, _, code = run(t, "seed", "--root", root, "--dry-run")
	if code != 0 || !strings.Contains(stdout, ids[0].Change+": seeded already, nothing would be written") || strings.Contains(stdout, "not recorded locally") {
		t.Fatalf("dry run again: %d\n%s", code, stdout)
	}

	stdout, _, code = run(t, "seed", "--root", root)
	if code != 0 || !strings.Contains(stdout, "attached already, nothing written") || strings.Contains(stdout, "seeded ") {
		t.Fatalf("again: %d\n%s", code, stdout)
	}
}

func TestSeedRefusesBeforeWriting(t *testing.T) {
	root, shared, _ := seedRepo(t)
	recordedAt := gitT(t, root, "rev-parse", "HEAD")

	t.Setenv(envToken, "store-credential")

	refused := func(why, want string, args ...string) {
		t.Helper()

		_, stderr, code := run(t, append([]string{"seed", "--root", root}, args...)...)
		if code == 0 || !strings.Contains(stderr, want) {
			t.Fatalf("%s: %d %s", why, code, stderr)
		}

		if got := listFiles(t, shared, recordedAt); got != nil {
			t.Fatalf("%s: lists were written", why)
		}
	}

	t.Setenv(envJobURL, "https://ci/jobs/plan")
	refused("a pipeline step", "never in a pipeline step")
	t.Setenv(envJobURL, "")

	t.Setenv(envPipelineStep, "1")
	refused("a step marked as a pipeline's", "never in a pipeline step")
	t.Setenv(envPipelineStep, "")

	t.Setenv(envChange, "pr-7")
	refused("a pull request's change", "never in a pipeline step")
	t.Setenv(envChange, "")

	t.Setenv(envPR, "7")
	refused("a pull request", "never in a pipeline step")
	t.Setenv(envPR, "")

	writeFile(t, filepath.Join(root, "e2e", "new.spec.ts"), "test('new')\n")
	refused("a dirty tree", "commit these first")

	if err := os.Remove(filepath.Join(root, "e2e", "new.spec.ts")); err != nil {
		t.Fatal(err)
	}

	t.Setenv(envToken, "")
	refused("no credential", "writing credential isn't in the environment (GITHUB_TOKEN)")
	t.Setenv(envToken, "store-credential")

	gitT(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "two")

	refused("not HEAD", "isn't HEAD", "--commit", recordedAt)
	refused("another commit's recording", "no local recording of "+gitT(t, root, "rev-parse", "--short=12", "HEAD")+" in the local store (it has recordings of: "+recordedAt[:12]+")")

	gitT(t, root, "checkout", "-q", "-b", "topic", recordedAt)
	gitT(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "off the branch")
	refused("off the default branch", "isn't on main")
}

const firstRunsConfig = `
pipeline: shop
suites:
  e2e:
    tag: slice-e2e
    run_slice: ci/tasks/e2e-slice.yml
HOST
concourse: {url: "https://ci", repository: {uri: x}, planner: {image: {repository: p}, digest: "sha256:x", bootstrap: {release: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}, bazelisk: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}}}}
runs:
  main:
    trigger: {branch: main}
    compare: last_passed
    select: affected
    suites: {e2e: {}}
`

func TestASeededCommitIsTheFirstRunsBase(t *testing.T) { seededCommitIsTheFirstRunsBase(t) }

func TestASeededCommitIsTheFirstRunsBaseOnABranch(t *testing.T) {
	onBranch(t, seededCommitIsTheFirstRunsBase)
}

func seededCommitIsTheFirstRunsBase(t *testing.T) {
	t.Helper()

	root, shared, _ := seedRepo(t)
	seeded := gitT(t, root, "rev-parse", "HEAD")

	t.Setenv(envToken, "store-credential")

	if _, stderr, code := run(t, "seed", "--root", root); code != 0 {
		t.Fatalf("seed: %d %s", code, stderr)
	}

	gitT(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "two")
	child := gitT(t, root, "rev-parse", "HEAD")

	cfgText := strings.Replace(firstRunsConfig, "HOST", "git: {remote: \""+shared.Remote+"\"}", 1)
	if onBranchStore {
		cfgText += "store: {backend: branch}\n"
	}

	cfg, err := config.Parse([]byte(cfgText))
	if err != nil {
		t.Fatal(err)
	}

	base := func(run, change, commit string) (string, string) {
		t.Helper()

		got, note, err := branchBase(t.Context(), cfg, runs.Change{Run: run, ID: change, Commit: commit, Branch: change}, openStore(cfg), root)
		if err != nil {
			t.Fatal(err)
		}

		return got, note
	}

	if got, note := base("main", "main", seeded); got != seeded || !strings.Contains(note, "its own base") {
		t.Fatalf("a run on the seeded commit: %s %q", got, note)
	}

	if got, note := base("main", "main", child); got != seeded || !strings.Contains(note, "which carries a baseline") {
		t.Fatalf("a run on its child: %s %q", got, note)
	}

	if got, _ := base("main", "topic", child); got != "" {
		t.Fatalf("another branch's run: %s", got)
	}

	if _, stderr, code := run(t, "baseline", "--root", root, "--run", "nope"); code != 2 || !strings.Contains(stderr, "no run") {
		t.Fatalf("an unknown run: %d %s", code, stderr)
	}

	cfgPath := filepath.Join(t.TempDir(), config.FileName)
	writeFile(t, cfgPath, cfgText)

	stdout, stderr, code := run(t, "baseline", "--root", root, "--config", cfgPath, "--run", "main", "--dry-run")
	if code != 0 || !strings.Contains(stdout, "dry run, nothing written") {
		t.Fatalf("baseline dry run: %d %s %s", code, stdout, stderr)
	}

	if got := listFiles(t, shared, child); got != nil {
		t.Fatalf("a dry run wrote: %v", got)
	}

	stdout, stderr, code = run(t, "baseline", "--root", root, "--config", cfgPath, "--run", "main")
	if code != 0 || !strings.Contains(stdout, "note a baseline of main set by hand") {
		t.Fatalf("baseline: %d %s %s", code, stdout, stderr)
	}

	if got, _ := base("main", "main", child); got != child {
		t.Fatalf("a run on the child with a baseline: %s", got)
	}

	if got, want := listFiles(t, shared, child), listFiles(t, shared, seeded); !maps.EqualFunc(got, want, func(a, b []byte) bool { return string(a) == string(b) }) {
		t.Fatalf("the baseline's lists %q, want the seed's %q", got, want)
	}

	stdout, _, code = run(t, "lists", "show", "--root", root, "--config", cfgPath, "--commit", child)
	if code != 0 || !strings.Contains(stdout, "a baseline of main set by hand at "+child[:12]) {
		t.Fatalf("lists show of the baseline: %d\n%s", code, stdout)
	}
}

type statusAPI struct {
	statuses map[string]map[string][2]string // sha: context: state, description
	posted   int
}

func (g *statusAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")

	switch {
	case r.Method == http.MethodPost && len(parts) == 5 && parts[3] == "statuses":
		var body struct {
			State       string `json:"state"`
			Context     string `json:"context"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		if g.statuses[parts[4]] == nil {
			g.statuses[parts[4]] = map[string][2]string{}
		}

		g.statuses[parts[4]][body.Context] = [2]string{body.State, body.Description}
		g.posted++
	case r.Method == http.MethodGet && len(parts) == 6 && parts[5] == "status":
		out := []map[string]string{}
		for ctx, st := range g.statuses[parts[4]] {
			out = append(out, map[string]string{"context": ctx, "state": st[0], "description": st[1]})
		}

		if err := json.NewEncoder(w).Encode(map[string]any{"statuses": out}); err != nil {
			panic(err)
		}
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func TestABaselineStatusIsTheFirstRunsBase(t *testing.T) {
	root, shared, _ := seedRepo(t)
	c := gitT(t, root, "rev-parse", "HEAD")

	api := &statusAPI{statuses: map[string]map[string][2]string{}}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	t.Setenv(envAPIURL, srv.URL)

	cfgText := strings.Replace(firstRunsConfig, "HOST", "github: {repository: o/r, token: gh}", 1) +
		"  pr:\n    trigger: {pull_requests: {base: main}}\n    compare: merged_target\n    select: affected\n    suites: {e2e: {}}\n" +
		"  release:\n    trigger: {branch: release}\n    compare: last_passed\n    select: affected\n    suites: {e2e: {}}\n"
	cfgPath := filepath.Join(t.TempDir(), config.FileName)
	writeFile(t, cfgPath, cfgText)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	baseline := []string{"baseline", "--root", root, "--config", cfgPath, "--run", "main"}

	refused := func(why, want string, args ...string) {
		t.Helper()

		_, stderr, code := run(t, args...)
		if code == 0 || !strings.Contains(stderr, want) || api.posted != 0 {
			t.Fatalf("%s: %d %s (posted %d)", why, code, stderr, api.posted)
		}
	}

	refused("no credential", "the report credential isn't in the environment", baseline...)

	t.Setenv(envToken, "report-credential")

	t.Setenv(envJobURL, "https://ci/jobs/plan")
	refused("a pipeline step", "never in a pipeline step", baseline...)
	t.Setenv(envJobURL, "")

	refused("a pull request run", "tests pull requests", "baseline", "--root", root, "--config", cfgPath, "--run", "pr")
	refused("another branch's run", "not the default branch", "baseline", "--root", root, "--config", cfgPath, "--run", "release")

	writeFile(t, filepath.Join(root, "e2e", "new.spec.ts"), "test('new')\n")
	refused("a dirty tree", "commit these first", baseline...)

	if err := os.Remove(filepath.Join(root, "e2e", "new.spec.ts")); err != nil {
		t.Fatal(err)
	}

	stdout, _, code := run(t, append(baseline, "--dry-run")...)
	if code != 0 || !strings.Contains(stdout, "dry run, nothing posted") || api.posted != 0 {
		t.Fatalf("dry run: %d %s", code, stdout)
	}

	api.statuses[c] = map[string][2]string{"pipelinegen/main": {"success", "every slice passed"}}

	gitT(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "two")
	child := gitT(t, root, "rev-parse", "HEAD")
	gitT(t, root, "checkout", "-q", c)

	base := func(commit string) string {
		t.Helper()

		got, _, err := branchBase(t.Context(), cfg, runs.Change{Run: "main", ID: "main", Commit: commit, Branch: "main"}, shared, root)
		if err != nil {
			t.Fatal(err)
		}

		return got
	}

	if got := base(c); got == c {
		t.Fatal("an ordinary passed status made its own commit's rerun empty")
	}

	if got := base(child); got != c {
		t.Fatalf("the child of a passed commit: %s", got)
	}

	delete(api.statuses, c)

	stdout, stderr, code := run(t, baseline...)
	if code != 0 || api.posted != 1 || api.statuses[c]["pipelinegen/main baseline"] != [2]string{"success", baselineDescription} {
		t.Fatalf("baseline: %d %s %s %v", code, stdout, stderr, api.statuses)
	}

	if len(baselineDescription) > 140 {
		t.Fatalf("the description is longer than a status holds: %d", len(baselineDescription))
	}

	if got := base(c); got != c {
		t.Fatalf("a run on the baseline: %s", got)
	}

	if got := base(child); got != c {
		t.Fatalf("a run on the baseline's child: %s", got)
	}

	api.statuses[c]["pipelinegen/main"] = [2]string{"error", "the plan failed"}

	if got := base(c); got != c {
		t.Fatalf("a run on the baseline after a failed plan: %s", got)
	}

	if got := base(child); got != c {
		t.Fatalf("a run on the baseline's child after a failed plan: %s", got)
	}

	api.statuses[c]["pipelinegen/main"] = [2]string{"failure", "a slice failed"}

	if got := base(child); got == c {
		t.Fatal("a failed run on the baseline still made it the child's base")
	}

	delete(api.statuses[c], "pipelinegen/main baseline")
	api.statuses[c]["pipelinegen/main"] = [2]string{"success", baselineDescription}

	if got := base(c); got != c {
		t.Fatalf("a baseline in the run's context: %s", got)
	}

	gitT(t, root, "checkout", "-q", "-b", "topic", c)
	gitT(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "off the branch")

	if _, stderr, code := run(t, baseline...); code == 0 || !strings.Contains(stderr, "isn't on main") || api.posted != 1 {
		t.Fatalf("off the branch: %d %s", code, stderr)
	}
}

func TestSeedOnlyAddsUnlessReplace(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "adds", true: "replaces"}[replace], func(t *testing.T) {
			root, shared, _ := seedRepo(t)
			head := gitT(t, root, "rev-parse", "HEAD")

			const key = "e2e/e2e/cart.spec.ts"

			if _, err := shared.Write(t.Context(), "lists", gitstore.Update{Ref: lists.Ref(head), Files: lists.Encode(recording.Lists{
				key: {"unit:a-trusted-run's"},
			})}); err != nil {
				t.Fatal(err)
			}

			t.Setenv(envToken, "store-credential")

			args := []string{"seed", "--root", root}
			if replace {
				args = append(args, "--replace")
			}

			if stdout, stderr, code := run(t, append(args, "--dry-run")...); code != 0 ||
				!strings.Contains(stdout, map[bool]string{false: "more units for 1 slice", true: "a replaced list for 1 slice"}[replace]) {
				t.Fatalf("dry run: %d\n%s\n%s", code, stdout, stderr)
			}

			if _, stderr, code := run(t, "seed", "--root", root); code != 0 {
				t.Fatalf("seed: %d %s", code, stderr)
			}

			if stdout, stderr, code := run(t, args...); code != 0 || replace && !strings.Contains(stdout, "seeded ") {
				t.Fatalf("seed %v: %d %s %s", args, code, stdout, stderr)
			}

			got, err := lists.Decode(listFiles(t, shared, head))
			if err != nil {
				t.Fatal(err)
			}

			want := []recording.ID{"unit:a-trusted-run's", recording.ID("unit:" + key[len("e2e/"):])}
			if replace {
				want = want[1:]
			}

			if !slices.Equal(got[key], want) {
				t.Fatalf("the cart spec's list: %v, want %v", got[key], want)
			}
		})
	}
}

func TestTheFirstPlansAfterABaselineSelectOnlyTheChange(t *testing.T) {
	testrepo.Parallel(t)

	bd := bazeldifftest.Tool(t).Path

	r := testrepo.New(t)
	c := r.Git("rev-parse", "HEAD")

	store := &gitstore.Store{Remote: storetest.Bare(t), Dir: filepath.Join(t.TempDir(), "scratch")}
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // a scratch directory

	if _, err := (lists.Attach{
		Repo: r.Dir, Run: seedRun, Change: "local-1", Commit: c, To: c, Recordings: recording.Lists{}, Trusted: true,
		Seed: &attached.Seed{From: c, Tool: Version},
	}).Do(t.Context(), store); err != nil {
		t.Fatal(err)
	}

	r.Write("api/money/money.go", "package money // changed\n")
	child := r.Commit("money")

	cfg, err := config.Parse([]byte(strings.Replace(firstRunsConfig, "HOST", "git: {remote: \""+store.Remote+"\"}", 1)))
	if err != nil {
		t.Fatal(err)
	}

	cache := t.TempDir()
	testrepo.Bazel(t, filepath.Join(cache, "tree"))

	for _, tc := range []struct{ head, want string }{
		{c, "backend: 0 selected, 1 untouched\ne2e: 0 selected, 2 untouched\nspecs: 0 selected, 2 untouched\n"},
		{child, "backend: 1 selected, 0 untouched\n  run //backendtests:money_test\ne2e: 1 selected, 1 untouched\n  run //e2e:checkout\n  run whole-suite check reconcile\nspecs: 1 selected, 1 untouched\n  run e2e/checkout.spec.ts\n"},
	} {
		base, _, err := branchBase(t.Context(), cfg, runs.Change{Run: "main", ID: "main", Commit: tc.head, Branch: "main"}, store, r.Dir)
		if err != nil || base != c {
			t.Fatalf("the base of %s: %s %v", tc.head[:12], base, err)
		}

		stdout, stderr, code := run(t, "affected", "-root", r.Dir, "-base", base, "-head", tc.head, "-cache", cache, "-bazel-diff", bd, "-no-lists")
		if code != 0 || stdout != tc.want {
			t.Fatalf("affected %s: %d\n%s\nwant\n%s\n%s", tc.head[:12], code, stdout, tc.want, stderr)
		}
	}
}

func TestSeedTakesEveryRecordingOfTheCommit(t *testing.T) {
	root, shared, _ := seedRepo(t)
	head := gitT(t, root, "rev-parse", "HEAD")

	path, err := localStorePath(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}

	local := &gitstore.Store{Remote: "file://" + path, Dir: filepath.Join(t.TempDir(), "local")}
	t.Cleanup(func() { _ = local.Close() }) //nolint:errcheck // a scratch directory

	later := report.ID{Run: localRun, Change: "local-29991231-235959-000000000", Commit: head}
	if err := report.WritePlan(t.Context(), local, later, []byte(`{"head": "`+head+`", "suites": [{"suite": "e2e", "selected": [
		{"name": "e2e/pay.spec.ts"}], "untouched": [], "whole_suite": []}]}`)); err != nil {
		t.Fatal(err)
	}

	recorded(t, local, later, "e2e", "e2e/pay.spec.ts", report.Passed, "unit:pay-later\n")
	recorded(t, local, later, "e2e", "e2e/cart.spec.ts", report.Passed, "unit:cart-later\n")

	t.Setenv(envToken, "store-credential")

	stdout, stderr, code := run(t, "seed", "--root", root)
	if code != 0 || !strings.Contains(stdout, "the local recordings local-") || strings.Contains(stdout, "no list for") {
		t.Fatalf("seed: %d\n%s\n%s", code, stdout, stderr)
	}

	got, err := lists.Decode(listFiles(t, shared, head))
	if err != nil {
		t.Fatal(err)
	}

	for key, want := range map[string]recording.ID{"e2e/e2e/cart.spec.ts": "unit:cart-later", "e2e/e2e/pay.spec.ts": "unit:pay-later"} {
		if !slices.Equal(got[key], []recording.ID{want}) {
			t.Errorf("%s: %v, want the newest passed recording %s", key, got[key], want)
		}
	}
}

var errCheckFinding = errors.New("pipelinegen.yaml: a finding of the check")

func TestABaseConfigThatCantPlan(t *testing.T) {
	repo := t.TempDir()
	headCfg := strings.Replace(firstRunsConfig, "HOST", "", 1)
	baseCfg := strings.Replace(headCfg, "    tag: slice-e2e\n", "    tag: slice-e2e\n    lock: {name: browsers, minutes: 90, slots: 1}\n", 1)

	writeFile(t, filepath.Join(repo, config.FileName), baseCfg)
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-m", "base")
	base := gitT(t, repo, "rev-parse", "HEAD")

	writeFile(t, filepath.Join(repo, config.FileName), headCfg)
	gitT(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-am", "head")
	head := gitT(t, repo, "rev-parse", "HEAD")

	cfg, err := config.Parse([]byte(headCfg))
	if err != nil {
		t.Fatal(err)
	}

	plan := func(c *config.Config, everything string, _ *config.Meaning) (*affected.Plan, error) {
		if c.Suites["e2e"].Lock != nil {
			return nil, errCheckFinding
		}

		return &affected.Plan{All: everything != "", Reason: everything}, nil
	}

	p, err := withBaseConfig(t.Context(), cfg, repo, "", base, head, true, plan)
	if err != nil || p.All || !strings.Contains(strings.Join(p.Warnings, "\n"), "the base's configuration ("+base[:12]+") can't plan, so this plan is made under this commit's configuration alone") {
		t.Fatalf("trusted: %+v %v", p, err)
	}

	p, err = withBaseConfig(t.Context(), cfg, repo, "", base, head, false, plan)
	if err != nil || !p.All || !strings.Contains(p.Reason, "the base's configuration can't plan, so what it selected can't be known: pipelinegen.yaml: a finding of the check") {
		t.Fatalf("proposed: %+v %v", p, err)
	}

	writeFile(t, filepath.Join(repo, config.FileName), strings.Replace(headCfg, "    tag: slice-e2e\n", "", 1))
	gitT(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-am", "unreadable")
	unreadable := gitT(t, repo, "rev-parse", "HEAD")

	p, err = withBaseConfig(t.Context(), cfg, repo, "", unreadable, head, true, plan)
	if err != nil || p.All || !strings.Contains(strings.Join(p.Warnings, "\n"), "can't plan, so this plan is made under this commit's configuration alone") {
		t.Fatalf("trusted, unreadable: %+v %v", p, err)
	}

	p, err = withBaseConfig(t.Context(), cfg, repo, "", unreadable, head, false, plan)
	if err != nil || !p.All || !strings.Contains(p.Reason, "the base's configuration can't be read") {
		t.Fatalf("proposed, unreadable: %+v %v", p, err)
	}
}
