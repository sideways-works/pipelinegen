# The pipelinegen book

pipelinegen makes CI run only the tests a change can reach, including the
ones the build can't see: a backend change runs the browser tests that
call it, and nothing else. This book explains how it works and walks you
through setting it up on your own project. You don't need to know anything
about it, or about CI, to start.

## How to read it

- New to the idea? Start with [what pipelinegen
  is](introduction/what-it-is.md) and [whether it fits your
  project](introduction/is-it-for-you.md).
- Want to understand it first? The [concepts](concepts/README.md) build up
  every idea on one small example, a notes service with a web page.
- Want to get going? The [step-by-step guide](tutorial/README.md) takes
  your repository from nothing to a working pipeline.
- Have it running and need one thing done? See the [how-to
  guides](how-to/README.md), the [reference](reference/config.md) and
  [troubleshooting](troubleshooting.md).
- Writing a plugin or changing pipelinegen itself? See [writing
  plugins](writing-plugins/README.md) and the
  [internals](internals/README.md).

The full contents are in [SUMMARY.md](SUMMARY.md); every term is in the
[glossary](glossary.md).
