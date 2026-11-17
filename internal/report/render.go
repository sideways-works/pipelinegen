package report

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/secret"
)

// Format is how a comment shows a run.
type Format string

// The comment formats: a list of suites and slices, or a Mermaid flowchart
// GitHub draws, with the failures listed under it.
const (
	List    Format = "list"
	Mermaid Format = "mermaid"
)

// Mermaid refuses diagrams over its text and edge limits (50,000 characters
// and 500 edges by default); a chart past these budgets is shown as the list
// instead.
const (
	mermaidMaxChars = 40000
	mermaidMaxEdges = 400
	// GitHub refuses comments longer than this.
	maxCommentBytes = 65536
)

// Marker is the hidden line that finds a run's comment again on a pull
// request, whoever wrote it and wherever it is.
func Marker(run string, pr int) string {
	return fmt.Sprintf("<!-- pipelinegen:%s:%d -->", run, pr)
}

// Comment renders the whole comment body for a run, always from the run's
// full state, so the last write is always right.
func Comment(r Run, pr int, format Format) string {
	r = r.Sorted()

	// past GitHub's limit, the reasons go first, then the failures' output;
	// slices go last, cut off at the end
	body := render(r, format, true)
	if len(body)+len(Marker(r.Name, pr))+1 > maxCommentBytes && r.Reasoning {
		r.Reasoning = false
		body = render(r, format, true)
	}

	if len(body)+len(Marker(r.Name, pr))+1 > maxCommentBytes {
		body = render(r, format, false)
	}

	body = Marker(r.Name, pr) + "\n" + body
	if len(body) > maxCommentBytes {
		const cut = "\n\n(cut: the comment reached GitHub's size limit)\n"

		body = body[:maxCommentBytes-len(cut)] + cut
	}

	return body
}

func render(r Run, format Format, tails bool) string {
	var b strings.Builder

	b.WriteString(heading(r))

	if format == Mermaid {
		if chart, ok := mermaid(r); ok {
			b.WriteString(chart)

			for _, s := range r.Suites {
				causes(&b, r, s, true)
				narrowed(&b, s, r.Narrowings, true)
			}

			reasonsList(&b, r)
			removed(&b, r)
			failures(&b, r, tails)
			noTests(&b, r)
			pluginCalls(&b, r)

			return b.String()
		}
	}

	list(&b, r)
	failures(&b, r, tails)
	noTests(&b, r)
	pluginCalls(&b, r)

	return b.String()
}

func heading(r Run) string {
	title := fmt.Sprintf("**%s** run of `%s`", r.Name, short(r.Commit))
	if r.URL != "" {
		title = fmt.Sprintf("**%s** run of `%s` ([plan](%s))", r.Name, short(r.Commit), r.URL)
	}

	var b strings.Builder

	b.WriteString(title + "\n\n")
	rebuild(&b, r)
	warnings(&b, r)
	net(&b, r)

	for _, s := range r.Suites {
		_, desc := s.Status()
		fmt.Fprintf(&b, "- **%s**: %s\n", s.Name, desc)
	}

	b.WriteString("\n")
	untold(&b, r)
	marked(&b, r)

	return b.String()
}

func marked(b *strings.Builder, r Run) {
	if !r.All || len(r.Marked) == 0 {
		return
	}

	keys := make([]string, 0, len(r.Marked))
	for k := range r.Marked {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	fmt.Fprintf(b, "Bound to everything until a recording tells them (%s):\n\n", plural(len(keys), "slice"))

	const most = 30

	for i, k := range keys {
		if i == most {
			fmt.Fprintf(b, "- and %d more\n", len(keys)-most)

			break
		}

		parts := make([]string, 0, len(r.Marked[k]))

		for _, m := range r.Marked[k] {
			text := m.Plugin + " couldn't tell"
			if !m.Since.IsZero() {
				text += " since " + m.Since.UTC().Format("2006-01-02") + " (" + age(r.Now.Sub(m.Since)) + ")"
			}

			if m.Reason != "" {
				text += ": " + oneLine(secret.Redact(m.Reason))
			}

			parts = append(parts, text)
		}

		fmt.Fprintf(b, "- `%s`: %s\n", strings.ReplaceAll(k, "`", "'"), strings.Join(parts, "; "))
	}

	b.WriteString("\n")
}

func age(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d/(24*time.Hour)))
	case d >= 24*time.Hour:
		return "1 day"
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d/time.Hour))
	default:
		return "under 2 hours"
	}
}

func warnings(b *strings.Builder, r Run) {
	ws := r.Warnings()
	if len(ws) == 0 {
		return
	}

	b.WriteString("Warnings:\n\n")

	for _, w := range ws {
		fmt.Fprintf(b, "- %s\n", oneLine(w))
	}

	b.WriteString("\n")
}

func rebuild(b *strings.Builder, r Run) {
	if !r.Rebuild.failing() {
		return
	}

	b.WriteString("The cold rebuild of the graph differs from the cache:\n\n")

	for _, l := range r.Rebuild.Lines() {
		fmt.Fprintf(b, "- %s\n", oneLine(l))
	}

	b.WriteString("\n")
}

func pluginCalls(b *strings.Builder, r Run) {
	asked, cached := r.Plugins.Totals()
	if asked+cached == 0 {
		return
	}

	fmt.Fprintf(b, "<details><summary>Plugins: %d of %d files answered from the cache</summary>\n\n", cached, asked+cached)
	b.WriteString("| Plugin | Call | Asked | From the cache |\n|---|---|---|---|\n")

	for _, c := range r.Plugins.Calls {
		fmt.Fprintf(b, "| %s | %s | %d | %d |\n", htmlEsc(c.Plugin), htmlEsc(c.Call), c.Asked, c.Cached)
	}

	b.WriteString("\n</details>\n\n")
}

func untold(b *strings.Builder, r Run) {
	var lines []string

	for _, s := range r.Suites {
		for _, sl := range s.Slices {
			for _, m := range sl.CannotTell {
				line := fmt.Sprintf("- `%s %s`: %s could not tell", s.Name, strings.ReplaceAll(sl.display(), "`", "'"), m.Plugin)
				if m.Reason != "" {
					line += " (" + oneLine(secret.Redact(m.Reason)) + ")"
				}

				lines = append(lines, line)
			}
		}
	}

	if len(lines) == 0 {
		return
	}

	const most = 20
	if len(lines) > most {
		lines = append(lines[:most], fmt.Sprintf("- and %d more", len(lines)-most))
	}

	b.WriteString("Could not be recorded (each is bound to everything, so it runs on every change, until a recording tells again; its list stays as it was):\n\n")
	b.WriteString(strings.Join(lines, "\n") + "\n\n")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

const groupFrom = 9

func grouped(s Suite) bool { return len(s.Slices) >= groupFrom }

func bad(st State) bool { return st == Failed || st == Errored || st == NotRun }

func list(b *strings.Builder, r Run) {
	for _, s := range r.Suites {
		fmt.Fprintf(b, "#### %s\n\n", s.Name)

		if s.Reason != "" {
			fmt.Fprintf(b, "Every slice runs: %s.\n\n", s.Reason)
		}

		causes(b, r, s, false)
		narrowed(b, s, r.Narrowings, false)

		for _, sl := range s.Slices {
			if bad(sl.State) {
				sliceLine(b, r, s, sl, "")
			}
		}

		if grouped(s) {
			groups(b, r, s)
		} else {
			for _, sl := range s.Slices {
				if !bad(sl.State) {
					sliceLine(b, r, s, sl, "")
				}
			}
		}

		if len(s.Untouched) > 0 {
			fmt.Fprintf(b, "- untouched: %d slices\n", len(s.Untouched))
		}

		if len(s.Removed) > 0 {
			fmt.Fprintf(b, "- removed: %s\n", codes(s.Removed))
		}

		b.WriteString("\n")
	}
}

func sliceLine(b *strings.Builder, r Run, s Suite, sl Slice, indent string) {
	fmt.Fprintf(b, "%s- %s %s\n", indent, stateWord(sl.State), link(sl.display(), sl.URL))

	if r.Reasoning {
		for _, line := range reasonLines(ownReasons(r, s, sl.Reasons)) {
			fmt.Fprintf(b, "%s  - %s\n", indent, line)
		}
	}
}

func groups(b *strings.Builder, r Run, s Suite) {
	for _, g := range s.groups() {
		var rest []Slice

		for _, sl := range g.slices {
			if !bad(sl.State) {
				rest = append(rest, sl)
			}
		}

		if len(rest) == 0 {
			continue
		}

		fmt.Fprintf(b, "- <details><summary><b>%s</b>: %s</summary>\n\n", htmlEsc(g.name), counts(rest))

		for _, sl := range rest {
			sliceLine(b, r, s, sl, "  ")
		}

		b.WriteString("\n  </details>\n")
	}
}

type group struct {
	name   string
	slices []Slice
}

func (s Suite) groups() []group {
	var out []group

	at := map[string]int{}

	for _, sl := range s.Slices {
		i, ok := at[sl.Group]
		if !ok {
			i = len(out)
			at[sl.Group] = i
			out = append(out, group{name: sl.Group})
		}

		out[i].slices = append(out[i].slices, sl)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].name < out[j].name })

	return out
}

var stateOrder = []State{Failed, Errored, NotRun, Running, Pending, Passed}

func counts(sls []Slice) string {
	n := map[State]int{}
	for _, sl := range sls {
		n[sl.State]++
	}

	parts := make([]string, 0, len(stateOrder))

	for _, st := range stateOrder {
		if n[st] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n[st], st))
		}
	}

	return strings.Join(parts, ", ")
}

func worst(sls []Slice) State {
	n := map[State]bool{}
	for _, sl := range sls {
		n[sl.State] = true
	}

	for _, st := range stateOrder {
		if n[st] {
			return st
		}
	}

	return Pending
}

func htmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func reasonsList(b *strings.Builder, r Run) {
	if !r.Reasoning {
		return
	}

	wrote := false

	for _, s := range r.Suites {
		for _, sl := range s.Slices {
			lines := reasonLines(ownReasons(r, s, sl.Reasons))
			if len(lines) == 0 {
				continue
			}

			if !wrote {
				b.WriteString("#### Why these slices run\n\n")

				wrote = true
			}

			fmt.Fprintf(b, "- `%s %s`: %s\n", s.Name, sl.display(), strings.Join(lines, "; "))
		}
	}

	if wrote {
		b.WriteString("\n")
	}
}

// narrowed says plainly how a selection was made smaller, so a pass never
// reads as a full one.
func narrowed(b *strings.Builder, s Suite, set []NarrowingSet, named bool) {
	if len(s.Narrowed) == 0 {
		return
	}

	// a slice several narrowings left out is named under each, and
	// counted once
	left, seen := 0, map[string]bool{}

	for _, n := range s.Narrowed {
		for _, name := range n.Dropped {
			if !seen[name] {
				left++
			}
		}

		for _, name := range n.Dropped {
			seen[name] = true
		}
	}

	what := "The change"
	if named {
		what = "In **" + s.Name + "**, the change"
	}

	fmt.Fprintf(b, "%s reaches %s; %d run.\n\n", what, plural(len(s.Slices)+left, "slice"), len(s.Slices))

	for _, n := range s.Narrowed {
		by := "`" + n.Narrowing + "`"
		if n.Narrowing == "" {
			by = n.Kind
		}

		if n.Kind == "sampled" {
			fmt.Fprintf(b, "- The sample (%s) left %d for the nightly", by, len(n.Dropped))
		} else {
			fmt.Fprintf(b, "- %s left out %d", by, len(n.Dropped))
		}

		if n.OffAt > 0 {
			fmt.Fprintf(b, "; they would run from narrowing level %d", n.OffAt)
		}

		if lv, ok := SetBy(set, n.Narrowing); ok {
			fmt.Fprintf(b, ", %s", secret.Redact(lv.Text()))
		}

		b.WriteString(".")

		// a reason may hold parentheses of its own (a sample_on pattern),
		// so it stands as a sentence
		if n.Reason != "" {
			fmt.Fprintf(b, " Why: %s.", strings.TrimSuffix(oneLine(secret.Redact(n.Reason)), "."))
		}

		b.WriteString("\n")
	}

	b.WriteString("\n")
}

func net(b *strings.Builder, r Run) {
	if r.Net == nil || len(r.Net.Traces) == 0 {
		return
	}

	if level, ok := r.Net.Raise(); ok {
		fmt.Fprintf(b, "**%d failures trace to narrowed changes: raise narrowing to %d", r.Net.Failing(), level)

		if set := r.Net.SetLevels(); len(set) > 0 {
			fmt.Fprintf(b, ", or lower %s, which the config set", strings.Join(set, ", "))
		}

		b.WriteString(".**\n\n")
	} else {
		b.WriteString("Failures that trace to narrowed changes:\n\n")
	}

	for _, t := range r.Net.Traces {
		who := "`" + short(t.Commit) + "`"
		if t.Subject != "" {
			who += " (" + t.Subject + ")"
		}

		off := fmt.Sprintf("off from level %d", t.OffAt)
		if t.Set != nil {
			off = secret.Redact(t.Set.Text())
		}

		fmt.Fprintf(b, "- `%s %s` first fails since %s, whose %s run left it out: %s by `%s`, %s\n",
			t.Suite, t.Slice, who, t.Run, t.Kind, t.Narrowing, off)
	}

	b.WriteString("\n")
}

func removed(b *strings.Builder, r Run) {
	for _, s := range r.Suites {
		if len(s.Removed) > 0 {
			fmt.Fprintf(b, "Removed from **%s**: %s\n\n", s.Name, codes(s.Removed))
		}
	}
}

func codes(labels []string) string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		out = append(out, "`"+name(l)+"`")
	}

	return strings.Join(out, ", ")
}

const maxCauses = 5

func shownCauses(r Run, s Suite) []Cause {
	if !r.Reasoning && s.Reason == "" {
		return nil
	}

	return s.Causes[:min(len(s.Causes), maxCauses)]
}

func causes(b *strings.Builder, r Run, s Suite, named bool) {
	shown := shownCauses(r, s)
	if len(shown) == 0 {
		return
	}

	if named {
		fmt.Fprintf(b, "Shared causes in **%s**:\n\n", s.Name)
	} else {
		b.WriteString("Shared causes:\n\n")
	}

	for _, c := range shown {
		fmt.Fprintf(b, "- %s: %s\n", plural(c.Slices, "slice"), reasonText(Reason{Kind: c.Kind, What: c.What, Through: c.Through}))
	}

	if more := len(s.Causes) - len(shown); more > 0 {
		fmt.Fprintf(b, "- and %s\n", plural(more, "more shared cause"))
	}

	b.WriteString("\n")
}

func ownReasons(r Run, s Suite, rs []Reason) []Reason {
	shown := shownCauses(r, s)
	if len(shown) == 0 {
		return rs
	}

	return slices.DeleteFunc(slices.Clone(rs), func(x Reason) bool {
		return slices.ContainsFunc(shown, func(c Cause) bool { return c.of(x) })
	})
}

const maxReasons = 3

func reasonLines(rs []Reason) []string {
	out := make([]string, 0, min(len(rs), maxReasons)+1)

	for i, x := range rs {
		if i == maxReasons {
			out = append(out, fmt.Sprintf("and %d more", len(rs)-maxReasons))

			break
		}

		out = append(out, reasonText(x))
	}

	return out
}

const maxThrough = 4

func reasonText(r Reason) string {
	what := "`" + strings.ReplaceAll(r.What, "`", "'") + "`"

	var text string

	switch r.Kind {
	case "everything":
		return "every slice runs: " + r.What
	case "new-slice":
		return "a new slice"
	case "test-added":
		text = "test added: " + what
	case "test-removed":
		text = "test removed: " + what
	case "test-changed":
		text = "test changed: " + what
	case "executed":
		text = "executed " + what + ", which changed"
	case "depends":
		text = "reaches " + what + ", which changed"
	case "opens":
		text = what + " changed in an app it opens"
	case "target":
		text = "target " + what + " changed"
	case "input-added":
		return "new input " + what
	case "input-removed":
		return "input " + what + " gone"
	case "input-unknown":
		return "input " + what + " not hashed, so it can't be told whether it changed"
	case "text-input":
		return "reads " + what + " as text, which changed"
	case "config":
		return "the config: " + r.What
	case "plugin":
		text = "the entry of plugin " + what + " changed"
	default:
		text = r.Kind + ": " + what
	}

	if path := throughText(r.Through); path != "" {
		text += ", through " + path
	}

	return text
}

func throughText(steps []string) string {
	if len(steps) == 0 {
		return ""
	}

	shown := steps
	gap := ""

	if len(steps) > maxThrough {
		shown = append(append([]string(nil), steps[:maxThrough-1]...), steps[len(steps)-1])
		gap = fmt.Sprintf(" (%d more) ", len(steps)-maxThrough)
	}

	parts := make([]string, 0, len(shown))
	for _, s := range shown {
		parts = append(parts, "`"+strings.ReplaceAll(s, "`", "'")+"`")
	}

	if gap == "" {
		return strings.Join(parts, " → ")
	}

	n := len(parts)

	return strings.Join(parts[:n-1], " → ") + " →" + gap + "→ " + parts[n-1]
}

func noTests(b *strings.Builder, r Run) {
	var names []string

	for _, s := range r.Suites {
		for _, sl := range s.Slices {
			if sl.NoTests {
				names = append(names, "`"+strings.ReplaceAll(s.Name+" "+sl.display(), "`", "'")+"`")
			}
		}
	}

	if len(names) > 0 {
		fmt.Fprintf(b, "Ran no tests: %s\n", strings.Join(names, ", "))
	}
}

func failures(b *strings.Builder, r Run, tails bool) {
	wrote := false

	for _, s := range r.Suites {
		// slices their batch never started are told once, not each
		if n := s.Counts()[NotRun]; n > 0 {
			if !wrote {
				b.WriteString("#### Failures\n\n")

				wrote = true
			}

			fmt.Fprintf(b, "- %s %s of %s: %s\n", stateWord(NotRun), plural(n, "slice"), s.Name, notRun.Tail)
		}

		for _, sl := range s.Slices {
			if sl.State != Failed && sl.State != Errored {
				continue
			}

			if !wrote {
				b.WriteString("#### Failures\n\n")

				wrote = true
			}

			fmt.Fprintf(b, "- %s %s\n", stateWord(sl.State), link(s.Name+" "+sl.display(), sl.URL))

			for _, f := range sl.Failures {
				fmt.Fprintf(b, "  - `%s`\n", strings.ReplaceAll(f.Test, "`", "'"))

				if tails && f.Tail != "" {
					fmt.Fprintf(b, "\n    ```\n%s\n    ```\n\n", indent(f.Tail, "    "))
				}
			}
		}
	}

	if wrote {
		b.WriteString("\n")
	}
}

func mermaid(r Run) (string, bool) {
	var b strings.Builder

	edges := 0

	b.WriteString("```mermaid\nflowchart LR\n")
	fmt.Fprintf(&b, "  plan[\"%s %s\"]\n", esc(r.Name), short(r.Commit))

	for i, s := range r.Suites {
		_, desc := s.Status()
		sid := fmt.Sprintf("s%d", i)

		fmt.Fprintf(&b, "  plan --> %s[\"%s: %s\"]\n", sid, esc(s.Name), esc(desc))

		edges++

		if grouped(s) {
			for j, g := range s.groups() {
				fmt.Fprintf(&b, "  %s --> %s_g%d[\"%s: %s\"]:::%s\n", sid, sid, j, esc(g.name), esc(counts(g.slices)), class(worst(g.slices)))

				edges++
			}
		} else {
			for j, sl := range s.Slices {
				fmt.Fprintf(&b, "  %s --> %s_%d[\"%s\"]:::%s\n", sid, sid, j, esc(sl.display()), class(sl.State))

				edges++
			}
		}

		for j, n := range s.Narrowed {
			text := fmt.Sprintf("%d left out by %s", len(n.Dropped), n.Narrowing)
			if n.Kind == "sampled" {
				text = fmt.Sprintf("%d left for the nightly by the sample", len(n.Dropped))
			}

			if n.OffAt > 0 {
				text += fmt.Sprintf(", run from level %d", n.OffAt)
			}

			fmt.Fprintf(&b, "  %s -.-> %s_n%d[\"%s\"]:::untouched\n", sid, sid, j, esc(text))

			edges++
		}

		if len(s.Untouched) > 0 {
			fmt.Fprintf(&b, "  %s --> %s_u[\"%d untouched\"]:::untouched\n", sid, sid, len(s.Untouched))

			edges++
		}
	}

	b.WriteString(`  classDef pending fill:#eaeef2,stroke:#8c959f,color:#24292f
  classDef running fill:#ddf4ff,stroke:#0969da,color:#0a3069
  classDef passed fill:#dafbe1,stroke:#1a7f37,color:#116329
  classDef failed fill:#ffebe9,stroke:#cf222e,color:#a40e26
  classDef errored fill:#fff8c5,stroke:#9a6700,color:#4d2d00
  classDef notrun fill:#fff8c5,stroke:#9a6700,color:#4d2d00,stroke-dasharray:3 3
  classDef untouched fill:#f6f8fa,stroke:#d0d7de,color:#57606a,stroke-dasharray:3 3
` + "```\n\n")

	chart := b.String()

	return chart, edges <= mermaidMaxEdges && len(chart) <= mermaidMaxChars
}

func stateWord(s State) string {
	return "**" + string(s) + "**"
}

func link(text, url string) string {
	if url == "" {
		return "`" + text + "`"
	}

	return fmt.Sprintf("[`%s`](%s)", text, url)
}

func (sl Slice) display() string {
	if sl.Name != "" {
		return sl.Name
	}

	return name(sl.Label)
}

func name(label string) string {
	return strings.TrimPrefix(label, "//")
}

func esc(s string) string {
	return strings.NewReplacer(`"`, "#quot;", "<", "#lt;", ">", "#gt;").Replace(s)
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(strings.ReplaceAll(s, "```", "'''"), "\n", "\n"+prefix)
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}

	return commit
}

func class(st State) string { return strings.ReplaceAll(string(st), " ", "") }
