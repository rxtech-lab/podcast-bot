import { auth } from "@/app/lib/auth";

const BASE = (process.env.BACKEND_BASE_URL ?? "").replace(/\/$/, "");
const SERVICE_TOKEN = process.env.BACKEND_SERVICE_TOKEN ?? "";

type Props = { params: Promise<{ id: string }> };

export async function GET(_request: Request, { params }: Props) {
  const { id } = await params;
  if (!BASE) return new Response("backend not configured", { status: 503 });

  const session = await auth();
  const token = session?.accessToken || SERVICE_TOKEN;
  if (!token) return new Response("unauthorized", { status: 401 });

  const response = await fetch(`${BASE}/api/jobs/${encodeURIComponent(id)}/subtitles`, {
    cache: "no-store",
    redirect: "follow",
    headers: { Authorization: `Bearer ${token}` },
  });

  if (!response.ok) {
    return new Response(await response.text(), {
      status: response.status,
      headers: { "Content-Type": "text/plain; charset=utf-8" },
    });
  }

  return new Response(await response.text(), {
    headers: {
      "Cache-Control": "no-store",
      "Content-Type": "text/vtt; charset=utf-8",
    },
  });
}
