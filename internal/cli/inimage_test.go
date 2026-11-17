package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

const imageConfig = `pipeline: shop
concourse: {include: ci/root.yml}
suites:
  unit:
    tests: ["**/*_test.sh"]
    image: busybox
    privileged: PRIVILEGED
    params: {GREETING: hello}
    steps:
      - prepare: command
        image: golang
        with: {run: ["true"]}
      - run: command
        with: {each: ["true"]}
`

const imageRoot = `resources:
  - name: busybox
    type: registry-image
    source: {repository: busybox, tag: 1.37.0}
    version: {digest: "sha256:` + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" + `"}
  - name: golang
    type: registry-image
    source: {repository: golang, tag: 1.27.1}
`

func imageBuild(t *testing.T, privileged bool) *localBuild {
	t.Helper()

	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "a-token-no-container-sees")

	root, work := t.TempDir(), t.TempDir()
	data := strings.Replace(imageConfig, "PRIVILEGED", strconv.FormatBool(privileged), 1)

	writeFile(t, filepath.Join(root, config.FileName), data)
	writeFile(t, filepath.Join(root, "ci", "root.yml"), imageRoot)
	gitT(t, root, "init", "-q", "-b", "main")
	gitT(t, root, "add", "-A")
	gitT(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-m", "one")

	cfg, err := config.Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}

	tool := filepath.Join(t.TempDir(), "pipelinegen")
	if err := os.WriteFile(tool, []byte("a linux pipelinegen"), 0o600); err != nil {
		t.Fatal(err)
	}

	b := &localBuild{
		cfg: cfg, cfgPath: filepath.Join(work, config.FileName), root: root, dir: filepath.Join(work, "build"), suite: "unit",
		ch: runs.Change{Commit: strings.TrimSpace(gitT(t, root, "rev-parse", "HEAD"))}, plugins: filepath.Join(root, "tools", "plugins"),
		box: &imageBox{tool: "/nonexistent/docker", linux: map[string]string{config.Platform(): tool}},
	}

	if err := os.MkdirAll(filepath.Join(b.dir, "tools"), 0o750); err != nil {
		t.Fatal(err)
	}

	return b
}

func argPairs(args []string, flag string) []string {
	var out []string

	for i := range len(args) - 1 {
		if args[i] == flag {
			out = append(out, args[i+1])
		}
	}

	return out
}

// The suite's image is its resource of the root pipeline file, by the digest the resource pins; its
// prepare runs in the prepare step's. The build's directory, the git directory (read only) and the
// tool are mounted where the build has them, and the environment is the task's: no token. It runs as
// root with the image's own HOME, as on CI, and what it wrote is handed back to this user after.
func TestASuiteRunsInItsImageAsCIRunsIt(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the user a container runs as is Linux's question")
	}

	b := imageBuild(t, false)

	img, err := b.image(t.Context(), "batch", "")
	if err != nil || img.ref != "busybox@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("batch image: %+v %v", img, err)
	}

	if prep, err := b.image(t.Context(), kindPrepare, ""); err != nil || prep.ref != "golang:1.27.1" {
		t.Fatalf("prepare image: %+v %v", prep, err)
	}

	r, back, err := b.containerRun(t.Context(), img, "batch", b.dir, []string{filepath.Join(b.dir, "tools", "pipelinegen"), "slice", "run"}, nil, config.Platform())
	if err != nil {
		t.Fatal(err)
	}

	args, err := r.Args()
	if err != nil {
		t.Fatal(err)
	}

	if slices.Contains(args, "--privileged") || slices.Contains(args, "--user") {
		t.Fatalf("args: %q", args)
	}

	env := argPairs(args, "--env")
	for _, want := range []string{"GREETING=hello", "GIT_CONFIG_KEY_0=safe.directory", "PIPELINEGEN_SUITE=unit", "PIPELINEGEN_RUN=local", "PIPELINEGEN_CONFIG=" + b.cfgPath,
		"PIPELINEGEN_RUN_FILE=" + filepath.Join(b.dir, "batches", runs.RunFile)} {
		if !slices.Contains(env, want) {
			t.Errorf("no %s in %q", want, env)
		}
	}

	if slices.ContainsFunc(env, func(kv string) bool {
		return strings.Contains(kv, "TOKEN") || strings.HasPrefix(kv, "PATH=") || strings.HasPrefix(kv, "HOME=")
	}) {
		t.Errorf("this machine's environment: %q", env)
	}

	common := filepath.Join(b.root, ".git")
	if resolved, err := filepath.EvalSymlinks(common); err == nil {
		common = resolved
	}

	vols := argPairs(args, "--volume")
	for _, want := range []string{filepath.Dir(b.dir) + ":" + filepath.Dir(b.dir), common + ":" + common + ":ro", b.box.linux[config.Platform()] + ":" + b.box.linux[config.Platform()] + ":ro"} {
		if !slices.Contains(vols, want) {
			t.Errorf("no --volume %s in %q", want, vols)
		}
	}

	if link, err := os.Readlink(filepath.Join(b.dir, "tools", "pipelinegen")); err != nil || link != b.box.linux[config.Platform()] {
		t.Errorf("tools/pipelinegen: %s %v", link, err)
	}

	if back == nil || back.Image != r.Image || !slices.Equal(back.Cmd, []string{"chown", "-R", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()), filepath.Dir(b.dir)}) {
		t.Errorf("hand back: %+v", back)
	}
}

// --as-me runs the steps as this user, whose home the image lacks; nothing needs handing back.
func TestAsMeRunsTheStepsAsThisUser(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the user a container runs as is Linux's question")
	}

	b := imageBuild(t, false)
	b.box.flags.asMe = true

	img, err := b.image(t.Context(), "batch", "")
	if err != nil {
		t.Fatal(err)
	}

	r, back, err := b.containerRun(t.Context(), img, "batch", b.dir, []string{"true"}, nil, config.Platform())
	if err != nil {
		t.Fatal(err)
	}

	args, err := r.Args()
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(argPairs(args, "--user"), []string{strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())}) ||
		!slices.Contains(argPairs(args, "--env"), "HOME=/tmp") || back != nil {
		t.Fatalf("args: %q, hand back %+v", args, back)
	}
}

// A privileged suite runs privileged and as root, as on CI, even with --as-me: it starts its own
// container engine. What it wrote is handed back when it ends.
func TestAPrivilegedSuiteRunsAsRootAndHandsItsFilesBack(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the user a container runs as is Linux's question")
	}

	b := imageBuild(t, true)
	b.box.flags.asMe = true

	img, err := b.image(t.Context(), "batch", "")
	if err != nil {
		t.Fatal(err)
	}

	r, back, err := b.containerRun(t.Context(), img, "batch", b.dir, []string{"true"}, nil, config.Platform())
	if err != nil {
		t.Fatal(err)
	}

	args, err := r.Args()
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(args, "--privileged") || slices.Contains(args, "--user") || back == nil {
		t.Fatalf("args: %q, hand back %+v", args, back)
	}
}

// An image named nowhere in the root pipeline file is refused before anything runs.
func TestAnImageTheRootFileDoesntDeclareIsRefused(t *testing.T) {
	b := imageBuild(t, false)

	s := b.cfg.Suites["unit"]
	s.Image = "node"
	b.cfg.Suites["unit"] = s

	if _, err := b.image(t.Context(), "batch", ""); err == nil || !strings.Contains(err.Error(), "suites.unit.image: no such image: it names node, which ci/root.yml doesn't declare") {
		t.Fatalf("err = %v", err)
	}
}
