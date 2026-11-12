// Lists the npm packages an esbuild bundle of a repository includes, as their
// lockfile paths, one per line, from esbuild's metafile.
//
//   node bundled.mjs REPO JSON
//
// JSON is a list of esbuild build options. An option "srcAlias":
// {"<package>": "<dir>"} resolves imports of the package to
// <dir>/<subpath or index>.ts, as a build from the dependency's sources does.
import { createRequire } from "node:module";
import { join, relative } from "node:path";

const [repo, spec] = process.argv.slice(2);
const require = createRequire(join(repo, "package.json"));
const { build } = require("esbuild");

const seen = new Set();
for (const opts of JSON.parse(spec)) {
  const { srcAlias, ...rest } = opts;
  const plugins = [];
  if (srcAlias) {
    plugins.push({
      name: "src-alias",
      setup(b) {
        for (const [pkg, dir] of Object.entries(srcAlias)) {
          const re = new RegExp(`^${pkg.replace(/[/.]/g, "\\$&")}(?:/(.*))?$`);
          b.onResolve({ filter: re }, (a) => {
            const sub = re.exec(a.path)[1];
            return { path: join(repo, dir, `${sub ?? "index"}.ts`) };
          });
        }
      },
    });
  }
  const r = await build({
    absWorkingDir: repo,
    bundle: true,
    platform: "node",
    target: "node22",
    logLevel: "silent",
    write: false,
    metafile: true,
    outdir: "/nonexistent",
    ...rest,
    plugins,
  });
  for (const input of Object.keys(r.metafile.inputs)) {
    const rel = relative(repo, join(repo, input)).replaceAll("\\", "/");
    const m = /^(.*node_modules\/(?:@[^/]+\/)?[^/]+)\//.exec(rel);
    if (m) seen.add(m[1]);
  }
}
for (const p of [...seen].sort()) console.log(p);
