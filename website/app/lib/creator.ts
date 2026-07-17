// Creator ids are backend-prefixed ("oauth:<subject>", "cookie:<name>", …).
// Web URLs use the bare id for the common oauth case so links read
// /c/<uuid> instead of /c/oauth:<uuid>. Ids with any other prefix keep it in
// the slug (the colon survives the round-trip), so nothing is ambiguous.

const OAUTH_PREFIX = "oauth:";

export function creatorSlug(id: string): string {
  return id.startsWith(OAUTH_PREFIX) ? id.slice(OAUTH_PREFIX.length) : id;
}

export function creatorIdFromSlug(slug: string): string {
  return slug.includes(":") ? slug : OAUTH_PREFIX + slug;
}

// Route params may arrive percent-encoded (e.g. %3A for old oauth: links).
export function decodeRouteParam(raw: string): string {
  try {
    return decodeURIComponent(raw);
  } catch {
    return raw;
  }
}
