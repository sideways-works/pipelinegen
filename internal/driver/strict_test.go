package driver

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func refusal(t *testing.T, verb string, conf map[string]any) error {
	t.Helper()

	c := &Command{Env: os.Environ(), Log: io.Discard}
	req := commandRequest(t, conf, false, "a")

	switch verb {
	case plugin.CallPrepare:
		return c.Prepare(t.Context(), req)
	case plugin.CallSetup:
		r, err := c.Setup(t.Context(), req)
		if err == nil {
			err = r.Stop(t.Context())
		}

		return err
	default:
		_, err := c.Run(t.Context(), req, func(plugin.DriverEvent) {})

		return err
	}
}

func TestACommandStepRefusesWhatItWouldIgnore(t *testing.T) {
	top := "the keys of the command driver's settings are run, start, stop, each, all, dir, env, ready, export"
	cases := []struct {
		verb string
		conf map[string]any
		want string
	}{
		{plugin.CallRun, map[string]any{"each": []any{"true"}, "evn": map[string]any{}}, "evn: unknown key; " + top},
		{plugin.CallPrepare, map[string]any{"run": []any{"true"}, "drr": "x"}, "drr: unknown key; " + top},
		{plugin.CallSetup, map[string]any{"start": []any{"sleep", "60"}, "ready": map[string]any{"htp": "http://127.0.0.1:1/"}},
			"ready.htp: unknown key; the keys of ready are http, file, tcp, seconds"},
		{plugin.CallSetup, map[string]any{"start": []any{"sleep", "60"}, "ready": map[string]any{"file": "x", "secs": 5}},
			"ready.secs: unknown key; the keys of ready are http, file, tcp, seconds"},
		// a key of another verb was read by none
		{plugin.CallRun, map[string]any{"each": []any{"true"}, "start": []any{"sleep", "60"}}, "start: a run step doesn't read it; it reads each, all, dir, env"},
		{plugin.CallSetup, map[string]any{"run": []any{"true"}, "each": []any{"true"}}, "each: a set-up doesn't read it; it reads run, start, stop, ready, export, dir, env"},
		{plugin.CallPrepare, map[string]any{"run": []any{"true"}, "export": map[string]any{"A": "b"}}, "export: a prepare step doesn't read it; it reads run, dir, env"},
		{plugin.CallRun, map[string]any{"each": "true"}, `each: takes a list, not "true"`},
		{plugin.CallRun, map[string]any{"each": []any{"true"}, "env": map[string]any{"A": 1}}, `env["A"]: takes a string, not 1`},
		{plugin.CallSetup, map[string]any{"start": []any{"sleep", "60"}, "ready": map[string]any{"file": "x", "seconds": "30"}}, `ready.seconds: takes a whole number, not "30"`},
		{plugin.CallRun, map[string]any{"each": []any{"true"}, "dir": nil}, "dir: has no value"},
		// what a step would misread
		{plugin.CallSetup, map[string]any{"run": []any{"true"}, "stop": []any{"true"}}, "stop: stops what start started, and the set-up has no start"},
		{plugin.CallSetup, map[string]any{"run": []any{"true"}, "ready": map[string]any{"file": "x"}}, "ready: waits for what start started, and the set-up has no start"},
		{plugin.CallSetup, map[string]any{"start": []any{"sleep", "60"}, "ready": map[string]any{"seconds": 30}}, "ready: names nothing to wait for; give one of http, tcp and file"},
		{plugin.CallSetup, map[string]any{"start": []any{"sleep", "60"}, "ready": map[string]any{"file": "x", "tcp": "127.0.0.1:1"}}, "ready: names tcp and file; it waits for one of them"},
		{plugin.CallSetup, map[string]any{"start": []any{"sleep", "60"}, "ready": map[string]any{"file": "x", "seconds": 0}}, "ready.seconds: 0 is less than 1; leave it out for two minutes"},
		{plugin.CallSetup, map[string]any{}, "a set-up needs run, start or export"},
		{plugin.CallPrepare, map[string]any{"dir": "."}, "a prepare step needs run"},
		{plugin.CallRun, map[string]any{"dir": "."}, "a run step needs each or all"},
		{plugin.CallRun, map[string]any{"each": []any{"true"}, "env": map[string]any{"A=B": "c"}}, `env["A=B"]: "A=B" is no variable's name`},
		{plugin.CallSetup, map[string]any{"export": map[string]any{"NOTES URL": "x"}}, `export["NOTES URL"]: "NOTES URL" is no variable's name`},
		{plugin.CallRun, map[string]any{"each": []any{"true"}, "dir": "/srv"}, `dir: "/srv" is no directory inside the tree`},
		{plugin.CallRun, map[string]any{"each": []any{"true"}, "dir": "../up"}, `dir: "../up" is no directory inside the tree`},
	}

	for _, c := range cases {
		err := refusal(t, c.verb, c.conf)
		if want := "command: settings: " + c.want; err == nil || err.Error() != want {
			t.Errorf("%s %v:\n got %v\nwant %s", c.verb, c.conf, err, want)
		}

		if verr := ValidateCommand(c.verb, c.conf); verr == nil || !strings.HasSuffix(err.Error(), verr.Error()) {
			t.Errorf("%s %v: validate says %v, the verb %v", c.verb, c.conf, verr, err)
		}
	}
}

func TestACommandStepTakesWhatItReads(t *testing.T) {
	cases := []struct {
		verb string
		conf map[string]any
	}{
		{plugin.CallPrepare, map[string]any{"run": []any{"true"}, "dir": ".", "env": map[string]any{"A": "b"}}},
		{plugin.CallSetup, map[string]any{"run": []any{"true"}, "export": map[string]any{"A": "{source}"}}},
		{plugin.CallSetup, map[string]any{
			"start": []any{"sh", "-c", "touch up; exec sleep 600"}, "stop": []any{"true"},
			"ready": map[string]any{"file": "up", "seconds": 30}, "env": map[string]any{"PIPELINEGEN_COVER_DIR": "{work}/ctl"},
		}},
		{plugin.CallRun, map[string]any{"each": []any{"true"}, "all": []any{"true"}}},
	}

	for _, c := range cases {
		if err := ValidateCommand(c.verb, c.conf); err != nil {
			t.Errorf("%s %v: validate: %v", c.verb, c.conf, err)
		}

		if err := refusal(t, c.verb, c.conf); err != nil {
			t.Errorf("%s %v: %v", c.verb, c.conf, err)
		}
	}
}
