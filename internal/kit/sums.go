package kit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/plugin"
	"github.com/sideways-works/plugkit/pin"
)

const sumsCheck = "a tool pinned by sums is checked by its release's SHA256SUMS, kept beside it"

// sumsSuffix names the list a tool pinned by sums is kept beside, as <name>.SHA256SUMS.
const sumsSuffix = ".SHA256SUMS"

// sums checks that the tasks the kit runs fetch a tool pinned by sums, with no sha256, by its
// list: a stand-in plugin's, and the runner's own. The kit places the list beside each, as a task
// that fetched them would hold it, so the tasks stay offline; a runner that reads only a sha256
// finds none to check the tool by and has to fetch it.
func (k *kit) sums(ctx context.Context, root string) {
	var (
		problems []string
		ran      bool
	)

	for _, v := range []struct {
		name string
		kind func(plugin.SpecTool) bool
	}{
		{"a plugin pinned by sums", func(t plugin.SpecTool) bool {
			return t.Kind != plugin.KindRunner && t.Kind != plugin.ToolPipelinegen && t.Kind != plugin.ToolBazelisk && t.Version != ""
		}},
		{"the runner pinned by sums", func(t plugin.SpecTool) bool { return t.Kind == plugin.KindRunner }},
	} {
		spec := k.bySums(k.pinned(Spec()), v.kind)
		work := k.WorkDir()

		var ans plugin.PipelineAnswer
		if err := k.call(ctx, plugin.CallPipeline, plugin.PipelineRequest{Request: k.p.NewRequest(work), Spec: spec, Root: root}, &ans); err != nil || len(ans.Files) != 1 {
			problems = append(problems, fmt.Sprintf("(%s) pipeline: %v (%d files)", v.name, err, len(ans.Files)))

			continue
		}

		text, err := os.ReadFile(filepath.Join(work, filepath.Clean(ans.Files[0]))) //nolint:gosec // inside the call's work directory
		if err != nil {
			problems = append(problems, fmt.Sprintf("(%s) %v", v.name, err))

			continue
		}

		runs := runTasks(ctx, taskRunner{spec: spec, runner: k.o.Path, work: k.WorkDir(), installs: installsOf(spec)}, text)

		var unfetched []string

		for _, key := range slices.Sorted(maps.Keys(runs.byTask)) {
			ran = true

			if p := runs.byTask[key].unfetched(); p != "" {
				unfetched = appendNew(unfetched, fmt.Sprintf("(%s) job %s, task %s: %s", v.name, strings.SplitN(key, "\n", 2)[0], runs.byTask[key].task, p))
			}
		}

		// each variant's own, so one's failures don't hide the other's
		problems = append(problems, firstFew(unfetched)...)
	}

	if !ran && len(problems) == 0 {
		k.Skip(sumsCheck, "the pipeline has no task the kit runs, none that runs a program of the runner's")

		return
	}

	k.Check(sumsCheck, strings.Join(problems, "; "))
}

// bySums is the spec with the first tool which picks pinned by sums instead of a sha256: by the
// checksum of the list the kit places beside it.
func (k *kit) bySums(s plugin.Spec, which func(plugin.SpecTool) bool) plugin.Spec {
	for i, t := range s.Tools {
		if which(t) {
			list := sumsList(t, placedSum(t, k.o.Path))
			sum := sha256.Sum256(list)
			s.Tools[i].SHA256, s.Tools[i].Sums = "", hex.EncodeToString(sum[:])

			break
		}
	}

	return s
}

// placedSum is the checksum of what the kit places for a tool: the runner under test, or the
// stand-in.
func placedSum(t plugin.SpecTool, runner string) string {
	if t.Kind == plugin.KindRunner {
		return fileSum(runner)
	}

	sum := sha256.Sum256([]byte(standInTool))

	return hex.EncodeToString(sum[:])
}

// sumsList is the SHA256SUMS of a tool's release as the kit has it: its linux-amd64 file, the
// one a task runs.
func sumsList(t plugin.SpecTool, sum string) []byte {
	return []byte(sum + "  " + config.ProgramOf(t.Repository, t.Path) + "-linux-amd64\n")
}

// listedProblems say where the tools pinned by sums in a task's directory don't check against
// their lists after it ran: a list beside a tool that isn't the pinned one, or a tool its line in
// the list doesn't match.
func (r taskRunner) listedProblems(td string) []string {
	var out []string

	for _, t := range r.spec.Tools {
		if t.Sums == "" {
			continue
		}

		dest := filepath.Join("tools", "plugins", t.Name)
		list := dest + sumsSuffix

		data, err := os.ReadFile(filepath.Join(td, list)) //nolint:gosec // the task's own directory
		if err != nil || fileSum(filepath.Join(td, list)) != t.Sums {
			out = append(out, list+" isn't the list the pin's sums names")

			continue
		}

		program := config.ProgramOf(t.Repository, t.Path)
		listed := pin.Listed(data)

		line := listed[program+"-linux-amd64"]
		if line == "" {
			line = listed[program+".mjs"]
		}

		if line == "" || fileSum(filepath.Join(td, dest)) != line {
			out = append(out, dest+" doesn't match its line in "+t.Name+sumsSuffix)
		}
	}

	return out
}

// unfetched says why a task didn't fetch its tools by their pins: it failed before it called the
// tool, or what it holds after it ran doesn't check against the lists. A task may fail after its
// calls: the stale check does when the stand-in writes another pipeline.
func (res *taskRun) unfetched() string {
	if res.exit != 0 && len(res.calls) == 0 {
		said := strings.TrimSpace(res.output)
		if i := strings.LastIndex(said, "\n"); i >= 0 {
			said = said[i+1:]
		}

		return fmt.Sprintf("exit %d: %s", res.exit, said)
	}

	return strings.Join(res.listed, "; ")
}
