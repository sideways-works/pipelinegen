package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/secret"
)

func TestAPipelineStepSaysWhereItsTimeWent(t *testing.T) {
	quiet := func(context.Context, []string, io.Writer) error { return nil }

	for _, tc := range []struct {
		name     string
		args     []string
		pipeline bool
		want     string
	}{
		{"tree", []string{"--run", "main"}, true, "pipelinegen tree: took "},
		{"plan", []string{"--decide"}, true, "pipelinegen plan: took "},
		{reportCommand, []string{"batch-end", "--batch", "0"}, true, "pipelinegen report batch-end: took "},
		{reportCommand, []string{"suite"}, true, "pipelinegen report suite: took "},
		// a batch's own calls, once per slice, and every step on a machine of one's own say nothing
		{reportCommand, []string{"slice", "x"}, true, ""},
		{"tree", []string{"--run", "main"}, false, ""},
	} {
		t.Setenv(envJobURL, "")
		t.Setenv(secret.PipelineStepVar, "")

		if tc.pipeline {
			t.Setenv(secret.PipelineStepVar, "1")
		}

		said := stderrOf(t, func() {
			if err := metered(tc.name, quiet)(t.Context(), tc.args, io.Discard); err != nil {
				t.Error(err)
			}
		})

		if tc.want == "" {
			if said != "" {
				t.Errorf("%s %v: said %q", tc.name, tc.args, said)
			}

			continue
		}

		if !strings.HasPrefix(said, tc.want) || !strings.Contains(said, "; git reached a remote 0 times in 0.0s\n") {
			t.Errorf("%s %v: said %q, want it to start %q", tc.name, tc.args, said, tc.want)
		}
	}
}
