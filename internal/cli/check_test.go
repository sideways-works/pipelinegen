package cli

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestCheckCommand(t *testing.T) {
	r := testrepo.New(t)

	out, _, code := run(t, "check", "--root", r.Dir)
	if code != 1 || !strings.Contains(out, ": no slice depends on this file") || !strings.Contains(out, "\n  fix: ") {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	out, _, code = run(t, "check", "--root", r.Dir, "--staged")
	if code != 0 || out != "" {
		t.Fatalf("staged, nothing touched: exit %d:\n%s", code, out)
	}

	r.Write("docs/new.md", "new\n")
	r.Git("add", "docs/new.md")

	out, _, code = run(t, "check", "--root", r.Dir, "--staged")
	if code != 1 || !strings.HasPrefix(out, "docs/new.md: no slice depends on this file") {
		t.Fatalf("staged, a new file: exit %d:\n%s", code, out)
	}
}

func TestCheckSummaryCountsByDirectory(t *testing.T) {
	r := testrepo.New(t)
	r.Write("docs/a.md", "a\n")
	r.Write("docs/b.md", "b\n")
	r.Write("stray.txt", "s\n")
	r.Commit("uncovered")

	full, _, _ := run(t, "check", "--root", r.Dir)

	n := 0

	for line := range strings.SplitSeq(strings.TrimSuffix(full, "\n"), "\n") {
		if !strings.HasPrefix(line, " ") {
			n++
		}
	}

	out, _, code := run(t, "check", "--root", r.Dir, "--summary")
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	if !strings.Contains(out, "3\tuncovered-file\tdocs/\n") || !regexp.MustCompile(`(?m)^\d+\tuncovered-file\t\.$`).MatchString(out) ||
		!strings.HasSuffix(out, "\n"+strconv.Itoa(n)+" findings\n") || strings.Contains(out, "fix:") {
		t.Fatalf("summary:\n%s\nfull:\n%s", out, full)
	}
}
