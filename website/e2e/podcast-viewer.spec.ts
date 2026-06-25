import { expect, test } from "@playwright/test";

test("public podcast can be viewed without signing in", async ({ page }) => {
  await page.goto("/p/public-podcast");

  await expect(page).toHaveURL(/\/p\/public-podcast$/);
  await expect(page.getByRole("heading", { name: "Public Podcast" })).toBeVisible();
  await expect(page.getByText("This is the public podcast transcript.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
});

test("private podcast redirects to login, then opens after sign in", async ({ page }) => {
  await page.goto("/p/private-podcast");

  await expect(page).toHaveURL(/\/login\?next=%2Fp%2Fprivate-podcast$/);
  await expect(page.getByRole("heading", { name: "Sign in to listen" })).toBeVisible();

  await page.getByRole("button", { name: "Sign in with RxLab" }).click();

  await expect(page).toHaveURL(/\/p\/private-podcast$/);
  await expect(page.getByRole("heading", { name: "Private Podcast" })).toBeVisible();
  await expect(page.getByText("This is the private podcast transcript.")).toBeVisible();
  await expect(page.getByRole("button", { name: "E2E Viewer" })).toBeVisible();
});

test("private podcast with stale session stops on login instead of redirecting forever", async ({
  page,
  baseURL,
}) => {
  await page.context().addCookies([
    {
      name: "podcast-viewer.e2e-user",
      value: "user-revoked",
      url: baseURL ?? "http://127.0.0.1:3000",
      httpOnly: true,
      sameSite: "Lax",
    },
  ]);

  await page.goto("/p/private-podcast");

  await expect(page).toHaveURL(/\/login\?next=%2Fp%2Fprivate-podcast$/);
  await expect(page.getByRole("heading", { name: "Sign in to listen" })).toBeVisible();
});
