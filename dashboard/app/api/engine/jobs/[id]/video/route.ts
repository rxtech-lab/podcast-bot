import { auth } from "@/lib/auth";
import { engineFetch } from "@/lib/engine";

export const dynamic = "force-dynamic";

// Serves the finished video. When the engine stores it in S3 it answers with a
// 302 to a presigned URL — we forward that redirect so the browser downloads
// straight from object storage. Otherwise we stream the mp4 through.
export async function GET(
  req: Request,
  { params }: { params: Promise<{ id: string }> },
) {
  const session = await auth();
  if (!session?.user?.id) {
    return new Response("unauthorized", { status: 401 });
  }
  const { id } = await params;
  const upstream = await engineFetch(`/api/jobs/${encodeURIComponent(id)}/video`, {
    redirect: "manual",
    signal: req.signal,
  });
  if (upstream.status === 302 || upstream.status === 301) {
    const loc = upstream.headers.get("location");
    if (loc) return Response.redirect(loc, 302);
  }
  if (!upstream.ok || !upstream.body) {
    return new Response("video not ready", { status: upstream.status });
  }
  return new Response(upstream.body, {
    status: 200,
    headers: {
      "Content-Type": "video/mp4",
      "Content-Disposition": `attachment; filename="${id}.mp4"`,
    },
  });
}
