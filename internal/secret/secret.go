// Package secret keeps secrets out of what pipelinegen prints.
package secret

import (
	"os"
	"slices"
	"strings"

	"github.com/sideways-works/plugkit/plugin"
	"github.com/sideways-works/plugkit/redact"
)

const Mask = redact.Mask

// Add records a secret value and the forms it travels in: base64, and an HTTP Basic credential
// with it as the password.
func Add(value string) { redact.Add(value) }

func Redact(text string) string { return redact.Redact(text) }

// GitEnv is the environment for git: no GIT_ variable (GIT_TRACE_CURL on a worker would print the
// Authorization header), no prompts, plus extra, hardened.
func GitEnv(extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra)+1)

	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}

	return HardenGit(append(append(env, "GIT_TERMINAL_PROMPT=0"), extra...))
}

// FillEnv fills a plugin's cache on a person's machine: their environment, so their Go and git
// settings hold, less every secret-like variable and pipelinegen's own, plus extra.
func FillEnv(extra map[string]string) []string {
	var env []string

	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if _, over := extra[name]; over || redact.SecretName(name) || strings.HasPrefix(strings.ToUpper(name), "PIPELINEGEN_") || name == "GIT_SSH_COMMAND" {
			continue
		}

		env = append(env, kv)
	}

	for _, name := range sortedKeys(extra) {
		env = append(env, name+"="+extra[name])
	}

	return env
}

// toolchainVars are what a plugin keeps of where its toolchain is, never its caches (describe
// declares those), credentials or network settings.
var toolchainVars = []string{"GOROOT", "GOTOOLCHAIN", "GOOS", "GOARCH"}

// PluginEnv is a plugin's whole environment: basics, locale, the Go toolchain's place, its
// declared caches, Go off the network; never a token, proxy, inherited cache or our settings.
func PluginEnv(caches map[string]string) []string {
	// a plugin reads files; Go must not reach for a module it lacks
	return append(plugin.Env(toolchainVars, caches), "GOPROXY=off")
}

// CacheVars maps the cache names a plugin declares to the variables their toolchain reads.
var CacheVars = map[string]string{
	"gocache":    "GOCACHE",
	"gomodcache": "GOMODCACHE",
	"npm":        "npm_config_cache",
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}
