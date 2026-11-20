// Package forgeplugintest serves tests the forges the tool pins, the way the tool downloads a
// release.
package forgeplugintest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	plugkitpin "github.com/sideways-works/plugkit/pin"
)

// Env names the variable that holds a built binary of the forge, served in place of its
// pinned release.
func Env(name string) string { return "PIPELINEGEN_TEST_" + strings.ToUpper(name) + "_FORGE" }

// servedPath is where a release's file is served: the repository, the release's tag and the
// file's name fill it, as they fill a pin's URL.
const servedPath = "/%s/releases/download/%s/%s"

// pinsEnv hands the served pins to the processes a test starts.
const pinsEnv = "PIPELINEGEN_TEST_FORGE_PINS"

// Serve serves each forge whose Env names a binary at its release's path, pinned by that
// binary's checksum, others keeping their pin; a child process takes the pins its parent served.
func Serve() (stop func(), err error) { //nolint:nonamedreturns // what it does and how to end it
	if data := os.Getenv(pinsEnv); data != "" {
		var pins map[string]config.ForgePin
		if err := json.Unmarshal([]byte(data), &pins); err != nil {
			return nil, fmt.Errorf("forgetest: %s: %w", pinsEnv, err)
		}

		use(pins)

		return func() {}, nil
	}

	files := map[string][]byte{}
	pins := map[string]config.ForgePin{}

	for name, pin := range config.PinnedForges {
		path := os.Getenv(Env(name))
		if path == "" {
			if sum, ok := pin.SHA256.For(config.Platform()); pin.Sums == "" && (!ok || len(sum) != sha256.Size*2) {
				fmt.Fprintf(os.Stderr, "forgetest: the %s forge's pin has no checksum for %s: set %s to a built %s\n",
					name, config.Platform(), Env(name), pin.Program())
			}

			continue
		}

		data, err := os.ReadFile(path) //nolint:gosec // the binary the test's environment names
		if err != nil {
			return nil, fmt.Errorf("forgetest: %s: %w", Env(name), err)
		}

		sum := sha256.Sum256(data)
		pin.SHA256 = config.Checksums{config.Platform(): hex.EncodeToString(sum[:])}
		pin.Sums = ""
		files[fmt.Sprintf(servedPath, pin.Repository, plugkitpin.Pin{Path: pin.Path, Version: pin.Version}.Tag(), pin.Program()+"-"+config.Platform())] = data
		pins[name] = pin
	}

	if len(pins) == 0 {
		return func() {}, nil
	}

	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("forgetest: %w", err)
	}

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:gosec // a test's own server
		data, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)

			return
		}

		_, _ = w.Write(data) //nolint:errcheck // a test's own server
	})}

	go srv.Serve(l) //nolint:errcheck // ends with the test binary

	for name, pin := range pins {
		pin.URL = "http://" + l.Addr().String() + servedPath
		pins[name] = pin
	}

	data, err := json.Marshal(pins)
	if err != nil {
		return nil, fmt.Errorf("forgetest: %w", err)
	}

	if err := os.Setenv(pinsEnv, string(data)); err != nil {
		return nil, fmt.Errorf("forgetest: %w", err)
	}

	use(pins)

	return func() { _ = srv.Close() }, nil //nolint:errcheck // the tests are over
}

func use(pins map[string]config.ForgePin) { maps.Copy(config.PinnedForges, pins) }
