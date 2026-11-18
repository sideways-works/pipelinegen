package buildfile

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/bazelbuild/buildtools/build"
)

type kind int

const (
	kNone kind = iota
	kStr
	kList
	kDict
	// kExt is a name loaded from another repository's .bzl file, or its attribute: a tool from outside, never
	// a file here; str holds the module's label, an outside input that covers nothing here.
	kExt
)

// value is an expression's value in the static subset the reader follows, strings and lists and dicts of
// them; bad says why one is outside it, which makes a target holding it unreadable, never empty.
type value struct {
	kind kind
	str  string
	list []string // a list's items, or a dict's values
	keys []string // a dict's keys
	// kw are the arguments a macro's **kwargs holds, passed on by name.
	kw  map[string]value
	bad string
	// outside is set when a value from another repository went into this one: besides its strings it may
	// hold some nobody here can tell.
	outside bool
}

func str(s string) value          { return value{kind: kStr, str: s} }
func list(l []string) value       { return value{kind: kList, list: l} }
func unreadable(why string) value { return value{bad: why} }

func carry(out value, from ...value) value {
	for _, f := range from {
		if f.outside || f.kind == kExt {
			out.outside = true
		}
	}

	return out
}

func (v value) strings() []string {
	switch v.kind {
	case kStr, kExt:
		return []string{v.str}
	case kList, kDict:
		return v.list
	default:
		return nil
	}
}

func (v value) items() []string {
	if v.kind == kDict {
		return v.keys
	}

	return v.strings()
}

type env map[string]value

type scope struct {
	g    *Graph
	pkg  string
	file *bzlFile
	vars env
}

func (s scope) with(name string, v value) scope {
	vars := make(env, len(s.vars)+1)
	maps.Copy(vars, s.vars)

	vars[name] = v

	return scope{g: s.g, pkg: s.pkg, file: s.file, vars: vars}
}

func (s scope) eval(e build.Expr) value {
	switch x := e.(type) {
	case *build.StringExpr:
		return str(x.Value)
	case *build.LiteralExpr:
		return value{kind: kNone}
	case *build.ListExpr:
		return s.items(x.List)
	case *build.TupleExpr:
		return s.items(x.List)
	case *build.ParenExpr:
		return s.eval(x.X)
	case *build.BinaryExpr:
		return s.binary(x)
	case *build.ConditionalExpr:
		// either branch may hold: both count, which only ever adds inputs
		return s.join(s.eval(x.Then), s.eval(x.Else))
	case *build.DictExpr:
		return s.dict(x)
	case *build.Ident:
		return s.ident(x.Name)
	case *build.CallExpr:
		return s.call(x)
	case *build.Comprehension:
		return s.comprehension(x)
	case *build.DotExpr:
		// bin.tsc on a name from outside is still that outside thing
		if v := s.eval(x.X); v.kind == kExt || v.bad != "" {
			return v
		}

		return unreadable(build.FormatString(e) + " cannot be read without running Starlark")
	default:
		return unreadable(build.FormatString(e) + " cannot be read without running Starlark")
	}
}

func (s scope) items(exprs []build.Expr) value {
	out := make([]string, 0, len(exprs))
	res := value{kind: kList}

	for _, e := range exprs {
		v := s.eval(e)
		if v.bad != "" {
			return v
		}

		out = append(out, v.strings()...)
		res = carry(res, v)
	}

	res.list = out

	return res
}

func (s scope) binary(x *build.BinaryExpr) value {
	a, b := s.eval(x.X), s.eval(x.Y)
	if a.bad == "" && b.bad == "" {
		return carry(s.combine(x, a, b), a, b)
	}

	return s.combine(x, a, b)
}

func (s scope) combine(x *build.BinaryExpr, a, b value) value {
	switch {
	case a.bad != "":
		return a
	case b.bad != "":
		return b
	case x.Op != "+" && x.Op != "|":
		return unreadable(fmt.Sprintf("%s: the operator %s cannot be read without running Starlark", build.FormatString(x), x.Op))
	case a.kind == kStr && b.kind == kStr:
		return str(a.str + b.str)
	case a.kind == kDict && b.kind == kDict:
		return value{kind: kDict, keys: slices.Concat(a.keys, b.keys), list: slices.Concat(a.list, b.list)}
	case (a.kind == kList || a.kind == kNone) && (b.kind == kList || b.kind == kNone):
		return list(slices.Concat(a.list, b.list))
	default:
		// a select added to a list and the like: every string either side
		// names may be an input
		return list(slices.Concat(a.strings(), b.strings()))
	}
}

func (s scope) join(a, b value) value {
	if a.bad != "" {
		return a
	}

	if b.bad != "" {
		return b
	}

	return carry(list(slices.Concat(a.strings(), b.strings())), a, b)
}

func (s scope) dict(x *build.DictExpr) value {
	d := value{kind: kDict}

	for _, kv := range x.List {
		k, v := s.eval(kv.Key), s.eval(kv.Value)

		if v.bad != "" {
			return v
		}

		// a key that isn't a plain string (a config setting's label is
		// one) still names no input of the dict's own
		if k.bad == "" {
			d.keys = append(d.keys, k.strings()...)
		}

		d.list = append(d.list, v.strings()...)
		d = carry(d, v)
	}

	return d
}

func (s scope) ident(name string) value {
	if v, ok := s.vars[name]; ok {
		return v
	}

	if s.file != nil {
		if v, ok := s.file.vars[name]; ok {
			return v
		}

		if l, ok := s.file.loads[name]; ok {
			return s.g.loadedValue(l)
		}
	}

	switch name {
	case "True", "False", "None":
		return value{kind: kNone}
	}

	return unreadable(name + " is not defined where it is used")
}

func (g *Graph) loadedValue(l loaded) value {
	if strings.HasPrefix(l.module, "@") {
		return value{kind: kExt, str: l.module}
	}

	b := g.bzlAt(l.module)
	if b == nil {
		return unreadable(fmt.Sprintf("%s comes from %s, which is not a file of the repository", l.symbol, l.module))
	}

	if v, ok := b.vars[l.symbol]; ok {
		return v
	}

	return unreadable(fmt.Sprintf("%s is no top-level value of %s", l.symbol, l.module))
}

func (s scope) call(x *build.CallExpr) value {
	if dot, ok := x.X.(*build.DotExpr); ok {
		return s.method(dot, x)
	}

	id, ok := x.X.(*build.Ident)
	if !ok {
		return unreadable(build.FormatString(x) + " cannot be read without running Starlark")
	}

	switch id.Name {
	case "glob":
		return s.glob(x)
	case "select":
		if len(x.List) == 0 {
			return value{kind: kNone}
		}

		v := s.eval(x.List[0])
		if v.bad != "" {
			return v
		}

		return carry(list(v.strings()), v)
	case "list", "sorted", "depset", "tuple":
		if len(x.List) == 0 {
			return list(nil)
		}

		v := s.eval(x.List[0])
		if v.bad != "" {
			return v
		}

		if v.kind == kExt {
			return outsideItems(x)
		}

		return carry(list(dedup(v.items())), v)
	}

	return unreadable(id.Name + "(...) cannot be read without running Starlark")
}

func (s scope) method(dot *build.DotExpr, x *build.CallExpr) value {
	v := s.eval(dot.X)
	if v.bad != "" {
		return v
	}

	switch {
	case dot.Name == "keys" && v.kind == kDict:
		return list(v.keys)
	case dot.Name == "values" && v.kind == kDict:
		return carry(list(v.list), v)
	}

	return unreadable(build.FormatString(x) + " cannot be read without running Starlark")
}

func (s scope) glob(x *build.CallExpr) value {
	var include, exclude value

	for i, arg := range x.List {
		switch a := arg.(type) {
		case *build.AssignExpr:
			if lhs, ok := a.LHSIdent(); ok {
				switch lhs.Name {
				case "include":
					include = s.eval(a.RHS)
				case "exclude":
					exclude = s.eval(a.RHS)
				}
			}
		default:
			if i == 0 {
				include = s.eval(a)
			}
		}
	}

	if include.bad != "" {
		return include
	}

	if exclude.bad != "" {
		return exclude
	}

	return list(s.g.glob(s.pkg, include.strings(), exclude.strings()))
}

// comprehension evaluates [body for name in xs] and {key: value for ...} over known lists; a condition it
// can't decide makes the whole comprehension unreadable rather than guessed.
func (s scope) comprehension(x *build.Comprehension) value {
	scopes, bad := s.clauses(x)
	if bad.bad != "" {
		return bad
	}

	out := value{kind: kList}
	if x.Curly {
		out.kind = kDict
	}

	for _, sc := range scopes {
		if kv, ok := x.Body.(*build.KeyValueExpr); ok {
			k, v := sc.eval(kv.Key), sc.eval(kv.Value)
			if k.bad != "" {
				return k
			}

			if v.bad != "" {
				return v
			}

			out.keys = append(out.keys, k.strings()...)
			out.list = append(out.list, v.strings()...)
			out = carry(out, v)

			continue
		}

		v := sc.eval(x.Body)
		if v.bad != "" {
			return v
		}

		out.list = append(out.list, v.strings()...)
		out = carry(out, v)
	}

	if out.kind == kDict {
		out.keys = dedup(out.keys)
	}

	return out
}

func (s scope) clauses(x *build.Comprehension) ([]scope, value) {
	scopes := []scope{s}

	for _, c := range x.Clauses {
		if ic, ok := c.(*build.IfClause); ok {
			var kept []scope

			for _, sc := range scopes {
				keep, ok := sc.test(ic.Cond)
				if !ok {
					return nil, unreadable(build.FormatString(x) + ": a comprehension's if other than string tests (startswith, endswith, in, ==, not) cannot be read without running Starlark")
				}

				if keep {
					kept = append(kept, sc)
				}
			}

			scopes = kept

			continue
		}

		fc, ok := c.(*build.ForClause)
		if !ok {
			return nil, unreadable(build.FormatString(x) + ": a comprehension clause that cannot be read")
		}

		id, ok := fc.Vars.(*build.Ident)
		if !ok {
			return nil, unreadable(build.FormatString(x) + ": only a comprehension over one name can be read")
		}

		var next []scope

		for _, sc := range scopes {
			over := sc.eval(fc.X)
			if over.bad != "" {
				return nil, over
			}

			if over.kind == kExt {
				return nil, outsideItems(x)
			}

			for _, item := range over.items() {
				next = append(next, sc.with(id.Name, str(item)))
			}
		}

		scopes = next
	}

	return scopes, value{}
}

// outsideItems is iterating over a name from another repository: only running that repository's Starlark
// knows what it holds, and it may well hold this tree's files.
func outsideItems(x build.Expr) value {
	return unreadable(build.FormatString(x) + " iterates over a value from another repository, which cannot be read without running Starlark")
}

func dedup(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))

	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}

	return out
}

// test decides a comprehension's condition made of string tests on known values: startswith, endswith (a
// string or tuple), in (a string or list), == and !=, joined by not, and, or; false for anything else.
func (s scope) test(e build.Expr) (result, ok bool) {
	switch x := e.(type) {
	case *build.ParenExpr:
		return s.test(x.X)
	case *build.UnaryExpr:
		if x.Op != "not" {
			return false, false
		}

		v, ok := s.test(x.X)

		return !v, ok
	case *build.BinaryExpr:
		return s.binaryTest(x)
	case *build.CallExpr:
		return s.affixTest(x)
	default:
		return false, false
	}
}

func (s scope) binaryTest(x *build.BinaryExpr) (result, ok bool) {
	switch x.Op {
	case "and", "or":
		a, okA := s.test(x.X)
		b, okB := s.test(x.Y)

		if !okA || !okB {
			return false, false
		}

		if x.Op == "and" {
			return a && b, true
		}

		return a || b, true
	}

	a, b := s.eval(x.X), s.eval(x.Y)
	if a.bad != "" || b.bad != "" || a.kind != kStr {
		return false, false
	}

	switch x.Op {
	case "==", "!=":
		if b.kind != kStr {
			return false, false
		}

		return (a.str == b.str) == (x.Op == "=="), true
	case "in", "not in":
		var found bool

		switch b.kind {
		case kStr:
			found = strings.Contains(b.str, a.str)
		case kList, kDict:
			found = slices.Contains(b.items(), a.str)
		default:
			return false, false
		}

		return found == (x.Op == "in"), true
	default:
		return false, false
	}
}

func (s scope) affixTest(x *build.CallExpr) (result, ok bool) {
	dot, isDot := x.X.(*build.DotExpr)
	if !isDot || len(x.List) != 1 || (dot.Name != "startswith" && dot.Name != "endswith") {
		return false, false
	}

	v, arg := s.eval(dot.X), s.eval(x.List[0])
	if v.bad != "" || arg.bad != "" || v.kind != kStr {
		return false, false
	}

	var affixes []string

	switch arg.kind {
	case kStr:
		affixes = []string{arg.str}
	case kList:
		affixes = arg.list
	default:
		return false, false
	}

	has := strings.HasPrefix
	if dot.Name == "endswith" {
		has = strings.HasSuffix
	}

	return slices.ContainsFunc(affixes, func(a string) bool { return has(v.str, a) }), true
}
