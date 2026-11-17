package config

import (
	"slices"
	"strings"
	"testing"
)

const withPackages = `
pipeline: shop
packages: [git, jq]
images: {registry: ghcr.io/owner/ci-images, credentials: registry_push, pull_credentials: registry_pull}
concourse:
  planner:
    packages: [curl]
suites:
  go:
    tests: ["**/*_test.go"]
    run_slice: ci/go.yml
    packages:
      - jq
      - {name: postgresql-client, apk: postgresql16-client}
`

func TestParsePackages(t *testing.T) {
	c, err := Parse([]byte(withPackages))
	if err != nil {
		t.Fatal(err)
	}

	got := c.PackagesFor("go").Strings()
	if want := []string{"git", "jq", "postgresql-client apk=postgresql16-client"}; !slices.Equal(got, want) {
		t.Fatalf("PackagesFor: %q, want %q", got, want)
	}

	pg := c.PackagesFor("go")[2]
	for family, want := range map[string]string{FamilyApk: "postgresql16-client", FamilyApt: "postgresql-client", FamilyRPM: "postgresql-client"} {
		if n, ok := pg.For(family); !ok || n != want {
			t.Errorf("For(%s) = %q, %v, want %q", family, n, ok, want)
		}
	}

	if !c.HasPackages() || c.Images.Registry != "ghcr.io/owner/ci-images" || c.Concourse.Planner.Packages[0].Name != "curl" {
		t.Fatalf("config: %+v %+v", c.Images, c.Concourse.Planner.Packages)
	}
}

func TestPackageWithoutDefault(t *testing.T) {
	p := Package{By: map[string]string{FamilyApt: "a", FamilyApk: "b"}}
	if _, ok := p.For(FamilyRPM); ok {
		t.Fatal("a family the map doesn't name, and no name: none")
	}

	if p.String() != " apk=b apt=a" {
		t.Fatalf("String: %q", p.String())
	}
}

func TestPackagesRefused(t *testing.T) {
	cases := map[string]string{ //nolint:gosec // config snippets, no credential
		"packages: [\"git; rm -rf /\"]":                                        "package \"git; rm -rf /\"",
		"packages: [{name: jq, dnf: jq}]":                                      "dnf is no family",
		"packages: [{apt: \"$(id)\"}]":                                         "apt's name",
		"packages: [{}]":                                                       "an empty package",
		"packages: [[jq]]":                                                     "a package is a name",
		"images: {registry: ghcr.io/o/x:latest}":                               "without a tag",
		"images: {registry: ((registry))}":                                     "can't take a var",
		"images: {credentials: creds}":                                         "images.registry is missing",
		"images: {registry: ghcr.io/o/x, credentials: \"((x))\"}":              "images.credentials must name a var",
		"images: {registry: ghcr.io/o/x, credentials: a, pull_credentials: a}": "must not get the var that pushes",
	}

	for extra, want := range cases {
		var err error
		if strings.HasPrefix(extra, "images") {
			_, err = Parse([]byte(strings.Replace(withPackages, "images: {registry: ghcr.io/owner/ci-images, credentials: registry_push, pull_credentials: registry_pull}\n", extra+"\n", 1)))
		} else {
			_, err = Parse([]byte(strings.Replace(withPackages, "packages: [git, jq]\n", extra+"\n", 1)))
		}

		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", extra, err, want)
		}
	}
}

func TestPackagesMeaning(t *testing.T) {
	base, err := Parse([]byte(withPackages))
	if err != nil {
		t.Fatal(err)
	}

	head, err := Parse([]byte(strings.Replace(withPackages, "packages: [git, jq]", "packages: [jq, git, make]", 1)))
	if err != nil {
		t.Fatal(err)
	}

	m := MeaningOf(base, head, "")
	if len(m.Parts) != 1 || m.Parts[0].Means != MeansSlices || !m.Understood() {
		t.Fatalf("parts: %+v", m.Parts)
	}

	if why, ok := m.SliceRuns("go", RoutedSlice{Name: "a_test.go"}); !ok || why != "suites.go.packages changed" {
		t.Fatalf("SliceRuns: %q %v", why, ok)
	}

	// the same packages in another order, and another registry, change nothing a test sees
	head, err = Parse([]byte(strings.NewReplacer("packages: [git, jq]", "packages: [jq, git]", "ci-images", "other").Replace(withPackages)))
	if err != nil {
		t.Fatal(err)
	}

	m = MeaningOf(base, head, "")
	if _, ok := m.SliceRuns("go", RoutedSlice{Name: "a_test.go"}); ok || slices.ContainsFunc(m.Parts, func(p Part) bool { return p.Means != MeansNothing && p.Means != MeansSlices }) {
		t.Fatalf("parts: %+v", m.Parts)
	}
}

func TestATaskMayNotHoldThePushingVar(t *testing.T) {
	for _, secrets := range []string{
		"    secrets: {batch: {REGISTRY: registry_push}}\n",
		"    secrets: {prepare: {REGISTRY_PASSWORD: registry_push.password}}\n",
	} {
		_, err := Parse([]byte(withPackages + secrets))
		if err == nil || !strings.Contains(err.Error(), "the var that pushes cached images (images.credentials)") {
			t.Errorf("%s: %v", secrets, err)
		}
	}

	if _, err := Parse([]byte(withPackages + "    secrets: {batch: {REGISTRY: registry_pull}}\n")); err != nil {
		t.Fatalf("the pulling var: %v", err)
	}
}

func TestAnOldConfigsPackagesLoadWithAWarning(t *testing.T) {
	for _, bad := range []string{"packages: [[jq]]", "packages: [{name: [jq]}]"} {
		data := []byte(strings.Replace(withPackages, "packages: [git, jq]", bad, 1))

		if _, err := Parse(data); err == nil || !strings.Contains(err.Error(), "a package is a name") {
			t.Errorf("%s: %v", bad, err)
		}

		c, err := ParseHistory(data)
		if err != nil || len(c.Warnings) == 0 {
			t.Errorf("%s: history: %v, warnings %q", bad, err, c.Warnings)
		}
	}
}
