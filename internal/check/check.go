// Package check finds what no slice covers: a tracked file that is the input
// of no slice and not declared untested, a tagged suite's target bound to
// nothing, and a repository not set up for pipelinegen. The commit hook and
// planning call the same Find, so they never disagree.
package check

import (
	"bytes"
	"cmp"
	"fmt"
	"maps"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/bazelbuild/buildtools/build"
	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/buildfile"
	"github.com/sideways-works/pipelinegen/internal/config"
)

// The kinds of finding.
const (
	// KindUncovered is a tracked file that is the input of no slice and
	// matches no untested entry.
	KindUncovered = "uncovered-file"
	// KindUntagged is a tagged suite without any target carrying its tag.
	KindUntagged = "untagged-slice"
	// KindUnbound is a tagged suite's target that depends on nothing but
	// its own sources.
	KindUnbound = "unbound-slice"
	// KindSetup is a repository missing a line pipelinegen needs.
	KindSetup = "setup"
	// KindBadConfig is a config entry that can't be right, such as a
	// pattern matching no file.
	KindBadConfig = "bad-config"
	// KindUnreadable is a BUILD or .bzl file that can't be parsed, or a
	// target whose inputs can't be read without running Starlark: what it
	// covers is unknown, so it fails rather than counting as covering
	// nothing.
	KindUnreadable = "unreadable"
	// KindDuplicate is a target naming one label twice in an attribute,
	// under two spellings (//docs and //docs:docs, say): Bazel refuses to
	// load its package.
	KindDuplicate = "duplicate-label"
	// KindDeclared is a warning naming each declared macro the check used,
	// with what it covered: a claim the check trusts, shown so the list
	// can't grow unseen.
	KindDeclared = "declared-macro"
	// KindVersionPinned is a warning that data a target takes from another
	// repository is pinned by that module's version alone: no lockfile
	// hash holds its content.
	KindVersionPinned = "pinned-by-version"
	// KindRunnerNotice is a warning the runner gave with the pipeline it
	// wrote: something about it a person should know, short of a refusal.
	KindRunnerNotice = "runner-notice"
	// KindPackages is a note of how a suite's steps get the packages it
	// declares on CI, or a warning that some of them can't.
	KindPackages = "packages"
	// KindUndescribedDriver is a warning that the check couldn't ask a
	// driver a suite's steps name what it is, so the steps that name it
	// went unchecked.
	KindUndescribedDriver = "undescribed-driver"
)

const declaredAttr = "macros.reads"

// Finding is one problem, with the file it is about and how to fix it.
type Finding struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Target string `json:"target,omitempty"`
	// Files are the files a finding about many is about, when its Path is
	// where to fix it: --staged reports it when one of them is staged.
	Files   []string `json:"files,omitempty"`
	Problem string   `json:"problem"`
	Fix     string   `json:"fix"`
	// Warning marks a finding that fails nothing: a line going away, or a
	// declaration the check trusts.
	Warning bool `json:"warning,omitempty"`
	Note    bool `json:"note,omitempty"`
}

// String is the finding as a person reads it in a hook's output: the
// problem on one line, the fix on the next.
func (f Finding) String() string {
	if f.Note {
		return fmt.Sprintf("note: %s: %s\n  fix: %s", f.Path, f.Problem, f.Fix)
	}

	if f.Warning {
		return fmt.Sprintf("warning: %s: %s\n  fix: %s", f.Path, f.Problem, f.Fix)
	}

	return fmt.Sprintf("%s: %s\n  fix: %s", f.Path, f.Problem, f.Fix)
}

func (f Finding) key() string {
	return strings.Join(append([]string{f.Kind, f.Path, f.Target, f.Problem, f.Fix, fmt.Sprint(f.Warning, f.Note)}, f.Files...), "\x00")
}

// Failures are the findings that aren't warnings.
func Failures(fs []Finding) []Finding {
	var out []Finding

	for _, f := range fs {
		if !f.Warning && !f.Note {
			out = append(out, f)
		}
	}

	return out
}

// Options are what a check needs to know of how planning runs.
type Options struct {
	NoDepsRepo bool
}

// Paths pipelinegen's setup lives in.
const (
	DepsRepo    = "pipelinegen_deps"
	DepsPath    = ".pipelinegen/deps"
	HiddenDir   = ".pipelinegen"
	moduleFile  = "MODULE.bazel"
	bazelIgnore = ".bazelignore"
	gitIgnore   = ".gitignore"
)

const runTag = "pipelinegen-run:"

// Find checks a tree. With staged nil it reports everything; otherwise only
// the findings about the staged paths, and the setup and config findings
// when a file they are about is staged.
func Find(cfg *config.Config, tree Tree, staged []string) []Finding {
	return FindWith(cfg, tree, staged, Options{})
}

// FindWith is Find for planning that runs as o says.
func FindWith(cfg *config.Config, tree Tree, staged []string, o Options) []Finding {
	g := buildfile.Load(tree.Files, tree.Read)

	var out []Finding

	for _, err := range g.Errors {
		out = append(out, Finding{
			Kind:    KindUnreadable,
			Path:    errPath(err),
			Problem: err.Error(),
			Fix:     "fix the file's syntax; until then nothing in it counts as covered",
		})
	}

	out = append(out, setup(tree, o)...)
	out = append(out, badConfig(cfg, tree)...)
	out = append(out, declareMacros(cfg, g)...)
	out = append(out, images(cfg, tree)...)
	out = append(out, packageModes(cfg, tree)...)

	broken := map[string]bool{}

	for _, f := range unreadableTargets(g) {
		out = append(out, f)
		broken[g.Targets[f.Target].Pkg] = true
	}

	out = append(out, duplicateLabels(g)...)
	out = append(out, versionPinned(tree, g)...)

	c := newCover(cfg, tree, g)
	out = append(out, c.suites()...)

	// a package whose BUILD file or a target of it can't be read says
	// nothing about its files; the unreadable finding is the one to fix

	for _, f := range out {
		switch {
		case f.Kind != KindUnreadable:
		case f.Target != "" && g.Targets[f.Target] != nil:
			broken[g.Targets[f.Target].Pkg] = true
		case buildfile.IsBuildFile(f.Path):
			broken[dirOf(f.Path)] = true
		}
	}

	for _, f := range c.uncovered() {
		if pkg, ok := g.PackageOf(f.Path); !ok || !broken[pkg] {
			out = append(out, f)
		}
	}

	if staged != nil {
		touched := map[string]bool{}
		for _, p := range staged {
			touched[p] = true
		}

		out = slices.DeleteFunc(out, func(f Finding) bool {
			// a directory's finding is about every file under it
			if dir, ok := strings.CutSuffix(f.Path, "/"); ok && f.Path != "/" {
				return !slices.ContainsFunc(staged, func(p string) bool { return strings.HasPrefix(p, dir+"/") })
			}

			return !touched[f.Path] && !slices.ContainsFunc(f.Files, func(p string) bool { return touched[p] })
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}

		return out[i].Kind < out[j].Kind
	})

	return out
}

func declareMacros(cfg *config.Config, g *buildfile.Graph) []Finding {
	if len(cfg.Macros) == 0 {
		return nil
	}

	declared := declaredMacros(cfg)

	calls := map[string][]string{}
	files := map[string]map[string]bool{}
	matched := map[string]map[string]bool{} // key to the patterns that matched

	for _, label := range slices.Sorted(keys(g.Targets)) {
		t := g.Targets[label]

		key, ok := declared[t.Macro]
		if !ok {
			continue
		}

		calls[key] = append(calls[key], label)

		if files[key] == nil {
			files[key], matched[key] = map[string]bool{}, map[string]bool{}
		}

		for _, p := range cfg.Macros[key].Reads {
			got := g.Glob(t.Pkg, []string{p})
			if len(got) > 0 {
				matched[key][p] = true
			}

			for _, l := range got {
				if !slices.Contains(t.Attrs[declaredAttr], l) {
					t.Attrs[declaredAttr] = append(t.Attrs[declaredAttr], l)
				}

				files[key][l] = true
			}
		}
	}

	return macroFindings(cfg, calls, files, matched)
}

func macroFindings(cfg *config.Config, calls map[string][]string, files, matched map[string]map[string]bool) []Finding {
	var out []Finding

	for _, key := range slices.Sorted(maps.Keys(cfg.Macros)) {
		m := cfg.Macros[key]

		if len(calls[key]) == 0 {
			out = append(out, Finding{
				Kind:    KindBadConfig,
				Path:    config.FileName,
				Problem: fmt.Sprintf("macros %q: no BUILD file calls it", key),
				Fix:     "remove the entry, or fix the macro's label or name",
			})

			continue
		}

		for _, p := range m.Reads {
			if !matched[key][p] {
				out = append(out, Finding{
					Kind:    KindBadConfig,
					Path:    config.FileName,
					Problem: fmt.Sprintf("macros %q: reads %q matches no file in any package that calls it", key, p),
					Fix:     "remove the pattern, or fix it (patterns are relative to the calling package)",
				})
			}
		}

		out = append(out, Finding{
			Kind: KindDeclared,
			Path: config.FileName,
			Problem: fmt.Sprintf("macros %q is trusted to read %s (%s): %d files in %s; planning holds it against Bazel",
				key, strings.Join(m.Reads, ", "), m.Reason, len(files[key]), strings.Join(calls[key], ", ")),
			Fix:     "keep reads to the files the macro's own globs take",
			Warning: true,
		})
	}

	return out
}

// duplicateLabels are the targets that name a label twice in one
// attribute, after canonicalising: Bazel refuses to load their package.
func duplicateLabels(g *buildfile.Graph) []Finding {
	var out []Finding

	for _, label := range slices.Sorted(keys(g.Targets)) {
		t := g.Targets[label]

		for _, attr := range slices.Sorted(keys(t.Attrs)) {
			seen := map[string]bool{}

			for _, l := range t.Attrs[attr] {
				if !seen[l] {
					seen[l] = true

					continue
				}

				out = append(out, Finding{
					Kind:    KindDuplicate,
					Path:    t.File,
					Target:  label,
					Problem: fmt.Sprintf("%s names %s twice in %s, and Bazel refuses to load the package", label, l, attr),
					Fix:     "name it once (//docs and //docs:docs are the same label)",
				})

				break
			}
		}
	}

	return out
}

func dirOf(p string) string {
	if d := path.Dir(p); d != "." {
		return d
	}

	return ""
}

var readableAttrs = map[string]bool{"name": true, "visibility": true, "licenses": true, "importpath": true}

func unreadableTargets(g *buildfile.Graph) []Finding {
	var out []Finding

	for _, label := range slices.Sorted(keys(g.Targets)) {
		t := g.Targets[label]

		var why []string

		for _, attr := range slices.Sorted(keys(t.Unreadable)) {
			if !readableAttrs[attr] {
				why = append(why, attr+": "+t.Unreadable[attr])
			}
		}

		if len(why) == 0 {
			continue
		}

		out = append(out, Finding{
			Kind:    KindUnreadable,
			Path:    t.File,
			Target:  label,
			Problem: fmt.Sprintf("%s's inputs can't be read without running Starlark (%s), so what it covers is unknown", label, strings.Join(why, "; ")),
			Fix:     "write those attributes with strings, lists, +, glob, select, list() and comprehensions over lists of the repository's own files and .bzl constants",
		})
	}

	return out
}

func errPath(err error) string {
	msg := strings.TrimPrefix(err.Error(), buildfile.ErrParse.Error()+": ")
	p, _, _ := strings.Cut(msg, ":")

	return p
}

func readText(tree Tree, p string) (string, bool) {
	if !slices.Contains(tree.Files, p) {
		return "", false
	}

	b, err := tree.Read(p)
	if err != nil {
		return "", false
	}

	return string(b), true
}

func setup(tree Tree, o Options) []Finding {
	if o.NoDepsRepo {
		return leftoverSetup(tree)
	}

	var out []Finding

	if !hasDepsRepo(tree) {
		out = append(out, Finding{
			Kind:    KindSetup,
			Path:    moduleFile,
			Problem: "the " + DepsRepo + " repository is not declared",
			Fix: `add to MODULE.bazel: local_repository = use_repo_rule("@bazel_tools//tools/build_defs/repo:local.bzl", "local_repository")` +
				` and local_repository(name = "` + DepsRepo + `", path = "` + DepsPath + `")`,
		})
	}

	for _, ig := range []struct{ file, line string }{{bazelIgnore, HiddenDir}, {gitIgnore, HiddenDir + "/"}} {
		text, _ := readText(tree, ig.file)
		if !ignores(text) {
			out = append(out, Finding{
				Kind:    KindSetup,
				Path:    ig.file,
				Problem: HiddenDir + " is not ignored",
				Fix:     "add the line " + ig.line + " to " + ig.file,
			})
		}
	}

	return out
}

func leftoverSetup(tree Tree) []Finding {
	var out []Finding

	if hasDepsRepo(tree) {
		out = append(out, Finding{
			Kind:    KindSetup,
			Path:    moduleFile,
			Problem: "declares the " + DepsRepo + " repository, which planning no longer writes or reads; the next release refuses it",
			Fix:     `remove local_repository(name = "` + DepsRepo + `", ...), and the local_repository = use_repo_rule(...) line if nothing else uses it`,
			Warning: true,
		})
	}

	for _, file := range []string{bazelIgnore, gitIgnore} {
		if text, _ := readText(tree, file); ignores(text) {
			out = append(out, Finding{
				Kind:    KindSetup,
				Path:    file,
				Problem: "lists " + HiddenDir + ", which pipelinegen no longer writes into the repository; the next release refuses the line",
				Fix:     "remove the " + HiddenDir + " line",
				Warning: true,
			})
		}
	}

	return out
}

func ignores(text string) bool {
	for line := range strings.SplitSeq(text, "\n") {
		switch strings.TrimSpace(line) {
		case HiddenDir, HiddenDir + "/", "/" + HiddenDir, "/" + HiddenDir + "/":
			return true
		}
	}

	return false
}

func hasDepsRepo(tree Tree) bool {
	text, ok := readText(tree, moduleFile)
	if !ok {
		return false
	}

	f, err := build.ParseModule(moduleFile, []byte(text))
	if err != nil {
		return false
	}

	for _, stmt := range f.Stmt {
		call, ok := stmt.(*build.CallExpr)
		if !ok {
			continue
		}

		r := &build.Rule{Call: call}
		if r.Kind() == "local_repository" && r.Name() == DepsRepo && strings.TrimSuffix(r.AttrString("path"), "/") == DepsPath {
			return true
		}
	}

	return false
}

// badConfig reports config patterns that match no tracked file: a typo or
// a leftover, either way not what the config means.
func badConfig(cfg *config.Config, tree Tree) []Finding {
	var out []Finding

	matchesSome := func(p string) bool {
		return slices.ContainsFunc(tree.Files, func(f string) bool { return config.MatchGlob(p, f) })
	}

	for _, u := range cfg.Untested {
		for _, p := range u.Paths {
			if !matchesSome(p) {
				out = append(out, Finding{
					Kind:    KindBadConfig,
					Path:    config.FileName,
					Problem: fmt.Sprintf("untested pattern %q matches no file", p),
					Fix:     "remove the pattern, or fix it",
				})
			}
		}
	}

	out = append(out, unrun(cfg)...)
	out = append(out, floodingBatches(cfg)...)

	for _, name := range cfg.SuiteNames() {
		for _, p := range cfg.Suites[name].Tests {
			if !matchesSome(p) {
				out = append(out, Finding{
					Kind:    KindBadConfig,
					Path:    config.FileName,
					Problem: fmt.Sprintf("suite %s: tests pattern %q matches no file", name, p),
					Fix:     "remove the pattern, or fix it",
				})
			}
		}
	}

	return out
}

// batchesFlood is how many times parallel a suite's batches may be before
// the check warns: every batch of a build runs at once, and beyond about
// this many one build's batches crowd out the other suites' on the
// workers.
const batchesFlood = 4

// floodingBatches warns of a suite whose builds run far more batches at once
// than its parallel says: batches far above it, or batch_minutes with no
// batches to bound how many a run of everything is dealt.
func floodingBatches(cfg *config.Config) []Finding {
	var out []Finding

	for _, name := range cfg.SuiteNames() {
		s := cfg.Suites[name]
		parallel := s.ParallelOrDefault()

		switch {
		case s.Batches > batchesFlood*parallel:
			out = append(out, Finding{
				Kind: KindBadConfig, Path: config.FileName, Warning: true,
				Problem: fmt.Sprintf("suite %s: batches (%d) is more than %d times parallel (%d): every batch of a build runs at once, so a build of everything runs %d batch tasks at the same time",
					name, s.Batches, batchesFlood, parallel, s.Batches),
				Fix: "lower batches to how many batch tasks the workers should run at once for one build, or raise parallel to say so",
			})
		case s.BatchMinutes > 0 && s.Batches == 0:
			out = append(out, Finding{
				Kind: KindBadConfig, Path: config.FileName, Warning: true,
				Problem: fmt.Sprintf("suite %s: batch_minutes (%d) without batches: a build of everything is dealt as many batches as its time asks for, up to one per slice, and every one runs at once",
					name, s.BatchMinutes),
				Fix: "set batches to the most batch tasks one build may run at once",
			})
		}
	}

	return out
}

// unrun reports each suite no run lists: a run plans only the suites it
// names, so its tests never run. Planning only warns of it, so a pipeline
// that ran yesterday keeps running while the check fails.
func unrun(cfg *config.Config) []Finding {
	if len(cfg.Runs) == 0 {
		return nil
	}

	listed := map[string]bool{}

	for _, r := range cfg.Runs {
		for name := range r.Suites {
			listed[name] = true
		}
	}

	var out []Finding

	for _, name := range cfg.SuiteNames() {
		if !listed[name] {
			out = append(out, Finding{
				Kind:    KindBadConfig,
				Path:    config.FileName,
				Problem: fmt.Sprintf("suite %s is in no run: its tests never run", name),
				Fix:     "list it under a run's suites (runs.<run>.suites), or remove it",
			})
		}
	}

	return out
}

// images finds an image a suite or the planner's prepare names that the root
// pipeline file doesn't declare as a registry-image resource. The file may
// hold secrets' names, so a finding names the resource and the file, never a
// value from it.
func images(cfg *config.Config, tree Tree) []Finding {
	named := map[string]string{}

	for _, name := range cfg.SuiteNames() {
		if img := cfg.Suites[name].Image; img != "" {
			named["suites."+name+".image"] = img
		}
	}

	if img := cfg.Concourse.Planner.PrepareImage; img != "" {
		named["concourse.planner.prepare_image"] = img
	}

	if len(named) == 0 {
		return nil
	}

	root := cfg.Concourse.Include
	declared, problem := registryImages(tree, root)

	var out []Finding

	for _, key := range slices.Sorted(maps.Keys(named)) {
		img := named[key]

		switch {
		case problem != "":
			out = append(out, Finding{
				Kind: KindBadConfig, Path: config.FileName,
				Problem: fmt.Sprintf("%s names %s, but %s", key, img, problem),
				Fix:     "declare the image as a registry-image resource in the root pipeline file, which concourse.include names",
			})
		case !declared[img]:
			out = append(out, Finding{
				Kind: KindBadConfig, Path: config.FileName,
				Problem: fmt.Sprintf("%s names %s, which %s doesn't declare as a registry-image resource", key, img, root),
				Fix:     "declare it there with type: registry-image, or fix the name",
			})
		}
	}

	return out
}

func registryImages(tree Tree, root string) (map[string]bool, string) {
	if root == "" {
		return nil, "concourse.include names no root pipeline file to declare it in"
	}

	data, err := tree.Read(root)
	if err != nil {
		return nil, root + " can't be read"
	}

	var file struct {
		Resources []struct {
			Name string `yaml:"name"`
			Type string `yaml:"type"`
		} `yaml:"resources"`
	}

	// a parse error may quote the file: only its name is said
	if yaml.Unmarshal(data, &file) != nil {
		return nil, root + " can't be read as YAML"
	}

	out := map[string]bool{}

	for _, r := range file.Resources {
		if r.Type == "registry-image" {
			out[r.Name] = true
		}
	}

	return out, ""
}

type cover struct {
	cfg     *config.Config
	tree    Tree
	g       *buildfile.Graph
	owners  map[string][]string // a file label to the targets listing it
	covered map[string]bool     // tracked paths
	seen    map[string]bool     // labels walked
}

func newCover(cfg *config.Config, tree Tree, g *buildfile.Graph) *cover {
	c := &cover{cfg: cfg, tree: tree, g: g, owners: map[string][]string{}, covered: map[string]bool{}, seen: map[string]bool{}}

	for _, label := range slices.Sorted(keys(g.Targets)) {
		for _, in := range g.Targets[label].Inputs() {
			c.owners[in] = append(c.owners[in], label)
		}
	}

	return c
}

func keys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func (c *cover) walk(label string) {
	queue := []string{label}

	for len(queue) > 0 {
		l := queue[0]
		queue = queue[1:]

		if c.seen[l] || strings.HasPrefix(l, "@") {
			continue
		}

		c.seen[l] = true

		if p, ok := c.g.LabelFile(l); ok {
			c.covered[p] = true

			// a file's package decides what its label is
			if pkg, ok := c.g.PackageOf(p); ok {
				c.covered[c.g.Packages[pkg]] = true
			}
		}

		t := c.g.Targets[l]
		if t == nil {
			continue
		}

		c.covered[c.g.Packages[t.Pkg]] = true
		c.covered[t.File] = true

		for _, bzl := range c.g.Loads[t.Pkg] {
			c.covered[bzl] = true
		}

		queue = append(queue, t.Inputs()...)

		for _, tag := range t.Tags() {
			if twin, ok := strings.CutPrefix(tag, runTag); ok {
				if canon, ok := buildfile.Canonical(t.Pkg, twin); ok {
					queue = append(queue, canon)
				}
			}
		}
	}
}

// suites walks every suite's slices: a suite of test files has a slice per
// file (the file, its owners and its extra deps), a tagged suite its tagged
// targets, each of which must depend on more than its own sources.
func (c *cover) suites() []Finding {
	var out []Finding

	for _, name := range c.cfg.SuiteNames() {
		s := c.cfg.Suites[name]

		if !s.Implicit() {
			out = append(out, c.tagged(name, s)...)

			continue
		}

		var alone []string

		for _, f := range c.tree.Files {
			// a recorded suite's helpers are covered by the recordings: a
			// slice that runs one records it, and a change to one no
			// recording holds runs the slice as an unclaimed file does
			if s.Bind == config.BindRecorded && matchAny(s.Tests, f) && matchAny(s.Helpers, f) && !matchAny(s.Exclude, f) {
				c.covered[f] = true
			}

			// helpers are no slices: they are covered when a slice uses them;
			// nor is a file Bazel ignores, which no target can run
			if !matchAny(s.Tests, f) || matchAny(s.Exclude, f) || matchAny(s.Helpers, f) {
				continue
			}

			if _, in := c.g.IgnoredRoot(f); in {
				continue
			}

			found, held := c.fileSlice(name, s, f)
			out = append(out, found...)

			if !held && s.Bind != config.BindRecorded && !c.named(s, f) {
				alone = append(alone, f)
			}
		}

		if len(alone) > 0 {
			out = append(out, aloneFinding(name, alone))
		}
	}

	return out
}

// fileSlice walks one test file's slice; held tells whether a target holds
// the file for planning to bind it through.
func (c *cover) fileSlice(suite string, s config.Suite, f string) (found []Finding, held bool) { //nolint:nonamedreturns // the findings and whether a target holds it
	c.covered[f] = true

	label, inPackage := c.g.FileLabel(f)
	if inPackage {
		c.walk(label)
	}

	switch owner, ok := ownerOf(s, f); {
	case ok:
		// an owner the tree lacks is a finding of its own
		held = true

		canon, valid := buildfile.Canonical("", owner)
		if !valid || c.g.Targets[canon] == nil {
			found = append(found, Finding{
				Kind:    KindUnbound,
				Path:    f,
				Target:  owner,
				Problem: fmt.Sprintf("suite %s's owner of this file, %s, is no target", suite, owner),
				Fix:     "declare " + owner + ", or change the suite's owner in " + config.FileName,
			})
		} else {
			c.walk(canon)
		}
	case inPackage:
		found, held = c.walkOwners(suite, label, f)
	}

	for _, ed := range s.ExtraDeps {
		if !matchAny(ed.Tests, f) {
			continue
		}

		for _, d := range ed.Deps {
			if canon, ok := buildfile.Canonical("", d); ok {
				c.walk(canon)
			}
		}
	}

	return found, held
}

func (c *cover) named(s config.Suite, f string) bool {
	for _, ed := range s.ExtraDeps {
		if matchAny(ed.Tests, f) && len(ed.Deps) > 0 {
			return true
		}
	}

	src, err := c.tree.Read(f)

	return err == nil && len(config.DataComments(bytes.NewReader(src))) > 0
}

// aloneFinding is a suite's test files that nothing but themselves binds:
// planning binds such a slice to its own file alone, so a change to what the
// file imports would select none of them.
func aloneFinding(suite string, files []string) Finding {
	examples := strings.Join(files[:min(2, len(files))], ", ")
	if len(files) > 2 {
		examples += fmt.Sprintf(" and %d more", len(files)-2)
	}

	return Finding{
		Kind:  KindUnbound,
		Path:  config.FileName,
		Files: files,
		Problem: fmt.Sprintf("suite %s names no owner, and no Bazel target holds %d of its test files (%s), so each is bound to nothing but itself: "+
			"a change to anything these files import selects none of them",
			suite, len(files), examples),
		Fix: "give suite " + suite + " an owner pattern in " + config.FileName + ", declare a Bazel target that holds the files, " +
			"or bind the suite by recording (bind: recorded) with a correlator that binds them",
	}
}

// walkOwners walks the targets that list a file: planning binds a test file
// only through them, so its package's other targets cover nothing on its
// behalf. A target holding a value from another repository may list it, so
// which target runs it is unknown: a finding.
func (c *cover) walkOwners(suite, label, f string) (found []Finding, held bool) { //nolint:nonamedreturns // the findings and whether a target holds it
	if owners := c.owners[label]; len(owners) > 0 {
		for _, o := range owners {
			c.walk(o)
		}

		return nil, true
	}

	pkg, _ := c.g.PackageOf(f)

	for _, l := range slices.Sorted(keys(c.g.Targets)) {
		t := c.g.Targets[l]
		if t.Pkg != pkg {
			continue
		}

		if attrs := outsideInputs(t); len(attrs) > 0 {
			return []Finding{{
				Kind:   KindUnreadable,
				Path:   f,
				Target: l,
				Problem: fmt.Sprintf("no target lists this test file where the check can read it, and %s's %s holds a value from another repository that may, so which target runs it is unknown",
					l, strings.Join(attrs, ", ")),
				Fix: "list the file in its test target's srcs, or give suite " + suite + " an owner pattern in " + config.FileName,
			}}, true
		}
	}

	return nil, false
}

func outsideInputs(t *buildfile.Target) []string {
	var out []string

	for _, attr := range slices.Sorted(keys(t.Outside)) {
		if !readableAttrs[attr] && attr != "tags" {
			out = append(out, attr)
		}
	}

	return out
}

func ownerOf(s config.Suite, f string) (string, bool) {
	if s.Owner == "" {
		return "", false
	}

	base := path.Base(f)
	stem, _, _ := strings.Cut(base, ".")

	return strings.NewReplacer("{dir}", dirOf(f), "{file}", f, "{base}", base, "{stem}", stem).Replace(s.Owner), true
}

func (c *cover) tagged(suite string, s config.Suite) []Finding {
	var out []Finding

	found, tag := false, s.Tag

	for _, label := range slices.Sorted(keys(c.g.Targets)) {
		t := c.g.Targets[label]
		if !slices.Contains(t.Tags(), tag) || s.ExcludesTarget(label) {
			continue
		}

		found = true

		c.walk(label)

		if len(t.Unreadable) == 0 && unbound(t) {
			out = append(out, Finding{
				Kind:    KindUnbound,
				Path:    t.File,
				Target:  label,
				Problem: fmt.Sprintf("slice %s depends on nothing but its own sources, so no change outside them would run it", label),
				Fix:     "make it depend on what it checks (data or deps), or the suite's change selects nothing",
			})
		}
	}

	if !found {
		out = append(out, Finding{
			Kind:    KindUntagged,
			Path:    config.FileName,
			Problem: fmt.Sprintf("suite %s has no target tagged %s", suite, tag),
			Fix:     fmt.Sprintf("add the tag %s to the suite's targets, or give the suite tests patterns", tag),
		})
	}

	return out
}

func unbound(t *buildfile.Target) bool {
	for attr, labels := range t.Attrs {
		if attr != "srcs" && len(labels) > 0 {
			return false
		}
	}

	for _, tag := range t.Tags() {
		if strings.HasPrefix(tag, runTag) {
			return false
		}
	}

	return true
}

func matchAny(patterns []string, p string) bool {
	return slices.ContainsFunc(patterns, func(pat string) bool { return config.MatchGlob(pat, p) })
}

func (c *cover) uncovered() []Finding {
	var untested []string
	for _, u := range c.cfg.Untested {
		untested = append(untested, u.Paths...)
	}

	var out []Finding

	ignored := map[string]int{} // an ignored directory to its uncovered files

	for _, f := range c.tree.Files {
		if c.covered[f] || f == config.FileName || matchAny(untested, f) {
			continue
		}

		// a change to it runs everything, or a sample
		if _, ok := c.cfg.MatchesEverythingOn(f); ok || c.cfg.MatchesSampleOn(f) {
			continue
		}

		// Bazel sees nothing there, so no slice can cover it: one finding
		// for the whole directory says so
		if dir, in := c.g.IgnoredRoot(f); in {
			ignored[dir]++

			continue
		}

		out = append(out, Finding{
			Kind:    KindUncovered,
			Path:    f,
			Problem: "no slice depends on this file, so a change to it would run nothing",
			Fix:     fixFor(f),
		})
	}

	for _, dir := range slices.Sorted(maps.Keys(ignored)) {
		out = append(out, Finding{
			Kind:    KindUncovered,
			Path:    dir + "/",
			Problem: fmt.Sprintf("Bazel ignores this directory (.bazelignore or REPO.bazel), so no slice can depend on its %d files and a change to them would run nothing", ignored[dir]),
			Fix:     fmt.Sprintf("add to untested in %s: - paths: [%q] with the reason no test of this workspace needs them", config.FileName, dir+"/**"),
		})
	}

	return out
}

func fixFor(f string) string {
	if buildfile.IsBuildFile(f) || path.Ext(f) == ".bzl" {
		return "make a slice depend on a target of this package, or add the file to untested in " + config.FileName + " with a reason"
	}

	return "add it to the srcs or data of a target a slice depends on, or to untested in " + config.FileName + " with a reason"
}

// versionPinned warns, once a repository, of data files targets take from a
// module only its version pins: planning takes such data as unchanged while
// the version and registries are, which rests on the registry never changing
// a published version.
func versionPinned(tree Tree, g *buildfile.Graph) []Finding {
	if slices.Contains(tree.Files, "MODULE.bazel.lock") {
		return nil
	}

	text, ok := readText(tree, moduleFile)
	if !ok {
		return nil
	}

	f, err := build.ParseModule(moduleFile, []byte(text))
	if err != nil {
		return nil
	}

	deps, fixed := map[string]bool{}, map[string]bool{}

	for _, stmt := range f.Stmt {
		call, ok := stmt.(*build.CallExpr)
		if !ok {
			continue
		}

		r := &build.Rule{Call: call}

		switch r.Kind() {
		case "bazel_dep":
			deps[cmp.Or(r.AttrString("repo_name"), r.AttrString("name"))] = true
		case "git_override", "archive_override":
			fixed[r.AttrString("module_name")] = true
		}
	}

	var out []Finding

	seen := map[string]bool{}

	for _, label := range slices.Sorted(keys(g.Targets)) {
		for _, l := range g.Targets[label].Attrs["data"] {
			repo, rest, ok := strings.Cut(strings.TrimPrefix(l, "@"), "//")
			_, name, _ := strings.Cut(rest, ":")

			if !ok || !strings.HasPrefix(l, "@") || !strings.Contains(name, ".") || !deps[repo] || fixed[repo] || seen[repo] {
				continue
			}

			seen[repo] = true

			out = append(out, Finding{
				Kind:    KindVersionPinned,
				Path:    moduleFile,
				Target:  label,
				Problem: fmt.Sprintf("%s takes %s, which only the version of the module %s pins (MODULE.bazel.lock isn't tracked): planning counts it unchanged while that version and the registries are", label, l, repo),
				Fix:     "commit MODULE.bazel.lock to pin it exactly",
				Warning: true,
			})
		}
	}

	return out
}
