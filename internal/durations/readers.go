package durations

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
)

// GoTests reads `go test -json` output: per package import path, the
// seconds of each top-level test. A subtest's time is inside its parent's,
// so subtests are left out; a test run several times (-count) adds up.
// Lines that are not JSON events (a build's own output) are skipped.
func GoTests(r io.Reader) (map[string]map[string]float64, error) {
	out := map[string]map[string]float64{}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}

		var ev struct {
			Action  string  `json:"Action"`  //nolint:tagliatelle // go test's field names
			Package string  `json:"Package"` //nolint:tagliatelle // go test's field names
			Test    string  `json:"Test"`    //nolint:tagliatelle // go test's field names
			Elapsed float64 `json:"Elapsed"` //nolint:tagliatelle // go test's field names
		}

		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}

		switch ev.Action {
		case "pass", "fail", "skip":
		default:
			continue
		}

		if ev.Test == "" || strings.Contains(ev.Test, "/") {
			continue
		}

		if out[ev.Package] == nil {
			out[ev.Package] = map[string]float64{}
		}

		out[ev.Package][ev.Test] += ev.Elapsed
	}

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: go test output: %w", ErrFormat, err)
	}

	return out, nil
}

// GoFiles turns one package's test times into test file times, given which
// file declares each test. Tests no file declares (removed since the run)
// are returned by name.
func GoFiles(tests map[string]float64, fileOf map[string]string, param string) (measured map[string]map[string]float64, unknown []string) {
	measured = map[string]map[string]float64{}

	for name, secs := range tests {
		file, ok := fileOf[name]
		if !ok {
			unknown = append(unknown, name)

			continue
		}

		if measured[file] == nil {
			measured[file] = map[string]float64{}
		}

		measured[file][param] += secs
	}

	return measured, unknown
}

// Playwright reads a report of Playwright's JSON reporter: per spec file,
// the milliseconds of every attempt of its tests (retries cost time too),
// as seconds per project. base is the directory, from the repository's
// root, that the report's file names are relative to (its rootDir).
func Playwright(r io.Reader, base string) (map[string]map[string]float64, error) {
	var rep struct {
		Suites []pwSuite `json:"suites"`
	}

	if err := json.NewDecoder(r).Decode(&rep); err != nil {
		return nil, fmt.Errorf("%w: playwright report: %w", ErrFormat, err)
	}

	out := map[string]map[string]float64{}

	var walk func(s pwSuite)

	walk = func(s pwSuite) {
		for _, sp := range s.Specs {
			file := sp.File
			if file == "" {
				file = s.File
			}

			file = path.Join(base, file)

			for _, t := range sp.Tests {
				for _, res := range t.Results {
					if out[file] == nil {
						out[file] = map[string]float64{}
					}

					out[file][t.ProjectName] += res.Duration / 1000
				}
			}
		}

		for _, sub := range s.Suites {
			walk(sub)
		}
	}

	for _, s := range rep.Suites {
		walk(s)
	}

	return out, nil
}

type pwSuite struct {
	File   string    `json:"file"`
	Specs  []pwSpec  `json:"specs"`
	Suites []pwSuite `json:"suites"`
}

type pwSpec struct {
	File  string `json:"file"`
	Tests []struct {
		ProjectName string `json:"projectName"` //nolint:tagliatelle // Playwright's field name
		Results     []struct {
			Duration float64 `json:"duration"`
		} `json:"results"`
	} `json:"tests"`
}
