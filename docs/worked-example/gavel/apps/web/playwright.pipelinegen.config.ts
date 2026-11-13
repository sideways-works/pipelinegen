// The browser specs as pipelinegen's batch runs them: the batch starts one
// server for all its specs, on the port it chose, so this config takes
// gavel's own and drops its webServer. The playwright driver asks for the
// JSON report itself.
import { defineConfig } from "@playwright/test";
import gavel from "./playwright.config";

const port = process.env.E2E_SERVER_PORT ?? "3333";

export default defineConfig({
  ...gavel,
  webServer: undefined,
  use: { ...gavel.use, baseURL: `http://localhost:${port}` },
});
