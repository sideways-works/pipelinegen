// What a pipelinegen recording run adds to the browser specs. Outside such a
// run (PIPELINEGEN_RECORD unset) it hands back Playwright's test unchanged.
//
// In a recording run it wraps the test with react-correlator's fixture,
// which writes each spec file's browser and Node coverage under
// $PIPELINEGEN_COVERAGE/<spec file>/react/, and it appends every request the
// spec's pages make to $PIPELINEGEN_COVERAGE/<spec file>/api-uses/requests.txt,
// one "METHOD URL" per line, where oapi-codegen-correlator reads which API
// operations the spec called. The fixture file comes from react-correlator's
// release; $REACT_CORRELATOR_FIXTURE names it.
import { appendFileSync, mkdirSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join, relative } from "node:path";
import type { TestType } from "@playwright/test";

const recording = process.env.PIPELINEGEN_RECORD === "true";
const out = process.env.PIPELINEGEN_COVERAGE ?? "coverage";
const root = process.env.PIPELINEGEN_TREE ?? process.cwd();

export function recorded<T extends TestType<any, any>>(test: T): T {
  if (!recording) return test;
  const { withCoverage } = createRequire(import.meta.url)(process.env.REACT_CORRELATOR_FIXTURE!);
  const covered = withCoverage(test, { out, root, node: true });
  return covered.extend({
    context: async ({ context }, use, testInfo) => {
      const file = join(out, relative(root, testInfo.file), "api-uses", "requests.txt");
      mkdirSync(dirname(file), { recursive: true });
      appendFileSync(file, "");
      context.on("request", (request) => {
        appendFileSync(file, `${request.method()} ${request.url()}\n`);
      });
      await use(context);
    },
  }) as T;
}
