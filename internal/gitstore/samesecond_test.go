package gitstore

import (
	"testing"
	"time"
)

// A record written again within the second of a compaction that moved its older copy into the run's ref
// ties with it by its marker's time: readers take the batch's copy then, and the next compaction keeps
// what they read.
func TestARecordWrittenAgainInTheSecondOfACompactionWins(t *testing.T) {
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	ctx := t.Context()
	remote := bare(t)

	store := func(batch string) *Store {
		s := groupedStore(t, remote, batch)
		s.Clock = func() time.Time { return at }

		return s
	}

	compact := func() {
		t.Helper()

		if _, err := store("").Compact(ctx, runsRoot+run+"/", "e2e"); err != nil {
			t.Fatal(err)
		}
	}

	write := func(batch, ref, body string) {
		t.Helper()

		if _, err := store(batch).Write(ctx, "write", Update{Ref: ref, Any: true, Files: files("x.json", body)}); err != nil {
			t.Fatal(err)
		}
	}

	write("", endRef(), "first")
	write("1", stateRef("a.spec.ts"), "first")
	compact()

	write("", endRef(), "again")
	write("9", stateRef("a.spec.ts"), "again")

	check := func(when string) {
		t.Helper()

		got := readAll(t, remote)
		for _, ref := range []string{endRef(), stateRef("a.spec.ts")} {
			if got[ref] != "x.json=again" {
				t.Errorf("%s: %s reads %q, want the record written last", when, ref, got[ref])
			}
		}
	}

	check("before the compaction")
	compact()
	check("after the compaction")
}
