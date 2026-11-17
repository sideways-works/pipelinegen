package config

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Package is one package a suite's steps need: one name for every package
// manager, or a name per family of managers where they differ.
type Package struct {
	// Name is the package's name for a manager By doesn't name, and the
	// command or package a machine without an image is checked for.
	Name string `yaml:"-"`
	// By names the package per family: apt (apt-get), apk, and rpm (dnf,
	// microdnf and yum, which install from the same repositories).
	By map[string]string `yaml:"-"`
}

// The families of package managers a package may be spelled for.
const (
	FamilyApt = "apt"
	FamilyApk = "apk"
	FamilyRPM = "rpm"
)

// Families lists the families in a stable order.
var Families = []string{FamilyApt, FamilyApk, FamilyRPM}

const packageDefault = "name"

var errPackage = errors.New("a package is a name, or a map of name, apt, apk and rpm to names")

// UnmarshalYAML reads a package as a plain name or as a map of names.
func (p *Package) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		p.Name = n.Value

		return nil
	case yaml.MappingNode:
		var m map[string]string
		if err := n.Decode(&m); err != nil {
			return fmt.Errorf("%w: %w", errPackage, err)
		}

		p.Name = m[packageDefault]
		delete(m, packageDefault)

		if len(m) > 0 {
			p.By = m
		}

		return nil
	case yaml.DocumentNode, yaml.SequenceNode, yaml.AliasNode:
	}

	return fmt.Errorf("%w (line %d)", errPackage, n.Line)
}

// MarshalYAML writes a package back in the form it was read in.
func (p Package) MarshalYAML() (any, error) {
	if len(p.By) == 0 {
		return p.Name, nil
	}

	m := maps.Clone(p.By)
	if p.Name != "" {
		m[packageDefault] = p.Name
	}

	return m, nil
}

// For is the package's name for a family, false when it has none.
func (p Package) For(family string) (string, bool) {
	if n, ok := p.By[family]; ok {
		return n, true
	}

	return p.Name, p.Name != ""
}

// String is the package as one stable line: its name, then each family's
// spelling, sorted.
func (p Package) String() string {
	parts := make([]string, 0, 1+len(p.By))
	parts = append(parts, p.Name)

	for _, f := range slices.Sorted(maps.Keys(p.By)) {
		parts = append(parts, f+"="+p.By[f])
	}

	return strings.Join(parts, " ")
}

// Packages is a list of packages.
type Packages []Package

// Sorted is the list without repeats, in String's order.
func (ps Packages) Sorted() Packages {
	seen := map[string]bool{}

	var out Packages

	for _, p := range ps {
		if s := p.String(); !seen[s] {
			seen[s] = true

			out = append(out, p)
		}
	}

	slices.SortFunc(out, func(a, b Package) int { return strings.Compare(a.String(), b.String()) })

	return out
}

// Strings are the packages' String lines.
func (ps Packages) Strings() []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.String())
	}

	return out
}

// PackagesFor is what a suite's steps need: the top level's packages and
// the suite's, sorted, each once.
func (c *Config) PackagesFor(suite string) Packages {
	return slices.Concat(c.Packages, c.Suites[suite].Packages).Sorted()
}

// HasPackages tells whether any step of the pipeline needs a package.
func (c *Config) HasPackages() bool {
	if len(c.Packages)+len(c.Concourse.Planner.Packages) > 0 {
		return true
	}

	return slices.ContainsFunc(c.SuiteNames(), func(s string) bool { return len(c.Suites[s].Packages) > 0 })
}

// Images is where cached images, a base image with a suite's packages
// installed, are kept.
type Images struct {
	// Registry is the repository the cached images are pushed to, each
	// tagged with its key.
	Registry string `yaml:"registry"`
	// Credentials names the var holding the username and password that
	// push to the registry; only the job that builds the images gets it.
	Credentials string `yaml:"credentials,omitempty"`
	// PullCredentials names the var that pulls from it, for a registry
	// that needs a login to pull; without it the images are pulled
	// anonymously.
	PullCredentials string `yaml:"pull_credentials,omitempty"`
}

// Names are shell words: they go into the install script as they are.
var packageName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._-]*$`)

// repository is a registry's repository without a tag or digest: an
// optional host (with a port), then lower case path components.
var repository = regexp.MustCompile(`^([A-Za-z0-9.-]+(:[0-9]+)?/)?[a-z0-9]+([._-]+[a-z0-9]+)*(/[a-z0-9]+([._-]+[a-z0-9]+)*)*$`)

func validatePackages(where string, ps Packages, bad func(string, ...any)) {
	for _, p := range ps {
		if p.Name == "" && len(p.By) == 0 {
			bad("%s: an empty package", where)

			continue
		}

		if p.Name != "" && !packageName.MatchString(p.Name) {
			bad("%s: package %q: a name is letters, digits and + . _ -, starting with a letter or digit", where, p.Name)
		}

		for _, f := range slices.Sorted(maps.Keys(p.By)) {
			switch n := p.By[f]; {
			case !slices.Contains(Families, f):
				bad("%s: package %s: %s is no family: name, apt, apk or rpm (rpm is dnf's, microdnf's and yum's)", where, p, f)
			case !packageName.MatchString(n):
				bad("%s: package %s: %s's name %q: a name is letters, digits and + . _ -, starting with a letter or digit", where, p, f, n)
			}
		}
	}
}

func (c *Config) validatePackages(bad func(string, ...any)) {
	validatePackages("packages", c.Packages, bad)
	validatePackages("concourse.planner.packages", c.Concourse.Planner.Packages, bad)

	for _, name := range c.SuiteNames() {
		validatePackages("suites."+name+".packages", c.Suites[name].Packages, bad)
	}

	i := c.Images
	if i == (Images{}) {
		return
	}

	switch {
	case i.Registry == "":
		bad("images.registry is missing: the repository the cached images are pushed to")
	case strings.Contains(i.Registry, "(("):
		bad("images.registry can't take a var: the images' key is part of the pipeline")
	case !repository.MatchString(i.Registry):
		bad("images.registry %q must be a repository without a tag or digest, such as ghcr.io/owner/ci-images", i.Registry)
	}

	for _, kv := range [][2]string{{"images.credentials", i.Credentials}, {"images.pull_credentials", i.PullCredentials}} {
		if kv[1] != "" && !varName.MatchString(kv[1]) {
			bad("%s must name a var, not hold a value: letters, digits, _ . -", kv[0])
		}
	}

	if i.Credentials != "" && i.Credentials == i.PullCredentials {
		bad("images.pull_credentials is images.credentials: a pull request's steps pull the images, and must not get the var that pushes them")
	}
}
