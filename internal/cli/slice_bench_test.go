package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/report"
)

const benchJournalLines = 4800

const benchSlices = 400

func BenchmarkSliceStart(b *testing.B) {
	for _, n := range []int{0, 1, 2} {
		b.Run(fmt.Sprintf("correlators=%d", n), func(b *testing.B) { benchSliceStart(b, n) })
	}
}

func benchSliceStart(b *testing.B, correlators int) {
	b.Helper()

	root, _, s := runEnv(b)

	recording := strings.Replace(
		strings.Replace(reportConfig, "    tag: slice-e2e\n", "    tag: slice-e2e\n    bind: recorded\n", 1),
		"    report: {comment: {format: list}}\n", "    report: {comment: {format: list}}\n    record: true\n", 1) + "plugins:\n  - {name: units, kind: sbom, repository: example/units, commit: 0123456789abcdef0123456789abcdef01234567}\n"

	var conf strings.Builder

	conf.WriteString(recording)

	if correlators > 0 {
		conf.WriteString("plugins:\n")
	}

	b.Chdir(b.TempDir())

	for i := range correlators {
		name := fmt.Sprintf("c%d", i)
		fmt.Fprintf(&conf, "  - {name: %s, kind: correlator, repository: example/%s, commit: 0123456789abcdef0123456789abcdef01234567}\n", name, name)
		writeFile(b, "tools/plugins/"+name, "#!/bin/sh\ncat >/dev/null\ncase $1 in\n"+
			"describe) echo '{\"protocol\": 1, \"kind\": \"correlator\", \"name\": \""+name+"\", \"version\": \"v0\", \"capabilities\": [\"collect\", \"observe\"]}' ;;\n"+
			"collect) echo '{}' ;;\nesac\n")

		if err := os.Chmod("tools/plugins/"+name, 0o700); err != nil { //nolint:gosec // the benchmark's own plugin
			b.Fatal(err)
		}
	}

	writeFile(b, filepath.Join(root, "pipelinegen.yaml"), conf.String())

	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}
	if err := report.WritePlan(b.Context(), s, id, []byte(`{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}, {"label": "//e2e:b"}]}]}`)); err != nil {
		b.Fatal(err)
	}

	writeFile(b, "batches/1/slices.json", `[{"name": "e2e.a", "label": "//e2e:a"}, {"name": "e2e.b", "label": "//e2e:b"}]`)
	writeFile(b, filepath.Join("result", report.JournalDir, "journal.jsonl"), longJournal(b))

	names := []string{"e2e.a", "e2e.b"}

	b.ResetTimer()

	i := 0
	self, children := cpu(b, syscall.RUSAGE_SELF), cpu(b, syscall.RUSAGE_CHILDREN)

	for b.Loop() {
		var stdout, stderr strings.Builder
		if code := Run(b.Context(), []string{"slice", "start", names[i%2], "-root", root}, &stdout, &stderr); code != 0 {
			b.Fatalf("start: %d %s", code, stderr.String())
		}

		i++
	}

	// CPU time says what the step costs whatever else the machine runs:
	// the command's own, and its plugins'
	b.ReportMetric(float64(cpu(b, syscall.RUSAGE_SELF)-self)/float64(time.Millisecond)/float64(i), "cpu-ms/op")
	b.ReportMetric(float64(cpu(b, syscall.RUSAGE_CHILDREN)-children)/float64(time.Millisecond)/float64(i), "plugin-cpu-ms/op")
}

func cpu(b *testing.B, who int) time.Duration {
	b.Helper()

	var ru syscall.Rusage
	if err := syscall.Getrusage(who, &ru); err != nil {
		b.Fatal(err)
	}

	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func longJournal(b *testing.B) string {
	b.Helper()

	var out strings.Builder

	now := time.Now().UTC()
	line := func(v any) {
		data, err := json.Marshal(v)
		if err != nil {
			b.Fatal(err)
		}

		out.Write(append(data, '\n'))
	}

	line(map[string]any{"event": "setup-end", "at": now})

	for k := 1; k < benchJournalLines-1; k++ {
		n := (k / 2) % benchSlices
		ev := map[string]any{"suite": "e2e", "slice": fmt.Sprintf("e2e.s%d", n), "label": fmt.Sprintf("//e2e:s%d", n),
			"result": "result/e2e.s.json", "source": "source",
			"coverage": []string{fmt.Sprintf("coverage/.pipelinegen/part-%d/e2e.s%d", k/(2*benchSlices)+1, n), "coverage/_setup"}}

		event := "start"
		if k%2 == 0 {
			event = "part-end"
		}

		line(map[string]any{"event": event, "at": now, "slice": ev})
	}

	line(map[string]any{"event": "flushed", "at": now, "lines": benchJournalLines - 1})

	return out.String()
}
