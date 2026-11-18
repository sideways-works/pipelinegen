package durations

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gotests"
)

// Reports are one run's test reports, to record in a suite's durations.
type Reports struct {
	GoJSON []io.Reader
	// Param names the parameter set a go test run, or the Seconds, ran
	// with (empty for none).
	Param      string
	Playwright []io.Reader
	// PlaywrightBase is the reports' rootDir from the repository's root; empty, it is found from the
	// report's rootDir and the suite's test files.
	PlaywrightBase string
	// Seconds are test files' times measured elsewhere (a batch's journal:
	// the wall time of each slice's parts), under Param.
	Seconds map[string]float64
}

// Repo is what Record reads of a repository: its files and how to read
// one.
type Repo struct {
	Files []string
	Read  func(string) ([]byte, error)
}

var ErrRecord = errors.New("record durations")

type Recorded struct {
	Path    string
	Content []byte
	// Unknown are tests the reports timed that no test file of the suite
	// declares (removed since, or run from elsewhere).
	Unknown []string
}

// Record takes a run's reports into a suite's durations file: measured files get new times, others
// keep theirs, and files gone are dropped. Go tests map to files by their declarations.
func Record(cfg *config.Config, repo Repo, suite string, reports Reports) (*Recorded, error) {
	s, ok := cfg.Suites[suite]

	switch {
	case !ok:
		return nil, fmt.Errorf("%w: no suite %s", ErrRecord, suite)
	case !s.Implicit():
		return nil, fmt.Errorf("%w: suite %s has no test files", ErrRecord, suite)
	case s.Durations == "":
		return nil, fmt.Errorf("%w: suite %s names no durations file", ErrRecord, suite)
	}

	tests := suiteFiles(s, repo.Files)

	f, err := current(repo, s.Durations)
	if err != nil {
		return nil, err
	}

	out := &Recorded{Path: s.Durations}
	measured := map[string]map[string]float64{}

	for _, r := range reports.GoJSON {
		m, unknown, err := recordGo(r, repo, tests, reports.Param)
		if err != nil {
			return nil, err
		}

		merge(measured, m)

		out.Unknown = append(out.Unknown, unknown...)
	}

	for _, r := range reports.Playwright {
		m, err := recordPlaywright(r, tests, reports.PlaywrightBase)
		if err != nil {
			return nil, err
		}

		merge(measured, m)
	}

	for file, secs := range reports.Seconds {
		merge(measured, map[string]map[string]float64{file: {reports.Param: secs}})
	}

	isTest := map[string]bool{}
	for _, t := range tests {
		isTest[t] = true
	}

	for file := range measured {
		if !isTest[file] {
			delete(measured, file)
		}
	}

	f.Update(measured, func(p string) bool { return isTest[p] })
	out.Content = f.Marshal()

	slices.Sort(out.Unknown)
	out.Unknown = slices.Compact(out.Unknown)

	return out, nil
}

func merge(into, from map[string]map[string]float64) {
	for file, per := range from {
		if into[file] == nil {
			into[file] = map[string]float64{}
		}

		for p, v := range per {
			into[file][p] += v
		}
	}
}

func suiteFiles(s config.Suite, files []string) []string {
	match := func(patterns []string, f string) bool {
		return slices.ContainsFunc(patterns, func(p string) bool { return config.MatchGlob(p, f) })
	}

	var out []string

	for _, f := range files {
		if match(s.Tests, f) && !match(s.Exclude, f) && !match(s.Helpers, f) {
			out = append(out, f)
		}
	}

	return out
}

func current(repo Repo, p string) (*File, error) {
	data, err := repo.Read(p)
	if errors.Is(err, fs.ErrNotExist) {
		return New(), nil
	}

	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %w", ErrRecord, p, err)
	}

	return Parse(data)
}

func recordGo(r io.Reader, repo Repo, tests []string, param string) (map[string]map[string]float64, []string, error) {
	byPkg, err := GoTests(r)
	if err != nil {
		return nil, nil, err
	}

	mods, err := modules(repo)
	if err != nil {
		return nil, nil, err
	}

	out := map[string]map[string]float64{}

	var unknown []string

	for _, pkg := range slices.Sorted(keys(byPkg)) {
		dir, ok := dirOf(pkg, mods)
		if !ok {
			unknown = append(unknown, prefixed(pkg, byPkg[pkg])...)

			continue
		}

		var files []string

		for _, t := range tests {
			if path.Dir(t) == dir && strings.HasSuffix(t, "_test.go") {
				files = append(files, t)
			}
		}

		byFile, err := gotests.ByFile(files, repo.Read)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrRecord, err)
		}

		m, unk := GoFiles(byPkg[pkg], gotests.FileOf(byFile), param)
		merge(out, m)

		for _, u := range unk {
			unknown = append(unknown, pkg+"."+u)
		}
	}

	return out, unknown, nil
}

func prefixed(pkg string, tests map[string]float64) []string {
	out := make([]string, 0, len(tests))
	for name := range tests {
		out = append(out, pkg+"."+name)
	}

	return out
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

func modules(repo Repo) (map[string]string, error) {
	out := map[string]string{}

	for _, f := range repo.Files {
		if path.Base(f) != "go.mod" {
			continue
		}

		data, err := repo.Read(f)
		if err != nil {
			return nil, fmt.Errorf("%w: read %s: %w", ErrRecord, f, err)
		}

		if mod := modulePath(string(data)); mod != "" {
			dir := path.Dir(f)
			if dir == "." {
				dir = ""
			}

			out[mod] = dir
		}
	}

	return out, nil
}

func modulePath(gomod string) string {
	for line := range strings.SplitSeq(gomod, "\n") {
		line, _, _ = strings.Cut(line, "//")
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "module" {
			return strings.Trim(fields[1], `"`)
		}
	}

	return ""
}

func dirOf(pkg string, mods map[string]string) (string, bool) {
	best := ""

	for mod := range mods {
		if (pkg == mod || strings.HasPrefix(pkg, mod+"/")) && len(mod) > len(best) {
			best = mod
		}
	}

	if best == "" {
		return "", false
	}

	rel := strings.TrimPrefix(strings.TrimPrefix(pkg, best), "/")

	return path.Join(mods[best], rel), true
}

func recordPlaywright(r io.Reader, tests []string, base string) (map[string]map[string]float64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%w: read a Playwright report: %w", ErrRecord, err)
	}

	m, err := Playwright(bytes.NewReader(data), "")
	if err != nil {
		return nil, err
	}

	if base == "" {
		if base, err = playwrightBase(data, m, tests); err != nil {
			return nil, err
		}
	}

	out := make(map[string]map[string]float64, len(m))
	for f, per := range m {
		out[path.Join(base, f)] = per
	}

	return out, nil
}

// playwrightBase is the shortest tail of the report's rootDir (the root itself included) under which
// its files are the suite's test files.
func playwrightBase(data []byte, m map[string]map[string]float64, tests []string) (string, error) {
	var rep struct {
		Config struct {
			RootDir string `json:"rootDir"` //nolint:tagliatelle // Playwright's field name
		} `json:"config"`
	}

	if err := json.Unmarshal(data, &rep); err != nil {
		return "", fmt.Errorf("%w: playwright report: %w", ErrFormat, err)
	}

	isTest := map[string]bool{}
	for _, t := range tests {
		isTest[t] = true
	}

	parts := strings.Split(strings.Trim(path.Clean(rep.Config.RootDir), "/"), "/")

	for k := 0; k <= len(parts); k++ {
		base := strings.Join(parts[len(parts)-k:], "/")

		found := 0

		for f := range m {
			if isTest[path.Join(base, f)] {
				found++
			}
		}

		if found > 0 && found*2 >= len(m) {
			return base, nil
		}
	}

	return "", fmt.Errorf("%w: no tail of the report's rootDir %q holds the suite's test files; pass the base directory", ErrRecord, rep.Config.RootDir)
}
