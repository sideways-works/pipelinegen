package affected

import (
	"cmp"
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/bazelbuild/buildtools/build"
)

// A label of another repository is judged by what pins that repository: its version, the lockfile's
// hashes of its registry files, a fixed override; content that changes without its pin can't be told.

// moduleRepo is the module a repository name stands for: @name, or a canonical @@name+ (@@name~
// before Bazel 8); false for an extension's repository.
var moduleRepo = regexp.MustCompile(`^@@?([A-Za-z0-9_.-]+?)[+~]?$`)

func repoOf(label string) (string, bool) {
	repo, _, ok := strings.Cut(label, "//")
	if !ok || !strings.HasPrefix(repo, "@") || repo == "@" || repo == "@@" {
		return "", false
	}

	return repo, true
}

func (o Options) pinOf(ctx context.Context, commit, repo string) (string, string) {
	module, ok, err := o.Repo.Show(ctx, commit, "MODULE.bazel")
	if err != nil || !ok {
		return "", "the commit has no MODULE.bazel to read its pin from"
	}

	f, err := build.ParseModule("MODULE.bazel", module)
	if err != nil {
		return "", "MODULE.bazel can't be read"
	}

	if m := moduleRepo.FindStringSubmatch(repo); m != nil && strings.Count(repo, "+") <= 1 && strings.Count(repo, "~") <= 1 {
		if name, version, ok := depOf(f, m[1], strings.HasPrefix(repo, "@@")); ok {
			return o.modulePin(ctx, commit, f, name, version)
		}
	}

	if pin, why, ok := rulePin(f, strings.TrimLeft(repo, "@")); ok {
		return pin, why
	}

	return "", "no bazel_dep or repository rule of MODULE.bazel names " + repo + " (an extension's repository): nothing the plan reads pins it"
}

func (o Options) modulePin(ctx context.Context, commit string, f *build.File, name, version string) (string, string) {
	overrides, fixed, why := overridesOf(f, name)
	if why != "" {
		return "", why
	}

	if version == "" && !fixed {
		return "", "the module " + name + " has no version: give its bazel_dep a version, or a git_override with a commit"
	}

	pin := slices.Concat([]string{name + "@" + version}, overrides, o.registries(ctx, commit))

	if lock, ok, err := o.Repo.Show(ctx, commit, "MODULE.bazel.lock"); err == nil && ok {
		hashes, ok := lockedHashes(lock, name, version)
		if !ok {
			return "", "MODULE.bazel.lock can't be read"
		}

		pin = append(pin, hashes...)
	} else if !fixed {
		// the build resolves it this way too, but only the registry's
		// promise never to change a published version holds it
		pin = append(pin, VersionOnly)
	}

	return strings.Join(pin, "\n"), ""
}

// VersionOnly marks a pin that rests on a module's version and registry
// alone: no lockfile hash, no fixed override.
const VersionOnly = "pinned by version (no lockfile hash)"

func (o Options) registries(ctx context.Context, commit string) []string {
	rc, ok, err := o.Repo.Show(ctx, commit, ".bazelrc")
	if err != nil || !ok {
		return nil
	}

	var out []string

	for field := range strings.FieldsSeq(string(rc)) {
		if r, ok := strings.CutPrefix(field, "--registry="); ok {
			out = append(out, "registry "+r)
		}
	}

	return out
}

func rulePin(f *build.File, name string) (string, string, bool) {
	for _, stmt := range f.Stmt {
		call, ok := stmt.(*build.CallExpr)
		if !ok {
			continue
		}

		r := &build.Rule{Call: call}
		if r.Name() != name {
			continue
		}

		switch {
		case r.AttrString("sha256") != "", r.AttrString("integrity") != "", r.AttrString("commit") != "":
			return build.FormatString(call), "", true
		default:
			return "", "the repository " + name + " (" + r.Kind() + ") has no sha256, integrity or commit: give it one", true
		}
	}

	return "", "", false
}

func overridesOf(f *build.File, name string) ([]string, bool, string) {
	var (
		out   []string
		fixed bool
	)

	for _, stmt := range f.Stmt {
		call, ok := stmt.(*build.CallExpr)
		if !ok {
			continue
		}

		r := &build.Rule{Call: call}
		if r.AttrString("module_name") != name {
			continue
		}

		switch r.Kind() {
		case "local_path_override":
			return nil, false, "a local_path_override of " + name + " lets its files change unseen: pin it with a version, or a git_override with a commit"
		case "git_override":
			if r.AttrString("commit") == "" {
				return nil, false, "the git_override of " + name + " follows a branch or tag: give it a commit"
			}

			fixed = true
		case "archive_override":
			if r.AttrString("integrity") == "" {
				return nil, false, "the archive_override of " + name + " has no integrity: give it one"
			}

			fixed = true
		}

		out = append(out, build.FormatString(call))
	}

	return out, fixed, ""
}

func lockedHashes(lock []byte, name, version string) ([]string, bool) {
	var l struct {
		RegistryFileHashes map[string]string `json:"registryFileHashes"` //nolint:tagliatelle // Bazel's own key
	}

	if json.Unmarshal(lock, &l) != nil {
		return nil, false
	}

	var out []string

	for _, url := range slices.Sorted(mapKeysOf(l.RegistryFileHashes)) {
		if strings.Contains(url, "/modules/"+name+"/"+version+"/") {
			out = append(out, url+"="+l.RegistryFileHashes[url])
		}
	}

	return out, true
}

func depOf(f *build.File, repo string, canonical bool) (string, string, bool) {
	for _, stmt := range f.Stmt {
		call, ok := stmt.(*build.CallExpr)
		if !ok {
			continue
		}

		r := &build.Rule{Call: call}
		if r.Kind() != "bazel_dep" {
			continue
		}

		name, apparent := r.AttrString("name"), r.AttrString("repo_name")
		if apparent == "" {
			apparent = name
		}

		if (canonical && name == repo) || (!canonical && apparent == repo) {
			return name, r.AttrString("version"), r.AttrString("version") != ""
		}
	}

	return "", "", false
}

func mapKeysOf[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

const (
	pinUnknown = iota
	pinSame
	pinChanged
)

func (o Options) pinnedLabel(label string) (int, string) {
	repo, ok := repoOf(label)
	if !ok || o.pinned == nil {
		return pinUnknown, ""
	}

	changed, why := o.pinned(repo)

	switch {
	case why != "":
		return pinUnknown, why
	case changed:
		return pinChanged, ""
	default:
		return pinSame, ""
	}
}

func (o Options) pinJudge(ctx context.Context, base, head string) func(repo string) (bool, string) {
	type verdict struct {
		changed bool
		why     string
	}

	seen := map[string]verdict{}

	return func(repo string) (bool, string) {
		if v, ok := seen[repo]; ok {
			return v.changed, v.why
		}

		was, whyWas := o.pinOf(ctx, base, repo)
		is, whyIs := o.pinOf(ctx, head, repo)

		v := verdict{changed: was != is, why: cmp.Or(whyIs, whyWas)}
		seen[repo] = v

		if v.why == "" && strings.HasSuffix(is, VersionOnly) {
			o.warnf("%s is %s: data from it counts as unchanged while its version and registry are; commit MODULE.bazel.lock to pin it exactly", repo, VersionOnly)
		}

		return v.changed, v.why
	}
}

func pinOfLabel(o Options, label string) int {
	v, _ := o.pinnedLabel(label)

	return v
}

func pinWhy(o Options, label string) string {
	_, why := o.pinnedLabel(label)

	return why
}
