// Package pinedit changes the plugin pins of a pipelinegen.yaml in its text: a release's version
// and checksum in place, a new entry inserted after its siblings. Every other byte, comments
// and blank lines among them, stays as it was; the YAML is parsed only to find where.
package pinedit

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/plugkit/pin"
)

var ErrEdit = errors.New("can't edit the config")

// Doc is a config's text and the nodes of its plugins.
type Doc struct {
	data    []byte
	root    *yaml.Node
	plugins *yaml.Node
	key     *yaml.Node
}

func Parse(data []byte) (*Doc, error) {
	d := &Doc{data: data}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEdit, err)
	}

	if len(doc.Content) == 0 {
		return d, nil
	}

	d.root = doc.Content[0]
	if d.root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: its top level is no mapping", ErrEdit)
	}

	d.key, d.plugins = lookup(d.root, "plugins")

	return d, nil
}

func (d *Doc) Bytes() []byte { return slices.Clone(d.data) }

// Entry is a plugin's entry: its mapping, and the group holding it when it is in one.
type Entry struct {
	Node  *yaml.Node
	Group *yaml.Node
}

// Find is the entry named name.
func (d *Doc) Find(name string) (Entry, bool) {
	for _, item := range d.items(d.plugins) {
		if _, with := lookup(item, "with"); with != nil {
			for _, e := range d.items(with) {
				if scalar(e, "name") == name {
					return Entry{Node: e, Group: item}, true
				}
			}

			continue
		}

		if scalar(item, "name") == name {
			return Entry{Node: item}, true
		}
	}

	return Entry{}, false
}

// SetRelease rewrites the named plugin's release version and its checksum, under key (sha256 or
// sums), in place.
func (d *Doc) SetRelease(name, version, key, sum string) error {
	e, ok := d.Find(name)
	if !ok {
		return fmt.Errorf("%w: no plugin %s", ErrEdit, name)
	}

	_, rel := lookup(e.Node, "release")
	if rel == nil || rel.Kind != yaml.MappingNode {
		return fmt.Errorf("%w: plugin %s has no release to move", ErrEdit, name)
	}

	var edits []edit

	_, v := lookup(rel, "version")
	if v == nil {
		return fmt.Errorf("%w: plugin %s's release has no version", ErrEdit, name)
	}

	ed, err := d.replace(v, version)
	if err != nil {
		return err
	}

	edits = append(edits, ed)

	other := map[string]string{"sha256": "sums", "sums": "sha256"}[key]

	switch _, s := lookup(rel, key); {
	case s != nil:
		ed, err := d.replace(s, sum)
		if err != nil {
			return err
		}

		edits = append(edits, ed)
	default:
		k, s := lookup(rel, other)
		if s == nil {
			return fmt.Errorf("%w: plugin %s's release has neither sha256 nor sums", ErrEdit, name)
		}

		for n, text := range map[*yaml.Node]string{k: key, s: sum} {
			ed, err := d.replace(n, text)
			if err != nil {
				return err
			}

			edits = append(edits, ed)
		}
	}

	return d.apply(edits)
}

// Field is one key of an entry to add, in the order written.
type Field struct {
	Key   string
	Value any
}

// AddToGroup adds an entry to the group of repository, the first whose repository is the same
// one under any spelling, or to a new group at the end of the plugins.
func (d *Doc) AddToGroup(repository string, fields []Field) error {
	for _, item := range d.items(d.plugins) {
		_, with := lookup(item, "with")
		if with == nil || !sameRepository(scalar(item, "repository"), repository) {
			continue
		}

		if with.Style&yaml.FlowStyle != 0 || len(with.Content) == 0 {
			return fmt.Errorf("%w: the group of %s writes with inline; write it as a list, an entry a line", ErrEdit, scalar(item, "repository"))
		}

		last := with.Content[len(with.Content)-1]

		dash, err := d.dashColumn(last)
		if err != nil {
			return err
		}

		return d.insertAfter(last, dash, entryText(fields, dash, last.Style&yaml.FlowStyle != 0))
	}

	return d.addItem(func(dash int) string {
		return pad(dash) + "- repository: " + render(repository) + "\n" +
			pad(dash+2) + "with:\n" + entryText(fields, dash+4, true)
	})
}

// AddFlat adds an entry of its own at the end of the plugins.
func (d *Doc) AddFlat(fields []Field) error {
	return d.addItem(func(dash int) string { return entryText(fields, dash, false) })
}

func (d *Doc) addItem(text func(dash int) string) error {
	switch {
	case d.plugins == nil:
		return d.appendAtEnd("plugins:\n" + text(2))
	case d.plugins.Kind == yaml.ScalarNode && d.plugins.Tag == "!!null" && d.plugins.Value == "":
		return d.insertLines(d.endLine(d.key, d.key.Column-1), text(d.key.Column-1+2))
	case d.plugins.Kind != yaml.SequenceNode || d.plugins.Style&yaml.FlowStyle != 0 || len(d.plugins.Content) == 0:
		return fmt.Errorf("%w: plugins is written inline; write it as a list, an entry a line", ErrEdit)
	}

	last := d.plugins.Content[len(d.plugins.Content)-1]

	dash, err := d.dashColumn(last)
	if err != nil {
		return err
	}

	return d.insertAfter(last, dash, text(dash))
}

// entryText is an entry at dash, on one line in flow style or a key a line.
func entryText(fields []Field, dash int, flow bool) string {
	parts := make([]string, 0, len(fields))

	for _, f := range fields {
		parts = append(parts, f.Key+": "+render(f.Value))
	}

	if flow {
		return pad(dash) + "- {" + strings.Join(parts, ", ") + "}\n"
	}

	var b strings.Builder

	for i, p := range parts {
		if i == 0 {
			b.WriteString(pad(dash) + "- " + p + "\n")
		} else {
			b.WriteString(pad(dash+2) + p + "\n")
		}
	}

	return b.String()
}

// render is a value in flow style, on one line.
func render(v any) string {
	n := &yaml.Node{}
	if err := n.Encode(v); err != nil {
		return fmt.Sprint(v)
	}

	setFlow(n)

	out, err := yaml.Marshal(n)
	if err != nil {
		return fmt.Sprint(v)
	}

	return strings.TrimSpace(string(out))
}

func setFlow(n *yaml.Node) {
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		n.Style |= yaml.FlowStyle
	}

	for _, c := range n.Content {
		setFlow(c)
	}
}

func pad(n int) string { return strings.Repeat(" ", n) }

func sameRepository(a, b string) bool {
	ra, err := pin.ParseRepository(a, nil)
	if err != nil {
		return a == b
	}

	rb, err := pin.ParseRepository(b, nil)
	if err != nil {
		return false
	}

	return strings.EqualFold(ra.Origin+"/"+ra.Project, rb.Origin+"/"+rb.Project)
}

func (d *Doc) items(seq *yaml.Node) []*yaml.Node {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}

	var out []*yaml.Node

	for _, n := range seq.Content {
		if n.Kind == yaml.MappingNode {
			out = append(out, n)
		}
	}

	return out
}

func lookup(m *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil
	}

	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}

	return nil, nil
}

func scalar(m *yaml.Node, key string) string {
	if _, v := lookup(m, key); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}

	return ""
}

func (d *Doc) lines() [][]byte { return bytes.SplitAfter(d.data, []byte("\n")) }

// offset is the byte offset of a node's line and column, both from 1, the column in characters.
func (d *Doc) offset(line, column int) (int, bool) {
	lines := d.lines()
	if line < 1 || line > len(lines) {
		return 0, false
	}

	off := 0
	for _, l := range lines[:line-1] {
		off += len(l)
	}

	l := lines[line-1]
	for range column - 1 {
		if len(l) == 0 {
			return 0, false
		}

		_, w := utf8.DecodeRune(l)
		l, off = l[w:], off+w
	}

	return off, true
}

type edit struct {
	from, to int
	text     string
}

// replace swaps a scalar's text for text, written plain when it can be and else as YAML would.
func (d *Doc) replace(n *yaml.Node, text string) (edit, error) {
	from, ok := d.offset(n.Line, n.Column)
	if !ok || n.Kind != yaml.ScalarNode {
		return edit{}, fmt.Errorf("%w: no scalar at line %d", ErrEdit, n.Line)
	}

	var src string

	switch n.Style {
	case 0:
		src = n.Value
	case yaml.SingleQuotedStyle:
		src = "'" + strings.ReplaceAll(n.Value, "'", "''") + "'"
	case yaml.DoubleQuotedStyle:
		src = `"` + n.Value + `"`
	default:
		return edit{}, fmt.Errorf("%w: line %d writes %q in a style this edit doesn't change", ErrEdit, n.Line, n.Value)
	}

	if !bytes.HasPrefix(d.data[from:], []byte(src)) {
		return edit{}, fmt.Errorf("%w: line %d doesn't read %q where the YAML puts it", ErrEdit, n.Line, n.Value)
	}

	out := render(text)
	if n.Style == yaml.DoubleQuotedStyle || n.Style == yaml.SingleQuotedStyle {
		out = string(src[0]) + text + string(src[0])
	}

	return edit{from: from, to: from + len(src), text: out}, nil
}

func (d *Doc) apply(edits []edit) error {
	slices.SortFunc(edits, func(a, b edit) int { return b.from - a.from })

	data := slices.Clone(d.data)
	for _, e := range edits {
		data = slices.Concat(data[:e.from], []byte(e.text), data[e.to:])
	}

	return d.reparse(data)
}

func (d *Doc) reparse(data []byte) error {
	nd, err := Parse(data)
	if err != nil {
		return err
	}

	*d = *nd

	return nil
}

// dashColumn is the column, from 0, of the dash that starts a sequence item.
func (d *Doc) dashColumn(item *yaml.Node) (int, error) {
	lines := d.lines()
	if item.Line < 1 || item.Line > len(lines) {
		return 0, fmt.Errorf("%w: no line %d", ErrEdit, item.Line)
	}

	runes := []rune(string(lines[item.Line-1]))
	for i := min(item.Column-2, len(runes)-1); i >= 0; i-- {
		switch runes[i] {
		case '-':
			return i, nil
		case ' ':
			continue
		}

		break
	}

	return 0, fmt.Errorf("%w: line %d: the entry doesn't start on its dash's line", ErrEdit, item.Line)
}

// endLine is the last line, from 1, of what starts at n: the last line of anything in it and the
// lines after it indented deeper than indent, up to a blank line.
func (d *Doc) endLine(n *yaml.Node, indent int) int {
	last := maxLine(n)
	lines := d.lines()

	for last < len(lines) {
		next := string(lines[last])
		trimmed := strings.TrimLeft(next, " ")

		if strings.TrimSpace(next) == "" || len(next)-len(trimmed) <= indent {
			break
		}

		last++
	}

	return last
}

func maxLine(n *yaml.Node) int {
	m := n.Line
	for _, c := range n.Content {
		m = max(m, maxLine(c))
	}

	return m
}

func (d *Doc) insertAfter(item *yaml.Node, dash int, text string) error {
	return d.insertLines(d.endLine(item, dash), text)
}

// insertLines puts text after line, from 1.
func (d *Doc) insertLines(line int, text string) error {
	lines := d.lines()

	off := 0
	for _, l := range lines[:min(line, len(lines))] {
		off += len(l)
	}

	data := slices.Clone(d.data)
	if off == len(data) && len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
		off++
	}

	return d.reparse(slices.Concat(data[:off], []byte(text), data[off:]))
}

func (d *Doc) appendAtEnd(text string) error {
	data := slices.Clone(d.data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}

	if len(data) > 0 {
		data = append(data, '\n')
	}

	return d.reparse(append(data, text...))
}
