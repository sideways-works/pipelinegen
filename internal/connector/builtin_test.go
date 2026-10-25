package connector

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sideways-works/pipelinegen/connectors/github"
	"github.com/sideways-works/pipelinegen/plugin"
)

func githubBuiltin(call string, req plugin.ExchangeRequest) (plugin.Round, error) {
	return github.Round(call, req, func(file string) ([]byte, error) { return os.ReadFile(filepath.Join(req.Work, file)) })
}

func TestABuiltinConnectorGoesThroughTheExchange(t *testing.T) {
	t.Parallel()

	h := newHost(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		if r.Header.Get("Authorization") != "Bearer "+changesToken {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		write(w, `[{"number": 7, "head": {"sha": "1111111111111111111111111111111111111111", "ref": "b", "repo": {"id": 1}}, "base": {"ref": "main", "repo": {"id": 1}}}]`)
	})

	e := exchange(t, h)
	e.Builtin = githubBuiltin
	e.Desc = github.Describe("builtin")
	e.Plugin = plugin.Plugin{Name: "github", Config: map[string]any{"api": h.URL}}
	e.Remote = "https://github.com/o/r.git"

	var a plugin.ChangesAnswer
	if err := e.Call(t.Context(), plugin.CallChanges, t.TempDir(), plugin.ChangesArgs{Base: "main"}, &a); err != nil {
		t.Fatal(err)
	}

	if len(a.Changes) != 1 || a.Changes[0].ID != "pr-7" || a.Changes[0].From != plugin.FromSame {
		t.Fatalf("%+v", a)
	}

	other := newHost(t, nil)
	e.Plugin.Config = map[string]any{"api": other.URL}

	if err := e.Call(t.Context(), plugin.CallChanges, t.TempDir(), plugin.ChangesArgs{Base: "main"}, &a); !errors.Is(err, ErrRefused) || other.hits() != 0 {
		t.Fatalf("another host: %v, %d hits", err, other.hits())
	}
}
