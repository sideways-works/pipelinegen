// Package recording reads and writes what a slice's run recorded, one unit id per line: what it
// touched, what only its batch's setup ran, and which correlators couldn't tell.
package recording

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// setupMark starts the setup section: what only the batch's setup ran,
	// or what ran only as loading.
	setupMark = "# setup"
	// observedMark opens a recording the correlators made, so an empty
	// one says they saw nothing rather than that nothing was recorded.
	observedMark = "# observed"
	// cannotTellMark starts a line naming a correlator that couldn't tell,
	// since when, and why: "# cannot tell: <plugin> (since <time>): <reason>".
	cannotTellMark = "# cannot tell: "
	sinceOpen      = " (since "
)

var ErrRecording = errors.New("recording")

// Empty is the plugin of the mark a recording gets when its tests ran and no correlator saw them
// touch anything: none of them could tell, though each read what it records.
const Empty = "correlators"

// Mark is a correlator that couldn't tell what a slice touched.
type Mark struct {
	Plugin string `json:"plugin"`
	Reason string `json:"reason,omitempty"`
	// Since is when a recording first couldn't tell, of the recordings
	// since the slice was last told.
	Since time.Time `json:"since,omitzero"`
}

type Recording struct {
	// Own are the units the slice's own tests touched; Setup those only
	// its batch's setup ran or that ran only as loading.
	Own, Setup []string
	// Observed is set for a recording the correlators made: then no ids
	// means they saw nothing, on evidence.
	Observed bool
	// CannotTell are the correlators that couldn't tell; such a recording
	// holds no ids, and the slice's list stays as it was.
	CannotTell []Mark
}

// HasData tells whether the recording says what the slice touched: a
// slice whose list it may change.
func (r Recording) HasData() bool {
	return len(r.CannotTell) == 0 && (r.Observed || len(r.Own) > 0 || len(r.Setup) > 0)
}

// Format writes the recording: the marks, the slice's own ids sorted and
// without duplicates, then the setup's under the setup mark.
func (r Recording) Format() []byte {
	var b bytes.Buffer

	if r.Observed || len(r.CannotTell) > 0 {
		b.WriteString(observedMark + "\n")
	}

	for _, m := range r.CannotTell {
		line := oneLine(m.Plugin)
		if !m.Since.IsZero() {
			line += sinceOpen + m.Since.UTC().Format(time.RFC3339) + ")"
		}

		if m.Reason != "" {
			line += ": " + oneLine(m.Reason)
		}

		b.WriteString(cannotTellMark + line + "\n")
	}

	if len(r.CannotTell) > 0 {
		return b.Bytes()
	}

	b.Write(List(r.Own))

	if setup := List(r.Setup); len(setup) > 0 {
		b.WriteString(setupMark + "\n")
		b.Write(setup)
	}

	return b.Bytes()
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// List writes ids one per line, sorted, without duplicates.
func List(ids []string) []byte {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)

	var b bytes.Buffer

	for _, id := range ids {
		b.WriteString(id)
		b.WriteByte('\n')
	}

	return b.Bytes()
}

// Parse reads a recording, in either form.
func Parse(data []byte) (Recording, error) {
	var (
		r   Recording
		cur = &r.Own
	)

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())

		switch {
		case line == "":
		case line == setupMark:
			cur = &r.Setup
		case line == observedMark:
			r.Observed = true
		case strings.HasPrefix(line, cannotTellMark):
			r.CannotTell = append(r.CannotTell, parseMark(strings.TrimPrefix(line, cannotTellMark)))
		default:
			if err := ValidID(line); err != nil {
				return Recording{}, err
			}

			*cur = append(*cur, line)
		}
	}

	if err := sc.Err(); err != nil {
		return Recording{}, fmt.Errorf("%w: %w", ErrRecording, err)
	}

	return r, nil
}

// parseMark reads "<plugin> (since <time>): <reason>", the time and the
// reason optional.
func parseMark(text string) Mark {
	head, reason, _ := strings.Cut(text, ": ")
	m := Mark{Plugin: head, Reason: reason}

	if plugin, since, ok := strings.Cut(head, sinceOpen); ok {
		if t, err := time.Parse(time.RFC3339, strings.TrimSuffix(since, ")")); err == nil {
			m.Plugin, m.Since = plugin, t
		}
	}

	return m
}

var namespace = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// oldID is a declaration as recordings held it before the correlators:
// <package directory>#<kind>:<name>.
var oldID = regexp.MustCompile(`^[^#\s]+#[a-z]+:\S+$`)

// ValidID tells whether a line is a unit id: namespaced (go:internal/notes#func:Create,
// pg:table:public.notes, file:<path>) or a declaration in the older form.
func ValidID(id string) error {
	if strings.ContainsAny(id, " \t\r\n") || strings.HasPrefix(id, "#") {
		return fmt.Errorf("%w: %q is no unit id", ErrRecording, id)
	}

	if oldID.MatchString(id) {
		return nil
	}

	if ns, rest, ok := strings.Cut(id, ":"); ok && rest != "" && namespace.MatchString(ns) {
		return nil
	}

	return fmt.Errorf("%w: %q is no unit id", ErrRecording, id)
}

// Namespaced tells whether an id is in a plugin's namespace (go:, pg:, api:), which only planning
// through the plugins can bind; file: is the core's own and the older form's too.
func Namespaced(id string) bool {
	ns, _, ok := strings.Cut(id, ":")

	return ok && ns != "file" && namespace.MatchString(ns)
}
