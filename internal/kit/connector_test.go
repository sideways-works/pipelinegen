package kit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

const fakePulls = `[
 {"number": 7, "head": "1111111111111111111111111111111111111111", "base": "main", "fork": false},
 {"number": 8, "head": "2222222222222222222222222222222222222222", "base": "main", "fork": true}
]`

func connectorFixtureFile(skip string) string {
	responses := []map[string]any{
		{"method": "GET", "path": "/api/pulls", "query": "state=open", "status": 200, "body_file": "pulls.json"},
		{"method": "POST", "path": "/api/statuses/1111111111111111111111111111111111111111", "status": 201, "body": "{}"},
		{"method": "GET", "path": "/api/statuses/1111111111111111111111111111111111111111", "status": 200, "body": `[{"context": "ci/main", "state": "failure"}]`},
		{"method": "GET", "path": "/api/commits/1111111111111111111111111111111111111111", "status": 200, "body": `{"message": "one line\n\nand a body"}`},
		{"method": "GET", "path": "/api/issues/7/comments", "status": 200, "body": "[]"},
		{"method": "POST", "path": "/api/issues/7/comments", "status": 201, "body": "{}"},
	}

	kept := responses[:0]

	for _, r := range responses {
		if r["path"] != skip {
			kept = append(kept, r)
		}
	}

	data, err := json.Marshal(map[string]any{"connector": map[string]any{
		"remote":    "{server}/team/app.git",
		"config":    map[string]any{"api": "{server}/api"},
		"responses": kept,
		"calls": map[string]any{
			"changes":  map[string]any{"base": "main"},
			"status":   map[string]any{"commit": "1111111111111111111111111111111111111111", "context": "ci/main", "state": "pending"},
			"statuses": map[string]any{"commit": "1111111111111111111111111111111111111111"},
			"subject":  map[string]any{"commit": "1111111111111111111111111111111111111111"},
			"comment":  map[string]any{"change": "pr-7", "marker": "<!-- m -->", "body": "<!-- m --> hello"},
			"links":    map[string]any{"commit": "1111111111111111111111111111111111111111"},
		},
		"forks": []string{"pr-8"},
		"same":  []string{"pr-7"},
	}})
	if err != nil {
		panic(err)
	}

	return string(data)
}

// connectorFakes is a connector for the API the fixture serves. kind says
// how it misbehaves: connector-leak reports a fork's change as the
// repository's own, connector-sneaky asks for requests where none belong,
// connector-greedy declares the store's purpose and sets Authorization.
func connectorFakes(kind string) map[string]plugin.Handler {
	d := plugin.Description{
		Kind: plugin.KindConnector, Capabilities: []string{plugin.CapChanges, plugin.CapStatuses, plugin.CapComment, plugin.CapForks},
		Purposes: []string{plugin.PurposeRead, plugin.PurposeChanges, plugin.PurposeReport},
		Auth:     &plugin.ConnectorAuth{Header: "Authorization", Scheme: "Bearer"}, RequestHeaders: []string{"Accept"},
	}

	if kind == "connector-greedy" {
		d.Purposes = append(d.Purposes, plugin.PurposeStore)
		d.RequestHeaders = append(d.RequestHeaders, "Authorization")
	}

	h := map[string]plugin.Handler{plugin.CallDescribe: describe(d)}

	for _, call := range plugin.ConnectorCalls {
		h[call] = func(in []byte) (any, error) { return fakeConnectorRound(kind, call, in) }
	}

	return h
}

func fakeConnectorRound(kind, call string, in []byte) (any, error) {
	var req plugin.ExchangeRequest
	if err := decode(in, &req); err != nil {
		return nil, err
	}

	api, ok := req.Config["api"].(string)
	if !ok {
		return nil, fmt.Errorf("%w: no api in the config", errUndeclared)
	}

	var args struct {
		Remote string `json:"remote"`
		Commit string `json:"commit"`
		Change string `json:"change"`
	}

	if err := json.Unmarshal(req.Args, &args); err != nil {
		return nil, fmt.Errorf("args: %w", err)
	}

	done := func(v any) (any, error) {
		data, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("answer: %w", err)
		}

		return plugin.Round{Done: data}, nil
	}

	ask := func(method, path, purpose string) (any, error) {
		r := plugin.HTTPRequest{ID: fmt.Sprintf("r%d", len(req.Responses)), Method: method, URL: api + path, Headers: map[string]string{"Accept": "application/json"}}
		if purpose != "" {
			r.Auth = &plugin.HTTPAuth{Purpose: purpose}
		}

		return plugin.Round{Requests: []plugin.HTTPRequest{r}}, nil
	}

	body := func(i int) []byte {
		data, err := os.ReadFile(filepath.Join(req.Work, req.Responses[i].BodyFile))
		if err != nil {
			return nil
		}

		return data
	}

	if kind == "connector-sneaky" && (call == plugin.CallIdentify || call == plugin.CallLinks) && len(req.Responses) == 0 {
		return ask("GET", "/whoami", "")
	}

	switch call {
	case plugin.CallIdentify:
		host := strings.TrimSuffix(api, "/api")

		return done(plugin.Identity{Mine: strings.HasPrefix(args.Remote, host+"/"), Repository: "team/app"})
	case plugin.CallLinks:
		return done(plugin.WebLinks{Commit: api + "/c/" + args.Commit})
	case plugin.CallChanges:
		if len(req.Responses) == 0 {
			return ask("GET", "/pulls?state=open", plugin.PurposeChanges)
		}

		var pulls []struct {
			Number int    `json:"number"`
			Head   string `json:"head"`
			Base   string `json:"base"`
			Fork   bool   `json:"fork"`
		}

		if err := json.Unmarshal(body(0), &pulls); err != nil {
			return nil, fmt.Errorf("pulls: %w", err)
		}

		var a plugin.ChangesAnswer

		for _, p := range pulls {
			from := plugin.FromSame
			if p.Fork && kind != "connector-leak" {
				from = plugin.FromFork
			}

			a.Changes = append(a.Changes, plugin.Change{ID: fmt.Sprintf("pr-%d", p.Number), Number: p.Number, Base: p.Base, Head: p.Head, From: from})
		}

		return done(a)
	case plugin.CallStatus:
		if len(req.Responses) == 0 {
			return ask("POST", "/statuses/"+args.Commit, plugin.PurposeReport)
		}

		return done(map[string]any{})
	case plugin.CallStatuses:
		if len(req.Responses) == 0 {
			return ask("GET", "/statuses/"+args.Commit, plugin.PurposeReport)
		}

		var a plugin.StatusesAnswer
		if err := json.Unmarshal(body(0), &a.Statuses); err != nil {
			return nil, fmt.Errorf("statuses: %w", err)
		}

		return done(a)
	case plugin.CallSubject:
		if len(req.Responses) == 0 {
			return ask("GET", "/commits/"+args.Commit, plugin.PurposeReport)
		}

		var c struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body(0), &c); err != nil {
			return nil, fmt.Errorf("commit: %w", err)
		}

		subject, _, _ := strings.Cut(c.Message, "\n")

		return done(plugin.SubjectAnswer{Subject: subject})
	case plugin.CallComment:
		n := strings.TrimPrefix(args.Change, "pr-")

		switch len(req.Responses) {
		case 0:
			return ask("GET", "/issues/"+n+"/comments", plugin.PurposeReport)
		case 1:
			return ask("POST", "/issues/"+n+"/comments", plugin.PurposeReport)
		}

		return done(map[string]any{})
	}

	return nil, fmt.Errorf("%w: %s", errUndeclared, call)
}

func runConnector(t *testing.T, kind, skip string, withTree bool) map[string]string {
	t.Helper()

	fixture := ""
	if withTree {
		fixture = connectorFixtureFile(skip)
	}

	return runConnectorOn(t, kind, fixture)
}

func runConnectorOn(t *testing.T, kind, fixture string) map[string]string {
	t.Helper()

	o := Options{Path: fakeExe(t, kind), Work: t.TempDir()}

	if fixture != "" {
		o.Tree = t.TempDir()

		for f, body := range map[string]string{kitFile: fixture, "pulls.json": fakePulls} {
			if err := os.WriteFile(filepath.Join(o.Tree, f), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	out := map[string]string{}

	for _, r := range Run(t.Context(), o) {
		if !r.Skipped {
			out[r.Check] = r.Problem
		}
	}

	return out
}

func TestAWellBehavedConnectorPasses(t *testing.T) {
	got := runConnector(t, "connector", "", true)

	for _, check := range []string{
		"describe answers the protocol", "refuses no-such-call", "refuses pipeline",
		"identify claims the fixture's remote", "identify disowns a remote elsewhere", "links answers without a request",
		"changes answers", "every change is well formed and says where it comes from", "a change from a fork never runs",
		"status answers", "statuses answers", "subject answers", "comment answers",
		"every request finds a response in the fixture", "no answer or message carries the token",
	} {
		problem, ran := got[check]
		if !ran || problem != "" {
			t.Errorf("%s: ran %v, problem %q", check, ran, problem)
		}
	}

	for check, problem := range got {
		if problem != "" {
			t.Errorf("%s: %s", check, problem)
		}
	}
}

// A connector that reports a fork's change as the repository's own fails:
// that change would run with the pipeline's credentials.
func TestAConnectorAdmittingAForkFails(t *testing.T) {
	got := runConnector(t, "connector-leak", "", true)
	if !strings.Contains(got["a change from a fork never runs"], "pr-8 comes from a fork") {
		t.Fatalf("%q", got["a change from a fork never runs"])
	}
}

func TestAConnectorRequestingWhereNoneBelongFails(t *testing.T) {
	got := runConnector(t, "connector-sneaky", "", true)

	for _, check := range []string{"identify claims the fixture's remote", "identify disowns a remote elsewhere", "links answers without a request"} {
		if !strings.Contains(got[check], "sends no requests") {
			t.Errorf("%s: %q", check, got[check])
		}
	}
}

func TestAGreedyConnectorFailsDescribe(t *testing.T) {
	got := runConnector(t, "connector-greedy", "", true)

	problem := got["describe answers the protocol"]
	if !strings.Contains(problem, `purpose "store"`) || !strings.Contains(problem, "header Authorization") {
		t.Fatalf("%q", problem)
	}
}

func TestAConnectorFixtureMustAnswerEverything(t *testing.T) {
	got := runConnector(t, "connector", "/api/issues/7/comments", true)
	if !strings.Contains(got["every request finds a response in the fixture"], "GET /api/issues/7/comments") {
		t.Fatalf("%q", got["every request finds a response in the fixture"])
	}

	got = runConnector(t, "connector", "", false)
	if !strings.Contains(got["the fixture exercises every call the connector declares"], "no --tree") {
		t.Fatalf("%v", got)
	}
}

// A host with no forks, a plain git server, says so in its fixture: the
// fork check then holds the changes it names to running.
func TestAFixtureWithoutForksSaysWhy(t *testing.T) {
	var f map[string]map[string]any
	if err := json.Unmarshal([]byte(connectorFixtureFile("")), &f); err != nil {
		t.Fatal(err)
	}

	delete(f["connector"], "forks")

	without := func() string {
		data, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}

		return string(data)
	}

	if got := runConnectorOn(t, "connector", without())["a change from a fork never runs"]; !strings.Contains(got, "no_forks") {
		t.Fatalf("no forks, no reason: %q", got)
	}

	f["connector"]["no_forks"] = "a plain git server has no forks"

	if got := runConnectorOn(t, "connector", without()); got["a change from a fork never runs"] != "" {
		t.Fatalf("no forks, and why: %q", got["a change from a fork never runs"])
	}

	f["connector"]["same"] = []string{"pr-8"}

	if got := runConnectorOn(t, "connector", without())["a change from a fork never runs"]; !strings.Contains(got, "pr-8 comes from the same repository") {
		t.Fatalf("a same change reported a fork: %q", got)
	}
}
