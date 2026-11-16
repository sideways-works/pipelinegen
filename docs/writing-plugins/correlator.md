# Writing a correlator

This page is for people writing a correlator. A correlator says who
touches or uses the units that sbom plugins list: links from one unit to
another, inside a domain or across domains, and which units a test touched
when it ran. It owns no units and no namespace. The fields are in
[plugins.md](../plugins.md#correlators).

## Calls

- `describe`: as for every plugin, plus `collects` when you handle only
  some of `collect`'s actions, and `observe_reads` naming the config keys
  whose paths you read while you observe.
- `detect`: claim the files you read, each with a role, on positive
  evidence.
- `links` or `bind`, for planning (below).
- `collect`, inside the batch task, and `observe`, in the step after it,
  for recording. That step sees only the tree and the coverage output
  ([what the step that observes can see](../plugins.md#what-the-step-that-observes-can-see)).

The protocol also defines `why`, which explains a link step by step. The
core doesn't call it yet.

## Links

A link says that when `from` is bound, `to` is bound too:

```json
{"from": "go:api/notes#func:Create", "to": "pg:table:public.notes", "kind": "writes"}
```

Both ends are IDs an sbom plugin lists; spell them with the library your
sbom shares, so they match. The core knows four kinds: `uses`, `reads`,
`writes` and `generates`. A change to a table's definition reaches its
readers and writers, and a change to seed rows reaches their readers only.
Any other kind is a plain link, but one that spells a known kind
differently (`Uses`) fails the plan. A link to an ID no plugin describes is a
naming convention that didn't hold: the core binds the link's source wide
and says why.

A correlator that knows how far each reference carries says so with the
typed kinds `follow`, `bind` and `dependents-only`, and may end links at
the core's own `group:<group>` and `node:<namespace>:<rest>` ids. Mark a
link that comes from a reference in the source with `"ref": true`, and
one that holds only where its target's package is linked with
`"if_linked": true`. The [protocol](../plugins.md#links) says what each
means. Typed links don't select anything yet, so keep answering `bind`
until planning walks them.

When you can't work out a unit's links (SQL built at run time, say), list
it under `cannot_tell` with the reason, and the rest of your answer stands.

Your answer may depend only on the files you claimed and those you read
along with a claim: the core caches it by their content. A set may name
units an sbom lists as `input_units` instead of files it reads: the core
then keys the set on those units' digests, so an edit that leaves them
alone serves the set from the cache. The kit gives
every other file of the fixture other bytes, and fails a correlator whose
links change.

## Links or bind

When your links are simple, answer `links`, and the core closes over them.
When the closure needs your own knowledge (Go's method sets and
interfaces, which would cost hundreds of MB to hand across), answer `bind`
instead: per slice and width, the units you bind, each with a reason, and
what the wider rule would add.

## Observing what a test touched

`observe` gets a slice and the directories it left behind, and answers the
IDs it touched:

```json
{"touched": ["go:api/notes#func:Create", "go:api/notes#func:validate"]}
```

The first of the request's `dirs` is always the setup's directory, and the
rest are the slice's parts: tell the setup by its position, never by its
name, since it may be named otherwise or not exist. The request's
`claimed` lists your own claims in the tree, so read the files you need
from it (a bundler's config, a source map) rather than walk the tree.

An empty answer says the slice touched nothing, so give one only on
positive evidence of a finished recording with nothing in it, such as
coverage files with zero counts. When the data is missing, or nothing shows
that recording happened, or what you see can't be mapped back to the
tree, answer "can't tell" (exit 3): the core then marks the slice, which
binds it to everything until a recording tells again. Never answer an
empty list for a recording you can't map: the kit checks that with a tree
the fixture's recording doesn't belong to.
Data you can't read or parse exits 1 and errors the slice. Even an empty
answer is taken as "can't tell" unless the suite sets `empty_recordings:
allow`, since a test file that touched nothing at all is rare.

`collect` runs while a batch runs, for each correlator that observes the
suite, with an action: `setup-end` once before
the first slice, then `slice-start` and `slice-end` around each slice. It
gathers what belongs to you into your own directory, which `observe` then
reads: what start-up and seeding ran into the setup's directory, and what
each slice ran into the slice's. A batch runs its slices one after another
against one stack, so reading and resetting something global between
slices (a log offset, a server's coverage counters) is safe. Keep
`collect` fast: each call is bounded, 10 seconds by default, and a failure
errors the slice. A call stopped by its limit says so, naming the call and
the slice: `sql collect (slice-end of e2e/notes.spec.ts): stopped after
10s, the limit for a collect call`. Note where the slice's part begins and
ends, and leave reading it to `observe`, which runs once per slice after
the batch and has no such limit. A repository whose collector needs longer
gives it more with `collect_seconds` on its entry; a suite's correlators
may take 90 seconds together, since a mark waits for each of them in turn.

## Narrowings

A narrowing is a narrower answer that saves runs and weakens the safety
argument a little. Declare each in `describe` with `off_at`, the lowest
narrowing level that switches it off. Answer both the wide and the narrow
links, each tagged with the narrowing and `while`, and the core keeps the
ones the level allows; one cached answer then serves every level, and the
plan can say what a narrowing dropped.

## Small correlators

The protocol keeps narrow correlators cheap: links found by scanning two
languages for a shared constant, observations read from a log. Message
subjects and their producers and consumers, message catalog keys and the
code that renders them, or webhook fixtures by event type each fit in a
small correlator.
