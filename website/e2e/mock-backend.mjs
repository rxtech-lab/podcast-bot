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
    cover: {
      type: "gradient",
      gradient_start: "#14b8a6",
      gradient_end: "#f59e0b",
    },
    creator: {
      id: "creator-1",
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

Bun.serve({
  port,
  hostname: "127.0.0.1",
  fetch(request) {
    const url = new URL(request.url);
    const auth = request.headers.get("authorization") ?? "";

    if (url.pathname === "/healthz") {
      return new Response("ok");
    }

    if (url.pathname === "/api/market/stations/public-podcast") {
      if (auth !== serviceToken) return unauthorized();
      return json(publicPodcast);
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
