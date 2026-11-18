package report

import "path/filepath"

// A batch's ensure may read its journal in another container at another absolute path, so the
// journal keeps every path of a slice's event relative to its own directory.

func (ev SliceEvent) withPaths(f func(string) string) SliceEvent {
	ev.Result = f(ev.Result)
	ev.Source = f(ev.Source)
	ev.Extras = f(ev.Extras)
	ev.CoverageRoot = f(ev.CoverageRoot)
	ev.Coverage = mapPaths(ev.Coverage, f)
	ev.Parts = mapPaths(ev.Parts, f)

	return ev
}

func mapPaths(ps []string, f func(string) string) []string {
	if ps == nil {
		return nil
	}

	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = f(p)
	}

	return out
}

func (j *Journal) dir() string {
	d := filepath.Dir(j.Path)
	if abs, err := filepath.Abs(d); err == nil {
		return abs
	}

	return d
}

func (j *Journal) portable(p string) string {
	if p == "" {
		return p
	}

	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}

	rel, err := filepath.Rel(j.dir(), abs)
	if err != nil {
		return p
	}

	return filepath.ToSlash(rel)
}

func (j *Journal) local(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}

	return filepath.Join(j.dir(), filepath.FromSlash(p))
}

func (j *Journal) portableLine(l journalLine) journalLine {
	if l.Slice != nil {
		ev := l.Slice.withPaths(j.portable)
		l.Slice = &ev
	}

	return l
}

func (j *Journal) localLine(l journalLine) journalLine {
	if l.Slice != nil {
		ev := l.Slice.withPaths(j.local)
		l.Slice = &ev
	}

	return l
}

func (s *summary) withSummaryPaths(f func(string) string) *summary {
	out := *s

	if s.Active != nil {
		ev := s.Active.withPaths(f)
		out.Active = &ev
	}

	if s.Slices != nil {
		out.Slices = make(map[string]*sliceSum, len(s.Slices))

		for k, ss := range s.Slices {
			c := *ss
			c.Parts = mapPaths(ss.Parts, f)
			out.Slices[k] = &c
		}
	}

	return &out
}
