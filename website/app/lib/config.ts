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

export function publicLink(id: string): string {
  return `${SITE_BASE_URL}/d/${id}`;
}

export function shareLink(token: string): string {
  return `${SITE_BASE_URL}/s/${token}`;
}
