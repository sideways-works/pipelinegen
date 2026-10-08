package testenv

import (
	"os"
	"path/filepath"
	"testing"
)

// ExitChild ends a test binary that its own tests started as a child (a
// command run in a process of its own, a fake plugin) with code. In a
// coverage build a test binary writes what it ran only when its tests end,
// which such a child never reaches, so it ends its tests first, running
// none, into GOCOVERDIR or the coverage directory beside the test binary.
func ExitChild(m *testing.M, code int) {
	if dir := coverDir(); dir != "" && testing.CoverMode() != "" {
		os.Args = []string{os.Args[0], "-test.run=^$", "-test.gocoverdir=" + dir}

		if null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
			os.Stdout, os.Stderr = null, null
		}

		m.Run()
	}

	os.Exit(code)
}

func coverDir() string {
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		return dir
	}

	exe, err := os.Executable()
	if err != nil {
		return ""
	}

	dir := filepath.Join(filepath.Dir(exe), "gocoverdir")
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}

	return ""
}
