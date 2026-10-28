package cli

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/check"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/driver"
	"github.com/sideways-works/pipelinegen/plugin"
)

// A driver the check can't ask is left to the batch, which asks it before anything runs.
func stepsFindings(ctx context.Context, cfg *config.Config, root, dir, cache string) []check.Finding {
	var out []check.Finding

	bad := func(problem, fix string) {
		out = append(out, check.Finding{Kind: check.KindBadConfig, Path: config.FileName, Problem: problem, Fix: fix})
	}

	descs := map[string]plugin.Description{config.BuiltinCommand: driver.CommandDescription()}
	unasked := map[string]error{}

	for _, p := range cfg.Plugins {
		if p.Kind != config.KindDriver {
			continue
		}

		d, err := pinnedDescribe(ctx, p, dir, cache)
		if err != nil {
			unasked[p.Name] = err

			continue
		}

		descs[p.Name] = d
	}

	// Say once per driver that its steps go unchecked, rather than pass them quietly.
	unchecked := map[string][]string{}
	validated := map[string][]validatedStep{}

	for _, name := range cfg.SuiteNames() {
		s := cfg.Suites[name]
		if !s.HasSteps() {
			continue
		}

		for i, st := range s.Steps {
			d, ok := descs[st.Driver()]
			if !ok {
				if _, why := unasked[st.Driver()]; why {
					unchecked[st.Driver()] = append(unchecked[st.Driver()], fmt.Sprintf("suite %s: steps[%d] (%s)", name, i, st.Kind()))
				}

				continue
			}

			where := fmt.Sprintf("suite %s: steps[%d] (%s %s)", name, i, st.Kind(), st.Driver())

			if d.Kind != plugin.KindDriver || !d.HasVerb(st.Kind()) {
				bad(where+": the driver has no "+st.Kind()+" verb (its verbs: "+strings.Join(d.Verbs, ", ")+")",
					"use a driver that "+st.Kind()+"s, or a "+config.BuiltinCommand+" step")
			}

			// a driver that validates checks every depth of its settings
			// itself, with the rules it runs by.
			if d.Validates {
				validated[st.Driver()] = append(validated[st.Driver()], validatedStep{suite: name, index: i, step: st})

				continue
			}

			for _, problem := range settingsProblems(d, cfg.DriverConfig(st)) {
				bad(where+": "+problem, "write only the settings the driver names: "+strings.Join(slices.Sorted(maps.Keys(d.Settings)), ", "))
			}
		}

		out = append(out, routingFindings(ctx, cfg, root, name, descs)...)
		out = append(out, formsFindings(ctx, cfg, name, descs, dir, cache)...)
	}

	out = append(out, validatedFindings(ctx, cfg, validated, descs, dir, cache)...)

	for _, drv := range slices.Sorted(maps.Keys(unchecked)) {
		out = append(out, check.Finding{
			Kind: check.KindUndescribedDriver, Path: config.FileName, Warning: true,
			Problem: fmt.Sprintf("couldn't ask the driver %s what it is (%v), so its steps went unchecked: their verbs, settings and routes (%s)",
				drv, unasked[drv], strings.Join(unchecked[drv], "; ")),
			Fix: "put the pinned " + drv + " into " + dir + " (or name its directory with --plugins) so the check can ask it",
		})
	}

	return out
}

func settingsProblems(d plugin.Description, conf map[string]any) []string {
	var out []string

	for _, k := range slices.Sorted(maps.Keys(conf)) {
		want, ok := d.Settings[k]
		if !ok {
			out = append(out, fmt.Sprintf("setting %s is not one %s takes", k, d.Name))

			continue
		}

		if !settingIs(want, conf[k]) {
			out = append(out, fmt.Sprintf("setting %s is a %s for %s, not %v", k, want, d.Name, conf[k]))
		}
	}

	return out
}

func settingIs(want string, v any) bool {
	switch want {
	case plugin.SettingBool:
		_, ok := v.(bool)

		return ok
	case plugin.SettingInt:
		_, ok := v.(int)

		return ok
	case plugin.SettingDuration:
		s, ok := v.(string)
		if !ok {
			return false
		}

		_, err := time.ParseDuration(s)

		return err == nil
	case plugin.SettingList:
		_, ok := v.([]any)

		return ok
	case plugin.SettingMap:
		_, ok := v.(map[string]any)

		return ok
	default:
		_, ok := v.(string)

		return ok
	}
}

func routingFindings(ctx context.Context, cfg *config.Config, root, suite string, descs map[string]plugin.Description) []check.Finding {
	all, err := recordSlices(ctx, cfg, root, suite, nil)
	if err != nil {
		// a suite that lists no slice is the check's other findings' to say.
		return nil
	}

	s := cfg.Suites[suite]

	var none, many, outside []string

	for _, sl := range fromBazel(all) {
		rs := config.RoutedSlice{Name: sl.Name, Label: sl.Label, Kind: sl.Kind}

		var takers []int

		for _, i := range s.StepsOf(config.StepRun) {
			if s.Steps[i].Takes(rs) {
				takers = append(takers, i)
			}
		}

		switch len(takers) {
		case 0:
			none = append(none, sl.Name)
		case 1:
			if d, ok := descs[s.Steps[takers[0]].Driver()]; ok && !routable(d, rs) {
				outside = append(outside, sl.Name)
			}
		default:
			many = append(many, sl.Name)
		}
	}

	var out []check.Finding

	add := func(names []string, problem, fix string) {
		if len(names) == 0 {
			return
		}

		out = append(out, check.Finding{
			Kind: check.KindBadConfig, Path: config.FileName,
			Problem: fmt.Sprintf("suite %s: %s %s: %s", suite, plural(len(names), "slice"), problem, strings.Join(firstFew(names, 10), ", ")),
			Fix:     fix,
		})
	}

	add(none, "no run step takes", "give a run step a take that matches them, or leave take out on the suite's only run step")
	add(many, "more than one run step takes", "make the run steps' takes exclusive: every slice is run by one step")
	add(outside, "go to a driver whose routes don't take them", "route them to a driver that runs them")

	return out
}

// A driver without routes takes any slice.
func routable(d plugin.Description, sl config.RoutedSlice) bool {
	r := d.Routes
	if r == nil {
		return true
	}

	if sl.Kind != "" {
		return slices.Contains(r.Kinds, "*") || slices.Contains(r.Kinds, sl.Kind)
	}

	return slices.ContainsFunc(r.Files, func(p string) bool { return config.MatchGlob(p, sl.Name) })
}

// A correlator that names no form yet leaves it to the recording: a note, not a failure.
func formsFindings(ctx context.Context, cfg *config.Config, suite string, descs map[string]plugin.Description, dir, cache string) []check.Finding {
	s := cfg.Suites[suite]
	if s.Bind != config.BindRecorded {
		return nil
	}

	var reads []string

	for _, name := range cfg.CorrelatorsFor(suite) {
		i := slices.IndexFunc(cfg.Plugins, func(p config.Plugin) bool { return p.Name == name })
		if i < 0 {
			continue
		}

		d, err := pinnedDescribe(ctx, cfg.Plugins[i], dir, cache)
		if err != nil || len(d.ObservesForms) == 0 {
			// one that can't say leaves the question open.
			return nil
		}

		reads = append(reads, d.ObservesForms...)
	}

	var out []check.Finding

	for _, i := range s.StepsOf(config.StepRun) {
		d, ok := descs[s.Steps[i].Driver()]
		if !ok || len(d.RecordingForms) == 0 {
			continue
		}

		if !slices.ContainsFunc(d.RecordingForms, func(f string) bool { return slices.Contains(reads, f) }) {
			out = append(out, check.Finding{
				Kind: check.KindBadConfig, Path: config.FileName,
				Problem: fmt.Sprintf("suite %s: driver %s records as %s, which none of the suite's correlators reads (they read %s): every recording would be one they can't tell",
					suite, d.Name, strings.Join(d.RecordingForms, ", "), strings.Join(reads, ", ")),
				Fix: "configure a correlator that reads it, or a driver that records in a form they read",
			})
		}
	}

	return out
}
