package testenv

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func init() { Git() } //nolint:gochecknoinits // before any test runs git

func TestGitSeesNoHelperOfTheMachine(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "git", "config", "--list", "--show-scope")
	// outside any repository: a repository's own config is the test's
	cmd.Dir = t.TempDir()

	out, err := cmd.CombinedOutput()
	if err != nil && len(out) > 0 {
		t.Fatalf("git config: %v: %s", err, out)
	}

	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if scope, _, _ := strings.Cut(line, "\t"); scope == "global" || scope == "system" {
			name, _, _ := strings.Cut(strings.TrimPrefix(line, scope+"\t"), "=")
			t.Errorf("git sees %s config from outside the test: %s", scope, name)
		}
	}
}

func TestABuildsEnvironmentNeverReachesATest(t *testing.T) {
	build := map[string]string{
		"PIPELINEGEN_RUN": "main", "PIPELINEGEN_SUITE": "go", "PIPELINEGEN_BATCH": "0",
		"PIPELINEGEN_RECORD": "true", "PIPELINEGEN_RUN_FILE": "batches/run.json", "PIPELINEGEN_JOB_URL": "https://ci.example/job",
		"PIPELINEGEN_TRIGGER": "trigger", "GITHUB_TOKEN": "x",
	}
	for k, v := range build {
		t.Setenv(k, v)
	}

	t.Setenv("PIPELINEGEN_TEST_BAZEL_DIFF", "/opt/bazel-diff")
	// a test process, not a child of one
	t.Setenv(scrubbedMark, "")
	os.Unsetenv(scrubbedMark) //nolint:errcheck // t.Setenv restores it

	Git()

	for k := range build {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s=%s reached the test", k, v)
		}
	}

	if os.Getenv("PIPELINEGEN_TEST_BAZEL_DIFF") != "/opt/bazel-diff" {
		t.Error("a test knob was dropped")
	}

	// a child the test starts keeps what the test set for it
	t.Setenv("PIPELINEGEN_RUN", "the test's own")
	Git()

	if os.Getenv("PIPELINEGEN_RUN") != "the test's own" {
		t.Error("a child's environment, set by its test, was dropped")
	}
}

func TestATestProcessLeavesNothingInTmp(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()

	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^$")

	cmd.Env = append(os.Environ(), "TMPDIR="+tmp)

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
		t.Fatalf("left %v (%v)", entries, err)
	}
}
