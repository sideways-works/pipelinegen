package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Difference is an answer a cold pass got from a plugin that differs from
// what the cache would have served: the answer depends on something its
// inputs don't name, or the stored answer is wrong.
type Difference struct {
	Plugin     string   `json:"plugin"`
	Call       string   `json:"call"`
	Inputs     []string `json:"inputs"`
	InputUnits []string `json:"input_units,omitempty"`
	What       []string `json:"what"`
}

// Differences are what a cold pass found the cache would have served
// otherwise, in the order found.
func (s *Set) Differences() []Difference {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.differences)
}

func (s *Set) differ(d Difference) {
	s.mu.Lock()
	s.differences = append(s.differences, d)
	s.mu.Unlock()
}

// coldSets asks the plugin about every claim and compares its sets with the
// cached ones; the cache never overwrites what it holds.
func (s *Set) coldSets(ctx context.Context, l *Loaded, call, root string, files map[string]string, units unitDigests, claims []plugin.Claim) (answer, []string, error) {
	// every set whose stored answer holds is compared, also one a warm
	// plan would ask about again because it shares an input with a set
	// that changed
	cached := s.lookup(ctx, l, call, files, units, claims).served

	asked := claimPaths(claims)
	s.count(l.Conf.Name, call, len(asked), 0)

	fresh, ok, err := s.ask(ctx, l, call, root, asked, claims)
	if err != nil || !ok {
		return answer{}, asked, err
	}

	// a set keyed on a path that is no file stays in the answer, bound
	// wide, and out of the cache
	good, untold := s.treeInputs(l, call, fresh.Sets, files, nil)

	s.compareSets(l, call, cached, good)

	if hasRest(fresh.answer) {
		if err := s.o.Answers.Put(l.key(call+"-rest-bearing"), []byte("1")); err != nil {
			return fresh.answer, untold, err
		}
	}

	if !fresh.cacheable {
		return fresh.answer, untold, nil
	}

	answered := slices.DeleteFunc(slices.Clone(asked), func(p string) bool { return slices.Contains(untold, p) })

	return fresh.answer, untold, s.keep(ctx, l, call, files, units, good, answered)
}

func (s *Set) compareSets(l *Loaded, call string, cached, fresh []plugin.Set) {
	byID := map[string]plugin.Set{}
	for _, set := range fresh {
		byID[listID(set.Inputs, set.InputUnits)] = set
	}

	for _, c := range cached {
		f, ok := byID[listID(c.Inputs, c.InputUnits)]

		what := []string{"the cache holds this set; the plugin no longer answers it"}
		if ok {
			what = setDiff(c, f)
		}

		if len(what) > 0 {
			s.differ(Difference{Plugin: l.Conf.Name, Call: call, Inputs: c.Inputs, InputUnits: c.InputUnits, What: what})
		}
	}
}

func (s *Set) compareClaims(l *Loaded, cached, fresh map[string][]plugin.Claim) {
	for _, p := range slices.Sorted(maps.Keys(cached)) {
		if a, b := texts(cached[p]), texts(fresh[p]); !slices.Equal(a, b) {
			s.differ(Difference{Plugin: l.Conf.Name, Call: plugin.CallDetect, Inputs: []string{p}, What: []string{
				fmt.Sprintf("claims %v cached, %v fresh", a, b),
			}})
		}
	}
}

func setDiff(cached, fresh plugin.Set) []string {
	var out []string

	if cached.Group != fresh.Group || cached.Whole != fresh.Whole {
		out = append(out, fmt.Sprintf("group %q whole %t cached, %q whole %t fresh", cached.Group, cached.Whole, fresh.Group, fresh.Whole))
	}

	out = append(out, unitsDiff(cached.Units, fresh.Units)...)
	out = append(out, onlyOn("link", texts(cached.Links), texts(fresh.Links))...)
	out = append(out, onlyOn("can't tell", texts(cached.CannotTell), texts(fresh.CannotTell))...)
	out = append(out, onlyOn("digest from", texts(cached.DigestFrom), texts(fresh.DigestFrom))...)
	out = append(out, onlyOn("complete", sortedCopy(cached.Complete), sortedCopy(fresh.Complete))...)

	return out
}

func unitsDiff(cached, fresh []plugin.Unit) []string {
	was := map[string]plugin.Unit{}
	for _, u := range cached {
		was[u.ID] = u
	}

	is := map[string]plugin.Unit{}
	for _, u := range fresh {
		is[u.ID] = u
	}

	var out []string

	for _, id := range slices.Sorted(maps.Keys(was)) {
		f, ok := is[id]

		switch {
		case !ok:
			out = append(out, "unit "+id+": cached only")
		case was[id].Digest != f.Digest:
			out = append(out, fmt.Sprintf("unit %s: digest %s cached, %s fresh", id, was[id].Digest, f.Digest))
		case text(was[id]) != text(f):
			out = append(out, fmt.Sprintf("unit %s: %s cached, %s fresh", id, text(was[id]), text(f)))
		}
	}

	for _, id := range slices.Sorted(maps.Keys(is)) {
		if _, ok := was[id]; !ok {
			out = append(out, "unit "+id+": fresh only")
		}
	}

	return out
}

func onlyOn(what string, cached, fresh []string) []string {
	var out []string

	for _, c := range cached {
		if !slices.Contains(fresh, c) {
			out = append(out, what+" "+c+": cached only")
		}
	}

	for _, f := range fresh {
		if !slices.Contains(cached, f) {
			out = append(out, what+" "+f+": fresh only")
		}
	}

	return out
}

func texts[T any](values []T) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, text(v))
	}

	slices.Sort(out)

	return out
}

func text(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}

	return string(data)
}
