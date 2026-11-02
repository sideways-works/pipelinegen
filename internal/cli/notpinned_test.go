package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

func TestAPluginNotPinnedIsSaidOnce(t *testing.T) {
	dir := pluginsDirFor(t)
	sha, sums := strings.Repeat("a", 64), strings.Repeat("b", 64)

	cfg := &config.Config{Plugins: []config.Plugin{
		{Name: "texts", Kind: config.KindSBOM, Repository: "o/texts", Release: &config.Release{Version: "v1.2.3", SHA256: sha}},
		{Name: "arrows", Kind: config.KindCorrelator, Repository: "o/arrows", Release: &config.Release{Version: "v0.4.0", Sums: sums}},
	}}

	var out bytes.Buffer

	sayer := &notPinnedSayer{w: &out}

	for range 2 {
		set, err := plugins.Load(t.Context(), plugins.Options{Config: cfg, Dir: dir, Cache: t.TempDir(), Work: t.TempDir(), Answers: &plugincache.Cache{Dir: t.TempDir()}})
		if err != nil {
			t.Fatal(err)
		}

		sayer.say(set)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("said %d lines, want one per plugin:\n%s", len(lines), out.String())
	}

	for _, c := range []struct{ plugin, said string }{
		{"texts", "pipelinegen: plugin texts is not the build its pin names (v1.2.3, sha256 " + sha + "): its binary's sha256 is "},
		{"arrows", "pipelinegen: plugin arrows can't be checked against its pin (v0.4.0, sums " + sums + "): " + plugins.ErrNoSums.Error() + "; its binary's sha256 is "},
	} {
		found, err := plugins.FileSHA256(filepath.Join(dir, c.plugin))
		if err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(out.String(), c.said+found+": it answers, and its answers are cached apart\n") {
			t.Errorf("%s: no line naming its pin and %s in\n%s", c.plugin, found, out.String())
		}
	}
}
