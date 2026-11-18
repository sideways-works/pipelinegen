package kit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/sideways-works/pipelinegen/internal/connector"
	"github.com/sideways-works/pipelinegen/plugin"
)

// connectorFixture is a fixture's connector section; every string may hold {server}, the kit's fake
// host, which stands for the repository's host.
type connectorFixture struct {
	Remote    string                     `json:"remote"`
	Config    map[string]any             `json:"config,omitempty"`
	Responses []fixtureResponse          `json:"responses"`
	Calls     map[string]json.RawMessage `json:"calls"`
	Forks     []string                   `json:"forks,omitempty"`
	Same      []string                   `json:"same,omitempty"`
	// NoForks says why the host has no forks to show (a plain git server):
	// the fork check then checks only that the same ones run.
	NoForks string `json:"no_forks,omitempty"`
}

type fixtureResponse struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   string            `json:"query,omitempty"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	// BodyFile is relative to the fixture tree; Body is used without one.
	BodyFile string `json:"body_file,omitempty"`
	Body     string `json:"body,omitempty"`
}

const foreignRemote = "https://elsewhere.invalid/someone/else.git"

var connectorCapabilities = []string{
	plugin.CapChanges, plugin.CapStatuses, plugin.CapComment, plugin.CapDraft, plugin.CapLabels,
	plugin.CapReadySince, plugin.CapForks, plugin.CapHeadRef,
}

var commitID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

func describeConnector(d plugin.Description) []string {
	var problems []string

	for _, c := range d.Capabilities {
		if !slices.Contains(connectorCapabilities, c) {
			problems = append(problems, fmt.Sprintf("capability %q", c))
		}
	}

	for _, p := range d.Purposes {
		if !slices.Contains([]string{plugin.PurposeRead, plugin.PurposeChanges, plugin.PurposeReport}, p) {
			problems = append(problems, fmt.Sprintf("purpose %q: a connector needs read, changes or report; the store's is the store's", p))
		}
	}

	if slices.ContainsFunc(d.Purposes, func(p string) bool { return p != plugin.PurposeRead }) && (d.Auth == nil || d.Auth.Header == "") {
		problems = append(problems, "it needs tokens for its requests, but auth names no header")
	}

	for _, h := range slices.Concat(d.RequestHeaders, d.ResponseHeaders) {
		c := textproto.CanonicalMIMEHeaderKey(h)
		if c == "Authorization" || c == "Cookie" || c == "Host" || strings.HasPrefix(c, "Proxy-") {
			problems = append(problems, fmt.Sprintf("header %s: only the core sets it", h))
		}
	}

	if _, err := connector.Allowed("", d.APIHosts, d.APIHosts); err != nil {
		problems = append(problems, err.Error())
	}

	for core, host := range d.States {
		if !slices.Contains(plugin.States, core) || host == "" {
			problems = append(problems, fmt.Sprintf("states: %q to %q", core, host))
		}
	}

	for state, r := range d.Fallback {
		switch {
		case !slices.Contains(plugin.States, state):
			problems = append(problems, fmt.Sprintf("fallback for state %q", state))
		case r.Auth != nil && r.Auth.Purpose != plugin.PurposeReport:
			problems = append(problems, fmt.Sprintf("fallback for %s asks for the %q token, not report's", state, r.Auth.Purpose))
		}
	}

	return problems
}

type fakeHost struct {
	*httptest.Server

	fx    *connectorFixture
	tree  string
	mu    sync.Mutex
	used  map[int]bool
	stray []string
}

func (h *fakeHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	last := -1

	for i, f := range h.fx.Responses {
		if !strings.EqualFold(f.Method, r.Method) || f.Path != r.URL.Path || (f.Query != "" && f.Query != r.URL.RawQuery) {
			continue
		}

		last = i

		if !h.used[i] {
			break
		}
	}

	if last < 0 {
		h.stray = append(h.stray, r.Method+" "+r.URL.RequestURI())

		w.WriteHeader(http.StatusNotFound)

		return
	}

	h.used[last] = true
	f := h.fx.Responses[last]

	body := []byte(f.Body)

	if f.BodyFile != "" {
		data, err := os.ReadFile(filepath.Join(h.tree, filepath.Clean(f.BodyFile)))
		if err != nil {
			h.stray = append(h.stray, fmt.Sprintf("%s %s: body_file: %v", r.Method, r.URL.RequestURI(), err))

			w.WriteHeader(http.StatusInternalServerError)

			return
		}

		body = data
	}

	for k, v := range f.Headers {
		w.Header().Set(k, h.fill(v))
	}

	w.WriteHeader(max(f.Status, http.StatusOK))

	if _, err := io.WriteString(w, h.fill(string(body))); err != nil {
		return
	}
}

func (h *fakeHost) fill(s string) string { return strings.ReplaceAll(s, "{server}", h.URL) }

func (k *kit) connectorFixture() (*connectorFixture, error) {
	data, err := os.ReadFile(filepath.Join(k.o.Tree, kitFile))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", kitFile, err)
	}

	var f struct {
		Connector *connectorFixture `json:"connector"`
	}

	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", kitFile, err)
	}

	if f.Connector == nil {
		return nil, fmt.Errorf("%w: %s has no \"connector\" section", errFixture, kitFile)
	}

	return f.Connector, nil
}

func (k *kit) connector(ctx context.Context) {
	if k.o.Tree == "" {
		k.check("the fixture exercises every call the connector declares", "no --tree, so identify, links and the calls of its capabilities never ran")

		return
	}

	fx, err := k.connectorFixture()
	if err != nil {
		k.check("the fixture names the connector's host", "%v", err)

		return
	}

	host := &fakeHost{fx: fx, tree: k.o.Tree, used: map[int]bool{}}
	host.Server = httptest.NewServer(host)

	defer host.Close()

	e, ok := k.exchange(host)
	if !ok {
		return
	}

	k.identify(ctx, e, host.fill(fx.Remote))

	var links plugin.WebLinks

	err = k.exchangeCall(ctx, e, plugin.CallLinks, k.args(fx, plugin.CallLinks, host), &links)
	k.check("links answers without a request", errText(err))

	if k.can(plugin.CapChanges) {
		k.changes(ctx, e, fx, host)
	}

	k.reports(ctx, e, fx, host)

	host.mu.Lock()
	stray := host.stray
	host.mu.Unlock()

	k.check("every request finds a response in the fixture", ifNot(len(stray) == 0, "none for "+strings.Join(firstFew(stray), ", ")))
}

func (k *kit) exchange(host *fakeHost) (*connector.Exchange, bool) {
	cfg := maps.Clone(k.o.Config)
	if cfg == nil {
		cfg = map[string]any{}
	}

	for key, v := range host.fx.Config {
		if s, ok := v.(string); ok {
			v = host.fill(s)
		}

		cfg[key] = v
	}

	// the core puts the code's remote in every call's config
	cfg["remote"] = host.fill(host.fx.Remote)

	p := k.p
	p.Config = cfg

	hosts, err := connector.Allowed(host.URL, k.desc.APIHosts, []string{host.URL})
	if err != nil {
		k.check("the fixture names the connector's host", "%v", err)

		return nil, false
	}

	return &connector.Exchange{
		Plugin: p, Desc: k.desc, Hosts: hosts, HTTP: host.Client(),
		Tokens: map[string]string{
			plugin.PurposeChanges: Canary + "-changes",
			plugin.PurposeReport:  Canary + "-report",
		},
	}, true
}

func (k *kit) args(fx *connectorFixture, call string, host *fakeHost) json.RawMessage {
	raw, ok := fx.Calls[call]
	if !ok {
		return json.RawMessage("{}")
	}

	return json.RawMessage(host.fill(string(raw)))
}

func (k *kit) exchangeCall(ctx context.Context, e *connector.Exchange, call string, args json.RawMessage, answer any) error {
	err := e.Call(ctx, call, k.work(), args, answer)
	if err != nil {
		k.seen.WriteString(err.Error())
	}

	k.seen.WriteString(canonical(answer))

	return err
}

// identify claims the fixture's remote and disowns one elsewhere, both
// without a request.
func (k *kit) identify(ctx context.Context, e *connector.Exchange, remote string) {
	var mine, theirs plugin.Identity

	err := k.exchangeCall(ctx, e, plugin.CallIdentify, mustJSON(plugin.IdentifyArgs{Remote: remote}), &mine)

	switch {
	case err != nil:
		k.check("identify claims the fixture's remote", "%v", err)
	case !mine.Mine || mine.Repository == "":
		k.check("identify claims the fixture's remote", "it answered %s", canonical(mine))
	default:
		k.check("identify claims the fixture's remote", "")
	}

	err = k.exchangeCall(ctx, e, plugin.CallIdentify, mustJSON(plugin.IdentifyArgs{Remote: foreignRemote}), &theirs)

	switch {
	case err != nil:
		k.check("identify disowns a remote elsewhere", "%v", err)
	default:
		k.check("identify disowns a remote elsewhere", ifNot(!theirs.Mine, "it claimed "+foreignRemote))
	}
}

// changes lists the fixture's changes: each well formed, and none the
// fixture shows from a fork admitted to run.
func (k *kit) changes(ctx context.Context, e *connector.Exchange, fx *connectorFixture, host *fakeHost) {
	if _, ok := fx.Calls[plugin.CallChanges]; !ok {
		k.check("the fixture exercises every call the connector declares", "it declares changes, and the fixture names no arguments for it")

		return
	}

	var a plugin.ChangesAnswer
	if err := k.exchangeCall(ctx, e, plugin.CallChanges, k.args(fx, plugin.CallChanges, host), &a); err != nil {
		k.check("changes answers", "%v", err)

		return
	}

	k.check("changes answers", ifNot(len(a.Changes) > 0, "it listed no change; the fixture's should show some"))

	byID := map[string]plugin.Change{}

	var problems []string

	for _, c := range a.Changes {
		switch {
		case c.ID == "" || byID[c.ID].ID != "":
			problems = append(problems, fmt.Sprintf("id %q empty or twice", c.ID))
		case !commitID.MatchString(c.Head) || c.Base == "":
			problems = append(problems, fmt.Sprintf("%s: head %q, base %q", c.ID, c.Head, c.Base))
		case !slices.Contains([]string{plugin.FromSame, plugin.FromFork, plugin.FromUnknown}, c.From):
			problems = append(problems, fmt.Sprintf("%s: from %q, not same, fork or unknown", c.ID, c.From))
		case !k.can(plugin.CapForks) && c.From != plugin.FromUnknown:
			problems = append(problems, fmt.Sprintf("%s: from %q without the forks capability, which must say unknown", c.ID, c.From))
		}

		byID[c.ID] = c
	}

	k.check("every change is well formed and says where it comes from", strings.Join(firstFew(problems), "; "))

	if !k.can(plugin.CapForks) {
		k.skip("a change from a fork never runs", "no forks capability: every change is unknown, and none runs")

		return
	}

	if len(fx.Forks) == 0 && (fx.NoForks == "" || len(fx.Same) == 0) {
		k.check("a change from a fork never runs", "the connector declares forks, and the fixture names no change from a fork (\"forks\": [ids]), "+
			"or says why its host has none (\"no_forks\") and names changes from the same repository (\"same\": [ids])")

		return
	}

	problems = nil

	for _, id := range fx.Forks {
		c, ok := byID[id]

		switch {
		case !ok:
			problems = append(problems, id+" isn't listed")
		case connector.Admit(c):
			problems = append(problems, fmt.Sprintf("%s comes from a fork and was reported %q", id, c.From))
		}
	}

	for _, id := range fx.Same {
		if c, ok := byID[id]; !ok || !connector.Admit(c) {
			problems = append(problems, fmt.Sprintf("%s comes from the same repository and was reported %q", id, c.From))
		}
	}

	k.check("a change from a fork never runs", strings.Join(problems, "; "))
}

func (k *kit) reports(ctx context.Context, e *connector.Exchange, fx *connectorFixture, host *fakeHost) {
	type call struct {
		name, capability string
		answer           any
		check            func() string
	}

	var (
		statuses plugin.StatusesAnswer
		subject  plugin.SubjectAnswer
	)

	calls := []call{
		{name: plugin.CallStatus, capability: plugin.CapStatuses, answer: &map[string]any{}},
		{name: plugin.CallStatuses, capability: plugin.CapStatuses, answer: &statuses, check: func() string {
			for _, s := range statuses.Statuses {
				if !slices.Contains(plugin.States, s.State) || s.Context == "" {
					return fmt.Sprintf("context %q in state %q, not one of the core's", s.Context, s.State)
				}
			}

			return ""
		}},
		{name: plugin.CallSubject, capability: plugin.CapStatuses, answer: &subject, check: func() string {
			return ifNot(subject.Subject != "" && !strings.Contains(subject.Subject, "\n"), fmt.Sprintf("subject %q", subject.Subject))
		}},
		{name: plugin.CallComment, capability: plugin.CapComment, answer: &map[string]any{}},
	}

	for _, c := range calls {
		if !k.can(c.capability) {
			continue
		}

		if _, ok := fx.Calls[c.name]; !ok {
			k.check("the fixture exercises every call the connector declares", "it declares %s, and the fixture names no arguments for %s", c.capability, c.name)

			continue
		}

		err := k.exchangeCall(ctx, e, c.name, k.args(fx, c.name, host), c.answer)

		switch {
		case err != nil:
			k.check(c.name+" answers", "%v", err)
		case c.check != nil:
			k.check(c.name+" answers", c.check())
		default:
			k.check(c.name+" answers", "")
		}
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}

	return data
}
