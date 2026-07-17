import { expect, test } from "@playwright/test";

test("marketplace lists public podcasts without signing in", async ({ page }) => {
  await page.goto("/");

  await expect(page.getByRole("heading", { name: "Marketplace" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Public Podcast" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Second Podcast" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
});

test("clicking a station opens its player", async ({ page }) => {
  await page.goto("/");

  await page.getByRole("link", { name: "Play Public Podcast" }).click();

  await expect(page).toHaveURL(/\/p\/public-podcast$/);
  await expect(page.getByRole("heading", { name: "Public Podcast" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Play podcast" })).toBeVisible();
});

test("search filters the station grid", async ({ page }) => {
  await page.goto("/");

  await page.getByRole("searchbox", { name: "Search podcasts" }).fill("second");
  await page.getByRole("searchbox", { name: "Search podcasts" }).press("Enter");

  await expect(page).toHaveURL(/\/\?q=second$/);
  await expect(page.getByRole("heading", { name: "Second Podcast" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Public Podcast" })).not.toBeVisible();
});

test("clicking a creator opens their station list", async ({ page }) => {
  await page.goto("/");

  await page.getByRole("link", { name: "PanelFM Creator" }).first().click();

  await expect(page).toHaveURL(/\/c\/creator-1$/);
  await expect(page.getByRole("heading", { name: "PanelFM Creator" })).toBeVisible();
  await expect(page.getByText("@panelfm · 2 followers")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Public Podcast" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Second Podcast" })).toBeVisible();
});

test("clicking an album chip opens the album with its episodes", async ({ page }) => {
  await page.goto("/");

  await page.getByRole("link", { name: "Mock Album" }).click();

  await expect(page).toHaveURL(/\/a\/album-1$/);
  await expect(page.getByRole("heading", { name: "Mock Album" })).toBeVisible();

  await page.getByRole("link", { name: /Second Podcast/ }).click();
  await expect(page).toHaveURL(/\/p\/second-podcast$/);
});

test("legacy oauth-prefixed creator links still resolve", async ({ page }) => {
  await page.goto("/c/oauth:creator-1");

  await expect(page.getByRole("heading", { name: "PanelFM Creator" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Public Podcast" })).toBeVisible();
});

test("player creator badge navigates to the creator's podcasts", async ({ page }) => {
  await page.goto("/p/public-podcast");

  await page.getByRole("link", { name: /PanelFM Creator/ }).click();

  await expect(page).toHaveURL(/\/c\/creator-1$/);
  await expect(page.getByRole("heading", { name: "PanelFM Creator" })).toBeVisible();
});

test("podcast fm logo navigates back home", async ({ page }) => {
  await page.goto("/p/public-podcast");

  await page.getByRole("link", { name: "podcast fm" }).click();

  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole("heading", { name: "Marketplace" })).toBeVisible();
});
