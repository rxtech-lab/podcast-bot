"use client";

import { useEffect, useRef, useState } from "react";
import type { FeedItem } from "@/lib/feed";

export type { FeedItem };

export interface EngineLiveState {
  /** Ordered transcript + system notes for the bubble chat view. */
  feed: FeedItem[];
  /** Explicit tool activity per agent name → "searching" | "memory" | "directing". */
  activity: Record<string, string>;
  /** Tool name (detail) per agent name, when known. */
  details: Record<string, string>;
  /** The single agent currently speaking, or null. Speaking is exclusive. */
  speaking: string | null;
  phaseLabel: string;
  ended: boolean;
}

const TOOL_ACTIVITIES = new Set(["searching", "memory", "directing"]);

function omit<T extends Record<string, unknown>>(obj: T, key: string): T {
  if (!(key in obj)) return obj;
  const next = { ...obj };
  delete next[key];
  return next;
}

// useEngineEvents subscribes to the dashboard's SSE proxy for one job and
// reduces the engine event stream into a chat feed + a coherent activity model.
//
// Activity has a single source of truth so the diagram never shows two agents
// "speaking" at once: `speaking` holds at most one agent. Everyone else with no
// explicit tool activity is derived as "thinking" by the consumer. Streaming
// transcript chunks are coalesced per turn (keyed by speaker) into one bubble.
export function useEngineEvents(
  jobId: string | null,
  initialFeed: FeedItem[] = [],
): EngineLiveState {
  const [state, setState] = useState<EngineLiveState>({
    feed: initialFeed,
    activity: {},
    details: {},
    speaking: null,
    phaseLabel: "",
    ended: false,
  });
  // In-flight turn being streamed; accumulated until its `done` chunk arrives.
  const inFlight = useRef<{ speaker: string; role: string; text: string } | null>(
    null,
  );

  useEffect(() => {
    if (!jobId) return;
    inFlight.current = null;
    const es = new EventSource(
      `/api/engine/events?channel=${encodeURIComponent(jobId)}`,
    );

    const pushSystem = (level: string, text: string) =>
      setState((s) => ({
        ...s,
        feed: [...s.feed.slice(-400), { type: "system", level, text }],
      }));

    es.addEventListener("status", (e) => {
      const t = JSON.parse((e as MessageEvent).data).text ?? "";
      if (t) pushSystem("status", t);
    });
    es.addEventListener("error", (e) => {
      const ev = e as MessageEvent;
      if (ev.data) pushSystem("error", JSON.parse(ev.data).text ?? "error");
    });
    es.addEventListener("phase", (e) => {
      const d = JSON.parse((e as MessageEvent).data);
      const label = d.label ?? d.phase ?? "";
      setState((s) => ({
        ...s,
        phaseLabel: label,
        feed: label
          ? [...s.feed.slice(-400), { type: "system", level: "phase", text: label }]
          : s.feed,
      }));
    });

    es.addEventListener("transcript", (e) => {
      const d = JSON.parse((e as MessageEvent).data);
      const speaker = d.speaker as string;
      if (!speaker) return;
      const role = (d.role as string) || "";

      // Start a new turn when the speaker changes; light up that speaker and
      // clear any stale tool activity now that they're talking.
      const cur = inFlight.current;
      if (!cur || cur.speaker !== speaker) {
        inFlight.current = { speaker, role, text: "" };
        setState((s) => ({
          ...s,
          speaking: speaker,
          activity: omit(s.activity, speaker),
          details: omit(s.details, speaker),
        }));
      }
      if (d.text) {
        const f = inFlight.current!;
        f.text += (f.text ? " " : "") + d.text;
      }
      if (d.done) {
        const f = inFlight.current;
        inFlight.current = null;
        const text = (d.text || f?.text || "").trim();
        if (!text) return;
        setState((s) => ({
          ...s,
          feed: [
            ...s.feed.slice(-400),
            { type: "message", speaker, role, text },
          ],
        }));
      }
    });

    es.addEventListener("agent_activity", (e) => {
      const d = JSON.parse((e as MessageEvent).data);
      const agent = d.agent as string;
      if (!agent) return;
      const act = (d.activity as string) || "idle";
      setState((s) => {
        if (act === "speaking") {
          return {
            ...s,
            speaking: agent,
            activity: omit(s.activity, agent),
            details: omit(s.details, agent),
          };
        }
        if (TOOL_ACTIVITIES.has(act)) {
          return {
            ...s,
            activity: { ...s.activity, [agent]: act },
            details: d.detail
              ? { ...s.details, [agent]: d.detail }
              : omit(s.details, agent),
          };
        }
        // idle (or unknown): drop tool activity; release the speaker slot if it
        // was this agent so others stop showing "thinking".
        return {
          ...s,
          speaking: s.speaking === agent ? null : s.speaking,
          activity: omit(s.activity, agent),
          details: omit(s.details, agent),
        };
      });
    });

    es.addEventListener("ended", () => {
      inFlight.current = null;
      setState((s) => ({
        ...s,
        ended: true,
        speaking: null,
        activity: {},
        details: {},
        feed: [...s.feed.slice(-400), { type: "system", level: "status", text: "stream ended" }],
      }));
    });

    return () => es.close();
  }, [jobId]);

  return state;
}
