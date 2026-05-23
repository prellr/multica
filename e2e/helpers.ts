import { expect, type Page } from "@playwright/test";
import { TestApiClient } from "./fixtures";

const DEFAULT_E2E_NAME = "E2E User";

function workerSuffix(): string {
  return process.env.TEST_WORKER_INDEX ?? "0";
}

function defaultE2EEmail(): string {
  return `e2e+${workerSuffix()}@multica.ai`;
}

function defaultE2EWorkspaceSlug(): string {
  return `e2e-workspace-${workerSuffix()}`;
}

export async function dismissStarterDialog(page: Page) {
  const starterDialog = page.getByRole("dialog", {
    name: "Welcome — add starter tasks?",
  });
  if (await starterDialog.isVisible({ timeout: 5000 }).catch(() => false)) {
    const startBlank = starterDialog.getByRole("button", {
      name: "Start blank workspace",
    });
    await startBlank.click();
    await expect(starterDialog).not.toBeVisible({ timeout: 5000 });
  }
}

export async function openManualIssueDialog(page: Page) {
  await page.getByRole("button", { name: "New Issue" }).click();
  const switchToManual = page.getByRole("button", { name: "Switch to Manual" });
  if (await switchToManual.isVisible({ timeout: 1000 }).catch(() => false)) {
    await switchToManual.click();
  }
  await expect(page.getByRole("textbox", { name: "Issue title" })).toBeVisible();
}

/**
 * Log in as the default E2E user and ensure the workspace exists first.
 * Authenticates via API (send-code → DB read → verify-code), then injects
 * the token into localStorage so the browser session is authenticated.
 *
 * Returns the E2E workspace slug so callers can build workspace-scoped URLs.
 */
export async function loginAsDefault(page: Page): Promise<string> {
  const api = new TestApiClient();
  await api.login(defaultE2EEmail(), DEFAULT_E2E_NAME);
  const workspace = await api.ensureWorkspace(
    "E2E Workspace",
    defaultE2EWorkspaceSlug(),
  );

  const token = api.getToken();
  if (!token) throw new Error("E2E login did not return a token");
  await page.addInitScript((t) => {
    localStorage.setItem("multica_token", t);
  }, token);
  await page.goto(`/${workspace.slug}/issues`);
  await page.waitForURL(/\/issues$/, { timeout: 10000 });
  await dismissStarterDialog(page);
  return workspace.slug;
}

/**
 * Create a TestApiClient logged in as the default E2E user.
 * Call api.cleanup() in afterEach to remove test data created during the test.
 */
export async function createTestApi(): Promise<TestApiClient> {
  const api = new TestApiClient();
  await api.login(defaultE2EEmail(), DEFAULT_E2E_NAME);
  await api.ensureWorkspace("E2E Workspace", defaultE2EWorkspaceSlug());
  return api;
}

export async function openWorkspaceMenu(page: Page) {
  // Click the workspace switcher button (has ChevronDown icon)
  await page.locator('[data-sidebar="menu-button"]').first().click();
  // Wait for dropdown to appear
  await page.getByText("Log out").waitFor({ state: "visible" });
}
