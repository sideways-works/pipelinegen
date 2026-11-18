package kit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

// kitUnknown is a key no driver takes: the kit adds it to the fixture's
// settings to see them refused.
const kitUnknown = "pipelinegen_kit_unknown"

func (k *kit) driverValidate(ctx context.Context, f kitDriver) {
	type verbConf struct {
		verb string
		conf map[string]any
	}

	var todo []verbConf

	for _, v := range []verbConf{{plugin.CallPrepare, f.Prepare}, {plugin.CallSetup, f.Setup}, {plugin.CallRun, f.Config}} {
		if !k.desc.HasVerb(v.verb) {
			continue
		}

		if v.conf == nil && v.verb == plugin.CallRun {
			v.conf = k.o.Config
		}

		todo = append(todo, v)
	}

	steps := make([]plugin.ValidateStep, 0, 2*len(todo))

	for _, v := range todo {
		bad := maps.Clone(v.conf)
		if bad == nil {
			bad = map[string]any{}
		}

		bad[kitUnknown] = 1

		steps = append(steps, plugin.ValidateStep{Verb: v.verb, Settings: v.conf}, plugin.ValidateStep{Verb: v.verb, Settings: bad})
	}

	var results []plugin.ValidateResult

	var a plugin.ValidateAnswer

	err := k.call(ctx, plugin.CallValidate, plugin.ValidateRequest{Request: k.p.NewRequest(k.WorkDir()), Steps: steps}, &a)
	if err == nil && len(a.Steps) != len(steps) {
		err = fmt.Errorf("%w: it answered %d steps of %d", plugin.ErrFailed, len(a.Steps), len(steps))
	}

	if err != nil {
		k.Check("validate answers every step", "%v", err)

		return
	}

	results = a.Steps

	var took, refused, verbs []string

	for i, v := range todo {
		if p := results[2*i].Problems; len(p) > 0 {
			took = append(took, fmt.Sprintf("%s: %s", v.verb, (&p[0]).Error()))
		}

		if !namesKey(results[2*i+1].Problems, kitUnknown) {
			refused = append(refused, fmt.Sprintf("%s: %v", v.verb, results[2*i+1].Problems))
		}

		if problem := k.verbRefuses(ctx, v.verb, steps[2*i+1].Settings); problem != "" {
			verbs = append(verbs, v.verb+": "+problem)
		}
	}

	k.Check("validate takes the fixture's settings", strings.Join(took, "; "))
	k.Check("validate refuses a key no driver takes, naming it", strings.Join(refused, "; "))
	k.Check("each verb refuses what validate refuses, before it runs anything", strings.Join(verbs, "; "))
}

func namesKey(problems []plugin.SettingError, key string) bool {
	for _, p := range problems {
		if strings.Contains(p.Place, key) || strings.Contains(p.Problem, key) {
			return true
		}
	}

	return false
}

func (k *kit) verbRefuses(ctx context.Context, verb string, conf map[string]any) string {
	tree := k.WorkDir()
	if err := copyTree(k.o.Tree, tree); err != nil {
		return err.Error()
	}

	f, _ := k.driverFixture()

	req := plugin.DriverRequest{
		Protocol: plugin.Protocol, Work: k.WorkDir(), Cache: k.p.Cache, Config: conf,
		Tree: tree, Suite: kitSuite, Prepared: k.WorkDir(), Slices: f.Slices,
	}

	in, err := json.Marshal(req)
	if err != nil {
		return err.Error()
	}

	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	cmd := exec.CommandContext(cctx, k.p.Path, verb) //nolint:gosec // the plugin under test
	cmd.Dir = tree
	cmd.Env = os.Environ()
	// stdin ends after the request: a set-up that took them stops at once
	cmd.Stdin = bytes.NewReader(in)

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	switch err := cmd.Run(); {
	case err == nil:
		return "it took them and exited 0"
	case !strings.Contains(stderr.String(), kitUnknown):
		return "it failed without naming " + kitUnknown + ": " + strings.TrimSpace(stderr.String())
	case stdout.Len() > 0 && verb != plugin.CallSetup:
		return "it answered before it refused: " + strings.TrimSpace(stdout.String())
	}

	return ""
}
