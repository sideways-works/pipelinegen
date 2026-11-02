package cli

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
)

const recordConfig = `pipeline: shop
suites:
  e2e:
    tests: ["e2e/*.spec.ts"]
    bind: recorded
    run_slice: ci/e2e-slice.yml
plugins:
  - {name: units, kind: sbom, repository: example/units, commit: 0123456789abcdef0123456789abcdef01234567}
  - {name: fake, kind: correlator, repository: example/fake, commit: 0123456789abcdef0123456789abcdef01234567, config: {}}
`

const recordTask = `platform: linux
image_resource: {type: registry-image, source: {repository: example/browsers, tag: "1"}}
inputs: [{name: source}, {name: tools}, {name: batches}]
outputs: [{name: result}, {name: coverage}]
params: {GREETING: hello}
run: {path: sh, args: [source/ci/e2e-slice.sh]}
`

const recordScript = `set -eu
test "$GREETING" = hello
tools/pipelinegen slice setup-end --root source
tools/pipelinegen slice list | cut -f1 | while read -r name; do
	tools/pipelinegen slice start "$name" --root source
	mkdir -p "coverage/$name/fake"
	echo "unit:$name" > "coverage/$name/fake/touched.txt"
	mkdir -p "$(dirname "result/$name.json")"
	echo '{"passed": true}' > "result/$name.json"
	tools/pipelinegen slice end "$name" --root source
done
`

func recordRepo(t *testing.T) (string, map[string]string) {
	t.Helper()

	return recordRepoWith(t, "{}")
}

func recordRepoWith(t *testing.T, fakeConfig string) (string, map[string]string) {
	t.Helper()

	root := t.TempDir()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for p, body := range map[string]string{
		config.FileName:            strings.Replace(recordConfig, "config: {}}", "config: "+fakeConfig+"}", 1),
		"ci/e2e-slice.yml":         recordTask,
		"ci/e2e-slice.sh":          recordScript,
		"e2e/cart.spec.ts":         "test('cart')\n",
		"e2e/pay.spec.ts":          "test('pay')\n",
		"tools/plugins/fake":       "#!/bin/sh\nFAKE=1 exec '" + exe + "' \"$@\"\n",
		"tools/plugins/units.note": "an sbom a recording never asks\n",
	} {
		writeFile(t, filepath.Join(root, p), body)
	}

	if err := os.Chmod(filepath.Join(root, "tools/plugins/fake"), 0o700); err != nil { //nolint:gosec // the test's own plugin
		t.Fatal(err)
	}

	gitT(t, root, "init", "-q", "-b", "main")
	gitT(t, root, "add", "-A", ":!tools")
	gitT(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-m", "one")

	return root, map[string]string{envGitURL: "", envToken: "", envJobURL: "", envPipelineStep: "", envRun: "", envConfig: ""}
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}

	return strings.TrimSpace(string(out))
}

func TestRecordAttachesListsOnThisMachine(t *testing.T) {
	root, env := recordRepo(t)

	stdout, stderr, code := runIn(t, root, env, "record", "--suite", "e2e", "--root", root)
	if code != 0 || !strings.Contains(stdout, "recorded 2 slices of e2e at ") {
		t.Fatalf("record: %d %s\n%s", code, stdout, stderr)
	}

	store := filepath.Join(gitT(t, root, "rev-parse", "--path-format=absolute", "--git-common-dir"), "pipelinegen", "local.git")
	if _, err := os.Stat(filepath.Join(store, "HEAD")); err != nil {
		t.Fatalf("no local store: %v", err)
	}

	if got := gitT(t, root, "remote"); got != "" {
		t.Fatalf("a remote appeared: %s", got)
	}

	cfg, err := config.Load(filepath.Join(root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	head := gitT(t, root, "rev-parse", "HEAD")

	var warn strings.Builder

	l, _ := baseLists(t.Context(), cfg, root, head, listsRemote, &warn)
	if !strings.HasPrefix(warn.String(), "pipelinegen affected: lists from the local store (recorded on this machine): "+store) {
		t.Fatalf("the local store isn't named first: %q", warn.String())
	}

	for _, slice := range []string{"e2e/cart.spec.ts", "e2e/pay.spec.ts"} {
		if ids := l["e2e/"+slice]; len(ids) != 1 || string(ids[0]) != "unit:"+slice {
			t.Errorf("%s's list: %v (all: %v)", slice, ids, l)
		}
	}

	gitT(t, root, "remote", "add", "origin", storetest.Bare(t))

	warn.Reset()

	if l, _ := baseLists(t.Context(), cfg, root, head, listsRemote, &warn); len(l) == 0 ||
		!strings.HasPrefix(warn.String(), "pipelinegen affected: lists from the local store") {
		t.Fatalf("with an origin: %q %v", warn.String(), l)
	}

	cfg.Git.Remote = storetest.Bare(t)

	warn.Reset()

	if l, _ := baseLists(t.Context(), cfg, root, head, listsRemote, &warn); len(l) != 0 || strings.Contains(warn.String(), "local store") {
		t.Fatalf("a named remote: %q %v", warn.String(), l)
	}

	if _, err := fromFlags(true, true); err == nil {
		t.Fatal("--no-lists with --local")
	}
}

func TestRecordRefusesAChangedTree(t *testing.T) {
	root, env := recordRepo(t)

	writeFile(t, filepath.Join(root, "e2e/cart.spec.ts"), "test('cart, changed')\n")
	writeFile(t, filepath.Join(root, "e2e/new.spec.ts"), "test('new')\n")

	_, stderr, code := runIn(t, root, env, "record", "--suite", "e2e", "--root", root)
	if code != 1 || !strings.Contains(stderr, "commit these first, then record") ||
		!strings.Contains(stderr, "e2e/cart.spec.ts") || !strings.Contains(stderr, "e2e/new.spec.ts") {
		t.Fatalf("%d %s", code, stderr)
	}
}

func TestRecordSaysWhereATaskFailed(t *testing.T) {
	root, env := recordRepo(t)

	writeFile(t, filepath.Join(root, "ci/e2e-slice.sh"), "echo 'browsers: not found' >&2\nexit 7\n")
	gitT(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-qam", "no browsers")

	_, stderr, code := runIn(t, root, env, "record", "--suite", "e2e", "--root", root)
	if code != 1 || !strings.Contains(stderr, "browsers: not found") ||
		!strings.Contains(stderr, "it ran on this machine, not in its image example/browsers:1, so the machine needs what that image has") ||
		!strings.Contains(stderr, "source is a fresh checkout of HEAD: what your working tree has installed or built is not in it") {
		t.Fatalf("%d %s", code, stderr)
	}
}

func TestRecordNeedsARecordedSuite(t *testing.T) {
	root, env := recordRepo(t)

	writeFile(t, filepath.Join(root, config.FileName), strings.Replace(recordConfig, "    bind: recorded\n", "", 1))
	gitT(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-qam", "graph")

	if _, stderr, code := runIn(t, root, env, "record", "--suite", "e2e", "--root", root); code != 1 || !strings.Contains(stderr, "isn't bound by recording") {
		t.Fatalf("%d %s", code, stderr)
	}

	if _, stderr, code := runIn(t, root, env, "record", "--root", root); code != 2 || !strings.Contains(stderr, "--suite is needed") {
		t.Fatalf("no suite: %d %s", code, stderr)
	}
}

func TestALocalConfigIsRefusedInAPipeline(t *testing.T) {
	root, env := recordRepo(t)
	other := filepath.Join(t.TempDir(), "other.yaml")
	writeFile(t, other, recordConfig)

	for name, more := range map[string]map[string]string{
		"a job's step":      {envJobURL: "https://ci/jobs/x"},
		"a pipeline's step": {envPipelineStep: "1"},
		"a declared run":    {envRun: "main"},
	} {
		e := maps.Clone(env)
		e[envConfig] = other
		maps.Copy(e, more)

		if _, stderr, code := runIn(t, root, e, "slices", "--root", root); code != 2 || !strings.Contains(stderr, "is for a local recording only") {
			t.Errorf("%s: %d %s", name, code, stderr)
		}
	}

	e := maps.Clone(env)
	e[envConfig], e[envRun] = other, localRun

	if _, stderr, code := runIn(t, root, e, "slices", "--root", root); code != 0 {
		t.Errorf("the local run: %d %s", code, stderr)
	}
}

func TestRecordSourceIsInsideTheBuild(t *testing.T) {
	root, env := recordRepo(t)

	writeFile(t, filepath.Join(root, config.FileName), strings.Replace(recordConfig,
		"    run_slice: ci/e2e-slice.yml\n", "    run_slice: ci/e2e-slice.yml\n    prepare: ci/e2e-prepare.yml\n", 1))
	writeFile(t, filepath.Join(root, "ci/e2e-prepare.yml"), `platform: linux
image_resource: {type: registry-image, source: {repository: example/browsers, tag: "1"}}
inputs: [{name: source}]
outputs: [{name: prepared}]
run: {path: sh, args: [-c, "cd source && echo built > ../prepared/app.txt"]}
`)
	writeFile(t, filepath.Join(root, "ci/e2e-slice.sh"), `set -eu
(cd source && test "$(cat ../prepared/app.txt)" = built)
tools/pipelinegen slice setup-end --root source
tools/pipelinegen slice list | cut -f1 | while read -r name; do
	tools/pipelinegen slice start "$name" --root source
	(cd source && mkdir -p "../coverage/$name/fake" && echo "unit:$name" > "../coverage/$name/fake/touched.txt")
	(cd source && mkdir -p "$(dirname "../result/$name.json")" && echo '{"passed": true}' > "../result/$name.json")
	tools/pipelinegen slice end "$name" --root source
done
`)
	writeFile(t, filepath.Join(root, "ci/e2e-slice.yml"), strings.Replace(recordTask, "{name: batches}]", "{name: batches}, {name: prepared}]", 1))
	gitT(t, root, "add", "-A", ":!tools")
	gitT(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-qm", "tasks that cd into source")

	work := t.TempDir()

	stdout, stderr, code := runIn(t, root, env, "record", "--suite", "e2e", "--root", root, "--work", work)
	if code != 0 || !strings.Contains(stdout, "recorded 2 slices of e2e at ") {
		t.Fatalf("record: %d %s\n%s", code, stdout, stderr)
	}

	for _, p := range []string{"prepared/app.txt", "coverage/e2e/cart.spec.ts/fake/touched.txt"} {
		if _, err := os.Stat(filepath.Join(work, "build", p)); err != nil {
			t.Errorf("%s isn't in the build: %v", p, err)
		}
	}

	for _, p := range []string{"prepared", "coverage", "result"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(root), p)); err == nil {
			t.Errorf("%s was written beside the working tree", p)
		}
	}

	if got := gitT(t, root, "worktree", "list"); strings.Count(got, "\n") != 0 {
		t.Errorf("the build's checkout is left: %s", got)
	}

	cfg, err := config.Load(filepath.Join(root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	var warn strings.Builder

	l, _ := baseLists(t.Context(), cfg, root, gitT(t, root, "rev-parse", "HEAD"), listsRemote, &warn)
	if ids := l["e2e/e2e/cart.spec.ts"]; len(ids) != 1 || string(ids[0]) != "unit:e2e/cart.spec.ts" {
		t.Fatalf("lists: %v (%s)", l, warn.String())
	}
}

func TestListsShowReadsTheLocalStore(t *testing.T) {
	root, env := recordRepo(t)

	if _, stderr, code := runIn(t, root, env, "record", "--suite", "e2e", "--root", root); code != 0 {
		t.Fatalf("record: %d %s", code, stderr)
	}

	store := filepath.Join(gitT(t, root, "rev-parse", "--path-format=absolute", "--git-common-dir"), "pipelinegen", "local.git")
	first := "pipelinegen lists show: lists from the local store (recorded on this machine): " + store

	stdout, stderr, code := runIn(t, root, env, "lists", "show", "--root", root)
	if code != 0 || !strings.HasPrefix(stderr, first) || !strings.Contains(stdout, "e2e/cart.spec.ts 1") {
		t.Fatalf("lists show: %d\n%s\n%s", code, stdout, stderr)
	}

	writeFile(t, filepath.Join(root, config.FileName), recordConfig+"git:\n  remote: "+storetest.Bare(t)+"\n")

	if stdout, stderr, code := runIn(t, root, env, "lists", "show", "--root", root); code != 0 || strings.Contains(stderr, "local store") || strings.Contains(stdout, "cart.spec.ts 1") {
		t.Fatalf("a named remote: %d\n%s\n%s", code, stdout, stderr)
	}

	if stdout, stderr, code := runIn(t, root, env, "lists", "show", "--local", "--root", root); code != 0 || !strings.HasPrefix(stderr, first) || !strings.Contains(stdout, "e2e/cart.spec.ts 1") {
		t.Fatalf("--local: %d\n%s\n%s", code, stdout, stderr)
	}
}

func TestRecordHoldsTheObservingStepToWhatItSees(t *testing.T) {
	for _, tc := range []struct {
		buildRoot string
		ok        bool
	}{
		{"source/dist", true},
		{"coverage/build", true},
		{"prepared/tree", false},
	} {
		t.Run(tc.buildRoot, func(t *testing.T) {
			root, env := recordRepoWith(t, "{build_root: "+tc.buildRoot+"}")

			stdout, stderr, code := runIn(t, root, env, "record", "--suite", "e2e", "--root", root)
			if tc.ok {
				if code != 0 || !strings.Contains(stdout, "recorded 2 slices of e2e at ") {
					t.Fatalf("record: %d %s\n%s", code, stdout, stderr)
				}

				return
			}

			if code == 0 || !strings.Contains(stderr, `fake config.build_root = "prepared/tree": the step that observes sees only the tree (source/) and the coverage output (coverage/)`) ||
				!strings.Contains(stderr, "have the batch task write it into its coverage output") || strings.Contains(stderr, "slice e2e/cart.spec.ts:") {
				t.Fatalf("record: %d %s\n%s", code, stdout, stderr)
			}
		})
	}
}

func TestRecordingsStartedInOneSecondAreRunsOfTheirOwn(t *testing.T) {
	at := time.Date(2026, 11, 14, 13, 39, 22, 1, time.UTC)

	first, second := localChange(at), localChange(at.Add(time.Microsecond))
	if first == second {
		t.Fatalf("both are %s", first)
	}

	if first != "local-20261114-133922-000000001" {
		t.Fatalf("named %s", first)
	}
}
