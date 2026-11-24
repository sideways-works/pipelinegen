package cli

import (
	"errors"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestARunnerThatCantHandTheStoresKeysIsRefused(t *testing.T) {
	cfg := &config.Config{
		Plugins: []config.Plugin{{Name: "s3", Kind: config.KindStore}},
		Store:   config.Store{Backend: config.StorePlugin, Plugin: "s3"},
	}

	if err := runnerHandsStoreKeys(cfg, "old", plugin.Description{Version: "v1"}); !errors.Is(err, errRunner) {
		t.Fatalf("a runner without %s: %v", plugin.CapStoreKeys, err)
	}

	if err := runnerHandsStoreKeys(cfg, "new", plugin.Description{Capabilities: []string{plugin.CapStoreKeys}}); err != nil {
		t.Fatalf("a runner with it: %v", err)
	}

	if err := runnerHandsStoreKeys(&config.Config{}, "old", plugin.Description{}); err != nil {
		t.Fatalf("the records in git: %v", err)
	}
}
