import { defineConfig, devices } from "@playwright/test";

// The browser end-to-end test (MG-58). It runs against a local stack that
// serves the built app (`make web` then `make dev`), and reads the sign-in
// code from the mail the fake sender writes. `make e2e-web` runs it.
export default defineConfig({
  testDir: "e2e",
  // One flow, start to finish; it makes its own account each run.
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 120_000,
  reporter: [["list"]],
  use: {
    baseURL: process.env.E2E_BASE_URL ?? "http://localhost:8080",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
