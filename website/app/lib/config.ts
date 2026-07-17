// Public deep-link configuration. These are not secrets — they appear in the
// AASA file and the rendered HTML.
export const APPLE_TEAM_ID = "T7GYB573Y6";
export const APP_BUNDLE_ID = "app.rxlab.debate-bot";
export const CLIP_BUNDLE_ID = "app.rxlab.debate-bot.Clip";

// The numeric App Store id, when known, enables the Smart App Banner / App Clip
// card. Optional — set NEXT_PUBLIC_APPLE_APP_ID once the app is on the store.
export const APPLE_APP_ID = process.env.NEXT_PUBLIC_APPLE_APP_ID ?? "";

export const SITE_BASE_URL = (
  process.env.NEXT_PUBLIC_SITE_BASE_URL ?? "https://podcast.rxlab.app"
).replace(/\/$/, "");

// Store links for the landing page download badges. Each badge renders only
// when its URL is set.
export const APP_STORE_URL = (process.env.NEXT_PUBLIC_APP_STORE_URL ?? "").trim();
export const TESTFLIGHT_URL = (process.env.NEXT_PUBLIC_TESTFLIGHT_URL ?? "").trim();

export function homeLink(): string {
  return SITE_BASE_URL;
}

export function marketplaceLink(): string {
  return `${SITE_BASE_URL}/marketplace`;
}

export function publicLink(id: string): string {
  return `${SITE_BASE_URL}/d/${id}`;
}

export function podcastLink(id: string): string {
  return `${SITE_BASE_URL}/p/${id}`;
}

export function albumLink(id: string): string {
  return `${SITE_BASE_URL}/a/${encodeURIComponent(id)}`;
}

export function creatorLink(slug: string): string {
  return `${SITE_BASE_URL}/c/${encodeURIComponent(slug)}`;
}

export function shareLink(token: string): string {
  return `${SITE_BASE_URL}/s/${token}`;
}

export function ogImageLink(params: {
  id?: string;
  token?: string;
  album?: string;
  creator?: string;
  screen?: "marketplace";
}): string {
  const search = new URLSearchParams();
  if (params.id) search.set("id", params.id);
  if (params.token) search.set("token", params.token);
  if (params.album) search.set("album", params.album);
  if (params.creator) search.set("creator", params.creator);
  if (params.screen) search.set("screen", params.screen);
  const query = search.toString();
  return `${SITE_BASE_URL}/api/og${query ? `?${query}` : ""}`;
}
