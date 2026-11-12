package release

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

// A release's NOTICE names the program, its copyright and licence, and each
// piece of third-party software its release files include, with that
// software's licence and NOTICE. For Go that is every module linked into a
// released main package on a release platform, and the Go standard library;
// for Node every npm package the bundle includes.

const (
	separator = "--------------------------------------------------------------------------------"
	copyright = "Copyright 2026 Sideways Works UG (haftungsbeschränkt)"
	family    = "github.com/sideways-works"
)

type noticeText struct {
	b     strings.Builder
	first bool
}

func (n *noticeText) entry(title string) {
	if !n.first {
		n.b.WriteString("\n")
	}

	n.first = false

	n.b.WriteString(separator + "\n" + title + "\n\n")
}

// fold breaks a line as fold -s -w 73 does, at the last blank within the
// width, and drops the blank it broke at.
func fold(line string) []string {
	const width = 73

	var out []string

	for len(line) > width {
		i := strings.LastIndexByte(line[:width], ' ')
		if i < 0 {
			out = append(out, line[:width])
			line = line[width:]

			continue
		}

		out = append(out, line[:i])
		line = line[i+1:]
	}

	return append(out, strings.TrimSuffix(line, " "))
}

func (n *noticeText) header(name, what string, own []string) {
	n.b.WriteString(name + "\n" + copyright + "\n\n")

	verb := "include"
	if !strings.HasSuffix(what, "s") {
		verb = "includes"
	}

	var text string

	if len(own) == 0 {
		text = "This product is licensed under the Apache License, Version 2.0 (see\nLICENSE). Its " + what + " " + verb +
			" the third-party software below,\neach under its own licence, reproduced here as those licences require."
	} else {
		names := strings.Join(own, ", ")
		if i := strings.LastIndex(names, ", "); i >= 0 {
			names = names[:i] + " and " + names[i+2:]
		}

		has := "have"
		if len(own) == 1 {
			has = "has"
		}

		text = "This product is licensed under the Apache License, Version 2.0 (see LICENSE). Its " + what + " " + verb +
			" code from " + names + ", which " + has + " the same copyright holder and licence, and the third-party software below, each under its own licence, reproduced here as those licences require."
	}

	for line := range strings.SplitSeq(text, "\n") {
		for _, l := range fold(line) {
			n.b.WriteString(l + "\n")
		}
	}

	n.b.WriteString("\n")
}

// file adds a text file as sed "s/\r$//" prints it, and a newline.
func (n *noticeText) file(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // a licence or notice of linked software
	if err != nil {
		return fmt.Errorf("notice: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}

	n.b.WriteString(strings.Join(lines, "\n") + "\n")

	return nil
}

var (
	licenceFile = regexp.MustCompile(`(?i)^(licen[cs]e|copying)`)
	noticeFile  = regexp.MustCompile(`(?i)^notice|noticetext`)
)

func (n *noticeText) texts(dir, what string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("notice: %s: %w", what, err)
	}

	var licences, notices []string

	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}

		if name := e.Name(); licenceFile.MatchString(name) {
			licences = append(licences, name)
		}

		if name := e.Name(); noticeFile.MatchString(name) {
			notices = append(notices, name)
		}
	}

	if len(licences) == 0 {
		return refuseNotice("%s has no licence file in %s", what, dir)
	}

	slices.Sort(licences)
	slices.Sort(notices)

	for _, f := range append(licences, notices...) {
		if err := n.file(filepath.Join(dir, f)); err != nil {
			return err
		}
	}

	return nil
}

func refuseNotice(format string, args ...any) error {
	return fmt.Errorf("%w: notice: %s", ErrRelease, fmt.Sprintf(format, args...))
}

func familyRepository(name string) string {
	for _, k := range plugin.Kinds {
		if base, ok := strings.CutSuffix(name, "-"+k); ok && base != "" {
			return "pipelinegen-" + k + "-" + base
		}
	}

	return name
}

type module struct{ path, version, dir string }

func goModules(ctx context.Context, dir string, mains []string) ([]module, []string, error) {
	const tmpl = `{{with .Module}}{{if not .Main}}{{if and .Replace .Replace.Version}}{{.Replace.Path}} {{.Replace.Version}} {{.Replace.Dir}}{{else if .Replace}}{{.Path}} (local) {{.Replace.Dir}}{{else}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}{{end}}`

	lines := map[string]bool{}

	for _, platform := range platforms {
		goos, goarch, _ := strings.Cut(platform, "-")

		args := append([]string{"list", "-deps", "-f", tmpl}, mains...)
		env := append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)

		cmd := exec.CommandContext(ctx, "go", args...) //nolint:gosec // the repository's own main packages
		cmd.Dir, cmd.Env, cmd.Stderr = dir, env, os.Stderr

		out, err := cmd.Output()
		if err != nil {
			return nil, nil, fmt.Errorf("notice: go list: %w", err)
		}

		for l := range strings.SplitSeq(string(out), "\n") {
			if l != "" {
				lines[l] = true
			}
		}
	}

	var (
		mods []module
		own  []string
	)

	for _, l := range slices.Sorted(func(yield func(string) bool) {
		for l := range lines {
			if !yield(l) {
				return
			}
		}
	}) {
		f := strings.SplitN(l, " ", 3)
		if len(f) < 3 {
			continue
		}

		if strings.HasPrefix(f[0], family+"/") {
			parts := strings.SplitN(f[0], "/", 4)

			own = append(own, family+"/"+familyRepository(parts[2]))

			continue
		}

		mods = append(mods, module{path: f[0], version: f[1], dir: f[2]})
	}

	slices.Sort(own)

	return mods, slices.Compact(own), nil
}

// GoNotice is the NOTICE of a Go repository's release of the program
// name, built from the main packages mains (./cmd/x, .).
func GoNotice(ctx context.Context, dir, name string, mains []string) (string, error) {
	mods, own, err := goModules(ctx, dir, mains)
	if err != nil {
		return "", err
	}

	goroot, err := output(ctx, dir, "go", "env", "GOROOT")
	if err != nil {
		return "", err
	}

	version, err := output(ctx, dir, "go", "env", "GOVERSION")
	if err != nil {
		return "", err
	}

	n := &noticeText{first: true}
	n.header(name, "release binaries", own)
	n.entry("The Go standard library (" + version + ")")

	if err := n.texts(goroot, "the Go standard library"); err != nil {
		return "", err
	}

	for _, m := range mods {
		n.entry(m.path + " " + m.version)

		if err := n.texts(m.dir, m.path); err != nil {
			return "", err
		}

		if err := n.embedded(ctx, m); err != nil {
			return "", err
		}
	}

	return n.b.String(), nil
}

// NodeNotice is the NOTICE of a Node repository's release of the program
// name, whose bundle includes the packages at the lockfile paths given
// (node_modules/x).
func NodeNotice(dir, name string, paths []string) (string, error) {
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}

	data, err := os.ReadFile(filepath.Join(dir, "package-lock.json")) //nolint:gosec // the package's own lockfile
	if err == nil {
		err = json.Unmarshal(data, &lock)
	}

	if err != nil {
		return "", fmt.Errorf("notice: package-lock.json: %w", err)
	}

	var own, third []string

	scoped := regexp.MustCompile(`^.*node_modules/@sideways-works/`)

	for _, p := range paths {
		p = strings.TrimPrefix(strings.TrimSuffix(p, "/"), "./")
		if scoped.MatchString(p) {
			own = append(own, family+"/"+familyRepository(scoped.ReplaceAllString(p, "")))
		} else {
			third = append(third, p)
		}
	}

	n := &noticeText{first: true}
	n.header(name, "release files", own)

	for _, p := range third {
		pkg, ok := lock.Packages[p]
		if !ok {
			return "", refuseNotice("package-lock.json has no %s", p)
		}

		title := regexp.MustCompile(`^.*node_modules/`).ReplaceAllString(p, "")
		n.entry(title + " " + pkg.Version)

		if err := n.texts(filepath.Join(dir, p), title); err != nil {
			return "", err
		}
	}

	return n.b.String(), nil
}

// embedded adds what a module ships in another form than Go code, with
// licences kept below its root or not at all: go-pgquery embeds libpg_query
// compiled to WebAssembly, with PostgreSQL's code, protobuf-c, xxHash and
// wasi-libc in it, whose licence files are fetched into $NOTICE_UPSTREAM
// (default ~/.cache/notice-upstream).
func (n *noticeText) embedded(ctx context.Context, m module) error {
	if m.path != "github.com/wasilibs/go-pgquery" {
		return nil
	}

	c := filepath.Join(m.dir, "internal", "cparser")
	if !exists(filepath.Join(c, "COPYRIGHT")) {
		return refuseNotice("%s has no internal/cparser/COPYRIGHT", m.path)
	}

	if err := n.file(filepath.Join(c, "COPYRIGHT")); err != nil {
		return err
	}

	protobuf := firstSubmatch(filepath.Join(c, "include", "protobuf-c.h"), `(?m)^#define PROTOBUF_C_VERSION[ \t\v\f\r]*"(.*)"(.*)$`)
	n.entry("protobuf-c " + protobuf + ", as embedded in " + m.path + " " + m.version + "'s parser")

	if err := n.leadingComment(filepath.Join(c, "protobuf-c.c")); err != nil {
		return err
	}

	n.b.WriteString("\n")

	n.entry("xxHash " + xxhashVersion(filepath.Join(c, "include", "xxhash.h")) + ", as embedded in " + m.path + " " + m.version + "'s parser")

	if err := n.leadingComment(filepath.Join(c, "include", "xxhash.h")); err != nil {
		return err
	}

	n.b.WriteString("\n")

	sdk := firstSubmatch(filepath.Join(m.dir, "buildtools", "wasm", "Dockerfile"), `(?m)^FROM ghcr\.io/webassembly/wasi-sdk:(wasi-sdk-[0-9.]*)`)
	if sdk == "" {
		return refuseNotice("%s: no wasi-sdk tag in buildtools/wasm/Dockerfile", m.path)
	}

	up, commit, err := wasiLibc(ctx, sdk)
	if err != nil {
		return err
	}

	n.entry("wasi-libc " + commit[:12] + " (the C library of " + sdk + "), as embedded in " + m.path + " " + m.version + "'s parser")

	for _, f := range wasiLibcFiles {
		if err := n.file(filepath.Join(up, filepath.FromSlash(f))); err != nil {
			return err
		}
	}

	return nil
}

var wasiLibcFiles = []string{licenseFile, "LICENSE-MIT", "libc-bottom-half/cloudlibc/LICENSE", "libc-top-half/musl/COPYRIGHT"}

func firstSubmatch(path, pattern string) string {
	data, err := os.ReadFile(path) //nolint:gosec // a module's own source
	if err != nil {
		return ""
	}

	// as sed's s///p prints the line: the match replaced, the rest kept
	if m := regexp.MustCompile(pattern).FindSubmatch(data); m != nil {
		return string(slices.Concat(m[1:]...))
	}

	return ""
}

func xxhashVersion(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // a module's own source
	if err != nil {
		return ""
	}

	var parts []string

	for line := range strings.SplitSeq(string(data), "\n") {
		if regexp.MustCompile(`^#define XXH_VERSION_(MAJOR|MINOR|RELEASE)`).MatchString(line) {
			if f := strings.Fields(line); len(f) >= 3 {
				parts = append(parts, f[2])
			}
		}
	}

	return strings.Join(parts, ".")
}

var (
	commentEnd   = regexp.MustCompile(`[ \t\r\n\f\v]*\*/[ \t\r\n\f\v]*$`)
	commentStart = regexp.MustCompile(`^/\*[ \t\r\n\f\v]?`)
	commentStar  = regexp.MustCompile(`^[ \t\r\n\f\v]*\*[ \t\r\n\f\v]?`)
)

func (n *noticeText) leadingComment(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // a module's own source
	if err != nil {
		return fmt.Errorf("notice: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	if !strings.HasPrefix(lines[0], "/*") {
		return refuseNotice("%s starts with no comment", path)
	}

	for i, line := range lines {
		done := commentEnd.MatchString(line)
		line = commentEnd.ReplaceAllString(line, "")
		line = commentStart.ReplaceAllString(line, "")
		line = commentStar.ReplaceAllString(line, "")

		if (i != 0 || line != "") && (!done || line != "") {
			n.b.WriteString(line + "\n")
		}

		if done {
			break
		}
	}

	return nil
}

func wasiLibc(ctx context.Context, sdk string) (string, string, error) {
	base := os.Getenv("NOTICE_UPSTREAM")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", fmt.Errorf("notice: %w", err)
		}

		base = filepath.Join(home, ".cache", "notice-upstream")
	}

	up := filepath.Join(base, "wasi-libc-"+sdk)

	if data, err := os.ReadFile(filepath.Join(up, "commit")); err == nil { //nolint:gosec // the upstream cache
		return up, strings.TrimSpace(string(data)), nil
	}

	api, err := fetch(ctx, "https://api.github.com/repos/WebAssembly/wasi-sdk/contents/src/wasi-libc?ref="+sdk)
	if err != nil {
		return "", "", refuseNotice("can't find the wasi-libc commit of %s", sdk)
	}

	m := regexp.MustCompile(`"sha": *"([0-9a-f]*)"`).FindSubmatch(api)
	if m == nil || len(m[1]) < 12 {
		return "", "", refuseNotice("can't find the wasi-libc commit of %s", sdk)
	}

	commit := string(m[1])

	for _, f := range wasiLibcFiles {
		data, err := fetch(ctx, "https://raw.githubusercontent.com/WebAssembly/wasi-libc/"+commit+"/"+f)
		if err != nil {
			return "", "", refuseNotice("can't fetch wasi-libc's %s at %s", f, commit)
		}

		p := filepath.Join(up, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil { //nolint:gosec // the upstream cache, under a fixed list of names
			return "", "", fmt.Errorf("notice: %w", err)
		}

		if err := os.WriteFile(p, data, 0o600); err != nil { //nolint:gosec // the upstream cache, under a fixed list of names
			return "", "", fmt.Errorf("notice: %w", err)
		}
	}

	if err := os.WriteFile(filepath.Join(up, "commit"), []byte(commit+"\n"), 0o600); err != nil { //nolint:gosec // the upstream cache
		return "", "", fmt.Errorf("notice: %w", err)
	}

	return up, commit, nil
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}

	req.Header.Set("User-Agent", "notice")

	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: GET %s: %s", ErrRelease, url, resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}

	return data, nil
}

// bundledScript lists the npm packages an esbuild bundle includes.
//
//go:embed bundled.mjs
var bundledScript string

// noticeBuilds are the esbuild builds package.json lists under
// "pipelinegen": {"notice": [...]}; a package that lists none can't say what
// its NOTICE covers and is refused, and an empty list says the bundle
// includes no npm package.
func noticeBuilds(dir string) ([]json.RawMessage, error) {
	var pkg struct {
		Pipelinegen struct {
			Notice *[]json.RawMessage `json:"notice"`
		} `json:"pipelinegen"`
	}

	data, err := os.ReadFile(filepath.Join(dir, "package.json")) //nolint:gosec // the package's own manifest
	if err == nil {
		err = json.Unmarshal(data, &pkg)
	}

	if err != nil {
		return nil, refuseNotice("package.json: %v", err)
	}

	if pkg.Pipelinegen.Notice == nil {
		return nil, refuse(`package.json names no builds under "pipelinegen": {"notice": [...]}, whose bundles the NOTICE covers ([] for a bundle of no npm package)`)
	}

	return *pkg.Pipelinegen.Notice, nil
}

func bundled(ctx context.Context, dir string, builds []json.RawMessage) ([]string, error) {
	spec, err := json.Marshal(builds)
	if err != nil {
		return nil, fmt.Errorf("notice: %w", err)
	}

	script := filepath.Join(dir, ".pipelinegen-bundled.mjs")
	if err := os.WriteFile(script, []byte(bundledScript), 0o600); err != nil {
		return nil, fmt.Errorf("notice: %w", err)
	}

	defer func() { _ = os.Remove(script) }() //nolint:errcheck // a scratch file

	out, err := output(ctx, dir, "node", script, dir, string(spec))
	if err != nil {
		return nil, fmt.Errorf("notice: the bundles' packages: %w", err)
	}

	var paths []string

	for l := range strings.SplitSeq(out, "\n") {
		if l != "" {
			paths = append(paths, l)
		}
	}

	return paths, nil
}

// Notice is the NOTICE the release of the repository in dir needs: for
// what its programs link, or for what its bundle includes.
func Notice(ctx context.Context, o IO, dir string) (string, error) {
	// the repository is named by its directory when it has no origin
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("notice: %w", err)
	}

	return noticeFor(ctx, o, dir, plugin.Program(repositoryName(ctx, dir)))
}

func noticeFor(ctx context.Context, o IO, dir, name string) (string, error) {
	switch isGo := exists(filepath.Join(dir, "go.mod")); {
	case isGo && !exists(filepath.Join(dir, "cmd", name)) && rootMain(dir):
		name = plugin.Program(filepath.Base(moduleLine(filepath.Join(dir, "go.mod"))))

		return GoNotice(ctx, dir, name, []string{"."})
	case isGo && exists(filepath.Join(dir, "cmd", name)):
		return GoNotice(ctx, dir, name, []string{"./cmd/" + name})
	case exists(filepath.Join(dir, "package.json")):
		builds, err := noticeBuilds(dir)
		if err != nil {
			return "", err
		}

		var paths []string

		if len(builds) > 0 {
			if !exists(filepath.Join(dir, "node_modules")) {
				if err := (IO{Stdout: discard{}, Stderr: o.Stderr}).run(ctx, dir, os.Environ(), "npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"); err != nil {
					return "", err
				}
			}

			if paths, err = bundled(ctx, dir, builds); err != nil {
				return "", err
			}
		}

		return NodeNotice(dir, name, paths)
	}

	return "", refuse("%s is neither a Go module with cmd/%s or a main package at its root, nor a Node package", dir, name)
}

func checkNotice(ctx context.Context, o IO, src, name string) error {
	want, err := noticeFor(ctx, o, src, name)
	if err != nil {
		return err
	}

	got, err := os.ReadFile(filepath.Join(src, "NOTICE")) //nolint:gosec // the release's own NOTICE
	if err != nil || string(got) != want {
		return refuse("NOTICE is not the one its release files need: write it with `go run ./ci/release notice DIR` in pipelinegen's checkout, and commit it")
	}

	return nil
}
