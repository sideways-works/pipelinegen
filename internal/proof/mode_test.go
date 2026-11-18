package proof

import (
	"errors"
	"testing"
)

func TestModeFromEnv(t *testing.T) {
	for _, tc := range []struct {
		flags        Options
		newest, tool string
		wantN, wantS bool
		refused      bool
	}{
		{},
		{newest: "true", wantN: true},
		{tool: "source", wantS: true},
		{flags: Options{Source: true}, wantS: true},
		{flags: Options{Source: true}, tool: "source", wantS: true},
		{newest: "true", tool: "source", refused: true},
		{newest: "yes", refused: true},
		{tool: "release", refused: true},
		{flags: Options{Newest: true}, tool: "source", refused: true},
	} {
		o := tc.flags

		err := o.ModeFromEnv(tc.newest, tc.tool)
		if tc.refused != errors.Is(err, ErrUsage) || (!tc.refused && (o.Newest != tc.wantN || o.Source != tc.wantS)) {
			t.Errorf("flags %+v, PROOF_NEWEST=%q PROOF_TOOL=%q: newest %v, source %v, %v", tc.flags, tc.newest, tc.tool, o.Newest, o.Source, err)
		}
	}
}
