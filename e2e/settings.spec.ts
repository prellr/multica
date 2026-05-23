import { test, expect } from "@playwright/test";
import { loginAsDefault } from "./helpers";

test.describe("Settings", () => {
  test("updating workspace name reflects in sidebar immediately", async ({
    page,
  }) => {
    await loginAsDefault(page);

    // Read the current workspace name from the sidebar
    const sidebarName = page.locator('[data-sidebar="menu-button"]').first();
    const originalName = await sidebarName.innerText();

    // Navigate to settings
    await page.getByRole("link", { name: "Settings" }).click();
    await expect(page).toHaveURL(/\/settings/);
    await page.getByRole("tab", { name: "General" }).click();
    const generalPanel = page.getByRole("tabpanel", { name: "General" });

    // Change workspace name
    const nameInput = generalPanel.getByRole("textbox").first();
    await nameInput.clear();
    const newName = "Renamed WS " + Date.now();
    await nameInput.fill(newName);

    // Save
    const saveButton = generalPanel.getByRole("button", { name: /^Save$/ });
    await expect(saveButton).toBeEnabled();
    await saveButton.focus();
    await Promise.all([
      page.waitForResponse((response) => {
        const request = response.request();
        return (
          request.method() === "PATCH" &&
          response.url().includes("/api/workspaces/") &&
          response.ok()
        );
      }),
      page.keyboard.press("Enter"),
    ]);

    // Sidebar should reflect the new name WITHOUT page refresh
    await expect(sidebarName).toContainText(newName, { timeout: 10000 });

    // Restore original name so other tests aren't affected
    await nameInput.clear();
    await nameInput.fill(originalName.trim());
    await expect(saveButton).toBeEnabled();
    await saveButton.focus();
    await Promise.all([
      page.waitForResponse((response) => {
        const request = response.request();
        return (
          request.method() === "PATCH" &&
          response.url().includes("/api/workspaces/") &&
          response.ok()
        );
      }),
      page.keyboard.press("Enter"),
    ]);
    await expect(sidebarName).toContainText(originalName.trim(), {
      timeout: 10000,
    });
  });
});
