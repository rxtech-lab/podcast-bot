import { APPLE_TEAM_ID, APP_BUNDLE_ID, CLIP_BUNDLE_ID } from "@/app/lib/config";

// Apple App Site Association. Served as application/json over HTTPS with no
// redirect so iOS can validate universal links (applinks) into the full app and
// App Clip invocations (appclips) for /d/* and /s/* deep links.
//
// Served via a Route Handler (not public/) to guarantee the JSON content-type;
// a public/ file with no extension would be served as octet-stream.
export const dynamic = "force-static";

export async function GET() {
  const appID = `${APPLE_TEAM_ID}.${APP_BUNDLE_ID}`;
  const body = {
    applinks: {
      apps: [],
      details: [
        {
          appIDs: [appID],
          components: [{ "/": "/d/*" }, { "/": "/s/*" }],
        },
      ],
    },
    appclips: {
      apps: [`${APPLE_TEAM_ID}.${CLIP_BUNDLE_ID}`],
    },
  };
  return new Response(JSON.stringify(body), {
    headers: { "content-type": "application/json" },
  });
}
