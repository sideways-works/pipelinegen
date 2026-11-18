package packages

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
)

type Present func(name string) bool

// Missing are the packages of ps that present finds under none of their names: steps on the
// machine itself install nothing, so a person is told what to install.
func Missing(ps config.Packages, present Present) []string {
	var out []string

	for _, p := range ps.Sorted() {
		spellings := make([]string, 0, 1+len(config.Families))
		spellings = append(spellings, p.Name)

		for _, f := range config.Families {
			spellings = append(spellings, p.By[f])
		}

		if !slices.ContainsFunc(spellings, func(n string) bool { return n != "" && present(n) }) {
			out = append(out, names(config.Packages{p})...)
		}
	}

	return out
}

// hostQueries ask the machine's package database whether a package is installed; a machine
// without one of them skips it.
var hostQueries = [][]string{
	{"dpkg-query", "-W", "-f", "${Status}"},
	{"rpm", "-q"},
	{"apk", "info", "-e"},
	{"pacman", "-Q"},
	{"brew", "list", "--versions"},
}

// OnHost finds a package on this machine as a command on PATH, or in its package database.
func OnHost(ctx context.Context) Present {
	return func(name string) bool {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}

		for _, q := range hostQueries {
			if _, err := exec.LookPath(q[0]); err != nil {
				continue
			}

			if installed(ctx, q, name) {
				return true
			}
		}

		return false
	}
}

func installed(ctx context.Context, q []string, name string) bool {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, q[0], append(slices.Clone(q[1:]), name)...).Output() //nolint:gosec // the machine's package database, asked about a declared package
	if err != nil {
		return false
	}

	// dpkg knows a removed package, whose configuration stays
	if q[0] == "dpkg-query" {
		return strings.HasSuffix(strings.TrimSpace(string(out)), " ok installed")
	}

	// brew prints nothing for a formula it doesn't have
	return q[0] != "brew" || strings.TrimSpace(string(out)) != ""
}
