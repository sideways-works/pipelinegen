// Package connector sends the requests a connector plugin answers with, as data: each checked, given the
// call's token, retried when it may pass. A connector never holds a token, nor can send one elsewhere.
package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"time"

	"github.com/sideways-works/pipelinegen/internal/secret"
	"github.com/sideways-works/pipelinegen/plugin"
)

// The caps on one call: rounds, requests in a round and in the whole call,
// and the size of any request or response body.
const (
	MaxRounds           = 10
	MaxRequestsPerRound = 20
	MaxRequests         = 100
	MaxBody             = 10 << 20
)

// ErrRefused is a request the core won't send, or a round it won't take:
// the call fails.
var ErrRefused = errors.New("connector request refused")

type Exchange struct {
	Plugin plugin.Plugin
	Desc   plugin.Description
	// Hosts are the origins (scheme://host[:port]) requests may go to: the code remote's, and the API
	// hosts the config confirms and describe declares (Allowed).
	Hosts  []string
	Tokens map[string]string
	Remote string
	// HTTP sends the requests; its redirect policy is replaced by one
	// that never leaves the request's origin.
	HTTP *http.Client

	pause, maxWait time.Duration
}

// Allowed is the origins requests may go to: the code remote's, and each
// API host both describe declares and the config confirms.
func Allowed(remoteOrigin string, declared, confirmed []string) ([]string, error) {
	out := []string{}

	if remoteOrigin != "" {
		o, err := normalOrigin(remoteOrigin)
		if err != nil {
			return nil, err
		}

		out = append(out, o)
	}

	in := map[string]bool{}

	for _, h := range confirmed {
		o, err := normalOrigin(h)
		if err != nil {
			return nil, err
		}

		in[o] = true
	}

	for _, h := range declared {
		o, err := normalOrigin(h)
		if err != nil {
			return nil, fmt.Errorf("%w: describe's api host: %w", ErrRefused, err)
		}

		if in[o] {
			out = append(out, o)
		}
	}

	return out, nil
}

// Call runs call with args in rounds in the work directory work, decoding the connector's answer into answer.
func (e *Exchange) Call(ctx context.Context, call, work string, args, answer any) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("connector %s %s: args: %w", e.Plugin.Name, call, err)
	}

	if err := os.MkdirAll(work, 0o750); err != nil {
		return fmt.Errorf("connector %s %s: %w", e.Plugin.Name, call, err)
	}

	for _, t := range e.Tokens {
		secret.Add(t)
	}

	c := callState{e: e, call: call, work: work, ids: map[string]bool{}}
	req := plugin.ExchangeRequest{Request: e.Plugin.NewRequest(work), Args: raw}

	// every call is told which repository it is about
	if e.Remote != "" {
		cfg := maps.Clone(req.Config)
		if cfg == nil {
			cfg = map[string]any{}
		}

		cfg["remote"] = e.Remote
		req.Config = cfg
	}

	for round := 1; ; round++ {
		r, err := e.round(ctx, call, req)
		if err != nil {
			return err
		}

		switch {
		case len(r.Done) > 0 && len(r.Requests) > 0:
			return c.refuse("round %d answers and asks for requests at once", round)
		case len(r.Done) > 0:
			if err := json.Unmarshal(r.Done, answer); err != nil {
				return fmt.Errorf("connector %s %s: unreadable answer: %w", e.Plugin.Name, call, err)
			}

			return nil
		case len(r.Requests) == 0:
			return c.refuse("round %d neither answers nor asks for anything", round)
		case round >= MaxRounds:
			return c.refuse("it still asks for requests after %d rounds", MaxRounds)
		}

		got, err := c.round(ctx, r.Requests)
		if err != nil {
			return err
		}

		req.Responses = append(req.Responses, got...)
	}
}

func (e *Exchange) round(ctx context.Context, call string, req plugin.ExchangeRequest) (plugin.Round, error) {
	var r plugin.Round
	if err := e.Plugin.Call(ctx, call, req, &r); err != nil {
		return r, fmt.Errorf("connector %s: %w", e.Plugin.Name, err)
	}

	return r, nil
}

type callState struct {
	e    *Exchange
	call string
	work string
	ids  map[string]bool
	sent int
}

func (c *callState) refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s %s: %s", ErrRefused, c.e.Plugin.Name, c.call, fmt.Sprintf(format, args...))
}

// round checks every request of a round before it sends any, then sends
// them in order.
func (c *callState) round(ctx context.Context, reqs []plugin.HTTPRequest) ([]plugin.HTTPResponse, error) {
	if len(reqs) > MaxRequestsPerRound {
		return nil, c.refuse("%d requests in one round, at most %d", len(reqs), MaxRequestsPerRound)
	}

	if c.sent+len(reqs) > MaxRequests {
		return nil, c.refuse("more than %d requests in one call", MaxRequests)
	}

	for _, r := range reqs {
		if problem := c.problem(r); problem != "" {
			return nil, c.refuse("request %q: %s", r.ID, problem)
		}

		c.ids[r.ID] = true
	}

	out := make([]plugin.HTTPResponse, 0, len(reqs))

	for _, r := range reqs {
		c.sent++

		resp, err := c.send(ctx, r, fmt.Sprintf("response-%d.body", c.sent))
		if err != nil {
			return nil, err
		}

		out = append(out, resp)
	}

	return out, nil
}
