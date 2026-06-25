import "server-only";
import type { DiscussionCover, Creator } from "@/app/lib/backend";

// Server-only fetchers for the view-only player. A signed-in user can open their
// own (possibly private) podcasts via the user-token discussion endpoint, and
// anyone signed in can open public ones via the service-token market endpoint.

const BASE = (process.env.BACKEND_BASE_URL ?? "").replace(/\/$/, "");
const SERVICE_TOKEN = process.env.BACKEND_SERVICE_TOKEN ?? "";

// One transcript line as served by the Go backend (native_discussion_lines).
export type TranscriptLine = {
  speaker: string;
  role: string;
  side?: string;
  text: string;
  start_ms?: number;
  is_user: boolean;
};

// The fields the player needs from a discussion.
export type ViewerDiscussion = {
  id: string;
  title: string;
  topic: string;
  status: string;
  job_id?: string;
  download_url?: string;
  captions_url?: string;
  duration_seconds?: number;
  cover?: DiscussionCover;
  creator?: Creator | null;
  lines: TranscriptLine[];
};

function normalize(d: Record<string, unknown>): ViewerDiscussion {
  const rawLines = Array.isArray(d.lines) ? (d.lines as Record<string, unknown>[]) : [];
  return {
    id: String(d.id ?? ""),
    title: String(d.title || d.topic || "Podcast"),
    topic: String(d.topic ?? ""),
    status: String(d.status ?? ""),
    job_id: typeof d.job_id === "string" ? d.job_id : undefined,
    download_url: typeof d.download_url === "string" ? d.download_url : undefined,
    captions_url:
      typeof d.job_id === "string" && d.job_id.trim() !== ""
        ? `/api/jobs/${encodeURIComponent(d.job_id)}/subtitles`
        : undefined,
    duration_seconds: typeof d.duration_seconds === "number" ? d.duration_seconds : undefined,
    cover: d.cover as DiscussionCover | undefined,
    creator: (d.creator as Creator | null | undefined) ?? null,
    lines: rawLines.map((l) => ({
      speaker: String(l.speaker ?? ""),
      role: String(l.role ?? ""),
      side: typeof l.side === "string" ? l.side : undefined,
      text: String(l.text ?? ""),
      start_ms: typeof l.start_ms === "number" ? l.start_ms : undefined,
      is_user: Boolean(l.is_user),
    })),
  };
}

// getViewerDiscussion resolves a discussion for the player. It first tries the
// authenticated owner endpoint with the user's rxlab access token (returns the
// user's own discussions, including private ones, with transcript + audio), then
// falls back to the public market endpoint with the service token. Returns null
// when the discussion is neither owned nor public (or the backend is unset).
export async function getViewerDiscussion(
  id: string,
  accessToken: string | undefined
): Promise<ViewerDiscussion | null> {
  if (!BASE) return null;
  const path = encodeURIComponent(id);

  if (accessToken) {
    const res = await fetch(`${BASE}/api/discussions/${path}`, {
      headers: { Authorization: `Bearer ${accessToken}` },
      cache: "no-store",
    });
    if (res.ok) return normalize(await res.json());
  }

  if (SERVICE_TOKEN) {
    const res = await fetch(`${BASE}/api/market/stations/${path}`, {
      headers: { Authorization: `Bearer ${SERVICE_TOKEN}` },
      cache: "no-store",
    });
    if (res.ok) return normalize(await res.json());
  }

  return null;
}
