import { APPLE_APP_ID, CLIP_BUNDLE_ID } from "@/app/lib/config";

// appleItunesMeta builds the `apple-itunes-app` meta tag content so Safari
// surfaces the App Clip card (and Smart App Banner) for a deep link. The
// app-clip-bundle-id drives the clip card; app-id (numeric) is included only
// when configured. app-argument carries the deep link into the app/clip.
export function appleItunesMeta(deepLink: string): Record<string, string> {
  const parts: string[] = [];
  if (APPLE_APP_ID) parts.push(`app-id=${APPLE_APP_ID}`);
  parts.push(`app-clip-bundle-id=${CLIP_BUNDLE_ID}`);
  parts.push("app-clip-display=card");
  parts.push(`app-argument=${deepLink}`);
  return { "apple-itunes-app": parts.join(", ") };
}
