package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/kit"
	pkit "github.com/sideways-works/plugkit/kit"
)

var errKit = errors.New("the plugin fails the protocol")

func runPlugin(ctx context.Context, args []string, stdout io.Writer) error {
	return subcommands(ctx, args, []sub{
		{"test", "check a plugin against the protocol: <executable> [--tree <dir>]", func(ctx context.Context, args []string) error { return pluginTest(ctx, args, stdout) }},
		{"add", "pin the newest release of each plugin a repository holds at a path, checked file by file: <git-url> [<path>...]", func(ctx context.Context, args []string) error { return pluginAdd(ctx, args, stdout) }},
		{"upgrade", "move the named plugins' pins, or every release pin, to their newest releases: [<name>...]", func(ctx context.Context, args []string) error { return pluginUpgrade(ctx, args, stdout) }},
		{"new", "start a plugin from the plugins repository's template of its kind: <kind> <name>", func(ctx context.Context, args []string) error { return pluginNew(ctx, args, stdout) }},
	})
}

func pluginTest(ctx context.Context, args []string, stdout io.Writer) error {
	var tree, cfgFile, cache, sbom, sbomCfgFile string

	fs := flag.NewFlagSet("plugin test", flag.ContinueOnError)
	pathVar(fs, &tree, "tree", "", "a fixture tree of the plugin's own, for sboms and correlators", reads)
	pathVar(fs, &cfgFile, "config", "", "a JSON file with the plugin's config block", readsFile)
	pathVar(fs, &cache, "cache", "", "the plugin's cache directory, already filled (a Go module cache in <dir>/gomodcache)", reads|writes)
	pathVar(fs, &sbom, "sbom", "", "an sbom plugin listing the units a correlator's links name, run beside it", readsFile)
	pathVar(fs, &sbomCfgFile, "sbom-config", "", "a JSON file with the companion sbom's config block", readsFile)

	// asked to describe itself, the command hands over its flags.
	if _, ok := ctx.Value(describeKey{}).(*[]*flag.FlagSet); ok || len(args) == 0 || strings.HasPrefix(args[0], "-") {
		if err := parse(ctx, fs, args); err != nil || ok {
			return err
		}

		return fmt.Errorf("%w: plugin test needs the plugin's executable", errUsage)
	}

	exe := args[0]

	if err := parse(ctx, fs, args[1:]); err != nil {
		return err
	}

	pluginCfg, err := readPluginConfig(cfgFile)
	if err != nil {
		return err
	}

	exePath, err := filepath.Abs(exe)
	if err != nil {
		return fmt.Errorf("plugin test: %w", err)
	}

	sbom, sbomCfg, err := companion(sbom, sbomCfgFile)
	if err != nil {
		return err
	}

	work, err := os.MkdirTemp("", "pipelinegen-kit-")
	if err != nil {
		return fmt.Errorf("plugin test: %w", err)
	}
	defer os.RemoveAll(work) //nolint:errcheck // a scratch directory

	failed, err := pkit.Print(stdout, kit.Run(ctx, kit.Options{Path: exePath, Tree: tree, Config: pluginCfg, Work: work, Cache: cache, Commands: ToolCommands(), SBOM: sbom, SBOMConfig: sbomCfg})) //nolint:contextcheck // describing the commands runs none of them
	if err != nil {
		return err
	}

	if failed > 0 {
		return fmt.Errorf("%w: %d checks failed", errKit, failed)
	}

	return nil
}

func companion(sbom, cfgFile string) (string, map[string]any, error) {
	cfg, err := readPluginConfig(cfgFile)
	if err != nil || sbom == "" {
		return "", cfg, err
	}

	abs, err := filepath.Abs(sbom)
	if err != nil {
		return "", nil, fmt.Errorf("plugin test: %w", err)
	}

	return abs, cfg, nil
}

func readPluginConfig(file string) (map[string]any, error) {
	if file == "" {
		return nil, nil //nolint:nilnil // no file is no config
	}

	data, err := os.ReadFile(file) //nolint:gosec // the file the caller names
	if err != nil {
		return nil, fmt.Errorf("plugin test: %w", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("plugin test: %s: %w", file, err)
	}

	return cfg, nil
}
