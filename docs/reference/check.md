# The check

Selection is only safe when every file is something a slice depends on,
or is said plainly to need no test. `pipelinegen check` makes sure of
that. It
reads BUILD files and git and starts no Bazel server, so it is fast enough
for a commit hook:

```sh
# .git/hooks/pre-commit, or your repository's own hook
pipelinegen check --staged || exit 1
```

`--staged` checks only what the commit touches; without it, the whole tree.
Planning runs the same check on the whole tree, so the hook and CI never
disagree; what planning does with its findings is on the page about
[uncovered files](../how-to/uncovered.md).

## What counts as covered

A file is covered when it is a test file of a suite, or something a slice
depends on: reached by a test file's owner target (or the targets that
list it, without an `owner` pattern), by a target named for it in
`extra_deps`, or by a tagged slice's dependencies. A file matched by
`everything_on` or `sample_on`, or listed in `untested`, counts as covered
too. The check doesn't read `pipelinegen:data` comments or recordings yet:
a file only such a comment names needs `extra_deps` or its owner target to
pass the check. A suite bound by recording is the exception for its own
`helpers`: a slice that runs a helper records it, so they count as covered
without a target holding them.

The check reads the tracked files and the new ones git doesn't ignore, so
a new test file counts before it is added. A new symlink is left out: the
link to the plugins' binaries in `tools/plugins` is a tool beside the tree,
not a file a slice must depend on.

A file a configured sbom plugin claims (a lockfile, a module's manifest)
is covered in planning: its units carry a change, so it needs neither a
Bazel target nor `everything_on`. The check in the commit hook runs no
plugin over the tree, so it can't see claims. It asks each configured
sbom plugin only for its `describe` answer (no file of the tree is read),
runs only a binary whose SHA-256 is the pin in the config, and keeps the
answer by that pin, so later runs need neither the binary nor a network.
An uncovered file the plugin's `candidates` or `include` match is then a
warning, "judged by the plan", and planning decides: covered if the plugin
claims it, uncovered otherwise. A plugin that isn't in `tools/plugins` (or
`--plugins`) is fetched from its pinned release, as `affected` fetches it
([plugins a command fetches](commands.md#plugins-a-command-fetches)). When
it can't be fetched, or the binary there isn't the pinned one, the finding
stays a failure and its fix says so.

## What the check reads

The check reads BUILD files without running them. It understands:

- strings, lists and dicts, `+` between them, and `a if c else b`
  (both branches count);
- `glob()` and `select()` (every branch counts);
- `list()`, `sorted()`, `depset()` and a dict's `keys()` and `values()`;
- comprehensions over lists it can read, such as
  `list({label: None for label in A + B})`, filtered by string tests on
  what it can read: `startswith`, `endswith`, `in`, `==` and `!=`, joined
  by `not`, `and` and `or`, as in
  `[t for t in ALL if not t.startswith("//docs:")]`;
- constants that a `.bzl` file of the repository defines at its top level
  and the BUILD file loads;
- macros from the repository's own `.bzl` files, with their arguments,
  their defaults and `**kwargs`;
- names loaded from another repository's `.bzl` file, such as an npm
  package's `bin` (`load("@npm//web:eslint/package_json.bzl", eslint_bin = "bin")`),
  and their attributes (`tsc_bin.tsc_binary`). Such a name is a tool from
  outside the tree: it covers no file of yours. Loads from the root
  module's own name (`@<module name>//`) are files of the tree.

Anything else is a call it would have to run, like a function of your own,
`%` formatting, indexing, or a comprehension whose `if` is more than those
string tests. Calling a name from another repository, or iterating over
one, is such a call too: it may produce files of the tree. The check names
the file, the target and the attribute, and reports the target as
`unreadable`.

A test file that no target lists in a form the check reads covers
nothing beyond itself: planning binds a test file only through the
targets that hold it, so the other targets of its package count for
nothing on its behalf. When one of them holds a name from another
repository, that name may be what lists the file, so the owner is unknown
and the test file is reported as `unreadable`. List the file in its test
target's `srcs`, or give the suite an `owner` pattern.

In a suite bound by the graph (`bind: graph`, the default) without an
`owner`, a test file that no target lists, that no `extra_deps` entry
names and that has no `pipelinegen:data` line is bound to nothing but
itself: a change to anything it imports would select nothing. The check
refuses such files with one `unbound-slice` finding per suite, on
`pipelinegen.yaml`, giving how many there are and two of them; `--staged`
reports it when one of them or the config is staged. Like every failing
finding, it fails a run's plan or makes it run everything (`uncovered`).
Where a plan meets such a slice anyway (`pipelinegen affected`, which runs
no check, or a test file only a target that is no test rule lists, which
the check counts as held), it selects the slice on every change and says
why.

## A macro from another repository

A macro from another repository is read as one target whose arguments are
its inputs. What it does inside is not seen: if it globs files itself
instead of taking them as an argument, those files show as uncovered. When
you can't pass them in (`srcs = glob(["src/**"])`), declare what the macro
reads:

```yaml
macros:
  "@web_tools//macros:web.bzl%web_project":
    reads: ["src/**", "public/**", index.html]
    reason: bundles the package's sources
```

Every call of `web_project` then covers the files of its own package that
the patterns match. The check prints each declaration as a
`declared-macro` warning, with how many files it covered and in which
targets, so the list stays in sight. A pattern that matches nothing in any
calling package, or a macro no BUILD file calls, is `bad-config`.

The check trusts what you declare. Planning, which runs Bazel anyway,
holds every call against the source files Bazel says the targets it made
depend on (the targets the macro marked with the call's name, or named
after it). A declared file they don't read fails the plan: the check
would count it as covered when a change to it may run nothing. A file of
the calling package they read that neither the declaration nor the call's
arguments name is a warning: the check calls it uncovered unless another
slice covers it, which is safe but tells you `reads` is short.

## The findings and their fixes

| Finding | What it means | Fix |
|---|---|---|
| `uncovered-file` | A tracked file that no slice depends on and no `untested` entry matches. A change to it would run nothing. Under a directory Bazel ignores, one finding stands for the whole directory: nothing there can be covered, and its test files are no slices. | Make a slice depend on it: the owner target of the tests that use it, or `extra_deps`. When no test can cover it (prose, repository settings), add it to `untested` with the reason. That is a decision for whoever owns the config, not a way past the hook. |
| `setup` | A warning, which fails nothing: a line planning no longer needs is still there, the `pipelinegen_deps` repository in `MODULE.bazel` or `.pipelinegen/` in `.bazelignore` and `.gitignore`. The warning says it will be refused. | Remove it as [fresh clones](../how-to/fresh-clones.md) shows. |
| `bad-config` | A config entry that can't be right, such as a pattern that matches no file, or a suite no run lists among its `suites` while the config declares runs ("suite <name> is in no run: its tests never run"). Planning only warns of it, since it says nothing about what a change reaches. | Fix what the message names; for a suite in no run, list it under a run's suites (`runs.<run>.suites`), or remove it. |
| `unreadable` | A BUILD or `.bzl` file the check can't parse, or a target with an input the check can't read (see above). Nobody can tell what such a target covers, so the check fails. | Fix the syntax, or write the inputs in a form the check reads. |
| `untagged-slice` | A tagged suite whose tag no target carries, so it has no slices. | Tag the targets it should run, or remove the suite. |
| `declared-macro` | A warning, which fails nothing: a declaration under `macros` the check used, with the files it covered. | Nothing, while the declaration is true. |
| `pinned-by-version` | A warning, which fails nothing: a target's data comes from a module only its version pins (no `MODULE.bazel.lock` is tracked), so planning counts that data unchanged while the version and the registries are, trusting the registry never to change a published version. | Commit `MODULE.bazel.lock` to pin it exactly. |
| `runner-notice` | A warning, which fails nothing: the configured runner, at hand in the plugins directory, wrote the pipeline and said something of it short of refusing it, such as a job its placement sends to untagged workers. `pipelinegen pipeline` prints the same notice. | Nothing, if that is what you mean; otherwise change what it names in the runner's config. |
| `undescribed-driver` | A warning, which fails nothing: a driver that a suite's steps name couldn't be asked what it is, because its binary isn't in the plugins directory (`tools/plugins/` or `--plugins`) and its pinned release couldn't be fetched, the binary there isn't the one its release pins, or it is pinned by a commit, which leaves no checksum to check it by (the warning then gives the commands that build it). The steps that name it went unchecked: their verbs, their settings under `with` and the driver's `config`, and the slices it takes. The same warning names a driver that validates its settings when the check knows its description but can't call it and kept no answer for these steps: their settings were checked by their top-level keys only. | Fix what kept its release from being fetched, put the pinned driver where the check looks, or pin it by a release, and run the check again. |
| `duplicate-label` | A target names one label twice in an attribute under two spellings (`//docs` and `//docs:docs`), which Bazel refuses when it loads the package. | Name it once. |
| `unbound-slice` | A tagged suite's slice that depends on nothing but its own files, so no change to the code it checks would run it; a test file whose `owner` pattern names a target that doesn't exist; or, once per suite bound by the graph without an `owner`, the test files no target holds and nothing else binds (see above): a change to anything these files import selects none of them. | Add the targets it checks to its `srcs` or `deps`, or declare the owner target. For the suite's files: give it an `owner` pattern, declare a Bazel target that holds them, or bind it by recording (`bind: recorded`) with a correlator that binds them. |

An `untested` entry looks like this:

```yaml
untested:
  - paths: ["docs/**", "**/*.md"]
    reason: prose, read by people only
  - paths: [".github/**"]
    reason: repository settings
```

Never commit with `--no-verify` to get past it: the planning job runs the
same check, and a file it finds either fails the run or makes it run
everything.
