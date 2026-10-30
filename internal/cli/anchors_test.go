package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
	"github.com/sideways-works/pipelinegen/plugin"
)

func fakeCatalogs(describe func(plugin.Description) plugin.Handler) map[string]plugin.Handler {
	return map[string]plugin.Handler{
		plugin.CallDescribe: describe(plugin.Description{
			Kind: plugin.KindSBOM, Name: "catalogs", Namespaces: []string{"cat"},
			Capabilities: []string{plugin.CallDetect, plugin.CallUnits},
			Candidates:   []string{"cats/**"},
			Inputs:       map[string]string{plugin.CallDetect: plugin.PerFile, plugin.CallUnits: plugin.PerFile},
			Narrowings:   []plugin.Narrowing{{Name: "catalog-keys", OffAt: 3, Note: "a catalog binds the keys looked up"}},
		}),
		plugin.CallDetect: func(in []byte) (any, error) {
			var req plugin.DetectRequest
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, fmt.Errorf("request: %w", err)
			}

			var a plugin.DetectAnswer

			for _, p := range req.Paths {
				if strings.HasSuffix(p, ".md") {
					a.Claims = append(a.Claims, plugin.Claim{Path: p, Role: "catalog", ByUnits: "catalog-keys", Anchor: "cat:" + path.Dir(p)})
				}
			}

			return a, nil
		},
		plugin.CallUnits: func(in []byte) (any, error) {
			var req plugin.TreeRequest
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, fmt.Errorf("request: %w", err)
			}

			var a plugin.UnitsAnswer

			anchors := map[string]bool{}

			for _, c := range req.Claimed {
				data, err := os.ReadFile(filepath.Join(req.Tree, c.Path))
				if err != nil {
					return nil, fmt.Errorf("units: %w", err)
				}

				for _, part := range strings.Split(string(data), "## ")[1:] {
					head, _, _ := strings.Cut(part, "\n")
					sum := sha256.Sum256([]byte(part))
					a.Units = append(a.Units, plugin.Unit{ID: "cat:" + c.Path + "#" + head, File: c.Path, Group: c.Path, Digest: hex.EncodeToString(sum[:8])})
				}

				if dir := path.Dir(c.Path); !anchors[dir] {
					anchors[dir] = true
					a.Units = append(a.Units, plugin.Unit{ID: "cat:" + dir, Group: dir, Digest: "anchor"})
				}
			}

			return a, nil
		},
	}
}

func anchorsRepo(t *testing.T) *testrepo.Repo {
	t.Helper()

	r := testrepo.New(t)
	r.Write("cats/BUILD.bazel", "filegroup(name = \"cats\", srcs = [\"web/en.md\", \"mail/en.md\"], visibility = [\"//visibility:public\"])\n")
	r.Write("cats/web/en.md", "## Start\nGo\n## Stop\nHalt\n")
	r.Write("cats/mail/en.md", "## Thanks\nMerci\n")
	r.Write("texts/BUILD.bazel", "filegroup(name = \"texts\", srcs = glob([\"*.txt\"]))\n")
	r.Write("texts/a.txt", "a\n")
	r.Write("texts/z.txt", "z\n")

	var specs strings.Builder

	for _, s := range []string{"a", "b", "c", "d", "e"} {
		fmt.Fprintf(&specs, "filegroup(name = %q, srcs = [%q])\n", s, s+".spec.ts")
		r.Write("browser/"+s+".spec.ts", "test('"+s+"')\n")
	}

	r.Write("browser/e.spec.ts", "// pipelinegen:data //cats\ntest('e')\n")

	r.Write("browser/BUILD.bazel", specs.String())
	r.Commit("a browser suite and its catalogs")

	return r
}

func anchorsConfig(t *testing.T, r *testrepo.Repo) *config.Config {
	t.Helper()

	cfg := byUnitsConfig(t, r)
	browser := cfg.Suites["browser"]
	browser.ExtraDeps = []config.ExtraDeps{{Tests: []string{"browser/*.spec.ts"}, Deps: []string{"//cats"}}}
	browser.TextInputs = []config.TextInputs{{Tests: []string{"browser/d.spec.ts"}, Paths: []string{"cats/web/*"}}}
	cfg.Suites["browser"] = browser
	cfg.Plugins[1] = config.Plugin{Name: "catalogs", Kind: config.KindSBOM, Repository: "o/catalogs", Release: cfg.Plugins[1].Release}

	return cfg
}

func TestAnAnchorProvesARecordingCoversItsCatalog(t *testing.T) {
	testrepo.Parallel(t)

	r := anchorsRepo(t)
	cfg := anchorsConfig(t, r)

	lists := recording.Lists{
		"browser/browser/a.spec.ts": {"t:a", "cat:cats/web", "cat:cats/web/en.md#Start"},
		"browser/browser/b.spec.ts": {"t:a", "cat:cats/web", "cat:cats/web/en.md#Stop"},
		"browser/browser/c.spec.ts": {"t:a"},
		"browser/browser/d.spec.ts": {"t:a", "cat:cats/web"},
		"browser/browser/e.spec.ts": {"t:a", "cat:cats/web"},
	}

	base := r.Git("rev-parse", "HEAD")
	r.Write("cats/web/en.md", "## Start\nBegin\n## Stop\nHalt\n")
	r.Write("texts/z.txt", "zz\n")
	head := r.Commit("the web catalog's start")

	p := planByUnits(t, r, cfg, lists, base, head)
	t.Logf("web: %s", summary(p))

	if got := browserNames(p); got != "browser/a.spec.ts,browser/c.spec.ts,browser/d.spec.ts,browser/e.spec.ts" {
		t.Errorf("web catalog selects %q", got)
	}

	if p.LabelGroups != 1 {
		t.Errorf("label groups %d, want one: the specs holding the anchor", p.LabelGroups)
	}

	var left []affected.Narrowed

	for _, sp := range p.Suites {
		if sp.Suite == "browser" {
			left = sp.Narrowed
		}
	}

	if len(left) != 1 || left[0].Narrowing != "catalog-keys" || strings.Join(left[0].Dropped, ",") != "browser/b.spec.ts" ||
		!strings.Contains(left[0].Reason, "its recording covers cats/web/en.md (cat:cats/web, by catalogs) and touched none of the changed units") {
		t.Errorf("narrowed %+v", left)
	}

	base = head

	r.Write("cats/mail/en.md", "## Thanks\nThank you\n")
	head = r.Commit("the mail catalog")

	p = planByUnits(t, r, cfg, lists, base, head)
	t.Logf("mail: %s", summary(p))

	if got := browserNames(p); got != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts,browser/d.spec.ts,browser/e.spec.ts" {
		t.Errorf("mail catalog selects %q, want every spec: no list holds its anchor", got)
	}
}
