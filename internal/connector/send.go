package connector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

const tries = 4

const maxRedirects = 5

var errRedirect = errors.New("redirect leaves the request's origin")

func (c *callState) send(ctx context.Context, r plugin.HTTPRequest, name string) (plugin.HTTPResponse, error) {
	var (
		last *attempt
		err  error
		wait time.Duration
	)

	for try := range tries {
		if try > 0 {
			pause := max(time.Duration(try*try)*c.e.firstPause(), wait)

			select {
			case <-ctx.Done():
				return plugin.HTTPResponse{}, fmt.Errorf("connector %s %s: request %q: %w", c.e.Plugin.Name, c.call, r.ID, ctx.Err())
			case <-time.After(pause):
			}
		}

		last, err = c.once(ctx, r)
		if errors.Is(err, ErrRefused) {
			return plugin.HTTPResponse{}, err
		}

		if err != nil {
			continue
		}

		var retry bool
		if retry, wait = c.e.retryable(last); !retry {
			break
		}
	}

	if err != nil {
		// a request that never got an answer fails the call: the
		// connector can't tell a lost write from a refused one
		return plugin.HTTPResponse{}, fmt.Errorf("connector %s %s: request %q: %w", c.e.Plugin.Name, c.call, r.ID, err)
	}

	if err := os.WriteFile(filepath.Join(c.work, name), last.body, 0o600); err != nil {
		return plugin.HTTPResponse{}, fmt.Errorf("connector %s %s: %w", c.e.Plugin.Name, c.call, err)
	}

	return plugin.HTTPResponse{ID: r.ID, Status: last.status, Headers: c.e.keptHeaders(last.header), BodyFile: name}, nil
}

type attempt struct {
	status int
	header http.Header
	body   []byte
}

// once sends r one time. A body over MaxBody is refused, not cut short: a
// connector reading half a page would answer as if it had it all.
func (c *callState) once(ctx context.Context, r plugin.HTTPRequest) (*attempt, error) {
	var body io.Reader
	if r.Body != "" {
		body = strings.NewReader(r.Body)
	}

	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, body)
	if err != nil {
		return nil, fmt.Errorf("%w: %s %s: request %q: %w", ErrRefused, c.e.Plugin.Name, c.call, r.ID, err)
	}

	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}

	if r.Auth != nil {
		a := c.e.Desc.Auth

		value := c.e.Tokens[r.Auth.Purpose]
		if a.Scheme != "" {
			value = a.Scheme + " " + value
		}

		req.Header.Set(a.Header, value)
	}

	resp, err := c.e.client().Do(req)
	if err != nil {
		if errors.Is(err, errRedirect) {
			return nil, fmt.Errorf("%w: %s %s: request %q: %w", ErrRefused, c.e.Plugin.Name, c.call, r.ID, errRedirect)
		}

		return nil, fmt.Errorf("%s %s: %w", r.Method, req.URL.Redacted(), unwrapURL(err))
	}

	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", r.Method, req.URL.Redacted(), err)
	}

	if len(data) > MaxBody {
		return nil, fmt.Errorf("%w: %s %s: request %q: the response's body is over %d bytes", ErrRefused, c.e.Plugin.Name, c.call, r.ID, MaxBody)
	}

	return &attempt{status: resp.StatusCode, header: resp.Header, body: data}, nil
}

func unwrapURL(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return ue.Err
	}

	return err
}

// client is the exchange's HTTP client whose redirects never leave the request's origin: the token may sit
// in any header a host names, and Go's client keeps a custom header on a redirect to another host.
func (e *Exchange) client() *http.Client {
	hc := http.Client{Timeout: 30 * time.Second}
	if e.HTTP != nil {
		hc = *e.HTTP
	}

	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("%w: more than %d redirects", errRedirect, maxRedirects)
		}

		from, err := normalOrigin(via[0].URL.Scheme + "://" + via[0].URL.Host)
		if err != nil {
			return errRedirect
		}

		to, err := normalOrigin(req.URL.Scheme + "://" + req.URL.Host)
		if err != nil || to != from {
			return errRedirect
		}

		return nil
	}

	return &hc
}

// retryable says whether an answer may pass on another try, and how long the host asked to wait: its own
// errors, and its rate limits (429, or a 403 that says so).
func (e *Exchange) retryable(a *attempt) (bool, time.Duration) {
	if a.status >= 500 {
		return true, e.retryAfter(a.header)
	}

	if a.status != http.StatusTooManyRequests && a.status != http.StatusForbidden {
		return false, 0
	}

	wait := e.retryAfter(a.header)
	limited := wait > 0 || a.status == http.StatusTooManyRequests ||
		a.header.Get("X-Ratelimit-Remaining") == "0" || a.header.Get("Ratelimit-Remaining") == "0" ||
		strings.Contains(strings.ToLower(string(a.body[:min(len(a.body), 1000)])), "rate limit")

	if limited && wait == 0 {
		wait = min(time.Minute, e.waitCap())
	}

	return limited, wait
}

func (e *Exchange) retryAfter(h http.Header) time.Duration {
	n, err := strconv.Atoi(h.Get("Retry-After"))
	if err != nil || n <= 0 {
		return 0
	}

	return min(time.Duration(n)*time.Second, e.waitCap())
}

func (e *Exchange) firstPause() time.Duration {
	if e.pause > 0 {
		return e.pause
	}

	return time.Second
}

func (e *Exchange) waitCap() time.Duration {
	if e.maxWait > 0 {
		return e.maxWait
	}

	return 90 * time.Second
}

func (e *Exchange) keptHeaders(h http.Header) map[string]string {
	out := map[string]string{}

	for _, name := range e.Desc.ResponseHeaders {
		canon := textproto.CanonicalMIMEHeaderKey(name)
		if v := h.Values(canon); len(v) > 0 {
			out[canon] = strings.Join(v, ", ")
		}
	}

	if len(out) == 0 {
		return nil
	}

	return out
}
