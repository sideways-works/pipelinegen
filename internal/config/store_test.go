package config

import (
	"strings"
	"testing"
)

const storePlugin = `
plugins:
  - {name: s3, kind: store, repository: https://example.test/plugins, path: store/s3, release: {version: v0.1.0, sha256: 0000000000000000000000000000000000000000000000000000000000000000}, config: {bucket: b}}
`

func TestAStorePluginNeedsItsKeys(t *testing.T) {
	write := "  credentials: {write: {access_key_id: store_key, secret_access_key: store_secret}}\n"
	read := "  credentials: {write: {access_key_id: store_key, secret_access_key: store_secret}, read: {access_key_id: read_key, secret_access_key: read_secret}}\n"

	for _, c := range []struct {
		name, store, want string
	}{
		{"no plugin entry", "store:\n  backend: plugin\n  plugin: nope\n" + write, "needs store.plugin"},
		{"no writing key", "store: {backend: plugin, plugin: s3}\n", "store.credentials.write"},
		{"no reading key with a run of pull requests", "store:\n  backend: plugin\n  plugin: s3\n" + write, "store.credentials.read"},
		{"a key that is no var", "store:\n  backend: plugin\n  plugin: s3\n  credentials: {write: {access_key_id: \"((x))\", secret_access_key: s}, read: {access_key_id: r, secret_access_key: s}}\n", "each the name of a CI var"},
		{"a plugin for the refs", "store: {backend: refs, plugin: s3}\n", "store.plugin names the store plugin"},
		{"keys and a plugin", "store:\n  backend: plugin\n  plugin: s3\n" + read, ""},
	} {
		_, err := Parse([]byte(withRuns + storePlugin + c.store))

		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
	}
}

func TestTheStorePluginIsItsEntry(t *testing.T) {
	c, err := Parse([]byte(withRuns + storePlugin + "store:\n  backend: plugin\n  plugin: s3\n  credentials: {write: {access_key_id: k, secret_access_key: s}, read: {access_key_id: r, secret_access_key: t}}\n"))
	if err != nil {
		t.Fatal(err)
	}

	p, ok := c.StorePlugin()
	if !ok || p.Name != "s3" || p.Kind != KindStore || p.Config["bucket"] != "b" {
		t.Fatalf("store plugin %+v %v", p, ok)
	}
}
