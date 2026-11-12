import { defineConfig } from "@playwright/test";

// The suite's set-up starts the server for the batch and exports its address.
export default defineConfig({
  testDir: ".",
  use: { baseURL: process.env.NOTES_URL ?? "http://127.0.0.1:8080" },
  projects: [{ name: "chromium", use: { browserName: "chromium" } }],
});
