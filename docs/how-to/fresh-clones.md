# Fresh clones

A fresh clone of a repository that uses pipelinegen needs nothing from pipelinegen before Bazel can read it:
planning writes nothing into the repository's tree, and bazel-diff hashes
the repository as it is.

`pipelinegen affected` on your machine fetches the lists it needs by name;
a fresh clone needs nothing else. The hidden refs aren't fetched by a plain
`git clone`, and you don't need them.

## Lines an older setup left

A repository set up to plan through a hidden Bazel repository holds
three things pipelinegen no longer needs: these lines in `MODULE.bazel`,

```starlark
local_repository = use_repo_rule("@bazel_tools//tools/build_defs/repo:local.bzl", "local_repository")
local_repository(name = "pipelinegen_deps", path = ".pipelinegen/deps")
```

`.pipelinegen/` in `.bazelignore` and `.gitignore`, and
`pipelinegen learn --stub` in the checkout's setup. pipelinegen still
accepts all of them: the check and the plan warn about each, saying it
will be refused, and `learn --stub` still writes its stub. Remove the
lines and the step. Remove the `use_repo_rule` line only if
nothing else in `MODULE.bazel` uses `local_repository`.
