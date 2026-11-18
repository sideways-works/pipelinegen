package spec

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/packages"
	"github.com/sideways-works/pipelinegen/plugin"
)

const packagesConfig = `
pipeline: shop
packages: [git]
images: {registry: ghcr.io/owner/ci-images, credentials: registry_push, pull_credentials: registry_pull}
concourse:
  include: ci/pipeline.yml
  planner:
    packages: [curl]
suites:
  pinned:
    tests: ["a/**"]
    run_slice: ci/a.yml
    image: golang
    packages: [jq]
  same:
    tests: ["b/**"]
    run_slice: ci/b.yml
    image: golang
    packages: [jq]
  tagged:
    tests: ["c/**"]
    run_slice: ci/c.yml
    image: node
  bare:
    tests: ["d/**"]
    run_slice: ci/d.yml
`

const include = `
resources:
  - name: golang
    type: registry-image
    source: {repository: golang, tag: "1.27"}
    version: {digest: "sha256:aaaa"}
  - name: node
    type: registry-image
    source: {repository: node, tag: "24"}
`

func TestSpecPackages(t *testing.T) {
	cfg, err := config.Parse([]byte(packagesConfig))
	if err != nil {
		t.Fatal(err)
	}

	s, err := FromInclude(cfg, []byte(include))
	if err != nil {
		t.Fatal(err)
	}

	if p := s.PlannerPackages; p == nil || !slices.Equal(p.Names, []string{"curl"}) || !strings.Contains(p.Install, "pg_want='curl'") {
		t.Fatalf("planner packages: %+v", p)
	}

	suites := map[string]plugin.SpecSuite{}
	for _, su := range s.Suites {
		suites[su.Name] = su
	}

	key := packages.Key("sha256:aaaa", config.Packages{{Name: "git"}, {Name: "jq"}})

	for _, name := range []string{"pinned", "same"} {
		p := suites[name].Packages
		if p == nil || !slices.Equal(p.Names, []string{"git", "jq"}) || p.Images["golang"] != key {
			t.Fatalf("%s: %+v, want the cached image %s", name, p, key)
		}
	}

	// a tag can move, and a task file's own image is no resource: installed per task
	for _, name := range []string{"tagged", "bare"} {
		if p := suites[name].Packages; p == nil || len(p.Images) > 0 || !slices.Equal(p.Names, []string{"git"}) {
			t.Fatalf("%s: %+v", name, p)
		}
	}

	i := s.Images
	if i == nil || i.Registry != "ghcr.io/owner/ci-images" || i.Credentials != "registry_push" || i.PullCredentials != "registry_pull" {
		t.Fatalf("images: %+v", i)
	}

	// two suites on one base with the same packages share one build
	want := plugin.SpecImageBuild{Key: key, Base: "golang", Digest: "sha256:aaaa", Names: []string{"git", "jq"}, Install: suites["pinned"].Packages.Install}
	if len(i.Builds) != 1 || !reflect.DeepEqual(i.Builds[0], want) {
		t.Fatalf("builds: %+v", i.Builds)
	}
}

// A config without packages gives the spec it gave before: a runner that knows nothing of them
// reads the same.
func TestSpecWithoutPackages(t *testing.T) {
	cfg, err := config.Parse([]byte(strings.NewReplacer(
		"packages: [git]\n", "", "images: {registry: ghcr.io/owner/ci-images, credentials: registry_push, pull_credentials: registry_pull}\n", "",
		"    packages: [curl]\n", "", "    packages: [jq]\n", "").Replace(packagesConfig)))
	if err != nil {
		t.Fatal(err)
	}

	s, err := FromInclude(cfg, []byte(include))
	if err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{`"packages"`, `"planner_packages"`, `"images"`} {
		if strings.Contains(string(data), key) {
			t.Fatalf("%s in a spec without packages:\n%s", key, data)
		}
	}
}
