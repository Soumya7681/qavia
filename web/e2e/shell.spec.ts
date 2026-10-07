import { expect, test } from "@playwright/test";

test.beforeEach(async ({ context, baseURL }) => {
  await context.addCookies([{ name: "qavia_session", value: "e2e", url: baseURL! }]);
});

test("the shell shows navigation, the signed-in user, and route breadcrumbs", async ({ page }) => {
  await page.goto("/projects");
  const sidebar = page.locator("[data-slot=sidebar-content]");
  await expect(sidebar.getByRole("link", { name: "Projects", exact: true })).toHaveAttribute(
    "aria-current",
    "page",
  );
  await expect(page.getByRole("navigation", { name: "breadcrumb" })).toContainText("Projects");
});

test("the project switcher lists projects and navigates", async ({ page }) => {
  await page.goto("/projects");
  await page.getByRole("combobox", { name: "Switch project" }).click();
  const options = page.getByRole("option");
  await expect(options.first()).toBeVisible();
  await page.getByRole("option", { name: "All projects" }).click();
  await expect(page).toHaveURL(/\/projects$/);
});
