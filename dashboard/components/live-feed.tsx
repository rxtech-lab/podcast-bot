"use client";

import { useEffect, useRef } from "react";
import { cn } from "@/lib/utils";
import type { FeedItem } from "@/lib/use-engine-events";

// Per-role bubble + avatar styling, mirroring the broadcast frontend so the
// dashboard's live view reads like the existing chat.
interface RoleStyle {
  text: string;
  bubble: string;
  ring: string;
  icon: string;
}

const ROLE_STYLES: Record<string, RoleStyle> = {
  host: {
    text: "text-sky-300",
    bubble: "bg-sky-500/[0.07] border-sky-500/20",
    ring: "bg-sky-500/15 ring-sky-500/30",
    icon: "🎙",
  },
  discussant: {
    text: "text-emerald-300",
    bubble: "bg-emerald-500/[0.07] border-emerald-500/20",
    ring: "bg-emerald-500/15 ring-emerald-500/30",
    icon: "💬",
  },
  commander: {
    text: "text-violet-300",
    bubble: "bg-violet-500/[0.07] border-violet-500/20",
    ring: "bg-violet-500/15 ring-violet-500/30",
    icon: "🎬",
  },
  viewer: {
    text: "text-amber-300",
    bubble: "bg-amber-500/[0.07] border-amber-500/20",
    ring: "bg-amber-500/15 ring-amber-500/30",
    icon: "👁",
  },
  user: {
    text: "text-primary",
    bubble: "bg-primary/[0.10] border-primary/30",
    ring: "bg-primary/20 ring-primary/40",
    icon: "🙋",
  },
};

function roleStyle(role?: string): RoleStyle {
  return ROLE_STYLES[role ?? ""] ?? ROLE_STYLES.discussant;
}

function Bubble({ item }: { item: FeedItem }) {
  const cfg = roleStyle(item.role);
  const isMine = item.role === "user";
  const initial = (item.speaker || "?").charAt(0).toUpperCase();
  return (
    <li className={cn("flex gap-2.5", isMine && "flex-row-reverse")}>
      <div
        className={cn(
          "flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-full text-xs font-bold ring-1",
          cfg.ring,
          cfg.text,
        )}
      >
        {ROLE_STYLES[item.role ?? ""] ? cfg.icon : initial}
      </div>
      <div
        className={cn(
          "flex min-w-0 max-w-[85%] flex-col gap-1",
          isMine ? "items-end" : "items-start",
        )}
      >
        <div className="flex items-baseline gap-1.5 px-1 text-[11px] leading-none">
          <span className={cn("font-semibold", cfg.text)}>
            {isMine ? "you" : item.speaker || "?"}
          </span>
          {item.role && item.role !== "user" ? (
            <span className="text-muted-foreground/80">· {item.role}</span>
          ) : null}
        </div>
        <div
          className={cn(
            "whitespace-pre-wrap break-words rounded-2xl border px-3 py-2 text-sm leading-snug",
            cfg.bubble,
          )}
        >
          {item.text}
        </div>
      </div>
    </li>
  );
}

function SystemNote({ item }: { item: FeedItem }) {
  return (
    <li className="flex justify-center">
      <span
        className={cn(
          "rounded-full px-2.5 py-1 text-[11px]",
          item.level === "error"
            ? "bg-destructive/10 text-destructive"
            : item.level === "phase"
              ? "bg-blue-500/10 text-blue-400"
              : "bg-muted/60 text-muted-foreground",
        )}
      >
        {item.text}
      </span>
    </li>
  );
}

export function LiveFeed({ feed }: { feed: FeedItem[] }) {
  const scrollRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [feed.length]);

  return (
    <div ref={scrollRef} className="flex-1 overflow-y-auto rounded-md border border-border bg-background/40 px-3 py-4">
      {feed.length === 0 ? (
        <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
          Waiting for the panel to start…
        </div>
      ) : (
        <ol className="m-0 flex list-none flex-col gap-3 p-0">
          {feed.map((item, i) =>
            item.type === "message" ? (
              <Bubble key={i} item={item} />
            ) : (
              <SystemNote key={i} item={item} />
            ),
          )}
        </ol>
      )}
    </div>
  );
}
