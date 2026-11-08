# The stored graph and the outward walk

This page is the contract for the planner's rebuild, for people building
any part of it: the links side of the core, the Go correlator's links, the
store, the walk and the tests that hold it equal to today's binding. It
says what the graph holds, what each link kind does at each width, what
counts as changed, what the walk answers, and what the store keeps for it.
`internal/graph` carries it out.

Before the walk, each plan bound every slice forward: the Go correlator
type-checked the whole tree per call, the core closed every slice over the
links, and the result went into a Bazel repository for bazel-diff to hash.
A build with the `pipelinegen_forward` tag still plans that way, for the
proof's comparison. The work
grows with the repository, not with the change. The rebuild keeps what the
plugins said, per file and per set, and plans outward: it finds the units
that changed and walks backwards from them to the slices that bind them.
What a plugin said about a file is worked out once and then kept up to
date: a commit asks again only about the files it changed.

## Which paths are hot

Two kinds of repository set the hot paths:

- A repository with no sbom or correlator plugin, such as the tool's
  own: every suite is bound by the Bazel graph, and its test files are
  selected through the join with bazel-diff on the main repository
  described under the API. Its pipeline is the first place a regression
  in that join shows.
- A monorepo with a Go backend and browser tests, at narrowing level 2
  and `follow: executed` everywhere, with no `follow_depth`. Its recorded
  suites are the browser tests (four correlators: Go, SQL, the API
  contract, React) and Go suites (three correlators); its suites of tagged
  checks are graph-bound.

So the hot path is the executed width at level 2: the setup split, the
sample and `sample-bound` are on, `unreached-new` is on (unbound names
play no part), the method-set and initialiser narrowings are on. The
default level 3 makes unbound names hot for any new repository. The
dependents width, `follow_depth`, level 5 and the implementation join are
corner cases; the walk keeps their rules all the same.

## The graph

### Nodes

A node is one of:

- a unit an sbom listed (`go:...`, `ts:...`, `pg:...`, `api:...`,
  `dep:...`), with its digest, group and file;
- `file:<path>`, the core's unit for a tracked file;
- `group:<group>`, the core's node for a group as units name it in their
  `group` (a Go package's directory). A slice that reaches a group's node
  expanded *links* that group: the group's acting declarations, its
  modules and its whole-package binding hang off it;
- `node:<namespace>:<rest>`, a junction a plugin needs that is no unit:
  a directory's test binary (`node:go:test:<dir>`), the meeting point of
  an interface method and its implementations
  (`node:go:impl:<method and signature>`). Any plugin may name one; the
  namespace prefix keeps two domains apart. A junction has no digest,
  never changes, is never dangling and is never one of a group's names.

`group:` and `node:` give correlators the endpoints they need while
keeping the rule that a correlator owns no units: nothing in those
namespaces has content that could change.

A link end that no sbom lists, outside `file:`, `group:` and `node:`, is
dangling, typed or plain: a naming convention that didn't hold.
`plugins.Set.Tree` marks the link's `from` end as one that can't be told,
so it binds its group wide, and the plan's warnings name the plugin and
the link. The walk then needs no rule of its own for it.

### Links and their kinds

Links come in two fields of `deps.Tree`.

`Links` are plain links, whatever their kind: `uses`, `reads`, `writes`,
`generates`, `changes`, or a plugin's own word. The forward closure has
always followed every one of them transitively, so the walk reads all of
them as follow links that are also walked backwards at the dependents
width, which is what they mean today.

`Typed` are links whose kind the walk reads apart. They stay out of
`Links` so that today's forward closure, the oracle, never follows them as
plain links. `graph.Load` reads both fields; nothing else maps kinds.

| Kind | Executed width | Dependents width |
|---|---|---|
| plain (`Links`) | target bound and expanded | the same, and the link is also walked against its direction, one step of depth |
| `follow` | target bound and expanded | target bound and expanded, one step of depth |
| `bind` | target bound, not expanded | target bound and expanded, one step of depth |
| `dependents-only` | nothing | target bound and expanded, one step of depth |

An unknown typed kind is read as plain, the widest: a typo selects more,
never less.

What "expanded" means:

- At the executed width a unit is *bound* or *bound and expanded*. Plain
  links leave every bound unit. Typed links leave only expanded units.
  A unit reached both ways is expanded. Roots are expanded.
- At the dependents width every unit the executed width binds is expanded
  at depth 0, and every unit reached from there is expanded too.

The Go correlator's rules map as follows, from its `graph/bind.go`:

- A reference to a type, a constant, a variable or a test function is a
  `follow` link.
- A reference to a function without statements is `bind`.
- A reference to a function with statements is `dependents-only`. At the
  executed width it binds nothing, since it runs only if it was recorded.
- A type to its methods: a `bind` link to each method without
  statements, always. A `bind` link to each method with statements,
  tagged wide under `method-sets`. A test method in a method set is
  `follow`, since test code has no coverage. The test type's own rule
  (all methods while `test-method-sets` is off or the type escapes to an
  interface) is a wide link under `test-method-sets`, with the escape
  decided by the plugin.
- `dependents-only` to every method, for the dependents width.
- A type a test file declares that doesn't escape: its references point
  to its `typedecl` unit. Two wide links `typedecl → type`, one under
  `method-sets` and one under `test-method-sets`, bring the full type
  back when either is off. A test type that escapes keeps an untagged
  `follow` link `typedecl → type`, since its sbom can't tell from syntax
  which types escape and marks every twin alike (below).

### Narrowing tags

A typed link uses the tags plain links already have: `narrowing` names
the narrowing, and `while` says when the link holds. `while: off` (the
default) is a wide link that holds while the narrowing is off. `while: on`
is a narrow link that replaces it while the narrowing is on. A link that
holds "unless N is on" is `narrowing: N, while: off`. A narrow rule may
also be written as an untagged link for what holds at every level plus a
wide link for what only the wide rule adds; it selects the same at every
level, its left-out records included. The core drops what the level
rules out into
`TypedRuledOut`, per narrowing, as it does for plain links into
`RuledOut`, and a narrowing's left-out record is the walk with those
links put back.

### Groups: linking, starting, whole packages, modules

A Go binary runs the initialisers of every package it links, so linking
is part of binding:

- each declaration links its package: `follow` to `group:<its dir>`;
- a package links what it imports: `follow` from `group:<dir>` to
  `group:<imported dir>`, for imports inside the tree;
- a package starts its acting declarations: `follow` from `group:<dir>`
  to each init, and to each variable whose initialiser may act: an
  untagged link to a variable whose initialiser calls outside
  `pure_calls`, and a wide link under `pure-initialisers` to one whose
  initialiser calls only functions in that list;
- a package bound whole (cgo, `go:linkname`, a function without a body,
  reflection by name, non-Go files, a build-excluded file): `bind` from
  `group:<dir>` to every declaration it has. These are links, not a group
  property, so whole binding needs nothing new in the core;
- modules: `bind` from `group:<dir>` to each `dep:go:<module>` the
  package itself depends on, external modules' own dependencies
  included, `while: on` under `module-bumps`; the import links between
  groups carry the rest of the binary's closure. With `module-bumps` off, the wide rule
  binds every module the package's main module requires: one `follow`
  link from `group:<dir>` to `node:go:module:<main module>`, `while: off`,
  and `bind` links from that junction to each required module, so the
  edges grow with modules, not with packages times modules;
- a test binary links more than its package: the slice's own test file
  links `follow` to `node:go:test:<dir>`, which links `follow` to
  `group:<dir>`, to the test imports' groups, and to TestMain, the test
  files' inits and their acting variables (the last under
  `pure-test-variables`, as for initialisers). Its test-only modules hang
  off it too.

The import links are why no `imports` kind is needed: a group's node binds
nothing by itself, since it has no digest, so a `follow` link into it
only means that the package is linked.

### Implementations

At the dependents width an interface call reaches every method that
structurally matches it in the tree. No single package's set can see all
of them. So the meeting point is a junction: the declaration that makes
an interface call (interface methods are no units) links
`dependents-only` to `node:go:impl:<method shape>`, the shape being the
name, the numbers of parameters and results, whether it is variadic, and
generics by name only; and each
package's set links that junction `dependents-only` to its own matching
methods. Today the Go correlator matches a concrete type against the whole
interface before it binds the method. A junction per method shape also
reaches methods of types that share that one shape without implementing
the interface, so the walk selects a superset of today's at the
dependents width. The equivalence tests accept that as an
explained class of their own: a slice the walk selects beyond the oracle
over a path through `node:go:impl:`. Links at the call sites where a value
is converted to the interface would be exact; that is a later step with
its own soundness argument.

Today the Go correlator follows an implementation only into packages the
slice's binaries link. A typed link carries that condition as
`"if_linked": true`: it holds only for slices that link the group of its
`to`. Plugins carry the flag, and the core treats such links as
unconditional. That over-selects only at the
dependents width (and level 5), which is always safe. An exact check per
candidate slice can come later without any plugin changing, once a
measurement says it matters.

### Depth

A hop the depth counts is a step of the dependents width: a typed link
followed at that width from a unit beyond the executed binding, or a
plain link taken against its direction. A hop into a `node:` junction
costs nothing, so the implementation join counts once. `follow_depth`
bounds one cumulative count over both kinds (0 is no bound), counted from
the executed binding. In the outward walk the walk runs against the
links, so the hop that costs a step is the one taken *along* a plain
link, from `From` to `To`; the free hop is the one against it.

The forward oracle counts the same way.

## A slice's roots

A recorded slice is rooted, expanded, at:

- every unit in its list (the units it touched), and with the setup split
  off, its suite's setup list too;
- the units its data names (`extra_deps`, `pipelinegen:data` lines);
- each of its own files: `file:<src>`, the units of a file bound by its
  units (`FileUnits`), and the units whose group is the file;
- the units declared in its own files: `Unit.File == src`. A test entry
  function the slice doesn't run is left out: an sbom marks an entry with
  the name a test runner selects it by, in the unit's `entry` field
  (`"entry": "TestCreate"`; for Go, functions named `Test`, `Benchmark`,
  `Fuzz` or `Example` in a test file, never `TestMain`), and
  when the slice names its `tests`, an entry outside them is no root. The
  core needs no language rules for this. A type with a `typedecl` twin is
  not a root itself; its sbom marks it `"not_root": true`. The slice
  roots at the twin, and the correlator's links from the twin bring the
  type where the narrowings or an escape call for it.

With the setup split on, a suite's setup is a slice of its own,
`_setup`, rooted at the setup list. A slice without a list is selected
when any unit changed, as through `:all` today; an unchanged tree selects
nothing. A slice's own test file that no plugin lists units in (a shell
or YAML test) is a unit of its own, `file:<path>`, its digest the file's
content, added to the tree before the walk takes it. So a change to that
file selects its slice and every slice without a list, while a changed
file that no slice owns and no unit holds (a README, a fixture) selects
neither. A graph-bound slice is rooted at its own files' units and its
data units, and its Bazel side stays with bazel-diff.

## What changed

### Keeping the stored graph up to date

What the plugins said is kept by content, so planning a commit asks only
about what the commit changed:

1. The sboms first. A per-file answer is keyed by the file's blob; a
   per-set answer by its inputs' blobs. A changed, added or removed file
   goes back to its sbom; its units are replaced, and a removed file's
   units are gone (absent is a value).
2. Digests resolved, with the sboms' own `digest_from` only.
3. The correlators, keyed on their sets' inputs: the input files' blobs,
   and the digests of the set's `input_units`. A set may name units it
   depends on instead of files: the Go correlator names one API unit per
   package in a set's import closure (the package's declarations without
   bodies, listed by go-sbom from syntax). So a body-only change in a core
   package asks one set again, and an API change asks its importers. An
   input unit no sbom lists is "absent" in the key only when an sbom that
   answered covers its namespace; otherwise the set is a miss and isn't
   stored. A set that is stale through an input unit alone is asked
   about through the input that the fewest sets hold (the package's own
   file), never through a `go.mod` that every set of the module holds.
4. Then the correlators' `digest_from`, and the assembly into a
   `deps.Tree` per tree.

The order is a guarantee the walk rests on: input units are keyed on
digests the sboms already settled.

An answer is written once under its key. When an ordinary plan computes
an answer whose key already holds other bytes, that is a fault too (an
undeclared input, or a plugin that isn't deterministic): the plan goes
on with its own fresh answer for its run, overwrites nothing, and warns
once, naming the plugin and the key.

The map from files to what was said about them lives in the `deps.Tree`
as `Unit.File` (the units each file holds) and in the cache's member
records (the sets that read a file, or an input unit). The store keeps no
per-tree index: a tree's index is its git tree (path to blob) together
with the objects keyed by plugin build and blob or set inputs. Member
records become one write-once file per (member key, answer), read as the
union of a directory, so two plans writing at once lose nothing.

A permanent test with counted calls holds this. After a commit changing
k files, exactly those files' unit answers and the sets naming them are
asked again, and nothing else. A body-only change in a core package asks
one links set again. A change that leaves every unit's digest as it was
(a comment, formatting) asks its sbom again and selects nothing through
the graph. A set keyed by files is asked again on such a change, since
its file's blob changed, but its links come back the same. A set keyed
only by units is not asked.

### The one definition of changed

`graph.Changed` is what the walk starts from, and nothing else decides
"changed". It is decided per width:

- a unit whose digest differs between the trees, or that exists in one
  tree only (a group's `:all` changed when any of its units changed,
  appeared or went, and a whole group is bound through its units, so this
  covers it);
- the `from` end of a link, plain or typed, that is in one tree only,
  where the width follows the link and its other end did not change
  itself. A link between two unchanged units can come and go: a blank
  import, a promoted method that moves when an embedding changes in
  another file. Counting its `from` end keeps the walk correct whatever a
  correlator does, at the cost of a rare extra selection where today's
  binding reached the same units another way. Two kinds of such links
  don't count. A `dependents-only` link doesn't count at the executed
  width, which never follows it, so no slice's binding there changes with
  it. A link whose other end is a unit that changed itself (its digest
  differs, or one tree only has it) doesn't count either: the walk
  starts from that unit and goes back over the link in whichever tree has
  it, so every slice that follows the link in either tree is selected
  through the unit, and the plan names the unit and the link. A type
  whose methods are added or removed is the common case: its links to
  the methods come and go with the methods, and only the slices that
  reach a method that changed are selected, not every slice that names
  the type. A link to an unchanged unit, a group, a junction or an id no
  tree lists as a unit still counts. Where a suite's sample takes the
  changed unit, the `from` ends of the links to it in one tree only count
  for that suite's slices after all, since the sample takes the unit and
  not the links that lead to it. The conformance kit warns about a
  plugin whose links change while neither end does, so such cases stay
  rare. The equivalence tests compare accordingly: the walk selects
  everything the oracle selects, and every slice it selects beyond that
  traces to a link present in one tree only;
- a plugin's pin (version, checksum, config). An sbom's pin counts as a
  change of every unit it lists that didn't change in its own right. A
  correlator's pin selects the recorded slices of the suites it observes
  (`Options.Observes`), whose recordings it makes, and the walk goes back
  from the near end of every link it gave (in force or ruled out, kept in
  the tree as `CorrelatorEnds`) and from every unit of a file it claims
  (`CorrelatorFiles`), with a step to its pin, so a slice whose binding
  holds one of them is selected too (a slice bound by the Bazel graph
  only through the units its data names, as for any other change). While `plugin-entry` is on, neither selects a recorded
  slice or a suite's setup: the suite's `_sample-entry.<suite>` target is
  selected instead, which runs the suite's sample, and each such slice
  nothing else selects is left out under `plugin-entry`. A unit that
  changed in its own right, by digest, presence or links, is a change as
  ever;
- an untold file of an sbom (a whole call that couldn't tell), as its
  `file:` unit. A correlator that couldn't tell in either tree selects
  every recorded slice it observes;
- a group whose unbound names differ, as a change of `group:<group>`
  (below);
- a unit with a group, in both trees, that can't be told in one of them
  only. A unit that can't be told binds its whole group, so a slice that
  binds it binds the group whole in one tree and not in the other, even
  where nothing it reached changed. The common case is a link whose
  target went away: the link stays in the head, dangling, and its source
  can't be told there. A slice that binds the source without expanding it
  never reaches the removed target, so the target's removal alone would
  not select it. Like a link in one tree only, this changes no unit's
  digest, so it doesn't select a slice without a list.

### The nightly rebuilds the graph cold

Keeping the graph up to date rests on every answer depending only on
its declared inputs. A plugin that reads a file it didn't declare, or a
key or an invalidation in the core that misses something, leaves a
stale answer in the cache that nothing else would notice. So a run that
selects everything and records (the nightly) builds the head tree's
graph cold, after its selection is made:

- every plugin is asked about every file and every set, and no cached
  answer is read;
- the result is compared with the graph the caches give for the same
  tree: per sbom and file, the units with their digests, groups, files,
  `entry` and `not_root`; per correlator and set, its inputs and input
  units and its links with their kinds, tags, `ref` and `if_linked`;
  `digest_from`; and the can't-tell marks;
- each difference is reported per plugin, naming the file or set and the
  unit or link that differs;
- the fresh answers are written to the store, a whole generation's
  objects in one push. A stored answer that differs is replaced only
  after the difference is reported. The comparison reads the shared
  level's answers and member records, past the local directory, so it
  sees what other machines read; without a shared level it reads the
  local directory. Member records grow by design, so they are compared
  as sets of answers. Every set whose stored answer still holds is
  compared, including one a warm plan would ask about again because it
  shares an input with a set that changed.

A difference is always a fault, of a plugin or of the core, and never
repairs itself in silence: it turns the run red. A config setting,
`rebuild_check`, makes it `error` (the default) or `warning`, for an
adopter in the middle of a migration. A plan that selects everything
loads no plugins for its selection today, so the rebuild is a step of
its own in the nightly's plan job. It never runs on pull requests.

It costs the plugins' full analysis of one tree once a night, what a
first plan on an empty cache costs (for the Go correlator, a type check
of every package). It is also the real-tree counterpart of the
conformance kit's cold and warm check.

Its guarantee test: a fake plugin whose answer depends on a file it
doesn't declare gives the same graph warm after that file changes; the
cold rebuild differs, and the comparison names the set. A plugin that
keeps its declared inputs compares equal.

## The walk

### Reaching a slice

The walk is backwards over the union of both trees' links, from the
changed nodes, with states (node, expanded?, depth used). At the executed
width, a node's predecessor over any link that binds it (plain, `follow`
or `bind`) is a binder and must be expanded. An expanded node's
predecessors over plain and `follow` links are expanded too. At the
dependents width every link kind counts, and depth is used as above. A
0-1 breadth-first order takes the fewest steps first. A slice is selected
when one of its roots is reached in an expanded state, or, for a unit
root, reached at all.

A unit that can't be told binds its whole group: a change to any unit of
that group reaches it, as if each unit of the group linked to it. A unit
that any set of either tree names as an input unit, and that no file
declares (a Go package's API unit), is not part of that: it keys its
importers' sets, and its digest changes only with a declaration's API,
which selects through that declaration. An input unit a file declares (a
function, a type) is part of it, like any declaration of the group.

The walk keeps one predecessor per state, so the reason for a selection
is the path from the slice's root to the changed node, as `[]Step`.

### Linked groups and unbound names

While `unreached-new` is off (from level 3), a slice depends on the names
nothing binds in each group it links: a name that appears or goes selects
it, while a changed body of a name nothing binds doesn't. "Nothing binds"
is over all slices, so the walk works it out per tree and width with one
forward walk from every recorded slice's roots together. A group's names
are the ids of its units, without `file:` and `node:` ids and without the
units no file declares that any set names as input units: a declared
input unit, a function say, is a name like any other. With
`referenced-new` on, a name that a unit bound at the executed width
references counts as bound, whether that unit is expanded or not: a link
of any kind marked `"ref": true`, which the Go correlator puts on every
link that comes from a syntax reference (calls included, never a method
set or an implementation). Where a group's unbound names
differ between the trees, `group:<group>` counts as changed, and the
slices that link the group are selected: those whose executed binding
reaches the group's node expanded. A slice links a group by its executed
binding at both widths, since a binary links the same packages either
way; the walk and the Go correlator both do so.

### Narrowings

For each narrowing in force, a second walk adds back the links it ruled
out (`RuledOut`, `TypedRuledOut`) and its wide rule. A slice only that
walk selects was left out by that narrowing, with the path that would have
selected it. That path runs through the links the narrowing ruled out, so
it holds in the trees with those links put back. A slice that several
such walks select is named under each of those narrowings, as the plan's
`narrowed` records from bound slices do.

### The sample

The walk owns the sample, and `toSample` stays as its oracle. Today's rule
is about how each slice binds a unit, not about whether the unit reaches
every slice, so the walk computes it the same way, for a changed unit
only. Both narrowings are off from level 3, so they act together or not
at all. For each suite with at least two recorded slices, in each width:

- a changed unit that every recorded slice of the suite has in its list
  goes to the suite's sample (`sample`);
- with `sample-bound` on as well, so does a changed unit that every
  recorded slice binds, where in each slice it is bound only because it
  runs at start-up (a `follow` link from a group's node) or only through
  units already in the sample, over plain and `follow` links. This is the
  least fixpoint `toSample` computes: a unit on a cycle that no slice
  reaches but through the sample stays each slice's, and so does a unit
  bound over a `bind` link, as today where a bind correlator gave no vias.

The walk decides this in the changed unit's cone, the nodes from which a
walk back from it reaches it at the executed width, without walking the
cone of every changed unit: a changed pin of an sbom makes every unit it
lists a change, tens of thousands on a large tree, and a walk per change
with each slice's binding worked out in it took hours. Instead, per tree
and suite, each slice's closure (what it binds, and which of that it
expands) is worked out once, and a changed unit that some slice doesn't
bind stays each slice's. One that every slice binds goes to the sample
when every slice lists it. Otherwise only `sample-bound` can take it, and
only when no slice lists it or holds it for another reason. Over the
whole tree, a least fixpoint of a looser rule (one that passes over
`bind` links, which may come from outside a cone) holds every unit that
`sample-bound` can take in any cone, so a changed unit outside it stays
each slice's. For one inside it, the fixpoint runs on the units it is
bound through, backwards from it, where inside its cone each slice binds
a unit as its closure has it; the cone itself is worked out only when a
`bind` link into one of those units is one a slice takes. With fewer
changes than slices, whether every slice binds a change is told by its
cone and the slices' roots instead. What goes to the sample is the same
in both widths, so it is decided once per plan and per narrowing the
walk puts back. The first form, a walk per change, stays in the tests
as the oracle the decision is compared with, on generated trees with
every unit changed by its pin. A changed unit that goes to the
sample selects the suite's sample target and none of the suite's slices
through that unit, whatever other way a slice binds it, as `toSample`
takes it out of every slice's units. The one exception is a slice that
binds the unit's whole group: it is still selected through the group.

The sample takes the unit, not what its links bind. When a unit every
slice runs changes so that it links something else (a function every
test runs now references another type), a slice that bound the old
target only through it, or binds the new one only through it, binds
other units than before, and `toSample` leaves those in the slice. So
for each link in one tree only that leaves a sampled unit, and at the
executed width for what that link's end binds only when expanded (its
`follow` links onward, its `bind` links), the walk goes back from the
end once over each tree's links alone, and selects a slice that binds the
end in one tree only. The path runs from the slice's root through the
sampled unit to the end. Past 64 such ends the walk stops comparing them
and keeps the sampled unit as a change of its own, which selects every
slice that binds it: more than `toSample`, never less.

With `sample-bound` on, a link that comes or goes can also move a unit
that didn't change from a slice to the suite's `sample-bound` target, or
back, and `toSample`'s split then differs between the trees where the
walk selects nothing. Such a move cannot hide a test: the slice binds no
unit that changed, by digest, presence or a link the width counts, and
every unit it lost or gained is bound to the sample's target in the
other tree, so what the slice runs and what it runs against are the same
in both trees. The test that moves links from unchanged units over random
trees accepts that difference only after checking exactly this, slice by
slice.

The setup slice is no slice the sample is drawn from: it keeps every unit
it binds, and a change the sample takes still selects it.

In a width where every slice the sample is drawn from is selected, the
suite's sample targets (`sample`, and `sample-bound` when it is on) are
selected too. Running them then costs nothing. It also covers the
dependents width, where `toSample` reads the dependents binding's first
reason for each unit, in the order the closure visited it: a unit can
join a sample target there because another unit's links moved, in a way
the walk, which uses each slice's executed binding, doesn't trace. On
synthetic trees that happened only where every slice of the suite was
selected anyway.

### Pseudo-targets

The planner already knows a suite's own targets by name, and the
selection keeps them: `_setup.<suite>` (the setup slice under the setup
split), `_sample.<suite>` and `_sample-bound.<suite>` come back as slices
of their suite, so the plan's sampling and reasons work as they do.

## The API

```go
type Key struct{ Suite, Name, Width string }

// Step is one hop of a path that runs from a slice's root towards a
// changed node. From and To are the link's own ends, as the plugin gave
// them. Kind is the link's kind, or one of "pin", "untold", "everything",
// "cannot-tell", "names" (a linked group's unbound names), "sample" for
// a step the core adds. Back marks a plain link the path takes against
// its direction, from To to From: a step of the dependents width.
type Step struct {
	From, To, Kind string
	Back           bool
}

// Path is one reason: the root it starts at, the changed node it ends at.
type Path struct {
	Listed, Changed string
	Steps           []Step
}

type Selection struct {
	// Selected are the slices selected, pseudo-targets among them, each
	// with up to Options.Paths shortest paths, in a fixed order; no path
	// for a slice without a list. A recorded slice selected through a
	// correlator has one path with no root and one step to the
	// correlator's pin unit: of kind "pin" when its pin changed and it
	// observes the slice's suite, of kind "everything" when it couldn't
	// tell in one tree. A slice whose binding holds an answer of a
	// correlator whose pin changed has a path from its root to that
	// answer and a "pin" step on to the pin unit.
	Selected map[Key][]Path
	// LeftOut are, per narrowing in force, the slices only it left out.
	LeftOut map[string]map[Key][]Path
}

// Hits are one suite's selected slices in one width, sorted by name.
func (s *Selection) Hits(suite, width string) []Hit

type Hit struct {
	Slice string
	Paths []Path
}

func Changed(base, head *deps.Tree, slices []plugins.BindSlice, width string, o Options) ([]Change, error)
func Select(ctx context.Context, base, head *deps.Tree, slices []plugins.BindSlice, widths []string, o Options) (*Selection, error)
```

The input is the same `[]plugins.BindSlice` the oracle gives
`plugins.Set.Bind`, with the lists as recorded: the sample split is the
walk's, never applied to the lists first. The level comes through the
trees (what is in force is in `Links` and `Typed`, what was ruled out in
`RuledOut` and `TypedRuledOut`) and through `Options.On`. Each slice's
depth is its `BindSlice.Depth`. Later, for the plan book, paths can be
kept as indexes into the graph's id table, read back with `Graph.ID`.

The planner calls it by default. A build with the `pipelinegen_forward`
tag binds forward instead, as before the walk, with
`affected.Options.Select` nil: the plugin proof plans every change with
both and compares them.
`internal/affected` joins the walk's selection with what bazel-diff says
about the main repository, which it hashes already: a slice's test file
(by the diff), and the labels of its owner's `data`, `extra_deps` and
`pipelinegen:data` lines (labels of files with units dropped, as today).
A slice is selected when one of those labels is impacted, or when the set
of labels differs between the trees. Graph-bound suites go through the
same join. After the switch, no deps repository is written at all.

Under the switch the plugins and the lists load whenever the config has
sbom or correlator plugins, whether or not `MODULE.bazel` still names the
deps repository. A slice keeps `@pipelinegen_deps//slices:<file>` as its
id in the plan's JSON, `why`, the reports and the runner's input, so the
plan's JSON contract doesn't change with the switch.

## Names the protocol and the tree gain

The links side adds these to `plugin/` and fills them in `deps.Tree`;
the walk reads them:

| Where | Name | Meaning |
|---|---|---|
| a link | `kind`: `follow`, `bind`, `dependents-only` | a typed link, kept in `deps.Tree.Typed` |
| a link | `if_linked: true` | holds only for slices that link the group of `to`; carried, not applied yet |
| a link end | `group:<group>`, `node:<namespace>:<rest>` | the core's nodes; never dangling, no digest |
| a unit | `entry: "<test name>"` | a test entry function, rooted only when the slice runs that test |
| a unit | `not_root: true` | never a root of the slice whose file declares it |
| a link | `ref: true` | the link comes from a syntax reference; `referenced-new` counts its target as bound |
| a set | `input_units: [ids]` | units the set's links depend on; their digests key the set |
| `deps.Unit` | `File`, `Entry`, `NotRoot` | the file the unit is declared in, and the unit's `entry` and `not_root` |
| `deps.Tree` | `Typed`, `TypedRuledOut` | typed links in force, and per narrowing the wide ones it ruled out |
| `deps.Tree` | `InputUnits` | every unit some set of the tree names as an input unit, sorted; those no file declares are no name of their group, and not bound with it whole |

## Open points

- The conformance kit's warning for links that change while neither end
  does (the links side), and how often the Go correlator triggers it.
- Whether the implementation join over-selects much at level 5 on a real
  tree; if it does, links at conversion sites, or the per-slice check of
  `if_linked`.

## The store's side

The walk needs three things from the store:

- answers: read one write-once entry by its key, read a member record as
  the union of everything ever added to it, and save the entries and
  member items a plan made in one write;
- lists: the lists planning uses for a commit, with where they came from
  and the slices marked as untold;
- runs: a run's plan and its book, written by the plan and read back by
  `why` and the reports.

Units and link sets come through the answers, so the walk asks the store
for nothing else. The loader reads the whole of both trees' graphs on
every plan, since the walk needs all of it: every object a tree names is
read each time it is planned. Generations of cached objects that drop
what no plan read for a while are therefore safe. An incremental load is
not planned; if one comes, it brings a "this tree still names these
objects" touch with it.
