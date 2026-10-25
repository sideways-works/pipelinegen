package connector

import (
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func TestAdmit(t *testing.T) {
	for from, want := range map[string]bool{
		plugin.FromSame: true, plugin.FromFork: false, plugin.FromUnknown: false, "": false, "Same": false,
	} {
		if got := Admit(plugin.Change{ID: "pr-1", From: from}); got != want {
			t.Errorf("from %q: admitted %v", from, got)
		}
	}
}

func TestFilterProblems(t *testing.T) {
	d := plugin.Description{Name: "plain", Capabilities: []string{plugin.CapChanges, plugin.CapLabels}}

	if got := FilterProblems(d, []string{"labels", "labels_without"}); len(got) != 0 {
		t.Fatalf("labels with the labels capability: %v", got)
	}

	got := FilterProblems(d, []string{"draft", "once_per_ready", "colour"})
	if len(got) != 3 || !strings.Contains(strings.Join(got, "\n"), `draft needs the connector's "draft" capability, which plain doesn't declare`) ||
		!strings.Contains(strings.Join(got, "\n"), `"ready_since"`) || !strings.Contains(strings.Join(got, "\n"), "colour is no pull request filter") {
		t.Fatalf("%v", got)
	}
}
