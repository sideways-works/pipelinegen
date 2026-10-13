package plugins

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestMissingNeedsAreProgramsNotOnPath(t *testing.T) {
	dir := t.TempDir()
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(dir, "tool"), []byte("#!/bin/sh\n")); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "plain"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	env := []string{"HOME=/nowhere", "PATH=" + t.TempDir() + string(filepath.ListSeparator) + dir}

	got := MissingNeeds([]string{"tool>=1.27", "tool", "plain", "absent~2", "gone"}, env)
	if !slices.Equal(got, []string{"plain", "absent~2", "gone"}) {
		t.Errorf("missing %v", got)
	}
}

func TestAPlanWarnsOfANeedThePathLacks(t *testing.T) {
	s := newSetup(t, "links")

	needs := []string{"sh", "pipelinegen-test-no-such-tool>=1.2"}
	described := map[string]plugin.Description{
		"texts": {Protocol: plugin.Protocol, Kind: plugin.KindSBOM, Name: "texts", Needs: needs},
		"uses":  {Protocol: plugin.Protocol, Kind: plugin.KindCorrelator, Name: "uses", Needs: []string{"sh"}},
	}

	set, err := Load(t.Context(), Options{Config: s.cfg, Dir: s.dir, Cache: t.TempDir(), Answers: s.cache, Work: t.TempDir(), Described: described})
	if err != nil {
		t.Fatal(err)
	}

	var said []string

	for _, w := range set.Report().Warnings {
		if strings.Contains(w, "PATH") {
			said = append(said, w)
		}
	}

	if len(said) != 1 || !strings.HasPrefix(said[0], "plugin texts needs pipelinegen-test-no-such-tool>=1.2, which this machine's PATH lacks") || strings.Contains(said[0], "sh,") {
		t.Errorf("warnings %q", said)
	}
}

func TestPlanningNeverAsksADriver(t *testing.T) {
	s := newSetup(t, "links")
	s.cfg.Plugins = append(s.cfg.Plugins, config.Plugin{Name: "shell", Kind: config.KindDriver, Repository: "example/shell"})

	set, err := Load(t.Context(), Options{Config: s.cfg, Dir: s.dir, Cache: t.TempDir(), Answers: s.cache, Work: t.TempDir()})
	if err != nil {
		t.Fatalf("a plan with a driver it can't run: %v", err)
	}

	for _, l := range set.Plugins {
		if l.Conf.Kind == config.KindDriver {
			t.Errorf("the plan loaded the driver %s", l.Conf.Name)
		}
	}
}
