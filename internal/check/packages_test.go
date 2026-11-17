package check

import (
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
)

func TestPackageModes(t *testing.T) {
	const root = `resources:
- {name: golang, type: registry-image, source: {repository: golang, tag: "1.27"}, version: {digest: "sha256:aaaa"}}
- {name: node, type: registry-image, source: {repository: node, tag: "24"}}
`

	m := good(t)
	m["ci/root.yml"] = root

	conf := cfg()
	conf.Concourse.Include = "ci/root.yml"
	conf.Packages = config.Packages{{Name: "jq"}}
	conf.Concourse.Planner.Packages = config.Packages{{Name: "curl"}}
	conf.Suites["backend"] = config.Suite{Tests: []string{"**/*_test.go"}, Image: "golang", Steps: []config.Step{{Run: "go"}}}
	conf.Suites["e2e"] = config.Suite{Tests: []string{"e2e/**/*.spec.ts"}, Image: "node", Steps: []config.Step{{Run: "command"}}}
	conf.Suites["lint"] = config.Suite{Tag: "slice-lint", RunSlice: "ci/lint.yml", Image: "node"}

	byText := func(got []Finding) map[string]Finding {
		out := map[string]Finding{}
		for _, f := range got {
			out[f.Problem] = f
		}

		return out
	}

	got := packageModes(conf, m.tree())
	if len(got) != 4 {
		t.Fatalf("findings: %v", got)
	}

	want := map[string]func(Finding) bool{
		"concourse.planner.packages (curl) are installed by each of pipelinegen's own steps": func(f Finding) bool { return f.Note },
		"suite backend's packages (jq) in golang come in a cached image, ghcr":               func(f Finding) bool { return f.Note },
		"suite e2e's packages (jq) in node are installed by each task, with a download cache: node is a tag, which can move": func(f Finding) bool {
			return f.Note
		},
		"suite lint runs task files (prepare, run_slice or whole_suite), whose scripts the runner can't put an install in front of": func(f Finding) bool {
			return f.Warning && strings.Contains(f.Problem, "on CI those tasks run without jq")
		},
	}

	conf.Images = config.Images{Registry: "ghcr.io/o/ci"}
	got = packageModes(conf, m.tree())

	for prefix, ok := range want {
		found := false

		for text, f := range byText(got) {
			if strings.HasPrefix(text, prefix) && ok(f) && f.Kind == KindPackages {
				found = true
			}
		}

		if !found {
			t.Errorf("no finding %q in %v", prefix, got)
		}
	}

	if len(Failures(got)) > 0 {
		t.Fatalf("a mode is no failure: %v", Failures(got))
	}

	conf.Images, conf.Packages, conf.Concourse.Planner.Packages = config.Images{}, nil, nil
	if got := packageModes(conf, m.tree()); len(got) > 0 {
		t.Fatalf("no packages, no findings: %v", got)
	}
}
