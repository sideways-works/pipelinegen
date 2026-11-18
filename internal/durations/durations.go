// Package durations keeps the recorded test time of each test file, the
// numbers pack sizes slices by, and reads them from test runs' output.
package durations

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
)

const Version = 1

// Plain is the parameter key of a suite run without parameter sets.
const Plain = ""

// File is a durations file: per test file (a path from the repository's
// root), the seconds its tests took per parameter set.
type File struct {
	Files map[string]map[string]float64
}

// ErrFormat wraps a durations file or a test report that can't be read.
var ErrFormat = errors.New("durations")

func New() *File {
	return &File{Files: map[string]map[string]float64{}}
}

// Read reads a durations file. A missing file is an empty one, so a suite
// can start before its first run.
func Read(path string) (*File, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path is the config's durations file
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}

	if err != nil {
		return nil, fmt.Errorf("read durations: %w", err)
	}

	return Parse(data)
}

// Parse reads a durations file's text. A file's value is a number of
// seconds, or an object of seconds per parameter set.
func Parse(data []byte) (*File, error) {
	var raw struct {
		Version int                        `json:"version"`
		Files   map[string]json.RawMessage `json:"files"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrFormat, err)
	}

	if raw.Version != Version {
		return nil, fmt.Errorf("%w: version %d, want %d", ErrFormat, raw.Version, Version)
	}

	f := New()

	for name, v := range raw.Files {
		var secs float64
		if err := json.Unmarshal(v, &secs); err == nil {
			f.Files[name] = map[string]float64{Plain: secs}

			continue
		}

		var per map[string]float64
		if err := json.Unmarshal(v, &per); err != nil {
			return nil, fmt.Errorf("%w: %s: a number of seconds or an object of them", ErrFormat, name)
		}

		f.Files[name] = per
	}

	return f, nil
}

// Marshal writes one test file per line, sorted, so a run's update shows as a small diff; a file
// timed without parameter sets is a plain number.
func (f *File) Marshal() []byte {
	var b bytes.Buffer

	fmt.Fprintf(&b, "{\"version\": %d, \"files\": {", Version)

	names := make([]string, 0, len(f.Files))
	for n := range f.Files {
		names = append(names, n)
	}

	sort.Strings(names)

	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}

		b.WriteString("\n  ")
		b.WriteString(quote(n))
		b.WriteString(": ")

		per := f.Files[n]
		if v, ok := per[Plain]; ok && len(per) == 1 {
			b.WriteString(number(v))

			continue
		}

		params := make([]string, 0, len(per))
		for p := range per {
			params = append(params, p)
		}

		sort.Strings(params)
		b.WriteByte('{')

		for j, p := range params {
			if j > 0 {
				b.WriteString(", ")
			}

			b.WriteString(quote(p))
			b.WriteString(": ")
			b.WriteString(number(per[p]))
		}

		b.WriteByte('}')
	}

	if len(names) > 0 {
		b.WriteByte('\n')
	}

	b.WriteString("}}\n")

	return b.Bytes()
}

func (f *File) Write(path string) error {
	if err := os.WriteFile(path, f.Marshal(), 0o644); err != nil { //nolint:gosec // a checked-in file, readable by all
		return fmt.Errorf("write durations: %w", err)
	}

	return nil
}

func quote(s string) string {
	q, err := json.Marshal(s)
	if err != nil {
		// unreachable for a string; Go's quoting is the closest fallback
		return strconv.Quote(s)
	}

	return string(q)
}

// number rounds to a tenth of a second: finer noise would change the file
// on every run.
func number(v float64) string {
	return strconv.FormatFloat(round(v), 'f', -1, 64)
}

func round(v float64) float64 {
	return math.Round(v*10) / 10
}

// Seconds is a test file's recorded time summed over params (every
// recorded parameter set when params is empty).
func (f *File) Seconds(file string, params []string) (secs float64, ok bool) {
	per, found := f.Files[file]
	if !found {
		return 0, false
	}

	for p, v := range per {
		if len(params) == 0 || slices.Contains(params, p) {
			secs += v
			ok = true
		}
	}

	return secs, ok
}

// Update takes a run's measured times: measured parameter sets get new values, the rest keep theirs;
// with exists set, files it doesn't hold were deleted and are dropped.
func (f *File) Update(measured map[string]map[string]float64, exists func(string) bool) {
	for file, per := range measured {
		if f.Files[file] == nil {
			f.Files[file] = map[string]float64{}
		}

		for p, v := range per {
			f.Files[file][p] = round(v)
		}
	}

	if exists == nil {
		return
	}

	for file := range f.Files {
		if !exists(file) {
			delete(f.Files, file)
		}
	}
}
