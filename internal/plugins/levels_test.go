package plugins

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/plugin"
)

func loadWithLevels(t *testing.T, levels map[string]config.NarrowingLevel) (*Set, error) {
	t.Helper()

	desc := plugin.Description{Kind: plugin.KindSBOM, Narrowings: []plugin.Narrowing{{Name: "document-units", OffAt: 2, Note: "per operation"}}}

	return Load(t.Context(), Options{
		Config: &config.Config{
			Plugins:    []config.Plugin{{Name: "openapi-sbom", Kind: config.KindSBOM}},
			Narrowings: levels,
		},
		Dir: t.TempDir(), Cache: t.TempDir(), Work: t.TempDir(),
		Described: map[string]plugin.Description{"openapi-sbom": desc},
	})
}

func TestTheConfigSetsANarrowingsLevel(t *testing.T) {
	s, err := loadWithLevels(t, map[string]config.NarrowingLevel{
		"document-units": {OffAt: 3, Note: "every contract's readers declared"},
		NarrowSample:     {OffAt: 1, Note: "suites whose slices differ"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if s.OffAt("document-units") != 3 || !s.On("document-units", 2) || s.On("document-units", 3) {
		t.Errorf("document-units: off from %d", s.OffAt("document-units"))
	}

	if s.OffAt(NarrowSample) != 1 || s.On(NarrowSample, 1) || slices.Contains(s.InForce(1), NarrowSample) {
		t.Errorf("sample: off from %d, in force at 1: %v", s.OffAt(NarrowSample), s.InForce(1))
	}

	want := []Override{
		{Name: "document-units", OffAt: 3, Declared: 2, By: "plugin openapi-sbom", Note: "every contract's readers declared"},
		{Name: NarrowSample, OffAt: 1, Declared: 3, By: ByCore, Note: "suites whose slices differ"},
	}
	if got := s.Overrides(); !slices.Equal(got, want) {
		t.Errorf("overrides %+v", got)
	}

	plain, err := loadWithLevels(t, nil)
	if err != nil || plain.OffAt("document-units") != 2 || len(plain.Overrides()) != 0 {
		t.Fatalf("without levels: %v %d %v", err, plain.OffAt("document-units"), plain.Overrides())
	}
}

func TestALevelForANarrowingNobodyDeclaresIsRefused(t *testing.T) {
	_, err := loadWithLevels(t, map[string]config.NarrowingLevel{"document-unit": {OffAt: 3, Note: "a typo"}})
	if !errors.Is(err, ErrPlugins) || !strings.Contains(err.Error(), "narrowings.document-unit: no configured plugin declares the narrowing") ||
		!strings.Contains(err.Error(), "document-units") {
		t.Fatalf("an unknown narrowing: %v", err)
	}
}
