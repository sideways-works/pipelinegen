package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sideways-works/pipelinegen/internal/check"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/spec"
)

// runnerFindings asks each runner for its pipeline when the config is checked, so what one refuses
// shows before the pipeline job sets it.
func runnerFindings(ctx context.Context, cfg *config.Config, root, pluginsDir string) []check.Finding {
	if len(cfg.Runs) == 0 {
		return nil
	}

	var out []check.Finding

	for _, conf := range cfg.Runners() {
		out = append(out, runnerFindingsOf(ctx, cfg, conf, root, pluginsDir)...)
	}

	return out
}

func runnerFindingsOf(ctx context.Context, cfg *config.Config, conf config.Plugin, root, pluginsDir string) []check.Finding {
	if _, err := os.Stat(filepath.Join(pluginsDir, conf.Name)); err != nil {
		return nil
	}

	// only the pipeline links to Concourse: a stand-in address lets the
	// runner be asked before the real one is known.
	c := *cfg
	if c.Concourse.URL == "" {
		c.Concourse.URL = varsOnlyURL
	}

	_, notices, err := pipelineText(ctx, &c, root, conf, "", pluginsDir)
	if err == nil {
		out := make([]check.Finding, 0, len(notices))

		for _, n := range notices {
			out = append(out, check.Finding{
				Kind: check.KindRunnerNotice, Path: config.FileName, Warning: true,
				Problem: fmt.Sprintf("runner %s: %s", conf.Name, n),
				Fix:     "nothing, if that is what you mean; otherwise change what it names in the runner's config",
			})
		}

		return out
	}

	if errors.Is(err, spec.ErrSpec) {
		return nil
	}

	return []check.Finding{{
		Kind: check.KindBadConfig, Path: config.FileName,
		Problem: fmt.Sprintf("runner %s refuses the pipeline: %v", conf.Name, err),
		Fix:     "fix what it names; `pipelinegen pipeline` asks the same runner",
	}}
}
