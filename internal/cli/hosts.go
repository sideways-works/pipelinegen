package cli

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/connector"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/plugin"
)

const defaultGitHubAPI = "https://api.github.com"

// openConnector fetches the connector the config pins under name and
// describes it. Its options come from the trusted config and the step's
// environment alone.
func openConnector(ctx context.Context, cfg *config.Config, name string, o connector.Options) (*connector.Host, error) {
	path, err := connector.Fetch(ctx, &http.Client{Timeout: 5 * time.Minute}, pluginsDir(), defaultCache(), cfg.ConnectorPin(name))
	if err != nil {
		return nil, err
	}

	o.Name, o.Path, o.Work = name, path, os.TempDir()

	return connector.Open(ctx, o)
}

// gitHubHost is the github section's host: the GitHub connector, fetched
// and described on its first call, so a step that never talks to GitHub
// never fetches it. It reports through the connector's calls and lists the
// pull requests with them.
type gitHubHost struct {
	cfg    *config.Config
	api    string
	remote string
	tokens map[string]string

	mu   sync.Mutex
	host *connector.Host
}

// newGitHubHost reads the API from the step's environment, never from the
// config, which a proposed change may have written.
func newGitHubHost(cfg *config.Config) *gitHubHost {
	api := strings.TrimRight(os.Getenv(envAPIURL), "/")
	if api == "" {
		api = defaultGitHubAPI
	}

	web := "github.com"
	if u, err := url.Parse(api); err == nil && api != defaultGitHubAPI && u.Host != "" {
		web = u.Host
	}

	return &gitHubHost{
		cfg: cfg, api: api, remote: "https://" + web + "/" + cfg.GitHub.Repository + ".git",
		tokens: map[string]string{
			plugin.PurposeChanges: tokenFor(cfg, plugin.PurposeChanges),
			plugin.PurposeReport:  tokenFor(cfg, plugin.PurposeReport),
		},
	}
}

// open keeps the connector once it opened, and tries again after a fetch
// that failed.
func (g *gitHubHost) open(ctx context.Context) (*connector.Host, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.host != nil {
		return g.host, nil
	}

	h, err := openConnector(ctx, g.cfg, config.ConnectorGitHub, connector.Options{
		Config: map[string]any{"api": g.api}, Remote: g.remote, API: g.api, Tokens: g.tokens,
	})
	if err != nil {
		return nil, err
	}

	g.host = h

	return h, nil
}

func (g *gitHubHost) Changes(ctx context.Context, a plugin.ChangesArgs) ([]plugin.Change, error) {
	h, err := g.open(ctx)
	if err != nil {
		return nil, err
	}

	return h.Changes(ctx, a)
}

func (g *gitHubHost) SetStatus(ctx context.Context, sha string, st report.CommitStatus) error {
	h, err := g.open(ctx)
	if err != nil {
		return err
	}

	return h.Status(ctx, plugin.StatusArgs{Commit: sha, Context: st.Context, State: st.State, Text: st.Description, URL: st.TargetURL})
}

func (g *gitHubHost) Statuses(ctx context.Context, sha string) ([]report.CommitStatus, error) {
	h, err := g.open(ctx)
	if err != nil {
		return nil, err
	}

	got, err := h.Statuses(ctx, sha)
	if err != nil {
		return nil, err
	}

	out := make([]report.CommitStatus, 0, len(got))
	for _, s := range got {
		out = append(out, report.CommitStatus{State: s.State, Context: s.Context, Description: s.Text, TargetURL: s.URL, UpdatedAt: s.Updated})
	}

	return out, nil
}

func (g *gitHubHost) Comment(ctx context.Context, pr int, marker, body string) error {
	h, err := g.open(ctx)
	if err != nil {
		return err
	}

	return h.Comment(ctx, plugin.CommentArgs{Change: "pr-" + strconv.Itoa(pr), Marker: marker, Body: body})
}

func (g *gitHubHost) CommitSubject(ctx context.Context, sha string) (string, error) {
	h, err := g.open(ctx)
	if err != nil {
		return "", err
	}

	return h.Subject(ctx, sha)
}

// statusesOf is a commit's newest status per context, as the run's own
// status checks read them.
func statusesOf(ctx context.Context, gh report.GitHub, sha string) (map[string]runs.Status, error) {
	all, err := gh.Statuses(ctx, sha)
	if err != nil {
		return nil, err
	}

	out := make(map[string]runs.Status, len(all))
	for _, st := range all {
		out[st.Context] = runs.Status{State: st.State, Description: st.Description}
	}

	return out, nil
}

// plannedSince finds, among a change's records, another head of it planned
// since a time: its commit, or "". refs holds at least the change's plans.
func plannedSince(refs map[string]gitstore.Ref, id report.ID) func(time.Time) (string, error) {
	return func(since time.Time) (string, error) {
		prefix := report.PlansOfChange(id.Run, id.Change)

		for name, ref := range refs {
			if commit, ok := strings.CutPrefix(name, prefix); ok && name != id.PlanRef() && ref.Time.After(since) {
				return commit, nil
			}
		}

		return "", nil
	}
}

// plannedSinceIn is plannedSince over the store's records of the change.
func plannedSinceIn(ctx context.Context, store *gitstore.Store, id report.ID) func(time.Time) (string, error) {
	return func(since time.Time) (string, error) {
		refs, err := store.List(ctx, report.PlansOfChange(id.Run, id.Change))
		if err != nil {
			return "", err
		}

		return plannedSince(refs, id)(since)
	}
}
