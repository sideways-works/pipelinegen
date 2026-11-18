package kit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
	pkit "github.com/sideways-works/plugkit/kit"
	"github.com/sideways-works/plugkit/semver"
)

const renderCheck = "render answers every declared target, at the fixture's versions, in the target's own steps"

// kitRender is what .pipelinegen-kit.json says of a rendering driver's fixture: the versions of
// each target to render for (inside the declared range, since the range alone names none), the
// shapes (both when left out), and the suite's step as a request carries it.
type kitRender struct {
	Targets    map[string]kitTarget `json:"targets"`
	Step       plugin.RenderStep    `json:"step"`
	Setup      []plugin.RenderSetup `json:"setup"`
	Record     bool                 `json:"record"`
	ObservedBy []string             `json:"observed_by"`
	Image      string               `json:"image"`
	Packages   []string             `json:"packages"`
	Params     map[string]string    `json:"params"`
	Secrets    []string             `json:"secrets"`
}

type kitTarget struct {
	Versions []string `json:"versions"`
	Shapes   []string `json:"shapes"`
}

// kitSecret is a secret every render request names, its variable holding the canary: a request
// carries names, so neither it nor the answer may hold the value.
const kitSecret = "PIPELINEGEN_KIT_SECRET" //nolint:gosec // a variable's name, never a value

func (k *kit) driverRender(ctx context.Context, f *kitRender) {
	if f == nil {
		k.Check(renderCheck, "the driver names renders, and the driver section of %s has no render", kitFile)

		return
	}

	secrets := append(slices.Clone(f.Secrets), kitSecret)

	restores := make([]func(), 0, len(secrets))
	for _, s := range secrets {
		restores = append(restores, pkit.SetEnv(s, Canary))
	}

	defer func() {
		for _, restore := range slices.Backward(restores) {
			restore()
		}
	}()

	var problems []string

	for _, r := range k.desc.Renders {
		t := f.Targets[r.Target]
		if len(t.Versions) == 0 {
			problems = append(problems, r.Target+": the fixture names no version of it")

			continue
		}

		shapes := t.Shapes
		if len(shapes) == 0 {
			shapes = []string{plugin.ShapePool, plugin.ShapeBatch}
		}

		// describe's check refused a range that doesn't parse
		rng, _ := semver.ParseRange(r.Versions) //nolint:errcheck // checked by describe

		for _, v := range t.Versions {
			if sv, err := semver.Parse(v); err != nil || !rng.Contains(sv) {
				problems = append(problems, fmt.Sprintf("%s: the fixture's version %q is outside %s", r.Target, v, r.Versions))

				continue
			}

			for _, shape := range shapes {
				req := plugin.RenderRequest{
					Request: k.request(), Target: r.Target, TargetVersion: v, Suite: kitSuite, Step: f.Step, Setup: f.Setup,
					Shape: plugin.RenderShape{Kind: shape}, Record: f.Record, ObservedBy: f.ObservedBy,
					Image: f.Image, Packages: f.Packages, Params: f.Params, Secrets: secrets,
				}

				if p := k.renderOnce(ctx, req); p != "" {
					problems = append(problems, fmt.Sprintf("%s %s, %s: %s", r.Target, v, shape, p))
				}
			}
		}
	}

	k.Check(renderCheck, strings.Join(firstFew(problems), "; "))
}

func (k *kit) renderOnce(ctx context.Context, req plugin.RenderRequest) string {
	if req.Shape.Kind != plugin.ShapePool && req.Shape.Kind != plugin.ShapeBatch {
		return fmt.Sprintf("the fixture's shape %q is neither %s nor %s", req.Shape.Kind, plugin.ShapePool, plugin.ShapeBatch)
	}

	if data, err := json.Marshal(req); err != nil || bytes.Contains(data, []byte(Canary)) {
		return "the request carries a secret's value"
	}

	var raw json.RawMessage
	if err := k.call(ctx, plugin.CallRender, req, &raw); err != nil {
		return err.Error()
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var a plugin.RenderAnswer
	if err := dec.Decode(&a); err != nil {
		return "the answer is no render answer: " + err.Error()
	}

	return a.Problem()
}
