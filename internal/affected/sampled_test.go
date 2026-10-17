package affected

import (
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
)

func TestAChangedEntryAndASampleOnFileRunOneSample(t *testing.T) {
	all := []bazel.Slice{}
	for _, name := range []string{"a", "b", "c", "d"} {
		all = append(all, bazel.Slice{Name: name, Label: bazel.SelectorLabel(name)})
	}

	changes := []gitx.Change{{Path: config.FileName}}
	on := &sampleOn{entry: config.FileName, what: config.FileName, only: true}

	for _, c := range []struct {
		name     string
		entry    bool
		reached  []string
		selected []string
		records  []string
	}{
		{"the entry and the sample_on file", true, nil, []string{"a"}, nil},
		{"the sample_on file alone", false, nil, nil, []string{"sampled sample_on"}},
		{"the sample_on file, with plugin-entry off", false, []string{"c", "d"}, []string{"c", "d"}, []string{"sampled sample_on"}},
	} {
		impacted := map[string]bool{all[0].Label: true}
		if c.entry {
			impacted[bazel.SelectorLabel(EntryTarget("s"))] = true
		}

		for _, name := range c.reached {
			impacted[bazel.SelectorLabel(name)] = true
		}

		reached := entrySide{reached: func(sl bazel.Slice) bool { return slices.Contains(c.reached, sl.Name) }}

		sp := selectSuite("s", config.Suite{}, all, impacted, true)
		sampleSuite(&sp, config.Suite{}, "s", all, impacted, &SampleRule{On: true, N: 1}, "0123456789abcdef", on, changes, reached)

		var selected, records []string

		for _, sl := range sp.Selected {
			selected = append(selected, sl.Name)
		}

		for _, n := range sp.Narrowed {
			records = append(records, n.Kind+" "+n.Narrowing)
		}

		if c.selected == nil && len(selected) != 1 || c.selected != nil && !slices.Equal(selected, c.selected) || !slices.Equal(records, c.records) {
			t.Errorf("%s: runs %v with records %v, want %v and %v", c.name, selected, records, c.selected, c.records)
		}
	}
}

func TestAnEntrysSampleIsDrawnFromTheSlicesItReached(t *testing.T) {
	var all []bazel.Slice
	for _, name := range []string{"new1", "new2", "new3", "rec1", "rec2", "rec3", "rec4"} {
		all = append(all, bazel.Slice{Name: name, Label: bazel.SelectorLabel(name)})
	}

	impacted := map[string]bool{bazel.SelectorLabel(EntryTarget("s")): true}
	for _, name := range []string{"new1", "new2", "new3"} {
		impacted[bazel.SelectorLabel(name)] = true
	}

	pool := map[string]bool{"rec1": true, "rec2": true, "rec3": true, "rec4": true}
	rule := &SampleRule{N: 2, Group: func(string) string { return "e2e" }}
	entry := entrySide{reached: func(bazel.Slice) bool { return false }, pool: pool}

	sp := selectSuite("s", config.Suite{}, all, impacted, true)
	sampleSuite(&sp, config.Suite{}, "s", all, impacted, rule, "0123456789abcdef", nil, nil, entry)

	var own, sampled int

	for _, sl := range sp.Selected {
		if pool[sl.Name] {
			sampled++
		} else {
			own++
		}
	}

	if own != 3 || sampled != 2 {
		t.Errorf("ran %d slices of their own and %d the entry reached, want 3 and 2: %v", own, sampled, sp.Selected)
	}

	impacted[bazel.SelectorLabel("rec1")] = true
	sp = selectSuite("s", config.Suite{}, all, impacted, true)
	sampleSuite(&sp, config.Suite{}, "s", all, impacted, rule, "0123456789abcdef", nil, nil, entry)

	if len(sp.Selected) != 5 {
		t.Errorf("rec1 ran anyway and should count toward the sample of 2: %v", sp.Selected)
	}
}
