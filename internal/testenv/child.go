package testenv

import (
	"os"
	"path/filepath"
	"testing"
)

// ExitChild ends a child the test binary started of itself: in a coverage build a test binary
// writes its coverage only when its tests end, so it ends them first, running none.
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
