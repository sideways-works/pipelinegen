package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type testCheck struct {
	Name    string   `json:"name"`
	Run     string   `json:"run"`
	Timeout string   `json:"timeout"`
	Need    []string `json:"need"`
}

func (*testCheck) SettingNoun() string { return "a check" }

type testSettings struct {
	Script  string            `json:"script"`
	Workers int               `json:"workers"`
	Install *bool             `json:"install"`
	Env     map[string]string `json:"env"`
	Checks  []testCheck       `json:"checks"`
}

func TestDecodeSettingsRefusesWhatADriverWouldIgnore(t *testing.T) {
	checks := func(items ...any) map[string]any { return map[string]any{"checks": items} }
	ok := map[string]any{"name": "a", "run": "true"}

	cases := []struct {
		conf  map[string]any
		place string
		want  string
	}{
		{map[string]any{"scrpit": "true"}, "scrpit", "unknown key; the keys of the settings are script, workers, install, env, checks"},
		{checks(ok, ok, map[string]any{"name": "c", "run": "true", "timout": "5m"}), "checks[2].timout", "unknown key; the keys of a check are name, run, timeout, need"},
		{checks(map[string]any{"name": "a", "timeout": 5}), "checks[0].timeout", "takes a string, not 5"},
		{checks(map[string]any{"name": "a", "need": "b"}), "checks[0].need", `takes a list, not "b"`},
		{checks("a"), "checks[0]", `takes a map of keys, not "a"`},
		{map[string]any{"checks": "a"}, "checks", `takes a list, not "a"`},
		{map[string]any{"workers": 1.5}, "workers", "takes a whole number, not 1.5"},
		{map[string]any{"workers": "2"}, "workers", `takes a whole number, not "2"`},
		{map[string]any{"install": "yes"}, "install", `takes true or false, not "yes"`},
		{map[string]any{"env": map[string]any{"A": 1}}, `env["A"]`, "takes a string, not 1"},
		{map[string]any{"env": []any{"A"}}, "env", "takes a map, not a list"},
		{map[string]any{"script": nil}, "script", "has no value"},
		{map[string]any{"install": nil}, "install", "has no value"},
	}

	for _, c := range cases {
		var s testSettings

		err := DecodeSettings(c.conf, &s)

		var se *SettingError
		if !errors.As(err, &se) || se.Place != c.place || se.Problem != c.want || !errors.Is(err, ErrSettings) {
			t.Errorf("%v: %v, want %s: %s", c.conf, err, c.place, c.want)
		}
	}

	var s testSettings
	if err := DecodeSettings(map[string]any{"workers": 2, "install": false, "checks": []any{ok}, "env": map[string]any{"A": "b"}}, &s); err != nil ||
		s.Workers != 2 || s.Install == nil || *s.Install || len(s.Checks) != 1 || s.Env["A"] != "b" {
		t.Errorf("settings taken: %+v %v", s, err)
	}
}

func TestServeDriverValidates(t *testing.T) {
	h := DriverHandlers{
		Describe: func() (Description, error) {
			return Description{Protocol: Protocol, Kind: KindDriver, Name: "d", Verbs: []string{CallRun}}, nil
		},
		Run: func(_ context.Context, _ DriverRequest, _ func(DriverEvent) error) error { return nil },
		Validate: func(verb string, settings map[string]any) error {
			var s testSettings
			if err := DecodeSettings(settings, &s); err != nil {
				return err
			}

			if verb != CallRun {
				return fmt.Errorf("%w: a run step's driver", ErrSettings)
			}

			return nil
		},
	}

	var out, errOut bytes.Buffer
	if code := ServeDriver(t.Context(), []string{CallDescribe}, strings.NewReader("{}"), &out, &errOut, h); code != 0 || !strings.Contains(out.String(), `"validates":true`) {
		t.Errorf("describe: %d %s %s", code, out.String(), errOut.String())
	}

	req, err := json.Marshal(ValidateRequest{Steps: []ValidateStep{
		{Verb: CallRun, Settings: map[string]any{"script": "true"}},
		{Verb: CallRun, Settings: map[string]any{"checks": []any{map[string]any{"name": "a", "timout": "5m"}}}},
		{Verb: CallSetup, Settings: map[string]any{}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	out.Reset()

	if code := ServeDriver(t.Context(), []string{CallValidate}, bytes.NewReader(req), &out, &errOut, h); code != 0 {
		t.Fatalf("validate: %d %s", code, errOut.String())
	}

	var a ValidateAnswer
	if err := json.Unmarshal(out.Bytes(), &a); err != nil {
		t.Fatal(err)
	}

	want := []ValidateResult{
		{},
		{Problems: []SettingError{{Place: "checks[0].timout", Problem: "unknown key; the keys of a check are name, run, timeout, need"}}},
		{Problems: []SettingError{{Problem: "a run step's driver"}}},
	}

	if got, wantJSON := must(json.Marshal(a.Steps)), must(json.Marshal(want)); string(got) != string(wantJSON) {
		t.Errorf("answer %s, want %s", got, wantJSON)
	}

	h.Validate = nil

	out.Reset()

	if code := ServeDriver(t.Context(), []string{CallDescribe}, strings.NewReader("{}"), &out, &errOut, h); code != 0 || strings.Contains(out.String(), "validates") {
		t.Errorf("describe without validate: %d %s", code, out.String())
	}

	if code := ServeDriver(t.Context(), []string{CallValidate}, bytes.NewReader(req), &out, &errOut, h); code == 0 {
		t.Error("validate answered by a driver without it")
	}
}

func must(data []byte, err error) []byte {
	if err != nil {
		panic(err)
	}

	return data
}
