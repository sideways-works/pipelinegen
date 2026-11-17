package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/check"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/driver"
	"github.com/sideways-works/pipelinegen/plugin"
)

type validatedStep struct {
	suite string
	index int
	step  config.Step
}

// A pinned driver's answer is kept by its pin and the request, so an unchanged config costs no call; one
// that can't be called leaves its steps checked against its describe's settings.
func validatedFindings(ctx context.Context, cfg *config.Config, by map[string][]validatedStep, descs map[string]plugin.Description, dir, cache string) []check.Finding {
	var out []check.Finding

	drivers := make([]string, 0, len(by))
	for name := range by {
		drivers = append(drivers, name)
	}

	slices.Sort(drivers)

	for _, name := range drivers {
		steps := by[name]

		req := make([]plugin.ValidateStep, 0, len(steps))
		for _, s := range steps {
			req = append(req, plugin.ValidateStep{Verb: s.step.Kind(), Settings: cfg.DriverConfig(s.step)})
		}

		results, err := validateSteps(ctx, cfg, name, req, dir, cache)
		if err != nil {
			var where []string

			for _, s := range steps {
				where = append(where, fmt.Sprintf("suites.%s.steps[%d]", s.suite, s.index))

				for _, problem := range settingsProblems(descs[name], cfg.DriverConfig(s.step)) {
					out = append(out, settingsFinding(fmt.Sprintf("suite %s: steps[%d] (%s %s): %s", s.suite, s.index, s.step.Kind(), name, problem)))
				}
			}

			out = append(out, check.Finding{
				Kind: check.KindUndescribedDriver, Path: config.FileName, Warning: true,
				Problem: fmt.Sprintf("couldn't ask the driver %s to check its steps' settings (%v), so only their top-level keys were checked (%s)",
					name, err, strings.Join(where, "; ")),
				Fix: askFix(name, dir, err),
			})

			continue
		}

		for i, s := range steps {
			for _, p := range results[i].Problems {
				out = append(out, settingsFinding(placeOf(cfg, s, p.Place)+": "+p.Problem))
			}
		}
	}

	return out
}

func settingsFinding(problem string) check.Finding {
	return check.Finding{
		Kind: check.KindBadConfig, Path: config.FileName, Problem: problem,
		Fix: "change the setting as the driver says: a step runs only with settings its driver takes",
	}
}

func placeOf(cfg *config.Config, s validatedStep, place string) string {
	step := fmt.Sprintf("suites.%s.steps[%d].with", s.suite, s.index)
	if place == "" {
		return step
	}

	top, _, _ := strings.Cut(place, ".")
	top, _, _ = strings.Cut(top, "[")

	if _, ok := s.step.With[top]; !ok {
		for j, p := range cfg.Plugins {
			if p.Kind == config.KindDriver && p.Name == s.step.Driver() {
				if _, ok := p.Config[top]; ok {
					return fmt.Sprintf("plugins[%d].config.%s (for suites.%s.steps[%d])", j, place, s.suite, s.index)
				}
			}
		}
	}

	return step + "." + place
}

func validateSteps(ctx context.Context, cfg *config.Config, name string, steps []plugin.ValidateStep, dir, cache string) ([]plugin.ValidateResult, error) {
	if name == config.BuiltinCommand {
		out := make([]plugin.ValidateResult, 0, len(steps))
		for _, s := range steps {
			out = append(out, plugin.ValidateResult{Problems: plugin.SettingProblems(driver.ValidateCommand(s.Verb, s.Settings))})
		}

		return out, nil
	}

	p, ok := cfg.Driver(name)
	if !ok {
		return nil, fmt.Errorf("%w: no driver %s", errSteps, name)
	}

	return pinnedValidate(ctx, p, steps, dir, cache)
}

func pinnedValidate(ctx context.Context, p config.Plugin, steps []plugin.ValidateStep, dir, cache string) ([]plugin.ValidateResult, error) {
	if p.Release == nil || p.Release.Pin() == "" {
		return nil, unpinned(p, dir)
	}

	data, err := json.Marshal(struct {
		Config map[string]any        `json:"config"`
		Steps  []plugin.ValidateStep `json:"steps"`
	}{p.Config, steps})
	if err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}

	sum := sha256.Sum256(data)
	kept := filepath.Join(cache, "validate", strings.ToLower(p.Release.Pin())+"-"+hex.EncodeToString(sum[:12])+".json")

	var results []plugin.ValidateResult
	if data, err := os.ReadFile(kept); err == nil && json.Unmarshal(data, &results) == nil && len(results) == len(steps) { //nolint:gosec // pipelinegen's own cache
		return results, nil
	}

	exe, err := pinnedOrFetched(ctx, p, dir)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(cache, 0o750); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}

	work, err := os.MkdirTemp(cache, "validate-")
	if err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}

	defer os.RemoveAll(work) //nolint:errcheck // a scratch directory

	results, err = (plugin.Plugin{Name: p.Name, Path: exe, Config: p.Config}).Validate(ctx, work, steps)
	if err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}

	if data, err := json.Marshal(results); err == nil && os.MkdirAll(filepath.Dir(kept), 0o750) == nil {
		_ = os.WriteFile(kept, data, 0o600) //nolint:errcheck // a cache: the next check asks again
	}

	return results, nil
}
