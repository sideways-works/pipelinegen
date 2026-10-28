package cli

import (
	"regexp"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/affected"
)

var showIDs bool

var showCache bool

var request = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS) /`)

// Only hops the plan's evidence holds are printed, never one made up.
func chainText(r affected.Reason, observedBy []string, ids bool) string {
	if len(r.Hops) == 0 || (r.Kind != affected.KindExecuted && r.Kind != affected.KindDepends) {
		return ""
	}

	first := r.Hops[0]
	name := hopName(first, ids)

	verb := "it executed " + name
	if request.MatchString(first.Label) {
		verb = "it requested " + name
	}

	// a list keeps no word of which correlator recorded a unit: only a
	// suite one correlator observes says who saw it.
	var notes []string
	if len(observedBy) == 1 {
		notes = append(notes, "seen by "+observedBy[0])
	}

	if first.File != "" && !request.MatchString(first.Label) && !namesFile(name, first.File) {
		notes = append(notes, first.File)
	}

	line := verb + " during its recording"
	if len(notes) > 0 {
		line += " (" + strings.Join(notes, "; ") + ")"
	}

	if len(r.Hops) == 1 {
		return line + ", which changed in this commit"
	}

	lines := []string{line}

	for i, h := range r.Hops[1:] {
		text := "→ " + linkWords(h) + " " + hopName(h, ids)
		if h.Plugin != "" {
			text += " (a link from " + h.Plugin + ")"
		}

		if i == len(r.Hops)-2 {
			text += ", which changed in this commit"
		}

		lines = append(lines, text)
	}

	return strings.Join(lines, "\n    ")
}

func namesFile(name, file string) bool {
	for w := range strings.FieldsSeq(name) {
		if strings.Contains(w, "/") && (file == w || strings.HasSuffix(file, "/"+w)) {
			return true
		}
	}

	return false
}

func linkWords(h affected.Hop) string {
	if h.Back {
		return "used by"
	}

	switch h.Kind {
	case "uses":
		return "which uses"
	case "reads":
		return "which reads"
	case "writes":
		return "which writes"
	case "generates":
		return "made by"
	default:
		return "which leads to"
	}
}

func hopName(h affected.Hop, ids bool) string {
	name := h.Label
	if name == "" {
		name = plainName(h.To)
	}

	if ids && name != h.To {
		name += " [" + h.To + "]"
	}

	return name
}

func plainName(id string) string {
	ns, rest, ok := strings.Cut(id, ":")
	if !ok {
		return id
	}

	where, what, hasWhat := strings.Cut(rest, "#")
	if !hasWhat {
		if ns == "api" {
			return "the operation " + rest
		}

		return rest
	}

	pkg := where[strings.LastIndex(where, "/")+1:]
	kind, name, hasKind := strings.Cut(what, ":")

	switch {
	case what == "init":
		return "the module " + where
	case !hasKind:
		return what
	case kind == "method" || kind == "type":
		return name
	case ns == "go":
		return pkg + "." + name
	default:
		return name
	}
}
