package config

import (
	"slices"
	"strings"
	"testing"
)

func meaningOf(t *testing.T, base, head []string) *Meaning {
	t.Helper()

	parse := func(edits []string) *Config {
		text := withSteps
		for i := 0; i+1 < len(edits); i += 2 {
			if !strings.Contains(text, edits[i]) {
				t.Fatalf("the fixture has no %q", edits[i])
			}

			text = strings.Replace(text, edits[i], edits[i+1], 1)
		}

		c, err := Parse([]byte(text))
		if err != nil {
			t.Fatal(err)
		}

		return c
	}

	return MeaningOf(parse(base), parse(head), "")
}

func runs(m *Meaning, suite string, names ...string) []string {
	var out []string

	for _, n := range names {
		sl := RoutedSlice{Name: n}
		if strings.HasPrefix(n, "api/") {
			sl.Label = "//api:c"
		}

		if _, ok := m.SliceRuns(suite, sl); ok {
			out = append(out, n)
		}
	}

	return out
}

func TestAConfigsLayoutMeansNothing(t *testing.T) {
	m := meaningOf(t, nil, []string{
		"pipeline: shop\n", "# the shop's pipeline\npipeline: shop\n",
		"    image: golang\n    params: {GOFLAGS: -trimpath}\n", "    params:\n      GOFLAGS: -trimpath\n    image: golang\n",
	})

	if m.Changed() || !m.Understood() {
		t.Fatalf("parts %+v", m.Parts)
	}
}

func TestARunStepsChangeRunsTheSlicesItTakes(t *testing.T) {
	slicesOf := []string{"web/a.sh", "web/b.sh", "api/c.sh"}

	m := meaningOf(t, nil, []string{"with: {each: [sh, \"{source}\"]}", "with: {each: [bash, \"{source}\"]}"})
	if got := runs(m, "smoke", slicesOf...); !slices.Equal(got, []string{"web/a.sh", "web/b.sh"}) || !m.Understood() {
		t.Errorf("a run step's with: %v, parts %+v", got, m.Parts)
	}

	why, _ := m.SliceRuns("smoke", RoutedSlice{Name: "web/a.sh"})
	if why != "suites.smoke.steps[2] (run command), which takes it, changed" {
		t.Errorf("why %q", why)
	}

	m = meaningOf(t, nil, []string{"take: [\"web/**\"]", "take: [\"web/a.sh\"]"})
	if got := runs(m, "smoke", slicesOf...); !slices.Equal(got, []string{"web/b.sh"}) {
		t.Errorf("a take that drops a slice: %v", got)
	}

	m = meaningOf(t, nil, []string{"with: {start: [server]}", "with: {start: [server, --quiet]}"})
	if got := runs(m, "smoke", slicesOf...); len(got) != 3 {
		t.Errorf("a set-up step: %v", got)
	}

	m = meaningOf(t, nil, []string{"{prepare: command, image: golang,", "{prepare: command, image: golang-next,"})
	if got := runs(m, "smoke", slicesOf...); len(got) != 3 {
		t.Errorf("a prepare step's image: %v", got)
	}

	m = meaningOf(t, nil, []string{"params: {GOFLAGS: -trimpath}", "params: {GOFLAGS: -mod=mod}"})
	if got := runs(m, "go", "a_test.go"); len(got) != 1 || len(runs(m, "smoke", slicesOf...)) != 0 {
		t.Errorf("a suite's params run its own slices only: %v", got)
	}
}

func TestADriversEntry(t *testing.T) {
	slicesOf := []RoutedSlice{{Name: "web/a.sh"}, {Name: "api/c.sh", Label: "//api:c"}}

	m := meaningOf(t, nil, []string{"repository: o/bazel-driver, release: {version: v1,", "repository: o/bazel-driver, release: {version: v2,"})
	if !m.Understood() || len(runs(m, "smoke", "web/a.sh", "api/c.sh")) != 0 {
		t.Fatalf("a pin runs no slice by itself: %+v", m.Parts)
	}

	var reached []string

	for _, sl := range slicesOf {
		if ds := m.Drivers("smoke", sl); len(ds) > 0 {
			reached = append(reached, sl.Name+":"+strings.Join(ds, ","))
		}
	}

	if !slices.Equal(reached, []string{"api/c.sh:bazel"}) {
		t.Errorf("the bazel driver reaches %v", reached)
	}

	m = meaningOf(t, nil, []string{"config: {timeout: 60m}", "config: {timeout: 90m}"})
	if len(runs(m, "go", "a_test.go")) != 1 || len(m.Drivers("go", RoutedSlice{Name: "a_test.go"})) != 0 {
		t.Errorf("the config block runs the steps it sets: %+v", m.Parts)
	}

	m = meaningOf(t, nil, []string{"config: {timeout: 60m}}", "}", "with: {tags: [cover]}", "with: {tags: [cover], timeout: 60m}"})
	if len(runs(m, "go", "a_test.go")) != 0 {
		t.Errorf("a setting moved into the step: %+v", m.Parts)
	}
}

func TestWhatThePlannerDoesntUnderstand(t *testing.T) {
	for _, c := range []struct {
		edit       []string
		understood bool
	}{
		{[]string{"pipeline: shop\n", "pipeline: shop\nnarrowing: 4\n"}, true},
		{[]string{"pipeline: shop\n", "pipeline: shop\nkeep: {runs: {last: 3}}\n"}, true},
		{[]string{"    image: golang\n", "    image: golang\n    parallel: 8\n"}, true},
		{[]string{"    image: golang\n", "    image: golang\n    minutes: 30\n"}, false},
		{[]string{"pipeline: shop\n", "pipeline: shop\nbazel: {startup_flags: [--host_jvm_args=-Xmx2g]}\n"}, false},
	} {
		m := meaningOf(t, nil, c.edit)
		if !m.Changed() && c.understood {
			continue
		}

		if m.Understood() != c.understood {
			t.Errorf("%q: understood %v, parts %+v", c.edit[1], m.Understood(), m.Parts)
		}
	}
}

func TestACollectLimitIsAChangedFile(t *testing.T) {
	entry := "  - {name: sql, kind: correlator, repository: o/sql, release: {version: v1, sha256: " + sha + "}"

	m := meaningOf(t, []string{"plugins:\n", "plugins:\n" + entry + "}\n"},
		[]string{"plugins:\n", "plugins:\n" + entry + ", collect_seconds: 30}\n"})
	if !m.Changed() || m.Understood() {
		t.Errorf("changed %v, understood %v, parts %+v", m.Changed(), m.Understood(), m.Parts)
	}
}

func TestAProposedChangesTrustedStepsAreNoDifference(t *testing.T) {
	base, err := Parse([]byte(withSteps))
	if err != nil {
		t.Fatal(err)
	}

	edited := strings.Replace(withSteps, "with: {each: [sh, \"{source}\"]}", "with: {each: [bash, \"{source}\"]}", 1)
	edited = strings.Replace(edited, "params: {GOFLAGS: -trimpath}", "params: {GOFLAGS: -mod=mod}", 1)

	head, err := Parse([]byte(edited))
	if err != nil {
		t.Fatal(err)
	}

	head.WithTrusted(base.Trusted())

	m := MeaningOf(base, head, "")
	if len(runs(m, "smoke", "web/a.sh")) != 0 || len(runs(m, "go", "a_test.go")) != 1 {
		t.Errorf("parts %+v", m.Parts)
	}
}
