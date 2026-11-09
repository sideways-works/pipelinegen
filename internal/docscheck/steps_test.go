package docscheck

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/cli"
	"github.com/sideways-works/pipelinegen/internal/toolcalls"
)

const stepsHeading = "Commands the generated pipeline calls"

var (
	backticked = regexp.MustCompile("`([^`]+)`")
	toolCall   = regexp.MustCompile(`^(?:tools/)?pipelinegen (.+)$`)
)

func TestTheStepTableIsTheTools(t *testing.T) {
	page := read(t, filepath.Join(repoRoot(t), "docs", "contracts.md"))

	var body string

	for _, s := range Sections(page, 2) {
		if s.Heading == stepsHeading {
			body = s.Body
		}
	}

	if body == "" {
		t.Fatalf("contracts.md has no section %q", stepsHeading)
	}

	cmds := cli.ToolCommands()
	seen := map[string]bool{}
	rows := 0

	for line := range strings.SplitSeq(body, "\n") {
		cells := strings.Split(strings.Trim(line, "| "), " | ")
		if !strings.HasPrefix(line, "|") || len(cells) != 5 || cells[0] == "Where" || strings.HasPrefix(cells[0], "---") {
			continue
		}

		rows++

		for _, m := range backticked.FindAllStringSubmatch(cells[1], -1) {
			call := toolCall.FindStringSubmatch(m[1])
			if call == nil {
				continue
			}

			c, err := cmds.Parse(strings.Fields(call[1]))
			if err != nil {
				t.Errorf("%s: %v", m[1], err)

				continue
			}

			seen[c.Command] = true

			// the batch task is the repository's own: section (a) says
			// what it has
			if cells[2] == "see (a)" {
				continue
			}

			env := map[string]string{toolcalls.EnvToken: "", toolcalls.EnvTrigger: "trigger"}

			reads, writes, problems := cmds.Mounts(c, env, false)
			all, _, _ := cmds.Mounts(c, env, true)

			for _, p := range problems {
				t.Errorf("%s: %s", m[1], p)
			}

			reads = append(reads, "tools")

			var recording []string

			for _, d := range all {
				if !slices.Contains(reads, d) {
					recording = append(recording, d)
				}
			}

			plain, rec, _ := strings.Cut(cells[2], "recording:")

			for _, c := range []struct {
				what, cell string
				want       []string
			}{
				{"inputs", plain, reads},
				{"inputs in a recording run", rec, recording},
				{"outputs", cells[3], writes},
				{"environment", cells[4], wantEnv(cmds[c.Command].Needs)},
			} {
				if got, want := names(c.cell), sorted(c.want); !slices.Equal(got, want) {
					t.Errorf("%s: %s %v, the tool says %v", m[1], c.what, got, want)
				}
			}
		}
	}

	if rows == 0 {
		t.Fatal("no rows in the step table")
	}

	for _, step := range toolcalls.Steps {
		if !seen[step] {
			t.Errorf("the step table has no row for pipelinegen %s", step)
		}
	}
}

func wantEnv(n toolcalls.Needs) []string {
	var out []string

	if n.Token == toolcalls.TokenRequired {
		out = append(out, toolcalls.EnvToken)
	}

	if n.ConcourseURL {
		out = append(out, toolcalls.EnvConcourseURL)
	}

	if n.RunFromEnv {
		out = append(out, toolcalls.EnvTrigger)
	}

	if n.Bazel {
		out = append(out, toolcalls.EnvBazelisk)
	}

	return out
}

func names(cell string) []string {
	var out []string

	for _, m := range backticked.FindAllStringSubmatch(cell, -1) {
		out = append(out, m[1])
	}

	return sorted(out)
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)

	return slices.Compact(out)
}
