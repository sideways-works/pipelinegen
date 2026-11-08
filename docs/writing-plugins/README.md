# Writing plugins

These pages are for people who want to write a plugin: an sbom plugin, a
correlator, a runner or a driver. Connectors (for a git host) have a protocol that
the core doesn't call yet ([a connector](connector.md)), and stores (for
where pipelinegen keeps its records) are a design ([a store](store.md)). They say how to build one and test it. The
protocol itself, with every call and field and the rules every plugin
keeps, is [plugins.md](../plugins.md); what plugins are and how a
repository uses them is in [the concepts](../concepts/units-and-sbom.md)
and [configuring plugins](../how-to/plugins.md).

- [An sbom plugin](sbom.md) says what exists in one domain: the units, with
  their digests, and how they hang together.
- [A correlator](correlator.md) says who touches or uses those units: links
  between them, and which of them a test touched when it ran.
- [A runner](runner.md) writes one CI system's pipeline.
- [A driver](driver.md) runs a suite's tests: one result and, in a
  recording run, one recording per slice, bundled or one by one.

An sbom plugin and its correlators come as a pair per domain and share a
small library, so both spell a unit's ID the same way. Name an sbom plugin
for what it describes (`go-sbom`, `pg-sbom`) and a correlator for the tool
or framework whose conventions it follows (`react-correlator`,
`gorm-correlator`).

## The shape of a plugin

A plugin is one executable, called as `<executable> <call>` with a JSON
request on stdin and a JSON answer on stdout. It reads files and answers
in JSON: it starts no server, connects to no database and uses no network.
Where files can't tell, it answers "can't tell" by exiting with 3, which is
an answer of its own, apart from a failure and from an empty answer. A
driver is the exception: it runs the tree's tests and may start servers
for them, in the suite's batch task only ([a driver](driver.md)). The
protocol page has the rest: [what every plugin
shares](../plugins.md#what-every-plugin-shares).

A plugin written in Go can use pipelinegen's `plugin` package: it holds the
protocol's messages, and `plugin.Serve` is a plugin's `main`, which runs
the named call's handler and turns `plugin.ErrCantTell` into exit code 3.

## Testing

`pipelinegen plugin test <executable> --tree <fixture>` checks a plugin
the way the core calls it, on a fixture tree of the plugin's own, and exits
1 when any check fails. Run it in the plugin repository's CI.
[Conformance](../plugins.md#conformance) lists what it checks.

## Releasing

Release with `go run ./ci/release` from this repository, as
[releases.md](../releases.md) describes: static binaries (or one bundled
`.mjs` file for a Node plugin), the plugin's own `describe` answer as
`plugin.json`, and `SHA256SUMS`. Repositories pin a plugin by version and
checksum, and a mismatch fails their pipeline.
