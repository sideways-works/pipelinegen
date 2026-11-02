package cli

import (
	"bytes"
	"testing"
)

func TestVersionPrintsTheVersion(t *testing.T) {
	var out, errs bytes.Buffer

	if code := Run(t.Context(), []string{"version"}, &out, &errs); code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}

	if out.String() != Version+"\n" {
		t.Errorf("printed %q", out.String())
	}
}

func TestPluginTestHelp(t *testing.T) {
	for _, args := range [][]string{{"plugin", "test", "--help"}, {"plugin", "--help"}} {
		var out, errs bytes.Buffer

		if code := Run(t.Context(), args, &out, &errs); code == 1 {
			t.Errorf("%v ran as a plugin: %s", args, errs.String())
		}
	}
}
