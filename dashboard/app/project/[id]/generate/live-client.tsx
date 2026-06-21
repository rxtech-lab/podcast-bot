"use client";

import { useEffect, useMemo, useRef, useState, useTransition } from "react";
import { useDebouncedCallback } from "use-debounce";
import Hls from "hls.js";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ScriptDiagram } from "@/components/script-diagram";
import { LiveFeed } from "@/components/live-feed";
import { useEngineEvents } from "@/lib/use-engine-events";
import { participate, pollJob } from "@/lib/actions/generation";
import { saveJobLog } from "@/lib/actions/job-log";
import type { DiscussionScript } from "@/lib/schema/script-types";
import type { FeedItem } from "@/lib/feed";

/** Live activity per node path: visual state + optional tool detail. */
export type NodeActivity = { state: string; detail?: string };

function LivePreview({ jobId, done }: { jobId: string; done: boolean }) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const hlsUrl = `/api/engine/jobs/${jobId}/hls/stream.m3u8`;
  const finalUrl = `/api/engine/jobs/${jobId}/video`;
  const [ready, setReady] = useState(false);

  // Poll the playlist until the encoder produces the first segment.
  useEffect(() => {
    if (done) return;
    let cancelled = false;
    (async () => {
      while (!cancelled) {
        try {
          const r = await fetch(hlsUrl, { method: "HEAD", cache: "no-store" });
          if (r.ok) {
            if (!cancelled) setReady(true);
            return;
          }
        } catch {
          /* keep polling */
        }
        await new Promise((res) => setTimeout(res, 1500));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [hlsUrl, done]);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    if (done) {
      video.src = finalUrl;
      return;
    }
    if (!ready) return;
    if (Hls.isSupported()) {
      const hls = new Hls({ liveSyncDurationCount: 3, enableWorker: true });
      hls.loadSource(hlsUrl);
      hls.attachMedia(video);
      return () => hls.destroy();
    }
    video.src = hlsUrl;
  }, [ready, done, hlsUrl, finalUrl]);

  return (
    <video
      ref={videoRef}
      controls
      autoPlay
      muted={!done}
      className="aspect-video w-full rounded-lg border border-border bg-black"
    />
  );
}

export function LiveClient({
  projectId,
  jobId,
  script,
  models,
  initialDone,
  initialVideoUrl,
  initialFeed,
}: {
  projectId: string;
  jobId: string;
  script: DiscussionScript;
  models: { id: string; label: string }[];
  initialDone: boolean;
  initialVideoUrl: string | null;
  initialFeed: FeedItem[];
}) {
  const live = useEngineEvents(initialDone ? null : jobId, initialFeed);
  const [done, setDone] = useState(initialDone);
  const [videoUrl, setVideoUrl] = useState<string | null>(initialVideoUrl);
  const [message, setMessage] = useState("");
  const [, startMsg] = useTransition();

  // Map engine activity (keyed by agent name) to diagram node paths.
  const nameToPath = useMemo(() => {
    const m: Record<string, string> = {};
    if (script.host?.name) m[script.host.name] = "host";
    if (script.commander?.name) m[script.commander.name] = "commander";
    script.discussants?.forEach((d, i) => {
      if (d.name) m[d.name] = `discussant:${i}`;
    });
    script.viewers?.forEach((v, i) => {
      if (v.name) m[v.name] = `viewer:${i}`;
    });
    return m;
  }, [script]);

  // Derive each node's display state from a single source of truth: explicit
  // tool activity wins; the lone speaker shows "speaking"; while someone speaks
  // the rest show "thinking"; otherwise "idle". The commander is silent, so it
  // only ever shows its own activity (directing) or idle.
  const activityByPath = useMemo(() => {
    const out: Record<string, NodeActivity> = {};
    const speaking = live.speaking;
    for (const [name, path] of Object.entries(nameToPath)) {
      const tool = live.activity[name];
      if (tool && tool !== "idle") {
        out[path] = { state: tool, detail: live.details[name] };
      } else if (path === "commander") {
        out[path] = { state: "idle" };
      } else if (speaking && name === speaking) {
        out[path] = { state: "speaking" };
      } else if (speaking) {
        out[path] = { state: "thinking" };
      } else {
        out[path] = { state: "idle" };
      }
    }
    return out;
  }, [live.activity, live.details, live.speaking, nameToPath]);

  // Persist the live feed (debounced) so it survives reloads and is replayable
  // after the job finishes — the engine SSE stream has no backlog. Writes a few
  // times per generation rather than per event.
  const persistFeed = useDebouncedCallback(
    (feed: FeedItem[]) => {
      if (feed.length === 0) return;
      void saveJobLog(projectId, jobId, feed);
    },
    2000,
    { maxWait: 10000 },
  );
  useEffect(() => {
    if (initialDone) return;
    persistFeed(live.feed);
  }, [live.feed, initialDone, persistFeed]);
  // Flush the pending write when the tab is hidden or the view unmounts so the
  // tail of the transcript isn't lost inside the debounce window.
  useEffect(() => {
    const flush = () => persistFeed.flush();
    document.addEventListener("visibilitychange", flush);
    return () => {
      document.removeEventListener("visibilitychange", flush);
      persistFeed.flush();
    };
  }, [persistFeed]);

  // Poll for completion; persist the final download link.
  useEffect(() => {
    if (done) return;
    const t = setInterval(async () => {
      try {
        const job = await pollJob(projectId, jobId);
        if (job.status === "done") {
          setDone(true);
          // Stable proxy route (engine mints a fresh presigned redirect there);
          // never the expiring presigned download_url.
          setVideoUrl(`/api/engine/jobs/${jobId}/video`);
          clearInterval(t);
        } else if (job.status === "error") {
          clearInterval(t);
        }
      } catch {
        /* keep polling */
      }
    }, 2500);
    return () => clearInterval(t);
  }, [done, projectId, jobId]);

  const onSend = () =>
    startMsg(async () => {
      const text = message.trim();
      if (!text) return;
      setMessage("");
      try {
        await participate(jobId, text);
      } catch {
        setMessage(text);
      }
    });

  return (
    <div className="flex h-screen flex-col">
      <header className="flex items-center justify-between border-b border-border px-6 py-3">
        <div className="flex items-center gap-3">
          <Link href="/projects" className="text-sm text-muted-foreground">
            ← Projects
          </Link>
          <span className="font-semibold">{script.title}</span>
          {live.phaseLabel ? (
            <span className="text-xs text-muted-foreground">
              {live.phaseLabel}
            </span>
          ) : null}
        </div>
        {done && videoUrl ? (
          <a href={videoUrl} download>
            <Button>Download video</Button>
          </a>
        ) : (
          <span className="text-sm text-muted-foreground">
            {done ? "Finished" : "Generating…"}
          </span>
        )}
      </header>

      <div className="grid flex-1 grid-cols-1 overflow-hidden lg:grid-cols-2">
        <div className="flex flex-col gap-3 overflow-hidden border-r border-border p-4">
          <LivePreview jobId={jobId} done={done} />
          <LiveFeed feed={live.feed} />
          {!done ? (
            <div className="flex gap-2">
              <Input
                placeholder="Join the discussion…"
                value={message}
                onChange={(e) => setMessage(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") onSend();
                }}
              />
              <Button variant="outline" onClick={onSend}>
                Send
              </Button>
            </div>
          ) : null}
        </div>

        <div className="h-[50vh] lg:h-auto">
          <ScriptDiagram
            script={script}
            models={models}
            readOnly
            activity={activityByPath}
          />
        </div>
      </div>
    </div>
  );
}
