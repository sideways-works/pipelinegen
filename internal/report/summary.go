package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// summaryFile holds what the journal's lines add up to, so a slice's start or end needn't read it all;
// one that doesn't cover the journal to its last byte is made again from it.
const summaryFile = "summary.json"

type summary struct {
	Size       int64                `json:"size"`
	Lines      int                  `json:"lines"`
	SetupEnded bool                 `json:"setup_ended,omitempty"`
	Since      time.Time            `json:"since"`
	Active     *SliceEvent          `json:"active,omitempty"`
	Slices     map[string]*sliceSum `json:"slices,omitempty"`
}

type sliceSum struct {
	Parts   []string  `json:"parts,omitempty"`
	Seconds float64   `json:"seconds,omitempty"`
	From    time.Time `json:"from,omitzero"`
	Trouble string    `json:"trouble,omitempty"`
	Use     *useSum   `json:"use,omitempty"`
}

func key(ev SliceEvent) string { return ev.Suite + "/" + ev.Slice }

func (s *summary) add(l journalLine) {
	s.Lines++
	if s.Lines == 1 {
		s.Since = l.At
	}

	switch l.Event {
	case eventFlushed:
		s.Since = l.At
	case eventSetupEnd:
		s.SetupEnded = true
	}

	if l.Slice == nil || l.Event == eventFlushed {
		return
	}

	ev := *l.Slice

	if l.Event == eventForget {
		delete(s.Slices, key(ev))

		if s.Active != nil && same(*s.Active, ev) {
			s.Active = nil
		}

		return
	}

	ss := s.slice(ev)

	if ss.Trouble == "" {
		ss.Trouble = ev.Unrecordable
	}

	switch l.Event {
	case eventStart:
		s.Active = &ev

		if len(ev.Coverage) > 0 && !slices.Contains(ss.Parts, ev.Coverage[0]) {
			ss.Parts = append(ss.Parts, ev.Coverage[0])
		}

		if ss.From.IsZero() {
			ss.From = l.At
		}
	case eventPartEnd, eventEnd:
		if s.Active != nil && same(*s.Active, ev) {
			s.Active = nil
		}

		if !ss.From.IsZero() {
			ss.Seconds += l.At.Sub(ss.From).Seconds()
			ss.From = time.Time{}
		}
	}

	ss.addUse(l.Event, ev.Use)
}

func (s *summary) slice(ev SliceEvent) *sliceSum {
	if s.Slices == nil {
		s.Slices = map[string]*sliceSum{}
	}

	ss, ok := s.Slices[key(ev)]
	if !ok {
		ss = &sliceSum{}
		s.Slices[key(ev)] = ss
	}

	return ss
}

func (s *summary) of(ev SliceEvent) sliceSum {
	if ss, ok := s.Slices[key(ev)]; ok {
		return *ss
	}

	return sliceSum{}
}

func summarize(lines []journalLine, size int64) *summary {
	s := &summary{Size: size}
	for _, l := range lines {
		s.add(l)
	}

	return s
}

func (j *Journal) summaryPath() string { return filepath.Join(filepath.Dir(j.Path), summaryFile) }

func (j *Journal) load() (*summary, error) {
	size := int64(0)

	info, err := os.Stat(j.Path)
	switch {
	case err == nil:
		size = info.Size()
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("journal: %w", err)
	}

	if data, err := os.ReadFile(j.summaryPath()); err == nil {
		var s summary
		if json.Unmarshal(data, &s) == nil && s.Size == size {
			j.sum = s.withSummaryPaths(j.local)

			return j.sum, nil
		}
	}

	lines, err := j.read()
	if err != nil {
		return nil, err
	}

	j.sum, j.dirty = summarize(lines, size), true
	j.saveSummary()

	return j.sum, nil
}

// saveSummary writes the summary beside the journal when it changed; it is
// only a shortcut, so a failed write is left to the next load.
func (j *Journal) saveSummary() {
	if j.sum == nil || !j.dirty {
		return
	}

	j.dirty = false

	data, err := json.Marshal(j.sum.withSummaryPaths(j.portable))
	if err != nil {
		return
	}

	tmp := j.summaryPath() + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil {
		return
	}

	if os.Rename(tmp, j.summaryPath()) != nil {
		_ = os.Remove(tmp) //nolint:errcheck // a leftover the next write replaces
	}
}
