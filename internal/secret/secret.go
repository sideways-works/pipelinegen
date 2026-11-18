// Package secret keeps secrets out of what pipelinegen prints.
package secret

import (
	"encoding/base64"
	"os"
	"slices"
	"strings"
	"sync"
)

const Mask = "***"

// minLen is the shortest value worth redacting: shorter ones would mask ordinary words.
const minLen = 8

var (
	mu     sync.RWMutex
	values []string
	once   sync.Once
)

func secretName(name string) bool {
	name = strings.ToUpper(name)

	for _, suffix := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "_KEY", "CREDENTIALS"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}

	return false
}

func load() {
	once.Do(func() {
		for _, kv := range os.Environ() {
			name, value, _ := strings.Cut(kv, "=")
			if secretName(name) {
				add(value)
			}
		}
	})
}

// Add records a secret value and the forms it travels in: base64, and an HTTP Basic credential
// with it as the password.
func Add(value string) {
	load()
	add(value)
}

func add(value string) {
	if len(value) < minLen {
		return
	}

	mu.Lock()
	defer mu.Unlock()

	for _, v := range []string{
		value,
		base64.StdEncoding.EncodeToString([]byte(value)),
		base64.StdEncoding.EncodeToString([]byte("x-access-token:" + value)),
	} {
		if !slices.Contains(values, v) {
			values = append(values, v)
		}
	}

	// longest first, so a value inside another is masked as part of it
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
}

func Redact(text string) string {
	load()

	mu.RLock()
	defer mu.RUnlock()

	for _, v := range values {
		text = strings.ReplaceAll(text, v, Mask)
	}

	return text
}

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
		if _, over := extra[name]; over || secretName(name) || strings.HasPrefix(strings.ToUpper(name), "PIPELINEGEN_") || name == "GIT_SSH_COMMAND" {
			continue
		}

		env = append(env, kv)
	}

	for _, name := range sortedKeys(extra) {
		env = append(env, name+"="+extra[name])
	}

	return env
}

var pluginVars = []string{"PATH", "HOME", "USER", "LANG", "LANGUAGE", "TZ", "TMPDIR"}

// toolchainVars are what a plugin keeps of where its toolchain is, never its caches (describe
// declares those), credentials or network settings.
var toolchainVars = []string{"GOROOT", "GOTOOLCHAIN", "GOOS", "GOARCH"}

// PluginEnv is a plugin's whole environment: basics, locale, the Go toolchain's place, its
// declared caches, Go off the network; never a token, proxy, inherited cache or our settings.
func PluginEnv(caches map[string]string) []string {
	var env []string

	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")

		if slices.Contains(pluginVars, name) || slices.Contains(toolchainVars, name) || strings.HasPrefix(name, "LC_") {
			env = append(env, kv)
		}
	}

	for _, name := range sortedKeys(caches) {
		env = append(env, name+"="+caches[name])
	}

	// a plugin reads files; Go must not reach for a module it lacks
	return append(env, "GOPROXY=off")
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
