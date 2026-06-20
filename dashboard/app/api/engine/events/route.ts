import { auth } from "@/lib/auth";
import { engineFetch } from "@/lib/engine";

export const dynamic = "force-dynamic";

// Proxies the engine's SSE event stream (transcript / phase / tick / status /
// agent_activity) for one job to the browser, attaching the service token
// server-side so the engine token never reaches the client. The browser
// connects with EventSource to a same-origin URL the session cookie protects.
export async function GET(req: Request) {
  const session = await auth();
  if (!session?.user?.id) {
    return new Response("unauthorized", { status: 401 });
  }
  const channel = new URL(req.url).searchParams.get("channel") ?? "";
  const upstream = await engineFetch(
    `/api/events?channel=${encodeURIComponent(channel)}`,
    { headers: { Accept: "text/event-stream" }, signal: req.signal },
  );
  if (!upstream.ok || !upstream.body) {
    return new Response("engine stream unavailable", { status: 502 });
  }
  return new Response(upstream.body, {
    headers: {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache, no-transform",
      Connection: "keep-alive",
      "X-Accel-Buffering": "no",
    },
  });
}
