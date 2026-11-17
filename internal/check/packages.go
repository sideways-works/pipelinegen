package check

import (
	"fmt"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/packages"
)

// packageModes says, for every suite that declares packages, how its steps
// get them on CI: a cached image, or an install in each task. A suite's
// task files run the repository's own scripts, which no install goes in
// front of: without a cached image they run without the packages, a
// warning.
func packageModes(cfg *config.Config, tree Tree) []Finding {
	if !cfg.HasPackages() {
		return nil
	}

	var out []Finding

	if ps := cfg.Concourse.Planner.Packages; len(ps) > 0 {
		out = append(out, Finding{
			Kind: KindPackages, Path: config.FileName, Note: true,
			Problem: fmt.Sprintf("concourse.planner.packages (%s) are installed by each of pipelinegen's own steps, with a download cache", strings.Join(packages.Names(ps), ", ")),
			Fix:     "nothing to fix: the planner image is never swapped for a cached one, since those steps hold the tokens that write",
		})
	}

	var bases map[string]packages.Base

	if f := cfg.Concourse.Include; f != "" {
		if data, err := tree.Read(f); err == nil {
			bases, _ = packages.Bases(data)
		}
	}

	for _, u := range packages.Uses(cfg, bases) {
		f := Finding{Kind: KindPackages, Path: config.FileName, Problem: u.Describe(cfg)}

		switch {
		case u.Key != "":
			f.Note, f.Fix = true, "nothing to fix: the pipeline builds it when the registry hasn't got it, and the steps install nothing"
		case packages.TaskFiles(cfg.Suites[u.Suite]):
			f.Warning = true
			f.Problem = fmt.Sprintf("suite %s runs task files (prepare, run_slice or whole_suite), whose scripts the runner can't put an install in front of, "+
				"and has no cached image: %s; on CI those tasks run without %s", u.Suite, u.Why, strings.Join(packages.Names(cfg.PackagesFor(u.Suite)), ", "))
			f.Fix = "set images.registry and name a registry-image resource pinned by digest in suites." + u.Suite + ".image, or install them in the task's image"
		default:
			f.Note, f.Fix = true, "nothing to fix; a cached image installs them once: images.registry, and the image pinned by digest"
		}

		out = append(out, f)
	}

	return out
}
