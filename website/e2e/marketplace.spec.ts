import { expect, test } from "@playwright/test";

async function expectGeneratedOGImage(page: import("@playwright/test").Page, path: string) {
  const response = await page.request.get(path);
  expect(response.ok()).toBe(true);
  expect(response.headers()["content-type"]).toContain("image/png");
  expect((await response.body()).byteLength).toBeGreaterThan(1_000);
}

test("landing page shows the hero and links to the marketplace", async ({ page }) => {
  await page.goto("/");

  await expect(
    page.getByRole("heading", { name: /Turn every question into a conversation/ })
  ).toBeVisible();
  // Download badges are hidden unless the store URLs are configured (they are
  // not in the e2e environment).
  await expect(page.getByRole("link", { name: "Download on the App Store" })).toHaveCount(0);

  await page.getByRole("link", { name: "Marketplace", exact: true }).first().click();
  await expect(page).toHaveURL(/\/marketplace$/);
  await expect(page.getByRole("heading", { name: "Marketplace" })).toBeVisible();
});

test("landing page exposes Open Graph metadata and a generated image", async ({ page }) => {
  await page.goto("/");

  await expect(page).toHaveTitle("PodcastFM — Turn every question into a conversation");
  await expect(page.locator('meta[property="og:title"]')).toHaveAttribute(
    "content",
    "PodcastFM — Turn every question into a conversation"
  );
  await expect(page.locator('meta[property="og:image"]')).toHaveAttribute(
    "content",
    /\/api\/og$/
  );
  await expectGeneratedOGImage(page, "/api/og");
});

test("legacy marketplace search links redirect to /marketplace", async ({ page }) => {
  await page.goto("/?q=second");

  await expect(page).toHaveURL(/\/marketplace\?q=second$/);
  await expect(page.getByRole("heading", { name: "Second Podcast" })).toBeVisible();
});

test("marketplace lists public podcasts without signing in", async ({ page }) => {
  await page.goto("/marketplace");

  await expect(page.getByRole("heading", { name: "Marketplace" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Public Podcast" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Second Podcast" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
});

test("marketplace exposes Open Graph metadata and a generated image", async ({ page }) => {
  await page.goto("/marketplace");

  await expect(page).toHaveTitle("PodcastFM — Marketplace");
  await expect(page.locator('meta[property="og:title"]')).toHaveAttribute(
    "content",
    "PodcastFM — Marketplace"
  );
  await expect(page.locator('meta[property="og:description"]')).toHaveAttribute(
    "content",
    "Browse public AI-generated podcasts from the community and press play."
  );
  await expect(page.locator('meta[property="og:image"]')).toHaveAttribute(
    "content",
    /\/api\/og\?screen=marketplace$/
  );
  await expectGeneratedOGImage(page, "/api/og?screen=marketplace");
});

test("clicking a station opens its player", async ({ page }) => {
  await page.goto("/marketplace");

  await page.getByRole("link", { name: "Play Public Podcast" }).click();

  await expect(page).toHaveURL(/\/p\/public-podcast$/);
  await expect(page.getByRole("heading", { name: "Public Podcast" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Play podcast" })).toBeVisible();
});

test("search filters the station grid", async ({ page }) => {
  await page.goto("/marketplace");

  await page.getByRole("searchbox", { name: "Search podcasts" }).fill("second");
  await page.getByRole("searchbox", { name: "Search podcasts" }).press("Enter");

  await expect(page).toHaveURL(/\/marketplace\?q=second$/);
  await expect(page.getByRole("heading", { name: "Second Podcast" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Public Podcast" })).not.toBeVisible();
});

test("clicking a creator opens their station list", async ({ page }) => {
  await page.goto("/marketplace");

  await page.getByRole("link", { name: "PanelFM Creator" }).first().click();

  await expect(page).toHaveURL(/\/c\/creator-1$/);
  await expect(page.getByRole("heading", { name: "PanelFM Creator" })).toBeVisible();
  await expect(page.getByText("@panelfm · 2 followers")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Public Podcast" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Second Podcast" })).toBeVisible();
});

test("clicking an album chip opens the album with its episodes", async ({ page }) => {
  await page.goto("/marketplace");

  await page.getByRole("link", { name: "Mock Album" }).click();

  await expect(page).toHaveURL(/\/a\/album-1$/);
  await expect(page.getByRole("heading", { name: "Mock Album" })).toBeVisible();

  await page.getByRole("link", { name: /Second Podcast/ }).click();
  await expect(page).toHaveURL(/\/p\/second-podcast$/);
});

test("album exposes Open Graph metadata and a generated image", async ({ page }) => {
  await page.goto("/a/album-1");

  await expect(page).toHaveTitle("Mock Album — PanelFM");
  await expect(page.locator('meta[property="og:title"]')).toHaveAttribute(
    "content",
    "Mock Album — PanelFM"
  );
  await expect(page.locator('meta[property="og:description"]')).toHaveAttribute(
    "content",
    "1 episode on PanelFM."
  );
  await expect(page.locator('meta[property="og:image"]')).toHaveAttribute(
    "content",
    /\/api\/og\?album=album-1$/
  );
  await expectGeneratedOGImage(page, "/api/og?album=album-1");
});

test("creator exposes Open Graph metadata and a generated image", async ({ page }) => {
  await page.goto("/c/creator-1");

  await expect(page).toHaveTitle("PanelFM Creator — PanelFM");
  await expect(page.locator('meta[property="og:title"]')).toHaveAttribute(
    "content",
    "PanelFM Creator — PanelFM"
  );
  await expect(page.locator('meta[property="og:description"]')).toHaveAttribute(
    "content",
    "Public podcasts by PanelFM Creator."
  );
  await expect(page.locator('meta[property="og:image"]')).toHaveAttribute(
    "content",
    /\/api\/og\?creator=creator-1$/
  );
  await expectGeneratedOGImage(page, "/api/og?creator=creator-1");
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
  await expect(
    page.getByRole("heading", { name: /Turn every question into a conversation/ })
  ).toBeVisible();
});
