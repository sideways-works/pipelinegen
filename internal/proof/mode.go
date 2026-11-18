package proof

import "fmt"

// The values of PROOF_NEWEST and PROOF_TOOL that choose a mode.
const (
	newestOn   = "true"
	toolSource = "source"
)

// ModeFromEnv adds the mode a suite's params name, PROOF_NEWEST "true" (--newest) or PROOF_TOOL
// "source" (--source), to the flags': one mode at most in all.
func (o *Options) ModeFromEnv(newest, tool string) error {
	if (newest != "" && newest != newestOn) || (tool != "" && tool != toolSource) || (newest != "" && tool != "") {
		return fmt.Errorf("%w: PROOF_NEWEST=%s PROOF_TOOL=%s: set at most one, PROOF_NEWEST to true or PROOF_TOOL to source", ErrUsage, newest, tool)
	}

	o.Newest = o.Newest || newest == newestOn
	o.Source = o.Source || tool == toolSource

	if o.Newest && o.Source {
		return fmt.Errorf("%w: --newest or --source, not both", ErrUsage)
	}

	return nil
}
