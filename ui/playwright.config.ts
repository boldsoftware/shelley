import { defineConfig, devices } from "@playwright/test";

/**
 * The test server is managed by globalSetup (scripts/global-setup.ts).
 * It starts shelley with --port 0 and exports the actual URL via
 * PLAYWRIGHT_TEST_BASE_URL, which Playwright's baseURL fixture reads
 * automatically. This eliminates hardcoded ports and port conflicts.
 *
 * To point at an already-running server, set TEST_SERVER_URL.
 *
 * @see https://playwright.dev/docs/test-configuration
 */
export default defineConfig({
  testDir: "./e2e",
  globalSetup: "./scripts/global-setup.ts",
  /* Run tests in files in parallel */
  fullyParallel: true,
  /* Fail the build on CI if you accidentally left test.only in the source code. */
  forbidOnly: !!process.env.CI,
  /* Retry on CI only */
  retries: process.env.CI ? 1 : 0,
  /* Per-test timeout. Playwright's 30s default is too tight for our flows:
   * several specs drive multiple agent round-trips, each guarded by its own
   * 30s expect() wait, so the whole test legitimately needs more than 30s
   * under CI load. When the test timeout fired mid-assertion Playwright tore
   * down the page, surfacing as "Target page, context or browser has been
   * closed" rather than a clear timeout. 120s gives ample headroom over the
   * realistic few-seconds runtime while still bounding a genuinely hung test. */
  timeout: 120_000,
  /* Use 2 workers in CI. All tests share a single predictable-mode server
   * backed by a single-writer SQLite DB; 3 workers caused systemic flake
   * from SSE/agent-working contention on ubuntu-latest. 2 keeps wall-clock
   * low while avoiding the overload that made every run drop ~1 test. */
  workers: process.env.CI ? 2 : 1,
  /* Reporter to use. See https://playwright.dev/docs/test-reporters */
  reporter: process.env.CI ? [["html", { open: "never" }], ["list"]] : "list",
  /* Shared settings for all the projects below. See https://playwright.dev/docs/api/class-testoptions. */
  use: {
    /* baseURL is set automatically via PLAYWRIGHT_TEST_BASE_URL from global-setup */
    /* Collect trace on all tests, keep only on failure */
    trace: "retain-on-failure",
    /* Take a screenshot after every test */
    screenshot: "on",
    /* Record video on all tests, keep only on failure */
    video: "retain-on-failure",
  },

  /*
   * Both frontends ("worlds") are built and served by one binary; the server
   * picks one per request from the `vue-ui` feature flag, overridable by the
   * X-Shelley-UI header. We run the whole suite once per world by pinning that
   * header per project, so every shared spec is exercised against both the Vue
   * and React frontends.
   *
   * Test layout:
   *   e2e/*.spec.ts        shared — run in BOTH worlds (the DOM/ARIA contract
   *                        is identical, so a spec should pass in both)
   *   e2e/vue/*.spec.ts    Vue-only — run only in the vue project
   *   e2e/react/*.spec.ts  React-only — run only in the react project
   *
   * When a behavior legitimately diverges between worlds, copy the spec into
   * e2e/vue/ and e2e/react/ and edit each independently, rather than adding
   * world conditionals to a shared spec.
   *
   * Set UI_WORLD=vue|react to run just one world locally.
   */
  projects: (["vue", "react"] as const)
    .filter((world) => !process.env.UI_WORLD || process.env.UI_WORLD === world)
    .map((world) => ({
      name: world,
      testIgnore: world === "vue" ? "**/react/**" : "**/vue/**",
      use: {
        ...devices["Pixel 5"],
        extraHTTPHeaders: { "X-Shelley-UI": world },
      },
    })),
});
