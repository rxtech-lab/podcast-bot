"use client";

import Link from "next/link";
import { useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import type { ViewerDiscussion } from "@/app/lib/viewer";
import { creatorSlug } from "@/app/lib/creator";

type CaptionCue = {
  startMs: number;
  endMs: number;
  text: string;
};

function durationToMs(seconds: number | undefined) {
  return typeof seconds === "number" && Number.isFinite(seconds) && seconds > 0
    ? seconds * 1000
    : 0;
}

function mediaDurationToMs(durationSeconds: number) {
  return Number.isFinite(durationSeconds) && durationSeconds > 0
    ? Math.floor(durationSeconds * 1000)
    : 0;
}

function parseVTTTime(value: string) {
  const parts = value.trim().replace(",", ".").split(":");
  const seconds = Number(parts.pop() ?? "0");
  const minutes = Number(parts.pop() ?? "0");
  const hours = Number(parts.pop() ?? "0");
  if (![hours, minutes, seconds].every(Number.isFinite)) return 0;
  return Math.floor(((hours * 60 + minutes) * 60 + seconds) * 1000);
}

function parseVTT(vtt: string): CaptionCue[] {
  return vtt
    .replace(/\r/g, "")
    .split(/\n\n+/)
    .map((block) => {
      const lines = block
        .split("\n")
        .map((line) => line.trim())
        .filter(Boolean);
      const timeIndex = lines.findIndex((line) => line.includes("-->"));
      if (timeIndex < 0) return null;
      const [startRaw, endRaw] = lines[timeIndex].split("-->");
      const text = lines.slice(timeIndex + 1).join(" ").trim();
      if (!text) return null;
      return {
        startMs: parseVTTTime(startRaw),
        endMs: parseVTTTime((endRaw ?? "").trim().split(/\s+/)[0]),
        text,
      };
    })
    .filter((cue): cue is CaptionCue => !!cue && cue.endMs > cue.startMs);
}

export function PodcastViewer({
  discussion,
  headerAction,
}: {
  discussion: ViewerDiscussion;
  // Trailing header control: the account dropdown (with sign-out) when signed
  // in, or a sign-in button when viewing a public podcast anonymously.
  headerAction?: ReactNode;
}) {
  const audioRef = useRef<HTMLAudioElement>(null);
  const [currentMs, setCurrentMs] = useState(0);
  const [durationMs, setDurationMs] = useState(durationToMs(discussion.duration_seconds));
  const [isPlaying, setIsPlaying] = useState(false);
  const [captionCues, setCaptionCues] = useState<CaptionCue[]>([]);

  const lines = discussion.lines;
  const transcriptLines = lines.filter((line) => !line.is_user);
  // Synced caption cues fall back to the transcript when the VTT sidecar is
  // unavailable (older podcasts never uploaded one, or the fetch 404s). Each
  // non-user line with a timestamp shows from its start until the next line
  // begins — so captions keep working from data the page already has.
  const lineCues = useMemo<CaptionCue[]>(() => {
    const timed = lines.filter(
      (line) => !line.is_user && typeof line.start_ms === "number" && line.start_ms >= 0
    );
    return timed.map((line, i) => {
      const startMs = line.start_ms as number;
      const nextStart = timed[i + 1]?.start_ms;
      const endMs =
        typeof nextStart === "number" && nextStart > startMs ? nextStart : startMs + 6000;
      return { startMs, endMs, text: line.text };
    });
  }, [lines]);
  const hasAudio = !!discussion.download_url;
  const coverURL = discussion.cover?.image_url?.trim();
  const coverStart = discussion.cover?.gradient_start || "#14b8a6";
  const coverEnd = discussion.cover?.gradient_end || "#f59e0b";
  // Prefer the finer-grained VTT cues; otherwise the transcript-derived ones.
  const effectiveCues = captionCues.length > 0 ? captionCues : lineCues;
  const captionDurationMs = effectiveCues.reduce((max, cue) => Math.max(max, cue.endMs), 0);
  const duration = Math.max(
    Number.isFinite(durationMs) ? durationMs : 0,
    currentMs,
    durationToMs(discussion.duration_seconds),
    captionDurationMs
  );
  const rangeMax = Math.max(duration, 1);
  const progress = duration > 0 ? Math.min(100, Math.max(0, (currentMs / duration) * 100)) : 0;
  const speakerCount = new Set(transcriptLines.map((line) => line.speaker).filter(Boolean)).size;
  const caption = useMemo(() => {
    return (
      effectiveCues.find((cue) => currentMs >= cue.startMs && currentMs < cue.endMs)?.text ?? ""
    );
  }, [effectiveCues, currentMs]);

  useEffect(() => {
    let cancelled = false;
    if (!discussion.captions_url) return;
    void fetch(discussion.captions_url, { cache: "no-store" })
      .then((response) => (response.ok ? response.text() : ""))
      .then((text) => {
        if (!cancelled) setCaptionCues(parseVTT(text));
      })
      .catch(() => {
        if (!cancelled) setCaptionCues([]);
      });
    return () => {
      cancelled = true;
    };
  }, [discussion.captions_url]);

  function togglePlayback() {
    const audio = audioRef.current;
    if (!audio) return;
    if (audio.paused) void audio.play().catch(() => {});
    else audio.pause();
  }

  function seekByInput(value: string) {
    const audio = audioRef.current;
    const next = Number(value);
    if (!audio || Number.isNaN(next)) return;
    audio.currentTime = next / 1000;
    setCurrentMs(next);
  }

  function formatTime(ms: number) {
    if (!Number.isFinite(ms) || ms <= 0) return "0:00";
    const total = Math.floor(ms / 1000);
    const minutes = Math.floor(total / 60);
    const seconds = total % 60;
    return `${minutes}:${seconds.toString().padStart(2, "0")}`;
  }

  function speakerLabel(line: (typeof lines)[number]) {
    return line.speaker || (line.is_user ? "You" : "Speaker");
  }

  return (
    <main className="min-h-screen overflow-x-clip bg-[#060807] text-stone-50">
      <div
        className="pointer-events-none fixed inset-0 opacity-70"
        style={{
          background: `radial-gradient(circle at 18% 12%, ${coverStart}33, transparent 28rem), radial-gradient(circle at 82% 16%, ${coverEnd}2e, transparent 26rem), linear-gradient(135deg, #060807 0%, #10130f 46%, #090b10 100%)`,
        }}
      />

      <div className="relative mx-auto grid w-full max-w-7xl gap-8 px-5 pt-10 pb-6 sm:px-8 lg:grid-cols-[minmax(18rem,0.85fr)_minmax(0,1.25fr)] lg:px-10 lg:py-0">
        <section className="lg:sticky lg:top-0 lg:h-screen lg:self-start lg:overflow-y-auto">
          <div className="flex min-h-full flex-col gap-6 lg:pt-10">
            <div className="space-y-6">
              <div className="flex items-center gap-2">
                <Link
                  href="/"
                  className="text-xs font-semibold uppercase text-teal-200/75 transition hover:text-teal-200"
                >
                  podcast fm
                </Link>
                {headerAction}
              </div>

              <div>
                <div
                  className="relative aspect-square max-h-[30rem] overflow-hidden rounded-t-lg border border-white/10 shadow-2xl shadow-black/40"
                  style={{
                    background: coverURL
                      ? `linear-gradient(145deg, ${coverStart}66, ${coverEnd}44)`
                      : `linear-gradient(145deg, ${coverStart}, ${coverEnd})`,
                  }}
                >
                  {coverURL ? (
                    // eslint-disable-next-line @next/next/no-img-element
                    <img
                      src={coverURL}
                      alt=""
                      className="h-full w-full object-cover"
                      draggable={false}
                    />
                  ) : (
                    <div className="flex h-full flex-col justify-between p-7">
                      <div className="h-16 w-16 rounded-full bg-white/[0.24] blur-sm" />
                      <div className="space-y-3">
                        <div className="h-3 w-24 rounded-full bg-white/[0.35]" />
                        <div className="h-3 w-36 rounded-full bg-white/[0.25]" />
                      </div>
                    </div>
                  )}
                  <div className="absolute inset-0 bg-[linear-gradient(180deg,transparent_35%,rgba(0,0,0,0.7)_100%)]" />
                  <div className="absolute inset-x-0 bottom-0 p-6">
                    <p className="text-xs font-medium uppercase text-white/60">
                      Now playing
                    </p>
                    <h1 className="mt-2 max-w-[24rem] text-2xl font-semibold leading-tight text-white sm:text-3xl">
                      {discussion.title}
                    </h1>
                  </div>
                </div>

                <section
                  aria-label="Podcast player"
                  className="rounded-b-lg border border-t-0 border-white/[0.12] bg-[#10120f]/90 p-4 shadow-2xl shadow-black/30 backdrop-blur"
                >
                  {hasAudio ? (
                    <>
                      <audio
                        ref={audioRef}
                        src={discussion.download_url}
                        preload="metadata"
                        className="hidden"
                        onLoadedMetadata={(e) => {
                          const nextDuration = mediaDurationToMs(e.currentTarget.duration);
                          if (nextDuration > 0) setDurationMs(nextDuration);
                        }}
                        onDurationChange={(e) => {
                          const nextDuration = mediaDurationToMs(e.currentTarget.duration);
                          if (nextDuration > 0) setDurationMs(nextDuration);
                        }}
                        onTimeUpdate={(e) =>
                          setCurrentMs(Math.floor(e.currentTarget.currentTime * 1000))
                        }
                        onPlay={() => setIsPlaying(true)}
                        onPause={() => setIsPlaying(false)}
                        onEnded={() => setIsPlaying(false)}
                      >
                        {discussion.captions_url ? (
                          <track
                            src={discussion.captions_url}
                            kind="captions"
                            srcLang="en"
                            label="Captions"
                            default
                          />
                        ) : null}
                      </audio>
                      <div className="flex items-center gap-4">
                        <button
                          type="button"
                          onClick={togglePlayback}
                          className="grid h-14 w-14 shrink-0 place-items-center rounded-full bg-teal-300 text-lg font-bold text-black shadow-lg shadow-teal-950/50 transition hover:bg-teal-200"
                          aria-label={isPlaying ? "Pause podcast" : "Play podcast"}
                        >
                          {isPlaying ? (
                            <span aria-hidden="true">II</span>
                          ) : (
                            <span
                              aria-hidden="true"
                              className="ml-1 h-0 w-0 border-y-[0.45rem] border-l-[0.72rem] border-y-transparent border-l-black"
                            />
                          )}
                        </button>
                        <div className="min-w-0 flex-1">
                          <div className="flex items-center justify-between gap-3 text-xs tabular-nums text-stone-400">
                            <span>{formatTime(currentMs)}</span>
                            <span>{formatTime(duration)}</span>
                          </div>
                          <input
                            type="range"
                            min={0}
                            max={rangeMax}
                            value={Math.min(currentMs, rangeMax)}
                            step={500}
                            onChange={(e) => seekByInput(e.currentTarget.value)}
                            aria-label="Podcast playback position"
                            className="mt-2 h-2 w-full cursor-pointer appearance-none rounded-full bg-white/[0.12] accent-teal-300"
                            style={{
                              background: `linear-gradient(90deg, #5eead4 ${progress}%, rgba(255,255,255,0.14) ${progress}%)`,
                            }}
                          />
                        </div>
                      </div>
                      <div
                        aria-live="polite"
                        className="mt-4 min-h-20 rounded-lg border border-white/10 bg-black/25 px-4 py-3"
                      >
                        <p className="text-xs font-semibold uppercase text-amber-200/80">
                          Caption
                        </p>
                        <p className="mt-2 min-h-[4.875rem] text-base leading-relaxed text-stone-100">
                          {caption || "Captions will appear here as the podcast plays."}
                        </p>
                      </div>
                    </>
                  ) : (
                    <p className="rounded-lg bg-white/[0.06] px-4 py-4 text-center text-sm text-stone-300">
                      Audio for this podcast isn&apos;t available.
                    </p>
                  )}
                </section>
              </div>

              {discussion.creator?.display_name ? (
                <Link
                  href={`/c/${encodeURIComponent(creatorSlug(discussion.creator.id))}`}
                  className="flex items-center gap-3 rounded-lg border border-white/10 bg-white/[0.06] px-4 py-3 transition hover:border-teal-300/40 hover:bg-white/[0.09]"
                >
                  {discussion.creator.avatar_url ? (
                    // eslint-disable-next-line @next/next/no-img-element
                    <img
                      src={discussion.creator.avatar_url}
                      alt=""
                      className="h-9 w-9 shrink-0 rounded-full object-cover"
                      draggable={false}
                    />
                  ) : (
                    <div className="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-white/[0.12] text-sm font-semibold uppercase text-stone-200">
                      {discussion.creator.display_name.trim().charAt(0).toUpperCase() || "?"}
                    </div>
                  )}
                  <div className="min-w-0">
                    <div className="text-[0.68rem] uppercase text-stone-400">Creator</div>
                    <div className="truncate text-sm font-medium text-stone-100">
                      {discussion.creator.display_name}
                    </div>
                  </div>
                  <span aria-hidden="true" className="ml-auto text-stone-500">
                    ›
                  </span>
                </Link>
              ) : null}

              <div className="grid grid-cols-3 gap-2 text-center">
                <div className="rounded-lg border border-white/10 bg-white/[0.06] px-3 py-3">
                  <div className="text-lg font-semibold text-white">{formatTime(duration)}</div>
                  <div className="mt-1 text-[0.68rem] uppercase text-stone-400">
                    Duration
                  </div>
                </div>
                <div className="rounded-lg border border-white/10 bg-white/[0.06] px-3 py-3">
                  <div className="text-lg font-semibold text-white">{transcriptLines.length}</div>
                  <div className="mt-1 text-[0.68rem] uppercase text-stone-400">
                    Lines
                  </div>
                </div>
                <div className="rounded-lg border border-white/10 bg-white/[0.06] px-3 py-3">
                  <div className="text-lg font-semibold text-white">{speakerCount || "-"}</div>
                  <div className="mt-1 text-[0.68rem] uppercase text-stone-400">
                    Voices
                  </div>
                </div>
              </div>
            </div>
          </div>
        </section>

        <section className="min-w-0 lg:py-10">
          <section className="py-2">
            <div className="mb-5 flex items-end justify-between gap-4">
              <div>
                <p className="text-xs font-semibold uppercase text-stone-500">
                  Transcript
                </p>
                <h2 className="mt-1 text-xl font-semibold text-stone-50">
                  Follow the conversation
                </h2>
              </div>
            </div>

            <ol className="space-y-3">
              {transcriptLines.map((line, i) => {
                return (
                  <li
                    key={`${i}-${line.start_ms ?? "line"}`}
                    className={`rounded-lg border border-white/[0.08] bg-white/[0.035] px-4 py-4 ${
                      line.is_user ? "border-l-4 border-l-amber-300/60" : ""
                    }`}
                  >
                    <span className="flex flex-wrap items-center gap-3">
                      <span className="text-xs font-semibold uppercase text-stone-400">
                        {speakerLabel(line)}
                      </span>
                      {typeof line.start_ms === "number" && line.start_ms > 0 ? (
                        <span className="text-xs tabular-nums text-stone-500">
                          {formatTime(line.start_ms)}
                        </span>
                      ) : null}
                    </span>
                    <span className="mt-2 block text-[1rem] leading-relaxed text-stone-300">
                      {line.text}
                    </span>
                  </li>
                );
              })}
            </ol>
          </section>
        </section>
      </div>
    </main>
  );
}
