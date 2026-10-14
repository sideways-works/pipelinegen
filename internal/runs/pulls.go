package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
)

// GitHub reads pull requests, their timelines and commit statuses.
type GitHub struct {
	API        string
	Repository string
	Token      string
	Client     *http.Client
}

var errGitHub = errors.New("github")

func (g *GitHub) get(ctx context.Context, p string, out any) error {
	api := strings.TrimRight(g.API, "/")
	if api == "" {
		api = "https://api.github.com"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/"+strings.TrimLeft(p, "/"), nil)
	if err != nil {
		return fmt.Errorf("%w: %w", errGitHub, err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")

	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}

	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: time.Minute}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", errGitHub, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s: %s", errGitHub, p, resp.Status)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: %s: %w", errGitHub, p, err)
	}

	return nil
}

func pages[T any](ctx context.Context, g *GitHub, p string) ([]T, error) {
	var all []T

	sep := "?"
	if strings.Contains(p, "?") {
		sep = "&"
	}

	for page := 1; page <= 10; page++ {
		var got []T
		if err := g.get(ctx, fmt.Sprintf("%s%sper_page=100&page=%d", p, sep, page), &got); err != nil {
			return nil, err
		}

		all = append(all, got...)

		if len(got) < 100 {
			break
		}
	}

	return all, nil
}

type pull struct {
	Number    int       `json:"number"`
	State     string    `json:"state"`
	Merged    bool      `json:"merged"`
	Draft     bool      `json:"draft"`
	CreatedAt time.Time `json:"created_at"`
	Head      struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// Admit says whether a pull request run plans this head: the head is still
// the pull request's, it passes the run's filters, and, with
// once_per_ready, no other head of it ran since it was last marked ready.
// It returns the reason when it doesn't.
func Admit(ctx context.Context, g *GitHub, cfg *config.Config, ch Change, runContext string) (bool, string, error) {
	pr := cfg.Runs[ch.Run].Trigger.PullRequests
	if pr == nil {
		return true, "", nil
	}

	var p pull
	if err := g.get(ctx, fmt.Sprintf("repos/%s/pulls/%d", g.Repository, ch.PR), &p); err != nil {
		return false, "", err
	}

	if why := filtered(p, pr, ch.Commit); why != "" {
		return false, why, nil
	}

	if !pr.OncePerReady {
		return true, "", nil
	}

	since, err := readySince(ctx, g, p)
	if err != nil {
		return false, "", err
	}

	other, err := earlierRun(ctx, g, p.Number, ch.Commit, since, runContext)
	if err != nil {
		return false, "", err
	}

	if other != "" {
		return false, fmt.Sprintf("pull request #%d ran at %s since it was last marked ready: set it to draft and ready again for a new run", p.Number, short(other)), nil
	}

	return true, "", nil
}

func filtered(p pull, pr *config.PullRequests, sha string) string {
	labels := make([]string, 0, len(p.Labels))
	for _, l := range p.Labels {
		labels = append(labels, l.Name)
	}

	switch {
	case p.State != "open" || p.Merged:
		return fmt.Sprintf("pull request #%d is closed", p.Number)
	case p.Head.SHA != sha:
		return fmt.Sprintf("pull request #%d moved on to %s", p.Number, short(p.Head.SHA))
	case pr.Draft.Wanted() != nil && *pr.Draft.Wanted() != p.Draft:
		return fmt.Sprintf("pull request #%d is %s", p.Number, map[bool]string{true: "a draft", false: "ready"}[p.Draft])
	}

	for _, l := range pr.Labels {
		if !slices.Contains(labels, l) {
			return fmt.Sprintf("pull request #%d lacks the label %s", p.Number, l)
		}
	}

	for _, l := range pr.LabelsWithout {
		if slices.Contains(labels, l) {
			return fmt.Sprintf("pull request #%d has the label %s", p.Number, l)
		}
	}

	if pr.Head != "" {
		if ok, err := path.Match(pr.Head, p.Head.Ref); err != nil || !ok {
			return fmt.Sprintf("pull request #%d's branch %s isn't %s", p.Number, p.Head.Ref, pr.Head)
		}
	}

	return ""
}

func readySince(ctx context.Context, g *GitHub, p pull) (time.Time, error) {
	type event struct {
		Event     string    `json:"event"`
		CreatedAt time.Time `json:"created_at"`
	}

	events, err := pages[event](ctx, g, fmt.Sprintf("repos/%s/issues/%d/timeline", g.Repository, p.Number))
	if err != nil {
		return time.Time{}, err
	}

	since := p.CreatedAt

	for _, e := range events {
		if e.Event == "ready_for_review" && e.CreatedAt.After(since) {
			since = e.CreatedAt
		}
	}

	return since, nil
}

func earlierRun(ctx context.Context, g *GitHub, number int, sha string, since time.Time, runContext string) (string, error) {
	type commit struct {
		SHA string `json:"sha"`
	}

	commits, err := pages[commit](ctx, g, fmt.Sprintf("repos/%s/pulls/%d/commits", g.Repository, number))
	if err != nil {
		return "", err
	}

	type status struct {
		Context   string    `json:"context"`
		CreatedAt time.Time `json:"created_at"`
	}

	// the last few heads are enough: older ones ran before the latest ready
	start := max(0, len(commits)-20)

	for _, c := range slices.Backward(commits[start:]) {
		if c.SHA == sha {
			continue
		}

		var sts []status
		if err := g.get(ctx, fmt.Sprintf("repos/%s/commits/%s/statuses?per_page=100", g.Repository, c.SHA), &sts); err != nil {
			return "", err
		}

		for _, st := range sts {
			if st.Context == runContext && st.CreatedAt.After(since) {
				return c.SHA, nil
			}
		}
	}

	return "", nil
}
