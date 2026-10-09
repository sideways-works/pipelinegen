package attached_test

import (
	"testing"

	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"

	"github.com/sideways-works/pipelinegen/internal/attached"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

func TestAttachmentsReadBack(t *testing.T) {
	s := storetest.Open(t, storetest.Bare(t))

	if got, err := attached.Read(t.Context(), s, "c1"); err != nil || len(got) != 0 {
		t.Fatalf("none: %v %v", got, err)
	}

	pr := attached.Attachment{Run: "pr", Change: "pr-7", Commit: "abc"}
	branch := attached.Attachment{Run: "main", Change: "release/1.2", Commit: "c1"}

	if branch.File() != "main/release%2F1.2/c1.json" {
		t.Fatalf("a change's slash stays in its part: %s", branch.File())
	}

	if _, err := s.Write(t.Context(), "attached", gitstore.Update{Ref: attached.Ref("c1"), Files: map[string][]byte{
		pr.File():     []byte(`{"run": "pr", "change": "pr-7", "commit": "abc"}`),
		branch.File(): []byte(`{"run": "main", "change": "release/1.2", "commit": "c1"}`),
	}}); err != nil {
		t.Fatal(err)
	}

	got, err := attached.Read(t.Context(), s, "c1")
	if err != nil || len(got) != 2 || got[0] != branch || got[1] != pr {
		t.Fatalf("read %v %v", got, err)
	}
}

func TestMain(m *testing.M) { storetest.Both(m) }
