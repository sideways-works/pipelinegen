package plugins

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// bindUnit is one [index, kind, via, [vias]] entry of a bind record, one per bound unit: the
// common shape is read by hand, anything else as JSON values.
type bindUnit struct {
	index    int
	hasIndex bool
	kind     string
	via      int
	hasVia   bool
	vias     []int
}

func (u *bindUnit) UnmarshalJSON(b []byte) error {
	if fast, ok := scanBindUnit(b); ok {
		*u = fast

		return nil
	}

	return u.fromValues(b)
}

func (u *bindUnit) fromValues(b []byte) error {
	var entry []any
	if err := json.Unmarshal(b, &entry); err != nil {
		return fmt.Errorf("a bind unit: %w", err)
	}

	*u = bindUnit{}

	if len(entry) > 0 {
		n, ok := entry[0].(float64)
		u.index, u.hasIndex = int(n), ok
	}

	if len(entry) > 1 {
		if kind, ok := entry[1].(string); ok {
			u.kind = kind
		}
	}

	if len(entry) > 2 {
		n, ok := entry[2].(float64)
		u.via, u.hasVia = int(n), ok
	}

	if len(entry) > 3 {
		vs, _ := entry[3].([]any) //nolint:errcheck // not a list: no vias
		for _, x := range vs {
			if n, ok := x.(float64); ok {
				u.vias = append(u.vias, int(n))
			}
		}
	}

	return nil
}

func scanBindUnit(b []byte) (bindUnit, bool) {
	var u bindUnit

	sc := unitScanner{b: b}

	if !sc.byte('[') {
		return u, false
	}

	if sc.byte(']') {
		return u, sc.end()
	}

	var ok bool
	if u.index, ok = sc.int(); !ok {
		return u, false
	}

	u.hasIndex = true

	for field := 1; ; field++ {
		if sc.byte(']') {
			return u, sc.end()
		}

		if !sc.byte(',') {
			return u, false
		}

		switch field {
		case 1:
			if u.kind, ok = sc.str(); !ok {
				return u, false
			}
		case 2:
			if u.via, ok = sc.int(); !ok {
				return u, false
			}

			u.hasVia = true
		case 3:
			if u.vias, ok = sc.ints(); !ok {
				return u, false
			}
		default:
			return u, false
		}
	}
}

type unitScanner struct {
	b []byte
	i int
}

func (s *unitScanner) space() {
	for s.i < len(s.b) && (s.b[s.i] == ' ' || s.b[s.i] == '\t' || s.b[s.i] == '\n' || s.b[s.i] == '\r') {
		s.i++
	}
}

func (s *unitScanner) byte(c byte) bool {
	s.space()

	if s.i < len(s.b) && s.b[s.i] == c {
		s.i++

		return true
	}

	return false
}

func (s *unitScanner) end() bool {
	s.space()

	return s.i == len(s.b)
}

func (s *unitScanner) int() (int, bool) {
	s.space()

	start := s.i
	if s.i < len(s.b) && s.b[s.i] == '-' {
		s.i++
	}

	digits := s.i
	for s.i < len(s.b) && s.b[s.i] >= '0' && s.b[s.i] <= '9' {
		s.i++
	}

	if s.i == digits || (s.i-digits > 1 && s.b[digits] == '0') {
		return 0, false
	}

	if s.i < len(s.b) && (s.b[s.i] == '.' || s.b[s.i] == 'e' || s.b[s.i] == 'E') {
		return 0, false
	}

	n, err := strconv.Atoi(string(s.b[start:s.i]))

	return n, err == nil
}

func (s *unitScanner) str() (string, bool) {
	if !s.byte('"') {
		return "", false
	}

	start := s.i
	for s.i < len(s.b) && s.b[s.i] != '"' {
		if c := s.b[s.i]; c == '\\' || c < 0x20 || c >= 0x80 {
			return "", false
		}

		s.i++
	}

	if s.i == len(s.b) {
		return "", false
	}

	out := string(s.b[start:s.i])
	s.i++

	return out, true
}

func (s *unitScanner) ints() ([]int, bool) {
	if !s.byte('[') {
		return nil, false
	}

	var out []int

	if s.byte(']') {
		return out, true
	}

	for {
		n, ok := s.int()
		if !ok {
			return nil, false
		}

		out = append(out, n)

		if s.byte(']') {
			return out, true
		}

		if !s.byte(',') {
			return nil, false
		}
	}
}
