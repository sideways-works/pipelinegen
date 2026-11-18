package connector

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/sideways-works/pipelinegen/plugin"
	"github.com/sideways-works/plugkit/exchange"
)

// Host is one connector ready to call: its binary described, the origins
// its requests may go to and the tokens they may carry.
type Host struct {
	ex   *Exchange
	work string
}

// Options open a Host. Everything in them comes from the trusted config and
// the pipeline's environment, never from a proposed change.
type Options struct {
	Name, Path string
	Config     map[string]any
	Remote     string
	// API is the root of the host's API the deployment names; its origin
	// takes requests whether or not describe declares it.
	API    string
	Tokens map[string]string
	// Work holds each call's files, in a directory of its own only this user can open, removed when it ends:
	// the responses in it decide what the connector answers.
	Work string
	HTTP *http.Client
}

func Open(ctx context.Context, o Options) (*Host, error) {
	if err := os.MkdirAll(o.Work, 0o750); err != nil {
		return nil, fmt.Errorf("connector %s: %w", o.Name, err)
	}

	p := plugin.Plugin{Name: o.Name, Path: o.Path, Config: o.Config}

	work, err := os.MkdirTemp(o.Work, "pipelinegen-connector-describe-*")
	if err != nil {
		return nil, fmt.Errorf("connector %s: %w", o.Name, err)
	}
	defer os.RemoveAll(work) //nolint:errcheck // scratch

	desc, err := p.Describe(ctx, work)
	if err != nil {
		return nil, fmt.Errorf("connector %s: %w", o.Name, err)
	}

	if desc.Kind != plugin.KindConnector {
		return nil, fmt.Errorf("%w: %s says it is a %s, not a connector", ErrRefused, o.Name, desc.Kind)
	}

	outright, confirmed := "", []string{}
	if o.API != "" {
		if outright, err = exchange.NormalOrigin(o.API); err != nil {
			return nil, fmt.Errorf("connector %s: api: %w", o.Name, err)
		}

		confirmed = append(confirmed, outright)
	}

	hosts, err := Allowed(outright, desc.APIHosts, confirmed)
	if err != nil {
		return nil, fmt.Errorf("connector %s: %w", o.Name, err)
	}

	return &Host{
		ex:   &Exchange{Plugin: p, Desc: desc, Hosts: hosts, Tokens: o.Tokens, Remote: o.Remote, HTTP: o.HTTP},
		work: o.Work,
	}, nil
}

func (h *Host) Description() plugin.Description { return h.ex.Desc }

func (h *Host) call(ctx context.Context, call string, args, answer any) error {
	work, err := os.MkdirTemp(h.work, "pipelinegen-connector-"+call+"-*")
	if err != nil {
		return fmt.Errorf("connector %s %s: %w", h.ex.Plugin.Name, call, err)
	}
	defer os.RemoveAll(work) //nolint:errcheck // scratch

	return h.ex.Call(ctx, call, work, args, answer)
}

func (h *Host) Changes(ctx context.Context, a plugin.ChangesArgs) ([]plugin.Change, error) {
	var out plugin.ChangesAnswer
	if err := h.call(ctx, plugin.CallChanges, a, &out); err != nil {
		return nil, err
	}

	return out.Changes, nil
}

func (h *Host) Status(ctx context.Context, a plugin.StatusArgs) error {
	var out map[string]any

	return h.call(ctx, plugin.CallStatus, a, &out)
}

func (h *Host) Statuses(ctx context.Context, commit string) ([]plugin.Status, error) {
	var out plugin.StatusesAnswer
	if err := h.call(ctx, plugin.CallStatuses, plugin.StatusesArgs{Commit: commit}, &out); err != nil {
		return nil, err
	}

	return out.Statuses, nil
}

func (h *Host) Subject(ctx context.Context, commit string) (string, error) {
	var out plugin.SubjectAnswer
	if err := h.call(ctx, plugin.CallSubject, plugin.SubjectArgs{Commit: commit}, &out); err != nil {
		return "", err
	}

	return out.Subject, nil
}

// Comment keeps one comment, found by its marker, on a change.
func (h *Host) Comment(ctx context.Context, a plugin.CommentArgs) error {
	var out map[string]any

	return h.call(ctx, plugin.CallComment, a, &out)
}
