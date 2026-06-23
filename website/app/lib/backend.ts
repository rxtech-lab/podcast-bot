import { cache } from "react";

// Server-only backend access. BACKEND_SERVICE_TOKEN is the Go backend's
// DASHBOARD_SERVICE_TOKEN; it must NEVER be exposed to the browser, so this
// module is imported only from server components / route handlers and the env
// vars are intentionally NOT prefixed with NEXT_PUBLIC_.
const BASE = (process.env.BACKEND_BASE_URL ?? "").replace(/\/$/, "");
const TOKEN = process.env.BACKEND_SERVICE_TOKEN ?? "";

export type DiscussionCover = {
  type?: string;
  image_url?: string;
  image_key?: string;
  gradient_start?: string;
  gradient_end?: string;
  prompt?: string;
};

export type Creator = {
  id: string;
  display_name: string;
  username?: string;
  avatar_url?: string;
};

// Metadata the deep-link pages render. Both the share-resolve endpoint and the
// public market endpoint are normalized into this shape.
export type DiscussionMeta = {
  id: string;
  title: string;
  topic: string;
  cover?: DiscussionCover;
  creator?: Creator | null;
};

function authHeaders(): HeadersInit {
  return { Authorization: `Bearer ${TOKEN}` };
}

// getShare resolves a private share token to its discussion metadata. Returns
// null when the link is expired/revoked/unknown (backend replies 410/404) so
// the page can render its error state.
export const getShare = cache(async (token: string): Promise<DiscussionMeta | null> => {
  if (!BASE || !TOKEN) return null;
  const res = await fetch(`${BASE}/api/share/${encodeURIComponent(token)}`, {
    headers: authHeaders(),
    cache: "no-store",
  });
  if (!res.ok) return null;
  const d = await res.json();
  return {
    id: d.id,
    title: d.title ?? d.topic ?? "Discussion",
    topic: d.topic ?? "",
    cover: d.cover,
    creator: d.creator ?? null,
  };
});

// getPublicDiscussion fetches a public discussion by id from the market
// endpoint. Returns null when not found / not public.
export const getPublicDiscussion = cache(async (id: string): Promise<DiscussionMeta | null> => {
  if (!BASE || !TOKEN) return null;
  const res = await fetch(`${BASE}/api/market/stations/${encodeURIComponent(id)}`, {
    headers: authHeaders(),
    cache: "no-store",
  });
  if (!res.ok) return null;
  const d = await res.json();
  return {
    id: d.id,
    title: d.title || d.topic || "Discussion",
    topic: d.topic ?? "",
    cover: d.cover,
    creator: d.creator ?? null,
  };
});

export function coverImageURL(meta: DiscussionMeta | null): string | null {
  const url = meta?.cover?.image_url;
  return url && url.trim() !== "" ? url : null;
}
