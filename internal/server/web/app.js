// debate-bot live viewer.
//
// Video + audio come from the HLS stream at /api/video/stream.m3u8 (with the
// transcript text baked in by ffmpeg drawtext). The chat panel is fed by SSE
// /api/events; history is loaded once via /api/transcript so late joiners see
// what they missed. Audio falls back to the raw /api/audio/stream mp3 if the
// HLS encoder isn't available.

const $ = (sel) => document.querySelector(sel);

const phaseEl = $("#phase");
const timerEl = $("#timer");
const statusEl = $("#status");
const chatLog = $("#chat-log");
const chatForm = $("#chat-form");
const chatInput = $("#chat-input");
const player = $("#player");
const playerFallback = $("#player-fallback");
const audioFallback = $("#audio-fallback");

// Accumulated text for the in-flight turn — committed to chat on Done.
let inFlight = null; // { speaker, role, side, text }

function fmtMs(ms) {
  if (!Number.isFinite(ms) || ms < 0) ms = 0;
  const total = Math.floor(ms / 1000);
  const s = total % 60;
  const m = Math.floor(total / 60) % 60;
  const h = Math.floor(total / 3600);
  const pad = (n) => n.toString().padStart(2, "0");
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`;
}

function speakerLabel({ speaker, role, side }) {
  if (role === "host") return "host";
  if (role === "affirmative") return `affirmative — ${speaker}`;
  if (role === "negative") return `negative — ${speaker}`;
  if (role === "judge") return "judge";
  if (role === "viewer") return `viewer — ${speaker}`;
  if (role === "user") return "audience";
  return speaker || "?";
}

function appendChat(line) {
  const li = document.createElement("li");
  const tag = document.createElement("span");
  tag.className = `speaker role-${line.role || "user"}`;
  tag.textContent = speakerLabel(line) + ":";
  li.appendChild(tag);
  li.appendChild(document.createTextNode(" " + line.text));
  chatLog.appendChild(li);
  chatLog.scrollTop = chatLog.scrollHeight;
}

async function loadHistory() {
  try {
    const resp = await fetch("/api/transcript");
    if (!resp.ok) return;
    const lines = await resp.json();
    for (const l of lines) appendChat(l);
  } catch (e) {
    console.warn("history load failed", e);
  }
}

async function startVideo() {
  const url = "/api/video/stream.m3u8";
  // The HLS playlist only appears once ffmpeg has finalised its first segment,
  // which doesn't happen until the orchestrator pushes some audio. So poll for
  // up to ~60s before showing the audio-only fallback.
  const deadline = Date.now() + 60_000;
  playerFallback.hidden = false;
  playerFallback.textContent = "warming up video stream…";
  while (Date.now() < deadline) {
    try {
      const resp = await fetch(url, { method: "HEAD", cache: "no-store" });
      if (resp.ok) {
        playerFallback.hidden = true;
        attachHLS(url);
        return;
      }
      if (resp.status !== 404) {
        // 5xx etc — the encoder is broken, no point waiting.
        showFallback();
        return;
      }
    } catch (e) {
      // network error; keep retrying
    }
    await new Promise((r) => setTimeout(r, 1500));
  }
  showFallback();
}

function attachHLS(url) {
  if (player.canPlayType("application/vnd.apple.mpegurl")) {
    // Safari, iOS — native HLS.
    player.src = url;
    player.play().catch(() => {});
  } else if (window.Hls && Hls.isSupported()) {
    const hls = new Hls({ liveSyncDurationCount: 3 });
    hls.loadSource(url);
    hls.attachMedia(player);
    hls.on(Hls.Events.MANIFEST_PARSED, () => {
      player.play().catch(() => {});
    });
    hls.on(Hls.Events.ERROR, (_, data) => {
      if (data.fatal) {
        console.warn("hls fatal", data);
        showFallback();
      }
    });
  } else {
    showFallback();
  }
}

function showFallback() {
  player.hidden = true;
  playerFallback.hidden = false;
  playerFallback.textContent =
    "video unavailable — check server stderr for 'video disabled' (likely missing system font; set DEBATE_BOT_FONT). audio still works below.";
  audioFallback.removeAttribute("hidden");
  audioFallback.style.display = "block";
  audioFallback.controls = true;
  audioFallback.autoplay = true;
}

function connectEvents() {
  const es = new EventSource("/api/events");
  es.addEventListener("transcript", (ev) => {
    const m = JSON.parse(ev.data);
    if (m.role === "user" && m.done) {
      appendChat(m);
      return;
    }
    // Buffer per-sentence text until Done so chat captures the full turn.
    if (m.text) {
      const id = `${m.role}|${m.speaker}|${m.side || ""}`;
      const curId = inFlight
        ? `${inFlight.role}|${inFlight.speaker}|${inFlight.side || ""}`
        : null;
      if (id !== curId) {
        inFlight = { speaker: m.speaker, role: m.role, side: m.side, text: "" };
      }
      const sep = inFlight.text.length > 0 ? " " : "";
      inFlight.text += sep + m.text;
    }
    if (m.done && inFlight) {
      const text = (m.text || inFlight.text || "").trim();
      if (text) {
        appendChat({
          speaker: inFlight.speaker,
          role: inFlight.role,
          side: inFlight.side,
          text,
        });
      }
      inFlight = null;
    }
  });
  es.addEventListener("tick", (ev) => {
    const m = JSON.parse(ev.data);
    timerEl.textContent = `${fmtMs(m.elapsed_ms)} / ${fmtMs(m.elapsed_ms + m.remaining_ms)}`;
  });
  es.addEventListener("phase", (ev) => {
    const m = JSON.parse(ev.data);
    phaseEl.textContent = `phase: ${m.phase}`;
  });
  es.addEventListener("status", (ev) => {
    const m = JSON.parse(ev.data);
    statusEl.textContent = m.text || "";
  });
  es.addEventListener("error", (ev) => {
    const m = JSON.parse(ev.data);
    statusEl.textContent = "error: " + m.text;
  });
  es.addEventListener("ended", () => {
    statusEl.textContent = "ended";
  });
  es.onerror = () => {
    statusEl.textContent = "reconnecting…";
  };
}

chatForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  const text = chatInput.value.trim();
  if (!text) return;
  chatInput.value = "";
  try {
    await fetch("/api/messages", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text }),
    });
  } catch (err) {
    console.warn("send failed", err);
  }
});

loadHistory().then(() => {
  startVideo();
  connectEvents();
});
