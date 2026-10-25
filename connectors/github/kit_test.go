package github_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/connectors/github"
	"github.com/sideways-works/pipelinegen/internal/kit"
	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/plugin"
)

const serveEnv = "GITHUB_CONNECTOR_SERVE"

func TestMain(m *testing.M) {
	if os.Getenv(serveEnv) != "" {
		testenv.ExitChild(m, plugin.Serve(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, handlers()))
	}

	os.Exit(m.Run())
}

func handlers() map[string]plugin.Handler {
	h := map[string]plugin.Handler{
		plugin.CallDescribe: func([]byte) (any, error) { return github.Describe("test"), nil },
	}

	for _, call := range plugin.ConnectorCalls {
		h[call] = func(in []byte) (any, error) {
			var req plugin.ExchangeRequest
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, fmt.Errorf("request: %w", err)
			}

			return github.Round(call, req, func(file string) ([]byte, error) {
				if !filepath.IsLocal(file) {
					return nil, fmt.Errorf("%w: body file %q outside the work directory", github.ErrGitHub, file)
				}

				return os.ReadFile(filepath.Join(req.Work, file))
			})
		}
	}

	return h
}

func TestTheBuiltinConnectorPassesTheKit(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	self := filepath.Join(t.TempDir(), "github-connector")
	if err := testenv.WriteExecutable(t.Context(), self, []byte("#!/bin/sh\n"+serveEnv+"=1 exec '"+exe+"' \"$@\"\n")); err != nil {
		t.Fatal(err)
	}

	results := kit.Run(t.Context(), kit.Options{Path: self, Tree: "testdata/kit", Work: t.TempDir()})

	var failed []string

	fork := false

	for _, r := range results {
		if r.Problem != "" && !r.Skipped && !r.Note {
			failed = append(failed, r.Check+": "+r.Problem)
		}

		if strings.Contains(r.Check, "fork") && !r.Skipped {
			fork = true
		}
	}

	if len(failed) > 0 || !fork {
		t.Fatalf("the kit: fork check ran %v; failed %v", fork, failed)
	}
}
