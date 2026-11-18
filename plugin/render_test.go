package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func goDriver(renders ...Render) Description {
	return Description{Renders: renders, Kind: KindDriver, Name: "go-driver", Version: "v0.4.0"}
}

func TestARenderIsNativeOnlyWhenAllThreeConditionsHold(t *testing.T) {
	piperun := Render{Target: "piperun", Versions: ">=1.3 <2", Plugins: map[string]string{"go-test": ">=1.0 <2"}}
	runner := RenderTarget{Target: "piperun", Version: "1.4.0", Pinned: map[string]string{"go-test": "1.1.0"}}

	for _, tc := range []struct {
		name   string
		target RenderTarget
		driver Description
		notice string
	}{
		{"all hold", runner, goDriver(piperun), ""},
		{"no render for the target", runner, goDriver(Render{Target: "github-actions", Versions: "*"}),
			"go-driver v0.4.0 renders no piperun steps: fallback"},
		{"the driver's range leaves the target out", RenderTarget{Target: "piperun", Version: "1.2.0", Pinned: runner.Pinned}, goDriver(piperun),
			"go-driver v0.4.0 renders piperun >=1.3 <2; this runner targets 1.2.0: fallback"},
		{"the runner's range leaves the driver out", RenderTarget{Target: "piperun", Version: "1.4.0", Pinned: runner.Pinned, Accepts: map[string]string{"go-driver": ">=0.4 !=0.4.0"}}, goDriver(piperun),
			"this runner renders go-driver >=0.4 !=0.4.0 natively; this is v0.4.0: fallback"},
		{"a plugin the render uses isn't pinned", RenderTarget{Target: "piperun", Version: "1.4.0"}, goDriver(piperun),
			"go-driver v0.4.0 renders piperun steps with the plugin go-test, which piperun doesn't pin: fallback"},
		{"a plugin is pinned outside its range", RenderTarget{Target: "piperun", Version: "1.4.0", Pinned: map[string]string{"go-test": "2.0.0"}}, goDriver(piperun),
			"go-driver v0.4.0 renders with go-test >=1.0 <2; piperun pins 2.0.0: fallback"},
		{"a pre-release target outside a plain range", RenderTarget{Target: "piperun", Version: "1.4.0-rc.1", Pinned: runner.Pinned}, goDriver(piperun),
			"go-driver v0.4.0 renders piperun >=1.3 <2; this runner targets 1.4.0-rc.1: fallback"},
		{"a malformed range falls back", runner, goDriver(Render{Target: "piperun", Versions: ">=one"}),
			`go-driver v0.4.0 renders piperun ">=one", which is no range: fallback`},
		{"a malformed target version falls back", RenderTarget{Target: "piperun", Version: "one"}, goDriver(piperun),
			`this runner's piperun version "one" is no version: fallback`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.target.Choose(tc.driver)

			if got.Native != (tc.notice == "") || got.Notice != tc.notice {
				t.Fatalf("got native %v, notice %q; want %q", got.Native, got.Notice, tc.notice)
			}

			if got.Native && got.Render.Target != "piperun" {
				t.Errorf("chose %+v", got.Render)
			}
		})
	}
}

func TestRendersWithBadRangesAreRefused(t *testing.T) {
	if p := RendersProblem([]Render{{Target: "piperun", Versions: ">=1.2 <2 || 1.5.0-rc.1", Plugins: map[string]string{"go-test": "*"}}}); p != "" {
		t.Errorf("a good render: %s", p)
	}

	for _, tc := range []struct {
		renders []Render
		want    string
	}{
		{[]Render{{Versions: "*"}}, "names no target"},
		{[]Render{{Target: "piperun", Versions: "~1.2"}}, `versions "~1.2"`},
		{[]Render{{Target: "piperun", Versions: ""}}, `versions ""`},
		{[]Render{{Target: "piperun", Versions: "*", Plugins: map[string]string{"go-test": ">=one"}}}, `plugin "go-test"`},
		{[]Render{{Target: "piperun", Versions: "*"}, {Target: "piperun", Versions: ">=2"}}, "rendered twice"},
	} {
		if p := RendersProblem(tc.renders); !strings.Contains(p, tc.want) {
			t.Errorf("%+v: %q, want %q", tc.renders, p, tc.want)
		}
	}
}

func TestARenderAnswerIsChecked(t *testing.T) {
	good := RenderAnswer{Steps: []json.RawMessage{json.RawMessage(`{"uses": "go-test"}`)}, Marks: MarksProtocol, Results: "results", Caches: map[string]string{"gomod": "/go/pkg/mod"}}
	if p := good.Problem(); p != "" {
		t.Errorf("a good answer: %s", p)
	}

	for want, a := range map[string]RenderAnswer{
		"no steps":                 {Marks: MarksFile, Results: "r"},
		"step 1 is no JSON object": {Steps: []json.RawMessage{json.RawMessage(`[1]`)}, Marks: MarksFile, Results: "r"},
		`marks "bell"`:             {Steps: good.Steps, Marks: "bell", Results: "r"},
		`results "../r"`:           {Steps: good.Steps, Marks: MarksFile, Results: "../r"},
		`results "/r"`:             {Steps: good.Steps, Marks: MarksFile, Results: "/r"},
		`cache "npm"`:              {Steps: good.Steps, Marks: MarksFile, Results: "r", Caches: map[string]string{"npm": "npm"}},
	} {
		if p := a.Problem(); !strings.Contains(p, want) {
			t.Errorf("%+v: %q, want %q", a, p, want)
		}
	}
}

func TestADriverServesRenderBesideItsVerbs(t *testing.T) {
	h := DriverHandlers{
		Describe: func() (Description, error) {
			return goDriver(Render{Target: "piperun", Versions: "*"}), nil
		},
		Render: func(_ context.Context, req RenderRequest) (RenderAnswer, error) {
			return RenderAnswer{
				Steps: []json.RawMessage{json.RawMessage(`{"uses": "go-test", "secrets": ["` + strings.Join(req.Secrets, `","`) + `"]}`)},
				Marks: MarksProtocol, Results: "results",
			}, nil
		},
	}

	var out, errs bytes.Buffer

	in := `{"target": "piperun", "target_version": "1.2.0", "suite": "go", "step": {"run": "go-driver"}, "shape": {"kind": "pool"}, "secrets": ["STRIPE_KEY"]}`
	if code := ServeDriver(t.Context(), []string{CallRender}, strings.NewReader(in), &out, &errs, h); code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}

	var a RenderAnswer
	if err := json.Unmarshal(out.Bytes(), &a); err != nil || len(a.Steps) != 1 || !strings.Contains(string(a.Steps[0]), "STRIPE_KEY") {
		t.Fatalf("answer %s: %v", out.String(), err)
	}

	h.Render = nil

	out.Reset()
	errs.Reset()

	if code := ServeDriver(t.Context(), []string{CallDescribe}, strings.NewReader("{}"), &out, &errs, h); code == 0 || !strings.Contains(errs.String(), "no render") {
		t.Errorf("a driver that names renders and can't render described itself: %s", out.String())
	}

	if code := ServeDriver(t.Context(), []string{CallRender}, strings.NewReader(in), &out, &errs, h); code == 0 {
		t.Error("a driver without render answered it")
	}
}
