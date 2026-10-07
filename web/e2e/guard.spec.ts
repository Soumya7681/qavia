import { expect, test } from "@playwright/test";

test("a visitor with no session is sent to sign in, keeping where they were going", async ({
  page,
}) => {
  await page.goto("/projects/abc");
  await expect(page).toHaveURL(/\/login\?next=%2Fprojects%2Fabc$/);
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
});

test("a signed-in user reaches the app", async ({ page, context, baseURL }) => {
  // The mock accepts any session; what is under test is that the guard lets a
  // session through and the (app) layout renders with the user it got from /me.
  await context.addCookies([{ name: "qavia_session", value: "e2e", url: baseURL! }]);
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
});
