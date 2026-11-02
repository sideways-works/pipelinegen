package cli

import (
	"bytes"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

var noticeRunner = strings.Replace(fakeRunner, `echo '{"files": ["pipeline.yml"]}'`,
	`printf '%s\n' '{"files": ["pipeline.yml"], "notices": ["placement: pr-go is placed by no rule", "placement: pr-go is placed by no rule", "two\nlines"]}'`, 1)

func TestARunnersNoticesAreSaidOnceAndFailNothing(t *testing.T) {
	if !strings.Contains(noticeRunner, "notices") {
		t.Fatal("the fake runner answers no notices")
	}

	r := testrepo.New(t)

	cfg := readText(t, filepath.Join(r.Dir, "pipelinegen.yaml")) +
		"plugins:\n  - {name: ci, kind: runner, repository: o/ci, commit: " + strings.Repeat("0", 40) + "}\n"
	r.Write("pipelinegen.yaml", cfg)

	plugins := t.TempDir()
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(plugins, "ci"), []byte(noticeRunner)); err != nil {
		t.Fatal(err)
	}

	c, err := config.Load(filepath.Join(r.Dir, "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	text, notices, err := pipelineText(t.Context(), c, r.Dir, "", plugins)
	if err != nil || string(text) != "from the runner: fixture\n" {
		t.Fatalf("pipeline: %q %v", text, err)
	}

	want := []string{"placement: pr-go is placed by no rule", "two lines"}
	if !slices.Equal(notices, want) {
		t.Fatalf("notices %q, want %q", notices, want)
	}

	var said bytes.Buffer

	sayNotices(&said, "ci", notices)

	if got := said.String(); got != "pipelinegen pipeline: runner ci: placement: pr-go is placed by no rule\npipelinegen pipeline: runner ci: two lines\n" {
		t.Errorf("said %q", got)
	}

	if stdout, _, code := run(t, "pipeline", "-root", r.Dir, "-plugins", plugins); code != 0 || stdout != "from the runner: fixture\n" {
		t.Fatalf("pipeline: exit %d: %q", code, stdout)
	}

	_, _, without := run(t, "check", "-root", r.Dir, "-plugins", t.TempDir())

	stdout, _, code := run(t, "check", "-root", r.Dir, "-plugins", plugins)
	if code != without || !strings.Contains(stdout, "warning: pipelinegen.yaml: runner ci: placement: pr-go is placed by no rule\n") ||
		!strings.Contains(stdout, "warning: pipelinegen.yaml: runner ci: two lines\n") {
		t.Fatalf("check: exit %d, %d without the runner: %s", code, without, stdout)
	}
}
