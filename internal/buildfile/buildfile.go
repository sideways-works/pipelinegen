// Package buildfile reads a repository's BUILD and .bzl files without Bazel:
// the targets they declare, what each names as its inputs, and the globs
// they match. It errs on the side of seeing more inputs, never fewer: a
// string that names an existing file or target counts as an input wherever
// it appears.
package buildfile

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/bazelbuild/buildtools/build"

	"github.com/sideways-works/pipelinegen/internal/config"
)

// Target is one rule (or opaque macro call) a BUILD file declares.
type Target struct {
	Label string
	Pkg   string
	Name  string
	Kind  string
	File  string
	// Attrs holds per attribute the labels it names, canonical: targets
	// and files of this repository (//pkg:name), and other repositories'
	// labels (@repo//pkg:name).
	Attrs   map[string][]string
	Strings map[string][]string
	// Unreadable says, per attribute, why its value can't be read
	// statically. Such a target's inputs are unknown, not empty.
	Unreadable map[string]string
	// Outside are the attributes holding a name loaded from another
	// repository's .bzl file: it covers nothing in this tree, but it may
	// hold strings of this package nobody can read here.
	Outside map[string]bool
	// Macro is, for a call of another repository's macro, the macro as
	// "<.bzl label>%<name>": what it reads inside can only be declared.
	Macro string
}

// Tags are the target's tags.
func (t *Target) Tags() []string { return t.Strings["tags"] }

// Graph is every target of the repository's BUILD files.
type Graph struct {
	Targets  map[string]*Target
	Files    map[string]bool
	Packages map[string]string
	Loads    map[string][]string
	// Errors are the BUILD and .bzl files that could not be read or parsed,
	// and the calls whose name can't be read, so no target stands for them.
	Errors []error

	read    func(string) ([]byte, error)
	self    map[string]bool // the names the root module calls itself by
	ignored func(dir string) bool
	bzl     map[string]*bzlFile
	byPath  []string
}

// ErrParse wraps a BUILD or .bzl file that can't be read.
var ErrParse = errors.New("build file")

var notInputs = map[string]bool{"name": true, "visibility": true, "tags": true, "licenses": true, "importpath": true}

// IsBuildFile tells whether a path is a BUILD file.
func IsBuildFile(p string) bool {
	b := path.Base(p)

	return b == "BUILD" || b == "BUILD.bazel"
}

// Load reads every BUILD file among the tracked files. read returns a
// file's content (from the work tree, or from git's index for a commit
// hook).
func Load(files []string, read func(string) ([]byte, error)) *Graph {
	g := &Graph{
		Targets:  map[string]*Target{},
		Files:    make(map[string]bool, len(files)),
		Packages: map[string]string{},
		Loads:    map[string][]string{},
		read:     read,
		bzl:      map[string]*bzlFile{},
	}

	for _, f := range files {
		g.Files[f] = true
	}

	g.self = selfNames(g.Files, read)

	ignored := ignoredDirs(g.Files, read)
	g.ignored = ignored

	for _, f := range files {
		if !IsBuildFile(f) || ignored(dirOf(f)) {
			continue
		}

		dir := dirOf(f)
		if prev, ok := g.Packages[dir]; ok && path.Base(prev) == "BUILD.bazel" {
			continue
		}

		g.Packages[dir] = f
	}

	// Bazel sees no file under a directory it ignores, so no glob does
	g.byPath = slices.Sorted(func(yield func(string) bool) {
		for f := range g.Files {
			if !ignored(dirOf(f)) && !yield(f) {
				return
			}
		}
	})

	for _, pkg := range slices.Sorted(mapKeys(g.Packages)) {
		g.loadPackage(pkg)
	}

	g.resolveAll()

	return g
}

// IgnoredRoot is, for a path in a directory Bazel ignores, the outermost
// ignored directory holding it (what .bazelignore or ignore_directories
// names); ok is false for a path Bazel sees.
func (g *Graph) IgnoredRoot(p string) (string, bool) {
	return ignoredRoot(g.ignored, p)
}

// Ignored reads which directories Bazel ignores in a tree (.bazelignore and
// REPO.bazel's ignore_directories) and tells, for a path, the outermost
// ignored directory holding it.
func Ignored(files []string, read func(string) ([]byte, error)) func(p string) (string, bool) {
	set := make(map[string]bool, len(files))
	for _, f := range files {
		set[f] = true
	}

	ignored := ignoredDirs(set, read)

	return func(p string) (string, bool) { return ignoredRoot(ignored, p) }
}

func ignoredRoot(ignored func(string) bool, p string) (string, bool) {
	root := ""

	for d := dirOf(p); d != ""; d = dirOf(d) {
		if ignored(d) {
			root = d
		}
	}

	return root, root != ""
}

// selfNames are the repository names MODULE.bazel gives the root module
// (its name and repo_name): a label in one of them is a label of this tree.
func selfNames(files map[string]bool, read func(string) ([]byte, error)) map[string]bool {
	out := map[string]bool{}

	if !files["MODULE.bazel"] {
		return out
	}

	data, err := read("MODULE.bazel")
	if err != nil {
		return out
	}

	f, err := build.ParseModule("MODULE.bazel", data)
	if err != nil {
		return out
	}

	for _, stmt := range f.Stmt {
		call, ok := stmt.(*build.CallExpr)
		if !ok {
			continue
		}

		r := &build.Rule{Call: call}
		if r.Kind() != "module" {
			continue
		}

		for _, attr := range []string{"name", "repo_name"} {
			if n := r.AttrString(attr); n != "" {
				out[n] = true
			}
		}
	}

	return out
}

func (g *Graph) canonical(pkg, s string) (string, bool) {
	l, ok := Canonical(pkg, s)
	if !ok || !strings.HasPrefix(l, "@") {
		return l, ok
	}

	repo, rest, _ := strings.Cut(l[1:], "//")
	if g.self[repo] {
		return "//" + rest, true
	}

	return l, true
}

var ignoreDirectories = regexp.MustCompile(`(?s)ignore_directories\(\s*\[(.*?)\]`)

var quoted = regexp.MustCompile(`"([^"]*)"|'([^']*)'`)

// ignoredDirs tells which directories Bazel ignores: those .bazelignore
// lists and those REPO.bazel's ignore_directories matches, with everything
// below; Bazel reads neither BUILD files nor sources there.
func ignoredDirs(files map[string]bool, read func(string) ([]byte, error)) func(dir string) bool {
	var paths, globs []string

	if files[".bazelignore"] {
		if data, err := read(".bazelignore"); err == nil {
			for line := range strings.SplitSeq(string(data), "\n") {
				if line = strings.Trim(strings.TrimSpace(line), "/"); line != "" && !strings.HasPrefix(line, "#") {
					paths = append(paths, line)
				}
			}
		}
	}

	if files["REPO.bazel"] {
		if data, err := read("REPO.bazel"); err == nil {
			for _, m := range ignoreDirectories.FindAllSubmatch(data, -1) {
				for _, q := range quoted.FindAllSubmatch(m[1], -1) {
					globs = append(globs, string(q[1])+string(q[2]))
				}
			}
		}
	}

	if len(paths) == 0 && len(globs) == 0 {
		return func(string) bool { return false }
	}

	memo := map[string]bool{}

	var ignored func(string) bool

	ignored = func(dir string) bool {
		if dir == "" {
			return false
		}

		if v, ok := memo[dir]; ok {
			return v
		}

		v := slices.Contains(paths, dir) ||
			slices.ContainsFunc(globs, func(g string) bool { return config.MatchGlob(g, dir) }) ||
			ignored(dirOf(dir))
		memo[dir] = v

		return v
	}

	return ignored
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func dirOf(p string) string {
	d := path.Dir(p)
	if d == "." {
		return ""
	}

	return d
}

// PackageOf is the package a path belongs to: its nearest directory with a
// BUILD file.
func (g *Graph) PackageOf(p string) (string, bool) {
	for d := dirOf(p); ; d = dirOf(d) {
		if _, ok := g.Packages[d]; ok {
			return d, true
		}

		if d == "" {
			return "", false
		}
	}
}

// FileLabel is a tracked file's label, in its package.
func (g *Graph) FileLabel(p string) (string, bool) {
	pkg, ok := g.PackageOf(p)
	if !ok {
		return "", false
	}

	rel := p
	if pkg != "" {
		rel = strings.TrimPrefix(p, pkg+"/")
	}

	return "//" + pkg + ":" + rel, true
}

// LabelFile is the tracked file a label names, if it names one.
func (g *Graph) LabelFile(label string) (string, bool) {
	rest, ok := strings.CutPrefix(label, "//")
	if !ok {
		return "", false
	}

	pkg, name, _ := strings.Cut(rest, ":")

	p := name
	if pkg != "" {
		p = pkg + "/" + name
	}

	if g.Files[p] {
		return p, true
	}

	return "", false
}

// Canonical turns a label written in package pkg into its canonical form,
// //pkg:name or @repo//pkg:name. ok is false for a string that is no label
// (a bare word, which may still name a file or target of the package).
func Canonical(pkg, s string) (string, bool) {
	switch {
	case strings.HasPrefix(s, "@@//"):
		s = s[2:]
	case strings.HasPrefix(s, "@//"):
		s = s[1:]
	case strings.HasPrefix(s, ":"):
		return "//" + pkg + s, true
	}

	repo := ""

	switch {
	case strings.HasPrefix(s, "//"):
	case strings.HasPrefix(s, "@"):
		r, rest, found := strings.Cut(strings.TrimLeft(s, "@"), "//")
		if !found {
			return "@" + r + "//:" + r, true
		}

		repo, s = "@"+r, "//"+rest
	default:
		return "", false
	}

	p, name, found := strings.Cut(s[2:], ":")
	if !found {
		name = path.Base(p)
	}

	return repo + "//" + p + ":" + name, true
}

func (g *Graph) glob(pkg string, include, exclude []string) []string {
	prefix := ""
	if pkg != "" {
		prefix = pkg + "/"
	}

	start := sort.SearchStrings(g.byPath, prefix)

	var out []string

	for _, f := range g.byPath[start:] {
		if !strings.HasPrefix(f, prefix) {
			break
		}

		rel := f[len(prefix):]
		if !matchAny(include, rel) || matchAny(exclude, rel) {
			continue
		}

		if own, _ := g.PackageOf(f); own != pkg {
			continue
		}

		out = append(out, "//"+pkg+":"+rel)
	}

	return out
}

func matchAny(patterns []string, rel string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool { return config.MatchGlob(p, rel) })
}

type bzlFile struct {
	path  string
	file  *build.File
	defs  map[string]*build.DefStmt
	vars  env
	loads map[string]loaded
	err   error
}

type loaded struct {
	module string // the canonical label of the .bzl file
	symbol string
}

func (g *Graph) loadPackage(pkg string) {
	buildPath := g.Packages[pkg]

	data, err := g.read(buildPath)
	if err != nil {
		g.Errors = append(g.Errors, fmt.Errorf("%w: %s: %w", ErrParse, buildPath, err))

		return
	}

	f, err := build.ParseBuild(buildPath, data)
	if err != nil {
		g.Errors = append(g.Errors, fmt.Errorf("%w: %w", ErrParse, err))

		return
	}

	file := &bzlFile{path: buildPath, file: f, vars: env{}, loads: map[string]loaded{}}
	g.scanTop(pkg, file)

	seen := map[string]bool{}
	g.collectLoads(file, seen)
	g.Loads[pkg] = slices.Sorted(mapKeys(seen))

	for _, stmt := range f.Stmt {
		if call, ok := stmt.(*build.CallExpr); ok {
			g.call(pkg, file, call, file.vars, 0)
		}
	}
}

func (g *Graph) scanTop(pkg string, file *bzlFile) {
	file.defs = map[string]*build.DefStmt{}

	for _, stmt := range file.file.Stmt {
		switch s := stmt.(type) {
		case *build.LoadStmt:
			module, ok := g.canonical(pkg, s.Module.Value)
			if !ok {
				continue
			}

			for i, to := range s.To {
				file.loads[to.Name] = loaded{module: module, symbol: s.From[i].Name}
			}
		case *build.AssignExpr:
			if id, ok := s.LHSIdent(); ok {
				file.vars[id.Name] = scope{g: g, pkg: pkg, file: file, vars: env{}}.eval(s.RHS)
			}
		case *build.DefStmt:
			file.defs[s.Name] = s
		}
	}
}

func (g *Graph) collectLoads(file *bzlFile, seen map[string]bool) {
	for _, l := range file.loads {
		p, ok := g.LabelFile(l.module)
		if !ok || seen[p] {
			continue
		}

		seen[p] = true

		if b := g.bzlAt(l.module); b != nil {
			g.collectLoads(b, seen)
		}
	}
}

func (g *Graph) bzlAt(module string) *bzlFile {
	p, ok := g.LabelFile(module)
	if !ok {
		return nil
	}

	if b, ok := g.bzl[p]; ok {
		if b.err != nil {
			return nil
		}

		return b
	}

	b := &bzlFile{path: p, vars: env{}, loads: map[string]loaded{}}
	g.bzl[p] = b

	data, err := g.read(p)
	if err == nil {
		b.file, err = build.ParseBzl(p, data)
	}

	if err != nil {
		b.err = err
		g.Errors = append(g.Errors, fmt.Errorf("%w: %s: %w", ErrParse, p, err))

		return nil
	}

	pkg, _ := g.PackageOf(p)
	g.scanTop(pkg, b)

	return b
}

// maxDepth bounds macro expansion, so a macro calling itself ends.
const maxDepth = 8

func (g *Graph) call(pkg string, file *bzlFile, call *build.CallExpr, vars env, depth int) {
	kind, macro, def := g.callee(file, call)
	sc := scope{g: g, pkg: pkg, file: file, vars: vars}

	kwargs := sc.arguments(call)

	name := kwargs["name"]
	if _, given := kwargs["name"]; given && (name.bad != "" || name.kind != kStr) {
		why := name.bad
		if why == "" {
			why = "it is no string"
		}

		g.Errors = append(g.Errors, fmt.Errorf("%w: %s: %s(): the name can't be read: %s", ErrParse, file.path, kind, why))

		return
	}

	if def != nil && depth < maxDepth {
		g.expand(pkg, macro, def, kwargs, depth+1)

		// a macro whose body declares no target of its own name is kept as
		// an opaque target, so its arguments still count as inputs
		if name.kind != kStr || g.Targets["//"+pkg+":"+name.str] != nil {
			return
		}
	}

	if name.kind != kStr || kind == "" {
		return
	}

	t := &Target{
		Macro:      g.outsideMacro(file, call),
		Label:      "//" + pkg + ":" + name.str,
		Pkg:        pkg,
		Name:       name.str,
		Kind:       kind,
		File:       file.path,
		Attrs:      map[string][]string{},
		Strings:    map[string][]string{},
		Unreadable: map[string]string{},
		Outside:    map[string]bool{},
	}

	for attr, v := range kwargs {
		if v.bad != "" {
			t.Unreadable[attr] = v.bad
		}

		if v.outside || v.kind == kExt {
			t.Outside[attr] = true
		}

		t.Strings[attr] = v.strings()
	}

	g.Targets[t.Label] = t
}

func (sc scope) arguments(call *build.CallExpr) map[string]value {
	kwargs := map[string]value{}

	for _, arg := range call.List {
		switch a := arg.(type) {
		case *build.AssignExpr:
			if id, ok := a.LHSIdent(); ok {
				kwargs[id.Name] = sc.eval(a.RHS)
			}
		case *build.UnaryExpr:
			if a.Op != "**" {
				continue
			}

			v := sc.eval(a.X)
			if v.bad != "" || v.kw == nil {
				kwargs["**"] = unreadable(build.FormatString(a) + " passes arguments that can't be read")

				continue
			}

			for k, x := range v.kw {
				if _, set := kwargs[k]; !set {
					kwargs[k] = x
				}
			}
		}
	}

	return kwargs
}

func (g *Graph) outsideMacro(file *bzlFile, call *build.CallExpr) string {
	id, ok := call.X.(*build.Ident)
	if !ok {
		return ""
	}

	if _, own := file.defs[id.Name]; own {
		return ""
	}

	l, ok := file.loads[id.Name]
	if !ok || !strings.HasPrefix(l.module, "@") {
		return ""
	}

	return l.module + "%" + l.symbol
}

// Glob is a glob in a package: its tracked files, outside its subpackages
// and what Bazel ignores, that match an include pattern, as file labels.
func (g *Graph) Glob(pkg string, include []string) []string {
	return g.glob(pkg, include, nil)
}

func (g *Graph) callee(file *bzlFile, call *build.CallExpr) (string, *bzlFile, *build.DefStmt) {
	switch x := call.X.(type) {
	case *build.DotExpr:
		return x.Name, nil, nil
	case *build.Ident:
		if def, ok := file.defs[x.Name]; ok {
			return x.Name, file, def
		}

		l, ok := file.loads[x.Name]
		if !ok {
			return x.Name, nil, nil
		}

		if b := g.bzlAt(l.module); b != nil {
			if def, ok := b.defs[l.symbol]; ok {
				return l.symbol, b, def
			}
		}

		return l.symbol, nil, nil
	default:
		return "", nil, nil
	}
}

func (g *Graph) expand(pkg string, file *bzlFile, def *build.DefStmt, kwargs map[string]value, depth int) {
	vars := g.bind(pkg, file, def, kwargs)

	g.body(pkg, file, def.Body, vars, depth)
}

func (g *Graph) bind(pkg string, file *bzlFile, def *build.DefStmt, kwargs map[string]value) env {
	vars := env{}
	named := map[string]bool{}

	for _, p := range def.Params {
		name, dflt := paramOf(p)
		if name == "" {
			continue
		}

		named[name] = true

		switch v, given := kwargs[name]; {
		case given:
			vars[name] = v
		case dflt != nil:
			vars[name] = scope{g: g, pkg: pkg, file: file, vars: env{}}.eval(dflt)
		default:
			vars[name] = value{kind: kNone}
		}
	}

	for _, p := range def.Params {
		x, ok := p.(*build.UnaryExpr)
		if !ok || x.Op != "**" {
			continue
		}

		if id, ok := x.X.(*build.Ident); ok {
			rest := map[string]value{}

			for k, v := range kwargs {
				if !named[k] {
					rest[k] = v
				}
			}

			vars[id.Name] = value{kind: kNone, kw: rest}
		}
	}

	return vars
}

//nolint:ireturn // a default is any expression; the syntax tree's type is the honest one
func paramOf(p build.Expr) (string, build.Expr) {
	switch x := p.(type) {
	case *build.Ident:
		return x.Name, nil
	case *build.AssignExpr:
		if id, ok := x.LHSIdent(); ok {
			return id.Name, x.RHS
		}
	}

	return "", nil
}

func (g *Graph) body(pkg string, file *bzlFile, stmts []build.Expr, vars env, depth int) {
	for _, stmt := range stmts {
		switch s := stmt.(type) {
		case *build.CallExpr:
			g.call(pkg, file, s, vars, depth)
		case *build.AssignExpr:
			if id, ok := s.LHSIdent(); ok {
				vars[id.Name] = scope{g: g, pkg: pkg, file: file, vars: vars}.eval(s.RHS)
			}
		case *build.IfStmt:
			g.body(pkg, file, s.True, vars, depth)
			g.body(pkg, file, s.False, vars, depth)
		}
	}
}

// resolveAll fills every target's Attrs with the strings that name a target
// or tracked file, as canonical labels. It runs once all packages are read,
// so a bare name may refer to a target declared later.
func (g *Graph) resolveAll() {
	for _, t := range g.Targets {
		for attr, values := range t.Strings {
			if notInputs[attr] {
				continue
			}

			for _, v := range values {
				if l, ok := g.resolve(t.Pkg, v); ok {
					t.Attrs[attr] = append(t.Attrs[attr], l)
				}
			}
		}
	}
}

func (g *Graph) resolve(pkg, v string) (string, bool) {
	if l, ok := g.canonical(pkg, v); ok {
		if strings.HasPrefix(l, "@") {
			return l, true
		}

		if g.Targets[l] != nil {
			return l, true
		}

		if _, ok := g.LabelFile(l); ok {
			return l, true
		}

		return "", false
	}

	// a make variable is no file; a space or a parenthesis may well be
	// part of one
	if v == "" || strings.Contains(v, "$(") {
		return "", false
	}

	l := "//" + pkg + ":" + v
	if g.Targets[l] != nil {
		return l, true
	}

	if _, ok := g.LabelFile(l); ok {
		return l, true
	}

	return "", false
}

// Inputs are the labels a target names in any attribute, sorted.
func (t *Target) Inputs() []string {
	n := 0
	for _, ls := range t.Attrs {
		n += len(ls)
	}

	out := make([]string, 0, n)
	for _, ls := range t.Attrs {
		out = append(out, ls...)
	}

	sort.Strings(out)

	return slices.Compact(out)
}
