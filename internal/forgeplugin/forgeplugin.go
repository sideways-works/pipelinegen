// Package forgeplugin sends the requests a forge plugin answers with, as data: each checked, given the
// call's token, retried when it may pass. A forge never holds a token, nor can send one elsewhere.
package forgeplugin

import (
	"context"
	"net/http"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
	"github.com/sideways-works/plugkit/exchange"
)

// The caps on one call: rounds, requests in a round and in the whole call,
// and the size of any request or response body.
const (
	MaxRounds           = exchange.MaxRounds
	MaxRequestsPerRound = exchange.MaxRequestsPerRound
	MaxRequests         = exchange.MaxRequests
	MaxBody             = exchange.MaxBody
)

// ErrRefused is a request the core won't send, or a round it won't take:
// the call fails.
var ErrRefused = exchange.ErrRefused

const noun = plugin.KindForge

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
	return exchange.Allowed(noun, remoteOrigin, declared, confirmed)
}

// Origin is a git remote's web origin, where its host's API is: an http(s) remote keeps scheme and port, an
// ssh or scp-form one (git@host:owner/name) is https on the default port, a file remote has none.
func Origin(remote string) (string, error) { return exchange.Origin(remote) }

// Call runs call with args in rounds in the work directory work, decoding the forge's answer into answer.
func (e *Exchange) Call(ctx context.Context, call, work string, args, answer any) error {
	x := exchange.Exchange{
		Name: e.Plugin.Name, Noun: noun, Plugin: e.Plugin, Request: e.Plugin.NewRequest(""),
		Purposes: plugin.CallPurposes, Auth: e.Desc.Auth, RequestHeaders: e.Desc.RequestHeaders, ResponseHeaders: e.Desc.ResponseHeaders,
		Hosts: e.Hosts, Tokens: e.Tokens, HTTP: e.HTTP, Pause: e.pause, MaxWait: e.maxWait,
	}

	// every call is told which repository it is about
	if e.Remote != "" {
		x.Config = map[string]any{"remote": e.Remote}
	}

	return x.Call(ctx, call, work, args, answer)
}
