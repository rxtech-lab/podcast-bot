import { auth } from "@/lib/auth";
import { engineFetch } from "@/lib/engine";

export const dynamic = "force-dynamic";

// Proxies the engine's live HLS playlist + segments during a render so the
// browser's hls.js can preview the video being generated.
export async function GET(
  req: Request,
  { params }: { params: Promise<{ id: string; file: string }> },
) {
  const session = await auth();
  if (!session?.user?.id) {
    return new Response("unauthorized", { status: 401 });
  }
  const { id, file } = await params;
  const upstream = await engineFetch(
    `/api/jobs/${encodeURIComponent(id)}/hls/${encodeURIComponent(file)}`,
    { signal: req.signal },
  );
  if (!upstream.body) {
    return new Response(null, { status: upstream.status });
  }
  const contentType = file.endsWith(".m3u8")
    ? "application/vnd.apple.mpegurl"
    : "video/mp2t";
  return new Response(upstream.body, {
    status: upstream.status,
    headers: { "Content-Type": contentType, "Cache-Control": "no-cache" },
  });
}
