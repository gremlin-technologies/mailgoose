import { defineConfig, devices } from "@playwright/test";

// The browser end-to-end test (MG-58), in four browsers (MG-59). It runs against a local stack that
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
  // WEB-2: current Chrome, Safari (WebKit), Firefox and Edge on desktop.
  // E2E_BROWSERS picks some, such as "chromium" where only that's installed.
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "firefox", use: { ...devices["Desktop Firefox"] } },
    { name: "webkit", use: { ...devices["Desktop Safari"] } },
    { name: "edge", use: { ...devices["Desktop Edge"], channel: "msedge" } },
  ].filter((p) => !process.env.E2E_BROWSERS || process.env.E2E_BROWSERS.split(",").includes(p.name)),
});
