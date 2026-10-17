package check

import (
	"strings"
	"testing"
)

func TestAnImageMustBeARegistryImageOfTheRootFile(t *testing.T) {
	const root = `common: &source {repository: registry.example/ci/node, username: ci, password: ((registry_password))}
resources:
- {name: node-image, type: registry-image, source: *source}
- {name: tools, type: git, source: {uri: https://example.com/tools.git, password: ((registry_password))}}
`

	for _, c := range []struct {
		name, root, suite, prepare string
		want                       []string
	}{
		{"declared", root, "node-image", "node-image", nil},
		{"not declared", root, "go-image", "", []string{"suites.e2e.image names go-image, which ci/root.yml doesn't declare"}},
		{"not a registry image", root, "", "tools", []string{"concourse.planner.prepare_image names tools, which ci/root.yml doesn't declare as a registry-image"}},
		{"no root file", "", "node-image", "", []string{"suites.e2e.image names node-image, but concourse.include names no root pipeline file"}},
		{"unreadable", "resources: [", "node-image", "", []string{"ci/root.yml can't be read as YAML"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := good(t)
			conf := cfg()

			if c.root != "" {
				m["ci/root.yml"] = c.root
				conf.Concourse.Include = "ci/root.yml"
			}

			e2e := conf.Suites["e2e"]
			e2e.Image = c.suite
			conf.Suites["e2e"] = e2e
			conf.Concourse.Planner.PrepareImage = c.prepare

			got := images(conf, m.tree())
			if len(got) != len(c.want) {
				t.Fatalf("findings %v", got)
			}

			for i, w := range c.want {
				if !strings.Contains(got[i].Problem, w) {
					t.Errorf("%q, want %q", got[i].Problem, w)
				}

				if strings.Contains(got[i].String(), "registry_password") || strings.Contains(got[i].String(), "registry.example") {
					t.Errorf("a value of the root file in %q", got[i])
				}
			}
		})
	}
}
