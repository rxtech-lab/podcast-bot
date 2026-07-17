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

// Compact album descriptor attached to marketplace rows and the album page.
export type AlbumSummary = {
  id: string;
  title: string;
  kind: string;
  cover?: DiscussionCover;
  episode_count: number;
};

// A creator's public profile as served by /api/market/creators/{id}.
export type CreatorProfile = Creator & {
  follower_count: number;
};

// One row of the public marketplace listing — just what the grid renders.
export type MarketStation = {
  id: string;
  title: string;
  topic: string;
  duration_seconds?: number;
  like_count: number;
  cover?: DiscussionCover;
  creator?: Creator | null;
  album?: AlbumSummary | null;
};

function normalizeStation(d: Record<string, unknown>): MarketStation {
  return {
    id: String(d.id ?? ""),
    title: String(d.title || d.topic || "Podcast"),
    topic: String(d.topic ?? ""),
    duration_seconds: typeof d.duration_seconds === "number" ? d.duration_seconds : undefined,
    like_count: typeof d.like_count === "number" ? d.like_count : 0,
    cover: d.cover as DiscussionCover | undefined,
    creator: (d.creator as Creator | null | undefined) ?? null,
    album: (d.album as AlbumSummary | null | undefined) ?? null,
  };
}

// marketFetch performs a market API request, preferring the signed-in user's
// access token (so is_self / private-visibility rules apply to them) and
// falling back to the service token for anonymous or expired sessions.
async function marketFetch(path: string, accessToken?: string): Promise<Response | null> {
  if (!BASE) return null;
  if (accessToken) {
    const res = await fetch(`${BASE}${path}`, {
      headers: { Authorization: `Bearer ${accessToken}` },
      cache: "no-store",
    });
    if (res.ok) return res;
  }
  if (!TOKEN) return null;
  const res = await fetch(`${BASE}${path}`, { headers: authHeaders(), cache: "no-store" });
  return res.ok ? res : null;
}

async function fetchStationList(
  path: string,
  accessToken?: string
): Promise<MarketStation[] | null> {
  const res = await marketFetch(path, accessToken);
  if (!res) return null;
  const items = (await res.json()) as Record<string, unknown>[] | null;
  return (items ?? []).map(normalizeStation);
}

function listQuery(q: string, limit: number, offset: number): string {
  const search = new URLSearchParams();
  if (q) search.set("q", q);
  search.set("limit", String(limit));
  search.set("offset", String(offset));
  return search.toString();
}

// listPublicStations fetches a page of public podcasts from the market
// listing endpoint. Returns null when the backend is unreachable or
// misconfigured (as opposed to an empty page) so the marketplace can render
// an error state instead of "no podcasts yet".
export const listPublicStations = cache(
  async (q: string, limit: number, offset: number): Promise<MarketStation[] | null> =>
    fetchStationList(`/api/market/stations?${listQuery(q, limit, offset)}`)
);

// listCreatorStations fetches a page of one creator's public podcasts.
export const listCreatorStations = cache(
  async (
    id: string,
    limit: number,
    offset: number,
    accessToken?: string
  ): Promise<MarketStation[] | null> =>
    fetchStationList(
      `/api/market/creators/${encodeURIComponent(id)}/stations?${listQuery("", limit, offset)}`,
      accessToken
    )
);

// getCreator fetches a creator's public profile. Null when unknown.
export const getCreator = cache(
  async (id: string, accessToken?: string): Promise<CreatorProfile | null> => {
    const res = await marketFetch(`/api/market/creators/${encodeURIComponent(id)}`, accessToken);
    if (!res) return null;
    const d = (await res.json()) as Record<string, unknown>;
  return {
    id: String(d.id ?? ""),
    display_name: String(d.display_name ?? "Creator"),
    username: typeof d.username === "string" ? d.username : undefined,
    avatar_url: typeof d.avatar_url === "string" ? d.avatar_url : undefined,
    follower_count: typeof d.follower_count === "number" ? d.follower_count : 0,
  };
});

export type AlbumDetail = {
  album: AlbumSummary;
  episodes: MarketStation[];
};

// getAlbum fetches a public album and its episodes. Null when unknown or not
// visible.
export const getAlbum = cache(async (id: string): Promise<AlbumDetail | null> => {
  if (!BASE || !TOKEN) return null;
  const res = await fetch(`${BASE}/api/market/albums/${encodeURIComponent(id)}`, {
    headers: authHeaders(),
    cache: "no-store",
  });
  if (!res.ok) return null;
  const body = (await res.json()) as {
    album?: Record<string, unknown> | null;
    episodes?: Record<string, unknown>[] | null;
  };
  if (!body.album) return null;
  const a = body.album;
  return {
    album: {
      id: String(a.id ?? ""),
      title: String(a.title ?? "Album"),
      kind: String(a.kind ?? ""),
      cover: a.cover as DiscussionCover | undefined,
      episode_count:
        typeof a.episode_count === "number" ? a.episode_count : (body.episodes ?? []).length,
    },
    episodes: (body.episodes ?? []).map(normalizeStation),
  };
});
