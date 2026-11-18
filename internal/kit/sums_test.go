package kit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/plugin"
)

// fetchScript checks each plugin a release pins in tools/plugins, as a task that fetched it
// would: by its line in the list beside it when it is pinned by sums, else by its sha256. The
// sha256-only kind reads no list, and the dropped kind removes the list once it checked by it.
// It then calls the tool by a path the kit can't read.
func fetchScript(kind string) string {
	sums := `if [ "$sums" != - ]; then
    echo "$sums  $f.SHA256SUMS" | sha256sum -c - >/dev/null
    sha=$(sed -n "s/^\([0-9a-f]\{64\}\)  $prog-linux-amd64\$/\1/p" "$f.SHA256SUMS")
  fi
`

	switch kind {
	case "runner-sha256-only":
		sums = ""
	case "runner-list-dropped":
		sums += `  rm -f "$f.SHA256SUMS"
`
	}

	return `printf '%s\n' "$TOOLS" | while read -r name version sha sums prog; do
  [ -n "$name" ] && [ "$version" != - ] || continue
  f=tools/plugins/$name
  ` + sums + `  if [ "$sha" = - ]; then
    echo "no checksum for $f" >&2
    exit 1
  fi
  echo "$sha  $f" | sha256sum -c - >/dev/null
done
t=tools/pipeline
"${t}gen" version
`
}

func sumsFake(kind string) map[string]plugin.Handler {
	return map[string]plugin.Handler{
		plugin.CallDescribe: describe(plugin.Description{Kind: plugin.KindRunner, Capabilities: []string{plugin.CapPlacement}}),
		plugin.CallPipeline: func(in []byte) (any, error) {
			var req plugin.PipelineRequest
			if err := decode(in, &req); err != nil {
				return nil, err
			}

			var tools strings.Builder

			for _, t := range req.Spec.Tools {
				if t.Kind == plugin.ToolPipelinegen || t.Kind == plugin.ToolBazelisk {
					continue
				}

				fmt.Fprintf(&tools, "%s %s %s %s %s\n", t.Name, orDash(t.Version), orDash(t.SHA256), orDash(t.Sums), config.ProgramOf(t.Repository, t.Path))
			}

			task := map[string]any{"task": "start", "config": map[string]any{
				"params": map[string]any{plugin.EnvPipelineStep: "1", "TOOLS": tools.String()},
				"run":    map[string]any{"path": "sh", "args": []any{"-ec", fetchScript(kind)}},
			}}

			text, err := yaml.Marshal(map[string]any{"jobs": []any{map[string]any{"name": "main-go", "plan": []any{task}}}})
			if err != nil {
				return nil, fmt.Errorf("pipeline: %w", err)
			}

			if err := os.WriteFile(filepath.Join(req.Work, "p.yml"), text, 0o600); err != nil {
				return nil, fmt.Errorf("pipeline: %w", err)
			}

			return plugin.PipelineAnswer{Files: []string{"p.yml"}}, nil
		},
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}

	return s
}

func TestAToolPinnedBySumsIsCheckedByItsList(t *testing.T) {
	for kind, want := range map[string][]string{
		"runner-listed":      nil,
		"runner-sha256-only": {"(a plugin pinned by sums) job main-go, task start: exit 1: no checksum for tools/plugins/github", "(the runner pinned by sums) job main-go, task start: exit 1: no checksum for tools/plugins/runner"},
		"runner-list-dropped": {
			"(a plugin pinned by sums) job main-go, task start: tools/plugins/github.SHA256SUMS isn't the list the pin's sums names",
			"(the runner pinned by sums) job main-go, task start: tools/plugins/runner.SHA256SUMS isn't the list the pin's sums names",
		},
	} {
		var got *Result

		for _, r := range Run(t.Context(), fakeOptions(t, kind, "", false, false)) {
			if r.Check == sumsCheck {
				got = &r
			}
		}

		switch {
		case got == nil || got.Skipped:
			t.Errorf("%s: the check didn't run: %v", kind, got)
		case want == nil && got.Problem != "":
			t.Errorf("%s: %s", kind, got.Problem)
		}

		for _, w := range want {
			if got != nil && !strings.Contains(got.Problem, w) {
				t.Errorf("%s: %q, want %q", kind, got.Problem, w)
			}
		}
	}
}

func TestARunnerWithoutTasksTheKitRunsSkipsTheSumsCheck(t *testing.T) {
	for _, r := range Run(t.Context(), fakeOptions(t, "runner", "", false, false)) {
		if r.Check == sumsCheck && !r.Skipped {
			t.Errorf("%+v", r)
		}
	}
}
