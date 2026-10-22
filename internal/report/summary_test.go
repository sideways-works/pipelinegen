package report

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestTheSummaryAgreesWithTheJournal(t *testing.T) {
	ctx := t.Context()
	r, _, c, j := journalSetup(t)

	a := sliceEv(t, "e2e.checkout", `{"passed": true}`)
	b := sliceEv(t, "e2e.orders", `{"passed": true}`)
	a.CoverageRoot = t.TempDir()

	steps := []func() error{
		func() error { return r.SetupEnd(ctx, j, t.TempDir()) },
		func() error { return r.Started(ctx, j, a, true, time.Hour) },
		func() error { return r.Started(ctx, j, b, true, time.Hour) },
		func() error { return r.Started(ctx, j, a, true, time.Hour) },
		func() error { _, err := r.Ended(ctx, j, b, time.Hour); return err },
		func() error { _, err := r.Ended(ctx, j, a, time.Hour); return err },
	}

	fromJournal := func() *summary {
		t.Helper()

		lines, err := j.read()
		if err != nil {
			t.Fatal(err)
		}

		info, err := os.Stat(j.Path)
		if err != nil {
			t.Fatal(err)
		}

		return summarize(lines, info.Size())
	}

	for i, step := range steps {
		c.t = c.t.Add(time.Duration(i+1) * time.Second)

		if i%2 == 1 {
			j = JournalIn(j.Path[:len(j.Path)-len("/"+JournalDir+"/"+journalFile)])
		}

		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}

		got, err := j.load()
		if err != nil {
			t.Fatal(err)
		}

		if want := fromJournal(); !reflect.DeepEqual(norm(t, got), norm(t, want)) {
			t.Fatalf("step %d: summary\n%+v\nwant\n%+v", i, got, want)
		}
	}

	sum := fromJournal()
	if parts := sum.of(a).Parts; len(parts) != 2 || sum.of(b).Seconds != 4 || sum.of(a).Seconds != 8 || sum.Active != nil {
		t.Fatalf("summary %+v a %+v b %+v", sum, sum.of(a), sum.of(b))
	}

	for _, bad := range []string{"{", `{"size": 1}`, ""} {
		if bad == "" {
			if err := os.Remove(j.summaryPath()); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(j.summaryPath(), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}

		got, err := JournalIn(j.Path[:len(j.Path)-len("/"+JournalDir+"/"+journalFile)]).load()
		if err != nil || !reflect.DeepEqual(norm(t, got), norm(t, sum)) {
			t.Fatalf("%q: %+v %v", bad, got, err)
		}
	}
}

func norm(t *testing.T, s *summary) summary {
	t.Helper()

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}

	var out summary
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}

	return out
}
