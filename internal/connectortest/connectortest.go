// Package connectortest serves tests the connectors the tool pins, the way
// the tool downloads a release.
package connectortest

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
	"github.com/sideways-works/pipelinegen/plugin"
)

// Env names the variable that holds a built binary of the connector name,
// served in place of its pinned release.
func Env(name string) string { return "PIPELINEGEN_TEST_" + strings.ToUpper(name) + "_CONNECTOR" }

// pinsEnv hands the served pins to the processes a test starts.
const pinsEnv = "PIPELINEGEN_TEST_CONNECTOR_PINS"

// Serve points the pinned connectors at a server of this process's: each
// connector whose Env names a binary is served at its release's path, and
// its pin is that binary's checksum, so a command under test downloads and
// checks it as it would the release. A connector no Env names keeps its
// pin, fetched from its release. In a process a test started, Serve takes
// the pins its parent served. stop ends the server.
func Serve() (stop func(), err error) { //nolint:nonamedreturns // what it does and how to end it
	if data := os.Getenv(pinsEnv); data != "" {
		var pins map[string]config.ConnectorPin
		if err := json.Unmarshal([]byte(data), &pins); err != nil {
			return nil, fmt.Errorf("connectortest: %s: %w", pinsEnv, err)
		}

		use(pins)

		return func() {}, nil
	}

	files := map[string][]byte{}
	pins := map[string]config.ConnectorPin{}

	for name, pin := range config.PinnedConnectors {
		path := os.Getenv(Env(name))
		if path == "" {
			if sum, ok := pin.SHA256.For(config.Platform()); pin.Sums == "" && (!ok || len(sum) != sha256.Size*2) {
				fmt.Fprintf(os.Stderr, "connectortest: the %s connector's pin has no checksum for %s: set %s to a built %s\n",
					name, config.Platform(), Env(name), plugin.Program(pin.Repository))
			}

			continue
		}

		data, err := os.ReadFile(path) //nolint:gosec // the binary the test's environment names
		if err != nil {
			return nil, fmt.Errorf("connectortest: %s: %w", Env(name), err)
		}

		sum := sha256.Sum256(data)
		pin.SHA256 = config.Checksums{config.Platform(): hex.EncodeToString(sum[:])}
		pin.Sums = ""
		files["/"+pin.Repository+"/releases/download/"+pin.Version+"/"+plugin.Program(pin.Repository)+"-"+config.Platform()] = data
		pins[name] = pin
	}

	if len(pins) == 0 {
		return func() {}, nil
	}

	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("connectortest: %w", err)
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
		pin.URL = "http://" + l.Addr().String() + "/%s/releases/download/%s/%s"
		pins[name] = pin
	}

	data, err := json.Marshal(pins)
	if err != nil {
		return nil, fmt.Errorf("connectortest: %w", err)
	}

	if err := os.Setenv(pinsEnv, string(data)); err != nil {
		return nil, fmt.Errorf("connectortest: %w", err)
	}

	use(pins)

	return func() { _ = srv.Close() }, nil //nolint:errcheck // the tests are over
}

func use(pins map[string]config.ConnectorPin) { maps.Copy(config.PinnedConnectors, pins) }
