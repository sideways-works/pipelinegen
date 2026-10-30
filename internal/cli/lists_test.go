package cli

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/recording"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/report"
)

func checkout(t *testing.T, root string, n int) []string {
	t.Helper()

	g := func(args ...string) string {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")

		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}

		return strings.TrimSpace(string(out))
	}

	g("init", "--quiet", "-b", "main")
	g("add", ".")
	g("commit", "--quiet", "-m", "config")

	out := []string{g("rev-parse", "HEAD")}

	for range n {
		g("commit", "--quiet", "--allow-empty", "-m", "next")
		out = append(out, g("rev-parse", "HEAD"))
	}

	return out
}

func recorded(t *testing.T, s *gitstore.Store, id report.ID, suite, slice string, state report.State, rec string) {
	t.Helper()

	if _, err := s.Write(t.Context(), "slice", gitstore.Update{Ref: id.StateRef(suite, slice), Any: true, Files: map[string][]byte{
		"state.json":    []byte(`{"label": "//e2e:` + slice + `", "state": "` + string(state) + `"}`),
		"recording.txt": []byte(rec),
	}}); err != nil {
		t.Fatal(err)
	}
}

const (
	unitCreate = "api/orders#func:Create"
	unitRefund = "api/orders#func:Refund"
	unitTotal  = "api/orders#method:Order.Total"
)

func TestListsAttachAndShow(t *testing.T) {
	root, _, store := runEnv(t)
	c := checkout(t, root, 2)

	tested := "0123456789abcdef0123456789abcdef01234567"
	id := report.ID{Run: "pr", Change: "pr-7", Commit: tested}

	attach := []string{"lists", "attach", "--root", root, "--run", "pr", "--change", "pr-7", "--commit", tested, "--to", c[2]}

	if _, stderr, code := run(t, attach...); code != 1 || !strings.Contains(stderr, "has no plan") {
		t.Fatalf("no plan: %d %s", code, stderr)
	}

	if _, _, code := run(t, "lists", "attach", "--root", root, "--run", "pr"); code != 2 {
		t.Fatalf("flags missing: %d", code)
	}

	plan := `{"head": "` + tested + `", "all": true, "suites": [{"suite": "e2e", "all": true, "selected": [
		{"name": "e2e.checkout", "label": "//e2e:checkout"}, {"name": "e2e.orders", "label": "//e2e:orders"}], "untouched": []}]}`
	if err := report.WritePlan(t.Context(), store, id, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Write(t.Context(), "lists", gitstore.Update{Ref: lists.Ref(c[0]), Files: lists.Encode(recording.Lists{
		"e2e/e2e.checkout": {unitCreate, unitTotal},
		"e2e/e2e.orders":   {unitTotal},
	})}); err != nil {
		t.Fatal(err)
	}

	recorded(t, store, id, "e2e", "e2e.checkout", report.Passed, unitRefund+"\n")
	recorded(t, store, id, "e2e", "e2e.orders", report.Failed, unitCreate+"\n")

	stdout, stderr, code := run(t, attach...)
	if code != 0 {
		t.Fatalf("attach: %d %s", code, stderr)
	}

	want := "lists of " + c[2][:12] + ": pr pr-7 0123456789ab merged into the lists of " + c[0][:12] + ", 2 commits back; 2 slices, 2 changed\n"
	if stdout != want {
		t.Fatalf("attach said\n%s\nwant\n%s", stdout, want)
	}

	stdout, _, code = run(t, attach...)
	if code != 0 || stdout != "lists of "+c[2][:12]+": pr pr-7 0123456789ab attached already, nothing written\n" {
		t.Fatalf("again: %d %q", code, stdout)
	}

	stdout, stderr, code = run(t, "lists", "show", "--root", root, "--commit", c[2])
	if code != 0 {
		t.Fatalf("show: %d %s", code, stderr)
	}

	want = "lists of " + c[2][:12] + ": its own lists\n" +
		"with the recordings of the live runs:\n  pr pr-7 0123456789ab (2 slices)\n" +
		"e2e: 2 slices\n  e2e.checkout 3\n  e2e.orders 2\n"
	if stdout != want {
		t.Fatalf("show said\n%s\nwant\n%s", stdout, want)
	}

	stdout, _, code = run(t, "lists", "show", "--root", root, "--slice", "e2e.orders", "--suite", "e2e")
	want = "lists of " + c[2][:12] + ": its own lists\n" +
		"with the recordings of the live runs:\n  pr pr-7 0123456789ab (2 slices)\n" +
		"e2e e2e.orders: 2 units\n  " + unitCreate + "\n  " + unitTotal + "\n"

	if code != 0 || stdout != want {
		t.Fatalf("show a slice: %d\n%s\nwant\n%s", code, stdout, want)
	}

	stdout, _, code = run(t, "lists", "show", "--root", root, "--commit", c[1], "--suite", "e2e")
	if code != 0 || !strings.HasPrefix(stdout, "lists of "+c[1][:12]+": the lists of its parent "+c[0][:12]+"\n") ||
		!strings.Contains(stdout, "e2e.checkout 3\n") {
		t.Fatalf("ancestor: %d\n%s", code, stdout)
	}

	if _, _, code := run(t, "lists", "show", "--root", root, "--suite", "nope"); code != 2 {
		t.Fatalf("unknown suite: %d", code)
	}

	if _, _, code := run(t, "lists", "show", "--root", root, "--commit", "feedface"); code != 2 {
		t.Fatalf("unknown commit: %d", code)
	}
}

func TestPlanningBindsWithLiveRecordings(t *testing.T) {
	root, _, store := runEnv(t)
	c := checkout(t, root, 2)

	if _, err := store.Write(t.Context(), "lists", gitstore.Update{Ref: lists.Ref(c[0]), Files: lists.Encode(recording.Lists{
		"e2e/e2e.checkout": {unitCreate},
	})}); err != nil {
		t.Fatal(err)
	}

	live := report.ID{Run: "pr", Change: "pr-9", Commit: c[2]}
	recorded(t, store, live, "e2e", "e2e.checkout", report.Passed, unitRefund+"\n")
	recorded(t, store, live, "e2e", "e2e.orders", report.Passed, unitTotal+"\n")

	got, src, err := planningLists(t.Context(), store, root, c[1], lists.Walk)
	if err != nil {
		t.Fatal(err)
	}

	if _, made := got["e2e/e2e.orders"]; !slices.Equal(got["e2e/e2e.checkout"], []recording.ID{unitCreate, unitRefund}) || made {
		t.Fatalf("lists %v", got)
	}

	if src == nil || src.Back != 1 || len(src.Live) != 1 || src.Live[0].Change != "pr-9" {
		t.Fatalf("source %+v", src)
	}

	if got, src, err := planningLists(t.Context(), store, root, "", lists.Walk); err != nil || len(got) != 0 || src != nil {
		t.Fatalf("no base: %v %+v %v", got, src, err)
	}
}

func TestASuiteEndAttachesAPassedBranchRun(t *testing.T) {
	root, api, store := runEnv(t)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), branchConfig+"    record: true\n")
	c := checkout(t, root, 1)

	t.Setenv(envRun, "main")
	t.Setenv(envChange, "main")
	t.Setenv(envCommit, c[1])
	t.Setenv(envPR, "0")

	id := report.ID{Run: "main", Change: "main", Commit: c[1]}
	if err := report.WritePlan(t.Context(), store, id, []byte(`{"all": true, "suites": [{"suite": "e2e", "all": true, "selected": [{"name": "e2e/a.spec.ts", "label": "@pipelinegen_deps//slices:e2e/a.spec.ts"}]}]}`)); err != nil {
		t.Fatal(err)
	}

	recorded(t, store, id, "e2e", "e2e/a.spec.ts", report.Passed, unitCreate+"\n# setup\n"+unitTotal+"\n")

	if _, stderr, code := run(t, "report", "suite", "--root", root); code != 0 {
		t.Fatalf("suite: %d %s", code, stderr)
	}

	if got := api.status("pipelinegen/main"); !strings.HasPrefix(got, "success") {
		t.Fatalf("run status %q", got)
	}

	stdout, _, code := run(t, "lists", "show", "--root", root, "--commit", c[1])
	if code != 0 || !strings.Contains(stdout, ": its own lists\n") || !strings.Contains(stdout, "  _setup 1\n  e2e/a.spec.ts 1\n") {
		t.Fatalf("lists after the run:\n%s", stdout)
	}
}

func TestListsShowSaysWhyAMarkedSliceRunsOnEveryChange(t *testing.T) {
	root, _, store := runEnv(t)
	c := checkout(t, root, 1)

	since := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	if _, err := store.Write(t.Context(), "lists", gitstore.Update{Ref: lists.Ref(c[1]), Files: lists.EncodeMarked(recording.Lists{
		"e2e/e2e.checkout": {unitCreate, unitTotal},
		"e2e/e2e.orders":   {unitTotal},
	}, lists.Marks{
		"e2e/e2e.checkout": {{Plugin: "sql", Reason: "no statement log", Since: since}},
		"e2e/e2e.search":   {{Plugin: "sql", Reason: "no statement log", Since: since}},
	})}); err != nil {
		t.Fatal(err)
	}

	mark := "bound to everything until a recording tells again: sql couldn't tell since 2026-03-01T03:00:00Z (no statement log)"

	stdout, stderr, code := run(t, "lists", "show", "--root", root, "--commit", c[1])
	if code != 0 || !strings.Contains(stdout, "e2e: 3 slices\n  e2e.checkout 2, "+mark+"\n  e2e.orders 1\n  e2e.search 0, "+mark+"\n") {
		t.Fatalf("show: %d %s\n%s", code, stderr, stdout)
	}

	stdout, _, code = run(t, "lists", "show", "--root", root, "--commit", c[1], "--slice", "e2e.checkout")
	if code != 0 || !strings.Contains(stdout, "e2e e2e.checkout: 2 units\n  "+mark+"\n  "+unitCreate+"\n") {
		t.Fatalf("show a slice: %d\n%s", code, stdout)
	}

	stdout, _, code = run(t, "lists", "show", "--root", root, "--commit", c[1], "--slice", "e2e.search")
	if code != 0 || !strings.Contains(stdout, "e2e e2e.search: 0 units\n  "+mark+"\n") {
		t.Fatalf("show a marked slice without a list: %d\n%s", code, stdout)
	}
}

func TestASuiteEndAttachesWhatPassedOfAFailedRun(t *testing.T) {
	root, api, store := runEnv(t)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), branchConfig+"    record: true\n")
	c := checkout(t, root, 1)

	const a, b = "e2e/e2e/a.spec.ts", "e2e/e2e/b.spec.ts"

	whole := map[string]bool{a: true, b: true}
	if _, err := (lists.Attach{
		Repo: root, Run: "main", Change: "main", Commit: c[0], To: c[0], Trusted: true,
		Recordings: recording.Lists{a: {unitTotal}, b: {unitTotal}}, Shrinkable: whole, Whole: whole,
	}).Do(t.Context(), store); err != nil {
		t.Fatal(err)
	}

	t.Setenv(envRun, "main")
	t.Setenv(envChange, "main")
	t.Setenv(envCommit, c[1])
	t.Setenv(envPR, "0")

	id := report.ID{Run: "main", Change: "main", Commit: c[1]}
	if err := report.WritePlan(t.Context(), store, id, []byte(`{"all": true, "suites": [{"suite": "e2e", "all": true, "selected": [
		{"name": "e2e/a.spec.ts", "label": "//e2e:a"}, {"name": "e2e/b.spec.ts", "label": "//e2e:b"}]}]}`)); err != nil {
		t.Fatal(err)
	}

	recorded(t, store, id, "e2e", "e2e/a.spec.ts", report.Passed, unitCreate+"\n")
	recorded(t, store, id, "e2e", "e2e/b.spec.ts", report.Failed, "# observed\n# cannot tell: react (since 2026-03-01T03:00:00Z): the page never loaded\n")

	if _, stderr, code := run(t, "report", "suite", "--root", root); code != 1 {
		t.Fatalf("suite: %d %s", code, stderr)
	}

	if got := api.status("pipelinegen/main"); strings.HasPrefix(got, "success") {
		t.Fatalf("run status %q", got)
	}

	stdout, _, code := run(t, "lists", "show", "--root", root, "--commit", c[1], "--slice", "e2e/b.spec.ts")
	want := "lists of " + c[1][:12] + ": its own lists\n"
	line := "  recorded whole at " + c[0][:12] + ": the run of everything at " + c[1][:12] + " didn't replace it (the slice didn't pass there, or couldn't be told)\n"

	mark := "  bound to everything until a recording tells again: react couldn't tell since 2026-03-01T03:00:00Z (the page never loaded)\n"
	if code != 0 || !strings.HasPrefix(stdout, want) || !strings.Contains(stdout, "e2e e2e/b.spec.ts: 1 unit\n"+mark+line+"  "+unitTotal+"\n") {
		t.Fatalf("the failed slice:\n%s", stdout)
	}

	l, src, err := lists.Nearest(t.Context(), store, root, c[1])
	if err != nil || len(src.CannotTell) != 0 || !slices.Equal(l[a], []recording.ID{unitCreate}) {
		t.Fatalf("lists %v, marks %v, %v", l, src.CannotTell, err)
	}

	stdout, _, code = run(t, "lists", "show", "--root", root, "--commit", c[1], "--slice", "e2e/a.spec.ts")
	if code != 0 || strings.Contains(stdout, "recorded whole at") || !strings.Contains(stdout, "e2e e2e/a.spec.ts: 1 unit\n  "+unitCreate+"\n") {
		t.Fatalf("the slice that passed:\n%s", stdout)
	}
}

func TestEndingARunAttachesWhatPassed(t *testing.T) {
	root, _, store := runEnv(t)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), branchConfig+"    record: true\n")
	c := checkout(t, root, 1)

	plan := func(commit string) {
		t.Helper()

		id := report.ID{Run: "main", Change: "main", Commit: commit}
		if err := report.WritePlan(t.Context(), store, id, []byte(`{"head": "`+commit+`", "all": true, "suites": [{"suite": "e2e", "all": true, "selected": [
			{"name": "e2e/a.spec.ts", "label": "//e2e:a"}, {"name": "e2e/b.spec.ts", "label": "//e2e:b"}]}]}`)); err != nil {
			t.Fatal(err)
		}

		recorded(t, store, id, "e2e", "e2e/a.spec.ts", report.Passed, unitCreate+"\n")
	}

	ended := func(commit string) string { return "main " + "main " + commit[:12] + ": suite e2e ended by hand\n" }

	plan(c[1])

	stdout, stderr, code := run(t, "end", "--run", "main", "--commit", c[1][:12], "-root", root)
	if code != 0 || stdout != ended(c[1]) {
		t.Fatalf("end: %d %q %s", code, stdout, stderr)
	}

	l, src, err := lists.Nearest(t.Context(), store, root, c[1])
	if err != nil || src.Back != 0 || !slices.Equal(l["e2e/e2e/a.spec.ts"], []recording.ID{unitCreate}) {
		t.Fatalf("the lists after the end: %v %+v %v", l, src, err)
	}

	if _, ok := l["e2e/e2e/b.spec.ts"]; ok {
		t.Fatalf("the slice that never ran has a list: %v", l)
	}

	gone := "0123456789abcdef0123456789abcdef01234567"
	plan(gone)

	stdout, stderr, code = run(t, "end", "--run", "main", "--commit", gone[:12], "-root", root)
	if code != 0 || stdout != ended(gone) {
		t.Fatalf("end of a commit the checkout lacks: %d %q %s", code, stdout, stderr)
	}
}
