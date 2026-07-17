const port = Number(process.env.E2E_BACKEND_PORT ?? 4555);
const serviceToken = "Bearer e2e-service-token";
const privateUserToken = "Bearer e2e-access-token:user-private";

function json(body, init = {}) {
  return new Response(JSON.stringify(body), {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...(init.headers ?? {}),
    },
  });
}

function unauthorized() {
  return new Response("unauthorized", { status: 401 });
}

function notFound() {
  return new Response("not found", { status: 404 });
}

function discussion(overrides) {
  return {
    id: overrides.id,
    title: overrides.title,
    topic: overrides.topic,
    status: "ready",
    visibility: overrides.visibility,
    duration_seconds: 7,
    like_count: overrides.like_count ?? 0,
    download_url: `http://127.0.0.1:${port}/audio/${overrides.id}.mp3`,
    cover: {
      type: "gradient",
      gradient_start: "#14b8a6",
      gradient_end: "#f59e0b",
    },
    creator: {
      id: "oauth:creator-1",
      display_name: "PanelFM Creator",
    },
    lines: [
      {
        speaker: "Host",
        role: "host",
        text: overrides.line,
        start_ms: 0,
        is_user: false,
      },
    ],
  };
}

const publicPodcast = discussion({
  id: "public-podcast",
  title: "Public Podcast",
  topic: "A public mock podcast",
  visibility: "public",
  line: "This is the public podcast transcript.",
});

const privatePodcast = discussion({
  id: "private-podcast",
  title: "Private Podcast",
  topic: "A private mock podcast",
  visibility: "private",
  line: "This is the private podcast transcript.",
});

const mockAlbum = {
  id: "album-1",
  title: "Mock Album",
  kind: "series",
  cover: {
    type: "gradient",
    gradient_start: "#14b8a6",
    gradient_end: "#f59e0b",
  },
  episode_count: 1,
};

const mockCreator = {
  id: "oauth:creator-1",
  display_name: "PanelFM Creator",
  username: "panelfm",
  follower_count: 2,
};

// The public marketplace listing. Only public podcasts appear here, matching
// the Go backend's ListPublic behavior.
const marketStations = [
  { ...publicPodcast, like_count: 3 },
  {
    ...discussion({
      id: "second-podcast",
      title: "Second Podcast",
      topic: "Another public mock podcast",
      visibility: "public",
      line: "This is the second podcast transcript.",
      like_count: 1,
    }),
    album: mockAlbum,
  },
];

Bun.serve({
  port,
  hostname: "127.0.0.1",
  fetch(request) {
    const url = new URL(request.url);
    // The web app percent-encodes path segments (oauth%3Acreator-1), so
    // compare against the decoded path like Go's ServeMux does.
    const path = decodeURIComponent(url.pathname);
    const auth = request.headers.get("authorization") ?? "";

    if (url.pathname === "/healthz") {
      return new Response("ok");
    }

    // Podcast audio is a public signed URL in production, so no auth here.
    if (url.pathname.startsWith("/audio/")) {
      return new Response(new Uint8Array(64), {
        headers: { "Content-Type": "audio/mpeg" },
      });
    }

    if (url.pathname === "/api/market/stations") {
      if (auth !== serviceToken) return unauthorized();
      const q = (url.searchParams.get("q") ?? "").trim().toLowerCase();
      const limit = Number(url.searchParams.get("limit")) || 20;
      const offset = Number(url.searchParams.get("offset")) || 0;
      const matches = marketStations.filter(
        (d) =>
          !q ||
          d.title.toLowerCase().includes(q) ||
          d.topic.toLowerCase().includes(q)
      );
      return json(matches.slice(offset, offset + limit));
    }

    if (url.pathname === "/api/market/stations/public-podcast") {
      if (auth !== serviceToken) return unauthorized();
      return json(publicPodcast);
    }

    if (url.pathname === "/api/market/stations/second-podcast") {
      if (auth !== serviceToken) return unauthorized();
      return json(marketStations[1]);
    }

    if (path === "/api/market/creators/oauth:creator-1") {
      if (auth !== serviceToken) return unauthorized();
      return json(mockCreator);
    }

    if (path === "/api/market/creators/oauth:creator-1/stations") {
      if (auth !== serviceToken) return unauthorized();
      return json(marketStations);
    }

    if (url.pathname === "/api/market/albums/album-1") {
      if (auth !== serviceToken) return unauthorized();
      return json({ album: mockAlbum, episodes: [marketStations[1]] });
    }

    if (url.pathname === "/api/market/stations/private-podcast") {
      if (auth !== serviceToken) return unauthorized();
      return notFound();
    }

    if (url.pathname === "/api/discussions/private-podcast") {
      if (auth !== privateUserToken) return unauthorized();
      return json(privatePodcast);
    }

    if (url.pathname === "/api/discussions/public-podcast") {
      if (auth !== privateUserToken) return unauthorized();
      return notFound();
    }

    return notFound();
  },
});

console.log(`Mock backend listening on http://127.0.0.1:${port}`);
