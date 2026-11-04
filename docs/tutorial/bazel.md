# Step 1: Bazel for your project

pipelinegen learns which parts of your repository a change touched from
Bazel's build graph ([what Bazel does](../concepts/bazel.md)). This step
gives your repository a Bazel workspace whose targets cover your files.
You don't have to switch your builds or your tests to Bazel: pipelinegen
only reads the graph, and your CI tasks can keep running `go test`.

## Install bazelisk

[Bazelisk](https://github.com/bazelbuild/bazelisk) starts the Bazel
version a repository asks for. Download the binary for your platform from
its [releases](https://github.com/bazelbuild/bazelisk/releases), put it on
your `PATH` as `bazelisk`, and, for convenience, also as `bazel`:

```sh
curl -fsSL -o ~/bin/bazelisk \
  https://github.com/bazelbuild/bazelisk/releases/download/v1.30.0/bazelisk-linux-amd64
chmod +x ~/bin/bazelisk
ln -s bazelisk ~/bin/bazel
```

pipelinegen runs `bazelisk` itself, from your `PATH`.

## If your repository already uses Bazel

Check that the graph loads:

```sh
bazel query //...
```

**You should see** a list of your targets, one label per line. Every test
file should be in some target's sources: a `go_test`, a `ts_project`, a
`filegroup`, whatever your rules use. Go on to [step 2](install.md).

**If you don't**, fix the error Bazel prints before going on; pipelinegen
runs the same queries.

## If it doesn't: a Go module

[Gazelle](https://github.com/bazel-contrib/bazel-gazelle) writes BUILD
files from Go code, so you don't write them by hand. In the repository's
root (where `go.mod` is), add three files.

`.bazelversion` pins the Bazel version:

```
9.2.0
```

`MODULE.bazel` declares the rules for Go and Gazelle, and the Go version
Bazel builds with:

```starlark
module(name = "notes")

bazel_dep(name = "rules_go", version = "0.64.1")
bazel_dep(name = "gazelle", version = "1.0.0-beta.1")

go_sdk = use_extension("@rules_go//go:extensions.bzl", "go_sdk")
go_sdk.download(version = "1.27.1")
```

`BUILD.bazel` gives Gazelle a target and your module's import path (the
`module` line of `go.mod`):

```starlark
load("@gazelle//:def.bzl", "gazelle")

# gazelle:prefix example.com/notes
gazelle(name = "gazelle")
```

If your module requires other modules, as the notes service does (the
runtime of its generated API server), also add these lines to
`MODULE.bazel`, and let `bazel mod tidy` fill in the `use_repo` line for
you:

```starlark
go_deps = use_extension("@gazelle//:extensions.bzl", "go_deps")
go_deps.from_file(go_mod = "//:go.mod")
```

<!-- output: mod-tidy -->
```
$ bazel mod tidy
INFO: Updated use_repo calls for @gazelle//:extensions.bzl%go_deps
```

Then let Gazelle write the BUILD files:

<!-- output: gazelle -->
```
$ bazel run //:gazelle
...
INFO: Build completed successfully
INFO: Running command line: bazel-bin/gazelle
```

The first run downloads Bazel, the Go rules and a Go toolchain, which
takes a few minutes. Afterwards each Go package has a `BUILD.bazel`.

### Files Gazelle doesn't know

Gazelle writes targets for Go code only. The notes service's browser
specs, under `e2e/`, need a target that says what they depend on besides
the backend code they reach (which the plugins bind in step 6): the page
they open and the files they read. A `filegroup` does it,
[e2e/BUILD.bazel](../../example-notes/e2e/BUILD.bazel):

```starlark
filegroup(
    name = "e2e",
    srcs = glob(["*.spec.ts"]),
    data = [
        "playwright.config.ts",
        "seed.json",
        "//web",
    ],
)
```

The notes service has one more such file: its API contract,
`api/notes.yaml`, from which oapi-codegen generates the server in
`api/notesapi/`. Gazelle's target for that package doesn't know the
generated code is made from the contract and its config, so say it, with
`# keep` so Gazelle leaves the lines alone
([api/notesapi/BUILD.bazel](../../example-notes/api/notesapi/BUILD.bazel)),
and export the contract from its own package
(`exports_files(["notes.yaml"])` in
[api/BUILD.bazel](../../example-notes/api/BUILD.bazel)):

```starlark
    data = [
        "cfg.yaml",  # keep
        "//api:notes.yaml",  # keep
    ],
```

In the browser specs' target, `//web` is the Go package that holds the
page; a change to the page then reaches the specs. The notes service
embeds the page in the server, so Gazelle's `go_library` for `web/` holds
it. A page or frontend that the server reads from disk instead needs a
target of its own, such as a `filegroup` listing its files, named here the
same way. Don't add the server (`//cmd/notes`) here: that would make every
backend change run every browser spec, which is what the plugins do
better.

Now the graph loads:

<!-- output: query -->
```
$ bazel query //...
//:gazelle
//:gazelle-runner
//api:api
//api:api_test
//api/notesapi:notesapi
//cmd/notes:notes
//cmd/notes:notes_lib
//e2e:e2e
//notes:notes
//notes:notes_test
//search:search
//search:search_test
//text:text
//text:text_test
//web:web
```

**You should see** a `go_library` target and a `go_test` target per Go
package (`//api:api` and `//api:api_test`), a `go_binary` for each
command, and your own targets (`//e2e:e2e`).

**If you don't:**

- An error that a repository such as `@bazel_gazelle` can't be resolved:
  the name in the `load` line must match the module's name in
  `MODULE.bazel` (`gazelle` above), or the `repo_name` its `bazel_dep`
  gives it.
- An error about an import that can't be resolved: the module has
  dependencies; add the `go_deps` lines above and run `bazel mod tidy`.

Add the Bazel output links to `.gitignore` and commit everything,
`MODULE.bazel.lock` included:

```sh
echo '/bazel-*' >> .gitignore
git add -A && git commit -m "build with Bazel"
```

Whenever you add a Go file, run `bazel run //:gazelle` again so the new
file is in a target. pipelinegen can't place a test file that no target
holds.

## If it doesn't: other languages

For a TypeScript or JavaScript frontend with its own build, the
[rules_js](https://github.com/aspect-build/rules_js) and
[rules_ts](https://github.com/aspect-build/rules_ts) documentation shows
how to declare packages and tests. This book doesn't walk through that
yet. What pipelinegen needs is less than a build: targets that hold your
files, such as the `filegroup` above for the browser specs. A `filegroup`
per directory of a frontend, listing its sources, is enough to start.

A `rules_js` project often passes its tools (eslint, tsc) from npm
packages to a macro of another repository. The check treats names loaded
from other repositories as tools; a macro that globs files itself is
declared under `macros` in the config ([the check's
reference](../reference/check.md#a-macro-from-another-repository)).

Next: [step 2, install pipelinegen](install.md).
