package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHub is the part of GitHub's API a run reports through.
type GitHub interface {
	// SetStatus sets one commit status.
	SetStatus(ctx context.Context, sha string, st CommitStatus) error
	// Statuses lists a commit's newest status per context.
	Statuses(ctx context.Context, sha string) ([]CommitStatus, error)
	// Comments lists a pull request's comments, oldest first.
	Comments(ctx context.Context, pr int) ([]IssueComment, error)
	CreateComment(ctx context.Context, pr int, body string) error
	UpdateComment(ctx context.Context, id int64, body string) error
	DeleteComment(ctx context.Context, id int64) error
	// CommitSubject is the first line of a commit's message.
	CommitSubject(ctx context.Context, sha string) (string, error)
}

// CommitStatus is one status on a commit.
type CommitStatus struct {
	State       string     `json:"state"`
	Context     string     `json:"context"`
	Description string     `json:"description"`
	TargetURL   string     `json:"target_url,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
}

// IssueComment is a pull request comment.
type IssueComment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Client is GitHub's REST API for one repository.
type Client struct {
	API        string
	Repository string
	// Token authenticates; it goes in a header only.
	Token string
	HTTP  *http.Client
	pause time.Duration
}

// ErrGitHub wraps an answer from GitHub that isn't a success.
var ErrGitHub = errors.New("github")

const (
	defaultAPI   = "https://api.github.com"
	apiTries     = 4
	commentsPage = 100
)

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload []byte

	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("encode %s %s: %w", method, path, err)
		}
	}

	api := c.API
	if api == "" {
		api = defaultAPI
	}

	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}

	var (
		last error
		wait time.Duration
	)

	for try := range apiTries {
		if try > 0 {
			pause := max(time.Duration(try*try)*c.retryPause(), wait)

			select {
			case <-ctx.Done():
				return fmt.Errorf("%s %s: %w", method, path, ctx.Err())
			case <-time.After(pause):
			}
		}

		var (
			retry bool
			err   error
		)

		retry, wait, err = c.once(ctx, hc, method, strings.TrimRight(api, "/")+path, payload, out)
		if err == nil || !retry {
			return err
		}

		last = err
	}

	return last
}

const maxRetryAfter = 90 * time.Second

// rateLimited tells whether a refusal is one of GitHub's rate limits, and
// how long to wait: secondary limits answer 403 or 429 with Retry-After or a
// message, the primary one with no requests remaining.
func rateLimited(resp *http.Response, body string) (bool, time.Duration) {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return false, 0
	}

	wait := time.Duration(0)

	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			wait = time.Duration(n) * time.Second
		}
	}

	limited := wait > 0 || resp.StatusCode == http.StatusTooManyRequests ||
		resp.Header.Get("X-Ratelimit-Remaining") == "0" || strings.Contains(strings.ToLower(body), "rate limit")
	if limited && wait == 0 {
		wait = time.Minute
	}

	return limited, min(wait, maxRetryAfter)
}

func (c *Client) retryPause() time.Duration {
	if c.pause > 0 {
		return c.pause
	}

	return time.Second
}

func (c *Client) once(ctx context.Context, hc *http.Client, method, u string, payload []byte, out any) (bool, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
	if err != nil {
		return false, 0, fmt.Errorf("%w: %s: %w", ErrGitHub, method, err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-Github-Api-Version", "2022-11-28")

	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return true, 0, fmt.Errorf("%w: %s %s: %w", ErrGitHub, method, req.URL.Path, err)
	}

	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return true, 0, fmt.Errorf("%w: %s %s: %w", ErrGitHub, method, req.URL.Path, err)
	}

	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 300 {
			msg = msg[:300]
		}

		limited, wait := rateLimited(resp, msg)

		return limited || resp.StatusCode >= 500, wait, fmt.Errorf("%w: %s %s: %s: %s", ErrGitHub, method, req.URL.Path, resp.Status, msg)
	}

	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return false, 0, fmt.Errorf("%w: %s %s: %w", ErrGitHub, method, req.URL.Path, err)
		}
	}

	return false, 0, nil
}

func (c *Client) repo() string {
	owner, name, _ := strings.Cut(c.Repository, "/")

	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

// SetStatus sets one commit status.
func (c *Client) SetStatus(ctx context.Context, sha string, st CommitStatus) error {
	return c.do(ctx, http.MethodPost, c.repo()+"/statuses/"+url.PathEscape(sha), st, nil)
}

// Statuses lists a commit's newest status per context.
func (c *Client) Statuses(ctx context.Context, sha string) ([]CommitStatus, error) {
	var all []CommitStatus

	for page := 1; ; page++ {
		var combined struct {
			Statuses []CommitStatus `json:"statuses"`
		}

		path := fmt.Sprintf("%s/commits/%s/status?per_page=%d&page=%d", c.repo(), url.PathEscape(sha), commentsPage, page)
		if err := c.do(ctx, http.MethodGet, path, nil, &combined); err != nil {
			return nil, err
		}

		all = append(all, combined.Statuses...)

		if len(combined.Statuses) < commentsPage {
			return all, nil
		}
	}
}

// CommitSubject is the first line of a commit's message.
func (c *Client) CommitSubject(ctx context.Context, sha string) (string, error) {
	var commit struct {
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}

	if err := c.do(ctx, http.MethodGet, c.repo()+"/commits/"+url.PathEscape(sha), nil, &commit); err != nil {
		return "", err
	}

	subject, _, _ := strings.Cut(commit.Commit.Message, "\n")

	return strings.TrimSpace(subject), nil
}

// Comments lists a pull request's comments, every page.
func (c *Client) Comments(ctx context.Context, pr int) ([]IssueComment, error) {
	var all []IssueComment

	for page := 1; ; page++ {
		var batch []IssueComment

		path := fmt.Sprintf("%s/issues/%d/comments?per_page=%d&page=%d", c.repo(), pr, commentsPage, page)
		if err := c.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}

		all = append(all, batch...)

		if len(batch) < commentsPage {
			return all, nil
		}
	}
}

// CreateComment adds a comment to a pull request.
func (c *Client) CreateComment(ctx context.Context, pr int, body string) error {
	return c.do(ctx, http.MethodPost, c.repo()+"/issues/"+strconv.Itoa(pr)+"/comments", map[string]string{"body": body}, nil)
}

// UpdateComment replaces a comment's body.
func (c *Client) UpdateComment(ctx context.Context, id int64, body string) error {
	return c.do(ctx, http.MethodPatch, c.repo()+"/issues/comments/"+strconv.FormatInt(id, 10), map[string]string{"body": body}, nil)
}

// DeleteComment removes a comment.
func (c *Client) DeleteComment(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, c.repo()+"/issues/comments/"+strconv.FormatInt(id, 10), nil, nil)
}
