package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

func runPlugins(ctx context.Context, args []string, stdout io.Writer) error {
	return subcommands(ctx, args, []sub{
		{"claims", "print what each sbom and correlator plugin claims in the tree, and in which role", func(ctx context.Context, args []string) error { return pluginsClaims(ctx, args, stdout) }},
	})
}

// pluginsClaims prints `<plugin>\t<path>\t<role>` per claim, and names a plugin that claims nothing.
func pluginsClaims(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var (
		c          common
		cache, dir string
		asJSON     bool
	)

	fs := flag.NewFlagSet("plugins claims", flag.ContinueOnError)
	c.register(fs)
	fs.BoolVar(&asJSON, "json", false, "print the claims as JSON: plugin, path, role and, where set, depends, by_units and recorded")
	pathVar(fs, &cache, "cache", defaultCache(), "where the plugins' answers are kept", reads|writes)
	pathVar(fs, &dir, "plugins", "", "the directory holding the configured sbom and correlator plugins (default: tools/plugins under the root)", reads)

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	cfg, err := c.load()
	if err != nil {
		return err
	}

	root := absRoot(c.root)
	cache = repoCache(ctx, cache, root)

	if dir == "" {
		dir = filepath.Join(root, "tools", "plugins")
	}

	work, done, err := callsDir(cache)
	if err != nil {
		return err
	}
	defer done()

	set, err := plugins.Load(ctx, plugins.Options{
		Config: cfg, Dir: absOr(dir), Cache: cache, Work: work,
		Answers: &plugincache.Cache{Dir: filepath.Join(cache, "answers")},
	})
	if err != nil {
		return err
	}

	notPinned.say(set)

	claimed, err := set.Claims(ctx, root)
	if err != nil {
		return err
	}

	return writeClaims(stdout, set, claimed, asJSON)
}

type pluginClaim struct {
	plugin.Claim

	Plugin string `json:"plugin"`
}

func writeClaims(stdout io.Writer, set *plugins.Set, claimed map[string][]plugin.Claim, asJSON bool) error {
	var (
		b   bytes.Buffer
		all = []pluginClaim{}
	)

	for _, l := range set.Plugins {
		cs := claimed[l.Conf.Name]
		if len(cs) == 0 && !asJSON {
			fmt.Fprintf(&b, "%s\t-\tclaims nothing\n", l.Conf.Name)

			continue
		}

		for _, cl := range slices.SortedFunc(slices.Values(cs), func(a, b plugin.Claim) int { return strings.Compare(a.Path, b.Path) }) {
			all = append(all, pluginClaim{Plugin: l.Conf.Name, Claim: cl})

			mark := ""

			switch {
			case cl.Recorded:
				mark = "\trecorded"
			case cl.Erased:
				mark = "\terased"
			}

			fmt.Fprintf(&b, "%s\t%s\t%s%s\n", l.Conf.Name, cl.Path, cl.Role, mark)
		}
	}

	if asJSON {
		b.Reset()

		enc := json.NewEncoder(&b)
		enc.SetIndent("", "  ")

		if err := enc.Encode(all); err != nil {
			return fmt.Errorf("encode: %w", err)
		}
	}

	return emit(stdout, &b)
}
